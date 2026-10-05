-- What an operator has read in the problems drawer, kept on the account so it
-- follows them to another browser, another device and a cleared cache.
--
-- It was a browser preference first, and "I snoozed it for a day and it came
-- back" was the result: the decision lived in one tab's localStorage, so a
-- second tab, a phone or a private window had never heard of it, and any tab
-- that wrote last overwrote the rest. One row per decision, rather than a JSON
-- document like user_preferences, is what lets two tabs each add their own
-- without one replacing the other.
--
-- key is the UI's own identity for a problem or for a whole kind of problem
-- ("type:pool.no_capacity"); the controller has no opinion about it and never
-- reads it. until is NULL for a plain "dismiss until resolved", and a moment
-- for a snooze. Both are Unix milliseconds like every other timestamp.
CREATE TABLE problem_dismissals (
    user_id      TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key          TEXT    NOT NULL,
    severity     TEXT    NOT NULL,
    dismissed_at INTEGER NOT NULL,
    until        INTEGER,
    PRIMARY KEY (user_id, key)
);
