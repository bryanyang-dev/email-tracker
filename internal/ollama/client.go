package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
		return nil, fmt.Errorf("create Ollama request: %w", err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Ollama: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ollama returned %s", response.Status)
	}

	var listing modelListResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize))
	if err := decoder.Decode(&listing); err != nil {
		return nil, fmt.Errorf("decode Ollama model list: %w", err)
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
	Response string `json:"response"`
	Done     bool   `json:"done"`
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
		return nil, fmt.Errorf("Ollama model is not configured")
	}
	select {
	case c.generationSlot <- struct{}{}:
		defer func() { <-c.generationSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

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
		return nil, fmt.Errorf("encode Ollama generation request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create Ollama generation request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Ollama generation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		return nil, fmt.Errorf("Ollama generation returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var generated generateResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxGenerationBytes)).Decode(&generated); err != nil {
		return nil, fmt.Errorf("decode Ollama generation response: %w", err)
	}
	if !generated.Done || strings.TrimSpace(generated.Response) == "" {
		return nil, fmt.Errorf("Ollama generation did not return a completed response")
	}
	return []byte(generated.Response), nil
}
