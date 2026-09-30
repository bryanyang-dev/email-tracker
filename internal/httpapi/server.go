package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"local-email-workspace/internal/config"
	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/googleapi"
	"local-email-workspace/internal/ollama"
)

const sessionCookieName = "lew_session"

type pendingAuthorization struct {
	sessionID string
	verifier  string
	expiresAt time.Time
}

type Server struct {
	config   config.Config
	store    credentials.Store
	oauth    *googleapi.OAuthClient
	gmail    *googleapi.GmailClient
	ollama   *ollama.Client
	mux      *http.ServeMux
	mu       sync.RWMutex
	sessions map[string]time.Time
	pending  map[string]pendingAuthorization
}

func New(cfg config.Config, store credentials.Store, httpClient *http.Client) *Server {
	server := &Server{
		config:   cfg,
		store:    store,
		oauth:    googleapi.NewOAuthClient(httpClient, cfg.GmailClientID, cfg.GmailClientSecret, cfg.OAuthRedirectURL),
		gmail:    googleapi.NewGmailClient(httpClient),
		ollama:   ollama.NewClient(cfg.OllamaBaseURL, httpClient),
		mux:      http.NewServeMux(),
		sessions: make(map[string]time.Time),
		pending:  make(map[string]pendingAuthorization),
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.securityHeaders(s.requireLoopbackHost(s.mux))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/v1/session", s.handleSession)
	s.mux.Handle("GET /api/v1/auth/gmail/status", s.authenticated(http.HandlerFunc(s.handleGmailStatus)))
	s.mux.Handle("POST /api/v1/auth/gmail/start", s.authenticated(s.requireOrigin(http.HandlerFunc(s.handleGmailStart))))
	s.mux.HandleFunc("GET /api/v1/auth/gmail/callback", s.handleGmailCallback)
	s.mux.Handle("POST /api/v1/auth/gmail/disconnect", s.authenticated(s.requireOrigin(http.HandlerFunc(s.handleGmailDisconnect))))
	s.mux.Handle("GET /api/v1/gmail/messages", s.authenticated(http.HandlerFunc(s.handleInbox)))
	s.mux.Handle("GET /api/v1/ollama/status", s.authenticated(http.HandlerFunc(s.handleOllamaStatus)))
}

type ollamaModelResponse struct {
	Name              string `json:"name"`
	ParameterSize     string `json:"parameterSize,omitempty"`
	QuantizationLevel string `json:"quantizationLevel,omitempty"`
	Size              int64  `json:"size"`
}

func (s *Server) handleOllamaStatus(w http.ResponseWriter, r *http.Request) {
	models, err := s.ollama.InstalledModels(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"models":    []ollamaModelResponse{},
			"message":   "Ollama is not running on the configured loopback endpoint.",
		})
		return
	}

	responseModels := make([]ollamaModelResponse, len(models))
	for index, model := range models {
		responseModels[index] = ollamaModelResponse{
			Name:              model.Name,
			ParameterSize:     model.Details.ParameterSize,
			QuantizationLevel: model.Details.QuantizationLevel,
			Size:              model.Size,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"models":    responseModels,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":          "ok",
		"gmailConfigured": s.config.GmailConfigured(),
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if !s.validOrigin(r.Header.Get("Origin")) {
		writeError(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.")
		return
	}

	sessionID, err := randomURLToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "Could not create a local session.")
		return
	}
	expiresAt := time.Now().Add(12 * time.Hour)
	s.mu.Lock()
	s.sessions[sessionID] = expiresAt
	s.pruneLocked(time.Now())
	s.mu.Unlock()

	setSessionCookie(w, sessionID, expiresAt)
	writeJSON(w, http.StatusCreated, map[string]string{"status": "ready"})
}

func (s *Server) handleGmailStatus(w http.ResponseWriter, r *http.Request) {
	if !s.config.GmailConfigured() {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "connected": false})
		return
	}
	credential, err := s.store.Load(r.Context())
	if errors.Is(err, credentials.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"configured": true, "connected": false})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential_store_failed", "Could not read Gmail authorization from Keychain.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured":   true,
		"connected":    true,
		"emailAddress": credential.EmailAddress,
	})
}

func (s *Server) handleGmailStart(w http.ResponseWriter, r *http.Request) {
	if !s.config.GmailConfigured() {
		writeError(w, http.StatusServiceUnavailable, "gmail_not_configured", "Set GMAIL_CLIENT_ID before connecting Gmail.")
		return
	}
	sessionID := sessionIDFromContext(r.Context())
	state, err := randomURLToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oauth_start_failed", "Could not start Gmail authorization.")
		return
	}
	verifier, err := randomURLToken(64)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oauth_start_failed", "Could not start Gmail authorization.")
		return
	}
	challengeHash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeHash[:])

	s.mu.Lock()
	s.pending[state] = pendingAuthorization{
		sessionID: sessionID,
		verifier:  verifier,
		expiresAt: time.Now().Add(10 * time.Minute),
	}
	s.pruneLocked(time.Now())
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{
		"authorizationUrl": s.oauth.AuthorizationURL(state, challenge),
	})
}

