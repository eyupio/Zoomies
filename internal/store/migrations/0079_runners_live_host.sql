-- A host read counts its live runners, and the count walked every row the host ever had:
-- removed and failed runners are kept for the history, so on a fleet that runs thousands of
-- jobs a day the count read every retained runner of every host on each ListHosts and
-- GetHost -- the scheduler's snapshot, every heartbeat, every runner event and every room a
-- problem prices. A partial index over only the live rows, with the columns the count
-- reads and sums, answers it without reading the table: its size is the live runners, not the
-- history. The predicate has to match the query's text, which is why that statement is a
-- constant with a plan test.
CREATE INDEX idx_runners_live_host ON runners(host_id, allocated_cpus, state)
	WHERE state NOT IN ('removed','failed');
