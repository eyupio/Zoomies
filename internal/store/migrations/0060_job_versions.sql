-- Which release ran a job, so that build times and stability can be compared
-- across releases.
--
-- The four columns are stamped once, when the controller claims a job or a
-- runner picks it up, and never rewritten: a job's numbers belong to the build
-- that produced them, and a later update from GitHub says nothing about it.
-- They are NULL on every job recorded before this migration, on purpose. The
-- release that was running at the time is not something a timestamp can
-- recover, and a guess would put a job under a release it never saw.
--
-- controller_version and controller_channel are the running controller's build
-- (version.Version and its install channel). agent_version is what the host
-- reported on its last heartbeat before the job was assigned to it, and
-- host_id is that host. Each is stamped independently, because a job is
-- claimed before it has a host.
ALTER TABLE jobs ADD COLUMN controller_version TEXT;
ALTER TABLE jobs ADD COLUMN controller_channel TEXT;
ALTER TABLE jobs ADD COLUMN agent_version TEXT;
ALTER TABLE jobs ADD COLUMN host_id TEXT;

-- The job statistics group a bounded window of jobs by release, and by job
-- name. Each index leads with the column the window is cut on, so the query
-- reads the window rather than the table.
CREATE INDEX idx_jobs_queued_version ON jobs(queued_at, controller_version);
CREATE INDEX idx_jobs_name_queued ON jobs(job_name, queued_at);

-- The same three facts in the usage ledger, copied from the job when the
-- runner's session is written, so a release comparison outlives
-- retention.jobs the way the runner-hours do. host_id is already there.
ALTER TABLE runner_sessions ADD COLUMN controller_version TEXT;
ALTER TABLE runner_sessions ADD COLUMN controller_channel TEXT;
ALTER TABLE runner_sessions ADD COLUMN agent_version TEXT;
