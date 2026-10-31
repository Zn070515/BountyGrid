CREATE TABLE IF NOT EXISTS consumed_approvals (
    approval_id  TEXT PRIMARY KEY,
    consumed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
