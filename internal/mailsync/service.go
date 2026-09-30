package mailsync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/googleapi"
	"local-email-workspace/internal/grouping"
	"local-email-workspace/internal/storage"
)

const (
	initialPageSize = "20"
	onboardingDays  = 14

	stateDiscovering = "discovering"
	stateCatchingUp  = "catching_up"
	stateComplete    = "complete"
	stateReconciling = "reconciling"
)

type gmailSource interface {
	Profile(context.Context, credentials.OAuthCredential) (googleapi.Profile, error)
	Recent(context.Context, credentials.OAuthCredential, string, string, string) (googleapi.InboxPage, error)
	History(context.Context, credentials.OAuthCredential, string, string) (googleapi.HistoryPage, error)
	Metadata(context.Context, credentials.OAuthCredential, string) (googleapi.InboxMessage, error)
}

type Service struct {
	repository storage.Repository
	gmail      gmailSource
	runMu      sync.Mutex
	now        func() time.Time
}

type Status struct {
	Phase                  string `json:"phase"`
	Complete               bool   `json:"complete"`
	MessagesCached         int    `json:"messagesCached"`
	HasMore                bool   `json:"hasMore"`
	Processed              int    `json:"processed"`
	OnboardingProcessed    int    `json:"onboardingProcessed"`
	EstimatedTotal         int    `json:"estimatedTotal"`
	PendingConversations   int    `json:"pendingConversations"`
	ConversationsProcessed int    `json:"conversationsProcessed"`
	ConversationsCreated   int    `json:"conversationsCreated"`
}

func New(repository storage.Repository, gmail gmailSource) *Service {
	return &Service{repository: repository, gmail: gmail, now: time.Now}
}

// SyncNextPage advances either the fixed two-week onboarding scan or one
// incremental Gmail History page. Provider IDs and cursors make every step
// safe to replay after cancellation or restart.
func (s *Service) SyncNextPage(ctx context.Context, credential credentials.OAuthCredential) (Status, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	emailAddress := credential.EmailAddress
	var profile googleapi.Profile
	if emailAddress == "" {
		var err error
		profile, err = s.gmail.Profile(ctx, credential)
		if err != nil {
			return Status{}, fmt.Errorf("load Gmail profile: %w", err)
		}
		emailAddress = profile.EmailAddress
	}
	account, err := s.repository.EnsureAccount(ctx, "gmail", emailAddress)
	if err != nil {
		return Status{}, fmt.Errorf("ensure synchronized account: %w", err)
	}

	cursor, err := s.repository.SyncCursor(ctx, account.ID)
	if errors.Is(err, storage.ErrNotFound) {
		cursor, err = s.newOnboardingCursor(ctx, credential, account.ID, profile)
		if err != nil {
			return Status{}, err
		}
	} else if err != nil {
		return Status{}, fmt.Errorf("read sync cursor: %w", err)
	} else if cursor.WindowStart.IsZero() || cursor.WindowEnd.IsZero() {
		cursor, err = s.newOnboardingCursor(ctx, credential, account.ID, googleapi.Profile{})
		if err != nil {
			return Status{}, err
		}
	}

	switch cursor.OnboardingState {
	case stateDiscovering, stateReconciling:
		return s.syncOnboardingPage(ctx, credential, account, cursor)
	case stateCatchingUp, stateComplete:
		return s.syncHistoryPage(ctx, credential, account, cursor)
	default:
		return Status{}, fmt.Errorf("unsupported onboarding state %q", cursor.OnboardingState)
	}
}

func (s *Service) newOnboardingCursor(
	ctx context.Context,
	credential credentials.OAuthCredential,
	accountID string,
	profile googleapi.Profile,
) (storage.SyncCursor, error) {
	if profile.HistoryID == "" {
		var err error
		profile, err = s.gmail.Profile(ctx, credential)
		if err != nil {
			return storage.SyncCursor{}, fmt.Errorf("load initial Gmail history ID: %w", err)
		}
	}
	now := s.now().UTC()
	windowStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -onboardingDays)
	windowEnd := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	return storage.SyncCursor{
		AccountID:       accountID,
		HistoryID:       profile.HistoryID,
		OnboardingState: stateDiscovering,
		WindowStart:     windowStart,
		WindowEnd:       windowEnd,
		UpdatedAt:       now,
	}, nil
}

