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
	"strconv"
	"sync"
	"time"

	"local-email-workspace/internal/config"
	"local-email-workspace/internal/conversations"
	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/googleapi"
	"local-email-workspace/internal/mailsync"
	"local-email-workspace/internal/observability"
	"local-email-workspace/internal/ollama"
	"local-email-workspace/internal/storage"
	"local-email-workspace/internal/triage"
)

const sessionCookieName = "lew_session"

type pendingAuthorization struct {
	sessionID string
	verifier  string
	expiresAt time.Time
}

type Server struct {
	config                config.Config
	store                 credentials.Store
	oauth                 *googleapi.OAuthClient
	gmail                 *googleapi.GmailClient
	ollama                *ollama.Client
	triage                *triage.Service
	mailSync              *mailsync.Service
	conversationProcessor *conversations.Service
	repository            storage.Repository
	mux                   *http.ServeMux
	mu                    sync.RWMutex
	sessions              map[string]time.Time
	pending               map[string]pendingAuthorization
}

func New(cfg config.Config, store credentials.Store, httpClient *http.Client) *Server {
	return NewWithRepository(cfg, store, httpClient, nil)
}

func NewWithRepository(
	cfg config.Config,
	store credentials.Store,
	httpClient *http.Client,
	repository storage.Repository,
) *Server {
	ollamaClient := ollama.NewClient(cfg.OllamaBaseURL, httpClient)
	gmailClient := googleapi.NewGmailClient(httpClient)
	server := &Server{
		config:   cfg,
		store:    store,
		oauth:    googleapi.NewOAuthClient(httpClient, cfg.GmailClientID, cfg.GmailClientSecret, cfg.OAuthRedirectURL),
		gmail:    gmailClient,
		ollama:   ollamaClient,
		triage:   triage.NewService(ollamaClient),
		mux:      http.NewServeMux(),
		sessions: make(map[string]time.Time),
		pending:  make(map[string]pendingAuthorization),
	}
	if repository != nil {
		server.mailSync = mailsync.New(repository, gmailClient)
		server.conversationProcessor = conversations.New(repository, gmailClient, server.triage)
		server.repository = repository
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.requestLogger(s.securityHeaders(s.requireLoopbackHost(s.mux)))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/v1/session", s.handleSession)
	s.mux.Handle("GET /api/v1/auth/gmail/status", s.authenticated(http.HandlerFunc(s.handleGmailStatus)))
	s.mux.Handle("POST /api/v1/auth/gmail/start", s.authenticated(s.requireOrigin(http.HandlerFunc(s.handleGmailStart))))
	s.mux.HandleFunc("GET /api/v1/auth/gmail/callback", s.handleGmailCallback)
	s.mux.Handle("POST /api/v1/auth/gmail/disconnect", s.authenticated(s.requireOrigin(http.HandlerFunc(s.handleGmailDisconnect))))
	s.mux.Handle("GET /api/v1/gmail/messages", s.authenticated(http.HandlerFunc(s.handleInbox)))
	s.mux.Handle("GET /api/v1/gmail/sync", s.authenticated(http.HandlerFunc(s.handleGmailSyncStatus)))
	s.mux.Handle("POST /api/v1/gmail/sync", s.authenticated(s.requireOrigin(http.HandlerFunc(s.handleGmailSync))))
	s.mux.Handle("GET /api/v1/gmail/threads/{threadID}", s.authenticated(http.HandlerFunc(s.handleThread)))
	s.mux.Handle("POST /api/v1/gmail/threads/{threadID}/triage", s.authenticated(s.requireOrigin(http.HandlerFunc(s.handleThreadTriage))))
	s.mux.Handle("GET /api/v1/conversations", s.authenticated(http.HandlerFunc(s.handleWorkspaceConversations)))
	s.mux.Handle("GET /api/v1/conversations/{conversationID}", s.authenticated(http.HandlerFunc(s.handleWorkspaceConversation)))
	s.mux.Handle("GET /api/v1/ollama/status", s.authenticated(http.HandlerFunc(s.handleOllamaStatus)))
}

type workspaceConversationSummaryResponse struct {
	ID              string             `json:"id"`
	Title           string             `json:"title"`
	State           string             `json:"state"`
	Importance      storage.Importance `json:"importance"`
	ImportanceScore float64            `json:"importanceScore"`
	UpdatedAt       string             `json:"updatedAt"`
	MessageCount    int                `json:"messageCount"`
	Unread          bool               `json:"unread"`
	NeedsAttention  bool               `json:"needsAttention"`
	Participants    []string           `json:"participants"`
	Preview         string             `json:"preview"`
	LatestUpdate    string             `json:"latestUpdate"`
	Category        string             `json:"category"`
	ReasonCodes     []string           `json:"reasonCodes"`
	AIStatus        string             `json:"aiStatus"`
}

type workspaceConversationCountsResponse struct {
	Active    int `json:"active"`
	Attention int `json:"attention"`
	Suggested int `json:"suggested"`
	Snoozed   int `json:"snoozed"`
	Resolved  int `json:"resolved"`
	All       int `json:"all"`
}

type workspaceConversationMessageResponse struct {
	ID                 string   `json:"id"`
	ThreadID           string   `json:"threadId"`
	Subject            string   `json:"subject"`
	From               string   `json:"from"`
	To                 string   `json:"to"`
	Cc                 string   `json:"cc,omitempty"`
	Date               string   `json:"date"`
	InternalAt         string   `json:"internalAt"`
	LabelIDs           []string `json:"labelIds"`
	Body               string   `json:"body"`
	BodySource         string   `json:"bodySource"`
	BodyTruncated      bool     `json:"bodyTruncated"`
	SuspiciousContent  bool     `json:"suspiciousContent"`
	HasListUnsubscribe bool     `json:"hasListUnsubscribe"`
	HasListID          bool     `json:"hasListId"`
	Precedence         string   `json:"precedence,omitempty"`
	AutoSubmitted      bool     `json:"autoSubmitted"`
	HasFeedbackID      bool     `json:"hasFeedbackId"`
}

type workspaceConversationResponse struct {
	workspaceConversationSummaryResponse
	Messages []workspaceConversationMessageResponse `json:"messages"`
}

func (s *Server) handleWorkspaceConversations(w http.ResponseWriter, r *http.Request) {
	if s.repository == nil {
		writeError(w, http.StatusServiceUnavailable, "workspace_unavailable", "Local workspace storage is unavailable.")
		return
	}
	view := r.URL.Query().Get("view")
	if view == "" {
		view = "all"
	}
	if !validConversationView(view) {
		writeError(w, http.StatusBadRequest, "invalid_conversation_view", "A valid conversation view is required.")
		return
	}
	conversations, counts, err := s.repository.WorkspaceConversations(r.Context(), view)
	if err != nil {
		slog.Error("workspace conversation list failed", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "conversation_list_failed", "Could not load local conversations.")
		return
	}
	items := make([]workspaceConversationSummaryResponse, len(conversations))
	for index, conversation := range conversations {
		items[index] = workspaceConversationSummary(conversation)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversations": items,
		"counts": workspaceConversationCountsResponse{
			Active: counts.Active, Attention: counts.Attention, Suggested: counts.Suggested,
			Snoozed: counts.Snoozed, Resolved: counts.Resolved, All: counts.All,
		},
	})
}

