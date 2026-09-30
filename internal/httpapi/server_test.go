package httpapi

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"local-email-workspace/internal/config"
	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/ollama"
	"local-email-workspace/internal/triage"
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

func TestGmailThreadReturnsNormalizedConversationBodies(t *testing.T) {
	body := base64.RawURLEncoding.EncodeToString([]byte(`<p>Please approve the proposal.</p><script>ignore()</script>`))
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/gmail/v1/users/me/threads/thread-1" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("format") != "full" {
			t.Fatalf("format = %q, want full", request.URL.Query().Get("format"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"thread-1",
				"messages":[{
					"id":"message-1",
					"threadId":"thread-1",
					"internalDate":"100",
					"payload":{
						"mimeType":"text/html",
						"headers":[{"name":"Subject","value":"Approval request"}],
						"body":{"data":"` + body + `"}
					}
				}]
			}`)),
		}, nil
	})}
	store := &credentials.MemoryStore{}
	store.Cache(credentials.OAuthCredential{
		AccessToken: "access-token",
		Expiry:      time.Now().Add(time.Hour),
	})
	server := New(config.Config{
		Address:          "127.0.0.1:8787",
		UIURL:            "http://127.0.0.1:5173",
		GmailClientID:    "test-client",
		OAuthRedirectURL: "http://127.0.0.1:8787/api/v1/auth/gmail/callback",
		OllamaBaseURL:    "http://127.0.0.1:11434",
	}, store, httpClient)
	cookie := createTestSession(t, server)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/v1/gmail/threads/thread-1", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	responseBody := response.Body.String()
	if !strings.Contains(responseBody, `"body":"Please approve the proposal."`) {
		t.Fatalf("body = %s", responseBody)
	}
	if strings.Contains(responseBody, "ignore()") {
		t.Fatalf("body contains script content: %s", responseBody)
	}
}

func TestGmailThreadTriageAppliesSuggestedFloorToGmailImportant(t *testing.T) {
	body := base64.RawURLEncoding.EncodeToString([]byte("Important account update"))
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/gmail/v1/users/me/threads/thread-1":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{
					"id":"thread-1",
					"messages":[{
						"id":"message-1",
						"threadId":"thread-1",
						"labelIds":["IMPORTANT"],
						"payload":{"mimeType":"text/plain","body":{"data":"` + body + `"}}
					}]
				}`)),
			}, nil
		case "/api/generate":
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{
					"response":"{\"visibility\":\"all\",\"category\":\"automated\",\"needs_action\":false,\"urgent\":false,\"confidence\":0.99,\"reason_codes\":[\"automated_notification\"],\"source_message_ids\":[\"m1\"]}",
					"done":true
				}`)),
			}, nil
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
			return nil, nil
		}
	})}
	store := &credentials.MemoryStore{}
	store.Cache(credentials.OAuthCredential{
		AccessToken:  "access-token",
		EmailAddress: "person@example.com",
		Expiry:       time.Now().Add(time.Hour),
	})
	server := New(config.Config{
		Address:          "127.0.0.1:8787",
		UIURL:            "http://127.0.0.1:5173",
		GmailClientID:    "test-client",
		OAuthRedirectURL: "http://127.0.0.1:8787/api/v1/auth/gmail/callback",
		OllamaBaseURL:    "http://127.0.0.1:11434",
		OllamaModel:      "qwen2.5:3b",
	}, store, httpClient)
	cookie := createTestSession(t, server)

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/v1/gmail/threads/thread-1/triage", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	responseBody := response.Body.String()
	if !strings.Contains(responseBody, `"visibility":"suggested"`) || !strings.Contains(responseBody, `"aiStatus":"applied"`) {
		t.Fatalf("body = %s", responseBody)
	}
}

func TestGmailThreadTriageSkipsOllamaForPoliticalBulkMail(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	body := base64.RawURLEncoding.EncodeToString([]byte("Campaign update: can you chip in by midnight?"))
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/api/generate" {
			t.Fatal("Ollama generation was called for deterministic bulk mail")
		}
		if request.URL.Path != "/gmail/v1/users/me/threads/thread-campaign" {
			t.Fatalf("unexpected path %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
				"id":"thread-campaign",
				"messages":[{
					"id":"message-campaign",
					"threadId":"thread-campaign",
					"labelIds":["INBOX"],
					"payload":{
						"mimeType":"text/plain",
						"headers":[
							{"name":"Subject","value":"Campaign update"},
							{"name":"List-Unsubscribe","value":"<https://example.com/unsubscribe>"}
						],
						"body":{"data":"` + body + `"}
					}
				}]
			}`)),
		}, nil
	})}
	store := &credentials.MemoryStore{}
	store.Cache(credentials.OAuthCredential{
		AccessToken:  "access-token",
		EmailAddress: "person@example.com",
		Expiry:       time.Now().Add(time.Hour),
	})
	server := New(config.Config{
		Address:          "127.0.0.1:8787",
		UIURL:            "http://127.0.0.1:5173",
		GmailClientID:    "test-client",
		OAuthRedirectURL: "http://127.0.0.1:8787/api/v1/auth/gmail/callback",
		OllamaBaseURL:    "http://127.0.0.1:11434",
		OllamaModel:      "qwen2.5:3b",
	}, store, httpClient)
	cookie := createTestSession(t, server)

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/v1/gmail/threads/thread-campaign/triage", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	responseBody := response.Body.String()
	if !strings.Contains(responseBody, `"visibility":"all"`) ||
		!strings.Contains(responseBody, `"category":"political_campaign"`) ||
		!strings.Contains(responseBody, `"aiStatus":"rules"`) {
		t.Fatalf("body = %s", responseBody)
	}
	logged := logs.String()
	if !strings.Contains(logged, `"msg":"gmail triage resolved by rules"`) ||
		!strings.Contains(logged, `"needs_ai":false`) ||
		!strings.Contains(logged, `"reason_codes":["political_campaign","bulk_sender"]`) {
		t.Fatalf("log = %s", logged)
	}
}

