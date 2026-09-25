-- Per-deployment capability flags. NULL means "fall back to whatever the
-- provider type supports", so existing rows keep their current behavior
-- and only newly-added deployments get an explicit override.
--
-- Shape:
--   {"chat": true, "stream_chat": true, "embeddings": false}
--
-- Routing uses these to skip deployments that don't support the requested
-- capability — e.g. an embeddings request to a chat-only deployment now
-- short-circuits at admission instead of failing upstream.

ALTER TABLE deployments ADD COLUMN capabilities JSONB;
