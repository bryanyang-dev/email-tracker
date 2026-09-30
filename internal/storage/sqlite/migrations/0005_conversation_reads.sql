ALTER TABLE provider_threads ADD COLUMN ai_status TEXT NOT NULL DEFAULT 'rules'
    CHECK (ai_status IN ('pending', 'applied', 'rules', 'unavailable', 'failed'));
