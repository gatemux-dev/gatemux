-- 0005 design doc: customer (end-user) budgets
CREATE TABLE customers (
    id BIGSERIAL PRIMARY KEY,
    team_id BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    external_id TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    usd_limit_cents BIGINT,
    period TEXT NOT NULL DEFAULT 'month',
    rpm INT,
    tpm INT,
    archived_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (team_id, external_id)
);
CREATE INDEX customers_team_external ON customers(team_id, external_id);

ALTER TABLE teams
    ADD COLUMN customer_creation_mode TEXT NOT NULL DEFAULT 'auto_create';

ALTER TABLE usage_log
    ADD COLUMN customer_id BIGINT REFERENCES customers(id) ON DELETE SET NULL,
    ADD COLUMN customer_external_id TEXT;
CREATE INDEX usage_log_customer ON usage_log(customer_id, ts DESC);
