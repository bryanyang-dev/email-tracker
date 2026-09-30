package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"local-email-workspace/internal/storage"
)

func TestOpenConfiguresAndMigratesDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "workspace.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	for pragma, want := range map[string]string{
		"foreign_keys": "1",
		"journal_mode": "wal",
	} {
		var got string
		if err := store.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s error = %v", pragma, err)
		}
		if got != want {
			t.Fatalf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}

	var migrationCount int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("read schema migrations: %v", err)
	}
	if migrationCount != 6 {
		t.Fatalf("migration count = %d, want 6", migrationCount)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database permissions = %o, want 600", got)
	}
}

func TestRepositoryPersistsIdempotentMailboxState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	account, err := store.EnsureAccount(ctx, "gmail", "Person@Example.com")
	if err != nil {
		t.Fatalf("EnsureAccount() error = %v", err)
	}
	repeatedAccount, err := store.EnsureAccount(ctx, "gmail", "person@example.com")
	if err != nil {
		t.Fatalf("repeated EnsureAccount() error = %v", err)
	}
	if repeatedAccount.ID != account.ID {
		t.Fatalf("account ID changed: %q != %q", repeatedAccount.ID, account.ID)
	}

	cursor := storage.SyncCursor{
		AccountID:           account.ID,
		HistoryID:           "history-20",
		InitialPageToken:    "page-2",
		InitialSyncComplete: false,
		OnboardingProcessed: 20,
		EstimatedTotal:      42,
	}
	if err := store.SaveSyncCursor(ctx, cursor); err != nil {
		t.Fatalf("SaveSyncCursor() error = %v", err)
	}
	gotCursor, err := store.SyncCursor(ctx, account.ID)
	if err != nil {
		t.Fatalf("SyncCursor() error = %v", err)
	}
	if gotCursor.HistoryID != cursor.HistoryID || gotCursor.InitialPageToken != cursor.InitialPageToken ||
		gotCursor.OnboardingProcessed != 20 || gotCursor.EstimatedTotal != 42 {
		t.Fatalf("SyncCursor() = %#v, want %#v", gotCursor, cursor)
	}

	receivedAt := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	message := storage.Message{
		AccountID:         account.ID,
		ProviderMessageID: "gmail-message-1",
		ProviderThreadID:  "gmail-thread-1",
		RFCMessageID:      "<message-1@example.com>",
		InReplyTo:         "<earlier@example.com>",
		References:        []string{"<earlier@example.com>"},
		Subject:           "Re: Project status",
		NormalizedSubject: "project status",
		From:              "sender@example.com",
		To:                "person@example.com",
		Snippet:           "Updated status",
		Body:              "The project is ready for review.",
		BodyHash:          "body-hash-1",
		BodyState:         storage.BodyHydrated,
		InternalAt:        receivedAt,
		LabelIDs:          []string{"INBOX", "IMPORTANT"},
		Unread:            true,
		Importance:        storage.ImportanceImportant,
	}
	created, err := store.UpsertMessage(ctx, message)
	if err != nil {
		t.Fatalf("UpsertMessage() error = %v", err)
	}
	message.Subject = "Updated project status"
	message.Body = ""
	message.BodyHash = ""
	message.BodyState = storage.BodyMetadataOnly
	message.Importance = storage.ImportanceUnclassified
	updated, err := store.UpsertMessage(ctx, message)
	if err != nil {
		t.Fatalf("repeated UpsertMessage() error = %v", err)
	}
	if updated.ID != created.ID {
		t.Fatalf("message ID changed: %q != %q", updated.ID, created.ID)
	}
	if updated.Subject != message.Subject {
		t.Fatalf("message subject = %q, want %q", updated.Subject, message.Subject)
	}
	if updated.Body != "The project is ready for review." || updated.BodyState != storage.BodyHydrated {
		t.Fatalf("metadata refresh replaced hydrated body: %#v", updated)
	}
	if updated.Importance != storage.ImportanceImportant {
		t.Fatalf("metadata refresh replaced importance = %q", updated.Importance)
	}

	conversation := storage.Conversation{
		ID:              "conversation-1",
		AccountID:       account.ID,
		Title:           "Project status",
		Importance:      storage.ImportanceImportant,
		ImportanceScore: 0.9,
		LastMessageAt:   receivedAt,
	}
	if err := store.CreateConversation(ctx, conversation, []string{"gmail-thread-1"}, []string{"gmail-message-1"}); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	persisted, err := reopened.MessageByProviderID(ctx, account.ID, "gmail-message-1")
	if err != nil {
		t.Fatalf("MessageByProviderID() after reopen error = %v", err)
	}
	if persisted.Subject != message.Subject || len(persisted.References) != 1 {
		t.Fatalf("persisted message = %#v", persisted)
	}
	conversations, counts, err := reopened.WorkspaceConversations(ctx, "all")
	if err != nil {
		t.Fatalf("WorkspaceConversations() error = %v", err)
	}
	if counts.All != 1 || counts.Active != 1 || len(conversations) != 1 {
		t.Fatalf("WorkspaceConversations() = %#v, counts = %#v", conversations, counts)
	}
	if got := conversations[0]; got.ID != conversation.ID || got.Title != conversation.Title ||
		got.MessageCount != 1 || !got.Unread || got.LatestBody != "The project is ready for review." {
		t.Fatalf("conversation projection = %#v", got)
	}
	detail, err := reopened.WorkspaceConversation(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("WorkspaceConversation() error = %v", err)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].ProviderMessageID != "gmail-message-1" ||
		detail.Messages[0].Body != "The project is ready for review." {
		t.Fatalf("conversation detail = %#v", detail)
	}
}

