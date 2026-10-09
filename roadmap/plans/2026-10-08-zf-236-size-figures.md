# ZF-236 the figures behind size advice: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the runs that label advice rests on: observed percentiles, the sample size and window, the recommended class with its reason, whether any host carries it, and a `not_enough_data` row where the fleet has seen too few runs, from the API, the CLI and the SizeAdvice card, with the constants named in the docs.

**Architecture:** The advice keeps its source, the kept job class (`store.JobClassAsked`), and gains the figures beside it. A pure `scheduler.Observe` turns a job's measured runs in a window into `Observed` (p50, p95 and max of CPU and memory, with the count); the controller fetches those runs per advice row through the existing windowed `JobClassHistory` query, bounded by `retention.jobs`, and says which bound applied. A row the gate used to drop for too few runs is now returned with `state: not_enough_data` and its count. Nothing about the class itself changes: it is still the nearest-rank p90 with a fifth added, and the docs now say so by name.

**Tech Stack:** Go 1.27 standard library; Svelte 5 and the existing tokens; the OpenAPI generators.

**Spec:** `roadmap/agent-readiness.md` section 5; the record is `ROADMAP.md` ZF-236.

## Global Constraints

- British spelling in prose; comments say *why*; `--` in code comments; no spaced em dash anywhere (a docs test enforces it); commit messages are imperative sentences, no prefix.
- No new dependency. `internal/scheduler` stays pure (no clock, no store); SQL only in `internal/store`; `internal/api` is transport only.
- A route change arrives with its `docs/api-surface.md` row and regenerated clients (`go run internal/api/gen_openapi.go`, `make openapi`); a flag change with `docs/cli.md` and `make generate` for the command reference.
- Problem codes and the catalog are untouched; `jobs.label_advice` keeps its meaning.
- Before every push: `make build-nogui`, `make fmt`, `make lint`, `make test`, `go mod tidy` leaves `go.mod`/`go.sum` unchanged, `make generate && git diff --exit-code`.

## Decisions the spec left open

- **Which percentile is which.** The class is decided from the nearest-rank p90 (`ProfilePercentile`) with `ProfileMemoryMargin`; the spec's `observed` block reports p50, p95 and max. Both are true and both are shown: `observed` is what the runs did, `recommended_class` and `reason` are what the class rule made of them. The docs section names the p90 as the rule and the p95 as the figure shown.
- **Window and bound.** `?window=` (and `--window`) defaults to `scheduler.ClassWindow` (14 days). The applied window is the smaller of what was asked and `retention.jobs` when that is positive; the response carries `window: {asked, applied, bound}` with `bound` one of `asked` or `retention`. Runs per row are capped at `store.JobClassHistoryLimit`, newest first.
- **`not_enough_data` rows.** A kept class with fewer than `AdviceMinRuns` runs is a row with `state: "not_enough_data"`, `kind: ""`, the count and the observed figures it has; `state` is `"ok"` otherwise. The `kind` filter never returns such a row, and the counts gain `not_enough_data`. Pinned jobs stay out, as today.
- **`fits`.** `fits.ok` is whether any host, by `EffectiveSizeClass()`, carries `recommended_class`; `fits.missing` names the class when none does. Host availability (cordoned, quiet) is not part of it: the question is whether the fleet has such a host at all.
- **MCP.** The `label_advice` tool keeps its arguments; it gets the new fields because it reads the route. Adding `repo`/`window` to the tool is left out: the spec names the CLI and the card, and the tool's own test holds unknown arguments refused.

## Review Focus

1. A job whose runs are all OOM-killed must report `observed.memory_mb.max` as the recorded peak, not the ×1.5 treated figure: the figures are what happened, the class is the rule. Pinned in Task 1.
2. `?window=0`, `?window=-1d` and `?window=abc` must be 400s naming `window`, and a window longer than retention must apply retention and say `bound: retention`. Pinned in Task 3.
3. A `not_enough_data` row for a job with one run must carry `observed.runs: 1` and p50 = p95 = max of that run, never a division by zero or an empty block. Pinned in Task 1.
4. The CLI table must print `--` for a figure a sparse row lacks (CPU peaks of 0), never `0.00`. Pinned in Task 4.
5. The card's "not enough data yet, N of 5 runs" must take the 5 from the payload or a constant the API exposes, never a literal, so a changed `AdviceMinRuns` cannot leave the card lying. Pinned in Task 5 (the payload carries `min_runs`).

---

### Task 1: `scheduler.Observe`, and the advice row that says it has too little data

