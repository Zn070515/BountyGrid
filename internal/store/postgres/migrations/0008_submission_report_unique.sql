-- One report may have only one real submission. Dry-run rows are excluded so
-- they do not block a later approved real submission.
CREATE UNIQUE INDEX IF NOT EXISTS uq_submissions_report
    ON submissions (report_id)
    WHERE status <> 'DRY_RUN';
