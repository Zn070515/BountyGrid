-- Core persistence invariants. Go remains the primary validator; these checks
-- stop obviously corrupt rows from entering PostgreSQL directly.

ALTER TABLE accounts
    ADD CONSTRAINT accounts_platform_chk CHECK (platform IN ('xianzhi','vulbox','butian'));

ALTER TABLE programs
    ADD CONSTRAINT programs_platform_chk CHECK (platform IN ('xianzhi','vulbox','butian')),
    ADD CONSTRAINT programs_status_chk CHECK (status IN ('RUNNING','PAUSED','UPCOMING','ENDED','UNKNOWN')),
    ADD CONSTRAINT programs_window_chk CHECK (status_valid_until > status_observed_at);

ALTER TABLE evidence
    ADD CONSTRAINT evidence_platform_chk CHECK (platform IN ('xianzhi','vulbox','butian')),
    ADD CONSTRAINT evidence_hash_chk CHECK (content_hash <> ''),
    ADD CONSTRAINT evidence_raw_ref_chk CHECK (raw_ref <> '');

ALTER TABLE observed_facts
    ADD CONSTRAINT facts_platform_chk CHECK (platform IN ('xianzhi','vulbox','butian')),
    ADD CONSTRAINT facts_subject_chk CHECK (subject IN ('ACCOUNT','PROGRAM','ASSET','PAYOUT')),
    ADD CONSTRAINT facts_confidence_chk CHECK (confidence >= 0 AND confidence <= 1),
    ADD CONSTRAINT facts_evidence_chk CHECK (jsonb_typeof(evidence_refs) = 'array' AND jsonb_array_length(evidence_refs) > 0);

ALTER TABLE derived_states
    ADD CONSTRAINT states_platform_chk CHECK (platform IN ('xianzhi','vulbox','butian')),
    ADD CONSTRAINT states_confidence_chk CHECK (confidence >= 0 AND confidence <= 1),
    ADD CONSTRAINT states_window_chk CHECK (valid_until > derived_at),
    ADD CONSTRAINT states_inputs_chk CHECK (jsonb_typeof(inputs) = 'array' AND jsonb_array_length(inputs) > 0);

ALTER TABLE action_decisions
    ADD CONSTRAINT decisions_decision_chk CHECK (decision IN ('ALLOW','DENY','REQUIRE_HUMAN','REQUIRE_MORE_EVIDENCE'));

ALTER TABLE competition_snapshots
    ADD CONSTRAINT competition_platform_chk CHECK (platform IN ('xianzhi','vulbox','butian')),
    ADD CONSTRAINT competition_confidence_chk CHECK (confidence >= 0 AND confidence <= 1);

ALTER TABLE opportunities
    ADD CONSTRAINT opportunities_confidence_chk CHECK (confidence >= 0 AND confidence <= 1);

ALTER TABLE submissions
    ADD CONSTRAINT submissions_receipt_chk CHECK (receipt <> ''),
    ADD CONSTRAINT submissions_approval_chk CHECK (approval_id <> '');

ALTER TABLE ledger_entries
    ADD CONSTRAINT ledger_platform_chk CHECK (platform IN ('xianzhi','vulbox','butian')),
    ADD CONSTRAINT ledger_kind_chk CHECK (kind IN ('EXPECTED','SUBMITTED','ACCEPTED','PAID','FEE','TAX','REFUND')),
    ADD CONSTRAINT ledger_amount_chk CHECK (amount_cents >= 0),
    ADD CONSTRAINT ledger_currency_chk CHECK (currency ~ '^[A-Z]{3}$');
