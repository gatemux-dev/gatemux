ALTER TABLE deployments ADD COLUMN streaming JSONB;
ALTER TABLE deployments ADD CONSTRAINT deployments_streaming_object CHECK (streaming IS NULL OR jsonb_typeof(streaming) = 'object');
