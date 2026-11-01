-- Model call state machine. A logical call is reserved before a provider is
-- chosen, marked STARTED immediately before invocation, and finished as
-- COMPLETED, AMBIGUOUS or FAILED_BEFORE_SEND. Only FAILED_BEFORE_SEND may be
-- reclaimed/retried; the other terminal states may already have been charged.

ALTER TABLE model_calls
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'RESERVED';

UPDATE model_calls SET status = 'RESERVED' WHERE status IS NULL OR status = '';

ALTER TABLE model_calls
    DROP CONSTRAINT IF EXISTS model_calls_status_chk;
ALTER TABLE model_calls
    ADD CONSTRAINT model_calls_status_chk
        CHECK (status IN ('RESERVED','STARTED','COMPLETED','AMBIGUOUS','FAILED_BEFORE_SEND'));

CREATE INDEX IF NOT EXISTS idx_model_calls_status
    ON model_calls (status, updated_at DESC);