func validConversationView(view string) bool {
	switch view {
	case "active", "attention", "suggested", "snoozed", "resolved", "all":
		return true
	default:
		return false
	}
}

func (s *Server) handleWorkspaceConversation(w http.ResponseWriter, r *http.Request) {
	if s.repository == nil {
		writeError(w, http.StatusServiceUnavailable, "workspace_unavailable", "Local workspace storage is unavailable.")
		return
	}
	identifier := r.PathValue("conversationID")
	if identifier == "" || len(identifier) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_conversation_id", "A valid conversation ID is required.")
		return
	}
	conversation, err := s.repository.WorkspaceConversation(r.Context(), identifier)
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "conversation_not_found", "The local conversation was not found.")
		return
	}
	if err != nil {
		slog.Error("workspace conversation load failed", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "conversation_load_failed", "Could not load the local conversation.")
		return
	}
	response := workspaceConversationResponse{workspaceConversationSummaryResponse: workspaceConversationSummary(conversation)}
	response.Messages = make([]workspaceConversationMessageResponse, len(conversation.Messages))
	for index, message := range conversation.Messages {
		response.Messages[index] = workspaceConversationMessageResponse{
			ID:                 message.ProviderMessageID,
			ThreadID:           message.ProviderThreadID,
			Subject:            message.Subject,
			From:               message.From,
			To:                 message.To,
			Cc:                 message.Cc,
			Date:               message.Date,
			InternalAt:         strconv.FormatInt(message.InternalAt.UnixMilli(), 10),
			LabelIDs:           message.LabelIDs,
			Body:               message.Body,
			BodySource:         "plain",
			BodyTruncated:      message.BodyState == storage.BodyTruncated,
			SuspiciousContent:  message.SuspiciousContent,
			HasListUnsubscribe: message.HasListUnsubscribe,
			HasListID:          message.HasListID,
			Precedence:         message.Precedence,
			AutoSubmitted:      message.AutoSubmitted,
			HasFeedbackID:      message.HasFeedbackID,
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func workspaceConversationSummary(conversation storage.WorkspaceConversationProjection) workspaceConversationSummaryResponse {
	latestUpdate := conversation.LatestBody
	if latestUpdate == "" {
		latestUpdate = conversation.Preview
	}
	return workspaceConversationSummaryResponse{
		ID:              conversation.ID,
		Title:           conversation.Title,
		State:           conversation.State,
		Importance:      conversation.Importance,
		ImportanceScore: conversation.ImportanceScore,
		UpdatedAt:       conversation.LastMessageAt.Format(time.RFC3339),
		MessageCount:    conversation.MessageCount,
		Unread:          conversation.Unread,
		NeedsAttention:  conversation.NeedsAttention,
		Participants:    conversation.Participants,
		Preview:         conversation.Preview,
		LatestUpdate:    latestUpdate,
		Category:        conversation.Category,
		ReasonCodes:     conversation.ReasonCodes,
		AIStatus:        conversation.AIStatus,
	}
}

func (s *Server) handleGmailSyncStatus(w http.ResponseWriter, r *http.Request) {
	if s.mailSync == nil {
		writeError(w, http.StatusServiceUnavailable, "mail_sync_unavailable", "Local mailbox storage is unavailable.")
		return
	}
	credential, err := s.validCredential(r.Context())
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "gmail_not_connected", "Connect Gmail before synchronizing the inbox.")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "gmail_reauthorization_required", "Gmail authorization needs to be renewed.")
		return
	}
	status, err := s.mailSync.Status(r.Context(), credential.EmailAddress)
	if err != nil {
		slog.Error("gmail sync status failed", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "mail_sync_status_failed", "Could not read local synchronization status.")
		return
	}
	status, err = s.withConversationProcessingStatus(r.Context(), credential.EmailAddress, status)
	if err != nil {
		slog.Error("conversation processing status failed", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "conversation_processing_failed", "Could not inspect cached conversations.")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleGmailSync(w http.ResponseWriter, r *http.Request) {
	if s.mailSync == nil {
		writeError(w, http.StatusServiceUnavailable, "mail_sync_unavailable", "Local mailbox storage is unavailable.")
		return
	}
	credential, err := s.validCredential(r.Context())
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "gmail_not_connected", "Connect Gmail before synchronizing the inbox.")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnauthorized, "gmail_reauthorization_required", "Gmail authorization needs to be renewed.")
		return
	}
	currentStatus, err := s.mailSync.Status(r.Context(), credential.EmailAddress)
	if err != nil {
		slog.Error("gmail sync status failed before conversation processing",
			"request_id", requestIDFromContext(r.Context()),
		)
		writeError(w, http.StatusInternalServerError, "mail_sync_status_failed", "Could not read local synchronization status.")
		return
	}
	if currentStatus.Complete && s.conversationProcessor != nil {
		account, accountErr := s.repository.AccountByProviderEmail(r.Context(), "gmail", credential.EmailAddress)
		if accountErr != nil {
			slog.Error("conversation account lookup failed", "request_id", requestIDFromContext(r.Context()))
			writeError(w, http.StatusInternalServerError, "conversation_processing_failed", "Could not process cached conversations.")
			return
		}
		pending, pendingErr := s.conversationProcessor.PendingCount(r.Context(), account.ID)
		if pendingErr != nil {
			slog.Error("pending conversation count failed", "request_id", requestIDFromContext(r.Context()))
			writeError(w, http.StatusInternalServerError, "conversation_processing_failed", "Could not process cached conversations.")
			return
		}
		if pending > 0 {
			result, processErr := s.conversationProcessor.ProcessNext(
				r.Context(), account, credential, s.selectedOllamaModel,
			)
			if processErr != nil {
				slog.Error("cached conversation processing failed",
					"request_id", requestIDFromContext(r.Context()),
					"error", processErr,
				)
				if isGmailRateLimit(processErr) {
					w.Header().Set("Retry-After", "5")
					writeError(w, http.StatusTooManyRequests, "gmail_rate_limited", "Gmail temporarily rate-limited indexing. Wait a moment, then resume.")
					return
				}
				writeError(w, http.StatusBadGateway, "conversation_processing_failed", "Could not process a cached conversation.")
				return
			}
			currentStatus.Phase = "processing"
			currentStatus.Complete = false
			// One final request checks Gmail History after the last cached
			// conversation, so processing responses always keep the loop alive.
			currentStatus.HasMore = true
			currentStatus.PendingConversations = result.Remaining
			if result.Processed {
				currentStatus.ConversationsProcessed = 1
			}
			if result.Created {
				currentStatus.ConversationsCreated = 1
			}
			writeJSON(w, http.StatusOK, currentStatus)
			return
		}
		grouped, groupErr := s.repository.GroupRelatedWorkspaceConversations(r.Context(), account.ID)
		if groupErr != nil {
			slog.Error("workspace conversation grouping failed", "request_id", requestIDFromContext(r.Context()))
			writeError(w, http.StatusInternalServerError, "conversation_grouping_failed", "Could not group cached conversations.")
			return
		}
		if grouped {
			slog.Info("related workspace conversations grouped", "request_id", requestIDFromContext(r.Context()))
			currentStatus.Phase = "processing"
			currentStatus.Complete = false
			currentStatus.HasMore = true
			writeJSON(w, http.StatusOK, currentStatus)
			return
		}
	}
	status, err := s.mailSync.SyncNextPage(r.Context(), credential)
	if err != nil {
		gmailStatus, gmailReason, _ := googleapi.GmailErrorDetails(err)
		slog.Error("gmail synchronization failed",
			"request_id", requestIDFromContext(r.Context()),
			"gmail_status", gmailStatus,
			"gmail_reason", fallbackLogValue(gmailReason, "unknown"),
		)
		writeError(w, http.StatusBadGateway, "mail_sync_failed", "Could not synchronize Gmail.")
		return
	}
	status, err = s.withConversationProcessingStatus(r.Context(), credential.EmailAddress, status)
	if err != nil {
		slog.Error("conversation processing status failed", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusInternalServerError, "conversation_processing_failed", "Could not inspect cached conversations.")
		return
	}
	slog.Info("gmail synchronization advanced",
		"request_id", requestIDFromContext(r.Context()),
		"phase", status.Phase,
		"processed", status.Processed,
		"onboarding_processed", status.OnboardingProcessed,
		"estimated_total", status.EstimatedTotal,
		"messages_cached", status.MessagesCached,
		"complete", status.Complete,
		"has_more", status.HasMore,
	)
	writeJSON(w, http.StatusOK, status)
}

