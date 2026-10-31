-- Supersede invalidation queries derived_states by input fact ID; index the
-- JSONB fact-ID array.
CREATE INDEX IF NOT EXISTS idx_states_inputs ON derived_states USING GIN (inputs);