func (s *Server) handleGmailCallback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	s.mu.Lock()
	pending, found := s.pending[state]
	if found {
		delete(s.pending, state)
	}
	s.mu.Unlock()

	if !found || pending.expiresAt.Before(time.Now()) || state == "" {
		s.redirectWithResult(w, r, "error", "invalid_oauth_state", "")
		return
	}
	if oauthError := r.URL.Query().Get("error"); oauthError != "" {
		s.redirectWithResult(w, r, "error", "authorization_denied", pending.sessionID)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.redirectWithResult(w, r, "error", "missing_authorization_code", pending.sessionID)
		return
	}

	credential, err := s.oauth.Exchange(r.Context(), code, pending.verifier)
	if err != nil {
		slog.Error("gmail oauth exchange failed", "error", err)
		s.redirectWithResult(w, r, "error", "token_exchange_failed", pending.sessionID)
		return
	}
	profile, err := s.gmail.Profile(r.Context(), credential)
	if err != nil {
		slog.Error("gmail profile request failed", "error", err)
		s.redirectWithResult(w, r, "error", "gmail_profile_failed", pending.sessionID)
		return
	}
	credential.EmailAddress = profile.EmailAddress
	if err := s.store.Save(r.Context(), credential); err != nil {
		slog.Error("gmail credential save failed", "error", err)
		s.redirectWithResult(w, r, "error", "credential_store_failed", pending.sessionID)
		return
	}

	s.redirectWithResult(w, r, "connected", "", pending.sessionID)
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	credential, err := s.validCredential(r.Context())
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "gmail_not_connected", "Connect Gmail before loading the inbox.")
		return
	}
	if err != nil {
		slog.Error("gmail credential refresh failed", "error", err)
		writeError(w, http.StatusUnauthorized, "gmail_reauthorization_required", "Gmail authorization needs to be renewed.")
		return
	}

	page, err := s.gmail.Inbox(r.Context(), credential, r.URL.Query().Get("limit"), r.URL.Query().Get("pageToken"))
	if err != nil {
		slog.Error("gmail inbox request failed", "error", err)
		writeError(w, http.StatusBadGateway, "gmail_request_failed", "Could not retrieve messages from Gmail.")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleGmailDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "disconnect_failed", "Could not remove Gmail authorization from Keychain.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validCredential(ctx context.Context) (credentials.OAuthCredential, error) {
	credential, err := s.store.Load(ctx)
	if err != nil {
		return credentials.OAuthCredential{}, err
	}
	if credential.AccessToken != "" && time.Until(credential.Expiry) > time.Minute {
		return credential, nil
	}
	credential, err = s.oauth.Refresh(ctx, credential)
	if err != nil {
		return credentials.OAuthCredential{}, err
	}
	// Google does not rotate the refresh token in a normal access-token refresh.
	// Keep the short-lived access token in memory so routine refreshes do not
	// trigger another macOS Keychain authorization dialog.
	s.store.Cache(credential)
	return credential, nil
}

func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "session_required", "Start a local application session first.")
			return
		}

		s.mu.RLock()
		expiresAt, found := s.sessions[cookie.Value]
		s.mu.RUnlock()
		if !found || expiresAt.Before(time.Now()) {
			writeError(w, http.StatusUnauthorized, "session_expired", "The local application session expired.")
			return
		}

		ctx := context.WithValue(r.Context(), sessionContextKey{}, cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.validOrigin(r.Header.Get("Origin")) {
			writeError(w, http.StatusForbidden, "invalid_origin", "The request origin is not allowed.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if parsedHost, _, err := net.SplitHostPort(r.Host); err == nil {
			host = parsedHost
		}
		if host != "127.0.0.1" {
			writeError(w, http.StatusForbidden, "invalid_host", "Only literal loopback requests are allowed.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) validOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(origin), []byte(s.config.UIURL)) == 1 ||
		subtle.ConstantTimeCompare([]byte(origin), []byte("http://"+s.config.Address)) == 1
}

func (s *Server) redirectWithResult(w http.ResponseWriter, r *http.Request, result, code, sessionID string) {
	destination, err := url.Parse(s.config.UIURL)
	if err != nil {
		http.Error(w, "Invalid UI URL", http.StatusInternalServerError)
		return
	}
	query := destination.Query()
	query.Set("gmail", result)
	if code != "" {
		query.Set("code", code)
	}
	destination.RawQuery = query.Encode()
	if sessionID != "" {
		setSessionCookie(w, sessionID, time.Now().Add(12*time.Hour))
	}
	http.Redirect(w, r, destination.String(), http.StatusFound)
}

func (s *Server) pruneLocked(now time.Time) {
	for sessionID, expiresAt := range s.sessions {
		if expiresAt.Before(now) {
			delete(s.sessions, sessionID)
		}
	}
	for state, pending := range s.pending {
		if pending.expiresAt.Before(now) {
			delete(s.pending, state)
		}
	}
}

type sessionContextKey struct{}

func sessionIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(sessionContextKey{}).(string)
	return value
}

func setSessionCookie(w http.ResponseWriter, value string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
}

func randomURLToken(byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("encode response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
