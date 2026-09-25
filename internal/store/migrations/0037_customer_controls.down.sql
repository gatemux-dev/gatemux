ALTER TABLE customers DROP CONSTRAINT customer_rates_valid, DROP CONSTRAINT customer_period_valid, DROP CONSTRAINT customer_budget_nonnegative;
DROP INDEX usage_log_customer_window;
DROP INDEX budget_reservations_customer_window;
ALTER TABLE budget_reservations DROP COLUMN customer_id;
ALTER TABLE teams DROP COLUMN customer_registration;
