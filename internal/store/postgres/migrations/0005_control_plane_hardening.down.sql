DROP INDEX IF EXISTS idx_research_tasks_program_created;
DROP INDEX IF EXISTS uq_research_tasks_active;
DROP TABLE IF EXISTS execution_receipts;
ALTER TABLE competition_snapshots DROP COLUMN IF EXISTS valid_until;
