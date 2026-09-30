package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInstalledModels(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tags" {
			t.Fatalf("request = %s %s, want GET /api/tags", r.Method, r.URL.Path)
		}
		return jsonResponse(http.StatusOK, `{
			"models": [{
				"name": "llama3.2:3b",
				"model": "llama3.2:3b",
				"modified_at": "2026-09-29T20:00:00Z",
				"size": 2019393189,
				"digest": "sha256-test",
				"details": {
					"format": "gguf",
					"family": "llama",
					"families": ["llama"],
					"parameter_size": "3.2B",
					"quantization_level": "Q4_K_M"
				}
			}]
		}`), nil
	})}

	client := NewClient("http://127.0.0.1:11434", httpClient)
	models, err := client.InstalledModels(context.Background())
	if err != nil {
		t.Fatalf("InstalledModels() error = %v", err)
	}
	if len(models) != 1 || models[0].Name != "llama3.2:3b" {
		t.Fatalf("models = %#v", models)
	}
	if models[0].Details.ParameterSize != "3.2B" || models[0].Details.QuantizationLevel != "Q4_K_M" {
		t.Fatalf("details = %#v", models[0].Details)
	}
}

func TestInstalledModelsRejectsUnexpectedStatus(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusServiceUnavailable, `{"error":"unavailable"}`), nil
	})}

	client := NewClient("http://127.0.0.1:11434", httpClient)
	if _, err := client.InstalledModels(context.Background()); err == nil {
		t.Fatal("InstalledModels() succeeded for a non-200 response")
	}
}

func TestGenerateStructuredUsesSchemaAndNonStreamingResponse(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/generate" {
			t.Fatalf("request = %s %s, want POST /api/generate", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["model"] != "qwen2.5:3b" || body["stream"] != false {
			t.Fatalf("body = %#v", body)
		}
		if body["think"] != false || body["keep_alive"] != "15m" {
			t.Fatalf("generation controls = %#v", body)
		}
		options, ok := body["options"].(map[string]any)
		if !ok || options["num_predict"] != float64(160) || options["num_ctx"] != float64(4096) {
			t.Fatalf("options = %#v", body["options"])
		}
		format, ok := body["format"].(map[string]any)
		if !ok || format["type"] != "object" {
			t.Fatalf("format = %#v", body["format"])
		}
		return jsonResponse(http.StatusOK, `{"response":"{\"visibility\":\"all\"}","done":true}`), nil
	})}
	client := NewClient("http://127.0.0.1:11434", httpClient)

	result, err := client.GenerateStructured(
		context.Background(),
		"qwen2.5:3b",
		"fixed system prompt",
		"untrusted input",
		map[string]any{"type": "object"},
	)
	if err != nil {
		t.Fatalf("GenerateStructured() error = %v", err)
	}
	if string(result) != `{"visibility":"all"}` {
		t.Fatalf("result = %s", result)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
