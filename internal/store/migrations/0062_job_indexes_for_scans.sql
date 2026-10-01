-- Two indexes for reads that were scanning the whole jobs table.
--
-- Faulted jobs. "A runner stopped under this job" is `runner_fault != '' OR
-- fault_kind != ''`, an OR across two columns that no single-column index can
-- serve, so every listing of the fleet's own failures read all of jobs and
-- sorted what it found -- about three quarters of a second at four hundred
-- thousand rows, and the common answer is none. The problems list asks twice
-- on every pass. A partial index holds only the rows that match, so the
-- usual case is an empty index and the read is free. The predicate has to be
-- spelt exactly as fleetFailedJobSQL spells it, or the planner cannot prove
-- the index covers the query; TestFaultedJobsAreReadFromTheirOwnIndex pins it.
CREATE INDEX IF NOT EXISTS idx_jobs_faulted ON jobs(queued_at DESC, id)
	WHERE (runner_fault != '' OR fault_kind != '');

-- The listing's default order is queued_at DESC, id ASC. The old index stopped
-- at queued_at, so SQLite scanned it and sorted the tie-break in a temporary
-- b-tree: a deep page (offset 200,000) took half a second where an index that
-- ends in id takes a few milliseconds. The new one serves every query the old
-- one did, because a b-tree prefix is an index, so the old one goes. All three
-- statements are idempotent, because the tests that wind a column off a table
-- replay later migrations over indexes that are already there.
CREATE INDEX IF NOT EXISTS idx_jobs_queued_id ON jobs(queued_at DESC, id);
DROP INDEX IF EXISTS idx_jobs_queued_at;