func isGmailRateLimit(err error) bool {
	status, reason, found := googleapi.GmailErrorDetails(err)
	if !found {
		return false
	}
	if status == http.StatusTooManyRequests {
		return true
	}
	if status != http.StatusForbidden {
		return false
	}
	switch reason {
	case "rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded",
		"RATE_LIMIT_EXCEEDED", "USER_RATE_LIMIT_EXCEEDED", "QUOTA_EXCEEDED":
		return true
	default:
		return false
	}
}

func (s *Server) withConversationProcessingStatus(
	ctx context.Context,
	emailAddress string,
	status mailsync.Status,
) (mailsync.Status, error) {
	if !status.Complete || s.conversationProcessor == nil {
		return status, nil
	}
	account, err := s.repository.AccountByProviderEmail(ctx, "gmail", emailAddress)
	if err != nil {
		return mailsync.Status{}, err
	}
	pending, err := s.conversationProcessor.PendingCount(ctx, account.ID)
	if err != nil {
		return mailsync.Status{}, err
	}
	if pending > 0 {
		status.Phase = "processing"
		status.Complete = false
		status.HasMore = true
		status.PendingConversations = pending
	}
	return status, nil
}

func fallbackLogValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
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
	triageModel := s.config.OllamaModel
	message := ""
	if triageModel == "" && len(models) == 1 {
		triageModel = models[0].Name
	} else if triageModel == "" && len(models) > 1 {
		message = "Set OLLAMA_MODEL to choose which installed model performs email triage."
	} else if triageModel != "" {
		found := false
		for _, model := range models {
			found = found || model.Name == triageModel
		}
		if !found {
			message = "The configured OLLAMA_MODEL is not installed."
			triageModel = ""
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":   true,
		"models":      responseModels,
		"triageModel": triageModel,
		"message":     message,
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

	now := time.Now()
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
		s.mu.Lock()
		s.pruneLocked(now)
		expiresAt, found := s.sessions[cookie.Value]
		s.mu.Unlock()
		if found {
			setSessionCookie(w, cookie.Value, expiresAt)
			slog.Info("local session reused", "request_id", requestIDFromContext(r.Context()))
			writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
	}

	sessionID, err := randomURLToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_failed", "Could not create a local session.")
		return
	}
	expiresAt := now.Add(12 * time.Hour)
	s.mu.Lock()
	s.sessions[sessionID] = expiresAt
	s.pruneLocked(now)
	s.mu.Unlock()

	setSessionCookie(w, sessionID, expiresAt)
	slog.Info("local session created", "request_id", requestIDFromContext(r.Context()))
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

	slog.Info("gmail authorization started", "request_id", requestIDFromContext(r.Context()))
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
		slog.Error("gmail authorization failed", "request_id", requestIDFromContext(r.Context()), "stage", "token_exchange")
		s.redirectWithResult(w, r, "error", "token_exchange_failed", pending.sessionID)
		return
	}
	profile, err := s.gmail.Profile(r.Context(), credential)
	if err != nil {
		slog.Error("gmail authorization failed", "request_id", requestIDFromContext(r.Context()), "stage", "profile")
		s.redirectWithResult(w, r, "error", "gmail_profile_failed", pending.sessionID)
		return
	}
	credential.EmailAddress = profile.EmailAddress
	if err := s.store.Save(r.Context(), credential); err != nil {
		slog.Error("gmail authorization failed", "request_id", requestIDFromContext(r.Context()), "stage", "credential_save")
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
		slog.Error("gmail credential unavailable", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusUnauthorized, "gmail_reauthorization_required", "Gmail authorization needs to be renewed.")
		return
	}

	page, err := s.gmail.Inbox(r.Context(), credential, r.URL.Query().Get("limit"), r.URL.Query().Get("pageToken"))
	if err != nil {
		slog.Error("gmail inbox load failed", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusBadGateway, "gmail_request_failed", "Could not retrieve messages from Gmail.")
		return
	}
	slog.Info("gmail inbox loaded",
		"request_id", requestIDFromContext(r.Context()),
		"message_count", len(page.Messages),
		"has_next_page", page.NextPageToken != "",
		"result_size", page.ResultSize,
	)
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleThread(w http.ResponseWriter, r *http.Request) {
	threadID := r.PathValue("threadID")
	if threadID == "" || len(threadID) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_thread_id", "A valid Gmail thread ID is required.")
		return
	}

	credential, err := s.validCredential(r.Context())
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "gmail_not_connected", "Connect Gmail before loading a conversation.")
		return
	}
	if err != nil {
		slog.Error("gmail credential unavailable", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusUnauthorized, "gmail_reauthorization_required", "Gmail authorization needs to be renewed.")
		return
	}

	conversation, err := s.gmail.Thread(r.Context(), credential, threadID)
	if err != nil {
		slog.Error("gmail conversation load failed", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusBadGateway, "gmail_thread_request_failed", "Could not retrieve the conversation from Gmail.")
		return
	}
	slog.Info("gmail conversation loaded",
		"request_id", requestIDFromContext(r.Context()),
		"message_count", len(conversation.Messages),
		"truncated", conversation.Truncated,
	)
	writeJSON(w, http.StatusOK, conversation)
}

