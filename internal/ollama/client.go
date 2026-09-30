package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"syscall"
	"time"

	"local-email-workspace/internal/observability"
)

const (
	requestTimeout     = 2 * time.Second
	generationTimeout  = 90 * time.Second
	maxResponseSize    = 2 << 20
	maxGenerationBytes = 1 << 20
)

type Client struct {
	baseURL        string
	httpClient     *http.Client
	generationSlot chan struct{}
}

const (
	ReasonModelNotConfigured     observability.FailureReason = "model_not_configured"
	ReasonQueueCancelled         observability.FailureReason = "queue_cancelled"
	ReasonQueueTimeout           observability.FailureReason = "queue_timeout"
	ReasonRequestEncodingFailed  observability.FailureReason = "request_encoding_failed"
	ReasonRequestCreationFailed  observability.FailureReason = "request_creation_failed"
	ReasonRequestTimeout         observability.FailureReason = "request_timeout"
	ReasonRequestCancelled       observability.FailureReason = "request_cancelled"
	ReasonConnectionRefused      observability.FailureReason = "connection_refused"
	ReasonRequestFailed          observability.FailureReason = "request_failed"
	ReasonUpstreamHTTPError      observability.FailureReason = "upstream_http_error"
	ReasonResponseDecodingFailed observability.FailureReason = "response_decoding_failed"
	ReasonIncompleteResponse     observability.FailureReason = "incomplete_response"
)

type clientFailure struct {
	reason observability.FailureReason
	err    error
}

func (e *clientFailure) Error() string {
	return e.err.Error()
}

func (e *clientFailure) Unwrap() error {
	return e.err
}

func (e *clientFailure) FailureReason() observability.FailureReason {
	return e.reason
}

func newClientError(reason observability.FailureReason, err error) error {
	return &clientFailure{reason: reason, err: err}
}

func requestFailureReason(err error) observability.FailureReason {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonRequestTimeout
	case errors.Is(err, context.Canceled):
		return ReasonRequestCancelled
	case errors.Is(err, syscall.ECONNREFUSED):
		return ReasonConnectionRefused
	default:
		return ReasonRequestFailed
	}
}

type Model struct {
	Name       string       `json:"name"`
	Model      string       `json:"model"`
	ModifiedAt time.Time    `json:"modified_at"`
	Size       int64        `json:"size"`
	Digest     string       `json:"digest"`
	Details    ModelDetails `json:"details"`
}

type ModelDetails struct {
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

type modelListResponse struct {
	Models []Model `json:"models"`
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	client := *httpClient
	client.Timeout = 0
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if client.Transport == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client.Transport = transport
	}
	return &Client{
		baseURL:        strings.TrimRight(baseURL, "/"),
		httpClient:     &client,
		generationSlot: make(chan struct{}, 1),
	}
}

func (c *Client) InstalledModels(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, newClientError(ReasonRequestCreationFailed, fmt.Errorf("create Ollama request: %w", err))
	}
	request.Header.Set("Accept", "application/json")

	startedAt := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		logOutboundCall(ctx, "list_models", http.MethodGet, 0, "error", startedAt)
		return nil, newClientError(requestFailureReason(err), fmt.Errorf("call Ollama: %w", err))
	}
	outcome := "success"
	defer func() {
		response.Body.Close()
		logOutboundCall(ctx, "list_models", http.MethodGet, response.StatusCode, outcome, startedAt)
	}()

	if response.StatusCode != http.StatusOK {
		outcome = "error"
		return nil, newClientError(ReasonUpstreamHTTPError, fmt.Errorf("Ollama returned %s", response.Status))
	}

	var listing modelListResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize))
	if err := decoder.Decode(&listing); err != nil {
		outcome = "error"
		return nil, newClientError(ReasonResponseDecodingFailed, fmt.Errorf("decode Ollama model list: %w", err))
	}
	if listing.Models == nil {
		listing.Models = []Model{}
	}
	return listing.Models, nil
}

type generateRequest struct {
	Model     string         `json:"model"`
	System    string         `json:"system"`
	Prompt    string         `json:"prompt"`
	Stream    bool           `json:"stream"`
	Think     bool           `json:"think"`
	KeepAlive string         `json:"keep_alive"`
	Format    any            `json:"format"`
	Options   map[string]any `json:"options"`
}

