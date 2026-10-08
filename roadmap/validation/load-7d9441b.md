# Load measurement: 7d9441b

Written by `make measure` on 2026-09-08T21:38:54Z.

| | |
| --- | --- |
| Commit | `7d9441b` |
| Machine | linux/amd64, 4 CPUs, go1.26.0 |
| Fixture | 10 hosts, 5 pools, 2000 runners, 10000 jobs, 5000 audit rows over 720h0m0s of history |
| Written through | `internal/store`, the same single writer the controller uses |
| Read through | the real router and the real store, on disk with its usual journal |
| Load while reading | 189 job writes landed during the timings |
| Samples | 40 requests per row |

| Read | Path | p50 | p95 | worst | bytes |
| --- | --- | --- | --- | --- | --- |
| Overview: the statistics tiles | `/api/v1/stats` | 3.6ms | 4.7ms | 7ms | 1 KB |
| Overview: the sparkline samples | `/api/v1/samples?window=1h` | 400µs | 800µs | 800µs | 9 KB |
| Overview: the problems drawer | `/api/v1/problems` | 17.5ms | 19.2ms | 25.8ms | 4 KB |
| Overview: recent scaling | `/api/v1/scaling-events?limit=10` | 300µs | 400µs | 400µs | 2 KB |
| Runners: the first page | `/api/v1/runners?limit=50` | 2.1ms | 2.9ms | 3.3ms | 13 KB |
| Runners: page 20 of the history | `/api/v1/runners?limit=50&offset=950&include_removed=true` | 1.6ms | 2.2ms | 2.4ms | 17 KB |
| Runners: filtered to idle | `/api/v1/runners?limit=50&state=idle` | 2ms | 2.9ms | 3.1ms | 13 KB |
| Jobs: the first page | `/api/v1/jobs?limit=50` | 900µs | 1.5ms | 2ms | 16 KB |
| Jobs: page 100 | `/api/v1/jobs?limit=50&offset=4950` | 1.4ms | 1.9ms | 2.3ms | 32 KB |
| Jobs: one repository | `/api/v1/jobs?limit=50&repo=acme/service-01` | 1.2ms | 1.7ms | 1.8ms | 32 KB |
| Hosts | `/api/v1/hosts` | 800µs | 900µs | 2.2ms | 4 KB |
| Pools | `/api/v1/pools` | 2.1ms | 2.8ms | 3.3ms | 6 KB |
| Audit: the first page | `/api/v1/audit?limit=50` | 500µs | 700µs | 1.2ms | 10 KB |
| Audit: filtered by action | `/api/v1/audit?limit=50&action=runner.drain` | 600µs | 800µs | 1.1ms | 10 KB |
| Audit: page 50 | `/api/v1/audit?limit=50&offset=2450` | 700µs | 900µs | 1ms | 10 KB |
| Usage: a month by pool | `/api/v1/usage?group_by=pool&from=2026-08-09T21:38:51Z&to=2026-09-08T21:38:51Z` | 30.8ms | 32.6ms | 33.8ms | 1 KB |
| Usage: a month by repository | `/api/v1/usage?group_by=repository&from=2026-08-09T21:38:51Z&to=2026-09-08T21:38:51Z` | 26.7ms | 29.2ms | 31.8ms | 4 KB |


## What this says

**Nothing here is a gate.** These are recorded figures on one machine; a
threshold in the test would either be so loose it never fires or so tight that a
slower runner fails a pull request that changed nothing. What the test does
assert is that every read still answers `200` with a month of history under it,
and that a deep page is still a page of rows; a query that got fast by
stopping short would look excellent in this table.

Three things came out of running it.

**The audit log's action filter had no index.** Filtering by action cost about
twice an unfiltered page (1.4 ms against 0.7 ms at five thousand rows), and the
plan said why: `SCAN audit_events USING INDEX idx_audit_created`; the whole
table in date order, discarding what does not match. Audit rows are the one
history Zoomies deliberately never prunes, so this is the table that grows
without limit and the scan that gets worse for ever. Migration `0021` adds
`idx_audit_action(action, created_at DESC)`, which serves the filter and the
ordering together; the filtered read now matches the unfiltered one.

**The usage report is the slowest read in the product**, at roughly 30 ms for a
month. It is not an index problem: the range already uses `idx_jobs_queued_at`,
and the cost is the concurrency accounting in Go over every job in the window,
which is what the report is. It is a report page rather than a dashboard, it is
not on the event stream, and 30 ms is not worth changing the shape of the data
for, but it is the first figure to watch if the retention window grows, because
it scales with the jobs in the range rather than with the rows returned.

**The problems drawer is the slowest thing on the Overview**, at about 18 ms
against a millisecond or two for everything else on that page. It is a series of
queries rather than one, it runs on every page load and again on every
`problems.updated` frame, and it is worth a look, but it is not on this
package's list, and changing it on the strength of one measurement without a
test that pins each problem's meaning would be trading a known cost for an
unknown one. Recorded here so the next person has the number.

## What this does not measure

| Not measured | Why | What it would take |
| --- | --- | --- |
| The browser | This times the API, not rendering. A page that fetches quickly and paints slowly would look fine here. | The Playwright tier, with a fixture this size behind it. |
| Concurrent readers | One client, one request at a time, so it measures latency and not contention. SQLite serialises writers and this fixture has one. | A load generator with a request concurrency, which changes what the numbers mean. |
| A real fleet's writes | The writes underneath are job upserts at 50/s. A real controller also reconciles, heartbeats and streams. | The drill tier at this fixture size. |
| Anything above ten thousand jobs | The retention windows keep a month, and this is a busy month. | A larger fixture, when somebody has a fleet that big. |
