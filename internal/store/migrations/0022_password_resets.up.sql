-- Phase 2: admin-issued password resets. The admin generates a token,
-- copies the resulting URL out-of-band to the user, and the user
-- consumes it to set a new password. No email infra needed.
CREATE TABLE password_resets (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  BYTEA NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    used_at     TIMESTAMPTZ,
    created_by  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX password_resets_user_id_idx     ON password_resets(user_id);
CREATE INDEX password_resets_expires_at_idx  ON password_resets(expires_at);
