# Historical implementation record

This is the completed code-review implementation record for `main` at
`be92082` (5 September 2026). Every item carries the review's finding ID, so
the full reasoning remains one lookup away in the review document, alongside
its final status and commit.

It is not an active plan. The repository-root [ROADMAP.md](ROADMAP.md) is the
sole source of truth for current scope, ordering and authorisation; record
delivery status there in [roadmap/progress.md](roadmap/progress.md). Retain
this file as history rather than adding new work to it.

## How the work is staged

1. **Wave 1: the twelve that buy the most.** Confirmed bugs no test reached, plus
   the two registration gaps every operator meets. Each fix ships with a test.
2. **Wave 2: the remaining confirmed bugs.** Write-only fields, false-positive
   warnings, docs that contradict the code.
3. **Wave 3: risks.** Races, leaks and missing guards that will bite under load or
   in a rare deployment shape.
4. **Wave 4: polish and documentation.** Consistency, copy, the missing operator
   pages. Cheap individually; worth batching by area.

Rules of the road: one commit per finding or tightly related group, imperative
sentence commit messages, a test with every behaviour change, `make lint` and
`make test` green before a push, docs updated in the same commit as the code
they describe.

## Wave 1: fix first

Done: 12 items, shipped in PR #62 (merged as 066806b): E01, C01, C02, A03, C03,
A02, U03, A04, A07, E04, B01, U01. Each has its commit on the branch and its
reasoning in the review document; the notes that outlived the wave are under
*Decisions worth recording*.

## Wave 2: remaining bugs

Done: 21 items, shipped in PR #63 (merged as 28520e8): A01, A05, A06, C04, C05,
C06, D01, D02, D03, D04, D05, E02, E03, E05, E06, P01, P02, P03, P04, P05, U02.
Each has its commit on the branch and its reasoning in the review document; the
notes that outlived the wave are under *Decisions worth recording*.

## Wave 3: risks

Done: 33 items, shipped in PR #64 (merged as 67ffddc): A08, A09, A10, A11, A12,
A13, A14, A15, C08, C09, C10, C11, C12, C13, C14, C22, E07, E08, E09, E10, E11,
E12, E13, E14, E15, E16, E17, E18, E19, U04, U05, U06, U07. Each has its commit
on the branch and its reasoning in the review document; the notes that outlived
the wave are under *Decisions worth recording*.

## Reported since the review

Things found in use rather than in the review, kept here so the plan stays the
one list.

- [x] **N01** [bug] A runner that died on creation was replaced in the same pass that
  noticed, so a pool with a bad image churned through a runner a second and spent two
  GitHub API calls each time, `internal/scheduler/scheduler.go`, done, the scheduler
  holds a pool back after a start failure (10 s, doubling to 5 min), keeps failed runners
  on the page for ten minutes, and raises `pool.runners_failing`; seen on the dev
  instance (video, 5 September). The cause of that instance's failures is still to be
  read off its Runners page now that the message stays there.
- [ ] **N02** [bug] Every runner on the dev instance sits in `registering` and never
  comes up, `internal/controller/agents.go`, seen on `zoomies-linux-64` on 6 September
  (screenshot), on a build from before Wave 2: its Jobs page showed copy that PR #63
  replaced. **Not reproducible in `main`, and the handoff was traced end to end after
  Wave 4d without finding a defect.** What was checked, so nobody checks it twice:

  * The one signal is sound. `agent.observe` reports a live workload with no asserted
    state and the backend's phase; `controller.applyReports` upgrades `registering` to
    `idle` on exactly that. Both halves have tests
    (`TestReconcileSamplesStatsForRunningWorkloads`, and `mustReportRunning` through the
    job lifecycle specs).
  * The container labels line up: `Spec.Labels` stamps the runner ID, `docker.Create`
    adds the role label, and `List` filters on both -- so a running container is seen,
    tracked and observed rather than mistaken for an orphan.
  * The backstop works. `scheduler.reap` runs before the enabled check, so
    `provision_timeout` fails a stuck runner even in a disabled pool, and the message
    names the state and the age.
  * A restart cannot leave rows behind: an embedded agent re-joins under the same host
    name, and `join` deletes the previous host row, cascading its runners away.

  Three fixes that landed since the screenshot each remove one candidate cause: A03
  (reclaiming a host that stopped heartbeating), E07 (the idempotent create path that
  used to rebuild a runner mid-job), and N01 (the start-failure backoff, which keeps a
  failed runner and its reason on the page for ten minutes).

  The remaining step is unchanged: deploy `main` and look again. What has changed is
  what an operator sees if it recurs. `runners.not_progressing` now says it, at half
  the provision timeout, and splits the two shapes so the next look starts from an
  answer rather than three sources: a runner still waiting for a container is the agent
  or the image, and one whose container started without registering is the runner
  process failing to reach GitHub. Whichever it is on that instance is the diagnosis
  this entry was missing.
