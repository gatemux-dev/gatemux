-- 0003 design doc: logging callbacks
CREATE TABLE callback_runs (
    id BIGSERIAL PRIMARY KEY,
    callback_name TEXT NOT NULL,
    event_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    status TEXT NOT NULL,
    error TEXT,
    latency_ms INT,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX callback_runs_callback_sent_at ON callback_runs(callback_name, sent_at DESC);
