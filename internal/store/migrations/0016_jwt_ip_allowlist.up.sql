-- 0008 design doc: enterprise auth — JWT + IP allowlist
ALTER TABLE virtual_keys
    ADD COLUMN allowed_cidrs JSONB;

ALTER TABLE teams
    ADD COLUMN jwt_jwks_url TEXT,
    ADD COLUMN jwt_team_claim TEXT,
    ADD COLUMN jwt_audience TEXT,
    ADD COLUMN jwt_mode TEXT NOT NULL DEFAULT 'jwt_or_key';
