ALTER TABLE ledger_entries DROP CONSTRAINT IF EXISTS ledger_kind_chk;
-- Preserve existing REVERSAL rows on rollback; new reversals are blocked.
ALTER TABLE ledger_entries
    ADD CONSTRAINT ledger_kind_chk
    CHECK (kind IN ('EXPECTED','SUBMITTED','ACCEPTED','PAID','FEE','TAX','REFUND')) NOT VALID;
