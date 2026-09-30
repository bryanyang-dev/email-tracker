package ollama

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureRunningDoesNotStartAnotherServer(t *testing.T) {
	client := NewClient("http://127.0.0.1:11434", modelListHTTPClient(t, nil))
	runtime := NewRuntime(client, "http://127.0.0.1:11434")
	runtime.startCommand = func() (*exec.Cmd, error) {
		t.Fatal("startCommand called for an available server")
		return nil, nil
	}

	started, err := runtime.EnsureRunning(context.Background())
	if err != nil {
		t.Fatalf("EnsureRunning() error = %v", err)
	}
	if started {
		t.Fatal("EnsureRunning() started another server")
	}
}

func TestEnsureRunningStartsInstalledCLIAndWaitsForReadiness(t *testing.T) {
	var available atomic.Bool
	client := NewClient("http://127.0.0.1:11434", modelListHTTPClient(t, &available))
	runtime := NewRuntime(client, "http://127.0.0.1:11434")
	runtime.startCommand = func() (*exec.Cmd, error) {
		available.Store(true)
		return exec.Command("sleep", "30"), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started, err := runtime.EnsureRunning(ctx)
	if err != nil {
		t.Fatalf("EnsureRunning() error = %v", err)
	}
	if !started {
		t.Fatal("EnsureRunning() did not report starting the server")
	}
	stopContext, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := runtime.Stop(stopContext); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestRuntimeEnvironmentExcludesApplicationSecrets(t *testing.T) {
	environment := runtimeEnvironment([]string{
		"HOME=/Users/person",
		"PATH=/usr/local/bin:/usr/bin",
		"GMAIL_CLIENT_SECRET=secret",
		"GMAIL_CLIENT_ID=client",
		"OLLAMA_MODELS=/models",
		"OLLAMA_HOST=remote.example:11434",
	}, "127.0.0.1:11434")
	joined := strings.Join(environment, "\n")

	if strings.Contains(joined, "GMAIL_") || strings.Contains(joined, "secret") {
		t.Fatalf("environment contains application credentials: %q", joined)
	}
	if !strings.Contains(joined, "OLLAMA_MODELS=/models") {
		t.Fatalf("environment omitted Ollama configuration: %q", joined)
	}
	if !strings.Contains(joined, "OLLAMA_HOST=127.0.0.1:11434") || strings.Contains(joined, "remote.example") {
		t.Fatalf("environment has unexpected Ollama host: %q", joined)
	}
}

func modelListHTTPClient(t *testing.T, available *atomic.Bool) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if available != nil && !available.Load() {
			return nil, context.DeadlineExceeded
		}
		return jsonResponse(http.StatusOK, `{"models":[]}`), nil
	})}
}
