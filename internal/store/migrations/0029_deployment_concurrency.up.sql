-- A NULL max_parallel_requests means unlimited. Positive values bound the
-- number of simultaneous upstream operations sent to this deployment by one
-- GateMux process. Distributed provider limits are a separate phase.
ALTER TABLE deployments
    ADD COLUMN max_parallel_requests INT
    CHECK (max_parallel_requests IS NULL OR max_parallel_requests > 0);