func (s *Service) syncOnboardingPage(
	ctx context.Context,
	credential credentials.OAuthCredential,
	account storage.Account,
	cursor storage.SyncCursor,
) (Status, error) {
	searchQuery := fmt.Sprintf("after:%s before:%s",
		cursor.WindowStart.Format("2006/01/02"), cursor.WindowEnd.Format("2006/01/02"))
	page, err := s.gmail.Recent(ctx, credential, initialPageSize, cursor.InitialPageToken, searchQuery)
	if err != nil {
		return Status{}, fmt.Errorf("load two-week Gmail metadata page: %w", err)
	}
	messages, err := cachedMessages(account.ID, page.Messages)
	if err != nil {
		return Status{}, err
	}
	cursor.InitialPageToken = page.NextPageToken
	cursor.OnboardingProcessed += len(messages)
	if page.ResultSize > cursor.EstimatedTotal {
		cursor.EstimatedTotal = page.ResultSize
	}
	if cursor.OnboardingProcessed > cursor.EstimatedTotal {
		cursor.EstimatedTotal = cursor.OnboardingProcessed
	}
	cursor.UpdatedAt = s.now().UTC()
	if page.NextPageToken == "" {
		cursor.InitialSyncComplete = true
		cursor.OnboardingState = stateCatchingUp
	}
	if err := s.repository.SaveMessagePage(ctx, messages, cursor); err != nil {
		return Status{}, fmt.Errorf("commit two-week Gmail metadata page: %w", err)
	}
	return s.statusForAccount(ctx, account, cursor, len(messages))
}

func (s *Service) syncHistoryPage(
	ctx context.Context,
	credential credentials.OAuthCredential,
	account storage.Account,
	cursor storage.SyncCursor,
) (Status, error) {
	page, err := s.gmail.History(ctx, credential, cursor.HistoryID, cursor.HistoryPageToken)
	if err != nil {
		statusCode, _, isGmailError := googleapi.GmailErrorDetails(err)
		if isGmailError && statusCode == 404 {
			reset, resetErr := s.newOnboardingCursor(ctx, credential, account.ID, googleapi.Profile{})
			if resetErr != nil {
				return Status{}, resetErr
			}
			reset.OnboardingState = stateReconciling
			if err := s.repository.SaveSyncCursor(ctx, reset); err != nil {
				return Status{}, fmt.Errorf("start bounded Gmail reconciliation: %w", err)
			}
			return s.statusForAccount(ctx, account, reset, 0)
		}
		return Status{}, fmt.Errorf("load Gmail history page: %w", err)
	}

	referencesToFetch := make(map[string]googleapi.MessageReference, len(page.NewMessages)+len(page.ChangedMessages))
	for _, reference := range page.NewMessages {
		referencesToFetch[reference.ID] = reference
	}
	for _, reference := range page.ChangedMessages {
		_, err := s.repository.MessageByProviderID(ctx, account.ID, reference.ID)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return Status{}, fmt.Errorf("check changed Gmail message cache: %w", err)
		}
		referencesToFetch[reference.ID] = reference
	}
	providerMessages := make([]googleapi.InboxMessage, 0, len(referencesToFetch))
	deletedIDs := make([]string, 0, len(page.DeletedMessages))
	for _, reference := range page.DeletedMessages {
		deletedIDs = append(deletedIDs, reference.ID)
	}
	for _, reference := range referencesToFetch {
		message, err := s.gmail.Metadata(ctx, credential, reference.ID)
		if err != nil {
			statusCode, _, isGmailError := googleapi.GmailErrorDetails(err)
			if isGmailError && statusCode == 404 {
				deletedIDs = append(deletedIDs, reference.ID)
				continue
			}
			return Status{}, fmt.Errorf("load changed Gmail message metadata: %w", err)
		}
		providerMessages = append(providerMessages, message)
	}
	messages, err := cachedMessages(account.ID, providerMessages)
	if err != nil {
		return Status{}, err
	}
	if err := s.repository.MarkMessagesDeleted(ctx, account.ID, deletedIDs); err != nil {
		return Status{}, err
	}

	cursor.HistoryPageToken = page.NextPageToken
	cursor.UpdatedAt = s.now().UTC()
	if page.NextPageToken == "" {
		if page.HistoryID != "" {
			cursor.HistoryID = page.HistoryID
		}
		cursor.HistoryPageToken = ""
		if cursor.OnboardingState != stateComplete {
			cursor.OnboardingState = stateComplete
			cursor.OnboardingCompleted = cursor.UpdatedAt
		}
	}
	if err := s.repository.SaveMessagePage(ctx, messages, cursor); err != nil {
		return Status{}, fmt.Errorf("commit Gmail history page: %w", err)
	}
	return s.statusForAccount(ctx, account, cursor, len(messages))
}

