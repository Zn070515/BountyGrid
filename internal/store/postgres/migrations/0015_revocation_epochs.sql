-- Durable per-program revocation epoch. An execution grant captures the epoch
-- when it is built; bumping the row invalidates every outstanding grant for the
-- program, and the epoch survives a process restart. The first bump inserts
-- epoch 1 so an unseen program is epoch 0 and a stamped grant of 0 still fails
-- closed after any revocation.
CREATE TABLE IF NOT EXISTS revocation_epochs (
    program_id TEXT PRIMARY KEY,
    epoch      BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);
