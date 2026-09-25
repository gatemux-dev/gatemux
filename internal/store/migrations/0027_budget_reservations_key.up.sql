-- Add key_id to budget_reservations so per-key admission can read
-- reserved+settled like the team/user/SA paths do, instead of only
-- usage_log (which doesn't reflect in-flight reservations and lets
-- two concurrent requests both pass admission against an empty bucket).
ALTER TABLE budget_reservations
    ADD COLUMN IF NOT EXISTS key_id BIGINT;

CREATE INDEX IF NOT EXISTS budget_reservations_key_id_idx
    ON budget_reservations (key_id, created_at)
    WHERE key_id IS NOT NULL;
