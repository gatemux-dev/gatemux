CREATE TABLE teams (
    id              BIGSERIAL PRIMARY KEY,
    slug            TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    usd_limit_cents BIGINT,
    period          TEXT NOT NULL DEFAULT 'month',
    allowed_models  JSONB NOT NULL DEFAULT '["*"]'::jsonb,
    rpm             INT,
    tpm             INT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at     TIMESTAMPTZ
);

CREATE TABLE virtual_keys (
    id                     BIGSERIAL PRIMARY KEY,
    team_id                BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    key_hash               BYTEA NOT NULL UNIQUE,
    key_prefix             TEXT NOT NULL,
    name                   TEXT NOT NULL DEFAULT '',
    scoped_usd_limit_cents BIGINT,
    scoped_rpm             INT,
    scoped_tpm             INT,
    last_used_at           TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at             TIMESTAMPTZ
);

CREATE INDEX virtual_keys_team_id_idx ON virtual_keys(team_id);

CREATE TABLE deployments (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    provider_type  TEXT NOT NULL,
    upstream_model TEXT NOT NULL,
    credential_ref TEXT NOT NULL,
    base_url       TEXT,
    region         TEXT,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    weight         INT NOT NULL DEFAULT 1,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE model_aliases (
    id         BIGSERIAL PRIMARY KEY,
    alias      TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE alias_deployments (
    alias_id      BIGINT NOT NULL REFERENCES model_aliases(id) ON DELETE CASCADE,
    deployment_id BIGINT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    priority      INT NOT NULL DEFAULT 0,
    weight        INT NOT NULL DEFAULT 1,
    PRIMARY KEY (alias_id, deployment_id)
);

CREATE TABLE usage_log (
    id                BIGSERIAL PRIMARY KEY,
    team_id           BIGINT NOT NULL REFERENCES teams(id),
    key_id            BIGINT REFERENCES virtual_keys(id),
    alias             TEXT NOT NULL,
    deployment_id     BIGINT REFERENCES deployments(id),
    request_id        TEXT NOT NULL,
    model_requested   TEXT NOT NULL,
    model_used        TEXT,
    prompt_tokens     INT NOT NULL DEFAULT 0,
    completion_tokens INT NOT NULL DEFAULT 0,
    total_tokens      INT NOT NULL DEFAULT 0,
    cost_cents        BIGINT NOT NULL DEFAULT 0,
    latency_ms        INT NOT NULL DEFAULT 0,
    status_code       INT NOT NULL DEFAULT 0,
    error             TEXT,
    ts                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX usage_log_team_ts_idx ON usage_log(team_id, ts DESC);
CREATE INDEX usage_log_ts_idx      ON usage_log(ts DESC);

CREATE TABLE pricing (
    provider_type            TEXT NOT NULL,
    upstream_model           TEXT NOT NULL,
    input_per_million_cents  BIGINT NOT NULL,
    output_per_million_cents BIGINT NOT NULL,
    effective_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider_type, upstream_model, effective_at)
);