func TestTriageFailureDetailsReportsSpecificGenerationReason(t *testing.T) {
	err := &triage.EvaluationError{
		Stage:  triage.StageModelGeneration,
		Reason: ollama.ReasonRequestTimeout,
		Err:    errors.New("request timed out"),
	}

	stage, reason := triageFailureDetails(err)
	if stage != triage.StageModelGeneration || reason != ollama.ReasonRequestTimeout {
		t.Fatalf("stage, reason = %q, %q", stage, reason)
	}
}

func TestTriageFailureDetailsReportsSpecificValidationReason(t *testing.T) {
	err := &triage.EvaluationError{
		Stage:  triage.StageResponseValidation,
		Reason: triage.ReasonUnknownCitation,
		Err:    errors.New("triage cited an unknown message alias"),
	}

	stage, reason := triageFailureDetails(err)
	if stage != triage.StageResponseValidation || reason != triage.ReasonUnknownCitation {
		t.Fatalf("stage, reason = %q, %q", stage, reason)
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

func TestRequestLoggingUsesRoutePatternAndOmitsQueryValues(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	server := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8787/api/v1/health?token=do-not-log", nil)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID header is empty")
	}
	logged := logs.String()
	if !strings.Contains(logged, `"msg":"api request completed"`) ||
		!strings.Contains(logged, `"route":"GET /api/v1/health"`) ||
		!strings.Contains(logged, `"status":200`) {
		t.Fatalf("log = %s", logged)
	}
	if strings.Contains(logged, "do-not-log") || strings.Contains(logged, "token=") {
		t.Fatalf("log contains query data: %s", logged)
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

func TestSessionReusesValidSessionCookie(t *testing.T) {
	server := newTestServer(t)
	cookie := createTestSession(t, server)

	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8787/api/v1/session", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != cookie.Value {
		t.Fatalf("cookies = %#v, want reused session", cookies)
	}
	server.mu.RLock()
	sessionCount := len(server.sessions)
	server.mu.RUnlock()
	if sessionCount != 1 {
		t.Fatalf("session count = %d, want 1", sessionCount)
	}
}
