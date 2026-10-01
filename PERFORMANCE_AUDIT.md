# Performance audit

Audited at `0b4734c` (main), 2026-10-01. Nothing in the codebase was changed; this file is the only addition.

## Summary

**Verdict.** The architecture is sound. There is one SQLite writer, a pooled reader, immutable hashed assets, an SSE-fed client cache (so nothing on the Overview polls), enforced bundle budgets, and batched pruning. The problems are not structural. They are a missing compression layer, a handful of queries whose cost scales with *all retained history* rather than the page or window being asked for, and one endpoint (`/hosts/samples`) that ships raw data the client then throws away. Every critical finding below has a small, local fix. I prototyped the query and index fixes against the same data and they hold (before/after numbers are in each finding).

### How this was measured

| Thing | Detail |
| --- | --- |
| Binary | Built from the tree; `make build`'s UI embed, run as `zoomies controller` with auth off. |
| Data | Seeded SQLite file, 463 MB: **400,000 jobs**, 1.2 M job events, 60,000 runners, 60,000 runner sessions, 150,000 scaling events, 60,000 audit rows, 130,000 fleet samples, 346,000 host samples, 12 pools, 8 hosts, 150 repos. |
| What that means | `retention.jobs` defaults to 30 days, so 400k rows is 30 days at ~13k jobs/day, or 90 days at ~4.4k/day. A default install at 4.4k jobs/day holds roughly a third of this. Costs that are linear in retained rows shrink accordingly; none of them change shape. |
| Machine | 4 vCPU, the controller running its own loop, one request at a time unless a load test is named. |
| Lighthouse 12 | Mobile preset, simulated throttling (1.6 Mbps, 150 ms RTT, 4× CPU slowdown), headless Chromium. **This is a simulation, not a mid-range Android phone.** Single runs; on this shared 4-core box they vary by roughly ±10 points, so the Overview and Jobs numbers are the reliable ones and the others are indicative. |
| Not measured | No real device, no multi-user browser test, and the public docs site (mkdocs) was not audited: its build pulls release and star data from the GitHub API, and the proxy here makes that unreliable. The app has no marketing landing page; **the sign-in page is the first page a visitor sees**, so that is what stands in for the landing page below. |

### Measured bundle sizes (gzipped, from `vite build`)

| Piece | Gzipped | Raw |
| --- | --- | --- |
| **App shell** (entry + static imports + CSS) | **102.3 KB** of the 200 KB budget | `index` 97 KB + `router` 150 KB JS, 61 KB + 25 KB CSS |
| `router` chunk (shared UI + Svelte runtime) | 53.8 KB | 153 KB |
| `index` chunk | 30.7 KB | 99 KB |
| Overview route | 12.5 KB (plus ~30 tiny shared chunks it pulls in) | 37 KB |
| Jobs route + DataGrid + JobFilters | 5.1 + 16.3 + 10.6 KB | 13 + 50 + 32 KB |
| Settings route | 45.4 KB | 157 KB |
| PoolWizardForm | 34.1 KB | 111 KB |
| xterm (log pages only) | 83.0 KB, +31.0 KB WebGL addon, +9.8 KB search addon | 331 + 113 + 32 KB |
| All of `assets/` | 166 files, 2.7 MB on disk | |

The budgets are respected. **But none of it is compressed on the wire** (finding C1), so users download the raw column, not this one.

### Lighthouse, mobile (simulated slow 4G), as shipped

| Page | Score | FCP | LCP | TBT | CLS | Transfer |
| --- | --- | --- | --- | --- | --- | --- |
| Sign-in (`/login`, auth on) | **69** | 4.4 s | 5.5 s | 0 ms | 0.009 | 671 KiB |
| Sign-in, behind a gzip proxy | 82 | 2.9 s | 4.0 s | 0 ms | 0.009 | 416 KiB |
| Overview (`/`, large account) | **33** | 4.1 s | 7.7 s | 810 ms | 0.278 | **4,756 KiB** |
| Overview, gzip proxy | 24–31 | 5.4 s | 7.2–7.3 s | 810–1,550 ms | 0.246 | 656–702 KiB |
| Jobs (`/jobs`) | **23** | 4.3 s | 9.6 s | 790 ms | **0.606** | 1,359 KiB |
| Runners / Pools / Usage / Workflows / Queue | 14 / 53 / 41 / 61 / 33 | 6.4–9.3 s | 6.4–9.3 s | 10–1,130 ms | 0.001–0.526 | 1.1–1.6 MiB |

The last row is the noisy one (the controller and Chromium shared four cores); treat it as a ranking, not as a result. The Overview with compression *and* bucketed samples still scored 29–31, which is an important negative result: **bytes alone do not fix the Overview.** The critical path is a request waterfall and one slow API call (C4, H1).

### Slowest endpoints and queries found (400k jobs, one request at a time)

| Endpoint | Time | Payload | Why |
| --- | --- | --- | --- |
| `GET /workflow-runs?limit=50` | **19–23 s** (10.4 s with a repo filter) | 26 KB | Window function over every job row, run twice (count + page). C3. |
| `GET /hosts/samples?window=168h` | 1.1 s | **25.9 MB** | Every raw minute sample. C2. |
| `GET /hosts/samples?window=24h` | 0.12–0.34 s | **3.69 MB** (38 KB gzipped) | Same. This is the Overview's default. C2. |
| `GET /problems` | **2.1–2.9 s** (4.1 s inside the real Overview load) | 18 KB | Two identical unindexed scans, ~1.7 s of it. C4. |
| `GET /usage` 365 d, by pool, daily | 3.2 s | 878 KB (44 KB gzipped) | Scans all jobs whatever the window. H3. |
| `GET /usage` 7 d, by pool, hourly | 3.4 s | 453 KB | Same. |
| `GET /usage` Overview's own call (today, hourly) | 1.3 s | 61 KB | Same. |
| `GET /jobs/stats` (7 d) | 0.94–1.18 s; 1.56 s grouped by pool | 1–5 KB | The same base CTE evaluated six times. H5. |
| `GET /jobs/facets` | 0.59 s | 2.4 KB | Two `SELECT DISTINCT` full scans. Every Jobs and Queue visit. H6. |
| `GET /jobs?limit=50&offset=200000` | 0.97 s (first page: 6 ms) | 30 KB | Deep offset plus a sort the index cannot serve. N1. |
| Cheap, for contrast | `/stats` 10–29 ms, `/pools` 9 ms, `/runners` 15–20 ms, `/audit` 5 ms, `/jobs` first page 5–7 ms | | These are fine. |

