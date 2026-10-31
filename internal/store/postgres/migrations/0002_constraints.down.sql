ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_platform_chk;
ALTER TABLE programs
    DROP CONSTRAINT IF EXISTS programs_platform_chk,
    DROP CONSTRAINT IF EXISTS programs_status_chk,
    DROP CONSTRAINT IF EXISTS programs_window_chk;
ALTER TABLE evidence
    DROP CONSTRAINT IF EXISTS evidence_platform_chk,
    DROP CONSTRAINT IF EXISTS evidence_hash_chk,
    DROP CONSTRAINT IF EXISTS evidence_raw_ref_chk;
ALTER TABLE observed_facts
    DROP CONSTRAINT IF EXISTS facts_platform_chk,
    DROP CONSTRAINT IF EXISTS facts_subject_chk,
    DROP CONSTRAINT IF EXISTS facts_confidence_chk,
    DROP CONSTRAINT IF EXISTS facts_evidence_chk;
ALTER TABLE derived_states
    DROP CONSTRAINT IF EXISTS states_platform_chk,
    DROP CONSTRAINT IF EXISTS states_confidence_chk,
    DROP CONSTRAINT IF EXISTS states_window_chk,
    DROP CONSTRAINT IF EXISTS states_inputs_chk;
ALTER TABLE action_decisions DROP CONSTRAINT IF EXISTS decisions_decision_chk;
ALTER TABLE competition_snapshots
    DROP CONSTRAINT IF EXISTS competition_platform_chk,
    DROP CONSTRAINT IF EXISTS competition_confidence_chk;
ALTER TABLE opportunities DROP CONSTRAINT IF EXISTS opportunities_confidence_chk;
ALTER TABLE submissions
    DROP CONSTRAINT IF EXISTS submissions_receipt_chk,
    DROP CONSTRAINT IF EXISTS submissions_approval_chk;
ALTER TABLE ledger_entries
    DROP CONSTRAINT IF EXISTS ledger_platform_chk,
    DROP CONSTRAINT IF EXISTS ledger_kind_chk,
    DROP CONSTRAINT IF EXISTS ledger_amount_chk,
    DROP CONSTRAINT IF EXISTS ledger_currency_chk;