type generateResponse struct {
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	TotalDuration      int64  `json:"total_duration"`
	LoadDuration       int64  `json:"load_duration"`
	PromptEvalCount    int    `json:"prompt_eval_count"`
	PromptEvalDuration int64  `json:"prompt_eval_duration"`
	EvalCount          int    `json:"eval_count"`
	EvalDuration       int64  `json:"eval_duration"`
}

// GenerateStructured sends a schema-constrained request to the configured
// loopback Ollama endpoint. Calls are serialized to keep local resource use
// bounded when several inbox items are triaged concurrently.
func (c *Client) GenerateStructured(
	ctx context.Context,
	model string,
	system string,
	prompt string,
	schema any,
) ([]byte, error) {
	if strings.TrimSpace(model) == "" {
		return nil, newClientError(ReasonModelNotConfigured, fmt.Errorf("Ollama model is not configured"))
	}
	queuedAt := time.Now()
	select {
	case c.generationSlot <- struct{}{}:
		defer func() { <-c.generationSlot }()
	case <-ctx.Done():
		reason := ReasonQueueCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = ReasonQueueTimeout
		}
		return nil, newClientError(reason, ctx.Err())
	}
	queueDuration := time.Since(queuedAt)

	ctx, cancel := context.WithTimeout(ctx, generationTimeout)
	defer cancel()
	payload, err := json.Marshal(generateRequest{
		Model:     model,
		System:    system,
		Prompt:    prompt,
		Stream:    false,
		Think:     false,
		KeepAlive: "15m",
		Format:    schema,
		Options: map[string]any{
			"temperature": 0,
			"num_ctx":     4096,
			"num_predict": 160,
		},
	})
	if err != nil {
		return nil, newClientError(ReasonRequestEncodingFailed, fmt.Errorf("encode Ollama generation request: %w", err))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return nil, newClientError(ReasonRequestCreationFailed, fmt.Errorf("create Ollama generation request: %w", err))
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	startedAt := time.Now()
	response, err := c.httpClient.Do(request)
	if err != nil {
		logGenerationCall(ctx, model, 0, "error", startedAt, queueDuration, len(payload), generateResponse{})
		return nil, newClientError(requestFailureReason(err), fmt.Errorf("call Ollama generation: %w", err))
	}
	outcome := "success"
	generated := generateResponse{}
	defer func() {
		response.Body.Close()
		logGenerationCall(ctx, model, response.StatusCode, outcome, startedAt, queueDuration, len(payload), generated)
	}()
	if response.StatusCode != http.StatusOK {
		outcome = "error"
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return nil, newClientError(ReasonUpstreamHTTPError, fmt.Errorf("Ollama generation returned %s: %s", response.Status, strings.TrimSpace(string(body))))
	}

	if err := json.NewDecoder(io.LimitReader(response.Body, maxGenerationBytes)).Decode(&generated); err != nil {
		outcome = "error"
		return nil, newClientError(ReasonResponseDecodingFailed, fmt.Errorf("decode Ollama generation response: %w", err))
	}
	if !generated.Done || strings.TrimSpace(generated.Response) == "" {
		outcome = "error"
		return nil, newClientError(ReasonIncompleteResponse, fmt.Errorf("Ollama generation did not return a completed response"))
	}
	return []byte(generated.Response), nil
}

func logOutboundCall(ctx context.Context, operation, method string, status int, outcome string, startedAt time.Time) {
	slog.Info("outbound api call completed",
		"request_id", observability.RequestID(ctx),
		"service", "ollama",
		"operation", operation,
		"method", method,
		"status", status,
		"outcome", outcome,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
}

func logGenerationCall(
	ctx context.Context,
	model string,
	status int,
	outcome string,
	startedAt time.Time,
	queueDuration time.Duration,
	requestBytes int,
	generated generateResponse,
) {
	slog.Info("outbound api call completed",
		"request_id", observability.RequestID(ctx),
		"service", "ollama",
		"operation", "generate",
		"method", http.MethodPost,
		"status", status,
		"outcome", outcome,
		"model", model,
		"queue_ms", queueDuration.Milliseconds(),
		"duration_ms", time.Since(startedAt).Milliseconds(),
		"request_bytes", requestBytes,
		"ollama_total_ms", time.Duration(generated.TotalDuration).Milliseconds(),
		"ollama_load_ms", time.Duration(generated.LoadDuration).Milliseconds(),
		"prompt_tokens", generated.PromptEvalCount,
		"prompt_eval_ms", time.Duration(generated.PromptEvalDuration).Milliseconds(),
		"output_tokens", generated.EvalCount,
		"output_eval_ms", time.Duration(generated.EvalDuration).Milliseconds(),
	)
}
