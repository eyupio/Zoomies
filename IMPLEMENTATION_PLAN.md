# Implementation plan

The working list for acting on the code review of `main` at `be92082`
(5 September 2026). Every item carries the review's finding ID, so the
full reasoning is one lookup away in the review document, and a status:
`todo`, `in progress`, `done` (with the commit), or `wontfix` (with why).

Keep it current: when a change lands, tick the box, write the commit, and
move anything it made unnecessary to *wontfix* rather than deleting it. A
plan that only grows is a plan nobody reads, so finished waves collapse to
their summary line once the next wave starts.

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

Done: 33 items, shipped in the Wave 3 pull request from this branch: A08, A09,
A10, A11, A12, A13, A14, A15, C08, C09, C10, C11, C12, C13, C14, C22, E07, E08,
E09, E10, E11, E12, E13, E14, E15, E16, E17, E18, E19, U04, U05, U06, U07. Each
has its commit on the branch and its reasoning in the review document; the notes
that outlived the wave are under *Decisions worth recording*.

## Reported since the review

Things found in use rather than in the review, kept here so the plan stays the
one list.

- [x] **N01** [bug] A runner that died on creation was replaced in the same pass that
  noticed, so a pool with a bad image churned through a runner a second and spent two
  GitHub API calls each time — `internal/scheduler/scheduler.go` — done, the scheduler
  holds a pool back after a start failure (10 s, doubling to 5 min), keeps failed runners
  on the page for ten minutes, and raises `pool.runners_failing`; seen on the dev
  instance (video, 5 September). The cause of that instance's failures is still to be
  read off its Runners page now that the message stays there.

## Wave 4: polish, gaps and nits

### Go core: store, scheduler, config, events, migrate

- [ ] **C07** [polish] `agent.root` warns about an agent process in a controller that runs no agent — `internal/config/validate.go:469-476`
- [ ] **C15** [polish] Three different definitions of a failed job — `internal/store/queries_events.go:485`
- [ ] **C16** [polish] `LIKE` searches do not escape `%` and `_` — `internal/store/queries_fleet.go:749`
- [ ] **C17** [polish] The scale-up reason ignores the repository quota — `internal/scheduler/scheduler.go:344-351`
- [ ] **C18** [polish] `bind: localhost:8080` is treated as a public bind — `internal/config/config.go:469`
- [ ] **C19** [polish] Block-sequence `runs-on` items keep trailing comments inside the label — `internal/migrate/runson.go:338`
- [ ] **C20** [nit] `Duration` decodes a bare integer as seconds from YAML but nanoseconds from JSON — `internal/store/models.go:481`
- [ ] **C21** [nit] Small inconsistencies in config, scheduler and events — `internal/config/config.go:563`

### API and controller

- [ ] **A16** [polish] Eight routes are registered but absent from the OpenAPI document that claims to be the whole surface — `internal/api/router.go:94-95`
- [ ] **A17** [polish] `runner.deleted` is documented and handled by the UI but never published — `internal/events/bus.go:27`
- [ ] **A18** [polish] `stage` is on `Runner` in the spec and on `TimelineEntry` in the code — `api/openapi.yaml:2302-2330`
- [ ] **A19** [polish] `DELETE /installations/{id}` force-kills running jobs without saying so — `internal/api/handlers_installations.go`
- [ ] **A20** [polish] Stats percentiles come from a silently truncated 500-row sample, computed twice per interval — `internal/controller/stats.go:109`
- [ ] **A21** [polish] The login 429 never carries `Retry-After` — `internal/api/errors.go:151-158`
- [ ] **A22** [polish] The host placement rule is copied four times, and 600 lines of GitHub orchestration sit in the transport package — `internal/scheduler/scheduler.go:507`
- [ ] **A23** [nit] Small API and controller inconsistencies — `internal/api/handlers_agents.go:163`

### Backends, agent, auth, installer, CLI and deploy

- [ ] **E20** [polish] Error text names commands that do not exist — `internal/auth/auth.go:84`
- [ ] **E21** [polish] `POST /pools/{id}/prewarm` writes no audit row — `internal/api/handlers_pools.go:619-636`
- [ ] **E22** [polish] Binary and image disagree on the version string, and the image has no build date — `.github/workflows/release.yml:28`
- [ ] **E23** [polish] `classify` leaves GitHub 422s unmapped — `internal/github/app.go:698-714`
- [ ] **E24** [polish] No `HEALTHCHECK` in `deploy/Dockerfile`, and none on the docker-run deployment — `deploy/Dockerfile`
- [ ] **E25** [polish] `install.sh` hints name a wrong path and a service that is never installed — `install.sh:977`
- [ ] **E26** [polish] The process backend puts the JIT config on the command line — `internal/backend/process.go:311`
- [ ] **E27** [polish] `deploy/*.service` are static copies that have already drifted from the installer templates — `deploy/zoomies.service`
- [x] **E29** [polish] The CLI's own examples use the wrong ID prefix and an unbranded label list — `cmd/zoomies/pools.go:95` — done, branded labels and ins_ IDs in the CLI examples
- [ ] **E28** [nit] Small installer, agent and deploy nits — `install.sh:584`

### Web UI: behaviour and state

