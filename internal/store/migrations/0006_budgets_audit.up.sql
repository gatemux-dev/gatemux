CREATE TABLE model_budgets (
    alias      TEXT PRIMARY KEY,
    limit_cents BIGINT NOT NULL,
    period     TEXT NOT NULL DEFAULT 'month',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE budget_reservations (
    request_id            TEXT PRIMARY KEY,
    team_id               BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    alias                 TEXT NOT NULL,
    estimated_cost_cents  BIGINT NOT NULL DEFAULT 0,
    settled_cost_cents    BIGINT NOT NULL DEFAULT 0,
    status                TEXT NOT NULL DEFAULT 'reserved',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    settled_at            TIMESTAMPTZ
);

CREATE INDEX budget_reservations_team_created_at_idx ON budget_reservations(team_id, created_at DESC);
CREATE INDEX budget_reservations_alias_created_at_idx ON budget_reservations(alias, created_at DESC);

CREATE TABLE audit_log (
    id            BIGSERIAL PRIMARY KEY,
    actor_type    TEXT NOT NULL,
    actor_id      TEXT NOT NULL,
    action        TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    metadata      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX audit_log_created_at_idx ON audit_log(created_at DESC);
CREATE INDEX audit_log_resource_idx   ON audit_log(resource_type, resource_id, created_at DESC);
