package googleapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	httpClient *http.Client
}

type Profile struct {
	EmailAddress  string `json:"emailAddress"`
	MessagesTotal int64  `json:"messagesTotal"`
	ThreadsTotal  int64  `json:"threadsTotal"`
	HistoryID     string `json:"historyId"`
}

type InboxMessage struct {
	ID         string   `json:"id"`
	ThreadID   string   `json:"threadId"`
	Subject    string   `json:"subject"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	Date       string   `json:"date"`
	Snippet    string   `json:"snippet"`
	Unread     bool     `json:"unread"`
	LabelIDs   []string `json:"labelIds"`
	InternalAt string   `json:"internalAt"`
}

type InboxPage struct {
	Messages      []InboxMessage `json:"messages"`
	NextPageToken string         `json:"nextPageToken,omitempty"`
	ResultSize    int            `json:"resultSize"`
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
	Messages []struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"messages"`
	NextPageToken      string `json:"nextPageToken"`
	ResultSizeEstimate int    `json:"resultSizeEstimate"`
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
	return &GmailClient{httpClient: httpClient}
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
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}

	var listing messageListResponse
	if err := c.getJSON(ctx, gmailAPIBaseURL+"/messages?"+query.Encode(), credential, &listing); err != nil {
		return InboxPage{}, err
	}

	messages := make([]InboxMessage, len(listing.Messages))
	var waitGroup sync.WaitGroup
	semaphore := make(chan struct{}, 6)
	errorsByIndex := make([]error, len(listing.Messages))

	for index, reference := range listing.Messages {
		index, reference := index, reference
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				errorsByIndex[index] = ctx.Err()
				return
			}
			message, err := c.message(ctx, credential, reference.ID)
			messages[index] = message
			errorsByIndex[index] = err
		}()
	}
	waitGroup.Wait()

	for _, err := range errorsByIndex {
		if err != nil {
			return InboxPage{}, fmt.Errorf("fetch inbox message metadata: %w", err)
		}
	}

	return InboxPage{
		Messages:      messages,
		NextPageToken: listing.NextPageToken,
		ResultSize:    listing.ResultSizeEstimate,
	}, nil
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
	for _, header := range []string{"Subject", "From", "To", "Date", "Message-ID", "In-Reply-To", "References"} {
		query.Add("metadataHeaders", header)
	}

	var response messageResponse
	endpoint := gmailAPIBaseURL + "/messages/" + url.PathEscape(id) + "?" + query.Encode()
	if err := c.getJSON(ctx, endpoint, credential, &response); err != nil {
		return InboxMessage{}, err
	}

	headers := headerValues(response.Payload.Headers)

	return InboxMessage{
		ID:         response.ID,
		ThreadID:   response.ThreadID,
		Subject:    fallback(headers["subject"], "(No subject)"),
		From:       headers["from"],
		To:         headers["to"],
		Date:       headers["date"],
		Snippet:    response.Snippet,
		Unread:     contains(response.LabelIDs, "UNREAD"),
		LabelIDs:   response.LabelIDs,
		InternalAt: response.InternalDate,
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
		return fmt.Errorf("Gmail API returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, responseLimit)).Decode(target); err != nil {
		outcome = "error"
		return fmt.Errorf("decode Gmail response: %w", err)
	}
	return nil
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
