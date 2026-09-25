-- Durable intent precedes upstream IO; completion and settlement commit together.
CREATE TABLE inference_journal (
    request_id TEXT PRIMARY KEY CHECK (octet_length(request_id) BETWEEN 1 AND 256),
    team_id BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    entry JSONB NOT NULL CHECK (octet_length(entry::text) <= 32768),
    upstream_started BOOLEAN NOT NULL DEFAULT FALSE,
    recover_after TIMESTAMPTZ NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','complete')),
    usage_id BIGINT REFERENCES usage_log(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ
);
CREATE INDEX inference_journal_recovery ON inference_journal(recover_after, request_id) WHERE state='pending';
ALTER TABLE usage_log ADD COLUMN accounting_id TEXT UNIQUE;
ALTER TABLE usage_log ADD COLUMN accounting_state TEXT NOT NULL DEFAULT 'legacy'
    CHECK (accounting_state IN ('legacy','priced','estimated','unknown','unpriced','not_billable'));

-- Existing history is not rewritten or deduplicated. Its evidence remains legacy.
