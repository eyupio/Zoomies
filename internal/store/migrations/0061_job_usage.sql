-- What each job actually used, so the scheduler can place the next run of the
-- same job on a host with room for it.
--
-- peak_cpus and peak_memory_mb are the most the job's runner was measured
-- using while GitHub said the job was in progress on it. They are written from
-- the agent's resource samples -- the same samples elastic CPU is decided on --
-- and only ever raised, so a sample that arrives late cannot lower a peak.
-- Zero is "never measured": a job from before this migration, one on a backend
-- that reports no usage, or one too short to be sampled.
--
-- oom_killed says the kernel killed something in the job's runner for its
-- memory limit: the whole container, or one step's process (exit 137) under a
-- runner that lived on. It is the fleet's fault, not the workflow's, and it is
-- the one failure the profile has to correct for -- a killed job's peak is the
-- limit it hit, not what it needed.
--
-- Nothing here outlives the job row. The profiles are computed from the jobs
-- retention.jobs keeps, so they roll forward and prune with them.
ALTER TABLE jobs ADD COLUMN peak_cpus REAL NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN peak_memory_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN oom_killed INTEGER NOT NULL DEFAULT 0;

-- A profile is read for every distinct queued job on every pass, so the lookup
-- seeks straight to one job's measured history, newest first.
CREATE INDEX idx_jobs_usage_profile ON jobs(repo, workflow, job_name, completed_at)
	WHERE state = 'completed' AND (peak_memory_mb > 0 OR peak_cpus > 0 OR oom_killed = 1);
