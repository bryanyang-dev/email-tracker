package config

import (
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
}

func TestLoadRejectsPublicBinding(t *testing.T) {
	t.Setenv("APP_ADDRESS", "0.0.0.0:8787")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with a public bind address")
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
