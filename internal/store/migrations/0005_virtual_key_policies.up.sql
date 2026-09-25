ALTER TABLE virtual_keys
    ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN allowed_models JSONB NOT NULL DEFAULT '["*"]'::jsonb,
    ADD COLUMN expires_at TIMESTAMPTZ,
    ADD COLUMN rotated_from_key_id BIGINT REFERENCES virtual_keys(id);

CREATE INDEX virtual_keys_rotated_from_key_id_idx ON virtual_keys(rotated_from_key_id);
