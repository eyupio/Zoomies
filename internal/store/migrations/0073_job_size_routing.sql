-- What the controller worked out about one job, and where it went.
--
-- size_class is the class it classified the job into when it first saw it, with
-- size_reason the sentence for why and size_basis how it got there: explicit (a
-- size label in runs-on), pin, history or default. routed_class is the class it
-- is sent to, which differs from size_class once the wait for room has run out
-- and routed_note says so. ran_class is the class of the host that took it, so
-- "where did it actually run" is a column rather than a join against a host
-- that may have changed class or gone since.
--
-- All of them are '' for every job that exists today, and for every job while
-- size routing is off, which is "not recorded". None is backfilled: what a host
-- was called or what a job would have been classed as then is not something a
-- timestamp can recover.
--
-- size_floor_mb is the memory the job's recent runs were measured needing,
-- with the margin a profile allows, when the class came from its history, and
-- 0 when it did not. It is what decides whether a smaller class than the job's
-- is an acceptable place for it to run when the right one has no room: a
-- machine that is not big enough would only kill it for memory. A job with no
-- floor is one nobody knows the needs of, so it is never sent somewhere smaller.
--
-- cpu_periods and cpu_throttled_periods are the runner's cumulative CPU
-- enforcement periods and how many of them it was throttled in, the last
-- sample taken while the job ran. The agents already send them for elastic CPU;
-- keeping them against the job is what lets a job that is held back by its
-- quota be told from one that is simply busy. Zero is "never sampled".
ALTER TABLE jobs ADD COLUMN size_class TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN size_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN size_basis TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN size_floor_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN routed_class TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN routed_note TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN ran_class TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN cpu_periods INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN cpu_throttled_periods INTEGER NOT NULL DEFAULT 0;
