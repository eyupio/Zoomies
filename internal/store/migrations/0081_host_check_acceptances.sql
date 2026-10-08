-- An operator's decision that one host's counted Warn on a doctor check is
-- deliberate. It is a table of its own, beside the report and not inside it,
-- because a heartbeat replaces the stored report wholesale: a decision written
-- into the report would vanish at the next beat, and one an agent could write
-- would let a host silence its own alarm.
--
-- accepted_current is the check's Current text when the decision was made. An
-- acceptance covers the check only while it still reads that, so a value that
-- moved is looked at again rather than waved through. expires_at is NOT NULL on
-- purpose: a decision that never ends is a decision nobody is asked to make
-- again. (host_id, check_id) is unique; accepting again renews the one row.
CREATE TABLE host_check_acceptances (
    id               TEXT    PRIMARY KEY,
    host_id          TEXT    NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    check_id         TEXT    NOT NULL,
    accepted_current TEXT    NOT NULL,
    reason           TEXT    NOT NULL,
    created_by       TEXT    NOT NULL,
    created_by_name  TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    UNIQUE (host_id, check_id)
);
