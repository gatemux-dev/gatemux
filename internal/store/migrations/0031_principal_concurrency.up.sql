ALTER TABLE users
    ADD COLUMN max_parallel_requests INT
    CHECK (max_parallel_requests IS NULL OR max_parallel_requests > 0);

ALTER TABLE service_accounts
    ADD COLUMN max_parallel_requests INT
    CHECK (max_parallel_requests IS NULL OR max_parallel_requests > 0);
