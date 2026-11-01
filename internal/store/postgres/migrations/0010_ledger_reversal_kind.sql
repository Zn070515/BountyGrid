-- Ledger v1 added the REVERSAL kind; update the original check constraint.
ALTER TABLE ledger_entries DROP CONSTRAINT IF EXISTS ledger_kind_chk;
ALTER TABLE ledger_entries
    ADD CONSTRAINT ledger_kind_chk
    CHECK (kind IN ('EXPECTED','SUBMITTED','ACCEPTED','PAID','FEE','TAX','REFUND','REVERSAL'));