- [ ] **U08** [polish] `Usage.svelte` bypasses the API client, the schema, the components, the tokens and the date conventions — `web/src/routes/Usage.svelte:5-36`
- [ ] **U09** [polish] Toast eviction can drop an un-dismissed error — `web/src/lib/state/toasts.svelte.ts:73`
- [ ] **U10** [polish] Constants and components duplicated, including one the code says it removed — `web/src/lib/settings/AccountPanel.svelte:19`
- [ ] **U11** [polish] `aria-rowcount` without `aria-rowindex` — `web/src/lib/components/DataGrid.svelte:437`
- [ ] **U13** [polish] Playwright does not exercise several documented behaviours — `web/tests`
- [ ] **U12** [nit] Small UI code nits — `web/src/lib/api/types.ts:164-181`

### Web UI: design, copy and consistency

- [ ] **P06** [polish] Five breakpoints beyond the two the guidelines allow — `web/src/lib/overview/FirstRun.svelte:412`
- [ ] **P07** [polish] 359 raw `px` values in 107 files against a rule that says never — `web/src/lib/components/DataGrid.svelte:592-605`
- [ ] **P08** [polish] The guidelines' token tables and the token file disagree — `web/src/lib/styles/tokens.css`
- [ ] **P09** [polish] Six metric tiles in a four-column grid — `web/src/lib/overview/FleetMetrics.svelte`
- [ ] **P10** [polish] Name cells wrap instead of truncating, and truncated text has no `title` — `web/src/routes/Runners.svelte:286-291`
- [ ] **P11** [polish] Focus ring removed without a visible replacement — `web/src/lib/shell/CommandPalette.svelte:473`
- [ ] **P12** [polish] Raw controls miss the 16 px phone rule — `web/src/lib/components/Pagination.svelte:118-127`
- [ ] **P13** [polish] Phone top bar shows `Ctrl K`, and the bottom-nav pill misaligns its icon — `web/src/lib/shell/TopBar.svelte:260-265`
- [ ] **P14** [polish] Copy inconsistencies: `--` in rendered text, mixed placeholders, a raw role id, diverging empty states — `web/src/lib/overview/FirstRun.svelte:165`
- [ ] **P15** [polish] Three grids render state three ways, and the busy hue and Play icon are spent twice — `web/src/lib/runners/RunnerStateCell.svelte`
- [ ] **P16** [polish] External links open inconsistently, and the footer's own rule is not true — `web/src/lib/shell/AppFooter.svelte:43`
- [ ] **P17** [polish] Tall label rows, a doubled button and US dates in the shipped screenshots — `web/src/lib/jobs/JobLabels.svelte`
- [ ] **P18** [polish] Number formatting bypassed in two grids — `web/src/routes/Pools.svelte:348`
- [ ] **P20** [polish] The a11y spec covers four of ten pages and the mobile spec asserts the opposite of the guidelines — `web/tests/a11y.spec.ts`
- [ ] **P19** [nit] Small design-system nits — `web/src/lib/components/Field.svelte:80-89`
- [ ] **P21** [nit] `theme-color` is fixed to near-black in both themes — `web/index.html`

### Docs, README and the site

- [ ] **D06** [polish] Eight warning codes are emitted but documented nowhere, and the third severity is undocumented — `internal/config/validate.go:253`
- [ ] **D08** [polish] `dependencies.md` has a row for an indirect dependency and none for `@types/node` — `docs/dependencies.md:28`
- [ ] **D09** [polish] The FAQ's structured data has 12 questions against 17 headings — `docs/faq.md:11-19`
- [ ] **D10** [polish] Configuration and brand pages: missing env names, an undocumented env var, root-only paths shown unconditionally, wrong counts — `docs/configuration.md:119-121`
- [ ] **D11** [polish] The sample scheduler line is not what a default install prints — `docs/quickstart.md:180`
- [ ] **D12** [polish] Alt text drifts per image, and 'every page' is photographed except three — `README.md:40`
- [ ] **D13** [polish] Paragraphs duplicated across README, home page and quick start have already diverged, and a maintainer TODO sits in operator docs — `README.md:207-215`
- [ ] **D16** [polish] The brand descriptor is 'Self-hosted Git runners' — `docs/brand.md:190`
- [ ] **D17** [polish] Two numbers for the Go version, and none for Node in the manifest — `README.md:368`
- [x] **D18** [polish] The docs and a CI comment say there is no release yet; `v0.1-alpha` was published on 4 September — `docs/configuration.md:223-224` — done, folded into B01
- [ ] **D19** [polish] Capacity-demand semantics understated; missing description; stale hook comment — `docs/capacity-demand-receiver.md:10-11`
- [ ] **D07** [gap] `capacity-demand-receiver.md` is orphaned — `docs/capacity-demand-receiver.md`
- [ ] **D15** [gap] The operator pages that do not exist — `docs/`
- [ ] **D14** [nit] Voice and reference nits across the docs — `docs/architecture.md`
- [ ] **D20** [nit] The state diagram omits two allowed edges — `docs/architecture.md`

### Build, CI and release

- [ ] **B02** [polish] The xterm route chunk exceeds the documented route budget, and the shell budget counts route CSS — `web/vite.config.ts:34`
- [ ] **B03** [nit] Workflow and Makefile nits — `.github/workflows/release.yml:12-13`

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
