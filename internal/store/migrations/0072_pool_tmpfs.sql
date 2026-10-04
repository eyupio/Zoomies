-- A pool may keep a runner's work folder and /tmp in memory instead of on the
-- host's disk. It is a JSON document, like cpu_burst, so a further folder is an
-- addition rather than a column; '{}' is nothing in memory, which is what every
-- existing pool keeps doing until somebody opts in.
ALTER TABLE pools ADD COLUMN tmpfs TEXT NOT NULL DEFAULT '{}';
