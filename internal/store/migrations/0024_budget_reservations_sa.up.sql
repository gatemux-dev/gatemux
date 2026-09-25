-- Phase 2 SA: budget reservations gain a service_account_id column so
-- in-flight estimates count against the SA budget, mirroring user_id.
ALTER TABLE budget_reservations
    ADD COLUMN service_account_id BIGINT REFERENCES service_accounts(id) ON DELETE SET NULL;
CREATE INDEX budget_reservations_service_account_created_at_idx
    ON budget_reservations(service_account_id, created_at DESC);
