-- Phase B: timing breakdown — split latency_ms into queue + upstream + ttfb
-- so the Settings → Usage detail row can show where time was spent.
-- All four columns are nullable; legacy rows keep latency_ms as the only
-- truth, new rows populate the breakdown when the v1 handler captures it.
ALTER TABLE usage_log
    ADD COLUMN queue_ms       INT,
    ADD COLUMN upstream_ms    INT,
    ADD COLUMN ttfb_ms        INT,
    ADD COLUMN postprocess_ms INT;

-- Phase C: opt-in payload capture. Default off everywhere — capture is
-- explicit per team. Bodies live in a sidecar table because they're large
-- and we don't want them on the hot read path.
ALTER TABLE teams
    ADD COLUMN capture_payloads BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE usage_log_payloads (
    usage_id      BIGINT PRIMARY KEY REFERENCES usage_log(id) ON DELETE CASCADE,
    request_body  JSONB,
    response_body JSONB,
    captured_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX usage_log_payloads_captured_at ON usage_log_payloads(captured_at);
