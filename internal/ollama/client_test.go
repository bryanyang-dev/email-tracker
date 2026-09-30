package ollama

import (
	"context"
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
