package googleapi

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"local-email-workspace/internal/credentials"
)

func TestThreadRetrievesAndNormalizesConversationBodies(t *testing.T) {
	plainBody := base64.RawURLEncoding.EncodeToString([]byte("Plain body\r\n\r\nPlease approve by Friday."))
	htmlBody := base64.RawURLEncoding.EncodeToString([]byte(`<p>Earlier update</p><script>ignore()</script>`))
	httpClient := &http.Client{Transport: gmailRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/gmail/v1/users/me/threads/thread-1" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("format") != "full" {
			t.Fatalf("format = %q, want full", request.URL.Query().Get("format"))
		}
		if request.Header.Get("Authorization") != "Bearer access-token" {
			t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
		}
		return gmailJSONResponse(`{
			"id": "thread-1",
			"historyId": "history-1",
			"messages": [
				{
					"id": "message-2",
					"threadId": "thread-1",
					"internalDate": "200",
					"labelIds": ["INBOX"],
					"payload": {
						"mimeType": "multipart/alternative",
						"headers": [
							{"name": "Subject", "value": "Approval request"},
							{"name": "From", "value": "Morgan <morgan@example.com>"},
							{"name": "To", "value": "person@example.com"},
							{"name": "List-Unsubscribe", "value": "<https://example.com/unsubscribe>"},
							{"name": "List-ID", "value": "campaign.example.com"},
							{"name": "Precedence", "value": "bulk"},
							{"name": "Auto-Submitted", "value": "auto-generated"},
							{"name": "Feedback-ID", "value": "campaign:example"}
						],
						"parts": [
							{"mimeType": "text/html", "body": {"data": "` + htmlBody + `"}},
							{"mimeType": "text/plain", "body": {"data": "` + plainBody + `"}}
						]
					}
				},
				{
					"id": "message-1",
					"threadId": "thread-1",
					"internalDate": "100",
					"payload": {
						"mimeType": "text/html",
						"headers": [{"name": "Subject", "value": "Approval request"}],
						"body": {"data": "` + htmlBody + `"}
					}
				}
			]
		}`), nil
	})}
	client := NewGmailClient(httpClient)

	conversation, err := client.Thread(context.Background(), credentials.OAuthCredential{AccessToken: "access-token"}, "thread-1")
	if err != nil {
		t.Fatalf("Thread() error = %v", err)
	}
	if len(conversation.Messages) != 2 {
		t.Fatalf("messages = %#v", conversation.Messages)
	}
	if conversation.Messages[0].ID != "message-1" || conversation.Messages[0].Body != "Earlier update" {
		t.Fatalf("first message = %#v", conversation.Messages[0])
	}
	if conversation.Messages[1].Body != "Plain body\n\nPlease approve by Friday." || conversation.Messages[1].BodySource != "plain" {
		t.Fatalf("second message = %#v", conversation.Messages[1])
	}
	if !conversation.Messages[1].HasListUnsubscribe || !conversation.Messages[1].HasListID ||
		conversation.Messages[1].Precedence != "bulk" || !conversation.Messages[1].AutoSubmitted ||
		!conversation.Messages[1].HasFeedbackID {
		t.Fatalf("bulk indicators = %#v", conversation.Messages[1])
	}
}

func TestThreadRetrievesExternalTextBody(t *testing.T) {
	var attachmentRequested bool
	httpClient := &http.Client{Transport: gmailRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/gmail/v1/users/me/threads/thread-1":
			return gmailJSONResponse(`{
				"id": "thread-1",
				"messages": [{
					"id": "message-1",
					"threadId": "thread-1",
					"payload": {
						"mimeType": "text/plain",
						"body": {"attachmentId": "body-1"}
					}
				}]
			}`), nil
		case "/gmail/v1/users/me/messages/message-1/attachments/body-1":
			attachmentRequested = true
			data := base64.RawURLEncoding.EncodeToString([]byte("External body"))
			return gmailJSONResponse(`{"data":"` + data + `"}`), nil
		default:
			t.Fatalf("unexpected path %q", request.URL.Path)
			return nil, nil
		}
	})}
	client := NewGmailClient(httpClient)

	conversation, err := client.Thread(context.Background(), credentials.OAuthCredential{AccessToken: "token"}, "thread-1")
	if err != nil {
		t.Fatalf("Thread() error = %v", err)
	}
	if !attachmentRequested || conversation.Messages[0].Body != "External body" {
		t.Fatalf("conversation = %#v, attachmentRequested = %v", conversation, attachmentRequested)
	}
}

func TestDecodeBase64URLTruncatesAtByteLimit(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("abcdef"))

	decoded, truncated, err := decodeBase64URL(encoded, 4)
	if err != nil {
		t.Fatalf("decodeBase64URL() error = %v", err)
	}
	if string(decoded) != "abcd" || !truncated {
		t.Fatalf("decoded = %q, truncated = %v", decoded, truncated)
	}
}

type gmailRoundTripFunc func(*http.Request) (*http.Response, error)

func (function gmailRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func gmailJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    &http.Request{},
	}
}
