CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL DEFAULT '',
    password_hash BYTEA,
    team_id       BIGINT REFERENCES teams(id) ON DELETE SET NULL,
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at TIMESTAMPTZ,
    archived_at   TIMESTAMPTZ
);

CREATE INDEX users_team_id_idx ON users(team_id);

CREATE TABLE invites (
    id           BIGSERIAL PRIMARY KEY,
    token_hash   BYTEA NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    email        TEXT,
    team_id      BIGINT REFERENCES teams(id) ON DELETE CASCADE,
    role         TEXT NOT NULL DEFAULT 'user',
    created_by   BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    accepted_at  TIMESTAMPTZ,
    accepted_by  BIGINT REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX invites_team_id_idx ON invites(team_id);

ALTER TABLE virtual_keys ALTER COLUMN team_id DROP NOT NULL;
ALTER TABLE virtual_keys ADD COLUMN user_id BIGINT REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE virtual_keys ADD CONSTRAINT virtual_keys_owner_check
    CHECK (team_id IS NOT NULL OR user_id IS NOT NULL);
CREATE INDEX virtual_keys_user_id_idx ON virtual_keys(user_id);
