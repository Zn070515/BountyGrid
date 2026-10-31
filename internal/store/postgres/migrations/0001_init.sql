-- BountyGrid initial schema.
-- JSONB columns keep platform-native snapshots; relational columns are for
-- querying/audit, not for re-encoding platform policy.

CREATE TABLE IF NOT EXISTS accounts (
    id                       TEXT PRIMARY KEY,
    platform                 TEXT NOT NULL,
    registered               BOOLEAN NOT NULL DEFAULT FALSE,
    identity_verified        BOOLEAN,
    level                    TEXT NOT NULL DEFAULT '',
    reputation_score         DOUBLE PRECISION,
    compliance_exam_completed BOOLEAN,
    practical_certification  BOOLEAN,
    bank_bound               BOOLEAN,
    wechat_bound             BOOLEAN,
    data                     JSONB NOT NULL,
    observed_at              TIMESTAMPTZ NOT NULL,
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS programs (
    id                    TEXT PRIMARY KEY,
    platform              TEXT NOT NULL,
    native_id             TEXT,
    name                  TEXT NOT NULL,
    type                  TEXT NOT NULL,
    status                TEXT NOT NULL,
    status_reason         TEXT,
    status_observed_at    TIMESTAMPTZ NOT NULL,
    status_valid_until    TIMESTAMPTZ NOT NULL,
    next_check_at         TIMESTAMPTZ,
    data                  JSONB NOT NULL,
    observed_at           TIMESTAMPTZ NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS evidence (
    id            TEXT PRIMARY KEY,
    platform      TEXT NOT NULL,
    source_type   TEXT NOT NULL,
    transport     TEXT NOT NULL,
    url           TEXT NOT NULL,
    page_title    TEXT,
    captured_at   TIMESTAMPTZ NOT NULL,
    content_hash  TEXT NOT NULL,
    raw_ref       TEXT NOT NULL,
    redactions    JSONB NOT NULL DEFAULT '[]',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS observed_facts (
    id                 TEXT PRIMARY KEY,
    subject            TEXT NOT NULL,
    platform           TEXT NOT NULL,
    account_id         TEXT,
    program_id         TEXT,
    asset_id           TEXT,
    predicate          TEXT NOT NULL,
    value              JSONB NOT NULL,
    unit               TEXT,
    observed_at        TIMESTAMPTZ NOT NULL,
    evidence_refs      JSONB NOT NULL,
    confidence         DOUBLE PRECISION NOT NULL,
    extractor_version  TEXT NOT NULL,
    superseded_by      TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS derived_states (
    id            TEXT PRIMARY KEY,
    subject       TEXT NOT NULL,
    platform      TEXT NOT NULL,
    account_id    TEXT,
    program_id    TEXT,
    state         JSONB NOT NULL,
    inputs        JSONB NOT NULL,
    observed_at   TIMESTAMPTZ NOT NULL,
    derived_at    TIMESTAMPTZ NOT NULL,
    valid_until   TIMESTAMPTZ NOT NULL,
    confidence    DOUBLE PRECISION NOT NULL,
    rule_version  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS action_decisions (
    id              TEXT PRIMARY KEY,
    request_id      TEXT NOT NULL,
    program_id      TEXT NOT NULL,
    action          TEXT NOT NULL,
    decision        TEXT NOT NULL,
    reasons         JSONB NOT NULL DEFAULT '[]',
    gates_evaluated JSONB NOT NULL DEFAULT '[]',
    policy_version  TEXT NOT NULL,
    decided_at      TIMESTAMPTZ NOT NULL,
    human_approval  JSONB,
    expires_at      TIMESTAMPTZ,
    constraints     JSONB,
    data            JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS competition_snapshots (
    id                  TEXT PRIMARY KEY,
    platform            TEXT NOT NULL,
    scope               TEXT NOT NULL,
    program_id          TEXT,
    observed_at         TIMESTAMPTZ NOT NULL,
    top1_share          DOUBLE PRECISION,
    top3_share          DOUBLE PRECISION,
    top10_share         DOUBLE PRECISION,
    duplicate_pressure  DOUBLE PRECISION,
    saturation          DOUBLE PRECISION,
    confidence          DOUBLE PRECISION NOT NULL,
    data                JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS opportunities (
    id                       TEXT PRIMARY KEY,
    program_id               TEXT NOT NULL,
    asset_id                 TEXT,
    expected_return          DOUBLE PRECISION,
    expected_return_per_cost DOUBLE PRECISION,
    competition_penalty      DOUBLE PRECISION NOT NULL DEFAULT 0,
    confidence               DOUBLE PRECISION NOT NULL,
    evaluated_at             TIMESTAMPTZ NOT NULL,
    data                     JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS research_tasks (
    id             TEXT PRIMARY KEY,
    opportunity_id TEXT NOT NULL,
    program_id     TEXT NOT NULL,
    asset_id       TEXT,
    status         TEXT NOT NULL,
    budget_cents   BIGINT,
    currency       CHAR(3),
    created_at     TIMESTAMPTZ NOT NULL,
    expires_at     TIMESTAMPTZ,
    data           JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS findings (
    id         TEXT PRIMARY KEY,
    task_id    TEXT NOT NULL,
    program_id TEXT NOT NULL,
    asset_id   TEXT,
    title      TEXT NOT NULL,
    vuln_type  TEXT,
    severity   TEXT,
    status     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    data       JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS verifications (
    id              TEXT PRIMARY KEY,
    finding_id      TEXT NOT NULL,
    verdict         TEXT NOT NULL,
    reproduced      BOOLEAN NOT NULL DEFAULT FALSE,
    in_scope        BOOLEAN NOT NULL DEFAULT FALSE,
    impact_verified BOOLEAN NOT NULL DEFAULT FALSE,
    data_minimized  BOOLEAN NOT NULL DEFAULT FALSE,
    duplicate_risk  DOUBLE PRECISION NOT NULL DEFAULT 0,
    verified_by     TEXT NOT NULL,
    verified_at     TIMESTAMPTZ NOT NULL,
    data            JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS report_drafts (
    id         TEXT PRIMARY KEY,
    finding_id TEXT NOT NULL,
    program_id TEXT NOT NULL,
    status     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    data       JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS submissions (
    id            TEXT PRIMARY KEY,
    report_id     TEXT NOT NULL,
    program_id    TEXT NOT NULL,
    submitted_at  TIMESTAMPTZ NOT NULL,
    platform_url  TEXT,
    receipt       TEXT NOT NULL,
    status        TEXT NOT NULL,
    approval_id   TEXT NOT NULL,
    data          JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS ledger_entries (
    id            TEXT PRIMARY KEY,
    platform      TEXT NOT NULL,
    program_id    TEXT NOT NULL,
    finding_id    TEXT,
    submission_id TEXT,
    kind          TEXT NOT NULL,
    amount_cents  BIGINT NOT NULL,
    currency      CHAR(3) NOT NULL,
    status        TEXT NOT NULL,
    occurred_at   TIMESTAMPTZ NOT NULL,
    data          JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
    id          BIGSERIAL PRIMARY KEY,
    event_type  TEXT NOT NULL,
    entity_type TEXT,
    entity_id   TEXT,
    actor_kind  TEXT,
    actor_id    TEXT,
    payload     JSONB NOT NULL DEFAULT '{}',
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_programs_platform_status ON programs (platform, status);
CREATE INDEX IF NOT EXISTS idx_programs_next_check ON programs (next_check_at);
CREATE INDEX IF NOT EXISTS idx_evidence_platform_captured ON evidence (platform, captured_at DESC);
CREATE INDEX IF NOT EXISTS idx_facts_program_predicate ON observed_facts (program_id, predicate);
CREATE INDEX IF NOT EXISTS idx_facts_observed_at ON observed_facts (observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_states_program ON derived_states (program_id, valid_until);
CREATE INDEX IF NOT EXISTS idx_decisions_program ON action_decisions (program_id, decided_at DESC);
CREATE INDEX IF NOT EXISTS idx_opportunities_program ON opportunities (program_id, evaluated_at DESC);
CREATE INDEX IF NOT EXISTS idx_findings_program ON findings (program_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_submissions_program ON submissions (program_id, submitted_at DESC);
CREATE INDEX IF NOT EXISTS idx_ledger_program ON ledger_entries (program_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_entity ON audit_events (entity_type, entity_id, recorded_at DESC);
