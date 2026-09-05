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

- [x] **E01** [bug] The process backend cannot download a runner in any shipped configuration — `internal/backend/process.go:81` — done, digests shipped for 2.337.0 in a generated table, three agent.* settings with env overrides and docs, bump workflow regenerates the table
- [x] **C01** [bug] `UpdateRunner` binds 20 arguments to 19 placeholders, so it can never update a row — `internal/store/queries_fleet.go:810-818` — done, every column bound, round-trip test
- [x] **C02** [bug] Usage grouped by installation fails whenever a job in the window has no pool — `internal/store/queries_usage.go:67` — done, COALESCE on the join key, test with a pool-less job
- [x] **A03** [bug] Runners on a host that stops heartbeating are never failed, and a stuck draining runner is never reaped — `internal/controller/agents.go:681-685` — done, runners on a host silent past hostLostAfter (5m) are failed and their job marked lost; a stop dropped after three deliveries fails its runner
- [x] **C03** [bug] A draining runner absorbs a queued job's demand, so the job waits for the drain to finish — `internal/scheduler/scheduler.go:224-260` — done, draining counted on the demand side, two tests
- [x] **A02** [bug] A slow dashboard silently loses frames; the SSE handler's recovery branch is dead code — `internal/events/bus.go:142-151` — done, the bus ends a slow subscriber's feed so the browser reconnects and replays
- [x] **U03** [bug] Removed runners never leave the fleet cache — `web/src/lib/state/fleet.svelte.ts:149-153` — done, removed frames delete the cache entry
- [x] **A04** [bug] The usage CSV is open to formula injection — `internal/api/handlers_usage.go:86-88` — done, csvText prefixes formula characters with an apostrophe
- [x] **A07** [risk] `CF-Connecting-IP` is believed from any trusted proxy, not only Cloudflare — `internal/api/auth.go:399-403` — done, header believed only from Cloudflare's own ranges; tests for both peers
- [x] **E04** [bug] `install.sh --answers` does not imply `--yes`, so an unattended run can exit 0 without configuring the host — `install.sh:204-205` — done, --answers implies --yes; agent mode is elevated like the controller (E05)
- [x] **B01** [risk] CI still overwrites `:latest` from main now that a release exists — `.github/workflows/ci.yml` — done, :latest dropped from the main images job; docs and summary rewritten
- [x] **U01** [bug] The Usage page is half-registered: `g u` is advertised in the navigation but does nothing — `web/src/lib/shell/Nav.svelte:43` — done, g u, palette entry, sitemap, Playwright sections and the guidelines all list Usage; the page's scroll frame no longer collides with Tailwind's .table utility, which widened the phone viewport and put the footer under the bottom bar

## Wave 2: remaining bugs

- [ ] **A01** [bug] Pool responses omit two fields the API accepts, so they are write-only — `internal/controller/views.go:369-398`
- [ ] **A05** [bug] `retention.audit` never prunes the audit log; it prunes scaling events — `internal/controller/background.go:116-119`
- [ ] **A06** [bug] Every agent join is audited twice — `internal/controller/agents.go:377`
- [ ] **C04** [bug] `crypto.key_in_config` warns when the key came from the environment and a config file merely exists — `internal/config/validate.go:320`
- [ ] **C05** [bug] The docs say a bare GHES hostname is accepted; the validator refuses to start on one — `docs/configuration.md:200`
- [ ] **C06** [bug] Three server timeouts have no `ZOOMIES_*` override — `internal/config/config.go:66-68`
- [ ] **D01** [bug] The sitemap never receives the per-page git date the SEO hook exists to provide — `hooks/seo.py:100-104`
- [ ] **D02** [bug] The README's headline `pools create` example is rejected — `README.md:311`
- [ ] **D03** [bug] Installation IDs are shown as `inst_`; the store mints `ins_` — `docs/hosts-and-pools.md:129`
- [x] **D04** [bug] The UI guidelines list nine pages and eight chords; the product has ten and nine — `docs/ui-guidelines.md:271-279` — done, folded into U01: the guidelines list ten pages and the u and m chords
- [ ] **D05** [bug] The security page says every dangerous toggle produces a startup warning and a UI entry; half do not — `docs/security.md:218-222`
- [x] **E02** [bug] The two runner version pins have diverged and only one is bumped automatically — `internal/backend/process.go:63` — done, folded into E01: DefaultRunnerVersion is 2.337.0 and the workflow bumps it
- [ ] **E03** [bug] The agent's version-skew warning fires on every release build — `internal/agent/daemon.go:634`
- [x] **E05** [bug] The documented add-a-host one-liner spends the join token and then fails to install the unit — `install.sh:1041` — done, folded into E04
- [ ] **E06** [bug] Answer-file keys `agent.name`, `agent.labels` and `agent.ca_file` are silently ignored — `internal/installer/join.go:57`
- [ ] **P01** [bug] A `startup_failure` job renders as a neutral 'Completed' with a check icon — `web/src/lib/status.ts:164-173`
- [ ] **P02** [bug] The tablet navigation never auto-collapses, though the guidelines promise it — `web/src/lib/shell/Nav.svelte:228`
- [ ] **P03** [bug] On a phone, grids never become cards and mutating actions are not hidden — `web/src/lib/components/DataGrid.svelte`
- [ ] **P04** [bug] Host card grammar: 'Its 1 runner keep going and finish their jobs' — `web/src/lib/hosts/HostCard.svelte:115-116`
- [ ] **P05** [bug] Problem titles are lowercase fragments with 'job(s)' — `internal/controller/problems.go:186`
- [ ] **U02** [bug] The grid's table-level key handler hijacks Enter, Space and arrows for every control inside a row — `web/src/lib/components/DataGrid.svelte:350-353`

