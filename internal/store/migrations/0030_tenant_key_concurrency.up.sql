-- NULL means unlimited. Positive values are enforced with distributed Redis
-- leases across every GateMux replica for the lifetime of each data-plane call.
ALTER TABLE teams
    ADD COLUMN max_parallel_requests INT
    CHECK (max_parallel_requests IS NULL OR max_parallel_requests > 0);

ALTER TABLE virtual_keys
    ADD COLUMN max_parallel_requests INT
    CHECK (max_parallel_requests IS NULL OR max_parallel_requests > 0);
