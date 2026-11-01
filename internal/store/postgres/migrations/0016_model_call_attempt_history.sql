-- Preserve the append-only provider-attempt ledger when a logical call is
-- reclaimed. Reclaim deletes the failed-before-send logical row (and, through
-- the foreign key, its current attempts); the history table keeps the old
-- attempts so reconciliation/audit can still prove what each provider may have
-- been asked to do.
CREATE TABLE IF NOT EXISTS model_call_attempt_history (
    attempt_id           TEXT PRIMARY KEY,
    call_id              TEXT NOT NULL,
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
    data                 JSONB NOT NULL DEFAULT '{}'::jsonb,
    archived_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_model_call_attempt_history_call
    ON model_call_attempt_history (call_id, started_at);
