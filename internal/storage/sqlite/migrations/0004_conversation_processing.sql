ALTER TABLE provider_threads ADD COLUMN classification_state TEXT NOT NULL DEFAULT 'pending'
    CHECK (classification_state IN ('pending', 'processed'));
ALTER TABLE provider_threads ADD COLUMN visibility TEXT NOT NULL DEFAULT ''
    CHECK (visibility IN ('', 'all', 'suggested', 'active'));
ALTER TABLE provider_threads ADD COLUMN category TEXT NOT NULL DEFAULT '';
ALTER TABLE provider_threads ADD COLUMN confidence REAL NOT NULL DEFAULT 0
    CHECK (confidence >= 0 AND confidence <= 1);
ALTER TABLE provider_threads ADD COLUMN reason_codes TEXT NOT NULL DEFAULT '[]';
ALTER TABLE provider_threads ADD COLUMN classified_at INTEGER NOT NULL DEFAULT 0;

ALTER TABLE messages ADD COLUMN suspicious_content INTEGER NOT NULL DEFAULT 0
    CHECK (suspicious_content IN (0, 1));
ALTER TABLE messages ADD COLUMN has_list_unsubscribe INTEGER NOT NULL DEFAULT 0
    CHECK (has_list_unsubscribe IN (0, 1));
ALTER TABLE messages ADD COLUMN has_list_id INTEGER NOT NULL DEFAULT 0
    CHECK (has_list_id IN (0, 1));
ALTER TABLE messages ADD COLUMN precedence TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN auto_submitted INTEGER NOT NULL DEFAULT 0
    CHECK (auto_submitted IN (0, 1));
ALTER TABLE messages ADD COLUMN has_feedback_id INTEGER NOT NULL DEFAULT 0
    CHECK (has_feedback_id IN (0, 1));
