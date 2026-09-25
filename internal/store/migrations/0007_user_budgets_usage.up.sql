ALTER TABLE users
    ADD COLUMN usd_limit_cents BIGINT,
    ADD COLUMN period TEXT NOT NULL DEFAULT 'month';

ALTER TABLE budget_reservations
    ADD COLUMN user_id BIGINT REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX budget_reservations_user_created_at_idx
    ON budget_reservations(user_id, created_at DESC);

ALTER TABLE usage_log
    ADD COLUMN user_id BIGINT REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX usage_log_user_ts_idx ON usage_log(user_id, ts DESC);
