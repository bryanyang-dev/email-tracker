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

	"local-email-workspace/internal/grouping"
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

func (s *Store) AccountByProviderEmail(ctx context.Context, provider, emailAddress string) (storage.Account, error) {
	return s.accountByProviderEmail(ctx, strings.TrimSpace(provider), strings.ToLower(strings.TrimSpace(emailAddress)))
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
	cursor = prepareSyncCursor(cursor)
	updatedAt := cursor.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_cursors(
			account_id, history_id, initial_page_token, history_page_token,
			initial_sync_complete, onboarding_state, window_start, window_end,
			onboarding_completed_at, onboarding_processed, estimated_total, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			history_id = excluded.history_id,
			initial_page_token = excluded.initial_page_token,
			history_page_token = excluded.history_page_token,
			initial_sync_complete = excluded.initial_sync_complete,
			onboarding_state = excluded.onboarding_state,
			window_start = excluded.window_start,
			window_end = excluded.window_end,
			onboarding_completed_at = excluded.onboarding_completed_at,
			onboarding_processed = excluded.onboarding_processed,
			estimated_total = excluded.estimated_total,
			updated_at = excluded.updated_at
	`, cursor.AccountID, cursor.HistoryID, cursor.InitialPageToken, cursor.HistoryPageToken,
		cursor.InitialSyncComplete, cursor.OnboardingState, optionalMillis(cursor.WindowStart),
		optionalMillis(cursor.WindowEnd), optionalMillis(cursor.OnboardingCompleted),
		cursor.OnboardingProcessed, cursor.EstimatedTotal, millis(updatedAt))
	if err != nil {
		return fmt.Errorf("save sync cursor: %w", err)
	}
	return nil
}

func saveSyncCursor(ctx context.Context, transaction *sql.Tx, cursor storage.SyncCursor) error {
	cursor = prepareSyncCursor(cursor)
	_, err := transaction.ExecContext(ctx, `
		INSERT INTO sync_cursors(
			account_id, history_id, initial_page_token, history_page_token,
			initial_sync_complete, onboarding_state, window_start, window_end,
			onboarding_completed_at, onboarding_processed, estimated_total, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			history_id = excluded.history_id,
			initial_page_token = excluded.initial_page_token,
			history_page_token = excluded.history_page_token,
			initial_sync_complete = excluded.initial_sync_complete,
			onboarding_state = excluded.onboarding_state,
			window_start = excluded.window_start,
			window_end = excluded.window_end,
			onboarding_completed_at = excluded.onboarding_completed_at,
			onboarding_processed = excluded.onboarding_processed,
			estimated_total = excluded.estimated_total,
			updated_at = excluded.updated_at
	`, cursor.AccountID, cursor.HistoryID, cursor.InitialPageToken, cursor.HistoryPageToken,
		cursor.InitialSyncComplete, cursor.OnboardingState, optionalMillis(cursor.WindowStart),
		optionalMillis(cursor.WindowEnd), optionalMillis(cursor.OnboardingCompleted),
		cursor.OnboardingProcessed, cursor.EstimatedTotal, millis(cursor.UpdatedAt))
	if err != nil {
		return fmt.Errorf("save sync cursor: %w", err)
	}
	return nil
}

func prepareSyncCursor(cursor storage.SyncCursor) storage.SyncCursor {
	if cursor.OnboardingState == "" {
		if cursor.InitialSyncComplete {
			cursor.OnboardingState = "complete"
		} else {
			cursor.OnboardingState = "discovering"
		}
	}
	return cursor
}

func (s *Store) SyncCursor(ctx context.Context, accountID string) (storage.SyncCursor, error) {
	var cursor storage.SyncCursor
	var windowStart, windowEnd, onboardingCompleted, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
		SELECT account_id, history_id, initial_page_token, history_page_token,
			initial_sync_complete, onboarding_state, window_start, window_end,
			onboarding_completed_at, onboarding_processed, estimated_total, updated_at
		FROM sync_cursors WHERE account_id = ?
	`, accountID).Scan(
		&cursor.AccountID, &cursor.HistoryID, &cursor.InitialPageToken, &cursor.HistoryPageToken,
		&cursor.InitialSyncComplete, &cursor.OnboardingState, &windowStart, &windowEnd,
		&onboardingCompleted, &cursor.OnboardingProcessed, &cursor.EstimatedTotal, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.SyncCursor{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.SyncCursor{}, fmt.Errorf("read sync cursor: %w", err)
	}
	cursor.UpdatedAt = fromMillis(updatedAt)
	cursor.WindowStart = fromOptionalMillis(windowStart)
	cursor.WindowEnd = fromOptionalMillis(windowEnd)
	cursor.OnboardingCompleted = fromOptionalMillis(onboardingCompleted)
	return cursor, nil
}

func (s *Store) UpsertMessage(ctx context.Context, message storage.Message) (storage.Message, error) {
	message, referencesJSON, labelsJSON, err := prepareMessage(message)
	if err != nil {
		return storage.Message{}, err
	}

	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Message{}, fmt.Errorf("begin message upsert: %w", err)
	}
	defer transaction.Rollback()
	if err := upsertMessage(ctx, transaction, message, referencesJSON, labelsJSON); err != nil {
		return storage.Message{}, err
	}
	if err := transaction.Commit(); err != nil {
		return storage.Message{}, fmt.Errorf("commit message upsert: %w", err)
	}
	return s.MessageByProviderID(ctx, message.AccountID, message.ProviderMessageID)
}

func prepareMessage(message storage.Message) (storage.Message, string, string, error) {
	if message.AccountID == "" || message.ProviderMessageID == "" || message.ProviderThreadID == "" {
		return storage.Message{}, "", "", fmt.Errorf("account, provider message, and provider thread IDs are required")
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
		return storage.Message{}, "", "", fmt.Errorf("encode reference IDs: %w", err)
	}
	labelsJSON, err := json.Marshal(message.LabelIDs)
	if err != nil {
		return storage.Message{}, "", "", fmt.Errorf("encode label IDs: %w", err)
	}
	return message, string(referencesJSON), string(labelsJSON), nil
}

func upsertMessage(ctx context.Context, transaction *sql.Tx, message storage.Message, referencesJSON, labelsJSON string) error {
	now := message.UpdatedAt
	providerThreadLocalID, err := ensureProviderThread(ctx, transaction, message.AccountID, message.ProviderThreadID, now)
	if err != nil {
		return err
	}
	if message.ID == "" {
		message.ID, err = newID()
		if err != nil {
			return err
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
			deleted_at = NULL,
			updated_at = excluded.updated_at
	`,
		message.ID, message.AccountID, providerThreadLocalID, message.ProviderMessageID,
		message.RFCMessageID, message.InReplyTo, referencesJSON, message.Subject,
		message.NormalizedSubject, message.From, message.To, message.Cc, message.Date,
		message.Snippet, message.Body, message.BodyHash, message.BodyState, millis(message.InternalAt),
		labelsJSON, message.Unread, message.Importance, millis(message.CreatedAt), millis(message.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert message: %w", err)
	}
	return nil
}

func (s *Store) SaveMessagePage(ctx context.Context, messages []storage.Message, cursor storage.SyncCursor) error {
	if cursor.AccountID == "" {
		return fmt.Errorf("account ID is required")
	}

	type preparedMessage struct {
		message        storage.Message
		referencesJSON string
		labelsJSON     string
	}
	prepared := make([]preparedMessage, len(messages))
	for index, message := range messages {
		if message.AccountID != cursor.AccountID {
			return fmt.Errorf("message account does not match sync cursor account")
		}
		normalized, referencesJSON, labelsJSON, err := prepareMessage(message)
		if err != nil {
			return err
		}
		prepared[index] = preparedMessage{normalized, referencesJSON, labelsJSON}
	}
	if cursor.UpdatedAt.IsZero() {
		cursor.UpdatedAt = time.Now().UTC()
	}

	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin message page save: %w", err)
	}
	defer transaction.Rollback()
	for _, item := range prepared {
		if err := upsertMessage(ctx, transaction, item.message, item.referencesJSON, item.labelsJSON); err != nil {
			return err
		}
	}
	if err := saveSyncCursor(ctx, transaction, cursor); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit message page save: %w", err)
	}
	return nil
}

func ensureProviderThread(ctx context.Context, transaction *sql.Tx, accountID, providerThreadID string, now time.Time) (string, error) {
	identifier, err := newID()
	if err != nil {
		return "", err
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO provider_threads(id, account_id, provider_thread_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, provider_thread_id) DO UPDATE SET
			updated_at = excluded.updated_at,
			classification_state = 'pending'
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
	var deletedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT m.id, m.account_id, m.provider_message_id, pt.provider_thread_id,
			m.rfc_message_id, m.in_reply_to, m.reference_ids, m.subject, m.normalized_subject,
			m.sender, m.recipients_to, m.recipients_cc, m.message_date, m.snippet,
			m.normalized_body, m.body_hash, m.body_state, m.internal_at, m.label_ids,
			m.unread, m.importance, m.suspicious_content, m.has_list_unsubscribe,
			m.has_list_id, m.precedence, m.auto_submitted, m.has_feedback_id,
			m.created_at, m.updated_at, m.deleted_at
		FROM messages m
		JOIN provider_threads pt ON pt.id = m.provider_thread_id
		WHERE m.account_id = ? AND m.provider_message_id = ?
	`, accountID, providerMessageID).Scan(
		&message.ID, &message.AccountID, &message.ProviderMessageID, &message.ProviderThreadID,
		&message.RFCMessageID, &message.InReplyTo, &referencesJSON, &message.Subject,
		&message.NormalizedSubject, &message.From, &message.To, &message.Cc, &message.Date,
		&message.Snippet, &message.Body, &message.BodyHash, &message.BodyState, &internalAt,
		&labelsJSON, &message.Unread, &message.Importance, &message.SuspiciousContent,
		&message.HasListUnsubscribe, &message.HasListID, &message.Precedence,
		&message.AutoSubmitted, &message.HasFeedbackID, &createdAt, &updatedAt, &deletedAt,
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
	if deletedAt.Valid {
		message.DeletedAt = fromMillis(deletedAt.Int64)
	}
	return message, nil
}

func (s *Store) MessageCount(ctx context.Context, accountID string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM messages WHERE account_id = ?", accountID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count messages: %w", err)
	}
	return count, nil
}

func (s *Store) MarkMessagesDeleted(ctx context.Context, accountID string, providerMessageIDs []string) error {
	if len(providerMessageIDs) == 0 {
		return nil
	}
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin message deletion update: %w", err)
	}
	defer transaction.Rollback()
	deletedAt := time.Now().UTC().UnixMilli()
	for _, providerMessageID := range providerMessageIDs {
		if _, err := transaction.ExecContext(ctx, `
			UPDATE provider_threads SET classification_state = 'pending', updated_at = ?
			WHERE id = (
				SELECT provider_thread_id FROM messages
				WHERE account_id = ? AND provider_message_id = ?
			)
		`, deletedAt, accountID, providerMessageID); err != nil {
			return fmt.Errorf("queue deleted message conversation: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE messages SET deleted_at = ?, updated_at = ?
			WHERE account_id = ? AND provider_message_id = ?
		`, deletedAt, deletedAt, accountID, providerMessageID); err != nil {
			return fmt.Errorf("mark message deleted: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit message deletion update: %w", err)
	}
	return nil
}

func (s *Store) PendingProviderConversationCount(ctx context.Context, accountID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM provider_threads pt
		WHERE pt.account_id = ?
			AND pt.classification_state = 'pending'
			AND EXISTS (
				SELECT 1 FROM messages m
				WHERE m.provider_thread_id = pt.id AND m.deleted_at IS NULL
			)
	`, accountID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending provider conversations: %w", err)
	}
	return count, nil
}

func (s *Store) PendingProviderConversations(
	ctx context.Context,
	accountID string,
	limit int,
) ([]storage.ProviderConversation, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT pt.provider_thread_id,
			EXISTS (
				SELECT 1
				FROM workspace_thread_provider_threads wtpt
				WHERE wtpt.provider_thread_id = pt.id
			)
		FROM provider_threads pt
		WHERE pt.account_id = ?
			AND pt.classification_state = 'pending'
			AND EXISTS (
				SELECT 1 FROM messages m
				WHERE m.provider_thread_id = pt.id AND m.deleted_at IS NULL
			)
		ORDER BY (
			SELECT MAX(m.internal_at) FROM messages m
			WHERE m.provider_thread_id = pt.id AND m.deleted_at IS NULL
		) DESC
		LIMIT ?
	`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending provider conversations: %w", err)
	}
	conversations := make([]storage.ProviderConversation, 0, limit)
	for rows.Next() {
		conversation := storage.ProviderConversation{AccountID: accountID}
		if err := rows.Scan(&conversation.ProviderThreadID, &conversation.ExistingWorkspaceThread); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan pending provider conversation: %w", err)
		}
		conversations = append(conversations, conversation)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close pending provider conversations: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending provider conversations: %w", err)
	}

	for index := range conversations {
		messages, err := s.messagesForProviderThread(ctx, accountID, conversations[index].ProviderThreadID)
		if err != nil {
			return nil, err
		}
		conversations[index].Messages = messages
	}
	return conversations, nil
}

func (s *Store) messagesForProviderThread(
	ctx context.Context,
	accountID, providerThreadID string,
) ([]storage.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.account_id, m.provider_message_id, pt.provider_thread_id,
			m.rfc_message_id, m.in_reply_to, m.reference_ids, m.subject, m.normalized_subject,
			m.sender, m.recipients_to, m.recipients_cc, m.message_date, m.snippet,
			m.normalized_body, m.body_hash, m.body_state, m.internal_at, m.label_ids,
			m.unread, m.importance, m.suspicious_content, m.has_list_unsubscribe,
			m.has_list_id, m.precedence, m.auto_submitted, m.has_feedback_id,
			m.created_at, m.updated_at, m.deleted_at
		FROM messages m
		JOIN provider_threads pt ON pt.id = m.provider_thread_id
		WHERE m.account_id = ? AND pt.provider_thread_id = ? AND m.deleted_at IS NULL
		ORDER BY m.internal_at
	`, accountID, providerThreadID)
	if err != nil {
		return nil, fmt.Errorf("list provider conversation messages: %w", err)
	}
	defer rows.Close()
	messages := make([]storage.Message, 0)
	for rows.Next() {
		var message storage.Message
		var referencesJSON, labelsJSON string
		var internalAt, createdAt, updatedAt int64
		var deletedAt sql.NullInt64
		if err := rows.Scan(
			&message.ID, &message.AccountID, &message.ProviderMessageID, &message.ProviderThreadID,
			&message.RFCMessageID, &message.InReplyTo, &referencesJSON, &message.Subject,
			&message.NormalizedSubject, &message.From, &message.To, &message.Cc, &message.Date,
			&message.Snippet, &message.Body, &message.BodyHash, &message.BodyState, &internalAt,
			&labelsJSON, &message.Unread, &message.Importance, &message.SuspiciousContent,
			&message.HasListUnsubscribe, &message.HasListID, &message.Precedence,
			&message.AutoSubmitted, &message.HasFeedbackID, &createdAt, &updatedAt, &deletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan provider conversation message: %w", err)
		}
		if err := decodeStoredMessage(&message, referencesJSON, labelsJSON, internalAt, createdAt, updatedAt, deletedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider conversation messages: %w", err)
	}
	return messages, nil
}

func decodeStoredMessage(
	message *storage.Message,
	referencesJSON, labelsJSON string,
	internalAt, createdAt, updatedAt int64,
	deletedAt sql.NullInt64,
) error {
	if err := json.Unmarshal([]byte(referencesJSON), &message.References); err != nil {
		return fmt.Errorf("decode reference IDs: %w", err)
	}
	if err := json.Unmarshal([]byte(labelsJSON), &message.LabelIDs); err != nil {
		return fmt.Errorf("decode label IDs: %w", err)
	}
	message.InternalAt = fromMillis(internalAt)
	message.CreatedAt = fromMillis(createdAt)
	message.UpdatedAt = fromMillis(updatedAt)
	if deletedAt.Valid {
		message.DeletedAt = fromMillis(deletedAt.Int64)
	}
	return nil
}

func (s *Store) SaveHydratedMessages(
	ctx context.Context,
	accountID, providerThreadID string,
	messages []storage.Message,
) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin hydrated message save: %w", err)
	}
	defer transaction.Rollback()
	now := time.Now().UTC().UnixMilli()
	for _, message := range messages {
		if message.ProviderMessageID == "" {
			return fmt.Errorf("provider message ID is required")
		}
		if message.BodyState != storage.BodyHydrated && message.BodyState != storage.BodyTruncated {
			return fmt.Errorf("hydrated message has invalid body state %q", message.BodyState)
		}
		labelsJSON, err := json.Marshal(message.LabelIDs)
		if err != nil {
			return fmt.Errorf("encode hydrated message labels: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE messages
			SET normalized_body = ?, body_hash = ?, body_state = ?, label_ids = ?,
				suspicious_content = ?, has_list_unsubscribe = ?, has_list_id = ?,
				precedence = ?, auto_submitted = ?, has_feedback_id = ?, updated_at = ?
			WHERE account_id = ? AND provider_message_id = ?
				AND provider_thread_id = (
					SELECT id FROM provider_threads
					WHERE account_id = ? AND provider_thread_id = ?
				)
		`, message.Body, message.BodyHash, message.BodyState, string(labelsJSON),
			message.SuspiciousContent, message.HasListUnsubscribe, message.HasListID,
			message.Precedence, message.AutoSubmitted, message.HasFeedbackID, now,
			accountID, message.ProviderMessageID, accountID, providerThreadID); err != nil {
			return fmt.Errorf("save hydrated message: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit hydrated messages: %w", err)
	}
	return nil
}

func (s *Store) SaveProviderConversationAssessment(
	ctx context.Context,
	assessment storage.ProviderConversationAssessment,
) (bool, error) {
	if assessment.AccountID == "" || assessment.ProviderThreadID == "" {
		return false, fmt.Errorf("account and provider thread IDs are required")
	}
	if assessment.Importance != storage.ImportanceImportant &&
		assessment.Importance != storage.ImportancePossiblyImportant &&
		assessment.Importance != storage.ImportanceLowValue &&
		assessment.Importance != storage.ImportanceUnclassified {
		return false, fmt.Errorf("invalid provider conversation importance %q", assessment.Importance)
	}
	reasonsJSON, err := json.Marshal(assessment.ReasonCodes)
	if err != nil {
		return false, fmt.Errorf("encode classification reasons: %w", err)
	}
	now := time.Now().UTC()
	lastMessageAt := assessment.LastMessageAt
	if lastMessageAt.IsZero() {
		lastMessageAt = now
	}

	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin provider conversation assessment: %w", err)
	}
	defer transaction.Rollback()
	var providerThreadLocalID string
	if err := transaction.QueryRowContext(ctx, `
		SELECT id FROM provider_threads WHERE account_id = ? AND provider_thread_id = ?
	`, assessment.AccountID, assessment.ProviderThreadID).Scan(&providerThreadLocalID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, storage.ErrNotFound
		}
		return false, fmt.Errorf("read assessed provider conversation: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE provider_threads
		SET classification_state = 'processed', visibility = ?, category = ?, confidence = ?,
			reason_codes = ?, ai_status = ?, needs_action = ?, urgent = ?, classified_at = ?, updated_at = ?
		WHERE id = ?
	`, assessment.Visibility, assessment.Category, assessment.Confidence, string(reasonsJSON),
		assessment.AIStatus, assessment.NeedsAction, assessment.Urgent,
		millis(now), millis(now), providerThreadLocalID); err != nil {
		return false, fmt.Errorf("save provider conversation assessment: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE messages SET importance = ?, updated_at = ?
		WHERE provider_thread_id = ? AND deleted_at IS NULL
			AND (
				importance = 'unclassified'
				OR ? IN ('important', 'possibly_important')
			)
	`, assessment.Importance, millis(now), providerThreadLocalID, assessment.Importance); err != nil {
		return false, fmt.Errorf("save assessed message importance: %w", err)
	}
	if assessment.Importance != storage.ImportanceImportant &&
		assessment.Importance != storage.ImportancePossiblyImportant {
		if err := transaction.Commit(); err != nil {
			return false, fmt.Errorf("commit provider conversation assessment: %w", err)
		}
		return false, nil
	}

	var workspaceThreadID string
	err = transaction.QueryRowContext(ctx, `
		SELECT workspace_thread_id
		FROM workspace_thread_provider_threads
		WHERE provider_thread_id = ?
	`, providerThreadLocalID).Scan(&workspaceThreadID)
	created := errors.Is(err, sql.ErrNoRows)
	if err != nil && !created {
		return false, fmt.Errorf("read provider conversation assignment: %w", err)
	}
	if created {
		workspaceThreadID, err = newID()
		if err != nil {
			return false, err
		}
		state := "suggested"
		if assessment.Importance == storage.ImportanceImportant {
			state = "active"
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO workspace_threads(
				id, account_id, title, state, importance, importance_score,
				last_message_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, workspaceThreadID, assessment.AccountID, assessment.Title, state,
			assessment.Importance, assessment.Confidence, millis(lastMessageAt), millis(now), millis(now)); err != nil {
			return false, fmt.Errorf("create assessed workspace conversation: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO workspace_thread_provider_threads(
				workspace_thread_id, provider_thread_id, assignment_origin, confidence, created_at
			) VALUES (?, ?, 'gmail_thread', 1, ?)
		`, workspaceThreadID, providerThreadLocalID, millis(now)); err != nil {
			return false, fmt.Errorf("assign assessed provider conversation: %w", err)
		}
	} else {
		if _, err := transaction.ExecContext(ctx, `
			UPDATE workspace_threads
			SET title = CASE WHEN title = '' THEN ? ELSE title END,
				state = CASE WHEN ? = 'important' AND state = 'suggested' THEN 'active' ELSE state END,
				importance = CASE WHEN ? = 'important' THEN 'important' ELSE importance END,
				importance_score = MAX(importance_score, ?),
				last_message_at = MAX(last_message_at, ?), updated_at = ?
			WHERE id = ?
		`, assessment.Title, assessment.Importance, assessment.Importance,
			assessment.Confidence, millis(lastMessageAt), millis(now), workspaceThreadID); err != nil {
			return false, fmt.Errorf("update assessed workspace conversation: %w", err)
		}
	}

	var nextPosition int
	if err := transaction.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(position), -1) + 1 FROM thread_messages WHERE workspace_thread_id = ?
	`, workspaceThreadID).Scan(&nextPosition); err != nil {
		return false, fmt.Errorf("read next workspace conversation position: %w", err)
	}
	messageRows, err := transaction.QueryContext(ctx, `
		SELECT id FROM messages
		WHERE provider_thread_id = ? AND deleted_at IS NULL
		ORDER BY internal_at
	`, providerThreadLocalID)
	if err != nil {
		return false, fmt.Errorf("list assessed conversation messages: %w", err)
	}
	messageIDs := make([]string, 0)
	for messageRows.Next() {
		var messageID string
		if err := messageRows.Scan(&messageID); err != nil {
			messageRows.Close()
			return false, fmt.Errorf("scan assessed conversation message: %w", err)
		}
		messageIDs = append(messageIDs, messageID)
	}
	if err := messageRows.Close(); err != nil {
		return false, fmt.Errorf("close assessed conversation messages: %w", err)
	}
	if err := messageRows.Err(); err != nil {
		return false, fmt.Errorf("iterate assessed conversation messages: %w", err)
	}
	for _, messageID := range messageIDs {
		result, err := transaction.ExecContext(ctx, `
			INSERT OR IGNORE INTO thread_messages(
				workspace_thread_id, message_id, assignment_origin, confidence, position, created_at
			) VALUES (?, ?, 'provider_thread', 1, ?, ?)
		`, workspaceThreadID, messageID, nextPosition, millis(now))
		if err != nil {
			return false, fmt.Errorf("assign assessed conversation message: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("count assessed conversation assignment: %w", err)
		}
		if affected == 1 {
			nextPosition++
		}
	}
	if err := transaction.Commit(); err != nil {
		return false, fmt.Errorf("commit assessed provider conversation: %w", err)
	}
	return created, nil
}

func (s *Store) WorkspaceConversations(
	ctx context.Context,
	view string,
) ([]storage.WorkspaceConversationProjection, storage.WorkspaceConversationCounts, error) {
	filter, err := workspaceConversationFilter(view)
	if err != nil {
		return nil, storage.WorkspaceConversationCounts{}, err
	}
	counts, err := s.workspaceConversationCounts(ctx)
	if err != nil {
		return nil, storage.WorkspaceConversationCounts{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM workspace_threads
		WHERE `+filter+`
		ORDER BY last_message_at DESC, id
	`)
	if err != nil {
		return nil, storage.WorkspaceConversationCounts{}, fmt.Errorf("list workspace conversation IDs: %w", err)
	}
	identifiers := make([]string, 0)
	for rows.Next() {
		var identifier string
		if err := rows.Scan(&identifier); err != nil {
			rows.Close()
			return nil, storage.WorkspaceConversationCounts{}, fmt.Errorf("scan workspace conversation ID: %w", err)
		}
		identifiers = append(identifiers, identifier)
	}
	if err := rows.Close(); err != nil {
		return nil, storage.WorkspaceConversationCounts{}, fmt.Errorf("close workspace conversation IDs: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, storage.WorkspaceConversationCounts{}, fmt.Errorf("iterate workspace conversation IDs: %w", err)
	}

	conversations := make([]storage.WorkspaceConversationProjection, 0, len(identifiers))
	for _, identifier := range identifiers {
		conversation, err := s.workspaceConversation(ctx, identifier, false)
		if err != nil {
			return nil, storage.WorkspaceConversationCounts{}, err
		}
		conversations = append(conversations, conversation)
	}
	return conversations, counts, nil
}

func workspaceConversationFilter(view string) (string, error) {
	switch view {
	case "", "all":
		return "1 = 1", nil
	case "active", "suggested", "snoozed", "resolved":
		return "state = '" + view + "'", nil
	case "attention":
		return `EXISTS (
			SELECT 1 FROM workspace_thread_provider_threads wtpt
			JOIN provider_threads pt ON pt.id = wtpt.provider_thread_id
			WHERE wtpt.workspace_thread_id = workspace_threads.id
				AND (pt.needs_action = 1 OR pt.urgent = 1)
		)`, nil
	default:
		return "", fmt.Errorf("invalid workspace conversation view %q", view)
	}
}

func (s *Store) workspaceConversationCounts(ctx context.Context) (storage.WorkspaceConversationCounts, error) {
	var counts storage.WorkspaceConversationCounts
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN wt.state = 'active' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN EXISTS (
				SELECT 1 FROM workspace_thread_provider_threads wtpt
				JOIN provider_threads pt ON pt.id = wtpt.provider_thread_id
				WHERE wtpt.workspace_thread_id = wt.id
					AND (pt.needs_action = 1 OR pt.urgent = 1)
			) THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN wt.state = 'suggested' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN wt.state = 'snoozed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN wt.state = 'resolved' THEN 1 ELSE 0 END), 0),
			COUNT(*)
		FROM workspace_threads wt
	`).Scan(&counts.Active, &counts.Attention, &counts.Suggested, &counts.Snoozed, &counts.Resolved, &counts.All)
	if err != nil {
		return storage.WorkspaceConversationCounts{}, fmt.Errorf("count workspace conversations: %w", err)
	}
	return counts, nil
}

func (s *Store) WorkspaceConversation(
	ctx context.Context,
	identifier string,
) (storage.WorkspaceConversationProjection, error) {
	return s.workspaceConversation(ctx, identifier, true)
}

func (s *Store) workspaceConversation(
	ctx context.Context,
	identifier string,
	includeMessages bool,
) (storage.WorkspaceConversationProjection, error) {
	var conversation storage.WorkspaceConversationProjection
	var lastMessageAt int64
	var reasonsJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT wt.id, wt.title, wt.state, wt.importance, wt.importance_score, wt.last_message_at,
			(SELECT COUNT(*) FROM thread_messages tm
				JOIN messages m ON m.id = tm.message_id
				WHERE tm.workspace_thread_id = wt.id AND m.deleted_at IS NULL),
			EXISTS (
				SELECT 1 FROM thread_messages tm
				JOIN messages m ON m.id = tm.message_id
				WHERE tm.workspace_thread_id = wt.id AND m.unread = 1 AND m.deleted_at IS NULL
			),
			EXISTS (
				SELECT 1 FROM workspace_thread_provider_threads wtpt
				JOIN provider_threads pt ON pt.id = wtpt.provider_thread_id
				WHERE wtpt.workspace_thread_id = wt.id
					AND (pt.needs_action = 1 OR pt.urgent = 1)
			),
			COALESCE((
				SELECT m.snippet FROM thread_messages tm
				JOIN messages m ON m.id = tm.message_id
				WHERE tm.workspace_thread_id = wt.id AND m.deleted_at IS NULL
				ORDER BY m.internal_at DESC LIMIT 1
			), ''),
			COALESCE((
				SELECT m.normalized_body FROM thread_messages tm
				JOIN messages m ON m.id = tm.message_id
				WHERE tm.workspace_thread_id = wt.id AND m.deleted_at IS NULL
				ORDER BY m.internal_at DESC LIMIT 1
			), ''),
			COALESCE((
				SELECT pt.category FROM workspace_thread_provider_threads wtpt
				JOIN provider_threads pt ON pt.id = wtpt.provider_thread_id
				WHERE wtpt.workspace_thread_id = wt.id
				ORDER BY pt.classified_at DESC LIMIT 1
			), 'other'),
			COALESCE((
				SELECT pt.reason_codes FROM workspace_thread_provider_threads wtpt
				JOIN provider_threads pt ON pt.id = wtpt.provider_thread_id
				WHERE wtpt.workspace_thread_id = wt.id
				ORDER BY pt.classified_at DESC LIMIT 1
			), '[]'),
			COALESCE((
				SELECT pt.ai_status FROM workspace_thread_provider_threads wtpt
				JOIN provider_threads pt ON pt.id = wtpt.provider_thread_id
				WHERE wtpt.workspace_thread_id = wt.id
				ORDER BY pt.classified_at DESC LIMIT 1
			), 'rules')
		FROM workspace_threads wt
		WHERE wt.id = ?
	`, identifier).Scan(
		&conversation.ID, &conversation.Title, &conversation.State, &conversation.Importance,
		&conversation.ImportanceScore, &lastMessageAt, &conversation.MessageCount,
		&conversation.Unread, &conversation.NeedsAttention, &conversation.Preview, &conversation.LatestBody,
		&conversation.Category, &reasonsJSON, &conversation.AIStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.WorkspaceConversationProjection{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.WorkspaceConversationProjection{}, fmt.Errorf("read workspace conversation: %w", err)
	}
	conversation.LastMessageAt = fromMillis(lastMessageAt)
	if err := json.Unmarshal([]byte(reasonsJSON), &conversation.ReasonCodes); err != nil {
		return storage.WorkspaceConversationProjection{}, fmt.Errorf("decode workspace conversation reasons: %w", err)
	}
	participants, err := s.workspaceConversationParticipants(ctx, identifier)
	if err != nil {
		return storage.WorkspaceConversationProjection{}, err
	}
	conversation.Participants = participants
	if includeMessages {
		conversation.Messages, err = s.messagesForWorkspaceConversation(ctx, identifier)
		if err != nil {
			return storage.WorkspaceConversationProjection{}, err
		}
	}
	return conversation, nil
}

func (s *Store) workspaceConversationParticipants(ctx context.Context, identifier string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.sender, MAX(m.internal_at) AS latest_at
		FROM thread_messages tm
		JOIN messages m ON m.id = tm.message_id
		WHERE tm.workspace_thread_id = ? AND m.deleted_at IS NULL AND m.sender <> ''
		GROUP BY m.sender
		ORDER BY latest_at DESC
		LIMIT 8
	`, identifier)
	if err != nil {
		return nil, fmt.Errorf("list workspace conversation participants: %w", err)
	}
	defer rows.Close()
	participants := make([]string, 0)
	for rows.Next() {
		var participant string
		var latestAt int64
		if err := rows.Scan(&participant, &latestAt); err != nil {
			return nil, fmt.Errorf("scan workspace conversation participant: %w", err)
		}
		participants = append(participants, participant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace conversation participants: %w", err)
	}
	return participants, nil
}

func (s *Store) messagesForWorkspaceConversation(ctx context.Context, identifier string) ([]storage.Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.account_id, m.provider_message_id, pt.provider_thread_id,
			m.rfc_message_id, m.in_reply_to, m.reference_ids, m.subject, m.normalized_subject,
			m.sender, m.recipients_to, m.recipients_cc, m.message_date, m.snippet,
			m.normalized_body, m.body_hash, m.body_state, m.internal_at, m.label_ids,
			m.unread, m.importance, m.suspicious_content, m.has_list_unsubscribe,
			m.has_list_id, m.precedence, m.auto_submitted, m.has_feedback_id,
			m.created_at, m.updated_at, m.deleted_at
		FROM thread_messages tm
		JOIN messages m ON m.id = tm.message_id
		JOIN provider_threads pt ON pt.id = m.provider_thread_id
		WHERE tm.workspace_thread_id = ? AND m.deleted_at IS NULL
		ORDER BY m.internal_at, tm.position
	`, identifier)
	if err != nil {
		return nil, fmt.Errorf("list workspace conversation messages: %w", err)
	}
	defer rows.Close()
	return scanStoredMessages(rows)
}

