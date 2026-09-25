-- Phase 3: provider health history. Sampler writes one row per
-- deployment every ~30s; the Settings → Providers card reads the
-- most recent N to draw a sparkline. The schema is deliberately tiny
-- (no JSONB) so a million rows still queries fast.
CREATE TABLE provider_health_samples (
    id              BIGSERIAL PRIMARY KEY,
    deployment_name TEXT NOT NULL,
    sampled_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ready           BOOLEAN NOT NULL,
    circuit         TEXT NOT NULL,
    consecutive_failures INT NOT NULL,
    rpm_5m          REAL,
    error_pct_5m    REAL,
    p50_ms          REAL,
    p95_ms          REAL
);
CREATE INDEX provider_health_samples_dep_at ON provider_health_samples(deployment_name, sampled_at DESC);
