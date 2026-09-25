-- 0006 design doc: extend deployment capabilities with new endpoint flags
-- Capabilities JSONB now also supports: moderation, rerank, images,
-- audio_transcribe, audio_speech, messages_passthrough. Existing rows
-- keep their current shape; parseCapabilities defaults the new keys to
-- false at read time.

-- 0006 design doc: pricing units beyond per-million-tokens
ALTER TABLE pricing
    ADD COLUMN unit TEXT NOT NULL DEFAULT 'per_million_tokens',
    ADD COLUMN per_unit_cents BIGINT;
