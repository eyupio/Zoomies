-- The CPU count a runner's toolchains were told to size their workers for
-- when it started, so the runner page can say what the running job was given
-- rather than what the pool would give a runner started now. Zero is a runner
-- that was told nothing, which every runner before this column was.
ALTER TABLE runners ADD COLUMN sized_for_cpus INTEGER NOT NULL DEFAULT 0;
