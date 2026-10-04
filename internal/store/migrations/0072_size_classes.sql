-- Size classes, pools the controller keeps, and what a job has been seen to need.
--
-- hosts.size_class is the controller's reading of how big a host is -- small,
-- medium or large -- with the class a change is waiting on and since when. It
-- is one JSON document in one column, like throttle, because it is read and
-- written whole, by one writer, and never searched by a field. '{}' is "not
-- worked out", which is every host until the controller has looked, and it
-- only looks when an operator has turned size routing or automatic pools on.
-- Nothing reads the column otherwise, so the upgrade changes no host.
ALTER TABLE hosts ADD COLUMN size_class TEXT NOT NULL DEFAULT '{}';

-- A pool the controller keeps, rather than one an operator made.
--
-- auto_key is the column that says so, and it is a column rather than a label
-- or a name for the reason providers carry theirs (see 0030): a label is the
-- operator's to edit and a name is something they can type, and either would
-- let an operator pool be mistaken for ours and rewritten. '' is every pool
-- that exists today, so none of them is ever touched. A key is an architecture
-- and a size class, and there is one pool for each in an installation.
--
-- The controller derives max_runners and min_runners from the hosts it finds,
-- so what the operator asked for needs somewhere of its own to live, or the
-- next pass would read its own output back as the ask: auto_min is how many
-- runners to keep warm, auto_cap the most the pool may ever have (0 is no
-- cap), and auto_paused takes the pool out of use without deleting it.
ALTER TABLE pools ADD COLUMN auto_key TEXT NOT NULL DEFAULT '';
ALTER TABLE pools ADD COLUMN auto_min INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pools ADD COLUMN auto_cap INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pools ADD COLUMN auto_paused INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX idx_pools_auto_key ON pools(installation_id, auto_key) WHERE auto_key != '';

-- The class a job is in now, kept apart from its runs.
--
-- It is state rather than something recomputed from history on every read,
-- because moving a job down has to be slower than moving it up: one run that
-- needed less is no reason to take a job off the hosts that coped with it, and a
-- class that was only ever the answer to the last twenty runs would swing with
-- every one of them. runs is how many measured runs the class was last worked
-- out from, computed_at when, and moved_at when the class last changed.
-- floor_mb is the memory those runs showed the job needing, which a queued job
-- is stamped with so that it is only ever offered a smaller class that has it.
CREATE TABLE job_classes (
    repo        TEXT    NOT NULL,
    workflow    TEXT    NOT NULL,
    job_name    TEXT    NOT NULL,
    class       TEXT    NOT NULL,
    reason      TEXT    NOT NULL DEFAULT '',
    basis       TEXT    NOT NULL DEFAULT '',
    floor_mb    INTEGER NOT NULL DEFAULT 0,
    runs        INTEGER NOT NULL DEFAULT 0,
    computed_at INTEGER NOT NULL,
    moved_at    INTEGER NOT NULL,
    PRIMARY KEY (repo, workflow, job_name)
) WITHOUT ROWID;

-- An operator's word on a class, which beats anything measured.
--
-- A pin is for one job, named by all three columns, or for every job of a
-- repository, with workflow and job_name left ''. A job's own pin wins over its
-- repository's. Nothing else is stored in the two blank columns, so a pin on a
-- workflow alone is not a thing this table can say.
CREATE TABLE size_pins (
    repo       TEXT    NOT NULL,
    workflow   TEXT    NOT NULL DEFAULT '',
    job_name   TEXT    NOT NULL DEFAULT '',
    class      TEXT    NOT NULL,
    created_by TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (repo, workflow, job_name)
) WITHOUT ROWID;
