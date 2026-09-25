-- 0007 design doc: smart routing
ALTER TABLE model_aliases
    ADD COLUMN strategy TEXT NOT NULL DEFAULT 'priority',
    ADD COLUMN strategy_options JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE deployments
    ADD COLUMN tags TEXT[] NOT NULL DEFAULT '{}'::text[];
CREATE INDEX deployments_tags_gin ON deployments USING GIN(tags);
