DROP INDEX IF EXISTS idx_model_calls_status;
ALTER TABLE model_calls DROP CONSTRAINT IF EXISTS model_calls_status_chk;
ALTER TABLE model_calls DROP COLUMN IF EXISTS status;
