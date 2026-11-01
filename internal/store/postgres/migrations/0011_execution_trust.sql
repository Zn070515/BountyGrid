-- Execution-trust hardening: durable human reviews, model call receipts, and
-- submission intent states that distinguish "never sent" from "might have been
-- received".

ALTER TABLE submission_intents
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'PENDING';

-- Existing intents were created before the status split. They are PENDING
-- (reserved but not proven sent), which keeps the TTL reclaim behavior.
UPDATE submission_intents SET status = 'PENDING' WHERE status = '';

ALTER TABLE submission_intents
    DROP CONSTRAINT IF EXISTS submission_intents_status_chk;
ALTER TABLE submission_intents
    ADD CONSTRAINT submission_intents_status_chk
        CHECK (status IN ('PENDING','SENT','CONFIRMED','AMBIGUOUS','FAILED_BEFORE_SEND'));

CREATE INDEX IF NOT EXISTS idx_submission_intents_status
    ON submission_intents (status, started_at);

CREATE TABLE IF NOT EXISTS human_reviews (
    id          TEXT PRIMARY KEY,
    finding_id  TEXT NOT NULL,
    reviewer    TEXT NOT NULL,
    reviewed_at TIMESTAMPTZ NOT NULL,
    verdict     TEXT NOT NULL,
    notes_hash  TEXT NOT NULL,
    evidence    JSONB NOT NULL DEFAULT '[]',
    data        JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_human_reviews_finding
    ON human_reviews (finding_id, reviewed_at DESC);

ALTER TABLE human_reviews
    DROP CONSTRAINT IF EXISTS human_reviews_verdict_chk;
ALTER TABLE human_reviews
    ADD CONSTRAINT human_reviews_verdict_chk CHECK (verdict IN ('APPROVED','REJECTED'));

-- A model call receipt reserves the logical call before any provider is
-- invoked. Response content is never stored; only hashes/usage/cost.
CREATE TABLE IF NOT EXISTS model_calls (
    id            TEXT PRIMARY KEY,
    task_id       TEXT,
    purpose       TEXT NOT NULL,
    data_class    TEXT NOT NULL,
    provider      TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '',
    prompt_hash   TEXT NOT NULL DEFAULT '',
    response_hash TEXT NOT NULL DEFAULT '',
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost_cents    BIGINT NOT NULL DEFAULT 0,
    currency      CHAR(3),
    error         TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMPTZ,
    ended_at      TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    data          JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_model_calls_task
    ON model_calls (task_id, updated_at DESC);

-- Execution receipts keep their idempotency key but now allow the AMBIGUOUS
-- status (a side effect that may have happened). The status column is TEXT, so
-- no constraint change is required; the partial index just makes manual
-- reconciliation queries cheap.
CREATE INDEX IF NOT EXISTS idx_receipts_ambiguous
    ON execution_receipts (updated_at DESC) WHERE status = 'AMBIGUOUS';
