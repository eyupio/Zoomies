-- Optional accounting metadata and a fair-share guard for repositories.
ALTER TABLE pools ADD COLUMN repository_concurrency_limit INTEGER NOT NULL DEFAULT 0
    CHECK (repository_concurrency_limit >= 0);
ALTER TABLE pools ADD COLUMN cost_per_runner_hour REAL
    CHECK (cost_per_runner_hour IS NULL OR cost_per_runner_hour >= 0);

