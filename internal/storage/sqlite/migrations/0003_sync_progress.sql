ALTER TABLE sync_cursors ADD COLUMN onboarding_processed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_cursors ADD COLUMN estimated_total INTEGER NOT NULL DEFAULT 0;