**Files:**
- Create: `internal/scheduler/observe.go`, `internal/scheduler/observe_test.go`
- Modify: `internal/scheduler/advice.go` (fields, the gate), `internal/scheduler/advice_test.go`

**Interfaces:**
- Produces:
  ```go
  package scheduler
  type Figures struct { P50, P95, Max float64 }                 // json p50, p95, max
  type Observed struct {
      Runs     int           `json:"runs"`
      Window   time.Duration `json:"-"`         // the controller renders window on the page, not per row
      CPU      Figures       `json:"cpu"`
      MemoryMB Figures       `json:"memory_mb"`
  }
  func Observe(runs []store.JobRun) Observed   // nearest-rank percentiles over PeakCPUs and PeakMemoryMB; zero value for no runs
  const AdviceStateOK = "ok"; const AdviceStateNotEnoughData = "not_enough_data"
  // Advice gains:
  //   State            string          `json:"state"`
  //   MinRuns          int             `json:"min_runs"`            // AdviceMinRuns, so a client can print "N of 5"
  //   RecommendedClass store.SizeClass `json:"recommended_class"`   // the kept class
  //   Reason           string          `json:"reason"`              // k.Reason
  //   Observed         *Observed       `json:"observed,omitempty"`  // filled by the controller
  //   Fits             *Fit            `json:"fits,omitempty"`      // filled by the controller
  type Fit struct { OK bool `json:"ok"`; Missing store.SizeClass `json:"missing,omitempty"` }
  ```
- `LabelAdvice` gate: `k.Runs < AdviceMinRuns` no longer returns nil; it returns `&Advice{State: AdviceStateNotEnoughData, Runs: k.Runs, MinRuns: AdviceMinRuns, RecommendedClass: k.Class, Reason: k.Reason, Labels: k.Labels, Repo…, Message: fmt.Sprintf("%d of %d measured runs so far; advice needs %d.", k.Runs, AdviceMinRuns, AdviceMinRuns)}`. `SortAdvice` puts `not_enough_data` rows last.

- [ ] **Step 1: Write the failing tests.** `observe_test.go`: `TestObserveIsTheNearestRankPercentilesOfWhatRunsUsed` (ten runs with memory 100..1000 MB: p50 500, p95 1000, max 1000; CPU likewise), `TestObserveOfOneRunIsThatRun` (Review Focus 3), `TestObserveReportsAKilledRunsRecordedPeakNotItsTreatedFigure` (Review Focus 1: one OOM run with PeakMemoryMB 3000 → max 3000, not 5400), `TestObserveOfNothingIsZero`. `advice_test.go`: a `kept(class, AdviceMinRuns, …)` row is `State ok`; `AdviceMinRuns-1` is `State not_enough_data` with `Runs` and `MinRuns` set and `Kind ""`; sorting puts it last.
- [ ] **Step 2: Run** `go test ./internal/scheduler -run 'Observe|Advice'`; expected FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same and `go test ./internal/scheduler`; expected PASS.
- [ ] **Step 5: Commit** `Say what the runs behind a size advice did, and when there are too few`.

### Task 2: the controller fills the figures, the window and the fit

**Files:**
- Modify: `internal/controller/size_views.go` (`LabelAdvice` takes options; memo keyed by them), `internal/controller/size_views_test.go`

**Interfaces:**
- Produces:
  ```go
  type AdviceOptions struct { Window time.Duration; Repo string }   // zero Window means ClassWindow
  type AdviceWindow struct { Asked time.Duration `json:"asked"`; Applied time.Duration `json:"applied"`; Bound string `json:"bound"` } // seconds on the wire via a Duration marshaller the views already use, or int seconds: match how views.go renders durations
  func (c *Controller) LabelAdvice(ctx context.Context, opts AdviceOptions) ([]*scheduler.Advice, AdviceWindow, error)
  func (c *Controller) adviceWindow(asked time.Duration) AdviceWindow   // min(asked, retention.jobs when > 0); Bound "asked" or "retention"
  ```
- For each row: `runs, err := c.st.JobClassHistory(ctx, a.Repo, a.Workflow, a.JobName, now.Add(-window.Applied), store.JobClassHistoryLimit)`; `obs := scheduler.Observe(runs)`; `a.Observed = &obs`. `Fits` from `c.st.ListHosts` and `EffectiveSizeClass()`. `Repo` filters rows before the history is read. `labelAdviceCounts` adds `not_enough_data`; `labelAdviceProblems` counts only `ok` rows (the problem is about advice, not about waiting).

