-- One human approval authorizes exactly one report submission.
CREATE UNIQUE INDEX IF NOT EXISTS uq_submissions_approval
    ON submissions (approval_id);

CREATE INDEX IF NOT EXISTS idx_submissions_report
    ON submissions (report_id, submitted_at DESC);
