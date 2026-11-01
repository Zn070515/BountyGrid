-- A report can have only one submission attempt in flight. The intent is
-- reserved before the platform call and removed by RecordSubmission.
CREATE TABLE IF NOT EXISTS submission_intents (
    report_id    TEXT PRIMARY KEY,
    approval_id  TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL
);
