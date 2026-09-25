-- Stable policy IDs retain in-flight memberships across clear/reconfigure.
CREATE TABLE routing_concurrency_limits (
    id BIGSERIAL PRIMARY KEY,
    scope TEXT NOT NULL CHECK (scope IN ('model', 'provider')),
    subject TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 256),
    max_parallel_requests INT CHECK (max_parallel_requests > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (scope, subject)
);
