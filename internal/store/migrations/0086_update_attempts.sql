-- Update attempts: every time the controller or a host was asked to update.
--
-- A table of its own, rather than columns on hosts or a key in
-- instance_settings, because an attempt is an event with a life of its own: it
-- is asked for, it ends one of four ways, and an operator wants the list of
-- them long after the host row has changed or gone. One row is one request to
-- change one target's version. The controller's own update and each host's are
-- the same shape, told apart by scope.
--
-- host_id names the host the attempt was for and is deliberately not a foreign
-- key, as it is not in the runner sessions or the host samples: a host that is
-- deleted and enrolled again under the same id (a re-join) has to keep the
-- history of what was done to it, and a key with ON DELETE CASCADE would erase
-- it while one without would refuse the delete. DeleteHostForgettingMachine
-- closes the host's open attempts as cancelled instead, so a row that outlives
-- its host never reads as still in flight. host_id is '' for the controller's
-- own attempts, which is what lets one index cover both scopes.
--
-- from_version and to_version are columns of that name because from is an SQL
-- keyword. state is requested until the attempt ends, then succeeded, failed,
-- timed_out or cancelled, and only ever moves out of requested. error is the
-- sentence an operator reads when it did not succeed, and '' otherwise.
-- requested_by is the user or token that asked, or '' for the planner.
CREATE TABLE update_attempts (
    id           TEXT    PRIMARY KEY,
    scope        TEXT    NOT NULL,
    host_id      TEXT    NOT NULL DEFAULT '',
    from_version TEXT    NOT NULL DEFAULT '',
    to_version   TEXT    NOT NULL DEFAULT '',
    trigger      TEXT    NOT NULL DEFAULT 'manual',
    requested_by TEXT    NOT NULL DEFAULT '',
    state        TEXT    NOT NULL DEFAULT 'requested',
    error        TEXT    NOT NULL DEFAULT '',
    requested_at INTEGER NOT NULL,
    finished_at  INTEGER
) WITHOUT ROWID;

-- At most one open attempt per target.
--
-- Two administrators pressing the button together, or the planner and a
-- button, must not both get to write a request for the same target: the
-- helper acts on one request at a time and a second would either be lost or
-- run the upgrade twice. The rule is the index's rather than a read followed
-- by a write, so it holds however the two callers interleave. It is partial
-- because finished attempts are history, and a target has any number of
-- those.
CREATE UNIQUE INDEX idx_update_attempts_open ON update_attempts(scope, host_id) WHERE state = 'requested';

-- The list an operator reads is newest first, for one target or for all.
CREATE INDEX idx_update_attempts_recent ON update_attempts(scope, host_id, requested_at DESC);
