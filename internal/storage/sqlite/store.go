package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"local-email-workspace/internal/storage"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db       *sql.DB
	writerMu sync.Mutex
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	store := &Store{db: database}
	if err := store.configure(context.Background()); err != nil {
		database.Close()
		return nil, err
	}
	if err := store.migrate(context.Background()); err != nil {
		database.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		database.Close()
		return nil, fmt.Errorf("restrict database permissions: %w", err)
	}
	return store, nil
}

func (s *Store) configure(ctx context.Context) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite: %w", err)
		}
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		applied, err := s.migrationApplied(ctx, entry.Name())
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		contents, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		transaction, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), err)
		}
		if _, err = transaction.ExecContext(ctx, string(contents)); err == nil {
			_, err = transaction.ExecContext(ctx,
				"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
				entry.Name(), time.Now().UTC().UnixMilli())
		}
		if err != nil {
			transaction.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Store) migrationApplied(ctx context.Context, version string) (bool, error) {
	var found int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM schema_migrations WHERE version = ?", version).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read migration ledger: %w", err)
	}
	return true, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) EnsureAccount(ctx context.Context, provider, emailAddress string) (storage.Account, error) {
	provider = strings.TrimSpace(provider)
	emailAddress = strings.ToLower(strings.TrimSpace(emailAddress))
	if provider == "" || emailAddress == "" {
		return storage.Account{}, fmt.Errorf("provider and email address are required")
	}

	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	now := time.Now().UTC()
	identifier, err := newID()
	if err != nil {
		return storage.Account{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO accounts(id, provider, email_address, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(provider, email_address) DO UPDATE SET updated_at = excluded.updated_at
	`, identifier, provider, emailAddress, millis(now), millis(now))
	if err != nil {
		return storage.Account{}, fmt.Errorf("ensure account: %w", err)
	}
	return s.accountByProviderEmail(ctx, provider, emailAddress)
}

func (s *Store) Account(ctx context.Context, id string) (storage.Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `
		SELECT id, provider, email_address, created_at, updated_at
		FROM accounts WHERE id = ?
	`, id))
}

func (s *Store) accountByProviderEmail(ctx context.Context, provider, emailAddress string) (storage.Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `
		SELECT id, provider, email_address, created_at, updated_at
		FROM accounts WHERE provider = ? AND email_address = ?
	`, provider, emailAddress))
}

type rowScanner interface {
	Scan(...any) error
}

func scanAccount(row rowScanner) (storage.Account, error) {
	var account storage.Account
	var createdAt, updatedAt int64
	if err := row.Scan(&account.ID, &account.Provider, &account.EmailAddress, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storage.Account{}, storage.ErrNotFound
		}
		return storage.Account{}, fmt.Errorf("scan account: %w", err)
	}
	account.CreatedAt = fromMillis(createdAt)
	account.UpdatedAt = fromMillis(updatedAt)
	return account, nil
}

func (s *Store) SaveSyncCursor(ctx context.Context, cursor storage.SyncCursor) error {
	if cursor.AccountID == "" {
		return fmt.Errorf("account ID is required")
	}
	updatedAt := cursor.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_cursors(account_id, history_id, initial_page_token, initial_sync_complete, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			history_id = excluded.history_id,
			initial_page_token = excluded.initial_page_token,
			initial_sync_complete = excluded.initial_sync_complete,
			updated_at = excluded.updated_at
	`, cursor.AccountID, cursor.HistoryID, cursor.InitialPageToken, cursor.InitialSyncComplete, millis(updatedAt))
	if err != nil {
		return fmt.Errorf("save sync cursor: %w", err)
	}
	return nil
}

func (s *Store) SyncCursor(ctx context.Context, accountID string) (storage.SyncCursor, error) {
	var cursor storage.SyncCursor
	var updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT account_id, history_id, initial_page_token, initial_sync_complete, updated_at
		FROM sync_cursors WHERE account_id = ?
	`, accountID).Scan(
		&cursor.AccountID, &cursor.HistoryID, &cursor.InitialPageToken,
		&cursor.InitialSyncComplete, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.SyncCursor{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.SyncCursor{}, fmt.Errorf("read sync cursor: %w", err)
	}
	cursor.UpdatedAt = fromMillis(updatedAt)
	return cursor, nil
}

func (s *Store) UpsertMessage(ctx context.Context, message storage.Message) (storage.Message, error) {
	if message.AccountID == "" || message.ProviderMessageID == "" || message.ProviderThreadID == "" {
		return storage.Message{}, fmt.Errorf("account, provider message, and provider thread IDs are required")
	}
	if message.BodyState == "" {
		message.BodyState = storage.BodyMetadataOnly
	}
	if message.Importance == "" {
		message.Importance = storage.ImportanceUnclassified
	}
	now := time.Now().UTC()
	if message.UpdatedAt.IsZero() {
		message.UpdatedAt = now
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = message.UpdatedAt
	}
	if message.InternalAt.IsZero() {
		message.InternalAt = message.UpdatedAt
	}

	referencesJSON, err := json.Marshal(message.References)
	if err != nil {
		return storage.Message{}, fmt.Errorf("encode reference IDs: %w", err)
	}
	labelsJSON, err := json.Marshal(message.LabelIDs)
	if err != nil {
		return storage.Message{}, fmt.Errorf("encode label IDs: %w", err)
	}

	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Message{}, fmt.Errorf("begin message upsert: %w", err)
	}
	defer transaction.Rollback()

	providerThreadLocalID, err := ensureProviderThread(ctx, transaction, message.AccountID, message.ProviderThreadID, now)
	if err != nil {
		return storage.Message{}, err
	}
	if message.ID == "" {
		message.ID, err = newID()
		if err != nil {
			return storage.Message{}, err
		}
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO messages(
			id, account_id, provider_thread_id, provider_message_id, rfc_message_id, in_reply_to,
			reference_ids, subject, normalized_subject, sender, recipients_to, recipients_cc,
			message_date, snippet, normalized_body, body_hash, body_state, internal_at,
			label_ids, unread, importance, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, provider_message_id) DO UPDATE SET
			provider_thread_id = excluded.provider_thread_id,
			rfc_message_id = excluded.rfc_message_id,
			in_reply_to = excluded.in_reply_to,
			reference_ids = excluded.reference_ids,
			subject = excluded.subject,
			normalized_subject = excluded.normalized_subject,
			sender = excluded.sender,
			recipients_to = excluded.recipients_to,
			recipients_cc = excluded.recipients_cc,
			message_date = excluded.message_date,
			snippet = excluded.snippet,
			normalized_body = CASE
				WHEN excluded.body_state = 'metadata_only' THEN messages.normalized_body
				ELSE excluded.normalized_body
			END,
			body_hash = CASE
				WHEN excluded.body_state = 'metadata_only' THEN messages.body_hash
				ELSE excluded.body_hash
			END,
			body_state = CASE
				WHEN excluded.body_state = 'metadata_only' AND messages.body_state <> 'metadata_only'
					THEN messages.body_state
				ELSE excluded.body_state
			END,
			internal_at = excluded.internal_at,
			label_ids = excluded.label_ids,
			unread = excluded.unread,
			importance = CASE
				WHEN excluded.importance = 'unclassified' THEN messages.importance
				ELSE excluded.importance
			END,
			updated_at = excluded.updated_at
	`,
		message.ID, message.AccountID, providerThreadLocalID, message.ProviderMessageID,
		message.RFCMessageID, message.InReplyTo, string(referencesJSON), message.Subject,
		message.NormalizedSubject, message.From, message.To, message.Cc, message.Date,
		message.Snippet, message.Body, message.BodyHash, message.BodyState, millis(message.InternalAt),
		string(labelsJSON), message.Unread, message.Importance, millis(message.CreatedAt), millis(message.UpdatedAt),
	)
	if err != nil {
		return storage.Message{}, fmt.Errorf("upsert message: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return storage.Message{}, fmt.Errorf("commit message upsert: %w", err)
	}
	return s.MessageByProviderID(ctx, message.AccountID, message.ProviderMessageID)
}

func ensureProviderThread(ctx context.Context, transaction *sql.Tx, accountID, providerThreadID string, now time.Time) (string, error) {
	identifier, err := newID()
	if err != nil {
		return "", err
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO provider_threads(id, account_id, provider_thread_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, provider_thread_id) DO UPDATE SET updated_at = excluded.updated_at
	`, identifier, accountID, providerThreadID, millis(now), millis(now))
	if err != nil {
		return "", fmt.Errorf("ensure provider thread: %w", err)
	}
	var localID string
	if err := transaction.QueryRowContext(ctx, `
		SELECT id FROM provider_threads WHERE account_id = ? AND provider_thread_id = ?
	`, accountID, providerThreadID).Scan(&localID); err != nil {
		return "", fmt.Errorf("read provider thread: %w", err)
	}
	return localID, nil
}

func (s *Store) MessageByProviderID(ctx context.Context, accountID, providerMessageID string) (storage.Message, error) {
	var message storage.Message
	var referencesJSON, labelsJSON string
	var internalAt, createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.account_id, m.provider_message_id, pt.provider_thread_id,
			m.rfc_message_id, m.in_reply_to, m.reference_ids, m.subject, m.normalized_subject,
			m.sender, m.recipients_to, m.recipients_cc, m.message_date, m.snippet,
			m.normalized_body, m.body_hash, m.body_state, m.internal_at, m.label_ids,
			m.unread, m.importance, m.created_at, m.updated_at
		FROM messages m
		JOIN provider_threads pt ON pt.id = m.provider_thread_id
		WHERE m.account_id = ? AND m.provider_message_id = ?
	`, accountID, providerMessageID).Scan(
		&message.ID, &message.AccountID, &message.ProviderMessageID, &message.ProviderThreadID,
		&message.RFCMessageID, &message.InReplyTo, &referencesJSON, &message.Subject,
		&message.NormalizedSubject, &message.From, &message.To, &message.Cc, &message.Date,
		&message.Snippet, &message.Body, &message.BodyHash, &message.BodyState, &internalAt,
		&labelsJSON, &message.Unread, &message.Importance, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Message{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.Message{}, fmt.Errorf("read message: %w", err)
	}
	if err := json.Unmarshal([]byte(referencesJSON), &message.References); err != nil {
		return storage.Message{}, fmt.Errorf("decode reference IDs: %w", err)
	}
	if err := json.Unmarshal([]byte(labelsJSON), &message.LabelIDs); err != nil {
		return storage.Message{}, fmt.Errorf("decode label IDs: %w", err)
	}
	message.InternalAt = fromMillis(internalAt)
	message.CreatedAt = fromMillis(createdAt)
	message.UpdatedAt = fromMillis(updatedAt)
	return message, nil
}

func (s *Store) CreateConversation(
	ctx context.Context,
	conversation storage.Conversation,
	providerThreadIDs []string,
	messageIDs []string,
) error {
	if conversation.Importance != storage.ImportanceImportant &&
		conversation.Importance != storage.ImportancePossiblyImportant {
		return fmt.Errorf("conversation importance must be important or possibly important")
	}
	if conversation.ID == "" || conversation.AccountID == "" || len(providerThreadIDs) == 0 || len(messageIDs) == 0 {
		return fmt.Errorf("conversation, account, provider thread, and message IDs are required")
	}
	now := time.Now().UTC()
	if conversation.CreatedAt.IsZero() {
		conversation.CreatedAt = now
	}
	if conversation.UpdatedAt.IsZero() {
		conversation.UpdatedAt = now
	}
	if conversation.LastMessageAt.IsZero() {
		conversation.LastMessageAt = now
	}
	if conversation.State == "" {
		conversation.State = "active"
	}

	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin conversation creation: %w", err)
	}
	defer transaction.Rollback()
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO workspace_threads(
			id, account_id, title, state, importance, importance_score,
			last_message_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, conversation.ID, conversation.AccountID, conversation.Title, conversation.State,
		conversation.Importance, conversation.ImportanceScore, millis(conversation.LastMessageAt),
		millis(conversation.CreatedAt), millis(conversation.UpdatedAt))
	if err != nil {
		return fmt.Errorf("create conversation: %w", err)
	}
	for _, providerThreadID := range providerThreadIDs {
		result, err := transaction.ExecContext(ctx, `
			INSERT INTO workspace_thread_provider_threads(
				workspace_thread_id, provider_thread_id, assignment_origin, confidence, created_at
			)
			SELECT ?, id, 'gmail_thread', 1, ?
			FROM provider_threads WHERE account_id = ? AND provider_thread_id = ?
		`, conversation.ID, millis(now), conversation.AccountID, providerThreadID)
		if err != nil {
			return fmt.Errorf("assign provider thread: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count assigned provider threads: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("assign provider thread %q: %w", providerThreadID, storage.ErrNotFound)
		}
	}
	for position, messageID := range messageIDs {
		result, err := transaction.ExecContext(ctx, `
			INSERT INTO thread_messages(
				workspace_thread_id, message_id, assignment_origin, confidence, position, created_at
			)
			SELECT ?, id, 'provider_thread', 1, ?, ?
			FROM messages WHERE account_id = ? AND provider_message_id = ?
		`, conversation.ID, position, millis(now), conversation.AccountID, messageID)
		if err != nil {
			return fmt.Errorf("assign message: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count assigned messages: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("assign message %q: %w", messageID, storage.ErrNotFound)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit conversation creation: %w", err)
	}
	return nil
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate local identifier: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func millis(value time.Time) int64 {
	return value.UTC().UnixMilli()
}

func fromMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}