type threadTriageResponse struct {
	Conversation googleapi.Conversation `json:"conversation"`
	Triage       triage.Assessment      `json:"triage"`
}

func (s *Server) handleThreadTriage(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	threadID := r.PathValue("threadID")
	if threadID == "" || len(threadID) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_thread_id", "A valid Gmail thread ID is required.")
		return
	}
	credential, err := s.validCredential(r.Context())
	if errors.Is(err, credentials.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "gmail_not_connected", "Connect Gmail before triaging a conversation.")
		return
	}
	if err != nil {
		slog.Error("gmail credential unavailable", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusUnauthorized, "gmail_reauthorization_required", "Gmail authorization needs to be renewed.")
		return
	}
	conversation, err := s.gmail.Thread(r.Context(), credential, threadID)
	if err != nil {
		slog.Error("gmail conversation load failed during triage", "request_id", requestIDFromContext(r.Context()))
		writeError(w, http.StatusBadGateway, "gmail_thread_request_failed", "Could not retrieve the conversation from Gmail.")
		return
	}

	messages := make([]triage.Message, len(conversation.Messages))
	for index, message := range conversation.Messages {
		messages[index] = triage.Message{
			ID:                 message.ID,
			Subject:            message.Subject,
			From:               message.From,
			To:                 message.To,
			Cc:                 message.Cc,
			Date:               message.Date,
			Body:               message.Body,
			LabelIDs:           message.LabelIDs,
			SuspiciousContent:  message.SuspiciousContent,
			HasListUnsubscribe: message.HasListUnsubscribe,
			HasListID:          message.HasListID,
			Precedence:         message.Precedence,
			AutoSubmitted:      message.AutoSubmitted,
			HasFeedbackID:      message.HasFeedbackID,
		}
	}
	assessment := triage.Baseline(messages, credential.EmailAddress)
	aiRequested := assessment.NeedsAI
	if !assessment.NeedsAI {
		slog.Info("gmail triage resolved by rules",
			"request_id", requestIDFromContext(r.Context()),
			"needs_ai", false,
			"message_count", len(messages),
			"visibility", assessment.Visibility,
			"category", assessment.Category,
			"reason_codes", assessment.ReasonCodes,
		)
	}
	if assessment.NeedsAI {
		model, modelErr := s.selectedOllamaModel(r.Context())
		if modelErr != nil {
			assessment.AIStatus = "unavailable"
			slog.Warn("local email triage failed",
				"request_id", requestIDFromContext(r.Context()),
				"stage", string(triage.StageModelSelection),
				"reason", string(observability.ReasonFromError(modelErr, ReasonModelSelectionFailed)),
			)
		} else {
			modelAssessment, inferenceErr := s.triage.Evaluate(r.Context(), model, messages)
			if inferenceErr != nil {
				assessment.AIStatus = "failed"
				stage, reason := triageFailureDetails(inferenceErr)
				slog.Warn("local email triage failed",
					"request_id", requestIDFromContext(r.Context()),
					"stage", string(stage),
					"reason", string(reason),
				)
			} else {
				assessment = triage.ApplyPolicy(assessment, modelAssessment)
			}
		}
	}
	slog.Info("gmail conversation triaged",
		"request_id", requestIDFromContext(r.Context()),
		"message_count", len(messages),
		"visibility", assessment.Visibility,
		"category", assessment.Category,
		"ai_requested", aiRequested,
		"ai_status", assessment.AIStatus,
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)

	writeJSON(w, http.StatusOK, threadTriageResponse{
		Conversation: conversation,
		Triage:       assessment,
	})
}

