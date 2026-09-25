-- Phase 2: OIDC. Users gain three columns:
--   oidc_sub               — IdP subject id we bind the row to. NULL for
--                            users created before OIDC or via password-only.
--   disabled_at            — admin override that blocks sign-in regardless
--                            of what the IdP asserts; null = active.
--   role_managed_by_oidc   — defaults TRUE so claim mapping owns role/team.
--                            Flipped to FALSE the moment an admin edits
--                            role or team manually, so claim mapping
--                            stops clobbering admin intent on the next
--                            sign-in.
ALTER TABLE users
    ADD COLUMN oidc_sub             TEXT,
    ADD COLUMN disabled_at          TIMESTAMPTZ,
    ADD COLUMN role_managed_by_oidc BOOLEAN NOT NULL DEFAULT TRUE;

CREATE UNIQUE INDEX users_oidc_sub_uniq ON users(oidc_sub) WHERE oidc_sub IS NOT NULL;

-- Existing users haven't had their role/team set by OIDC yet; mark them
-- so claim mapping doesn't stomp on hand-curated admin/manager rows the
-- first time they sign in via OIDC.
UPDATE users SET role_managed_by_oidc = FALSE;
