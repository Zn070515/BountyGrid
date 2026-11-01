DROP INDEX IF EXISTS idx_model_call_attempts_ambiguous;
DROP INDEX IF EXISTS idx_model_call_attempts_call;
DROP TABLE IF EXISTS model_call_attempts;

ALTER TABLE model_calls DROP COLUMN IF EXISTS has_ambiguous_attempt;
ALTER TABLE model_calls DROP COLUMN IF EXISTS total_cost_cents;
ALTER TABLE model_calls DROP COLUMN IF EXISTS step_id;
