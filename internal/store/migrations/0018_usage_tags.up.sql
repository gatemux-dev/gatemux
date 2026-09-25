-- Per-request tags from the X-Gatemux-Tags header. Used both for routing
-- (smart routing tagged-strategy, doc 0007) and for spend grouping
-- for request-level attribution and reporting.
ALTER TABLE usage_log
    ADD COLUMN request_tags TEXT[] NOT NULL DEFAULT '{}'::text[];
CREATE INDEX usage_log_request_tags_gin ON usage_log USING GIN(request_tags);
