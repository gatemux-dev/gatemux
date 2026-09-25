-- Phase 2: session management. Sessions need source metadata so the
-- Account → Sessions page can show "where was this signed in from" and
-- the user can spot anomalies. INET stays consistent with usage_log.
ALTER TABLE sessions
    ADD COLUMN ip         INET,
    ADD COLUMN user_agent TEXT;
