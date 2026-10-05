-- The memory valve lets a container runner be given more memory than it was
-- created with while its job runs, out of memory the host has not promised to
-- any runner.
--
-- memory_burst is a pool's policy for it, a JSON document like cpu_burst so a
-- further knob is an addition rather than a column. '{}' is off, which is what
-- every pool that exists today keeps being until somebody opts in: an upgrade
-- never changes the limit of a runner on a pool that did not ask.
ALTER TABLE pools ADD COLUMN memory_burst TEXT NOT NULL DEFAULT '{}';
-- lent_memory_mb is how much a runner's containers hold beyond what they were
-- created with. Unlike a CPU boost it cannot be taken back, so it is the
-- placement ledger's to remember: a host that has lent 2 GB has 2 GB less room
-- for the next runner, and that has to survive a controller restart. It is
-- written by the agent's report alone; 0 is nothing lent, which every existing
-- runner is.
ALTER TABLE runners ADD COLUMN lent_memory_mb INTEGER NOT NULL DEFAULT 0;
-- scratch records which of a runner's folders were kept in memory when it was
-- created, and at what size, as the backend worked it out from the pool, the
-- host's own say and the runner's limits. It is recorded rather than recomputed
-- for the reason the allocation is: a pool edited since would give a different
-- answer than the one the running job was started with. '' is not recorded,
-- which is every runner that exists today and every runner of a pool that keeps
-- nothing in memory.
ALTER TABLE runners ADD COLUMN scratch TEXT NOT NULL DEFAULT '';
