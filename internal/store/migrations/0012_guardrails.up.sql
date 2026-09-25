-- 0004 design doc: guardrails
CREATE TABLE guardrail_decisions (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    team_id BIGINT REFERENCES teams(id) ON DELETE SET NULL,
    alias TEXT,
    guardrail_name TEXT NOT NULL,
    phase TEXT NOT NULL,
    decision TEXT NOT NULL,
    severity TEXT,
    reason TEXT,
    tags JSONB,
    latency_ms INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX guardrail_decisions_request ON guardrail_decisions(request_id);
CREATE INDEX guardrail_decisions_team_ts ON guardrail_decisions(team_id, created_at DESC);

ALTER TABLE model_aliases
    ADD COLUMN guardrails JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE teams
    ADD COLUMN guardrails JSONB NOT NULL DEFAULT '[]'::jsonb;