## Wave 3: risks

- [ ] **A08** [risk] `allowed_origins: ["*"]` switches CSRF protection off with no warning — `internal/api/auth.go:256`
- [ ] **A09** [risk] `PATCH /settings` returns 200 for changes the running process does not apply — `internal/api/handlers_admin.go:628`
- [ ] **A10** [risk] Runtime settings are written into the live config unsynchronised, and one is not a machine word — `internal/api/handlers_admin.go`
- [ ] **A11** [risk] Capacity-demand delivery runs synchronously inside the reconcile pass — `internal/controller/reconcile.go:72`
- [ ] **A12** [risk] A panicking loop stays dead while the process reports healthy — `internal/controller/controller.go:248-261`
- [ ] **A13** [risk] Database errors surface as 422s carrying raw error text, bypassing the 500 path and its logging — `internal/api/handlers_agents.go:37`
- [ ] **A14** [risk] Agent protocol decoding rejects unknown fields, so any additive change breaks mixed versions — `internal/api/handlers_agents.go`
- [ ] **A15** [risk] Demo seeding is guarded only by the absence of pools — `internal/controller/seed.go:60-75`
- [ ] **C08** [risk] Migration files share numeric prefixes — `internal/store/migrations/0005_pool_priority.sql`
- [ ] **C09** [risk] A self-transition `idle → idle` re-stamps `last_idle_at` and resets the idle timer — `internal/store/models.go:140-142`
- [ ] **C10** [risk] A stale webhook delivery still overwrites identity and linkage fields — `internal/store/queries_events.go:87-140`
- [ ] **C11** [risk] Phantom `queued` jobs live for ever and remain scheduler demand — `internal/store/queries_events.go:533`
- [ ] **C12** [risk] `StatsSince` scans the whole completed set on every reconcile pass — `internal/store/queries_events.go:479-486`
- [ ] **C13** [risk] Four dangerous values draw no validator finding — `internal/config/validate.go:187`
- [ ] **C14** [risk] Event replay has no gap signal and IDs restart at 1 after a controller restart — `internal/events/bus.go:221`
- [ ] **C22** [risk] `internal/cryptox` has no tests — `internal/cryptox`
- [ ] **E07** [risk] A redelivered `create_runner` task destroys a live runner and reuses a spent JIT config — `internal/agent/daemon.go`
- [ ] **E08** [risk] The process backend has no process group and the units set no `KillMode`, so an agent restart kills every job — `internal/backend/process.go:381-386`
- [ ] **E09** [risk] `extractTarGz` accepts absolute symlink targets — `internal/backend/process.go:883-890`
- [ ] **E10** [risk] Deployment-approval jobs are recorded as `queued` and drive scale-ups nobody can use — `internal/github/webhook.go:215-218`
- [ ] **E11** [risk] OIDC adopts an existing local account by username — `internal/auth/oidc.go:280-287`
- [ ] **E12** [risk] The fallback poller can spend the hourly API quota in the exact failure it exists for — `internal/github/app.go:484-544`
- [ ] **E13** [risk] `runner-entrypoint.sh` deregisters non-ephemeral runners with a one-hour token that has usually expired — `deploy/runner-entrypoint.sh:66-70`
- [ ] **E14** [risk] The runner image fetches the actions/runner tarball without a checksum and hides dependency failures — `deploy/Dockerfile.runner:72-76`
- [ ] **E15** [risk] The dind sidecar carries none of the pool's resource limits — `internal/backend/docker.go:459-491`
- [ ] **E16** [risk] Podman's `:z` relabel is applied to the host socket bind — `internal/backend/docker.go:394`
- [ ] **E17** [risk] `zoomies uninstall` deregisters every `zoomies-*` runner in the organisation — `internal/installer/uninstall.go:482`
- [ ] **E18** [risk] A native install on a host without systemd or launchd renders a compose file that starts from an empty database — `internal/installer/service.go:350-361`
- [ ] **E19** [risk] Containers may be created from the manifest digest, which classic Docker cannot resolve as an image — `internal/backend/dockerapi.go:541-546`
- [ ] **U04** [risk] Session expiry throws away the return path — `web/src/App.svelte:38-42`
- [ ] **U05** [risk] Frames that arrive during a reconcile are discarded, and `stats` can regress — `web/src/lib/state/fleet.svelte.ts:219-236`
- [ ] **U06** [risk] `liveKey={fleet.version}` refetches the Runners and Pools grids on every frame, and a sustained stream starves the fetch — `web/src/lib/components/DataGrid.svelte:164-208`
- [ ] **U07** [risk] Global shortcuts stay live under a modal, and two focus traps then fight — `web/src/lib/keys.ts:235-277`

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
- [ ] **E29** [polish] The CLI's own examples use the wrong ID prefix and an unbranded label list — `cmd/zoomies/pools.go:95`
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
  version bump workflow regenerates it.
