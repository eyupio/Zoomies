-- Two changes to the jobs table, made together because the first needs it
-- rebuilt and the second is an index the rebuild has to recreate anyway.
--
-- A job GitHub is holding for a deployment review is "waiting": not demand,
-- because nothing may run it until somebody approves it, and ahead of "queued"
-- in the lifecycle, because the approval arrives as an ordinary queued
-- delivery. The state column's CHECK admits only the three states the first
-- schema knew, and SQLite cannot alter a CHECK in place, so the table is
-- rebuilt with one more. Nothing references jobs by foreign key, so the copy is
-- the whole of it; the unique index on github_job_id is what the upsert keys
-- on and comes back with the others.
--
-- StatsSince counts completed jobs inside a rolling window on every reconcile
-- pass while a browser is connected, and PruneJobs deletes them past the
-- retention window; both filter on state and completed_at and neither had an
-- index to do it with, so a month of history on a busy fleet was a full scan
-- every ten seconds.
CREATE TABLE jobs_new (
    id             TEXT PRIMARY KEY,
    github_job_id  INTEGER NOT NULL,
    github_run_id  INTEGER NOT NULL DEFAULT 0,
    repo           TEXT    NOT NULL DEFAULT '',
    workflow       TEXT    NOT NULL DEFAULT '',
    job_name       TEXT    NOT NULL DEFAULT '',
    labels         TEXT    NOT NULL DEFAULT '[]',
    state          TEXT    NOT NULL CHECK (state IN ('waiting','queued','in_progress','completed')),
    conclusion     TEXT    NOT NULL DEFAULT '',
    pool_id        TEXT    NOT NULL DEFAULT '',
    runner_id      TEXT    NOT NULL DEFAULT '',
    runner_name    TEXT    NOT NULL DEFAULT '',
    html_url       TEXT    NOT NULL DEFAULT '',
    queued_at      INTEGER NOT NULL,
    started_at     INTEGER,
    completed_at   INTEGER,
    matched        INTEGER NOT NULL DEFAULT 0,
    head_branch    TEXT    NOT NULL DEFAULT '',
    head_sha       TEXT    NOT NULL DEFAULT '',
    run_attempt    INTEGER NOT NULL DEFAULT 0,
    steps          TEXT    NOT NULL DEFAULT '[]',
    runner_fault   TEXT    NOT NULL DEFAULT ''
);
INSERT INTO jobs_new (id, github_job_id, github_run_id, repo, workflow, job_name, labels, state,
    conclusion, pool_id, runner_id, runner_name, html_url, queued_at, started_at, completed_at,
    matched, head_branch, head_sha, run_attempt, steps, runner_fault)
SELECT id, github_job_id, github_run_id, repo, workflow, job_name, labels, state,
    conclusion, pool_id, runner_id, runner_name, html_url, queued_at, started_at, completed_at,
    matched, head_branch, head_sha, run_attempt, steps, runner_fault
FROM jobs;
DROP TABLE jobs;
ALTER TABLE jobs_new RENAME TO jobs;
CREATE UNIQUE INDEX idx_jobs_github ON jobs(github_job_id);
CREATE INDEX idx_jobs_state_queued ON jobs(state, queued_at);
CREATE INDEX idx_jobs_pool ON jobs(pool_id, queued_at DESC);
CREATE INDEX idx_jobs_repo ON jobs(repo, queued_at DESC);
CREATE INDEX idx_jobs_queued_at ON jobs(queued_at DESC);
CREATE INDEX idx_jobs_completed ON jobs(state, completed_at);
