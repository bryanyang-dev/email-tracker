ALTER TABLE provider_threads ADD COLUMN needs_action INTEGER NOT NULL DEFAULT 0
    CHECK (needs_action IN (0, 1));
ALTER TABLE provider_threads ADD COLUMN urgent INTEGER NOT NULL DEFAULT 0
    CHECK (urgent IN (0, 1));

UPDATE provider_threads
SET needs_action = 1
WHERE category = 'action_required'
    OR reason_codes LIKE '%"direct_request"%'
    OR reason_codes LIKE '%"request_language"%'
    OR reason_codes LIKE '%"deadline"%'
    OR reason_codes LIKE '%"deadline_language"%';

UPDATE provider_threads
SET urgent = 1
WHERE category = 'urgent';
