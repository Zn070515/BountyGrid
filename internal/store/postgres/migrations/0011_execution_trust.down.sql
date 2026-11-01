DROP INDEX IF EXISTS idx_receipts_ambiguous;
DROP INDEX IF EXISTS idx_model_calls_task;
DROP TABLE IF EXISTS model_calls;
DROP INDEX IF EXISTS idx_human_reviews_finding;
DROP TABLE IF EXISTS human_reviews;
DROP INDEX IF EXISTS idx_submission_intents_status;
ALTER TABLE submission_intents DROP CONSTRAINT IF EXISTS submission_intents_status_chk;
ALTER TABLE submission_intents DROP COLUMN IF EXISTS status;
