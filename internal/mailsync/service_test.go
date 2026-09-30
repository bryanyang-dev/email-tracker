package mailsync

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/googleapi"
	"local-email-workspace/internal/storage"
	storagesqlite "local-email-workspace/internal/storage/sqlite"
)

type fakeGmail struct {
	profile       googleapi.Profile
	recentPages   []googleapi.InboxPage
	historyPages  []googleapi.HistoryPage
	historyErr    error
	metadata      map[string]googleapi.InboxMessage
	profileCalls  int
	recentCalls   int
	historyCalls  int
	pageTokens    []string
	searchQueries []string
	historyStarts []string
}

func (f *fakeGmail) Profile(context.Context, credentials.OAuthCredential) (googleapi.Profile, error) {
	f.profileCalls++
	return f.profile, nil
}

func (f *fakeGmail) Recent(
	_ context.Context,
	_ credentials.OAuthCredential,
	limit string,
	pageToken string,
	searchQuery string,
) (googleapi.InboxPage, error) {
	if limit != initialPageSize {
		panic("unexpected page size " + limit)
	}
	f.pageTokens = append(f.pageTokens, pageToken)
	f.searchQueries = append(f.searchQueries, searchQuery)
	page := f.recentPages[f.recentCalls]
	f.recentCalls++
	return page, nil
}

func (f *fakeGmail) History(
	_ context.Context,
	_ credentials.OAuthCredential,
	startHistoryID string,
	_ string,
) (googleapi.HistoryPage, error) {
	f.historyStarts = append(f.historyStarts, startHistoryID)
	if f.historyErr != nil {
		return googleapi.HistoryPage{}, f.historyErr
	}
	page := f.historyPages[f.historyCalls]
	f.historyCalls++
	return page, nil
}

