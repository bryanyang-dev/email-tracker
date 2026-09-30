package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"local-email-workspace/internal/config"
	"local-email-workspace/internal/credentials"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return New(config.Config{
		Address:          "127.0.0.1:8787",
		UIURL:            "http://127.0.0.1:5173",
		GmailClientID:    "test-client",
		OAuthRedirectURL: "http://127.0.0.1:8787/api/v1/auth/gmail/callback",
		OllamaBaseURL:    "http://127.0.0.1:1",
	}, &credentials.MemoryStore{}, &http.Client{Timeout: time.Second})
}

func TestOllamaStatusReturnsInstalledModels(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("path = %q, want /api/tags", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
			"models": [{
				"name": "qwen2.5:3b",
				"size": 1900000000,
				"details": {"parameter_size": "3.1B", "quantization_level": "Q4_K_M"}
			}]
		}`)),
		}, nil
	})}

	server := New(config.Config{
		Address:          "127.0.0.1:8787",
		UIURL:            "http://127.0.0.1:5173",
		GmailClientID:    "test-client",
		OAuthRedirectURL: "http://127.0.0.1:8787/api/v1/auth/gmail/callback",
		OllamaBaseURL:    "http://127.0.0.1:11434",
	}, &credentials.MemoryStore{}, httpClient)
	cookie := createTestSession(t, server)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/v1/ollama/status", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); !strings.Contains(body, `"available":true`) || !strings.Contains(body, `"name":"qwen2.5:3b"`) {
		t.Fatalf("body = %s", body)
	}
}

func TestOllamaStatusKeepsApplicationAvailableWhenOllamaIsStopped(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	server := New(config.Config{
		Address:          "127.0.0.1:8787",
		UIURL:            "http://127.0.0.1:5173",
		GmailClientID:    "test-client",
		OAuthRedirectURL: "http://127.0.0.1:8787/api/v1/auth/gmail/callback",
		OllamaBaseURL:    "http://127.0.0.1:11434",
	}, &credentials.MemoryStore{}, httpClient)
	cookie := createTestSession(t, server)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/v1/ollama/status", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); !strings.Contains(body, `"available":false`) {
		t.Fatalf("body = %s", body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func createTestSession(t *testing.T, server *Server) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/v1/session", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %#v", cookies)
	}
	return cookies[0]
}

func TestHealthReportsGmailConfiguration(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/v1/health", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), `"gmailConfigured":true`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestSessionRejectsUnexpectedOrigin(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/v1/session", nil)
	request.Header.Set("Origin", "https://example.com")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestSessionCreatesHTTPOnlyStrictCookie(t *testing.T) {
	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/v1/session", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusCreated)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies = %#v", cookies)
	}
}