- [x] **N03** [bug] The Jobs page called every queued job no pool claims a job that
  will never run, in red, and the problems drawer said nothing would run them; the
  installation's webhooks cover jobs on GitHub's own runners, on a hosted-runner vendor
  and on any other self-hosted provider in the organisation, so next to another provider
  that was every job, `web/src/lib/jobs/UnmatchedNote.svelte`, done, jobs whose labels
  all name GitHub's or a vendor's runners are `hosted` in the view and badged neutrally,
  a queued job no pool here claims is reported only after two minutes, and the note, the
  problem and the log line give both readings (screenshot, 6 September).
- [x] **N04** [bug] The Overview's Active jobs panel asked for every running job and
  headed the answer "what the fleet is running at this moment", so a job on another
  provider's runner was presented as this fleet's; nothing marked it and nothing
  filtered it, `web/src/lib/overview/ActiveJobs.svelte`, done, both job panels ask
  the server for this fleet's own work and share one persisted switch that widens them,
  every row no runner here ran is badged `Elsewhere`, and Recent outcomes' live frames
  now use the same predicate as its fetch (screenshot, 6 September).
- [x] **N05** [bug] A pool could not be edited at all: the wizard's review step calls
  `POST /pools/validate`, which never said which pool it was a dry run of, so the name
  check compared the pool against every pool including itself and refused with "a pool
  called X already exists", about itself. The only way to save any edit was to rename
  the pool as well, `internal/api/handlers_pools.go:437`, done, the endpoint takes
  `?id=` and the edit form passes it, with a test covering all three cases (reported
  6 September).
- [x] **N06** [gap] Ticking a pool offered Enable and Disable but not Edit, so an
  operator who had ticked a row to disable it had to untick, find the row again and
  open its menu to change anything else, `web/src/routes/Pools.svelte`, done, a bulk
  action can declare that it acts on one row, and Edit is offered whatever is selected
  and says why it is disabled while more than one is ticked (reported 6 September).

## Wave 4: polish, gaps and nits

