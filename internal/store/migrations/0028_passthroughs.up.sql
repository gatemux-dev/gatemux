-- Generic passthrough endpoints. A passthrough is "expose this base URL
-- under /passthrough/{name}/* with this auth header swapped in." It's
-- intentionally separate from deployments because passthroughs don't
-- have to be a model — observability proxies, file APIs, anything an
-- operator wants to fan out behind the gateway's auth/audit/rate-limit.
CREATE TABLE IF NOT EXISTS passthroughs (
    id                BIGSERIAL PRIMARY KEY,
    name              TEXT NOT NULL UNIQUE,
    target_url        TEXT NOT NULL,
    -- auth_header is the header name to set on the forwarded request.
    -- Empty string means don't attach any auth header (target accepts
    -- anonymous, or auth comes from another header set on the client).
    auth_header       TEXT NOT NULL DEFAULT '',
    -- auth_value_env names the env var holding the credential. Lookup
    -- happens at request time so a rotated env var doesn't need a DB
    -- update. Empty when auth_header is empty.
    auth_value_env    TEXT NOT NULL DEFAULT '',
    -- auth_value_prefix is prepended to the env-var value (e.g.
    -- "Bearer " for OpenAI, empty for X-API-Key style headers).
    auth_value_prefix TEXT NOT NULL DEFAULT '',
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    archived_at       TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS passthroughs_enabled_idx
    ON passthroughs (enabled)
    WHERE archived_at IS NULL;
