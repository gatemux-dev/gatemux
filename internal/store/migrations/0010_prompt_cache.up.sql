-- 0001 design doc: per-alias prompt cache
ALTER TABLE model_aliases
    ADD COLUMN cache_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN cache_ttl_seconds INT NOT NULL DEFAULT 0;

ALTER TABLE usage_log
    ADD COLUMN cached BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN cache_origin_request_id TEXT;
