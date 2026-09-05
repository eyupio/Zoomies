-- What GitHub says about a job beyond its state: the branch and attempt it ran
-- for, and the steps it ran, so a failed job can say which step failed without
-- the operator leaving for GitHub.
ALTER TABLE jobs ADD COLUMN head_branch TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN head_sha    TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN run_attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN steps       TEXT NOT NULL DEFAULT '[]';

-- The fleet's own side of a failure: what the runner executing this job said
-- when it stopped before GitHub reported the job over. GitHub will call such a
-- job "failure" like any other; this column is what tells the two apart.
ALTER TABLE jobs ADD COLUMN runner_fault TEXT NOT NULL DEFAULT '';

-- One row per thing Zoomies observed or did about a job, in the order it
-- happened. The jobs row holds the current truth; this holds how it got there.
CREATE TABLE job_events (
    id          TEXT PRIMARY KEY,
    job_id      TEXT NOT NULL,
    at          INTEGER NOT NULL,
    kind        TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL DEFAULT '',
    runner_id   TEXT NOT NULL DEFAULT '',
    runner_name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_job_events_job ON job_events(job_id, at);
