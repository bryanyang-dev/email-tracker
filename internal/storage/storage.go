package storage

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("record not found")

type Importance string

const (
	ImportanceUnclassified      Importance = "unclassified"
	ImportanceImportant         Importance = "important"
	ImportancePossiblyImportant Importance = "possibly_important"
	ImportanceLowValue          Importance = "low_value"
)

type BodyState string

const (
	BodyMetadataOnly BodyState = "metadata_only"
	BodyHydrated     BodyState = "hydrated"
	BodyTruncated    BodyState = "truncated"
)

type Account struct {
	ID           string
	Provider     string
	EmailAddress string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type SyncCursor struct {
	AccountID           string
	HistoryID           string
	InitialPageToken    string
	HistoryPageToken    string
	InitialSyncComplete bool
	OnboardingState     string
	WindowStart         time.Time
	WindowEnd           time.Time
	OnboardingCompleted time.Time
	OnboardingProcessed int
	EstimatedTotal      int
	UpdatedAt           time.Time
}

type Message struct {
	ID                 string
	AccountID          string
	ProviderMessageID  string
	ProviderThreadID   string
	RFCMessageID       string
	InReplyTo          string
	References         []string
	Subject            string
	NormalizedSubject  string
	From               string
	To                 string
	Cc                 string
	Date               string
	Snippet            string
	Body               string
	BodyHash           string
	BodyState          BodyState
	InternalAt         time.Time
	LabelIDs           []string
	Unread             bool
	Importance         Importance
	SuspiciousContent  bool
	HasListUnsubscribe bool
	HasListID          bool
	Precedence         string
	AutoSubmitted      bool
	HasFeedbackID      bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          time.Time
}

type ProviderConversation struct {
	AccountID               string
	ProviderThreadID        string
	ExistingWorkspaceThread bool
	Messages                []Message
}

type ProviderConversationAssessment struct {
	AccountID        string
	ProviderThreadID string
	Title            string
	Visibility       string
	Category         string
	Confidence       float64
	ReasonCodes      []string
	AIStatus         string
	NeedsAction      bool
	Urgent           bool
	Importance       Importance
	LastMessageAt    time.Time
}

type WorkspaceConversationProjection struct {
	ID              string
	Title           string
	State           string
	Importance      Importance
	ImportanceScore float64
	LastMessageAt   time.Time
	MessageCount    int
	Unread          bool
	NeedsAttention  bool
	Participants    []string
	Preview         string
	LatestBody      string
	Category        string
	ReasonCodes     []string
	AIStatus        string
	Messages        []Message
}

type WorkspaceConversationCounts struct {
	Active    int
	Attention int
	Suggested int
	Snoozed   int
	Resolved  int
	All       int
}

type Conversation struct {
	ID              string
	AccountID       string
	Title           string
	State           string
	Importance      Importance
	ImportanceScore float64
	LastMessageAt   time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Repository is the durable boundary used by synchronization and grouping.
// Gmail, HTTP, and AI packages should depend on this interface rather than SQL.
type Repository interface {
	EnsureAccount(context.Context, string, string) (Account, error)
	Account(context.Context, string) (Account, error)
	AccountByProviderEmail(context.Context, string, string) (Account, error)
	SaveSyncCursor(context.Context, SyncCursor) error
	SyncCursor(context.Context, string) (SyncCursor, error)
	UpsertMessage(context.Context, Message) (Message, error)
	SaveMessagePage(context.Context, []Message, SyncCursor) error
	MessageByProviderID(context.Context, string, string) (Message, error)
	MessageCount(context.Context, string) (int, error)
	MarkMessagesDeleted(context.Context, string, []string) error
	PendingProviderConversations(context.Context, string, int) ([]ProviderConversation, error)
	PendingProviderConversationCount(context.Context, string) (int, error)
	SaveHydratedMessages(context.Context, string, string, []Message) error
	SaveProviderConversationAssessment(context.Context, ProviderConversationAssessment) (bool, error)
	WorkspaceConversations(context.Context, string) ([]WorkspaceConversationProjection, WorkspaceConversationCounts, error)
	WorkspaceConversation(context.Context, string) (WorkspaceConversationProjection, error)
	GroupRelatedWorkspaceConversations(context.Context, string) (bool, error)
	CreateConversation(context.Context, Conversation, []string, []string) error
	Close() error
}
