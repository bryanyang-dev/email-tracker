CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    email_address TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (provider, email_address)
);

CREATE TABLE sync_cursors (
    account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    history_id TEXT NOT NULL DEFAULT '',
    initial_page_token TEXT NOT NULL DEFAULT '',
    initial_sync_complete INTEGER NOT NULL DEFAULT 0 CHECK (initial_sync_complete IN (0, 1)),
    updated_at INTEGER NOT NULL
);

CREATE TABLE provider_threads (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    provider_thread_id TEXT NOT NULL,
    history_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (account_id, provider_thread_id)
);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    provider_thread_id TEXT NOT NULL REFERENCES provider_threads(id) ON DELETE CASCADE,
    provider_message_id TEXT NOT NULL,
    rfc_message_id TEXT NOT NULL DEFAULT '',
    in_reply_to TEXT NOT NULL DEFAULT '',
    reference_ids TEXT NOT NULL DEFAULT '[]',
    subject TEXT NOT NULL DEFAULT '',
    normalized_subject TEXT NOT NULL DEFAULT '',
    sender TEXT NOT NULL DEFAULT '',
    recipients_to TEXT NOT NULL DEFAULT '',
    recipients_cc TEXT NOT NULL DEFAULT '',
    message_date TEXT NOT NULL DEFAULT '',
    snippet TEXT NOT NULL DEFAULT '',
    normalized_body TEXT NOT NULL DEFAULT '',
    body_hash TEXT NOT NULL DEFAULT '',
    body_state TEXT NOT NULL DEFAULT 'metadata_only'
        CHECK (body_state IN ('metadata_only', 'hydrated', 'truncated')),
    internal_at INTEGER NOT NULL,
    label_ids TEXT NOT NULL DEFAULT '[]',
    unread INTEGER NOT NULL DEFAULT 0 CHECK (unread IN (0, 1)),
    importance TEXT NOT NULL DEFAULT 'unclassified'
        CHECK (importance IN ('unclassified', 'important', 'possibly_important', 'low_value')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (account_id, provider_message_id)
);

CREATE INDEX messages_provider_thread_idx ON messages(provider_thread_id, internal_at);
CREATE INDEX messages_rfc_message_id_idx ON messages(account_id, rfc_message_id) WHERE rfc_message_id <> '';
CREATE INDEX messages_in_reply_to_idx ON messages(account_id, in_reply_to) WHERE in_reply_to <> '';
CREATE INDEX messages_subject_time_idx ON messages(account_id, normalized_subject, internal_at);
CREATE INDEX messages_importance_idx ON messages(account_id, importance, internal_at DESC);

CREATE TABLE workspace_threads (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'active'
        CHECK (state IN ('active', 'suggested', 'resolved', 'snoozed')),
    importance TEXT NOT NULL
        CHECK (importance IN ('important', 'possibly_important')),
    importance_score REAL NOT NULL DEFAULT 0 CHECK (importance_score >= 0 AND importance_score <= 1),
    last_message_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX workspace_threads_activity_idx
    ON workspace_threads(account_id, state, last_message_at DESC);

CREATE TABLE workspace_thread_provider_threads (
    workspace_thread_id TEXT NOT NULL REFERENCES workspace_threads(id) ON DELETE CASCADE,
    provider_thread_id TEXT NOT NULL REFERENCES provider_threads(id) ON DELETE CASCADE,
    assignment_origin TEXT NOT NULL
        CHECK (assignment_origin IN ('gmail_thread', 'headers', 'rules', 'ai_suggestion', 'user')),
    confidence REAL NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    locked INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1)),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (workspace_thread_id, provider_thread_id),
    UNIQUE (provider_thread_id)
);

CREATE TABLE thread_messages (
    workspace_thread_id TEXT NOT NULL REFERENCES workspace_threads(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    assignment_origin TEXT NOT NULL
        CHECK (assignment_origin IN ('provider_thread', 'headers', 'rules', 'ai_suggestion', 'user')),
    confidence REAL NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    position INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (workspace_thread_id, message_id),
    UNIQUE (message_id)
);

CREATE INDEX thread_messages_order_idx ON thread_messages(workspace_thread_id, position);

CREATE TABLE thread_relationships (
    left_workspace_thread_id TEXT NOT NULL REFERENCES workspace_threads(id) ON DELETE CASCADE,
    right_workspace_thread_id TEXT NOT NULL REFERENCES workspace_threads(id) ON DELETE CASCADE,
    relationship TEXT NOT NULL CHECK (relationship IN ('related', 'never_merge')),
    source TEXT NOT NULL CHECK (source IN ('rules', 'ai_suggestion', 'user')),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (left_workspace_thread_id, right_workspace_thread_id),
    CHECK (left_workspace_thread_id < right_workspace_thread_id)
);

CREATE TABLE grouping_suggestions (
    id TEXT PRIMARY KEY,
    source_provider_thread_id TEXT NOT NULL REFERENCES provider_threads(id) ON DELETE CASCADE,
    candidate_workspace_thread_id TEXT NOT NULL REFERENCES workspace_threads(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'accepted', 'rejected', 'superseded')),
    confidence REAL NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    reason_codes TEXT NOT NULL DEFAULT '[]',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (source_provider_thread_id, candidate_workspace_thread_id)
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    account_id TEXT REFERENCES accounts(id) ON DELETE CASCADE,
    job_type TEXT NOT NULL,
    entity_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'retry', 'completed', 'failed')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at INTEGER NOT NULL,
    lease_expires_at INTEGER,
    payload TEXT NOT NULL DEFAULT '{}',
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (job_type, entity_key)
);

CREATE INDEX jobs_ready_idx ON jobs(status, available_at);