func TestCreateConversationRejectsLowValueMail(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	err = store.CreateConversation(context.Background(), storage.Conversation{
		ID:         "conversation-1",
		AccountID:  "account-1",
		Importance: storage.ImportanceLowValue,
	}, []string{"thread-1"}, []string{"message-1"})
	if err == nil {
		t.Fatal("CreateConversation() accepted low-value mail")
	}
}

func TestGroupRelatedWorkspaceConversationsMergesRecruitingThreads(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	account, err := store.EnsureAccount(ctx, "gmail", "person@example.com")
	if err != nil {
		t.Fatalf("EnsureAccount() error = %v", err)
	}
	now := time.Now().UTC()
	for _, message := range []storage.Message{
		{
			AccountID: account.ID, ProviderMessageID: "message-1", ProviderThreadID: "thread-1",
			Subject: "Your interview with Snorkel AI", NormalizedSubject: "your interview with snorkel ai",
			From: "Recruiting <recruiting@snorkel.ai>", InternalAt: now.Add(-24 * time.Hour),
			Body: "Interview details", BodyState: storage.BodyHydrated, Importance: storage.ImportanceImportant,
		},
		{
			AccountID: account.ID, ProviderMessageID: "message-2", ProviderThreadID: "thread-2",
			Subject: "Reminder: Snorkel AI technical screen", NormalizedSubject: "reminder: snorkel ai technical screen",
			From: "Scheduler <scheduler@greenhouse.io>", InternalAt: now,
			Body: "Screen reminder", BodyState: storage.BodyHydrated, Importance: storage.ImportanceImportant,
		},
	} {
		if _, err := store.UpsertMessage(ctx, message); err != nil {
			t.Fatalf("UpsertMessage(%s) error = %v", message.ProviderMessageID, err)
		}
	}
	if err := store.CreateConversation(ctx, storage.Conversation{
		ID: "conversation-1", AccountID: account.ID, Title: "Your interview with Snorkel AI",
		Importance: storage.ImportanceImportant, ImportanceScore: 0.9, LastMessageAt: now.Add(-24 * time.Hour),
	}, []string{"thread-1"}, []string{"message-1"}); err != nil {
		t.Fatalf("CreateConversation(1) error = %v", err)
	}
	if err := store.CreateConversation(ctx, storage.Conversation{
		ID: "conversation-2", AccountID: account.ID, Title: "Reminder: Snorkel AI technical screen",
		Importance: storage.ImportanceImportant, ImportanceScore: 0.95, LastMessageAt: now,
	}, []string{"thread-2"}, []string{"message-2"}); err != nil {
		t.Fatalf("CreateConversation(2) error = %v", err)
	}

	grouped, err := store.GroupRelatedWorkspaceConversations(ctx, account.ID)
	if err != nil {
		t.Fatalf("GroupRelatedWorkspaceConversations() error = %v", err)
	}
	if !grouped {
		t.Fatal("GroupRelatedWorkspaceConversations() did not merge related threads")
	}
	conversations, counts, err := store.WorkspaceConversations(ctx, "all")
	if err != nil {
		t.Fatalf("WorkspaceConversations() error = %v", err)
	}
	if len(conversations) != 1 || counts.All != 1 || conversations[0].MessageCount != 2 {
		t.Fatalf("grouped conversations = %#v, counts = %#v", conversations, counts)
	}
	detail, err := store.WorkspaceConversation(ctx, conversations[0].ID)
	if err != nil || len(detail.Messages) != 2 {
		t.Fatalf("WorkspaceConversation() = %#v, error = %v", detail, err)
	}
	grouped, err = store.GroupRelatedWorkspaceConversations(ctx, account.ID)
	if err != nil || grouped {
		t.Fatalf("second grouping = %v, error = %v", grouped, err)
	}
}

func TestRepositoryReturnsNotFound(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	_, err = store.Account(context.Background(), "missing")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Account() error = %v, want ErrNotFound", err)
	}
}

var _ storage.Repository = (*Store)(nil)
