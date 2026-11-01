-- Logical model calls vs provider attempts.
--
-- model_calls stays the idempotency boundary (one row per logical call) and
-- now records the worker step and the call-level cost/ambiguity roll-up.
-- model_call_attempts is an append-only ledger with one row per provider
-- invocation; an ambiguous attempt may already have been charged even when a
-- later attempt completes the logical call.

ALTER TABLE model_calls
    ADD COLUMN IF NOT EXISTS step_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls
    ADD COLUMN IF NOT EXISTS total_cost_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE model_calls
    ADD COLUMN IF NOT EXISTS has_ambiguous_attempt BOOLEAN NOT NULL DEFAULT FALSE;

-- Existing rows predate the split. Their recorded cost is the best available
-- logical total, and AMBIGUOUS rows had exactly one (ambiguous) attempt.
UPDATE model_calls
   SET total_cost_cents = cost_cents
 WHERE total_cost_cents = 0 AND cost_cents <> 0;
UPDATE model_calls
   SET has_ambiguous_attempt = TRUE
 WHERE status = 'AMBIGUOUS';

CREATE TABLE IF NOT EXISTS model_call_attempts (
    attempt_id           TEXT PRIMARY KEY,
    call_id              TEXT NOT NULL REFERENCES model_calls(id) ON DELETE CASCADE,
    provider             TEXT NOT NULL DEFAULT '',
    model                TEXT NOT NULL DEFAULT '',
    status               TEXT NOT NULL,
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
    actual_cost_cents    BIGINT NOT NULL DEFAULT 0,
    currency             CHAR(3),
    input_tokens         INTEGER NOT NULL DEFAULT 0,
    output_tokens        INTEGER NOT NULL DEFAULT 0,
    prompt_hash          TEXT NOT NULL DEFAULT '',
    response_hash        TEXT NOT NULL DEFAULT '',
    error                TEXT NOT NULL DEFAULT '',
    started_at           TIMESTAMPTZ,
    ended_at             TIMESTAMPTZ,
    data                 JSONB NOT NULL DEFAULT '{}'::jsonb
);

ALTER TABLE model_call_attempts
    DROP CONSTRAINT IF EXISTS model_call_attempts_status_chk;
ALTER TABLE model_call_attempts
    ADD CONSTRAINT model_call_attempts_status_chk
        CHECK (status IN ('COMPLETED','AMBIGUOUS','FAILED_BEFORE_SEND'));

CREATE INDEX IF NOT EXISTS idx_model_call_attempts_call
    ON model_call_attempts (call_id, started_at);
CREATE INDEX IF NOT EXISTS idx_model_call_attempts_ambiguous
    ON model_call_attempts (call_id) WHERE status = 'AMBIGUOUS';
