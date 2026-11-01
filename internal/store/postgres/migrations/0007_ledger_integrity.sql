-- Ledger integrity: external references make imported cash events idempotent;
-- reversals point at the entry they correct. Amounts remain non-negative and
-- entries remain append-only.
ALTER TABLE ledger_entries
    ADD COLUMN IF NOT EXISTS external_ref TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS reverses_id  TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS notes        TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS uq_ledger_external
    ON ledger_entries (platform, kind, external_ref)
    WHERE external_ref <> '';

CREATE INDEX IF NOT EXISTS idx_ledger_platform_occurred
    ON ledger_entries (platform, occurred_at DESC);

CREATE INDEX IF NOT EXISTS idx_ledger_reverses
    ON ledger_entries (reverses_id)
    WHERE reverses_id <> '';
