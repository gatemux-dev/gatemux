-- A paused key is refused at authentication but keeps its identity,
-- limits and history, and can be resumed; revocation stays permanent.
ALTER TABLE virtual_keys ADD COLUMN disabled_at TIMESTAMPTZ;
