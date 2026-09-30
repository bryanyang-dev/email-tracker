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
	InitialSyncComplete bool
	UpdatedAt           time.Time
}

type Message struct {
	ID                string
	AccountID         string
	ProviderMessageID string
	ProviderThreadID  string
	RFCMessageID      string
	InReplyTo         string
	References        []string
	Subject           string
	NormalizedSubject string
	From              string
	To                string
	Cc                string
	Date              string
	Snippet           string
	Body              string
	BodyHash          string
	BodyState         BodyState
	InternalAt        time.Time
	LabelIDs          []string
	Unread            bool
	Importance        Importance
	CreatedAt         time.Time
	UpdatedAt         time.Time
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
	SaveSyncCursor(context.Context, SyncCursor) error
	SyncCursor(context.Context, string) (SyncCursor, error)
	UpsertMessage(context.Context, Message) (Message, error)
	MessageByProviderID(context.Context, string, string) (Message, error)
	CreateConversation(context.Context, Conversation, []string, []string) error
	Close() error
}
