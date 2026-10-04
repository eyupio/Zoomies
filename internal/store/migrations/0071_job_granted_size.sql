-- The size of runner a job was given.
--
-- granted_cpus, granted_memory_mb and granted_source are copied from the
-- runner that took the job, once, the first time the controller sees the two
-- together. A runner row says what it was created with, but a runner is
-- removed when its job ends and pruned later; the job is the record an
-- operator reads a week afterwards, and it has to say how big the machine it
-- ran on was without depending on a row that has gone.
--
-- Zero and '' are "not recorded": every job from before this migration, and a
-- job on a runner that was created with no limits at all. Neither is
-- backfilled. What a pool, a host or a profile said at the time is not
-- something a timestamp can recover, and a guess would put a job in a size it
-- never had.
ALTER TABLE jobs ADD COLUMN granted_cpus REAL NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN granted_memory_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN granted_source TEXT NOT NULL DEFAULT '';
