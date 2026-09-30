package conversations

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/googleapi"
	"local-email-workspace/internal/storage"
	storagesqlite "local-email-workspace/internal/storage/sqlite"
	"local-email-workspace/internal/triage"
)

type fakeGmail struct {
	conversation googleapi.Conversation
	calls        int
}

func (f *fakeGmail) Thread(
	context.Context,
	credentials.OAuthCredential,
	string,
) (googleapi.Conversation, error) {
	f.calls++
	return f.conversation, nil
}

type fakeTriage struct {
	assessment triage.Assessment
	calls      int
}

func (f *fakeTriage) Evaluate(
	context.Context,
	string,
	[]triage.Message,
) (triage.Assessment, error) {
	f.calls++
	return f.assessment, nil
}

func TestProcessNextHydratesAndCreatesImportantConversation(t *testing.T) {
	repository, err := storagesqlite.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer repository.Close()
	account, err := repository.EnsureAccount(context.Background(), "gmail", "person@example.com")
	if err != nil {
		t.Fatalf("EnsureAccount() error = %v", err)
	}
	if _, err := repository.UpsertMessage(context.Background(), storage.Message{
		AccountID:         account.ID,
		ProviderMessageID: "message-1",
		ProviderThreadID:  "thread-1",
		Subject:           "Approval needed",
		From:              "sender@example.com",
		To:                "person@example.com",
		Snippet:           "Could you take a look?",
		InternalAt:        time.UnixMilli(1000),
		LabelIDs:          []string{"INBOX", "IMPORTANT"},
	}); err != nil {
		t.Fatalf("UpsertMessage() error = %v", err)
	}

	gmail := &fakeGmail{conversation: googleapi.Conversation{
		ID: "thread-1",
		Messages: []googleapi.ConversationMessage{{
			ID: "message-1", ThreadID: "thread-1", Subject: "Approval needed",
			From: "sender@example.com", To: "person@example.com", Body: "Please approve the proposal by Friday.",
			LabelIDs: []string{"INBOX", "IMPORTANT"}, SuspiciousContent: true, HasListID: true,
		}},
	}}
	evaluator := &fakeTriage{assessment: triage.Assessment{
		Visibility:       triage.VisibilityActive,
		Category:         "action_required",
		NeedsAction:      true,
		Confidence:       0.95,
		ReasonCodes:      []string{"direct_request", "deadline"},
		SourceMessageIDs: []string{"message-1"},
		AIStatus:         "applied",
	}}
	service := New(repository, gmail, evaluator)
	result, err := service.ProcessNext(
		context.Background(), account, credentials.OAuthCredential{},
		func(context.Context) (string, error) { return "test-model", nil },
	)
	if err != nil {
		t.Fatalf("ProcessNext() error = %v", err)
	}
	if !result.Processed || !result.Created || result.Visibility != triage.VisibilityActive || result.Remaining != 0 {
		t.Fatalf("ProcessNext() = %#v", result)
	}
	if gmail.calls != 1 || evaluator.calls != 1 {
		t.Fatalf("calls: Gmail = %d, evaluator = %d", gmail.calls, evaluator.calls)
	}
	message, err := repository.MessageByProviderID(context.Background(), account.ID, "message-1")
	if err != nil {
		t.Fatalf("MessageByProviderID() error = %v", err)
	}
	if message.Body != "Please approve the proposal by Friday." || message.BodyHash == "" ||
		message.BodyState != storage.BodyHydrated || message.Importance != storage.ImportanceImportant ||
		!message.SuspiciousContent || !message.HasListID {
		t.Fatalf("hydrated message = %#v", message)
	}
	workspaceConversations, counts, err := repository.WorkspaceConversations(context.Background(), "active")
	if err != nil {
		t.Fatalf("WorkspaceConversations() error = %v", err)
	}
	if len(workspaceConversations) != 1 || !workspaceConversations[0].NeedsAttention || counts.Attention != 1 {
		t.Fatalf("workspace conversations = %#v, counts = %#v", workspaceConversations, counts)
	}

	if _, err := repository.UpsertMessage(context.Background(), storage.Message{
		AccountID:         account.ID,
		ProviderMessageID: "message-2",
		ProviderThreadID:  "thread-1",
		Subject:           "Re: Approval needed",
		From:              "sender@example.com",
		To:                "person@example.com",
		Snippet:           "Following up.",
		InternalAt:        time.UnixMilli(2000),
		LabelIDs:          []string{"INBOX"},
	}); err != nil {
		t.Fatalf("second UpsertMessage() error = %v", err)
	}
	gmail.conversation.Messages = append(gmail.conversation.Messages, googleapi.ConversationMessage{
		ID: "message-2", ThreadID: "thread-1", Subject: "Re: Approval needed",
		From: "sender@example.com", To: "person@example.com", Body: "Following up on the approval request.",
		LabelIDs: []string{"INBOX"},
	})
	result, err = service.ProcessNext(
		context.Background(), account, credentials.OAuthCredential{},
		func(context.Context) (string, error) { return "test-model", nil },
	)
	if err != nil {
		t.Fatalf("second ProcessNext() error = %v", err)
	}
	if result.Created || result.Remaining != 0 {
		t.Fatalf("second ProcessNext() = %#v", result)
	}
	secondMessage, err := repository.MessageByProviderID(context.Background(), account.ID, "message-2")
	if err != nil {
		t.Fatalf("second MessageByProviderID() error = %v", err)
	}
	if secondMessage.BodyState != storage.BodyHydrated || secondMessage.Importance != storage.ImportanceImportant {
		t.Fatalf("second hydrated message = %#v", secondMessage)
	}
}

func TestProcessNextSkipsHydrationForRuleResolvedBulkMail(t *testing.T) {
	repository, err := storagesqlite.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer repository.Close()
	account, err := repository.EnsureAccount(context.Background(), "gmail", "person@example.com")
	if err != nil {
		t.Fatalf("EnsureAccount() error = %v", err)
	}
	if _, err := repository.UpsertMessage(context.Background(), storage.Message{
		AccountID:         account.ID,
		ProviderMessageID: "message-ad",
		ProviderThreadID:  "thread-ad",
		Subject:           "Weekly deals",
		From:              "offers@example.com",
		To:                "person@example.com",
		Snippet:           "Buy now and save.",
		InternalAt:        time.UnixMilli(1000),
		LabelIDs:          []string{"INBOX", "CATEGORY_PROMOTIONS"},
	}); err != nil {
		t.Fatalf("UpsertMessage() error = %v", err)
	}

	gmail := &fakeGmail{}
	evaluator := &fakeTriage{}
	service := New(repository, gmail, evaluator)
	result, err := service.ProcessNext(
		context.Background(), account, credentials.OAuthCredential{},
		func(context.Context) (string, error) { return "test-model", nil },
	)
	if err != nil {
		t.Fatalf("ProcessNext() error = %v", err)
	}
	if result.Created || gmail.calls != 0 || evaluator.calls != 0 {
		t.Fatalf("result = %#v, Gmail calls = %d, evaluator calls = %d", result, gmail.calls, evaluator.calls)
	}
	message, err := repository.MessageByProviderID(context.Background(), account.ID, "message-ad")
	if err != nil {
		t.Fatalf("MessageByProviderID() error = %v", err)
	}
	if message.Importance != storage.ImportanceLowValue || message.BodyState != storage.BodyMetadataOnly {
		t.Fatalf("bulk message = %#v", message)
	}
}
