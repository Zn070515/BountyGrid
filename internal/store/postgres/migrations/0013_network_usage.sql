-- Durable daily network usage. The capability broker counts every request per
-- account/program/interface so separate executions (or an Egress restart) cannot
-- reset the platform's PerInterfaceDailyLimits. A row exists only for a counted
-- business day; increment/decrement is exactly one atomic upsert.
CREATE TABLE IF NOT EXISTS network_usage (
    account_id    TEXT NOT NULL,
    program_id    TEXT NOT NULL,
    interface     TEXT NOT NULL,
    business_date DATE NOT NULL,
    count         BIGINT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (account_id, program_id, interface, business_date)
);