func TestExpiredHistoryStartsBoundedReconciliation(t *testing.T) {
	repository, err := storagesqlite.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer repository.Close()
	account, err := repository.EnsureAccount(context.Background(), "gmail", "person@example.com")
	if err != nil {
		t.Fatalf("EnsureAccount() error = %v", err)
	}
	if err := repository.SaveSyncCursor(context.Background(), storage.SyncCursor{
		AccountID:           account.ID,
		HistoryID:           "expired-history",
		InitialSyncComplete: true,
		OnboardingState:     stateComplete,
		WindowStart:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		WindowEnd:           time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("SaveSyncCursor() error = %v", err)
	}
	gmail := &fakeGmail{
		profile:    googleapi.Profile{EmailAddress: "person@example.com", HistoryID: "fresh-history"},
		historyErr: &googleapi.GmailAPIError{StatusCode: 404, Reason: "notFound"},
	}
	service := New(repository, gmail)
	service.now = func() time.Time {
		return time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	}

	status, err := service.SyncNextPage(context.Background(), credentials.OAuthCredential{EmailAddress: "person@example.com"})
	if err != nil {
		t.Fatalf("SyncNextPage() error = %v", err)
	}
	if status.Phase != stateReconciling || status.Complete || !status.HasMore {
		t.Fatalf("status = %#v", status)
	}
	cursor, err := repository.SyncCursor(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("SyncCursor() error = %v", err)
	}
	if cursor.HistoryID != "fresh-history" || cursor.WindowStart.Format("2006-01-02") != "2026-09-16" {
		t.Fatalf("cursor = %#v", cursor)
	}
}

func (f *fakeGmail) Metadata(
	_ context.Context,
	_ credentials.OAuthCredential,
	messageID string,
) (googleapi.InboxMessage, error) {
	return f.metadata[messageID], nil
}

func TestTwoWeekOnboardingCompletesThenUsesGmailHistory(t *testing.T) {
	repository, err := storagesqlite.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer repository.Close()

	gmail := &fakeGmail{
		profile: googleapi.Profile{EmailAddress: "person@example.com", HistoryID: "history-10"},
		recentPages: []googleapi.InboxPage{
			{
				Messages: []googleapi.InboxMessage{{
					ID: "message-1", ThreadID: "thread-1", Subject: "Re: Project update",
					RFCMessageID: "<message-1@example.com>", InternalAt: "1000",
				}},
				NextPageToken: "page-2",
				ResultSize:    2,
			},
			{
				Messages: []googleapi.InboxMessage{{
					ID: "message-2", ThreadID: "thread-1", Subject: "Project update",
					InReplyTo: "<message-1@example.com>", InternalAt: "2000",
				}},
				ResultSize: 2,
			},
		},
		historyPages: []googleapi.HistoryPage{
			{HistoryID: "history-12"},
			{
				NewMessages: []googleapi.MessageReference{{ID: "message-3", ThreadID: "thread-1"}},
				HistoryID:   "history-13",
			},
		},
		metadata: map[string]googleapi.InboxMessage{
			"message-3": {
				ID: "message-3", ThreadID: "thread-1", Subject: "Re: Project update", InternalAt: "3000",
			},
		},
	}
	service := New(repository, gmail)
	service.now = func() time.Time {
		return time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)
	}
	credential := credentials.OAuthCredential{EmailAddress: "person@example.com"}

	first, err := service.SyncNextPage(context.Background(), credential)
	if err != nil {
		t.Fatalf("first SyncNextPage() error = %v", err)
	}
	if first.Phase != stateDiscovering || first.Complete || !first.HasMore || first.MessagesCached != 1 {
		t.Fatalf("first status = %#v", first)
	}
	if first.OnboardingProcessed != 1 || first.EstimatedTotal != 2 {
		t.Fatalf("first progress = %#v", first)
	}
	second, err := service.SyncNextPage(context.Background(), credential)
	if err != nil {
		t.Fatalf("second SyncNextPage() error = %v", err)
	}
	if second.Phase != stateCatchingUp || second.Complete || !second.HasMore || second.MessagesCached != 2 {
		t.Fatalf("second status = %#v", second)
	}
	completed, err := service.SyncNextPage(context.Background(), credential)
	if err != nil {
		t.Fatalf("catch-up SyncNextPage() error = %v", err)
	}
	if completed.Phase != stateComplete || !completed.Complete || completed.HasMore {
		t.Fatalf("completed status = %#v", completed)
	}
	returning, err := service.SyncNextPage(context.Background(), credential)
	if err != nil {
		t.Fatalf("returning SyncNextPage() error = %v", err)
	}
	if !returning.Complete || returning.MessagesCached != 3 || returning.Processed != 1 {
		t.Fatalf("returning status = %#v", returning)
	}

	if gmail.profileCalls != 1 {
		t.Fatalf("profile calls = %d, want 1", gmail.profileCalls)
	}
	if len(gmail.searchQueries) != 2 || gmail.searchQueries[0] != "after:2026/09/16 before:2026/10/01" {
		t.Fatalf("search queries = %#v", gmail.searchQueries)
	}
	if len(gmail.pageTokens) != 2 || gmail.pageTokens[0] != "" || gmail.pageTokens[1] != "page-2" {
		t.Fatalf("page tokens = %#v", gmail.pageTokens)
	}
	if len(gmail.historyStarts) != 2 || gmail.historyStarts[0] != "history-10" || gmail.historyStarts[1] != "history-12" {
		t.Fatalf("history starts = %#v", gmail.historyStarts)
	}

	account, err := repository.AccountByProviderEmail(context.Background(), "gmail", "person@example.com")
	if err != nil {
		t.Fatalf("AccountByProviderEmail() error = %v", err)
	}
	message, err := repository.MessageByProviderID(context.Background(), account.ID, "message-1")
	if err != nil {
		t.Fatalf("MessageByProviderID() error = %v", err)
	}
	if message.NormalizedSubject != "project update" || message.BodyState != storage.BodyMetadataOnly {
		t.Fatalf("cached message = %#v", message)
	}
	cursor, err := repository.SyncCursor(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("SyncCursor() error = %v", err)
	}
	if cursor.HistoryID != "history-13" || cursor.OnboardingState != stateComplete || cursor.OnboardingCompleted.IsZero() {
		t.Fatalf("cursor = %#v", cursor)
	}
	if cursor.OnboardingProcessed != 2 || cursor.EstimatedTotal != 2 {
		t.Fatalf("cursor progress = %#v", cursor)
	}
}
