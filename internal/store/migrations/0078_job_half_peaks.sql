-- peak_runner_memory_mb and peak_daemon_memory_mb are the most each container of a
-- docker-in-docker pair was measured using while the job ran, beside peak_memory_mb,
-- which is the two added together. The thin-sidecar guard sizes a pool's smallest runner
-- so that each half holds its own peak, and what it had to go on was a window of the last
-- few hours held in memory -- against a week of jobs. A weekly build whose daemon used
-- 2.6 GB was missing from a window of light phases, and the minimum was lowered under it.
-- 0 is not measured, which is every job that ran before this and every job that did not
-- run in a pair; the guard counts the jobs that have one and waits for enough of them.
ALTER TABLE jobs ADD COLUMN peak_runner_memory_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN peak_daemon_memory_mb INTEGER NOT NULL DEFAULT 0;