**Load test (Python threads against localhost, 12 s each):**

| Endpoint | Clients | Throughput | p50 | p99 |
| --- | --- | --- | --- | --- |
| `/stats` | 16 | 188 req/s | 83 ms | 152 ms |
| `/jobs?limit=50` | 16 | 211 req/s | 72 ms | 155 ms |
| `/hosts/samples?window=24h` | 8 | 22.6 req/s (≈ 84 MB/s of JSON) | 354 ms | 488 ms |
| **`/problems`** | **8** | **0.7 req/s** | **24.8 s** | **24.9 s** |

Eight operators opening the dashboard at once, with the default configuration, each wait about 25 seconds. The ordinary read paths are healthy.

**Steady-state CPU** (same data, GitHub credentials deliberately unusable, so the loop keeps retrying; read the absolute numbers as an upper bound): 22% of one core with nobody connected; **63%** with a single SSE subscriber, because `publishDerived` recomputes `Stats` and `Problems` after every pass whenever anybody is listening (C4).

---

## 1. Critical

### C1. Nothing is compressed: not the JS, the CSS, the JSON or the SVG

- **Pass:** Engineering review, and the first thing a real user feels.
- **Where:** `internal/api/web.go` (`spaHandler`, `http.ServeContent` straight from the embed), `internal/api/errors.go:81` (`writeJSON`), `internal/api/router.go:30-41` (middleware chain). A search of `internal` and `cmd` for any `Content-Encoding`, `gzip.NewWriter`, `middleware.Compress` or Brotli finds only the backup archive and the OpenAPI generator.
- **Problem:** `Settings.*.js` is served as 157,037 identity bytes (45 KB gzipped). Lighthouse's `uses-text-compression` reports **4,074 KiB** of savings on the Overview and 255 KiB on sign-in. Sign-in is 671 KiB over the wire; the shell alone is ~420 KB raw against 102 KB gzipped, so the enforced "200 KB gzipped" budget protects nothing the user actually downloads. Measured effect of putting a gzip proxy in front, with no other change: sign-in **69 → 82, FCP 4.4 → 2.9 s, LCP 5.5 → 4.0 s, 671 → 416 KiB**; Overview **4,756 → 702 KiB**.
- **Fix:**
  1. **Static assets: compress at build time, serve precompressed.** After `vite build`, write `.br` and `.gz` siblings for everything in `assets/` (a small Vite plugin or a `make ui` step; Node ships Brotli in `zlib`, so no dependency). Embed them. In `spaHandler`, pick by `Accept-Encoding` (`br`, then `gzip`), set `Content-Encoding`, `Vary: Accept-Encoding`, and keep the identity `Content-Length`/`ETag` logic. There is no request-time CPU. The binary grows by roughly 25% of 2.7 MB (estimate).
  2. **JSON API: a pooled `compress/gzip` middleware** (standard library, about 40 lines, which is what `CLAUDE.md`'s "could fifty lines of our own do it?" asks for; `klauspost/compress` is already in the module graph as an indirect dependency if a faster encoder is wanted, but that needs a `docs/dependencies.md` row). Only for `Content-Type: application/json`/`text/*`, only above ~1 KB, and **never** for `text/event-stream` (SSE already sets `no-transform`), the log relay or download streams, or the backup routes.
  3. If the controller is run over TLS it already negotiates h2 (`internal/api/server.go:393`); on plain HTTP it is HTTP/1.1, which matters for H1.
- **Trade-off, stated plainly:** compressing a response that mixes a secret with attacker-influenced text is the BREACH pattern. Exclude the endpoints that return a token (`/auth/session`, token creation, MCP OAuth) and the cost is nil for everything else.
- **Expected gain:** −3.5 to −4 MB on a large-account Overview, −255 KiB and about −1.5 s FCP on sign-in (**measured** via the proxy). Confidence: high.
- **Verify:** `curl -sH 'Accept-Encoding: br' -I /assets/<Settings>.js` shows `Content-Encoding: br`; re-run Lighthouse on `/login` and `/` and compare `total-byte-weight`; a Go test that `text/event-stream` and `/auth/session` responses are never encoded.

### C2. `/hosts/samples` sends every raw minute sample so the browser can throw 95% away

- **Pass:** Engineering review; Real-user walkthrough (Overview, Hosts).
- **Where:** `internal/api/handlers_hosts.go:43` (`handleListHostSamples`), `controller.HostSamples`, `internal/store/queries_host_samples.go`; client `web/src/lib/insights/HostCapacityMap.svelte:173`, `web/src/lib/insights/hostSeries.ts:346` (`WINDOWS`, `mergeHostSamples`).
- **Problem:** the controller writes one row per host per minute. The Overview opens the capacity map at **24 h**, which the chart draws as 96 fifteen-minute peaks per host, and fetches all 1,440 raw rows per host. Measured with 8 hosts: **3.69 MB for 24 h** (338 ms; 38 KB gzipped), **25.9 MB for 168 h** (1.1 s). It is the single largest request on the Overview (3,607 KB of 4,756 KB) and it grows linearly with *hosts × window*, so a 50-host fleet is roughly 23 MB for the default view and 160 MB for 7 days (extrapolation). The browser then parses 11,520 objects on a 4×-throttled CPU, which is part of the 810 ms TBT. The chart's own text says it draws "5-minute peaks", "15-minute peaks", "hourly peaks"; the server could say the same.
- **Fix:** add an optional `bucket` query parameter (seconds; default 0 keeps today's raw shape for the CLI and MCP). When set, aggregate in SQL: `SELECT host_id, (at/?)*? AS at, MAX(cpu_percent), MAX(load_average_1m), MAX(active_runners), MIN(memory_available_mb), … FROM host_samples WHERE at >= ? GROUP BY host_id, at/?`. `HostCapacityMap` passes `chosen.bucket` for 6 h / 24 h / 7 d (300 / 900 / 3600), and keeps raw for ≤ 1 h. Update `api/openapi.yaml`, regenerate `openapi_spec.go` and `schema.d.ts` (CI diffs both). Which columns need MAX versus MIN versus last should follow what `hostSeries.ts` already folds.
- **Expected gain:** a prototype that bucketed to 15-minute maxima before the browser saw it took the 24 h response from 3.69 MB to **3.1 KB gzipped** (**measured**). 7 d goes from 25.9 MB to ~170 rows per host. Confidence: high on bytes. Lighthouse's *simulated* LCP did not move (7.2 s with it, 7.2–7.3 s without), because LCP is gated elsewhere (C4/H1); the saving is link occupancy, parse time and memory, and it is what stops a larger fleet from falling off a cliff.
- **Verify:** `curl -s -H 'Accept-Encoding: identity' '…/hosts/samples?window=24h&bucket=900' | wc -c`; a store test comparing a bucketed result with the client's fold of raw samples; Lighthouse `total-byte-weight` and TBT on `/`.

### C3. `GET /workflow-runs` takes 19–23 seconds, and a repo filter only halves it

- **Pass:** Engineering review; Real-user walkthrough (Workflows page, large account).
- **Where:** `internal/store/workflow_runs.go:164` (`ListWorkflowRuns`) and `workflowRunsFrom` (just below it). Called by `web/src/routes/Workflows.svelte:162`.
- **Problem:** the inner query is `SELECT jobs.*, hostedJobSQL, managedJobSQL, MAX(run_attempt) OVER (PARTITION BY repo, github_run_id, job_name) FROM jobs`, wrapped in `GROUP BY repo, github_run_id`, and the **same statement is executed twice**, once for `COUNT(*)` and once for the page. The `WHERE` that narrows to the filter is applied *outside* the window, so every job row is windowed, run through the `json_each` label predicates and aggregated before `LIMIT 50` is reached. Measured: **19.2 s** unfiltered, **10.4 s** filtered to one repo; the bare window over 400k rows is 1.2 s alone, so the per-row JSON predicates and the aggregation are the rest. It is also a single connection's worth of CPU for that whole time, so a few people opening Workflows starve everything else on four cores.
- **Fix:** page first, aggregate second. For the default sort (`queued_at DESC`) and a filter that does not depend on run state:
  1. Pick the page's runs from the index: `SELECT repo, github_run_id FROM (SELECT repo, github_run_id, queued_at FROM jobs [job-level WHERE] ORDER BY queued_at DESC LIMIT 8*? ) GROUP BY repo, github_run_id ORDER BY MAX(queued_at) DESC LIMIT ?`.
  2. Run the existing aggregate only over `WHERE (repo, github_run_id) IN (<that page>)`, which `idx_jobs_run` serves.
  3. Replace the exact `COUNT(*)` with a capped count (`LIMIT 10001` then "10,000+") or a cached one. Nobody pages to run 96,000.
  4. Where the filter or sort *does* depend on aggregated state (`state`, `jobs`, `duration`, a status filter), keep the slow path but bound it with a default time window (`since` = 7 days, which the UI can say).
  5. Longer term, a `workflow_runs` row maintained by the job upsert (there is exactly one writer, so it is race-free) makes every run view O(page).
- **Trade-off:** ordering becomes "most recently queued job" rather than "first-queued job of the run" for step 1; define it, or take the first-queued from the aggregate in step 2 and re-sort the 50.
- **Expected gain:** a prototype of steps 1–2 (core aggregate, 400k rows) ran in **1.2 ms** against ~9.5 s per pass today (**measured on the aggregate only**; the production query carries more columns, so budget tens of milliseconds, not microseconds). Whole endpoint from ~20 s to <100 ms for the default view (estimate, medium-high confidence).
- **Verify:** time the endpoint with the same seed; `EXPLAIN QUERY PLAN` must show `idx_jobs_queued_at` then `idx_jobs_run`, with no full `SCAN jobs` outside the page; add a store benchmark seeded with 400k jobs (CLAUDE.md asks new behaviour to arrive with a test) and the existing run-listing tests must still pass unchanged.

### C4. `/problems` costs 2–4 s, gates the whole Overview, and burns a core while anyone has a tab open

- **Pass:** Engineering review; Real-user walkthrough (dashboard, large account).
- **Where:**
  - `internal/controller/problems.go:1797` (`lostRunnerProblems`) and `:1863` (`oomKilledProblems`): both call `ListJobs(JobFilter{FaultedOnly: true}, Page{Limit: 100, Sort: "queued_at", Desc: true})`: the **same query twice**.
  - `internal/store/queries_events.go:995` (`fleetFailedJobSQL`: `(runner_fault != '' OR fault_kind != '')`) and `:512` (`ListJobs`, which also runs `COUNT(*)` with that predicate).
  - `internal/controller/derived.go:33` (`publishDerived`) and `internal/api/handlers_overview.go:72` (`handleProblems`, which recomputes from scratch per request).
  - `web/src/lib/state/fleet.svelte.ts:334` (`#fetchAll`).
- **Problem:** the OR across two columns matches no index (`idx_jobs_fault_kind` covers one of them), so each of those queries is a full table scan with a temp B-tree: **67 ms for the count plus 625–770 ms for the page, twice** ≈ 1.7 s of the 2.1–2.9 s `/problems` time. (My seed has zero faulted jobs, which is the common case, so the scan finds nothing and still pays full price.) Three consequences:
  1. **The Overview renders nothing until the slowest call returns.** `#fetchAll` awaits `Promise.all([listPools, listHosts, listRunners, getStats, listProblems, …])` and `fleet.loaded` flips only when all seven are in. In the real page load `/problems` took **4,056 ms** while `/pools`, `/hosts`, `/stats` took 9–84 ms. Tiles, pool utilisation and the feed sit on skeletons for that long. That is the main reason the Overview scores 33 even with compression and bucketed samples.
  2. **It does not scale with viewers.** 8 concurrent `GET /problems`: **0.7 req/s, p50 24.8 s**.
  3. **It runs forever.** With one SSE subscriber the controller used **63% of a core against 22% idle**, because `Stats` and `Problems` are recomputed after every 10 s reconcile pass and every housekeeping tick whenever `bus.Subscribers() > 0`.
- **Fix (all four are independent, in this order):**
  1. **Add a partial index** in a new migration: `CREATE INDEX idx_jobs_faulted ON jobs(queued_at DESC, id) WHERE (runner_fault != '' OR fault_kind != '');`. The predicate must be textually the one `fleetFailedJobSQL` emits, or SQLite will not use it; add a store test asserting the plan. Measured on the seeded database: **faulted page 754 ms → 0.1 ms, count 67 ms → 0.0 ms**.
  2. **Fetch once.** Have the two problem sections share one `ListJobs` result (compute it once per `Problems()` call).
  3. **Compute `Problems()` once per pass and serve it from that.** `publishDerived` already calls it; store the result with its timestamp and have `handleProblems` return it if it is younger than a few seconds (single-flight so concurrent callers share one computation). That makes the endpoint's cost independent of viewer count.
  4. **Stop gating the Overview on it.** In `#fetchAll`, await pools/hosts/runners/stats/scaling for `loaded` and let `listProblems` land on its own (`Promise.allSettled`, or a second awaited step). The problems summary already has a `loading` state.
- **Expected gain:** `/problems` from 2.1–2.9 s to well under 100 ms (steps 1–2 measured at the query level; the rest of the endpoint was not profiled separately). With step 4 the Overview's content no longer waits on it at all. Steady-state CPU with a tab open should fall back toward the idle figure (estimate, medium confidence). The 8-client case becomes a cache hit.
- **Verify:** `time curl /api/v1/problems`; re-run the 8-client load script; `EXPLAIN QUERY PLAN` on the faulted query; `/proc/<pid>/stat` CPU over 20 s with and without an SSE client; Lighthouse FCP/LCP on `/`.

---

## 2. High impact

### H1. Cold-start request waterfall: ~35 one-kilobyte chunks queue ahead of the first API calls

- **Pass:** Real-user walkthrough (first visit, every route).
- **Where:** `web/vite.config.ts` (`rollupOptions.output`, no chunk grouping), `web/src/App.svelte:44-88` (boot: `session.boot()` → `getMeta()` → `refresh()`, then `fleet.start()`), `web/src/lib/router.ts` (dynamic `import()` per route).
- **Problem:** from the Lighthouse trace of `/` (compressed run): HTML at 0.6 s, shell JS by ~1.7 s, then the Overview route chunk at ~2.0 s, which fans out into ~35 more tiny modules (`Badge`, `Select`, `Switch`, `Panel`, `Duration`, `ChartPanel`, `plot`, `chevron-down`, `arrow-right`…, each ~1 KB) that finish between **2.6 and 5.1 s**. `/api/v1/meta` was issued at 2.1 s but did not complete until **5.0 s**, and `/pools` and `/hosts` were not issued until **5.2 s** — behind the module wave on a six-connection HTTP/1.1 link (plain-HTTP controllers are HTTP/1.1). Boot is a chain of round trips (HTML → JS → `meta` → `session` → `fleet`), each paid at 150 ms RTT, and the first API call cannot start before the shell has run.
- **Fix:**
  1. Merge small shared modules into one group so the route graph is a few files, not dozens: in `vite.config.ts` use Rolldown's `output.codeSplitting` groups with a `minSize` (or `manualChunks` equivalent) so anything under ~8 KB gzipped joins the shared UI chunk. The shell budget has 98 KB of headroom, so this fits.
  2. Start the two calls every page needs before the framework boots: `<link rel="preload" as="fetch" crossorigin href="/api/v1/meta">` in `index.html` (and the session call when auth is on), so the response is waiting when `session.boot()` asks. A preload hint must match the request mode and credentials exactly, or the response is fetched twice.
  3. Start `fleet.start()` as soon as `meta` says the session is ready, not after the route has mounted. It already does this once `authenticated` flips; confirm nothing else holds it.
  4. Document (or test) that deployments behind TLS get h2, which removes most of the connection-limit penalty on its own.
- **Expected gain:** −1 to −2 s on the Overview's first useful render under HTTP/1.1 (estimate from the trace; low-to-medium confidence, since it depends on whether the controller is behind an h2 proxy). Fewer requests is a gain either way: 62 on the Overview, 74 on Jobs.
- **Verify:** Lighthouse `network-requests` count and `critical-request-chains`; the time `/api/v1/pools` is first requested relative to the shell script; the `make test-ui` Playwright suite must still pass.

### H2. A 1254×1254 PNG for a 24-pixel icon, and another for the sign-in logo

- **Pass:** Real-user walkthrough (every authenticated page; first-run).
- **Where:** `web/src/lib/components/Logo.svelte:36` (`size > 64 ? mark-white.png : paw-swish-white.png`) and `:57` (`logo-white.png`); `internal/api/webdist/brand/`.
- **Problem:** measured file sizes and pixel dimensions: `paw-swish-white.png` is **1254×1254, 116 KB, drawn at 24 px in the shell on every page**; `logo-white.png` is **1254×1254, 176 KB**, the sign-in and first-run lockup drawn at 220–330 px; `icon-192.png` is 45 KB. On sign-in the logo is **26% of all bytes** (176 of 671 KiB). The comment in `Logo.svelte` says the paw "is drawn far larger than it is ever placed, so the browser already has pixels to spare", which is exactly the cost. At 1.6 Mbps, the two images alone occupy the link for roughly 1.5 s (calculation: 292 KB × 8 / 1.6 Mbps), competing with the JS the page needs. They also sit in the first-paint critical region.
- **Fix:** export purpose-sized copies from the same artwork (the brand guide forbids cropping, not resizing): a 96 px and 144 px paw for the nav mark (a few KB each as PNG-8 or WebP) with `srcset`, and a 660 px lockup as WebP/AVIF (est. 25–40 KB). Keep the 1254 px originals for the docs site. Add `fetchpriority="low"` to the nav mark and `fetchpriority="high"` to the sign-in lockup, which is the LCP element there. Keep `width`/`height` as they are (CLS is already fine on sign-in: 0.009).
- **Expected gain:** −250 to −290 KB on first load of every page (sizes **measured**, replacements **estimated**), ≈ −1.3 s of link time on the simulated connection (calculation). Confidence: high on bytes, medium on the visible time.
- **Verify:** `ls -l` the exports; Lighthouse `total-byte-weight` and the LCP element on `/login`.

### H3. `GET /usage` scans the whole jobs table whatever the window

- **Pass:** Engineering review; Real-user walkthrough (the Overview's activity matrix, the Usage page).
- **Where:** `internal/store/queries_usage.go:119-` (`UsageWithInterval`), the `WHERE j.queued_at < ? AND COALESCE(j.completed_at, ?) >= ?` in its main query, plus `usageAllocation` in `queries_usage_daily.go:232`.
- **Problem:** `COALESCE(completed_at, now) >= from` cannot use an index, so the only usable term is `queued_at < to`, which is *the whole retained table*. A **7-day** window reads and joins all 400k rows to return 31k: **815 ms for the SQL alone**. End to end the Overview's own call (today, hourly) took 1.3 s, a 7-day hourly report 3.4 s, a 365-day daily report 3.2 s. All aggregation happens in Go, row by row; the response for 12 pools and a year is 878 KB because every pool carries a full-length zero-filled `history` array (44 KB gzipped).
- **Fix:**
  1. Replace the predicate with two index-driven branches: `SELECT … WHERE state = 'completed' AND completed_at >= ? AND queued_at < ?` (`idx_jobs_completed`) `UNION ALL SELECT … WHERE state IN ('waiting','queued','in_progress') AND queued_at < ?` (`idx_jobs_state_queued`). Use the explicit `IN`: my first attempt with `state != 'completed'` kept a full scan and gained nothing (825 ms), which is worth a code comment.
  2. Omit all-zero buckets from `history` (the client already fills the window with `fillWindow`), or send it columnar.
  3. Profile `usageAllocation` separately; I measured the job scan, not the allocation join.
- **Trade-off:** correctness depends on `state='completed'` being equivalent to `completed_at IS NOT NULL`; add a test with a job that is cancelled-before-start, and one that is queued with no `completed_at`.
- **Expected gain:** the job scan **815 ms → 113 ms for 7 days** (measured), 504 ms for 30 days, 1.24 s for 365 days (from 1.22 s, so a year-long report gains nothing from this alone; it needs step 2 and, long term, a daily job rollup like the runner-session one that already exists in `queries_usage_daily.go`). The Overview's matrix call falls to roughly the allocation cost. Confidence: high for ≤ 30 d.
- **Verify:** `EXPLAIN QUERY PLAN` shows two `SEARCH` branches and no `SCAN j`; `time curl` the three windows; the existing usage tests unchanged.

### H4. SQLite runs with its defaults: no `mmap_size`, so scan-heavy queries re-read the file through the page cache

- **Pass:** Engineering review.
- **Where:** `internal/store/store.go:183-189` (the read/write DSN `_pragma` list).
- **Problem:** the pragmas set are `journal_mode`, `busy_timeout`, `foreign_keys`, `synchronous`, `journal_size_limit`. The reader pool has up to 8 connections, each with the default 2 MB cache. Measured on the 463 MB file, three scan-heavy queries under each setting (warm OS cache, best of two):

  | Setting | faulted page (scan) | usage 7 d (scan) | `DISTINCT workflow` |
  | --- | --- | --- | --- |
  | default | 710 ms | 1,073 ms | 49 ms |
  | `cache_size=-32768` | 738 ms | 1,064 ms | 65 ms |
  | **`mmap_size=268435456`** | **337 ms** | **558 ms** | 35 ms |
  | `temp_store=memory` | 655 ms | 1,036 ms | 45 ms |
  | mmap + temp memory | 287 ms | 630 ms | 40 ms |

  A bigger per-connection cache did nothing; `mmap_size` roughly halves scans because every reader shares the OS-mapped file.
- **Fix:** add `mmap_size(268435456)` (256 MB) to the DSN. Optionally `temp_store(MEMORY)` (a minor gain for the sorts above). Do **not** raise `cache_size`: it is per connection, multiplied by 8, and measured no benefit.
- **Trade-off:** SQLite's own documentation warns that memory-mapped I/O is unsafe if the file can be truncated underneath the process or on some network filesystems (a failed read becomes a `SIGBUS` rather than an error). Controllers on NFS or similar should not enable it, so make it a setting or skip it when the path is on a network mount. It does not change correctness on local disks.
- **Expected gain:** ~2× on every query that scans (measured); the indexed paths are unaffected. Confidence: high. This is a masking fix: the scans in C3/C4/H3 should still be removed, not tuned.
- **Verify:** repeat the table above through the store; `PRAGMA mmap_size;` on a pooled reader returns the value.

### H5. `/jobs/stats` evaluates the same CTE six times

- **Pass:** Engineering review; Real-user walkthrough (Usage page's release table).
- **Where:** `internal/store/queries_job_stats.go:109` (`JobStats`): the `base` CTE (with a correlated `runners` subquery per job) is prefixed to six separate statements (counts, fault kinds, peaks, and one windowed-percentile query each for duration, wait and startup).
- **Problem:** default 7-day window: **0.94–1.18 s**; grouped by pool **1.56 s**. The base scan alone is 92 ms for 31k rows, so the other ~850 ms is repetition. Used by `ReleaseTable.svelte`, the MCP `job_stats` tool, and the CLI. The cost is linear in the window, up to the 90-day cap.
- **Fix:** read the base rows **once** into a slice (the pattern `UsageWithInterval` already uses) and compute counts, fault kinds, peaks and the three percentile pairs in Go; or materialise into a `TEMP` table for the one transaction and run the six aggregates over that. Percentiles in Go are a sort per group, no window functions.
- **Expected gain:** ~1 s → ~150 ms for 7 days (estimate from the 92 ms base scan; medium confidence).
- **Verify:** time `/jobs/stats` and `/jobs/stats?group_by=pool`; the existing `jobs_stats_test.go` must still pass unchanged (they pin the percentile rule).

### H6. `/jobs/facets` scans the table twice per Jobs or Queue page open

- **Pass:** Real-user walkthrough (opening Jobs, the core list; Queue).
- **Where:** `internal/api/handlers_jobs.go:427`, `internal/store/queries_events.go:860` (`JobDistinct`). Called from `web/src/routes/Jobs.svelte:100` and `Queue.svelte:121`.
- **Problem:** `SELECT DISTINCT workflow FROM jobs …` and `… conclusion …` have no index, so each is `SCAN jobs` plus a temp B-tree: 123 ms and 113 ms of SQL, **0.59 s** for the endpoint measured under load, every time either page opens. `repo` is fine (covering index, 2 ms).
- **Fix:** `conclusion` is a closed set; return it as a constant. For `workflow`, cache the answer for 30–60 s (invalidate nothing; facets are suggestions, not data), or add `CREATE INDEX idx_jobs_workflow ON jobs(workflow)` and accept the write cost on the hottest table. The cache is cheaper and sufficient.
- **Expected gain:** 0.6 s → ~1 ms on a warm cache (estimate, high confidence). Filter dropdowns populate faster on both pages.
- **Verify:** `time curl /api/v1/jobs/facets` twice.

### H7. Late layout shifts on the Overview, Jobs and Runners

- **Pass:** Real-user walkthrough.
- **Where:** Overview `div.tiles` (shifted twice, 0.246 and 0.240), Jobs `div.content` (0.606), Runners (0.526); `web/src/routes/Overview.svelte`, `web/src/lib/overview/FirstRun.svelte`, `FleetMetrics.svelte`.
- **Problem:** the CLS is real and large (Good is under 0.1), and Lighthouse names the same container in both runs. On the Overview, the nearest cause is content appearing above `.tiles` after data lands (`FirstRun` renders above the metrics and decides late), and tiles that load into a different height from their placeholder. **Caveat:** my seeded fleet has no working GitHub App, so `FirstRun` was visible here (it was also the LCP element); on a configured fleet that block may not appear and the shift may be smaller. I could not isolate Jobs or Runners further without a real browser session.
- **Fix:** reserve the space. Give `FirstRun` a fixed-height slot (or render it only after `fleet.loaded`, below the tiles), and give the tiles' and grids' loading state the same height as the loaded state. With C4's change the first paint no longer waits several seconds for `/problems`, so the placeholder phase also shortens.
- **Expected gain:** CLS from 0.25–0.6 to under 0.1 (estimate, medium confidence).
- **Verify:** Lighthouse `layout-shifts` audit on `/`, `/jobs` and `/runners`; the Playwright accessibility and mobile specs in `web/tests`.

---

## 3. Nice to have

### N1. Deep paging and the `ORDER BY queued_at DESC, id ASC` tie-break

- **Pass:** Engineering review.
- **Where:** `internal/store/queries_events.go:512` (`ListJobs`), index `idx_jobs_queued_at` in migration 0001; the keyset variant `ListJobsAfter` already exists at `:581`.
- **Problem:** the `id ASC` tie-break is not in the index, so SQLite scans the index and adds a temp B-tree for the tie-break. First pages are fine (5–7 ms); `offset=200000` is **0.97 s** (405–525 ms of SQL).
- **Fix:** replace `idx_jobs_queued_at` with `CREATE INDEX idx_jobs_queued_id ON jobs(queued_at DESC, id)` (it serves both orders, so drop the old one) and move the Jobs grid to the cursor API it already has.
- **Expected gain:** offset 200k from 499 ms to **3.7 ms** of SQL (**measured**). Rarely felt, because few people page that deep; the cursor change also stops rows shifting between pages while the list is live.
- **Verify:** `EXPLAIN QUERY PLAN` shows a covering scan with no temp B-tree. Index build time on the large table was not measured, so check it before shipping the migration.

### N2. The brand images and icons are revalidated every five minutes

- **Where:** `internal/api/web.go:221` (`setCacheHeaders`: non-`assets/` files get `public, max-age=300`).
- **Problem:** `/brand/*`, the favicons and `icon-192.png` are unhashed, so a returning user on mobile revalidates ~350 KB of images every five minutes. The conditional request returns 304, which is cheap in bytes but a round trip at 150 ms, in the first-paint region.
- **Fix:** `max-age=604800, stale-while-revalidate=86400` for `/brand/*` and the icons (they change with releases, not minutes), or put a content hash in their names. Keep `site.webmanifest` at 300 s.
- **Expected gain:** removes a handful of round trips per revisit (estimate, high confidence). **Verify:** DevTools network panel on a second visit.

### N3. Aborted duplicate requests on the Overview

- **Where:** `web/src/lib/overview/FleetActivity.svelte:178-185`, `web/src/lib/insights/HostCapacityMap.svelte:169-190`, `ActiveJobs.svelte`.
- **Problem:** the trace for one Overview load shows `usage`, `samples?window=1h`, `jobs?state=in_progress` and `hosts/samples` each issued twice, the first as an aborted 0-byte request, because the effect re-runs when preferences settle. The server still starts work for each (the SQL is cancelled via context, not free).
- **Fix:** derive the effect's inputs after `prefs` has loaded, so it runs once.
- **Expected gain:** four fewer requests per load; small. **Verify:** the requests list in Lighthouse.

### N4. Paint something when the HTML arrives

- **Where:** `internal/api/webdist/index.html` (built from `web/index.html`).
- **Problem:** the document arrives in 0.58 s and nothing paints until the render-blocking CSS (61 KB + 25 KB raw; Lighthouse attributes 1.35 s) and the module graph are in, so sign-in FCP is 2.9–4.4 s. The theme script already handles the background flash.
- **Fix:** inline a tiny shell in `#app` (background from the existing `--z-bg`, a spinner or the wordmark) so there is a first paint at HTML arrival, and let the app replace it on mount. The 3 KB document can carry it. This is purely perceived speed; C1 and H1 are what actually shorten the load.
- **Expected gain:** FCP to roughly the HTML arrival time (estimate, medium confidence). **Verify:** Lighthouse FCP on `/login`.

### N5. Fonts

- **Where:** `web/src/lib/styles/fonts.css`.
- **Problem:** `font-display: swap` is right, but there is no `<link rel="preload" as="font">` (Inter's latin file was requested at 2.1 s, after the CSS was parsed), and the fallback face has no `size-adjust`, so the swap can reflow text. JetBrains Mono ships two weights (21 KB each). The 85 KB `latin-ext` file loads only when a glyph needs it (the `unicode-range` is correct).
- **Fix:** preload `inter-latin-wght-normal` (the hashed name needs a small build step to inject) and add a `size-adjust`/`ascent-override` fallback face. Check whether the 500 weight of the mono face is used before keeping it.
- **Expected gain:** small and mostly CLS-related (estimate, low-to-medium confidence). Do this after H7, not before. **Verify:** Lighthouse CLS and font request start time.

### N6. Things I looked at and would not change

These are listed so nobody burns time on them. The shell is 102 KB gzipped with no heavy dependency (no date library, no chart library, no full lodash; `@lucide/svelte` is imported by name from 133 sites and tree-shakes). xterm and its WebGL addon are lazy and budgeted. `assets/` is content-hashed and served `immutable` for a year; `index.html` is `no-cache` with an ETag derived from the bytes. Pruning is batched at 500 rows per transaction, so it does not hold the single writer for long (the candidate select costs 85 ms per batch on this data, which is acceptable). The scheduler/reconcile path and the SSE fan-out were not a bottleneck here; `publishDerived` is correctly skipped when nobody is listening. GitHub clients have timeouts (`internal/github/app.go`). Retention exists for the large tables; audit is deliberately never pruned. I did not recommend memoisation anywhere: no render path in the Overview or Jobs showed up in the main-thread breakdown as anything but parse/layout of the data in C2.

---

## Real-user walkthrough

*Simulated* mid-range Android (Lighthouse's mobile profile: 1.6 Mbps, 150 ms RTT, 4× CPU slowdown) on a large account. Numbers are from the runs above; steps I could not run are said so.

### First visit, empty cache

| Step | What a user sees | Measured / basis |
| --- | --- | --- |
| **Sign-in** | A blank screen until ~4.4 s, then the form; logo arrives with it. Usable (no blocking time) about 5.5 s in. | FCP 4.4 s, LCP 5.5 s, 671 KiB. With compression: FCP 2.9 s, LCP 4.0 s. The 176 KB logo is the LCP element and a quarter of the bytes (C1, H2). |
| **Sign-up (first administrator)** | Same shell and bundle as sign-in, as a route inside it. | **Not measured separately**: it uses the same assets, so C1/H1/H2 apply identically. |
| **Onboarding (first-run checklist, GitHub setup)** | The first-run block sits above the tiles and pulls the 176 KB lockup. | **Not walked**: needs a real GitHub App. The checklist was visible in my seed and was both the LCP element and, probably, the layout-shift source on `/` (H7). |
| **Main dashboard (Overview)** | Blank for ~4 s, then skeleton tiles and panels for another ~3 s while seven API calls settle, waiting on `/problems` (2–4 s). Tiles then jump as the layout changes (CLS 0.25). The browser is also pulling 3.7 MB of host samples in the background, which on the simulated link would occupy it for ~18 s (calculation: 3.69 MB × 8 / 1.6 Mbps), and parsing 11.5k objects on a slowed CPU. | FCP 4.1 s, LCP 7.7 s, TBT 810 ms, CLS 0.278, 4.7 MB. (C1, C2, C4, H1, H7.) |
| **Core feature (Jobs)** | The list's first page is quick on the server (6 ms) but the page is last to settle: LCP 9.6 s, and the content block jumps by 0.6 CLS. | Score 23. The facets call adds 0.6 s on every open (H6). |
| **Search / filtering** | Filtering by repo, state or pool is fast: 5–7 ms server-side. | Measured. The filter dropdowns wait on `/jobs/facets` (H6). |
| **Large list / report** | **Workflows: 19–23 s of nothing** before rows appear, 10 s with a repo filter. Usage: 1.3 s for today's matrix, 3.2 s for a year. Release statistics: ~1 s. | Measured at the API (C3, H3, H5). Lighthouse's own Workflows score (61) is high only because it measures the shell, not this call. |

### Returning user, same large account

The hashed assets are immutable, so the shell is not downloaded again; a revisit costs the 3 KB `index.html` (revalidated), the unhashed images (revalidated every five minutes, N2) and the API. The API is then the whole experience: **I estimate a first useful Overview paint of roughly 2.5–4.5 s on a warm cache**, set by `/problems` (2–4 s) plus a round trip (estimate, not measured). With C4's fixes the same visit should be bounded by the 9–84 ms calls instead.

### As the account grows

Costs fall into two groups, and both follow from the code paths above (the endpoint timings are measured at 400k jobs; the growth is reasoning from the query shapes, not repeated measurement at other sizes):

- **Linear in retained history, on every visit:** `/workflow-runs`, `/problems` (and the controller's idle CPU while a tab is open), `/usage` at any window, `/jobs/facets`, `/jobs/stats`. Raising `retention.jobs` from 30 to 90 days triples all of them. The partial index, the two-branch usage predicate and the page-first run listing make them depend on the page or window instead.
- **Linear in fleet size × window:** `/hosts/samples`. 8 hosts is 3.7 MB for a day and 26 MB for a week; the bytes scale with the number of hosts, with no bound.

Everything that is already index-driven (`/jobs` first page, `/runners`, `/audit`, `/stats`, `/pools`) stayed at 5–30 ms and held 200 requests/second with 16 clients.

---

## Top 5 fixes by expected user impact

1. **Compress everything (C1).** Measured: −4 MB on the large-account Overview, sign-in FCP 4.4 → 2.9 s, LCP 5.5 → 4.0 s. Affects every page and every user, including the first visit of every visitor. Precompress assets at build, gzip the JSON.
2. **Make `/problems` cheap and stop the Overview waiting on it (C4).** One partial index (754 ms → 0.1 ms measured), one shared query, a per-pass cache, and `loaded` no longer gated on it. This is the Overview's real critical path, the 25-second eight-viewer collapse, and the steady 40-point CPU cost of a tab being open.
3. **Bucket host samples on the server (C2).** 3.69 MB → ~3 KB gzipped for the default view (measured), and 26 MB → a few hundred rows for 7 days. Stops the dashboard degrading as hosts are added.
4. **Rewrite `/workflow-runs` to page first (C3).** 19–23 s → expected <100 ms. The one page that is unusable on a large account today.
5. **Resize the brand images and trim the boot waterfall (H2 + H1).** −250 to −290 KB on every page's first load (sizes measured), and the first API calls no longer queue behind ~35 one-kilobyte modules.

### Quick wins (each under 15 minutes)

- Add `mmap_size(268435456)` to the store DSN: ~2× on scan queries, one line (H4). Skip it on network filesystems.
- Migration with `idx_jobs_faulted` (partial, `WHERE (runner_fault != '' OR fault_kind != '')`): 754 ms → 0.1 ms (C4 step 1).
- Replace `idx_jobs_queued_at` with `(queued_at DESC, id)`: 499 ms → 3.7 ms for deep pages (N1).
- Share the single `ListJobs(FaultedOnly)` result between `lostRunnerProblems` and `oomKilledProblems` (C4 step 2).
- `Promise.allSettled` (or a second step) for `listProblems` in `fleet.svelte.ts:334` so the Overview stops waiting on it (C4 step 4).
- Return `conclusion` facets as a constant and cache `workflow` facets for 60 s (H6).
- `max-age=604800` for `/brand/*` and icons in `web.go:221` (N2).
- Swap the usage predicate for the two-branch form with an explicit `IN ('waiting','queued','in_progress')` (H3 step 1): 815 ms → 113 ms for a week, though it needs the two tests described there, so it sits at the top of the 15-minute bound.

### Cross-cutting notes for whoever implements these

- Any API shape change (`bucket`, a capped `total`) must go through `api/openapi.yaml`, then `go run internal/api/gen_openapi.go` and `make openapi`; CI diffs both. Keep the old behaviour as the default so the CLI and the MCP tools are unaffected.
- New indexes go in a numbered migration, and each is worth an `EXPLAIN QUERY PLAN` assertion in a store test so a predicate rewording cannot silently turn it off. Index build time on a large `jobs` table was not measured; check it, because migrations run at startup under the single writer.
- Add the 400k-job seed as a benchmark fixture (or a `make bench-store` target) so these numbers can be re-taken. I seeded with a throwaway script outside the repo; nothing about it is checked in.
