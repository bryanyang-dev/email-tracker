ALTER TABLE sync_cursors ADD COLUMN history_page_token TEXT NOT NULL DEFAULT '';
ALTER TABLE sync_cursors ADD COLUMN onboarding_state TEXT NOT NULL DEFAULT 'discovering'
    CHECK (onboarding_state IN ('discovering', 'catching_up', 'complete', 'reconciling'));
ALTER TABLE sync_cursors ADD COLUMN window_start INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_cursors ADD COLUMN window_end INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_cursors ADD COLUMN onboarding_completed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN deleted_at INTEGER;

-- Earlier development builds synchronized an unbounded INBOX listing. Its
-- page tokens cannot be reused with the fixed two-week onboarding query.
UPDATE sync_cursors
SET initial_page_token = '',
    history_page_token = '',
    initial_sync_complete = 0,
    onboarding_state = 'discovering',
    window_start = 0,
    window_end = 0,
    onboarding_completed_at = 0;
