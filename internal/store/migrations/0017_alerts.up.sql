-- 0009 design doc: operational UX — alert rules + events
CREATE TABLE alert_rules (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    scope_type TEXT NOT NULL,
    scope_id BIGINT,
    trigger_type TEXT NOT NULL,
    threshold_options JSONB NOT NULL DEFAULT '{}'::jsonb,
    channel_type TEXT NOT NULL,
    channel_target TEXT NOT NULL,
    cooldown_seconds INT NOT NULL DEFAULT 300,
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX alert_rules_scope ON alert_rules(scope_type, scope_id);

CREATE TABLE alert_events (
    id BIGSERIAL PRIMARY KEY,
    rule_id BIGINT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    fired_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    delivery_status TEXT NOT NULL DEFAULT 'pending',
    delivery_error TEXT
);
CREATE INDEX alert_events_rule_fired ON alert_events(rule_id, fired_at DESC);
