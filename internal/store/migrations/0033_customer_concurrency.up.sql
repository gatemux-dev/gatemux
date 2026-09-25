ALTER TABLE customers ADD COLUMN max_parallel_requests INT
    CHECK (max_parallel_requests > 0);

CREATE INDEX customers_concurrency_policies ON customers (team_id, external_id)
    WHERE max_parallel_requests IS NOT NULL AND archived_at IS NULL;