type modelSelectionError struct {
	reason observability.FailureReason
	err    error
}

const (
	ReasonModelSelectionFailed observability.FailureReason = "model_selection_failed"
	ReasonOllamaUnavailable    observability.FailureReason = "ollama_unavailable"
	ReasonNoModelsInstalled    observability.FailureReason = "no_models_installed"
	ReasonModelNotSelected     observability.FailureReason = "model_not_selected"
)

func (e *modelSelectionError) Error() string {
	return e.err.Error()
}

func (e *modelSelectionError) Unwrap() error {
	return e.err
}

func (e *modelSelectionError) FailureReason() observability.FailureReason {
	return e.reason
}

func newModelSelectionError(fallbackReason observability.FailureReason, err error) error {
	return &modelSelectionError{
		reason: observability.ReasonFromError(err, fallbackReason),
		err:    err,
	}
}

func triageFailureDetails(err error) (stage triage.FailureStage, reason observability.FailureReason) {
	stage = triage.StageUnknown
	reason = triage.ReasonUnknownFailure
	var evaluationError *triage.EvaluationError
	if !errors.As(err, &evaluationError) {
		return stage, reason
	}
	return evaluationError.Stage, evaluationError.Reason
}

func (s *Server) selectedOllamaModel(ctx context.Context) (string, error) {
	if s.config.OllamaModel != "" {
		return s.config.OllamaModel, nil
	}
	models, err := s.ollama.InstalledModels(ctx)
	if err != nil {
		return "", newModelSelectionError(ReasonOllamaUnavailable, err)
	}
	if len(models) == 0 {
		return "", newModelSelectionError(ReasonNoModelsInstalled, fmt.Errorf("no Ollama model is installed"))
	}
	if len(models) > 1 {
		return "", newModelSelectionError(ReasonModelNotSelected, fmt.Errorf("OLLAMA_MODEL is required when multiple models are installed"))
	}
	return models[0].Name, nil
}

