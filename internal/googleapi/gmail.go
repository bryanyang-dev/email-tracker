package googleapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/mailbody"
)

const gmailAPIBaseURL = "https://gmail.googleapis.com/gmail/v1/users/me"

const (
	maxThreadResponseBytes   = 16 << 20
	maxDecodedTextPartBytes  = 2 << 20
	maxConversationTextBytes = 8 << 20
	maxThreadMessages        = 200
	maxDecodedMIMEParts      = 512
	maxDecodedMIMEDepth      = 32
)

type GmailClient struct {
	httpClient             *http.Client
	retryBaseDelay         time.Duration
	minimumRequestInterval time.Duration
	requestSerial          sync.Mutex
	nextRequestAt          time.Time
}

type GmailAPIError struct {
	StatusCode int
	Reason     string
	RetryAfter time.Duration
}

func (e *GmailAPIError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("Gmail API returned HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("Gmail API returned HTTP %d (%s)", e.StatusCode, e.Reason)
}

func (e *GmailAPIError) Retryable() bool {
	if e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError {
		return true
	}
	if e.StatusCode != http.StatusForbidden {
		return false
	}
	switch e.Reason {
	case "rateLimitExceeded", "userRateLimitExceeded", "quotaExceeded", "backendError",
		"RATE_LIMIT_EXCEEDED", "USER_RATE_LIMIT_EXCEEDED", "QUOTA_EXCEEDED":
		return true
	default:
		return false
	}
}

func GmailErrorDetails(err error) (statusCode int, reason string, found bool) {
	var apiError *GmailAPIError
	if !errors.As(err, &apiError) {
		return 0, "", false
	}
	return apiError.StatusCode, apiError.Reason, true
}

type Profile struct {
	EmailAddress  string `json:"emailAddress"`
	MessagesTotal int64  `json:"messagesTotal"`
	ThreadsTotal  int64  `json:"threadsTotal"`
	HistoryID     string `json:"historyId"`
}

type InboxMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	RFCMessageID string   `json:"rfcMessageId,omitempty"`
	InReplyTo    string   `json:"inReplyTo,omitempty"`
	References   []string `json:"references,omitempty"`
	Subject      string   `json:"subject"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	Cc           string   `json:"cc,omitempty"`
	Date         string   `json:"date"`
	Snippet      string   `json:"snippet"`
	Unread       bool     `json:"unread"`
	LabelIDs     []string `json:"labelIds"`
	InternalAt   string   `json:"internalAt"`
}

type InboxPage struct {
	Messages      []InboxMessage `json:"messages"`
	NextPageToken string         `json:"nextPageToken,omitempty"`
	ResultSize    int            `json:"resultSize"`
}

type HistoryPage struct {
	NewMessages     []MessageReference
	ChangedMessages []MessageReference
	DeletedMessages []MessageReference
	NextPageToken   string
	HistoryID       string
}

type MessageReference struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}

type Conversation struct {
	ID        string                `json:"id"`
	HistoryID string                `json:"historyId"`
	Messages  []ConversationMessage `json:"messages"`
	Truncated bool                  `json:"truncated"`
}

type ConversationMessage struct {
	ID                 string   `json:"id"`
	ThreadID           string   `json:"threadId"`
	RFCMessageID       string   `json:"rfcMessageId,omitempty"`
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

type messageListResponse struct {
	Messages           []MessageReference `json:"messages"`
	NextPageToken      string             `json:"nextPageToken"`
	ResultSizeEstimate int                `json:"resultSizeEstimate"`
}

type historyResponse struct {
	History []struct {
		MessagesAdded []struct {
			Message MessageReference `json:"message"`
		} `json:"messagesAdded"`
		MessagesDeleted []struct {
			Message MessageReference `json:"message"`
		} `json:"messagesDeleted"`
		LabelsAdded []struct {
			Message MessageReference `json:"message"`
		} `json:"labelsAdded"`
		LabelsRemoved []struct {
			Message MessageReference `json:"message"`
		} `json:"labelsRemoved"`
	} `json:"history"`
	NextPageToken string `json:"nextPageToken"`
	HistoryID     string `json:"historyId"`
}

type messageHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type messagePartBody struct {
	AttachmentID string `json:"attachmentId"`
	Size         int    `json:"size"`
	Data         string `json:"data"`
}

type messagePart struct {
	PartID   string          `json:"partId"`
	MIMEType string          `json:"mimeType"`
	Filename string          `json:"filename"`
	Headers  []messageHeader `json:"headers"`
	Body     messagePartBody `json:"body"`
	Parts    []messagePart   `json:"parts"`
}

type messageResponse struct {
	ID           string      `json:"id"`
	ThreadID     string      `json:"threadId"`
	LabelIDs     []string    `json:"labelIds"`
	Snippet      string      `json:"snippet"`
	InternalDate string      `json:"internalDate"`
	Payload      messagePart `json:"payload"`
}

type threadResponse struct {
	ID        string            `json:"id"`
	HistoryID string            `json:"historyId"`
	Messages  []messageResponse `json:"messages"`
}

func NewGmailClient(httpClient *http.Client) *GmailClient {
	return &GmailClient{
		httpClient:             httpClient,
		retryBaseDelay:         time.Second,
		minimumRequestInterval: 250 * time.Millisecond,
	}
}

func (c *GmailClient) Profile(ctx context.Context, credential credentials.OAuthCredential) (Profile, error) {
	var profile Profile
	if err := c.getJSON(ctx, gmailAPIBaseURL+"/profile", credential, &profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func (c *GmailClient) Inbox(ctx context.Context, credential credentials.OAuthCredential, rawLimit, pageToken string) (InboxPage, error) {
	query := url.Values{
		"labelIds":   {"INBOX"},
		"maxResults": {strconv.Itoa(clampLimit(rawLimit))},
	}
	return c.messageMetadataPage(ctx, credential, query, pageToken)
}

func (c *GmailClient) Recent(
	ctx context.Context,
	credential credentials.OAuthCredential,
	rawLimit, pageToken, searchQuery string,
) (InboxPage, error) {
	query := url.Values{
		"maxResults": {strconv.Itoa(clampLimit(rawLimit))},
		"q":          {searchQuery},
	}
	return c.messageMetadataPage(ctx, credential, query, pageToken)
}

func (c *GmailClient) messageMetadataPage(
	ctx context.Context,
	credential credentials.OAuthCredential,
	query url.Values,
	pageToken string,
) (InboxPage, error) {
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}

	var listing messageListResponse
	if err := c.getJSON(ctx, gmailAPIBaseURL+"/messages?"+query.Encode(), credential, &listing); err != nil {
		return InboxPage{}, err
	}

	messages := make([]InboxMessage, len(listing.Messages))
	for index, reference := range listing.Messages {
		if err := ctx.Err(); err != nil {
			return InboxPage{}, err
		}
		message, err := c.messageWithRetry(ctx, credential, reference.ID)
		if err != nil {
			return InboxPage{}, fmt.Errorf("fetch inbox message metadata: %w", err)
		}
		messages[index] = message
	}

	return InboxPage{
		Messages:      messages,
		NextPageToken: listing.NextPageToken,
		ResultSize:    listing.ResultSizeEstimate,
	}, nil
}

func (c *GmailClient) History(
	ctx context.Context,
	credential credentials.OAuthCredential,
	startHistoryID, pageToken string,
) (HistoryPage, error) {
	query := url.Values{
		"startHistoryId": {startHistoryID},
		"maxResults":     {"20"},
	}
	for _, historyType := range []string{"messageAdded", "messageDeleted", "labelAdded", "labelRemoved"} {
		query.Add("historyTypes", historyType)
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	var response historyResponse
	if err := c.getJSON(ctx, gmailAPIBaseURL+"/history?"+query.Encode(), credential, &response); err != nil {
		return HistoryPage{}, err
	}

	added := make(map[string]MessageReference)
	changed := make(map[string]MessageReference)
	deleted := make(map[string]MessageReference)
	for _, record := range response.History {
		for _, addition := range record.MessagesAdded {
			added[addition.Message.ID] = addition.Message
		}
		for _, labels := range record.LabelsAdded {
			changed[labels.Message.ID] = labels.Message
		}
		for _, labels := range record.LabelsRemoved {
			changed[labels.Message.ID] = labels.Message
		}
		for _, deletion := range record.MessagesDeleted {
			delete(added, deletion.Message.ID)
			delete(changed, deletion.Message.ID)
			deleted[deletion.Message.ID] = deletion.Message
		}
	}
	newMessages := make([]MessageReference, 0, len(added))
	for _, reference := range added {
		if reference.ID != "" {
			newMessages = append(newMessages, reference)
		}
	}
	changedMessages := make([]MessageReference, 0, len(changed))
	for _, reference := range changed {
		if reference.ID != "" {
			changedMessages = append(changedMessages, reference)
		}
	}
	deletedMessages := make([]MessageReference, 0, len(deleted))
	for _, reference := range deleted {
		if reference.ID != "" {
			deletedMessages = append(deletedMessages, reference)
		}
	}
	return HistoryPage{
		NewMessages:     newMessages,
		ChangedMessages: changedMessages,
		DeletedMessages: deletedMessages,
		NextPageToken:   response.NextPageToken,
		HistoryID:       response.HistoryID,
	}, nil
}

func (c *GmailClient) Metadata(
	ctx context.Context,
	credential credentials.OAuthCredential,
	messageID string,
) (InboxMessage, error) {
	return c.messageWithRetry(ctx, credential, messageID)
}

func (c *GmailClient) messageWithRetry(
	ctx context.Context,
	credential credentials.OAuthCredential,
	id string,
) (InboxMessage, error) {
	const maximumAttempts = 3
	for attempt := 0; attempt < maximumAttempts; attempt++ {
		message, err := c.message(ctx, credential, id)
		if err == nil {
			return message, nil
		}
		var apiError *GmailAPIError
		if !errors.As(err, &apiError) || !apiError.Retryable() || attempt == maximumAttempts-1 {
			return InboxMessage{}, err
		}
		delay := c.retryBaseDelay * time.Duration(1<<attempt)
		if apiError.RetryAfter > delay {
			delay = apiError.RetryAfter
		}
		delay = min(delay, 5*time.Second)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return InboxMessage{}, ctx.Err()
		}
	}
	return InboxMessage{}, fmt.Errorf("Gmail metadata retry exhausted")
}

func (c *GmailClient) Thread(ctx context.Context, credential credentials.OAuthCredential, threadID string) (Conversation, error) {
	query := url.Values{"format": {"full"}}
	endpoint := gmailAPIBaseURL + "/threads/" + url.PathEscape(threadID) + "?" + query.Encode()

	var response threadResponse
	if err := c.getJSONLimit(ctx, endpoint, credential, &response, maxThreadResponseBytes); err != nil {
		return Conversation{}, err
	}

	providerMessages := response.Messages
	truncated := len(providerMessages) > maxThreadMessages
	if truncated {
		providerMessages = providerMessages[len(providerMessages)-maxThreadMessages:]
	}

	messages := make([]ConversationMessage, 0, len(providerMessages))
	decodedTextBytes := 0
	for _, providerMessage := range providerMessages {
		normalized, err := c.normalizeConversationMessage(ctx, credential, providerMessage, &decodedTextBytes)
		if err != nil {
			return Conversation{}, fmt.Errorf("normalize Gmail message %s: %w", providerMessage.ID, err)
		}
		messages = append(messages, normalized)
	}
	sort.SliceStable(messages, func(left, right int) bool {
		leftTime, _ := strconv.ParseInt(messages[left].InternalAt, 10, 64)
		rightTime, _ := strconv.ParseInt(messages[right].InternalAt, 10, 64)
		return leftTime < rightTime
	})

	return Conversation{
		ID:        response.ID,
		HistoryID: response.HistoryID,
		Messages:  messages,
		Truncated: truncated,
	}, nil
}

func (c *GmailClient) normalizeConversationMessage(
	ctx context.Context,
	credential credentials.OAuthCredential,
	message messageResponse,
	decodedTextBytes *int,
) (ConversationMessage, error) {
	partCount := 0
	part, err := c.decodeMessagePart(ctx, credential, message.ID, message.Payload, 0, &partCount, decodedTextBytes)
	if err != nil {
		return ConversationMessage{}, err
	}
	result := mailbody.Normalize(part)
	headers := headerValues(message.Payload.Headers)
	precedence := strings.ToLower(strings.TrimSpace(headers["precedence"]))
	if precedence != "bulk" && precedence != "list" && precedence != "junk" {
		precedence = ""
	}
	autoSubmitted := strings.ToLower(strings.TrimSpace(headers["auto-submitted"]))

	return ConversationMessage{
		ID:                 message.ID,
		ThreadID:           message.ThreadID,
		RFCMessageID:       headers["message-id"],
		Subject:            fallback(headers["subject"], "(No subject)"),
		From:               headers["from"],
		To:                 headers["to"],
		Cc:                 headers["cc"],
		Date:               headers["date"],
		InternalAt:         message.InternalDate,
		LabelIDs:           message.LabelIDs,
		Body:               result.Text,
		BodySource:         fallback(result.Source, "none"),
		BodyTruncated:      result.Truncated,
		SuspiciousContent:  result.SuspiciousContent,
		HasListUnsubscribe: headers["list-unsubscribe"] != "",
		HasListID:          headers["list-id"] != "",
		Precedence:         precedence,
		AutoSubmitted:      autoSubmitted != "" && autoSubmitted != "no",
		HasFeedbackID:      headers["feedback-id"] != "",
	}, nil
}

func (c *GmailClient) decodeMessagePart(
	ctx context.Context,
	credential credentials.OAuthCredential,
	messageID string,
	part messagePart,
	depth int,
	partCount *int,
	decodedTextBytes *int,
) (mailbody.Part, error) {
	*partCount++
	if depth > maxDecodedMIMEDepth || *partCount > maxDecodedMIMEParts {
		return mailbody.Part{MIMEType: part.MIMEType, Truncated: true}, nil
	}
	disposition := headerValues(part.Headers)["content-disposition"]
	normalized := mailbody.Part{
		MIMEType:    part.MIMEType,
		Filename:    part.Filename,
		Disposition: disposition,
		Parts:       make([]mailbody.Part, 0, len(part.Parts)),
	}

	isAttachment := part.Filename != "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(disposition)), "attachment")
	isText := strings.HasPrefix(strings.ToLower(strings.TrimSpace(part.MIMEType)), "text/")
	if !isAttachment && isText {
		remaining := maxConversationTextBytes - *decodedTextBytes
		if remaining <= 0 {
			normalized.Truncated = true
		} else {
			encoded := part.Body.Data
			if encoded == "" && part.Body.AttachmentID != "" {
				var body messagePartBody
				endpoint := gmailAPIBaseURL + "/messages/" + url.PathEscape(messageID) +
					"/attachments/" + url.PathEscape(part.Body.AttachmentID)
				if err := c.getJSONLimit(ctx, endpoint, credential, &body, 3<<20); err != nil {
					return mailbody.Part{}, fmt.Errorf("fetch external text body: %w", err)
				}
				encoded = body.Data
			}
			partLimit := min(maxDecodedTextPartBytes, remaining)
			decoded, truncated, err := decodeBase64URL(encoded, partLimit)
			if err != nil {
				return mailbody.Part{}, fmt.Errorf("decode %s body: %w", part.MIMEType, err)
			}
			normalized.Data = decoded
			normalized.Truncated = truncated
			*decodedTextBytes += len(decoded)
		}
	}

	for _, child := range part.Parts {
		decoded, err := c.decodeMessagePart(ctx, credential, messageID, child, depth+1, partCount, decodedTextBytes)
		if err != nil {
			return mailbody.Part{}, err
		}
		normalized.Parts = append(normalized.Parts, decoded)
	}
	return normalized, nil
}

func decodeBase64URL(encoded string, limit int) ([]byte, bool, error) {
	if encoded == "" {
		return nil, false, nil
	}
	encodedLimit := base64.RawURLEncoding.EncodedLen(limit)
	truncated := len(encoded) > encodedLimit
	if truncated {
		encoded = encoded[:encodedLimit]
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, false, err
	}
	if len(decoded) > limit {
		decoded = decoded[:limit]
		truncated = true
	}
	return decoded, truncated, nil
}

func (c *GmailClient) message(ctx context.Context, credential credentials.OAuthCredential, id string) (InboxMessage, error) {
	query := url.Values{"format": {"metadata"}}
	for _, header := range []string{"Subject", "From", "To", "Cc", "Date", "Message-ID", "In-Reply-To", "References"} {
		query.Add("metadataHeaders", header)
	}

	var response messageResponse
	endpoint := gmailAPIBaseURL + "/messages/" + url.PathEscape(id) + "?" + query.Encode()
	if err := c.getJSON(ctx, endpoint, credential, &response); err != nil {
		return InboxMessage{}, err
	}

	headers := headerValues(response.Payload.Headers)

	return InboxMessage{
		ID:           response.ID,
		ThreadID:     response.ThreadID,
		RFCMessageID: headers["message-id"],
		InReplyTo:    headers["in-reply-to"],
		References:   strings.Fields(headers["references"]),
		Subject:      fallback(headers["subject"], "(No subject)"),
		From:         headers["from"],
		To:           headers["to"],
		Cc:           headers["cc"],
		Date:         headers["date"],
		Snippet:      response.Snippet,
		Unread:       contains(response.LabelIDs, "UNREAD"),
		LabelIDs:     response.LabelIDs,
		InternalAt:   response.InternalDate,
	}, nil
}

func (c *GmailClient) getJSON(ctx context.Context, endpoint string, credential credentials.OAuthCredential, target any) error {
	return c.getJSONLimit(ctx, endpoint, credential, target, 4<<20)
}

func (c *GmailClient) getJSONLimit(
	ctx context.Context,
	endpoint string,
	credential credentials.OAuthCredential,
	target any,
	responseLimit int64,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create Gmail request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Accept", "application/json")
	if err := c.beginRequest(ctx); err != nil {
		return fmt.Errorf("wait for Gmail request slot: %w", err)
	}
	cooldown := time.Duration(0)
	defer func() { c.finishRequest(cooldown) }()

	startedAt := time.Now()
	operation := gmailOperation(endpoint)
	response, err := c.httpClient.Do(request)
	if err != nil {
		logOutboundCall(ctx, "gmail", operation, http.MethodGet, 0, "error", startedAt)
		return fmt.Errorf("call Gmail API: %w", err)
	}
	outcome := "success"
	defer func() {
		response.Body.Close()
		logOutboundCall(ctx, "gmail", operation, http.MethodGet, response.StatusCode, outcome, startedAt)
	}()

	if response.StatusCode != http.StatusOK {
		outcome = "error"
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		apiError := parseGmailAPIError(response, body)
		if apiError.Retryable() {
			cooldown = max(apiError.RetryAfter, c.retryBaseDelay)
		}
		return apiError
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, responseLimit)).Decode(target); err != nil {
		outcome = "error"
		return fmt.Errorf("decode Gmail response: %w", err)
	}
	return nil
}

func (c *GmailClient) beginRequest(ctx context.Context) error {
	c.requestSerial.Lock()
	delay := time.Until(c.nextRequestAt)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		c.requestSerial.Unlock()
		return ctx.Err()
	}
}

func (c *GmailClient) finishRequest(cooldown time.Duration) {
	c.nextRequestAt = time.Now().Add(max(c.minimumRequestInterval, cooldown))
	c.requestSerial.Unlock()
}

func parseGmailAPIError(response *http.Response, body []byte) *GmailAPIError {
	var envelope struct {
		Error struct {
			Status string `json:"status"`
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	reason := ""
	if len(envelope.Error.Errors) > 0 {
		reason = envelope.Error.Errors[0].Reason
	}
	if reason == "" && len(envelope.Error.Details) > 0 {
		reason = envelope.Error.Details[0].Reason
	}
	if reason == "" {
		reason = envelope.Error.Status
	}
	retryAfter := time.Duration(0)
	if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
		retryAfter = time.Duration(seconds) * time.Second
	} else if retryAt, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
		retryAfter = max(time.Until(retryAt), 0)
	}
	return &GmailAPIError{
		StatusCode: response.StatusCode,
		Reason:     reason,
		RetryAfter: retryAfter,
	}
}

func gmailOperation(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "unknown"
	}
	path := strings.TrimPrefix(parsed.Path, "/gmail/v1/users/me/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(parts) == 1 && parts[0] == "profile":
		return "get_profile"
	case len(parts) == 1 && parts[0] == "messages":
		return "list_messages"
	case len(parts) == 1 && parts[0] == "history":
		return "list_history"
	case len(parts) == 2 && parts[0] == "messages":
		return "get_message"
	case len(parts) == 4 && parts[0] == "messages" && parts[2] == "attachments":
		return "get_attachment"
	case len(parts) == 2 && parts[0] == "threads":
		return "get_thread"
	default:
		return "unknown"
	}
}

func headerValues(headers []messageHeader) map[string]string {
	values := make(map[string]string, len(headers))
	for _, header := range headers {
		values[strings.ToLower(header.Name)] = header.Value
	}
	return values
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func fallback(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
