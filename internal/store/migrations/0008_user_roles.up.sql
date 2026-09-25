-- Adds an explicit role enum to users, replacing the binary is_admin flag
-- as the source of truth. is_admin is kept for one release as a denormalized
-- mirror so existing code keeps working until everything is on role.
--
-- Roles:
--   admin   — global control plane access (configures providers, pricing,
--             cross-team visibility)
--   manager — owns one team's keys, budget, and member invites; cannot touch
--             global config or other teams
--   member  — uses /me/* only

ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'member';
ALTER TABLE users ADD CONSTRAINT users_role_check
    CHECK (role IN ('admin', 'manager', 'member'));

UPDATE users SET role = 'admin' WHERE is_admin = TRUE;

CREATE INDEX users_role_idx ON users(role);
