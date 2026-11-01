DROP INDEX IF EXISTS idx_ledger_reverses;
DROP INDEX IF EXISTS idx_ledger_platform_occurred;
DROP INDEX IF EXISTS uq_ledger_external;
ALTER TABLE ledger_entries
    DROP COLUMN IF EXISTS notes,
    DROP COLUMN IF EXISTS reverses_id,
    DROP COLUMN IF EXISTS external_ref;
