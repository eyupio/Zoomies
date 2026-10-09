-- Update rollouts: one walk of the fleet to a new release, host by host.
--
-- A table of its own, rather than a flag on update_attempts, because a rollout
-- outlives each of its steps and has a state no attempt has: halted, where it
-- has stopped for a person to look and nothing new will start until they say
-- so. Its steps are the attempts that name it (rollout_id, below). target is
-- the release tag it is walking the fleet to, fixed when it starts, so a
-- release published halfway through cannot change what the remaining hosts
-- are given.
--
-- state is running until the rollout ends, halted while it waits for an
-- operator (halted_reason is the sentence they read), then done or cancelled
-- for good. trigger and started_by mean what they do on an attempt: manual or
-- auto, and who asked, by the name the attempts record (the planner's own
-- name for its rollouts). Times are Unix milliseconds, as everywhere.
--
-- host_ids is the hosts it was started for, a JSON array, and empty for every
-- host that is behind: an administrator who asked for one host must not find
-- the fleet restarted. resumed_at is when it started or was last resumed; a
-- failure before it is one an operator has resumed past, and must not halt it
-- again. cancelled_by is the person who cancelled it, and '' when it was not
-- cancelled or the planner cancelled it: auto does not start again a rollout a
-- person stopped, but one it stopped itself (the mode switched) is no such
-- decision.
CREATE TABLE update_rollouts (
    id            TEXT    PRIMARY KEY,
    target        TEXT    NOT NULL,
    trigger       TEXT    NOT NULL DEFAULT 'manual',
    state         TEXT    NOT NULL DEFAULT 'running',
    started_by    TEXT    NOT NULL DEFAULT '',
    halted_reason TEXT    NOT NULL DEFAULT '',
    host_ids      TEXT    NOT NULL DEFAULT '[]',
    cancelled_by  TEXT    NOT NULL DEFAULT '',
    started_at    INTEGER NOT NULL,
    resumed_at    INTEGER NOT NULL,
    finished_at   INTEGER
) WITHOUT ROWID;

-- At most one open rollout, whether it is running or halted.
--
-- Two rollouts walking the fleet together would each choose the next host from
-- a different picture of it and could take two hosts down at once, which is
-- the thing a rollout exists to prevent. The index is on a constant, so every
-- open row collides with every other, and it is partial so finished rollouts,
-- which are history, do not take part. As with update_attempts the rule is the
-- index's rather than a read followed by a write, so it holds however two
-- callers interleave.
CREATE UNIQUE INDEX idx_update_rollouts_open ON update_rollouts((1)) WHERE state IN ('running', 'halted');

-- The rollouts an operator reads are newest first.
CREATE INDEX idx_update_rollouts_recent ON update_rollouts(started_at DESC);

-- rollout_id is the rollout that asked for an attempt, '' for the controller's
-- own attempts and for any a person started with a button, which is every
-- attempt that exists today. It is not a foreign key, for the reason host_id
-- is not: history must survive the row it names being pruned or the host being
-- deleted. An additive column, as 0076 added its own.
ALTER TABLE update_attempts ADD COLUMN rollout_id TEXT NOT NULL DEFAULT '';

-- Finished rollouts are pruned with finished attempts, on the same window
-- (retention.update_attempts): a rollout is only the grouping of those
-- attempts, and one kept longer than the steps it was made of would list
-- nothing. A second setting would be a second thing to explain for no
-- difference an operator could want. An open rollout is never pruned.
