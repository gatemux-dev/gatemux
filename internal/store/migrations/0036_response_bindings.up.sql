-- Metadata only: upstream owns response content. IDs remain tenant/principal-
-- scoped across replicas, and no upstream ID is accepted without a binding.
CREATE TABLE response_bindings (
  id TEXT PRIMARY KEY,
  team_id BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  owner TEXT NOT NULL,
  customer_external_id TEXT NOT NULL DEFAULT '',
  alias TEXT NOT NULL,
  deployment TEXT NOT NULL,
  target_fingerprint TEXT NOT NULL,
  upstream_id TEXT NOT NULL DEFAULT '',
  previous_id TEXT NOT NULL DEFAULT '',
  total_tokens BIGINT NOT NULL DEFAULT 0 CHECK (total_tokens >= 0),
  status TEXT NOT NULL DEFAULT 'in_progress',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '30 days'
);
CREATE INDEX response_bindings_expiry ON response_bindings (expires_at);
CREATE INDEX response_bindings_owner ON response_bindings (team_id, owner);