Shipped in four batches. **4a** (16 items, PR #65, merged as afb1d50): the Go
core, the API contract and the controller. **4b** (11 items, PR #66, merged as
bd1582b): backends, agent, auth, installer, deploy and the build. **4c** (22
items, PR #67, merged as 0d3010d): the web UI's tokens, state, copy and specs,
plus N05 and N06. **4d** (14 items): the documentation -- the reference pages
and operator pages that did not exist, the corrections, and one home per
paragraph.

That closes the review. Two items are open and neither is a code change waiting
to be written: **D16** is a brand decision for the maintainer, and **N02** needs
`main` deployed before it can be looked at again.

### Go core: store, scheduler, config, events, migrate

- [x] **C07** [polish] `agent.root` warns about an agent process in a controller that runs no agent, `internal/config/validate.go:469-476`, done, agent.root fires only where an agent runs, gated on the same predicate as the rest of the agent section
- [x] **C15** [polish] Three different definitions of a failed job, `internal/store/queries_events.go:485`, done, store.FailedConclusions is the one list, spelt into SQL and Go; FailedStep is where a job stopped, not whether
- [x] **C16** [polish] `LIKE` searches do not escape `%` and `_`, `internal/store/queries_fleet.go:749`, done, every LIKE escapes % _ and \ and says ESCAPE
- [x] **C17** [polish] The scale-up reason ignores the repository quota, `internal/scheduler/scheduler.go:344-351`, done, the reason counts the admitted jobs and names the deferred ones and their repositories
- [x] **C18** [polish] `bind: localhost:8080` is treated as a public bind, `internal/config/config.go:469`, done, one loopbackHost helper answers for bind, external URL, origins and OIDC issuer
- [x] **C19** [polish] Block-sequence `runs-on` items keep trailing comments inside the label, `internal/migrate/runson.go:338`, done, items are split from their comments before classification; a commented item or a comment inside the list leaves the job alone with a reason
- [x] **C20** [nit] `Duration` decodes a bare integer as seconds from YAML but nanoseconds from JSON, `internal/store/models.go:481`, done, both decoders refuse a bare number with the same sentence
- [x] **C21** [nit] Small inconsistencies in config, scheduler and events, `internal/config/config.go:563`, done, tls.mode lowercased from the file, errors.Is for EOF, warning accepted, lifetime wording fixed everywhere, Subscribe watcher ends on Close

### API and controller

- [x] **A16** [polish] Eight routes are registered but absent from the OpenAPI document that claims to be the whole surface, `internal/api/router.go:94-95`, done, the OIDC and agent routes are in the spec (agent ones x-internal with their own security scheme), the three roles are recorded, and the test walks the router as well as the spec
- [x] **A17** [polish] `runner.deleted` is documented and handled by the UI but never published, `internal/events/bus.go:27`, done, the store deletes return the runner rows they took, DELETE ... RETURNING, and the controller announces each before the pool, host or installation
- [x] **A18** [polish] `stage` is on `Runner` in the spec and on `TimelineEntry` in the code, `api/openapi.yaml:2302-2330`, done, stage moved to TimelineEntry
- [x] **A19** [polish] `DELETE /installations/{id}` force-kills running jobs without saying so, `internal/api/handlers_installations.go`, done, documented in the spec, the API page and the dialog, with the pool drain as the way to keep the jobs
- [x] **A20** [polish] Stats percentiles come from a silently truncated 500-row sample, computed twice per interval, `internal/controller/stats.go:109`, done, StartupSamples queries the window; the page, its fake limit and the loop are gone
- [x] **A21** [polish] The login 429 never carries `Retry-After`, `internal/api/errors.go:151-158`, done, the auth service exposes the limiter window and the handler sends it; the spec documents the header
- [x] **A22** [polish] The host placement rule is copied four times, and 600 lines of GitHub orchestration sit in the transport package, `internal/scheduler/scheduler.go:507`, done, scheduler.HostCanRun and its parts are the rule; HostFit and the migration service live in the controller
- [x] **A23** [nit] Small API and controller inconsistencies, `internal/api/handlers_agents.go:163`, done, all seven: quiet log cuts, 400 for malformed deliveries, scoped cordon warning, aggregate failed count, demo addresses, list shapes documented, Cache-Control: no-store

### Backends, agent, auth, installer, CLI and deploy

- [x] **E20** [polish] Error text names commands that do not exist, `internal/auth/auth.go:84`, done, the join-token message names the real command, and `zoomies users passwd` now exists, reading the password from the terminal or stdin
- [x] **E21** [polish] `POST /pools/{id}/prewarm` writes no audit row, `internal/api/handlers_pools.go:619-636`, done, pool.prewarm writes a row naming the pool and the image
- [x] **E22** [polish] Binary and image disagree on the version string, and the image has no build date, `.github/workflows/release.yml:28`, done, both release jobs take the version from one expression, the Makefile strips the v, the image learns a build date, and each version field is filled in on its own
- [x] **E23** [polish] `classify` leaves GitHub 422s unmapped, `internal/github/app.go:698-714`, done, a 422 is github.ErrInvalid carrying the field-by-field reason, answered by the API as a 422
- [x] **E24** [polish] No `HEALTHCHECK` in `deploy/Dockerfile`, and none on the docker-run deployment, `deploy/Dockerfile`, done, the image declares the HEALTHCHECK, in exec form, which a docker run inherits
- [x] **E25** [polish] `install.sh` hints name a wrong path and a service that is never installed, `install.sh:977`, done, the downgrade hint names the real database path and the OpenRC hint names the container
- [x] **E26** [polish] The process backend puts the JIT config on the command line, `internal/backend/process.go:311`, done, the JIT config goes in ACTIONS_RUNNER_INPUT_JITCONFIG, not argv
- [x] **E27** [polish] `deploy/*.service` are static copies that have already drifted from the installer templates, `deploy/zoomies.service`, done, deleted; the installer templates are the only copy
- [x] **E29** [polish] The CLI's own examples use the wrong ID prefix and an unbranded label list, `cmd/zoomies/pools.go:95`, done, branded labels and ins_ IDs in the CLI examples
- [x] **E28** [nit] Small installer, agent and deploy nits, `install.sh:584`, done, poll backoff jitter, no gid guess in compose, runners.create removed, agent.registry_auth added, no npm install fallback, install.sh wrapped in main with its downloads pinned to https

### Web UI: behaviour and state

- [x] **U08** [polish] `Usage.svelte` bypasses the API client, the schema, the components, the tokens and the date conventions, `web/src/routes/Usage.svelte:5-36`, done, rebuilt on client.ts, the generated schema, DateRange, LoadingBoundary and the primitives; pools are named, the range and grouping live in the address bar so a report is a link, and the CSV carries the same range
- [x] **U09** [polish] Toast eviction can drop an un-dismissed error, `web/src/lib/state/toasts.svelte.ts:73`, done, eviction takes the oldest toast that was leaving anyway, so a run of successes cannot push an unread refusal off the screen; a spec drives it through the pools page
- [x] **U10** [polish] Constants and components duplicated, including one the code says it removed, `web/src/lib/settings/AccountPanel.svelte:19`, done, one definition each: MIN_PASSWORD_LENGTH imported rather than repeated, lib/roles.ts, lib/addresses.ts, lib/pools/consequences.ts, and one Panel in lib/components
- [x] **U11** [polish] `aria-rowcount` without `aria-rowindex`, `web/src/lib/components/DataGrid.svelte:437`, done, the header is row 1 and each row carries offset + index + 2, so the announced total is reconcilable
- [x] **U13** [polish] Playwright does not exercise several documented behaviours, `web/tests`, done, new specs for the g chords and / and ?, the Audit page, Settings create/edit/delete for accounts and tokens, the log viewer, a dropped stream, and an optimistic change the controller refuses
- [x] **U12** [nit] Small UI code nits, `web/src/lib/api/types.ts:164-181`, done, EVENT_KINDS deleted, the palette guarded while closed and its scrim made a plain element, Jobs validates ?state=, a refused confirmation reports cancellation, the router caches loaded components, and the two serve scripts share one harness that waits for exit

### Web UI: design, copy and consistency

- [x] **P06** [polish] Five breakpoints beyond the two the guidelines allow, `web/src/lib/overview/FirstRun.svelte:412`, done, the four strays all meant "stack this on a phone" and say 768; the one min-width: 1181px is the other side of the same threshold and says so
- [x] **P07** [polish] 359 raw `px` values in 107 files against a rule that says never, `web/src/lib/components/DataGrid.svelte:592-605`, done, 402 raw px across 110 files becomes 32: border widths in three weights, sub-grid nudges, drawn-control sizes, tracking and the dialog and drawer measures are tokens, and the rule now names the three things that stay raw and why
- [x] **P08** [polish] The guidelines' token tables and the token file disagree, `web/src/lib/styles/tokens.css`, done, the shadow and weight tables match the file, --z-focus-ring and --z-accent-active have consumers, Brand White and Cool Grey are derived rather than repeated, and the danger button takes its own contrast colour
- [x] **P09** [polish] Six metric tiles in a four-column grid, `web/src/lib/overview/FleetMetrics.svelte`, done, the column count is a divisor of six at every width, so no row is left part empty
- [x] **P10** [polish] Name cells wrap instead of truncating, and truncated text has no `title`, `web/src/routes/Runners.svelte:286-291`, done, one line with an ellipsis and the full value in a title, in both grids and for any column the grid renders itself
- [x] **P11** [polish] Focus ring removed without a visible replacement, `web/src/lib/shell/CommandPalette.svelte:473`, done, the palette's search row and the label field carry a real ring drawn as a box-shadow, and the wizard step uses :focus-visible
- [x] **P12** [polish] Raw controls miss the 16 px phone rule, `web/src/lib/components/Pagination.svelte:118-127`, done, pagination's select, the date range and the label field have the phone rule -- and the rule now beats the size selectors it was losing to
- [x] **P13** [polish] Phone top bar shows `Ctrl K`, and the bottom-nav pill misaligns its icon, `web/src/lib/shell/TopBar.svelte:260-265`, done, the key cap goes on a phone and the button carries its own name, and the bottom-nav entries centre their icons
- [x] **P14** [polish] Copy inconsistencies: `--` in rendered text, mixed placeholders, a raw role id, diverging empty states, `web/src/lib/overview/FirstRun.svelte:165`, done, real em dashes in rendered copy including two server sentences, the role label map on the account panel, and the guidelines' empty-state examples match the pages
- [x] **P15** [polish] Three grids render state three ways, and the busy hue and Play icon are spent twice, `web/src/lib/runners/RunnerStateCell.svelte`, done, one state cell in lib/components used by all three grids, a power switch for enable and disable, and a neutral fill for slot occupancy
- [x] **P16** [polish] External links open inconsistently, and the footer's own rule is not true, `web/src/lib/shell/AppFooter.svelte:43`, done, one rule -- a link that leaves the product opens a new tab and says so -- with a newTab prop on Button and the guidelines rewritten around it
- [x] **P17** [polish] Tall label rows, a doubled button and US dates in the shipped screenshots, `web/src/lib/jobs/JobLabels.svelte`, done, chips stay on one line in a grid and wrap only where the whole set is on show, the Hosts page keeps one Add a host button, and the screenshot runner pins en-GB and Europe/London
- [x] **P18** [polish] Number formatting bypassed in two grids, `web/src/routes/Pools.svelte:348`, done, both counts go through formatNumber
- [x] **P20** [polish] The a11y spec covers four of ten pages and the mobile spec asserts the opposite of the guidelines, `web/tests/a11y.spec.ts`, done, the audit covers all ten pages and gained a drawer trap test, a reduced-motion check, the live regions and reflow at 200% zoom; the mobile spec asserts the 16px control rule
- [x] **P19** [nit] Small design-system nits, `web/src/lib/components/Field.svelte:80-89`, done, the stale Field comment removed, the shortcut sheet lower-cased to match the nav, weights, tracking and easing added to the @theme mapping, --z-danger-contrast used, and the docs say why the nav shortens Migrate
- [x] **P21** [nit] `theme-color` is fixed to near-black in both themes, `web/index.html`, done, two media-scoped tags for the system default, pinned by theme.svelte.ts when the operator has chosen a side

### Docs, README and the site

- [x] **D06** [polish] Eight warning codes are emitted but documented nowhere, and the third severity is undocumented, `internal/config/validate.go:253`, done, docs/problem-codes.md documents all 70 codes grouped by area, the third severity is named in CLAUDE.md, and internal/docs asserts the page stays complete
- [x] **D08** [polish] `dependencies.md` has a row for an indirect dependency and none for `@types/node`, `docs/dependencies.md:28`, done, the indirect row dropped and @types/node given one, in the same why-not-what voice
- [x] **D09** [polish] The FAQ's structured data has 12 questions against 17 headings, `docs/faq.md:11-19`, done, the structured data is generated from the page's own headings, so it cannot fall behind again
- [x] **D10** [polish] Configuration and brand pages: missing env names, an undocumented env var, root-only paths shown unconditionally, wrong counts, `docs/configuration.md:119-121`, done, the OIDC env names added, the undocumented override documented, a config-dir/state-dir table with placeholders replacing the root-only paths, and every count corrected
- [x] **D11** [polish] The sample scheduler line is not what a default install prints, `docs/quickstart.md:180`, done, the sample line is what a default install actually prints
- [x] **D12** [polish] Alt text drifts per image, and 'every page' is photographed except three, `README.md:40`, done, one description per screenshot across README, ui.md and migration.md, with TestOneDescriptionPerScreenshot holding it there, and the two missing pages photographed
- [x] **D13** [polish] Paragraphs duplicated across README, home page and quick start have already diverged, and a maintainer TODO sits in operator docs, `README.md:207-215`, done, the five duplicated passages each have one home and the rest point at it; the README keeps the claim and drops the specification
- [ ] **D16** [polish] The brand descriptor is 'Self-hosted Git runners', `docs/brand.md:190`, **for the maintainer**: the descriptor is drawn into the wordmark artwork, not just set in the page, so correcting it to "GitHub Actions" means redrawing the lockup. That is a brand call rather than a docs fix, and it is left for whoever owns the mark.
- [x] **D17** [polish] Two numbers for the Go version, and none for Node in the manifest, `README.md:368`, done, one Go version, and Node given a floor in the manifest
- [x] **D18** [polish] The docs and a CI comment say there is no release yet; `v0.1-alpha` was published on 4 September, `docs/configuration.md:223-224`, done, folded into B01
- [x] **D19** [polish] Capacity-demand semantics understated; missing description; stale hook comment, `docs/capacity-demand-receiver.md:10-11`, done, the semantics stated in full, a description added, and the stale hook comment corrected
- [x] **D07** [gap] `capacity-demand-receiver.md` is orphaned, `docs/capacity-demand-receiver.md`, done, the page is in the Guide, alongside the three new operator pages
- [x] **D15** [gap] The operator pages that do not exist, `docs/`, done, troubleshooting.md, upgrading.md, backup-and-restore.md, SECURITY.md, cli.md and metrics.md written, with internal/docs asserting the last two stay complete
- [x] **D14** [nit] Voice and reference nits across the docs, `docs/architecture.md`, done, ten unlabelled fences given a language, the component inventory corrected, CLAUDE.md's counts and deploy/ claim fixed, and the layout blocks now tested against the repository
- [x] **D20** [nit] The state diagram omits two allowed edges, `docs/architecture.md`, done, the two allowed edges are in the diagram

### Build, CI and release

- [x] **B02** [polish] The xterm route chunk exceeds the documented route budget, and the shell budget counts route CSS, `web/vite.config.ts:34`, done, the shell counts only the entry and its static imports; routes are enforced with xterm named in ROUTE_ALLOWANCES
- [x] **B03** [nit] Workflow and Makefile nits, `.github/workflows/release.yml:12-13`, done, release.yml uses its own env pins, make lint uses git ls-files, openapi drops its build dependency

## Decisions worth recording

- `:latest` on the published images now means the most recent release, moved by
  `release.yml` only; `main` is the moving tag.
- A host that has not heartbeated for `hostLostAfter` has its runners failed and
  replaced; a host that is merely late (past the 90 s health timeout but inside
  that grace) is marked unhealthy and left alone.
- `CF-Connecting-IP` is believed only from Cloudflare's own address ranges,
  whatever else is in `trusted_proxies`.
- The process backend ships digests for the runner release it pins; the digest
  table is generated (`go run internal/backend/gen_runner_digests.go`) and the
  version bump workflow regenerates it, and the runner image checks its download
  against the same numbers.
- A repository on a personal account is a target too: the docs say organisation
  *or* repository wherever they used to say organisation.
- The running configuration is an immutable snapshot (`config.Live`); the
  controller's `UpdateConfig` is the only writer and retunes the timers and the
  log level, so a runtime setting is in effect when the API says it is.
- A failed runner stays on the Runners page for ten minutes, and a pool whose
  runners die before registering waits before creating another, doubling from
  ten seconds to five minutes. Runners that ran a job and then failed do not
  count against the pool.
- A job GitHub holds for a deployment review is `waiting`, a state of its own
  ahead of `queued`; the jobs table was rebuilt (migration 0009) to admit it.
- A queued job GitHub has said nothing about for a day is retired as
  `completed` / `stale`, which is what GitHub itself does with it.
- Event ids are `<epoch>.<sequence>`; a stream whose gap could not be replayed
  opens with a `resync` frame and the UI fetches afresh on it.
- A single sign-on identity links by username only to an account made for SSO;
  taking over a password account is `oidc.link_by_username`, off and warned about.
- Process-backend runners lead their own process group and the units say
  `KillMode=process`, so a stop or restart of the agent reaches the agent only.
- Refusals the auth service makes are `auth.ErrInvalidInput` and answer 422; every
  other error is a 500 with a request ID, never quoted to an anonymous caller.
