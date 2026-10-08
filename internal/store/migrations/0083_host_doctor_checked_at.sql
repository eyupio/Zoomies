-- When the controller last accepted a report from this host. An unchanged
-- report moves only this figure, so the report body is not rewritten (a hosts
-- UPDATE rewrites the whole record, body included). Milliseconds; 0 means the
-- body's own checked_at is the newest word.
ALTER TABLE hosts ADD COLUMN doctor_checked_at INTEGER NOT NULL DEFAULT 0;