func scanStoredMessages(rows *sql.Rows) ([]storage.Message, error) {
	messages := make([]storage.Message, 0)
	for rows.Next() {
		var message storage.Message
		var referencesJSON, labelsJSON string
		var internalAt, createdAt, updatedAt int64
		var deletedAt sql.NullInt64
		if err := rows.Scan(
			&message.ID, &message.AccountID, &message.ProviderMessageID, &message.ProviderThreadID,
			&message.RFCMessageID, &message.InReplyTo, &referencesJSON, &message.Subject,
			&message.NormalizedSubject, &message.From, &message.To, &message.Cc, &message.Date,
			&message.Snippet, &message.Body, &message.BodyHash, &message.BodyState, &internalAt,
			&labelsJSON, &message.Unread, &message.Importance, &message.SuspiciousContent,
			&message.HasListUnsubscribe, &message.HasListID, &message.Precedence,
			&message.AutoSubmitted, &message.HasFeedbackID, &createdAt, &updatedAt, &deletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan stored message: %w", err)
		}
		if err := decodeStoredMessage(&message, referencesJSON, labelsJSON, internalAt, createdAt, updatedAt, deletedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stored messages: %w", err)
	}
	return messages, nil
}

type workspaceGroupingCandidate struct {
	ID       string
	Messages []storage.Message
}

func (s *Store) GroupRelatedWorkspaceConversations(ctx context.Context, accountID string) (bool, error) {
	account, err := s.Account(ctx, accountID)
	if err != nil {
		return false, fmt.Errorf("read grouping account: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT wt.id
		FROM workspace_threads wt
		WHERE wt.account_id = ?
			AND NOT EXISTS (
				SELECT 1 FROM workspace_thread_provider_threads wtpt
				WHERE wtpt.workspace_thread_id = wt.id AND wtpt.locked = 1
			)
		ORDER BY wt.last_message_at DESC, wt.id
		LIMIT 500
	`, accountID)
	if err != nil {
		return false, fmt.Errorf("list grouping candidates: %w", err)
	}
	identifiers := make([]string, 0)
	for rows.Next() {
		var identifier string
		if err := rows.Scan(&identifier); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan grouping candidate: %w", err)
		}
		identifiers = append(identifiers, identifier)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close grouping candidates: %w", err)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate grouping candidates: %w", err)
	}

	candidates := make([]workspaceGroupingCandidate, 0, len(identifiers))
	for _, identifier := range identifiers {
		conversation, err := s.workspaceConversation(ctx, identifier, true)
		if err != nil {
			return false, err
		}
		candidates = append(candidates, workspaceGroupingCandidate{
			ID:       identifier,
			Messages: conversation.Messages,
		})
	}
	neverMerge, err := s.neverMergeRelationships(ctx, accountID)
	if err != nil {
		return false, err
	}
	for leftIndex := range candidates {
		for rightIndex := leftIndex + 1; rightIndex < len(candidates); rightIndex++ {
			left := candidates[leftIndex]
			right := candidates[rightIndex]
			if neverMerge[relationshipKey(left.ID, right.ID)] {
				continue
			}
			match := grouping.Compare(
				groupingMessages(left.Messages), groupingMessages(right.Messages), account.EmailAddress,
			)
			if !match.Matched {
				continue
			}
			if err := s.mergeWorkspaceConversations(ctx, accountID, left.ID, right.ID, match); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func groupingMessages(messages []storage.Message) []grouping.Message {
	result := make([]grouping.Message, len(messages))
	for index, message := range messages {
		result[index] = grouping.Message{
			RFCMessageID:      message.RFCMessageID,
			InReplyTo:         message.InReplyTo,
			References:        message.References,
			NormalizedSubject: message.NormalizedSubject,
			From:              message.From,
			InternalAt:        message.InternalAt,
		}
	}
	return result
}

func (s *Store) neverMergeRelationships(ctx context.Context, accountID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT tr.left_workspace_thread_id, tr.right_workspace_thread_id
		FROM thread_relationships tr
		JOIN workspace_threads wt ON wt.id = tr.left_workspace_thread_id
		WHERE wt.account_id = ? AND tr.relationship = 'never_merge'
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("list never-merge relationships: %w", err)
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var left, right string
		if err := rows.Scan(&left, &right); err != nil {
			return nil, fmt.Errorf("scan never-merge relationship: %w", err)
		}
		result[relationshipKey(left, right)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate never-merge relationships: %w", err)
	}
	return result, nil
}

func relationshipKey(left, right string) string {
	if left > right {
		left, right = right, left
	}
	return left + "\x00" + right
}

func (s *Store) mergeWorkspaceConversations(
	ctx context.Context,
	accountID, sourceID, targetID string,
	match grouping.Match,
) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace conversation merge: %w", err)
	}
	defer transaction.Rollback()

	type threadState struct {
		title           string
		state           string
		importance      storage.Importance
		importanceScore float64
		lastMessageAt   int64
	}
	readState := func(identifier string) (threadState, error) {
		var state threadState
		err := transaction.QueryRowContext(ctx, `
			SELECT title, state, importance, importance_score, last_message_at
			FROM workspace_threads WHERE id = ? AND account_id = ?
		`, identifier, accountID).Scan(
			&state.title, &state.state, &state.importance, &state.importanceScore, &state.lastMessageAt,
		)
		return state, err
	}
	source, err := readState(sourceID)
	if err != nil {
		return fmt.Errorf("read source workspace conversation: %w", err)
	}
	target, err := readState(targetID)
	if err != nil {
		return fmt.Errorf("read target workspace conversation: %w", err)
	}
	if source.lastMessageAt > target.lastMessageAt {
		target.title = source.title
	}
	if source.state == "active" || target.state == "active" {
		target.state = "active"
	}
	if source.importance == storage.ImportanceImportant {
		target.importance = storage.ImportanceImportant
	}
	if source.importanceScore > target.importanceScore {
		target.importanceScore = source.importanceScore
	}
	if source.lastMessageAt > target.lastMessageAt {
		target.lastMessageAt = source.lastMessageAt
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := transaction.ExecContext(ctx, `
		UPDATE workspace_threads
		SET title = ?, state = ?, importance = ?, importance_score = ?,
			last_message_at = ?, updated_at = ?
		WHERE id = ? AND account_id = ?
	`, target.title, target.state, target.importance, target.importanceScore,
		target.lastMessageAt, now, targetID, accountID); err != nil {
		return fmt.Errorf("update grouped workspace conversation: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE workspace_thread_provider_threads
		SET workspace_thread_id = ?, assignment_origin = ?, confidence = ?
		WHERE workspace_thread_id = ?
	`, targetID, match.Origin, match.Confidence, sourceID); err != nil {
		return fmt.Errorf("move grouped provider conversations: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE thread_messages SET workspace_thread_id = ? WHERE workspace_thread_id = ?
	`, targetID, sourceID); err != nil {
		return fmt.Errorf("move grouped messages: %w", err)
	}
	type relationship struct {
		other        string
		relationship string
		source       string
		createdAt    int64
	}
	relationshipRows, err := transaction.QueryContext(ctx, `
		SELECT
			CASE WHEN left_workspace_thread_id = ? THEN right_workspace_thread_id ELSE left_workspace_thread_id END,
			relationship, source, created_at
		FROM thread_relationships
		WHERE left_workspace_thread_id = ? OR right_workspace_thread_id = ?
	`, sourceID, sourceID, sourceID)
	if err != nil {
		return fmt.Errorf("list grouped conversation relationships: %w", err)
	}
	relationships := make([]relationship, 0)
	for relationshipRows.Next() {
		var item relationship
		if err := relationshipRows.Scan(&item.other, &item.relationship, &item.source, &item.createdAt); err != nil {
			relationshipRows.Close()
			return fmt.Errorf("scan grouped conversation relationship: %w", err)
		}
		relationships = append(relationships, item)
	}
	if err := relationshipRows.Close(); err != nil {
		return fmt.Errorf("close grouped conversation relationships: %w", err)
	}
	if err := relationshipRows.Err(); err != nil {
		return fmt.Errorf("iterate grouped conversation relationships: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		DELETE FROM thread_relationships
		WHERE left_workspace_thread_id = ? OR right_workspace_thread_id = ?
	`, sourceID, sourceID); err != nil {
		return fmt.Errorf("remove grouped conversation relationships: %w", err)
	}
	for _, item := range relationships {
		if item.other == targetID {
			continue
		}
		left, right := targetID, item.other
		if left > right {
			left, right = right, left
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT OR IGNORE INTO thread_relationships(
				left_workspace_thread_id, right_workspace_thread_id, relationship, source, created_at
			) VALUES (?, ?, ?, ?, ?)
		`, left, right, item.relationship, item.source, item.createdAt); err != nil {
			return fmt.Errorf("preserve grouped conversation relationship: %w", err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM workspace_threads WHERE id = ?`, sourceID); err != nil {
		return fmt.Errorf("delete grouped source conversation: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit workspace conversation merge: %w", err)
	}
	return nil
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

func optionalMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return millis(value)
}

func fromOptionalMillis(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return fromMillis(value)
}
