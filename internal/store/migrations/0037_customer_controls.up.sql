ALTER TABLE teams ADD COLUMN customer_registration TEXT NOT NULL DEFAULT 'optional'
  CHECK (customer_registration IN ('optional', 'required', 'auto_create'));
ALTER TABLE budget_reservations ADD COLUMN customer_id BIGINT REFERENCES customers(id) ON DELETE SET NULL;
CREATE INDEX budget_reservations_customer_window ON budget_reservations (customer_id, created_at) WHERE customer_id IS NOT NULL;
CREATE INDEX usage_log_customer_window ON usage_log (customer_id, ts) WHERE customer_id IS NOT NULL;
ALTER TABLE customers ADD CONSTRAINT customer_budget_nonnegative CHECK (usd_limit_cents IS NULL OR usd_limit_cents >= 0) NOT VALID;
ALTER TABLE customers ADD CONSTRAINT customer_period_valid CHECK (period IN ('day', 'month')) NOT VALID;
ALTER TABLE customers ADD CONSTRAINT customer_rates_valid CHECK ((rpm IS NULL OR rpm BETWEEN 0 AND 1000000000) AND (tpm IS NULL OR tpm BETWEEN 0 AND 1000000000)) NOT VALID;