func (s *Server) handleGmailDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Delete(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "disconnect_failed", "Could not remove Gmail authorization from Keychain.")
		return
	}
	slog.Info("gmail disconnected", "request_id", requestIDFromContext(r.Context()))
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
	startedAt := time.Now()
	slog.Info("gmail credential refresh started", "request_id", requestIDFromContext(ctx))
	credential, err = s.oauth.Refresh(ctx, credential)
	if err != nil {
		slog.Warn("gmail credential refresh completed",
			"request_id", requestIDFromContext(ctx),
			"outcome", "error",
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
		return credentials.OAuthCredential{}, err
	}
	// Google does not rotate the refresh token in a normal access-token refresh.
	// Keep the short-lived access token in memory so routine refreshes do not
	// trigger another macOS Keychain authorization dialog.
	s.store.Cache(credential)
	slog.Info("gmail credential refresh completed",
		"request_id", requestIDFromContext(ctx),
		"outcome", "success",
		"duration_ms", time.Since(startedAt).Milliseconds(),
	)
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

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	written, err := w.ResponseWriter.Write(body)
	w.bytes += written
	return written, err
}

func (w *responseRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := randomURLToken(12)
		if err != nil {
			requestID = "unavailable"
		}
		w.Header().Set("X-Request-ID", requestID)
		request := r.WithContext(observability.WithRequestID(r.Context(), requestID))
		recorder := &responseRecorder{ResponseWriter: w}
		startedAt := time.Now()

		next.ServeHTTP(recorder, request)

		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		route := request.Pattern
		if route == "" {
			route = "unmatched"
		}
		slog.Info("api request completed",
			"request_id", requestID,
			"method", request.Method,
			"route", route,
			"status", status,
			"response_bytes", recorder.bytes,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
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
	slog.Info("gmail authorization completed",
		"request_id", requestIDFromContext(r.Context()),
		"result", result,
		"code", code,
	)
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

func requestIDFromContext(ctx context.Context) string {
	return observability.RequestID(ctx)
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
