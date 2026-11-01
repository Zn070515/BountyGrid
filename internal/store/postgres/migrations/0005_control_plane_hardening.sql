-- Control-plane hardening: competition expiry, durable execution receipts,
-- and an exclusive active research task per program.

ALTER TABLE competition_snapshots
    ADD COLUMN IF NOT EXISTS valid_until TIMESTAMPTZ;

-- Existing snapshots keep influencing scheduling only for a bounded window.
UPDATE competition_snapshots
SET valid_until = observed_at + INTERVAL '30 days'
WHERE valid_until IS NULL;

CREATE TABLE IF NOT EXISTS execution_receipts (
    id              TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    decision_id     TEXT NOT NULL,
    tool_name       TEXT NOT NULL,
    action          TEXT NOT NULL,
    program_id      TEXT NOT NULL,
    status          TEXT NOT NULL,
    output          TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    started_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    data            JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_receipts_program ON execution_receipts (program_id, started_at DESC);

-- At most one QUEUED task per program. Older queued tasks are superseded by
-- PutResearchTask in the same transaction, so history is never overwritten.
CREATE UNIQUE INDEX IF NOT EXISTS uq_research_tasks_active
    ON research_tasks (program_id) WHERE status = 'QUEUED';

CREATE INDEX IF NOT EXISTS idx_research_tasks_program_created
    ON research_tasks (program_id, created_at DESC);