func cachedMessages(accountID string, providerMessages []googleapi.InboxMessage) ([]storage.Message, error) {
	messages := make([]storage.Message, len(providerMessages))
	for index, providerMessage := range providerMessages {
		internalMilliseconds, err := strconv.ParseInt(providerMessage.InternalAt, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse Gmail internal date: %w", err)
		}
		messages[index] = storage.Message{
			AccountID:         accountID,
			ProviderMessageID: providerMessage.ID,
			ProviderThreadID:  providerMessage.ThreadID,
			RFCMessageID:      providerMessage.RFCMessageID,
			InReplyTo:         providerMessage.InReplyTo,
			References:        providerMessage.References,
			Subject:           providerMessage.Subject,
			NormalizedSubject: grouping.NormalizeSubject(providerMessage.Subject),
			From:              providerMessage.From,
			To:                providerMessage.To,
			Cc:                providerMessage.Cc,
			Date:              providerMessage.Date,
			Snippet:           providerMessage.Snippet,
			BodyState:         storage.BodyMetadataOnly,
			InternalAt:        time.UnixMilli(internalMilliseconds).UTC(),
			LabelIDs:          providerMessage.LabelIDs,
			Unread:            providerMessage.Unread,
			Importance:        storage.ImportanceUnclassified,
		}
	}
	return messages, nil
}

func (s *Service) Status(ctx context.Context, emailAddress string) (Status, error) {
	account, err := s.repository.AccountByProviderEmail(ctx, "gmail", emailAddress)
	if errors.Is(err, storage.ErrNotFound) {
		return Status{Phase: "not_started", HasMore: true}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("find synchronized account: %w", err)
	}
	cursor, err := s.repository.SyncCursor(ctx, account.ID)
	if errors.Is(err, storage.ErrNotFound) {
		return s.statusForAccount(ctx, account, storage.SyncCursor{OnboardingState: stateDiscovering}, 0)
	}
	if err != nil {
		return Status{}, fmt.Errorf("read sync cursor: %w", err)
	}
	return s.statusForAccount(ctx, account, cursor, 0)
}

func (s *Service) statusForAccount(
	ctx context.Context,
	account storage.Account,
	cursor storage.SyncCursor,
	processed int,
) (Status, error) {
	messageCount, err := s.repository.MessageCount(ctx, account.ID)
	if err != nil {
		return Status{}, err
	}
	phase := cursor.OnboardingState
	if phase == "" {
		phase = stateDiscovering
	}
	return Status{
		Phase:               phase,
		Complete:            phase == stateComplete,
		MessagesCached:      messageCount,
		HasMore:             phase != stateComplete || cursor.HistoryPageToken != "",
		Processed:           processed,
		OnboardingProcessed: cursor.OnboardingProcessed,
		EstimatedTotal:      cursor.EstimatedTotal,
	}, nil
}
