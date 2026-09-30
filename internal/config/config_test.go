package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUsesLoopbackDefaults(t *testing.T) {
	t.Setenv("APP_ADDRESS", "")
	t.Setenv("APP_UI_URL", "")
	t.Setenv("GMAIL_CLIENT_ID", "client-id")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Address != defaultAddress {
		t.Fatalf("Address = %q, want %q", got.Address, defaultAddress)
	}
	if got.OAuthRedirectURL != "http://127.0.0.1:8787/api/v1/auth/gmail/callback" {
		t.Fatalf("OAuthRedirectURL = %q", got.OAuthRedirectURL)
	}
	if got.OllamaBaseURL != defaultOllamaBaseURL {
		t.Fatalf("OllamaBaseURL = %q, want %q", got.OllamaBaseURL, defaultOllamaBaseURL)
	}
	if !got.OllamaAutoStart {
		t.Fatal("OllamaAutoStart = false, want true")
	}
	if !filepath.IsAbs(got.DataDirectory) {
		t.Fatalf("DataDirectory = %q, want an absolute path", got.DataDirectory)
	}
}

func TestLoadUsesConfiguredDataDirectory(t *testing.T) {
	want := filepath.Join(t.TempDir(), "workspace-data")
	t.Setenv("APP_DATA_DIR", want)

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.DataDirectory != want {
		t.Fatalf("DataDirectory = %q, want %q", got.DataDirectory, want)
	}
}

func TestLoadRejectsRelativeDataDirectory(t *testing.T) {
	t.Setenv("APP_DATA_DIR", "local-data")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a relative APP_DATA_DIR")
	}
}

func TestLoadRejectsPublicBinding(t *testing.T) {
	t.Setenv("APP_ADDRESS", "0.0.0.0:8787")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with a public bind address")
	}
}

func TestLoadRejectsRemoteOllamaEndpoint(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "https://ollama.example.com")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with a remote Ollama endpoint")
	}
}

func TestLoadRejectsOllamaEndpointWithQuery(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://127.0.0.1:11434?remote=value")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with an Ollama endpoint query")
	}
}

func TestLoadAllowsDisablingOllamaAutoStart(t *testing.T) {
	t.Setenv("OLLAMA_AUTO_START", "false")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.OllamaAutoStart {
		t.Fatal("OllamaAutoStart = true, want false")
	}
}

func TestLoadReadsPreferredOllamaModel(t *testing.T) {
	t.Setenv("OLLAMA_MODEL", "  qwen2.5:3b  ")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.OllamaModel != "qwen2.5:3b" {
		t.Fatalf("OllamaModel = %q", got.OllamaModel)
	}
}

func TestLoadRejectsInvalidOllamaAutoStart(t *testing.T) {
	t.Setenv("OLLAMA_AUTO_START", "sometimes")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted an invalid OLLAMA_AUTO_START value")
	}
}

func TestParseDotEnvDoesNotOverrideProcessEnvironment(t *testing.T) {
	t.Setenv("GMAIL_CLIENT_ID", "from-process")
	input := strings.NewReader("GMAIL_CLIENT_ID=from-file\nGMAIL_CLIENT_SECRET='quoted-secret'\n")

	if err := parseDotEnv(input); err != nil {
		t.Fatalf("parseDotEnv() error = %v", err)
	}
	if got := envOrDefault("GMAIL_CLIENT_ID", ""); got != "from-process" {
		t.Fatalf("GMAIL_CLIENT_ID = %q, want process value", got)
	}
	if got := envOrDefault("GMAIL_CLIENT_SECRET", ""); got != "quoted-secret" {
		t.Fatalf("GMAIL_CLIENT_SECRET was not parsed")
	}
}

func TestParseDotEnvRejectsInvalidAssignments(t *testing.T) {
	if err := parseDotEnv(strings.NewReader("NOT A NAME=value\n")); err == nil {
		t.Fatal("parseDotEnv() accepted an invalid variable name")
	}
}

func TestParseDotEnvRejectsUnmatchedQuotes(t *testing.T) {
	if err := parseDotEnv(strings.NewReader("GMAIL_CLIENT_ID='unterminated\n")); err == nil {
		t.Fatal("parseDotEnv() accepted an unmatched quote")
	}
}