- [ ] **Step 1: Write the failing tests** extending `TestLabelAdviceIsWorkedOutFromWhatRunsCallFor…`: each row has `Observed.Runs == 12`, `MemoryMB.P95 == 6000` for the e2e job, `Fits.OK` true for a class the `classFleet` carries and `Fits.Missing == store.SizeLarge` when the only large host is removed; `Repo: "acme/docs"` returns one row; a window of 1 hour returns `Observed.Runs == 0` for runs measured a day ago while `Runs` (the class's count) is unchanged; `TestTheAdviceWindowIsBoundedByRetention` (retention 7d, asked 14d → applied 7d, bound retention; asked 3d → bound asked; retention 0 → asked).
- [ ] **Step 2: Run** `go test ./internal/controller -run 'LabelAdvice|AdviceWindow'`; expected FAIL.
- [ ] **Step 3: Implement**; update the two call sites (`handlers_size.go`, `labelAdviceCounts`/`labelAdviceProblems`) to pass `AdviceOptions{}`.
- [ ] **Step 4: Run** `go test ./internal/controller -run 'LabelAdvice|Advice|Size'`; expected PASS.
- [ ] **Step 5: Commit** `Fetch the runs behind each advice row, within a window retention bounds`.

### Task 3: the route takes `window` and `repo` and says which bound applied

**Files:**
- Modify: `internal/api/handlers_size.go`, `api/openapi.yaml` (`/label-advice` params `window` (string, duration such as `14d`, default 14d), `repo`; response gains `window: AdviceWindow`, `counts.not_enough_data`; `LabelAdvice` schema gains `state`, `min_runs`, `recommended_class`, `reason`, `observed`, `fits` with new schemas `AdviceObserved`, `AdviceFigures`, `AdviceFit`, `AdviceWindow`; the "at least five" sentence names `min_runs`), `docs/api-surface.md:202`, `internal/api/size_test.go`, `internal/api/shapes_test.go` (add LabelAdvice to the shapes it checks)
- Generate: `go run internal/api/gen_openapi.go`, `make openapi`

- [ ] **Step 1: Write the failing tests** extending `TestLabelAdviceIsPagedFilteredAndCounted`: the response carries `window.applied`, `window.bound`; `?repo=` narrows; `?window=0`, `?window=-1d`, `?window=abc` are 400 naming `window` (Review Focus 2); `?window=400d` with retention 30d gives `bound: "retention"`; `?kind=too_small` never returns a `not_enough_data` row; the item shape matches the spec (shapes test).
- [ ] **Step 2: Run** `go test ./internal/api -run 'LabelAdvice|Shapes'`; expected FAIL.
- [ ] **Step 3: Implement**, using `parseAgo`-style parsing on the server (there is `parseDuration`/`queryDuration` in `internal/api/params.go` or add one accepting `d`/`w`); regenerate; update the api-surface row.
- [ ] **Step 4: Run** the tests and `cd web && npm run check`; expected PASS.
- [ ] **Step 5: Commit** `Let label advice be asked for a repository and a window, and say which bound applied`.

### Task 4: `zoomies jobs advice --window --repo`, with the figures

**Files:**
- Modify: `cmd/zoomies/size.go` (`jobsAdvice`: flags `--window` (default `14d`), `--repo`; columns `p95 memory`, `max`, `runs in window`; `not_enough_data` rows printed as "not enough data yet, N of M runs"; the empty-state note takes M from the payload's `min_runs` or, with no rows, from a constant exported by the API? No: print "advice needs a job's first measured runs" without the number), `cmd/zoomies/types.go` (`labelAdviceItem` gains the fields), `cmd/zoomies/size_test.go`, `docs/cli.md:286-295`
- Generate: `make generate`

- [ ] **Step 1: Write the failing tests**: `TestJobsAdviceShowsTheFiguresBehindEachRow` (a row prints `6000 MB` under p95 and `12` runs), `TestJobsAdviceSendsTheWindowAndRepository` (`window=7d`, `repo=acme/widgets` on the wire), `TestJobsAdvicePrintsASparseRowAsNotEnoughData` ("not enough data yet, 2 of 5 runs"; `--` where a figure is 0, Review Focus 4), `TestJobsAdviceRefusesAWindowItCannotRead` (exit 2 for `--window soon`).
- [ ] **Step 2: Run** `go test ./cmd/zoomies -run JobsAdvice`; expected FAIL.
- [ ] **Step 3: Implement**; document in `docs/cli.md`; `make generate`.
- [ ] **Step 4: Run** `go test ./cmd/zoomies`; expected PASS (the reference test included).
- [ ] **Step 5: Commit** `Show the figures behind jobs advice, for one repository and a chosen window`.

### Task 5: the SizeAdvice card shows p95, max and the sample size

**Files:**
- Modify: `web/src/lib/jobs/SizeAdvice.svelte` (each row: `p95 {mb} · max {mb}` in `--z-font-mono`, `{observed.runs} runs in {window}` in `--z-text-xs` muted; a `not_enough_data` row reads "not enough data yet, {runs} of {min_runs} runs" with no Pin button; the lead text no longer says "five" but `{minRuns}` from the first row's `min_runs` or hides the number), `web/src/lib/jobs/size.ts` (`observedWords(o, window)` and `sparseWords(runs, minRuns)`), `web/unit/size-advice-words.test.ts`, `web/tests/auto-pools.spec.ts` (the fixture rows gain `observed`; assert `p95` text and the sparse row)
- Generate: none (types come from Task 3's client)

- [ ] **Step 1: Write the failing unit test**: `observedWords({runs: 12, memory_mb: {p95: 6000, max: 7100}}, 14d)` → `"p95 5.9 GB · max 6.9 GB · 12 runs in 14 days"`; `sparseWords(2, 5)` → `"not enough data yet, 2 of 5 runs"`.
- [ ] **Step 2: Run** `cd web && npm run test:unit`; expected FAIL.
- [ ] **Step 3: Implement**; update the Playwright spec.
- [ ] **Step 4: Run** `npm run lint && npm run check && npm run test:unit`, then `make build VERSION=dev` and the auto-pools spec; expected PASS.
- [ ] **Step 5: Commit** `Put the p95, the max and the sample size beside each size advice`.

### Task 6: the docs name the constants, and a test holds them to the code

**Files:**
- Modify: `docs/auto-pools.md` (after `## Label advice`: `### How the figures are computed`, naming `ProfilePercentile` as the 90th percentile of the newest twenty peaks over fourteen days, `ProfileMemoryMargin` as a fifth added, `ProfileOOMGrowth` as a killed run counted at one and a half times what it reached, `AdviceMinRuns` as five; and `### What is deliberately not inferred`: no class from a single run, no class from a pinned job's history, no figure from runs the retention has pruned, no "fits" from a host that is cordoned or quiet), `docs/api-surface.md`, `docs/problem-codes.md:288` and `cmd/zoomies/size.go`/`internal/mcp/tools.go` text where "five" is literal (derive from `AdviceMinRuns` in code; in prose, name it beside the constant)
- Create: `internal/scheduler/docs_figures_test.go`: `TestTheFiguresSectionNamesTheConstants` reads `../../docs/auto-pools.md` and asserts the section holds the rendered values (`90th`, `a fifth`, `one and a half`, `five`) computed from the constants, so a changed constant fails the test.
- Modify: `roadmap/progress.md` (ZF-236 `in_progress`; ZF-231 `implemented` with #750 once merged)

- [ ] **Step 1: Write the failing docs test**; run; expected FAIL (no section yet).
- [ ] **Step 2: Write the sections**; `mkdocs build --strict` passes; the test passes.
- [ ] **Step 3: Run the whole gate** (Global Constraints).
- [ ] **Step 4: Commit** `Name the figures size advice is computed from`; push; open the pull request as a draft with *How to verify*: `go test ./internal/scheduler -run 'Observe|Advice|Figures'`, `go test ./internal/controller -run 'LabelAdvice'`, `go test ./internal/api -run LabelAdvice`, `go test ./cmd/zoomies -run JobsAdvice`, and against `zoomies demo`: `zoomies jobs advice --window 7d` shows the p95 column.

---

## Self-review

- **Spec coverage.** `observed`, `recommended_class`, `reason`, `fits`, `not_enough_data`: Tasks 1 and 2; `--window`, `--repo`, the bound: Tasks 3 and 4; the card: Task 5; the two docs sections: Task 6; fixtures for over, right, under and sparse, and boundaries at the named constants: Tasks 1 and 2 (the existing advice and profile tests already hold the over/right/under cases; Task 1 adds the sparse row and the `AdviceMinRuns` boundary on both sides).
- **Types.** `Observed`, `Fit`, `AdviceWindow` are defined once (Tasks 1 and 2) and consumed by name in Tasks 3 to 5; `min_runs` travels on every row so the CLI and the card never hard-code the five.
- **Proportion.** The plan is about as long as the spec section is short because the spec is a list; the bodies are the implementer's.
