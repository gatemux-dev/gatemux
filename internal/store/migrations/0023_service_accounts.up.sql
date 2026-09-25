-- Phase 2: service accounts. Non-human principals scoped to a team,
-- distinct from human users (no email/password, no session, no role).
-- Each SA can own virtual keys and carries its own budget so bot
-- traffic can be gated separately from human traffic.
CREATE TABLE service_accounts (
    id              BIGSERIAL PRIMARY KEY,
    team_id         BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT,
    usd_limit_cents BIGINT,
    period          TEXT NOT NULL DEFAULT 'month',
    rpm             INT,
    tpm             INT,
    created_by      BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at     TIMESTAMPTZ,
    UNIQUE (team_id, name)
);
CREATE INDEX service_accounts_team_id_idx ON service_accounts(team_id);

-- A virtual key is owned by exactly one of: human user, service account,
-- or the team (legacy "team key"). user_id and service_account_id are
-- mutually exclusive at the application level; the partial check
-- enforces it cheaply without a CHECK that breaks legacy NULL/NULL rows.
ALTER TABLE virtual_keys
    ADD COLUMN service_account_id BIGINT REFERENCES service_accounts(id) ON DELETE CASCADE;
CREATE INDEX virtual_keys_service_account_id_idx ON virtual_keys(service_account_id);

-- Forbid attaching a key to both a user and an SA at once. Existing
-- rows have NULL/NULL or NULL/something, so backfill is unnecessary.
ALTER TABLE virtual_keys
    ADD CONSTRAINT virtual_keys_owner_xor
        CHECK (user_id IS NULL OR service_account_id IS NULL);

-- usage_log gains the SA dimension so the existing per-row attribution
-- works when a key is owned by an SA.
ALTER TABLE usage_log
    ADD COLUMN service_account_id BIGINT REFERENCES service_accounts(id) ON DELETE SET NULL;
CREATE INDEX usage_log_service_account_id_idx ON usage_log(service_account_id);
