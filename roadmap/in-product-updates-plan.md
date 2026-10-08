# ZF-232: updating from the web UI — Implementation Plan

**Status**: proposed. The design is approved; this plan is waiting for the
owner's review and their choice of how to execute it. Nothing is built.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator update the controller and its hosts from the web UI —
off, a button, or automatically — without running `zoomies upgrade` by hand.

**Architecture:** Each host that opts in gets a root-owned systemd path unit
that, on a request file naming one validated release tag, runs the existing
`zoomies upgrade` unattended and writes a result. The controller records
attempts, writes its own request and queues an `update_agent` task for hosts,
and a pure planner (`internal/updates`) decides what `auto` does and says why.
Six stacked pull requests, each shippable alone because the mode defaults to
`off`.

**Tech Stack:** Go (standard library only), SQLite through `internal/store`,
systemd path units, Svelte 5 with the generated OpenAPI client, Playwright.

**Spec:** [in-product-updates.md](in-product-updates.md), and the decision it
rests on, [0011](decisions/0011-an-operator-may-let-zoomies-update-itself.md).
Read both before starting a task. This plan decides names and order; the spec
decides behaviour. Where this plan cites a line, it is the line at `cabdd1a`;
find the symbol, not the number.

## Global Constraints

* Go 1.27.1 or later, `CGO_ENABLED=0`, standard library only. This plan adds no
  dependency, so `docs/dependencies.md` does not change.
* Tests in `internal/{config,docs,controller,mcp,installer,agent,store,naming,updates}`
  do not import `internal/api`, so they run without `make build-nogui`. Only
  `internal/api`, `cmd/zoomies` and `test/*` need the embed stub; run
  `make build-nogui` once first. `make lint` and `make test` are green before every
  push. A subset: `make test TEST_PKGS="./internal/config/ ./internal/docs/"`.
* Only `internal/store` writes SQL. `internal/updates` stays pure — no clock
  read, database or network. Everything that touches a file, a command or the
  clock lives in `internal/updates/channel`, `internal/installer` or
  `internal/controller`.
* Settings live in the database and are read from the effective configuration,
  never from a `ZOOMIES_*` variable (`TestNoCodeReadsASettingFromTheEnvironment`).
  `ZOOMIES_BASE_URL` and `ZOOMIES_REPO` are not settings and may be read.
* `updates.mode` is `off | manual | auto`, default `off`. `updates.soak` is a
  duration, default `24h`, `0` allowed, applies to `auto` only. Both are
  platform-scoped and live. In mode `off` the controller makes the one
  `releases/latest` request it makes today, unchanged; the release list is
  fetched only when the mode is not `off`.
* Roles: `platform` changes the mode and updates the controller; `admin`
  updates hosts and checks for a release; `operator` and `viewer` read. With the
  mode `off` every action refuses with `update.mode_off`. Every action also
  refuses while `c.mayAct()` is false (the controller is fenced or has lost its
  lease).
* An eligible release has a tag matching `^v[0-9]+\.[0-9]+\.[0-9]+$`, is neither
  a prerelease nor a draft, and carries `checksums.txt` and the binary for the
  host's OS and architecture. `manual` takes the newest by version. `auto` takes
  it once it has been public for `updates.soak`, counted from `published_at`; a
  newer release restarts the wait.
* A host's target is `v` plus `version.Release(version.Version)` and only when
  that matches the tag pattern, never "latest" and never `version.InstallTag`
  (which answers `dev`). Every comparison of a version with a tag uses
  `version.CompareBuilds`, because release binaries report `1.3.5` without the `v`.
* The helper request carries `"v": 1`, is at most 4096 bytes and holds no URL,
  path, command or flag. The helper allows at most two attempts per tag and one
  per ten minutes, keeps its state in `/var/lib/zoomies-update/`, never runs
  `zoomies upgrade` with `--yes`, and runs it as a child process.
* The update folder is created only when the helper is installed. It is not in
  `config.SharedLayout`. On a native install its location is recorded in
  `update-helper.json` beside the `--config` file; in a container it is
  `<shared>/update`.
* An attempt times out after 90 minutes; the helper's unit allows 2 hours. After
  a failure the planner waits 30 minutes; after two failures of one tag it waits
  for an operator.
* One open attempt per target, enforced by a partial unique index. Attempt and
  rollout ids are `store.NewID` with the prefixes `upd` and `rol`. The host
  column has no foreign key.
* Every `*.updated` event is the resource's `GET` shape, published through the
  controller's helpers and never from a store row. The controller never dials an
  agent.
* Update problems never carry a `Remedy` (the autopilot applies every remedy).
  `controller.*` codes are platform-audience; `host.update_*` codes are
  fleet-audience and exempt from the public status page.
* API refusals use the `update.*` codes, which are added to the closed
  `ErrorCode` enum (`internal/api/errors.go`, `api/openapi.yaml`, the generated
  client), as `limit_reached` was.
* A new migration takes the next unused prefix (0082 today), alone, appended to
  `shippedMigrations`; a shipped migration is never edited or renamed.
* Every endpoint change updates `api/openapi.yaml`, regenerates
  `internal/api/openapi_spec.go` (`go run internal/api/gen_openapi.go`) and
  `web/src/lib/api/schema.d.ts` (`make openapi`), and touches
  `docs/api-surface.md`. Every new problem code, metric and command gets its row
  on the page `internal/docs` tests, in the same pull request as the code.
* Voice: British spelling; `--` in Go and TypeScript comments and `—` in
  Markdown; comments say why; error messages tell a person what to do; test
  names are sentences about the behaviour; commit messages are imperative
  sentences in plain prose with no prefix.
* UI: no raw colour and no repeated literal (tokens live in
  `web/src/lib/styles/tokens.css`), no new library, the app shell stays under
  200 KB gzipped, no "controller log" in `web/src`, and every new surface has a
  Playwright spec that runs on desktop and mobile with the accessibility pass.
* Every behavioural test is run once against the code with its rule removed and
  seen to fail (delivery rule 14), and the pull request says which were checked.

## Review Focus

The spec says what the software must do. It is silent on these, and none is an
obvious happy path, so each is pinned by a test in the task that owns the code.

1. **The update folder cannot be written when a button is pressed** — a
   container without the mount, a full disk, the wrong owner, the folder
   replaced by a file. Expect a refusal that names the folder and the fix, no
   attempt left open, and nothing half-written. Tasks 2.1 and 3.2.
2. **A `result.json` the controller did not ask for** — left from before a
   restart, for an attempt already closed, for none, or malformed or oversized.
   Expect it ignored: no crash, no attempt reopened or closed twice. A failed
   result for an open attempt closes it with the helper's sentence. Task 3.2.
3. **A host that goes away mid-rollout** — deleted, renamed, re-joined under a
   new id, or lost. Expect its open attempt to close as `cancelled`, the rollout
   not to wait on it, and a failure to halt the rollout rather than start the
   next host. Tasks 3.1, 4.3 and 5.3.
4. **Two actors at once** — two administrators, or the planner and a button.
   Expect exactly one open attempt per controller or host, `update.in_progress`
   for the second, and never a second request file. Tasks 3.1 and 3.2.
5. **The mode is switched to `off`, or the helper removed, while something is in
   flight.** Expect in-flight attempts to finish and be recorded, nothing new to
   start, a pending rollout to be cancelled, and the status to say why. Tasks
   3.2 and 5.3.

## Delivery mechanics

Each Part below is one pull request and is shippable alone. Part N is branched
from Part N−1 and opened as a draft against it, so a Part can be reviewed before
the one beneath it merges; Part 1 branches from `claude/practical-curie-g3wv84`,
the branch of the design pull request. Suggested branch names:
`claude/zf232-1-status`, `-2-helper`, `-3-controller`, `-4-hosts`, `-5-auto`,
`-6-drills`. The owner chooses at handoff between this and putting every Part's
commits on one branch; the first keeps each review small (delivery rule 8) and
needs permission to push branches beyond the designated one.

Each Part ends with its own `make lint` and the tests named in its tasks, and
updates the ZF-232 row in [progress.md](progress.md) (`in_progress` from Part 1,
`done` after Part 6).

## File structure

New packages and files, each with one job. Existing files are named in the task
that changes them.

| Path | Responsibility |
| --- | --- |
| `internal/updates/release.go`, `target.go` | `Release`, `Mode`, `ValidTag`, `TargetTag`, `Newest`, `Choose`: the pure eligibility rule and its sentences |
| `internal/updates/wire.go` | `Request`, `Result`, `ParseRequest`: the two JSON documents the service and the helper exchange |
| `internal/updates/plan.go` | `Snapshot`, `HostFacts`, `Action`, `Plan`, `Decide`: the pure planner (Part 5) |
| `internal/updates/channel/channel.go` | The service side of the update folder: `Locate`, `WriteRequest`, `ReadResult`, `ReadMarker` |
| `internal/installer/updatehelper_request.go` | The root side: the hostile reader, the rate-limit state, the result writer |
| `internal/installer/updatehelper.go` | `RunUpdateHelper`: read, validate, run the engine as a child, answer |
| `internal/installer/updatehelper_units.go` | Unit rendering, install and remove, the pointer and marker files |
| `internal/controller/updates_apply.go`, `updates_views.go`, `updates_loop.go`, `updates_hosts.go` | Requests and results, the status view, the loop, host updates |
| `internal/store/update_attempts.go`, `update_rollouts.go` | The two tables' queries |
| `internal/agent/update.go` | The agent's half: advertise, write the request, report the result |
| `internal/api/handlers_updates.go` | The `updates` routes |
| `cmd/zoomies/updates.go` | `zoomies updates ...` and the local `helper` group |
| `web/src/lib/updates/`, `web/src/lib/state/updates.svelte.ts`, `web/src/lib/settings/UpdatesPanel.svelte` | The UI's words, state and panel |

---

## Part 1 — The mode, the release list and a read-only status

Pull request 1. Exit: a mode is stored and shown and nothing acts; the soak edge
is a table test; in mode `off` the controller's one request is unchanged.

### Task 1.1: The two settings, their findings, and the MCP allowlist

**Files:**
- Modify: `internal/config/config.go` (`Updates`, `Default()`, `normalize()`), `internal/config/settings.go` (two rows after `updates.check_interval`), `internal/config/validate.go` (`validateUpdates`, called after the `updates.interval_too_fast` block), `internal/mcp/admin_tools.go` (`tunableSettings`), `web/src/lib/settings/settings.ts` (`SECTION_BLURB.updates`)
- Docs: `docs/configuration.md` (YAML block, table, prose), `docs/problem-codes.md` (config table), and the four sentences that say `update_settings` may write `updates.*`: `docs/cli.md`, `docs/connect-claude.md`, `docs/security.md`, `docs/configuration.md`
- Create: `internal/config/updates_test.go`. Modify: `internal/mcp/admin_tools_test.go`

**Interfaces:**
- Produces: `config.Updates{Mode string; CheckInterval, Soak time.Duration}`; registry keys `updates.mode`, `updates.soak`; findings `updates.mode` (error), `updates.soak_negative` (error), `updates.mode_without_check` (warning), `updates.auto` (info), `updates.auto_without_soak` (warning).

- [ ] **Step 1: Write the failing tests**, modelled on `internal/config/size_routing_test.go`:
  - `TestUpdatesDefaultToOffWithADaySoak`: `Default().Updates` is `Mode "off"`, `Soak 24*time.Hour`, `CheckInterval 24*time.Hour`.
  - `TestUpdatesModeFromYAMLIsLowerCasedAndTrimmed`: `mode: " Auto "` loads as `"auto"`.
  - `TestAnUnknownUpdatesModeIsAnErrorThatNamesTheChoices`: finding `updates.mode`, `SeverityError`, Fix names `off`, `manual` and `auto`.
  - `TestANegativeSoakFromYAMLIsAnError`: `soak: -1h` raises `updates.soak_negative`.
  - `TestAModeWithoutAReleaseCheckIsAWarning`: `manual` and `auto` with `CheckInterval 0` raise `updates.mode_without_check` at `SeverityWarning`; `off` with `0` raises nothing.
  - `TestAutoRaisesAnInfoFindingNamingTheSetting`: `Setting == "updates.mode"`, `SeverityInfo`; `manual` raises none.
  - `TestAutoWithNoSoakIsAWarning`: `auto` with `Soak 0` raises `updates.auto_without_soak`.
  - In `internal/mcp/admin_tools_test.go`, `TestUpdateSettingsCannotWriteTheUpdatesMode`: `tunable("updates.mode")` and `tunable("updates.soak")` are false; `tunable("updates.check_interval")` is true.
- [ ] **Step 2: Run** `go test ./internal/config/ ./internal/mcp/`. Expected: FAIL, unknown fields.
- [ ] **Step 3: Implement.** Add `Mode string \`yaml:"mode"\`` and `Soak time.Duration \`yaml:"soak"\`` to `config.Updates` and rewrite its comment, which still says the controller does not update itself. The default `Updates{Mode: "off", CheckInterval: 24*time.Hour, Soak: 24*time.Hour}` must be a non-empty valid choice or the registry's round-trip test fails. Registry rows: `updates.mode` with Label **"Release update mode"** (the refusal text reads "is not a release update mode"; "Update mode" would read "a update mode"), Env `ZOOMIES_UPDATE_MODE`, `KindEnum`, `ScopePlatform`, `Live`, Choices `off manual auto`, a Summary naming each; `updates.soak` with Label "Release update soak", Env `ZOOMIES_UPDATE_SOAK`, `KindDuration`, same scope, no `Floor`. `normalize()` lower-cases and trims `Mode`, as it does for `Kennel.Scope`. `validateUpdates` follows `validateSizeRouting`; info titles are lower-case with no full stop. In `tunableSettings` replace `"updates."` with `"updates.check_interval"` and say why in the comment above it.
- [ ] **Step 4: Docs.** `docs/configuration.md`: the YAML block, a table row beside `updates.check_interval` (an enum row spells each choice in backticks with "(default)"), and the prose. `docs/problem-codes.md`: five rows beside the existing `updates.*` ones. Reword the four sentences about `update_settings`. Set the blurb.
- [ ] **Step 5: Run** `go test ./internal/config/ ./internal/mcp/ ./internal/docs/ ./internal/installer/`. Expected: PASS; `TestEverySettingIsRegistered`, `TestEveryConfigurationKeyIsDocumented` and `TestEveryProblemCodeIsDocumented` cover the new rows. Mutation check: delete the `Soak == 0` branch of `validateUpdates`; `TestAutoWithNoSoakIsAWarning` must fail; restore it.
- [ ] **Step 6: Commit** "Add the update mode and soak settings, and keep an assistant from changing them".

### Task 1.2: The eligibility rule

**Files:**
- Create: `internal/updates/release.go`, `internal/updates/target.go`, `internal/updates/release_test.go`, `internal/updates/target_test.go`

**Interfaces:**
- Produces: `type Mode string` with `ModeOff`, `ModeManual`, `ModeAuto`; `type Release struct{Tag, URL string; PublishedAt time.Time; Prerelease, Draft bool; Assets []string}`; `func ValidTag(tag string) bool`; `func TargetTag(running string) (string, bool)`; `func AssetName(goos, goarch string) string` (empty unless `goos` is `linux` or `darwin`, the only systems `SelfUpdate` serves); `func (r Release) Complete(goos, goarch string) bool`; `func Newest(releases []Release, goos, goarch string) (Release, bool)`; `type ChooseInput struct{Mode Mode; Soak time.Duration; Now time.Time; Running string; Releases []Release; GOOS, GOARCH string}`; `type Target struct{Release Release; Newer bool; DueAt time.Time; Reason string}`; `func Choose(ChooseInput) Target`; `func (t Target) DueBy(now time.Time) bool`.

- [ ] **Step 1: Write the failing tests:**
  - `TestValidTagIsStrict`: accepts `v1.3.5` and `v10.0.12`; refuses `V1.3.5`, `1.3.5`, `v1.3`, `v1.3.5-rc1`, `"v1.3.5\n"`, `v1.3.5;x`, `v１.３.５` and `""`.
  - `TestTargetTagUsesTheRunningRelease`: `"1.3.5"` and `"v1.3.5"` give `("v1.3.5", true)`; `dev`, `main-sha-abc1234`, `v0.2-beta-5-gabc1234`, `v1.2`, `0.2-beta` and `""` give `false`.
  - `TestCompleteNeedsTheChecksumsAndTheHostsBinary`: `checksums.txt` plus `zoomies_linux_amd64` is complete for linux/amd64; dropping either, or having only `zoomies_linux_arm64`, is not; any release is incomplete for `windows`.
  - `TestNewestIgnoresIncompletePrereleaseDraftAndOddTags`: with `v1.3.4` complete, `v1.3.5` lacking `checksums.txt`, `V1.1.0` complete-looking, `v1.4.0-rc1` a prerelease and `v1.5.0` a draft, the answer is `v1.3.4`.
  - `TestNewestOrdersByVersionNotByPublishTime`: `v1.3.10` published before `v1.3.9` still wins.
  - `TestChooseOffersNothingWhenTheModeIsOff`: `Reason` starts "Updates are off".
  - `TestChooseSaysUpToDateWhenRunningTheNewest` and `TestChooseNeverOffersADowngrade` (running `1.4.0`, newest `v1.3.4`): `Newer` false.
  - `TestChooseRefusesABuildThatIsNotFromARelease`: running `main-sha-abc1234`; `Reason` contains "not from a release".
  - `TestChooseManualTakesTheNewestAtOnce`: `Newer`, and `DueBy(now)` true for a release published a minute ago.
  - `TestChooseAutoWaitsForTheSoak`: soak 24h, published 6h ago: `DueAt == PublishedAt.Add(24h)` and `Reason == "Waiting: v1.3.5 has been public for 6 hours and auto waits for 24; it can be taken in 18 hours."`.
  - `TestChooseAutoIsDueExactlyAtTheBoundary`: `DueBy(DueAt)` true, `DueBy(DueAt.Add(-time.Nanosecond))` false.
  - `TestANewerReleaseRestartsTheSoak`: `v1.3.1` at t0, `v1.3.2` at t0+4h26m, now t0+25h, soak 24h: target `v1.3.2`, `DueAt == t0+4h26m+24h`, `DueBy(now)` false; `v1.3.1` is never the target.
  - `TestChooseTreatsAFuturePublishedAtAsNotYetDue`: published an hour after now: `DueBy(now)` false and `Reason` contains no `-`.
- [ ] **Step 2: Run** `go test ./internal/updates/`. Expected: FAIL, package does not build.
- [ ] **Step 3: Implement.** `Newest` orders with `version.CompareBuilds` (the only ordering this repository trusts) and breaks ties by `PublishedAt`. `Choose` is `Newest`, then a comparison of `Running` with the target using `CompareBuilds` (only `SkewBehind` is `Newer`), then the mode. Durations in `Reason` round down to whole hours at an hour or more, else whole minutes, else "less than a minute". `ValidTag` is one anchored `regexp` compiled once (Go's `$` does not match before a trailing newline).
- [ ] **Step 4: Run** the same command. Expected: PASS. Mutation check: change the soak comparison to `After`; the boundary test must fail; restore.
- [ ] **Step 5: Commit** "Add the pure rule that picks which release an update mode would take".

### Task 1.3: The release list in the controller

**Files:**
- Create: `internal/controller/updates_errors.go`
- Modify: `internal/controller/updates.go`, `internal/controller/helpers_test.go` (`harnessTransport.RoundTrip`), `internal/controller/updates_test.go`

**Interfaces:**
- Consumes: `updates.Release`, `updates.Newest`.
- Produces: `releaseState` gains `Releases []updates.Release`; `const releaseListURL = "https://api.github.com/repos/eyupio/zoomies/releases?per_page=10"`; `func (c *Controller) checkForRelease(ctx context.Context) error` (was no result); `func (c *Controller) CheckForReleases(ctx context.Context) error`; `var ErrUpdateCheckDisabled`.

- [ ] **Step 1: Write the failing tests** in `updates_test.go`, using `roundTripFunc` and `withVersion`:
  - `TestAnOffModeAsksForTheLatestReleaseAndNeverTheList`: the transport records `latestReleaseURL` only.
  - `TestAManualModeAsksForTheReleaseList`: it records `releaseListURL` only.
  - `TestTheNoticeAndTheStatusNameTheSameRelease`: mode `manual`, a list with `v1.3.5` lacking `checksums.txt` and `v1.3.4` complete: `latestRelease().Tag == "v1.3.4"`, so `updateProblems` names `v1.3.4`.
  - `TestCheckForReleasesRefusesWhenTheCheckIsOff`: `CheckInterval 0` gives `ErrUpdateCheckDisabled` and no request.
  - `TestCheckForReleasesIsLimitedToOneRequestAMinute`: a second call after 59s makes no request and returns nil; after 61s it asks.
  - `TestAFailedListRequestKeepsTheLastAnswer`: a 403 returns an error and `latestRelease()` is unchanged.
  - `TestAListOfAnUnexpectedShapeIsIgnored`: `{}` records nothing and returns an error.
- [ ] **Step 2: Run** `go test ./internal/controller/ -run 'TestAnOff|TestAManual|TestTheNotice|TestCheckForReleases|TestAFailedList|TestAListOf'`. Expected: FAIL.
- [ ] **Step 3: Implement.** `checkForRelease` keeps today's request and state when `c.cfg().Updates.Mode` is `off` or the build is a development build, and otherwise decodes `releaseListURL` (`tag_name`, `html_url`, `published_at`, `prerelease`, `draft`, `assets[].name`) into `[]updates.Release`, stores the whole list and sets `Tag`/`URL` from `updates.Newest` for the controller's own `runtime.GOOS/GOARCH`. A failure keeps the previous state. `CheckForReleases` stamps its own time under `c.mu` (the housekeeping `last` is a local), returns `ErrUpdateCheckDisabled` when the interval is not positive, and returns without a request inside a minute. `background.go` keeps ignoring the error. Extend `harnessTransport.RoundTrip` to answer `releaseListURL` with `[]`, or every housekeeping test reaches api.github.com.
- [ ] **Step 4: Run** `go test ./internal/controller/`. Expected: PASS. Mutation check: make `off` use the list URL; `TestAnOffMode...` must fail; restore.
- [ ] **Step 5: Commit** "Read the release list when updating is on, and keep the single request when it is off".

### Task 1.4: The status view, its route and its event

**Files:**
- Create: `internal/controller/updates_views.go`, `internal/api/handlers_updates.go`, `internal/api/updates_test.go`
- Modify: `internal/controller/metrics.go` and `docs/metrics.md` (a scrape-time gauge `zoomies_update_available`, 1 while the target is newer than the running build, declared with a `descX`, listed in `Describe` and emitted by `gauge()` in `Collect`), `internal/events/bus.go` (`KindUpdates Kind = "updates.updated"`), `internal/controller/controller.go` (`lastUpdates []byte` beside `lastKennel`), `internal/controller/derived.go` (a block in `publishDerived` copied from the `kennel.summary` one), `internal/controller/derived_test.go` (`TestNothingIsComputedForNobody`), `internal/auth/rbac.go` and `rbac_test.go` (`ActionUpdatesRead` at viewer), `internal/api/router.go`, `internal/api/api_test.go` (`routeTable`), `api/openapi.yaml` (tag `updates`, path `/updates`, schema `UpdatesStatus`), `docs/api-surface.md` (route row, the SSE kinds paragraph and the "computed" bullet), `web/src/lib/api/types.ts` (`EventPayloads['updates.updated']`)
- Generate: `go run internal/api/gen_openapi.go`, then `make openapi`

**Interfaces:**
- Consumes: `updates.Choose`, `releaseState`.
- Produces: `type UpdatesView struct{Mode, Soak string; Running UpdatesRunning; Latest *UpdatesRelease; Target *UpdatesTarget; Reason string; CheckedAt *time.Time}` (`UpdatesRunning{Version string; Release bool}`, `UpdatesRelease{Tag, URL string; PublishedAt time.Time}`, `UpdatesTarget{Tag string; Newer bool; DueAt *time.Time}`; snake_case JSON, durations rendered as the neighbouring views render them); `func (c *Controller) UpdatesView(ctx context.Context) (*UpdatesView, error)`; `type updatesResponse = controller.UpdatesView`.

- [ ] **Step 1: Write the failing tests:**
  - `TestTheStatusSaysOffAndOffersNothingWhenTheModeIsOff` and `TestTheStatusNamesTheTargetAndWhenAutoWouldTakeIt` (controller, with a list and `withVersion(t, "1.3.0")`).
  - `TestTheMetricsSayWhetherANewerReleaseIsWaiting`: the gauge is 0 up to date and 1 with a newer target, and `TestEveryMetricIsDocumented` passes.
  - `TestUpdatesUpdatedIsSentOnlyWhenTheStatusChanges`: `h.listen`, `nextOfKind(events.KindUpdates)`, then `nothingFor` after an unchanged pass.
  - `TestNothingIsComputedForNobody` gains a case: with no subscriber the view is never rendered.
  - API: `TestAViewerCanReadTheUpdatesStatus` through `routeTable`; `TestResponsesMatchTheSpecShapes` and `TestEveryActionIsReachableThroughARoute` must keep passing with the new action.
- [ ] **Step 2: Run** `make build-nogui` once, then `go test ./internal/controller/ ./internal/api/ ./internal/auth/`. Expected: FAIL.
- [ ] **Step 3: Implement** the view as the single renderer that `publishDerived` and the handler share, with `Reason` copied from `updates.Choose`. Register the route under `s.require(auth.ActionUpdatesRead)`. Edit the spec, run both generators, and commit the three generated or edited files together.
- [ ] **Step 4: Run** the same command plus `go test ./internal/docs/`. Expected: PASS. Mutation check: publish unconditionally; the "only when it changes" test must fail; restore.
- [ ] **Step 5: Commit** "Report what an update would take on GET /updates and the event stream".

### Task 1.5: The read-only Settings → Updates panel

**Files:**
- Create: `web/src/lib/updates/words.ts`, `web/unit/updates-words.test.ts`, `web/src/lib/state/updates.svelte.ts`, `web/src/lib/settings/UpdatesPanel.svelte`, `web/tests/updates.spec.ts`, `web/tests/support/serve-updates.mjs`, `internal/controller/seed_updates.go`
- Modify: `web/src/lib/settings/pages.ts` (a Controller-group entry `{id: 'updates', needs: 'viewer'}`), `web/src/routes/Settings.svelte` (import, a branch **above** the final `<AboutPanel />` fallthrough, and the page-list copy), `web/src/lib/settings/SettingsRail.svelte` and `SettingsIndex.svelte` (their role copy), `web/src/lib/api/client.ts` (`getUpdates`), `web/src/lib/api/types.ts` (`UpdatesStatus`), `web/src/lib/problems/targets.ts` and its unit test (`controller.update_available` links to `/settings/updates`), `web/playwright.config.ts` (projects `updates` and `updates-mobile` on one new port), `a11y.spec.ts` (`PAGES`, `SETTINGS_OUTLINES`), `mobile.spec.ts`, `settings.spec.ts`, `docs/ui.md`

**Interfaces:**
- Consumes: `UpdatesStatus` from the generated client; the `updates.updated` event.
- Produces: `updates` state with `follow(): () => void`, `refresh(): Promise<void>`, `status`; words `describeMode(mode)` and `targetLine(status)`; the seed `ZOOMIES_SEED_UPDATES`.

- [ ] **Step 1: Write the failing tests.** Unit: `updates-words.test.ts` pins the sentence for each mode (`off`, `manual`, `auto`) and for a target with and without a due time. Playwright `updates.spec.ts` (desktop and Pixel 7): opens `/settings/updates` on the seeded server; sees running `1.3.0`, target `v1.3.2` and a "Waiting: …" or "available" sentence; sees the mode as text, not a control; has no Update button yet; passes the a11y pass; `expectNoSidewaysScroll` at 360px.
- [ ] **Step 2: Run** `cd web && npm run test:unit`. Expected: FAIL.
- [ ] **Step 3: Implement the seed.** `seedUpdates()` in `seed_updates.go`, called beside the existing seed hook (`grep -n seedStuck internal/controller`), is inert unless `ZOOMIES_SEED_UPDATES` is set (it is not a registry setting, so `TestNoCodeReadsASettingFromTheEnvironment` is unaffected). It installs a transport that serves a release list — `v1.3.1` published 30 hours ago, `v1.3.2` six hours ago, both complete for this platform — as `c.httpClient`, and sets `version.Version = "1.3.0"`, because the binary under test is `dev`, which no release comparison accepts. `serve-updates.mjs` is `serve.mjs` plus that variable and `ZOOMIES_UPDATE_MODE=manual`. The variable's value is the path of a temporary update folder, which Part 3 puts to use; the script creates it at a path derived from the port, so the spec can find it too.
- [ ] **Step 4: Implement the panel**, state and words with design tokens only; reuse `Panel`, `Badge`, `EmptyState`. Admins do not receive `updates.*` from `/settings`, so the panel reads mode and soak from `GET /api/v1/updates`. The state module follows `state/kennel.svelte.ts`: subscribe to `updates.updated` on mount and refetch when the stream returns.
- [ ] **Step 5: Run** `cd web && npm run lint && npm run test:unit`, then `make build VERSION=dev && cd web && npx playwright test tests/updates.spec.ts --project=updates --project=updates-mobile`. Expected: PASS. Check the shell budget: `npm run build` must not fail the size gate.
- [ ] **Step 6: Commit** "Show what an update would take on a Settings page, read-only".
- [ ] **Step 7:** Update the ZF-232 row in `roadmap/progress.md` to `in_progress` with what merged; commit "Record the first part of the update work".

---

## Part 2 — The helper

Pull request 2. Exit: a request file makes `zoomies upgrade` run against a fake
`systemctl`; every refusal has a test; nothing is created on a host that did not
ask for it.

### Task 2.1: The documents and the service's side of the folder

**Files:**
- Create: `internal/updates/wire.go`, `internal/updates/wire_test.go`, `internal/updates/channel/channel.go`, `internal/updates/channel/channel_test.go`

**Interfaces:**
- Produces: `const WireVersion = 1`, `MaxRequestBytes = 4096`, `MaxLogTailBytes = 16384`; `type Request struct{V int; ID, Tag, RequestedBy string; RequestedAt time.Time}` (JSON `v`, `id`, `tag`, `requested_by`, `requested_at`); `type Result struct{V int; ID string; OK bool; Tag, From, To, Error, LogTail string; StartedAt, FinishedAt time.Time}`; `func ParseRequest([]byte) (Request, error)`.
- `channel`: constants `RequestFile`, `ResultFile`, `MarkerFile`, `PointerFile = "update-helper.json"`; `type Marker struct{V int; Version, Binary string; InstalledAt time.Time}`; `type Pointer struct{V int; Dir, Binary, Account string; UID int}`; `type Locator struct{ConfigPath, SharedDir string; InContainer bool}`; `func Locate(Locator) (dir string, ok bool)`; `func WriteRequest(dir string, r updates.Request) error`; `func ReadResult(dir string) (updates.Result, bool, error)`; `func ReadMarker(dir string) (Marker, bool, error)`; `var ErrRequestPending`.

- [ ] **Step 1: Write the failing tests:**
  - `TestParseRequestAcceptsTheDocumentedShape`, `...RefusesAnUnknownField`, `...RefusesAWireVersionItDoesNotKnow`, `...RefusesABadTag` (the `ValidTag` table), `...RefusesABodyOverFourKilobytes` (4097 bytes), `...RefusesAnEmptyOrOverlongID` (0 and 65 characters).
  - `TestWriteRequestIsAtomicAndNeverCreatesTheFolder`: a missing folder is an error naming it and leaves nothing behind; with the folder present the file appears whole.
  - `TestWriteRequestRefusesWhileAnEarlierOneIsWaiting`: `errors.Is(err, ErrRequestPending)`.
  - `TestWriteRequestFailsCleanlyWhenTheFolderIsAFile` (Review Focus 1): an error, and no temporary file anywhere.
  - `TestReadResultIsAbsentNotAnError`, `...RefusesAnOversizedFile`, `...RefusesInvalidJSON`.
  - `TestLocatePrefersThePointerBesideTheConfigFile`, `TestLocateFallsBackToTheSharedFolderInAContainer` (`<SharedDir>/update`), `TestLocateFindsNothingOtherwise`.
- [ ] **Step 2: Run** `go test ./internal/updates/...`. Expected: FAIL.
- [ ] **Step 3: Implement.** `ParseRequest` uses `json.Decoder` with `DisallowUnknownFields`, checks `len` against `MaxRequestBytes` first, and requires `ValidTag`. `WriteRequest` writes a temporary file in the folder and `os.Rename`s it, mode 0640. The folder is never created here: it is created by the installer, with the account's ownership, or not at all.
- [ ] **Step 4: Run** the same command. Expected: PASS. Mutation check: drop the `DisallowUnknownFields` call; the unknown-field test must fail; restore.
- [ ] **Step 5: Commit** "Add the request and result documents and the service's side of the update folder".

### Task 2.2: Fetch, pre-flight and install as separate steps

**Files:**
- Modify: `internal/installer/selfupdate.go`, `internal/installer/selfupdate_test.go`, `cmd/zoomies/install.go` (`runUpgradeNamed`, `selfUpdate`), `cmd/zoomies/upgrade_flow_test.go`

**Interfaces:**
- Produces: `type Candidate struct{Tag, Path string}`; `func FetchRelease(ctx context.Context, opts SelfUpdateOptions) (*Candidate, SelfUpdateResult, error)` (nil `Candidate` when nothing is newer); `func (c *Candidate) Install(opts SelfUpdateOptions) error` (hard-links the old binary to `BinaryPath + ".previous"`, replacing any earlier one, then renames); `func (c *Candidate) Discard()`; `SelfUpdateOptions.Preflight func(ctx context.Context, candidate string) error`. `SelfUpdate` keeps its signature as fetch, optional pre-flight, install.

- [ ] **Step 1: Write the failing tests**, using `updateServer` and `updateFixture`:
  - `TestFetchLeavesTheInstalledBinaryUntouched`, `TestInstallKeepsThePreviousBinaryBesideIt` (the `.previous` file has the old bytes; a second install replaces it), `TestAFailedPreflightLeavesTheBinaryInPlace` (error wraps the pre-flight's; installed bytes unchanged; the temporary file is gone), `TestThePreflightRunsBetweenTheDownloadAndTheReplace` (a recorder shows the order), `TestThePreflightIsNotRunWhenNothingIsNewer`, and every existing `SelfUpdate` test still passes unchanged.
  - In `cmd/zoomies/upgrade_flow_test.go`, `TestTheCandidateIsAskedToCheckTheDeploymentBeforeItReplacesTheBinary`: the recorded argv is `<candidate> upgrade --check --non-interactive --no-download --installed-binary <installed>` with the deployment-selecting flags passed through, the environment has `ZOOMIES_NO_SELF_UPDATE=1`, and `--yes` never appears. `TestAnInvalidDeploymentStillStopsBeforeTheBinaryDownload` (existing) still sees zero requests.
- [ ] **Step 2: Run** `go test ./internal/installer/ -run SelfUpdate` and, after `make build-nogui`, `go test ./cmd/zoomies/ -run 'Upgrade|Candidate'`. Expected: FAIL.
- [ ] **Step 3: Implement** by splitting `SelfUpdate` at the temporary file: everything up to and including the `version --short` smoke test becomes `FetchRelease`; the rename becomes `Install`. In `runUpgradeNamed` the order becomes the existing in-process preview, then fetch, then the candidate pre-flight through the same `run` seam, then install, then `reexecBinary`. `--installed-binary` is required in the pre-flight, because without it `os.Executable()` is the temporary path and `prepareNative` refuses.
- [ ] **Step 4: Run** the same commands plus `go test ./internal/installer/`. Expected: PASS. Mutation check: run install before the pre-flight; the order test must fail.
- [ ] **Step 5: Commit** "Check the downloaded release against this deployment before it replaces the binary".

### Task 2.3: The root side reads a request as hostile input

**Files:**
- Create: `internal/installer/updatehelper_request.go`, `internal/installer/updatehelper_request_test.go`

**Interfaces:**
- Consumes: `updates.ParseRequest`, `updates.Result`.
- Produces: `type helperState struct{Seen []string; Attempts map[string][]time.Time}` with `loadHelperState(dir string)` and `save`; `func readRequest(dir *os.Root, wantUID int, ownerOf func(os.FileInfo) (int, bool)) (updates.Request, error)` (consumes the file: removes it once read); `func writeResult(dir *os.Root, r updates.Result) error`; `const helperMaxAttemptsPerTag = 2`, `helperMinInterval = 10 * time.Minute`, `helperSeenIDs = 64`.

- [ ] **Step 1: Write the failing tests** against `t.TempDir()` with an `ownerOf` seam, one case each:
  - `TestTheHelperRefusesARequestThatIsASymlink` (to a file outside and to one inside the folder), `...ADirectory`, `...AnOversizedRequest`, `...ARequestOwnedBySomeoneElse`, `...AnUnknownField`, `...ABadTag` (`v1.3`, `1.3.5`, `v1.3.5-rc1`, `"v1.3.5\n"`, `../v1.3.5`).
  - `TestTheHelperConsumesTheRequestItReads`: the file is gone afterwards, also when it was refused.
  - `TestTheHelperRefusesARepeatedRequestID`, `...ATagTriedTwiceAlready` (the third attempt), `...AnAttemptWithinTenMinutesOfTheLast`, and `TestTheHelperStateSurvivesARestart`.
  - `TestTheResultIsWrittenWithoutFollowingAPlantedLink`: `result.json` pre-planted as a symlink to another file; afterwards the link is replaced by a regular file and the target is unchanged.
  - `TestARefusedRequestIsAnsweredWithItsReason`.
- [ ] **Step 2: Run** `go test ./internal/installer/ -run TheHelper`. Expected: FAIL.
- [ ] **Step 3: Implement** with `os.OpenRoot`, one `Open`, `Lstat`-equivalent checks on the descriptor (`Stat` on the open file: regular, size, owner via `fileOwner`), a bounded read, then `Remove`. State lives in a directory the caller passes (`/var/lib/zoomies-update/` in production). `writeResult` creates a temporary name through the root handle and renames it over `result.json`.
- [ ] **Step 4: Run** the same command. Expected: PASS. Mutation check: skip the owner check; its test must fail; restore.
- [ ] **Step 5: Commit** "Read an update request as hostile input, and answer it without trusting the folder".

### Task 2.4: The helper's run, and the `updates` command group

**Files:**
- Create: `internal/installer/updatehelper.go`, `internal/installer/updatehelper_test.go`, `cmd/zoomies/updates.go`, `cmd/zoomies/updates_test.go`
- Modify: `cmd/zoomies/main.go` (one `commands()` entry `{"updates", groupFleet, "Release updates, and the helper that applies them", runUpdates}` after `hosts`), `docs/cli.md` (a `### zoomies updates` section whose text contains the literal ``zoomies updates``, which `TestEveryCommandIsDocumented` matches by prefix, and a sentence that `helper` is local and root-only)

**Interfaces:**
- Consumes: Tasks 2.1 and 2.3.
- Produces: `type HelperOptions struct{Dir, StateDir, BinaryPath, LockPath string; ServiceUID int; Now func() time.Time; Out io.Writer; ownerOf func(os.FileInfo) (int, bool); installedVersion func(ctx context.Context) (string, error); upgrade func(ctx context.Context, tag string) (output string, err error)}`; `func RunUpdateHelper(ctx context.Context, opts HelperOptions) error`; CLI `zoomies updates helper run` and `helper status`.

- [ ] **Step 1: Write the failing tests:**
  - `TestTheHelperRunsTheUpgradeForTheRequestedTagAsAChildProcess`: the `upgrade` seam gets the tag `v1.3.5`; the argv it builds is `upgrade --version v1.3.5 --non-interactive` and never contains `--yes`.
  - `TestTheHelperAnswersAnInstalledTagAsDoneWithoutRunningAnything` (installed `1.3.5` and `1.4.0` for tag `v1.3.5`: `OK` true, the seam never called).
  - `TestTheHelperRefusesABuildThatIsNotFromARelease` (installed `main-sha-abc1234`).
  - `TestTheHelperRefusesWhileTheUpgradeLockExists` (a file at `LockPath`; the seam never called; the result error names the file).
  - `TestAFailedUpgradeIsAnsweredWithTheEnginesLastSentence` and `TestTheLogTailIsBoundedAndValidUTF8` (20 KiB of output with a broken byte: at most 16384 bytes, valid).
  - CLI: `TestHelperStatusNamesTheFolderAndTheLastResult` and `TestHelperRunRefusesToRunAsAnUnprivilegedUser`.
- [ ] **Step 2: Run** `go test ./internal/installer/ -run Helper` and, after `make build-nogui`, `go test ./cmd/zoomies/ -run Updates`. Expected: FAIL.
- [ ] **Step 3: Implement** `RunUpdateHelper` as read, compare (`version.CompareBuilds` on the installed version, obtained by running the installed binary's `version --short`), lock check by `os.Stat` (never taken), run, answer. The child is `exec.CommandContext(<BinaryPath>, args...)` with the helper's own environment; it cannot be in-process, because the engine re-executes itself. A required addition's sentence comes back as the error; the tail of the output is the log tail.
- [ ] **Step 4: Run** the same commands. Expected: PASS. Mutation check: pass `--yes`; the argv test must fail.
- [ ] **Step 5: Commit** "Run the upgrade for a validated request and write down how it went".

### Task 2.5: The units, install and remove, and what the agent owns

**Files:**
- Create: `internal/installer/updatehelper_units.go`, `internal/installer/updatehelper_units_test.go`
- Modify: `internal/installer/uninstall.go` (the stop/disable loop at the unit list and the removal loop, `UninstallItems`, and removal of `<binary>.previous`), `internal/installer/uninstall_test.go`, `cmd/zoomies/updates.go` (`helper install` and `helper remove`), `docs/security.md` ("What the agent owns on a host": a table row, and the lead-in, which says the agent "only ever dials out" and must now say the helper is root-owned and is not the agent), `internal/docs/agent_owns_test.go`

**Interfaces:**
- Produces: `const UpdatePathUnit = "zoomies-update.path"`, `UpdateServiceUnit = "zoomies-update.service"`, `UpdateHelperStateDir = "/var/lib/zoomies-update"`; `func RenderUpdateUnits(binary, dir string) (pathUnit, serviceUnit string)`; `type InstallHelperOptions struct{Deployment Deployment; StateDir, ConfigDir, BinaryPath string; ServiceUID int; unitDir string; run commandRunner}`; `func InstallUpdateHelper(ctx context.Context, opts InstallHelperOptions) error`; `func RemoveUpdateHelper(ctx context.Context, opts InstallHelperOptions) error`.

- [ ] **Step 1: Write the failing tests** with `recordingRunner` and a `t.TempDir()` unit directory:
  - `TestTheRenderedUnitsWatchTheRealFolderAndCallTheRealBinary` (exact `PathExists=<dir>/request.json`, `ExecStart=<binary> updates helper run`, `TimeoutStartSec=2h`, `ProtectHome=read-only`, `%` doubled in paths).
  - `TestInstallingTheHelperWritesTheUnitsThePointerAndTheMarker`: on a native install the folder `update` under the state directory exists, owned by the account (seam), mode 0750; `update-helper.json` is beside the config file; `helper.json` is in the folder; the runner saw `daemon-reload` then `enable --now zoomies-update.path`.
  - `TestInstallingAgainChangesNothing` and `TestInstallRefusesToReplaceAUnitWithDifferentContents`.
  - `TestAContainerInstallUsesTheSharedFolderAndWritesNoPointer`, and `TestAControllerOnlyContainerIsRefusedWithTheReason` (no shared mount).
  - `TestRemovingTheHelperStopsDisablesAndDeletesItsUnitsAndFiles`; in `uninstall_test.go`, `TestUninstallRemovesTheHelperAndThePreviousBinary`, and the helper appears in `UninstallItems`.
  - `internal/docs/agent_owns_test.go` gains `{"`zoomies-update.path`", "the helper's trigger, present only where its owner installed it"}` and one for the request folder.
- [ ] **Step 2: Run** `go test ./internal/installer/ ./internal/docs/`. Expected: FAIL.
- [ ] **Step 3: Implement**, mirroring `installHostHealth`: `os.OpenRoot`, create the folder through it, `fileOwner` checks, `writeFileAtomic` for the units, refusal of a differing existing unit. The folder is created only here. On a native install the service account is the one the installed unit names (`ReadUnitIdentity`) and the state directory is the unit's; in a container the account is `ImageUID` and the folder is `SharedHostDir/update`. Do **not** add `update` to `config.SharedLayout`.
- [ ] **Step 4: Run** the same command, then `make build-nogui && go test ./cmd/zoomies/`. Expected: PASS. Mutation check: add `update` to `SharedLayout`; `shared_test.go`'s layout counts must fail; revert.
- [ ] **Step 5: Commit** "Add the update helper's units, and say on the security page that the host owner chose them".

### Task 2.6: Offer the helper when a host is upgraded

**Files:**
- Modify: `internal/installer/upgrade.go` (`UpgradeOptions`, a step after `settleLayout`), `internal/installer/layout.go` (nothing for the question itself; the container mount, only if the shared folder is absent from a runner-hosting container), `cmd/zoomies/install.go` (`--update-helper`), `internal/installer/upgrade_test.go` or a new `updatehelper_offer_test.go`, `docs/upgrading.md` (the upgrade's questions)

**Interfaces:**
- Produces: `UpgradeOptions.UpdateHelper bool` (the `--update-helper` flag). The question reuses `askApproval`'s reader (`In`, `Interactive`) with a `[y/N]` default; there is no new callback.

- [ ] **Step 1: Write the failing tests**, copying the table in `TestAnUpgradeAddsTheSharedMountOnlyWithApproval`:
  - `TestAnUpgradeAsksAboutTheHelperAsItsOwnQuestionThatDefaultsToNo`: Enter, EOF and `n` leave it uninstalled; `y` installs; `AssumeYes` alone does not install it; `UpdateHelper: true` installs without asking; unattended (`NonInteractive`) skips and prints `Add later: sudo zoomies updates helper install`.
  - `TestTheHelperIsOfferedOnlyOnASystemdHost` (no `/run/systemd/system`: no question).
  - `TestTheHelperIsNotOfferedAgainOnceInstalled`.
  - `TestAUpgradeWithTheHelperInstalledStillUpgradesWhenTheQuestionIsDeclined`.
- [ ] **Step 2: Run** `go test ./internal/installer/ -run Helper`. Expected: FAIL.
- [ ] **Step 3: Implement** the question after the layout review and before the pull and restart, gated on `p.unit` or `p.nativeUnits` for native and on the container record otherwise (a native install writes no deployment record, so `p.record.Mode` is empty there). `--yes` approves the batch of layout additions and settings moves; it must not approve this.
- [ ] **Step 4: Run** `go test ./internal/installer/`. Expected: PASS. Mutation check: let `AssumeYes` approve; the "alone does not install" case must fail.
- [ ] **Step 5: Commit** "Ask, as a question of its own, whether an upgraded host may be updated from the web".

### Task 2.7: Offer the helper at install and at join

**Files:**
- Modify: `internal/installer/installer.go` (`Options`, a step beside `stepCLIConfig`, a prompt in the pattern `create := true; if i.interactive { i.confirm(...) }` defaulting to false), `internal/installer/container.go` (after `up`), `internal/installer/answers.go` (`Answers.UpdateHelper *bool`, `exampleAnswers`), `internal/installer/join.go` (`JoinOptions.UpdateHelper`, a `[y/N]` prompt like `confirmRejoin`), `cmd/zoomies/install.go` and `cmd/zoomies/agent.go` (`--update-helper`), their tests, `TestWriteExampleRoundTrips`

**Interfaces:**
- Consumes: `InstallUpdateHelper` from Task 2.5.

- [ ] **Step 1: Write the failing tests:** `TestInitDoesNotInstallTheHelperUnlessAsked` (prompt default no; `--update-helper` and the answers key `update_helper: true` install it; `AssumeYes` does not); `TestAgentJoinOffersTheHelperAndDefaultsToNo`; `TestTheAnswersFileAcceptsTheUpdateHelperKey` (strict `KnownFields` still refuses a typo); `TestWriteExampleRoundTrips` passes with the new key documented.
- [ ] **Step 2: Run** `go test ./internal/installer/`. Expected: FAIL.
- [ ] **Step 3: Implement.** `init` on a finished install runs the upgrade and never reaches its own prompt, so no change there. `agent join`'s `--yes` means only "re-join"; do not reuse it.
- [ ] **Step 4: Run** `go test ./internal/installer/` and, after `make build-nogui`, `go test ./cmd/zoomies/`. Expected: PASS.
- [ ] **Step 5: Commit** "Offer the update helper when a host is installed or joined, never by default".
- [ ] **Step 6:** Update the ZF-232 row in `roadmap/progress.md`; commit "Record the helper".

## Part 3 — Updating the controller from a button

Pull request 3. Exit: an attempt is recorded and closed across a restart; a
failure problem carries the helper's sentence; only `platform` can press the
button.

### Task 3.1: The attempts table

**Files:**
- Create: `internal/store/migrations/0082_update_attempts.sql` (use the next unused prefix at the time), `internal/store/update_attempts.go`, `internal/store/update_attempts_test.go`
- Modify: `internal/store/migrations_test.go` (append to `shippedMigrations`), `internal/store/ids.go` (`PrefixUpdateAttempt = "upd"`), `internal/store/queries_fleet.go` (`DeleteHostForgettingMachine` closes the host's open attempts inside its transaction), `internal/config/config.go`, `settings.go`, `validate.go` (key `retention.update_attempts`, 90 days, with `Floor: 24*time.Hour` in the registry), `docs/configuration.md` (two places), `internal/controller/background.go` (a row in the prune job table)

**Interfaces:**
- Produces: `type UpdateAttempt struct{ID, Scope, HostID, FromVersion, ToVersion, Trigger, RequestedBy, State, Error string; RequestedAt time.Time; FinishedAt *time.Time}`; constants `UpdateScopeController`, `UpdateScopeHost`, `UpdateTriggerManual`, `UpdateTriggerAuto`, `UpdateRequested`, `UpdateSucceeded`, `UpdateFailed`, `UpdateTimedOut`, `UpdateCancelled`; `func (s *Store) CreateUpdateAttempt(ctx context.Context, a *UpdateAttempt) error` (sets `ID` and `RequestedAt`; `ErrConflict` when one is open for the target); `func (s *Store) FinishUpdateAttempt(ctx context.Context, id, state, errText string) (bool, error)` (only from `requested`; false when it was not); `func (s *Store) OpenUpdateAttempts(ctx context.Context) ([]UpdateAttempt, error)`; `func (s *Store) ListUpdateAttempts(ctx context.Context, scope, hostID string, limit int) ([]UpdateAttempt, error)`; `func (s *Store) PruneUpdateAttempts(ctx context.Context, before time.Time) (int, error)`.

- [ ] **Step 1: Write the failing tests** with `newTestStoreAt(t, clock)`:
  - `TestOnlyOneUpdateAttemptIsOpenPerTarget`: a second controller attempt gives `ErrConflict`; two different hosts may each have one; after finishing, a new one is allowed (Review Focus 4).
  - `TestFinishingAnAttemptIsMonotonic`: finishing twice returns `true` then `false`, and the state is the first's.
  - `TestDeletingAHostClosesItsOpenAttemptsAsCancelled` (Review Focus 3).
  - `TestAReJoinKeepsAHostsAttemptHistory`: `DeleteHost` then `CreateHost` under the same id leaves finished rows in place, because the column has no foreign key.
  - `TestPruneSparesOpenAttempts`; `TestTheRetentionKeyHasAFloor`.
- [ ] **Step 2: Run** `go test ./internal/store/ ./internal/config/`. Expected: FAIL.
- [ ] **Step 3: Implement.** The migration header says why: a table of its own, why `host_id` has no foreign key, and why a partial unique index (`WHERE state = 'requested'`, over `(scope, host_id)`) carries the "one open attempt" rule — the model is `0074_size_classes.sql`. Columns are `from_version` and `to_version` because `from` is an SQL keyword. Ids are `NewID(PrefixUpdateAttempt)`; times are Unix milliseconds through the store's helpers, never `time.Now()`. Create inside `s.tx`; `wrapWrite` turns the unique violation into `ErrConflict`.
- [ ] **Step 4: Run** the same command and `go test ./internal/controller/ -run Prune`. Expected: PASS. Mutation check: drop the partial index; the open-per-target test must fail.
- [ ] **Step 5: Commit** "Record each update attempt, one open at a time, without tying it to the host row".

### Task 3.2: Request, watch and close

**Files:**
- Create: `internal/controller/updates_apply.go`, `internal/controller/updates_apply_test.go`, `internal/controller/updates_loop.go`
- Modify: `internal/controller/updates_errors.go` (the sentinels), `internal/controller/updates_views.go` (the helper and attempt blocks), `internal/controller/controller.go` (`Options.UpdateDir string`; `c.spawn("updates", loopCtx, c.updatesLoop)`; `UpdateConfig` calls `KickUpdates` when `before.Updates != after.Updates`), `internal/controller/derived.go`, `internal/auth/rbac.go` (`ActionUpdatesCheck` admin, `ActionUpdatesApply` platform) and its tests, `internal/controller/metrics.go` and `docs/metrics.md`

**Interfaces:**
- Consumes: Tasks 1.2, 1.3, 2.1, 3.1.
- Produces: `type UpdateActor struct{ID, Name string}`; the sentinels `ErrUpdateModeOff`, `ErrUpdateHelperMissing`, `ErrUpdateInProgress`, `ErrUpdateNotARelease`, `ErrUpdateNothingNewer`, `ErrUpdateHostCannotUpdate` and `ErrUpdateRolloutHalted` — all declared here beside Task 1.3's `ErrUpdateCheckDisabled`, so Task 3.4's mapper covers all eight codes, and Parts 4 and 5 only use them; `func (c *Controller) RequestControllerUpdate(ctx context.Context, by UpdateActor, tag string) (*UpdatesView, error)` (empty `tag` takes the newest complete release); `func (c *Controller) KickUpdates()`; the view gains `Helper{State, Reason, InstallCommand string}` and `Controller *UpdatesAttempt{ID, State, From, To, Trigger string; RequestedAt time.Time; FinishedAt *time.Time; Error string}`; counter `zoomies_update_attempts_total{kind,result}`.

- [ ] **Step 1: Write the failing tests** with the controller `newHarness`, `h.advance`, `h.restart`, an `Options.UpdateDir` temp folder holding a marker, and `withVersion`:
  - `TestRequestingAControllerUpdateRecordsTheAttemptAndWritesTheRequest`: one open attempt (`from` the running version, `to` the tag), `request.json` parses to the same id and tag, an audit row names the actor, and a frame is published.
  - Refusals, each with its sentinel: `...WhenTheModeIsOff`, `...WithoutAHelper`, `...OnABuildThatIsNotARelease`, `...WhenNothingIsNewer`, `...WhileFenced` (`mayAct` false).
  - `TestASecondRequestWhileOneIsOpenWritesNoSecondFile` (Review Focus 4): `ErrUpdateInProgress`, and the folder holds the first request only.
  - `TestAnUnwritableFolderRefusesAndLeavesNoAttempt` (Review Focus 1): the folder replaced by a regular file; the error names the folder; `OpenUpdateAttempts` is empty.
  - `TestAStaleOrUnknownResultIsIgnored` (Review Focus 2), table: the id of a closed attempt, an unknown id, malformed JSON, an oversized file — no state change, no panic.
  - `TestAFailedResultClosesTheOpenAttemptWithTheHelpersSentence`.
  - `TestTheNewProcessClosesTheAttemptAsSucceededWhenItRunsTheTarget`: `withVersion(t, "1.3.5")` after `h.restart()` with an open attempt to `v1.3.5`; and `...EvenWithoutAResultFile`.
  - `TestAnAttemptTimesOutAfterNinetyMinutes`: `h.advance(89*time.Minute)` leaves it open, `91*time.Minute` closes it as `timed_out`.
  - `TestSwitchingTheModeOffLetsAnInFlightAttemptFinish` (Review Focus 5): after the switch a result still closes it and no new request is accepted.
- [ ] **Step 2: Run** `go test ./internal/controller/ -run 'Update|Attempt'`. Expected: FAIL.
- [ ] **Step 3: Implement.** `c.updateDir()` is `Options.UpdateDir` when set, else `channel.Locate(channel.Locator{ConfigPath: c.cfg().Path(), SharedDir: config.SharedDir(), InContainer: backend.InContainer()})`. The loop copies the shape of `autoPoolLoop`: a cap-1 kick channel, its own pass mutex, never `reconcileMu`, started always and doing nothing while the mode is `off`; every action starts with `c.mayAct()`. A pass reads open attempts, reads `result.json` from the folder (`channel.ReadResult`), and closes attempts: a result whose id matches an open attempt closes it (`ok` and the running version at or past the tag → `succeeded`, else `failed` with `Error`); an open attempt older than 90 minutes → `timed_out`. Success is decided with `version.CompareBuilds`, never equality. `RequestControllerUpdate` order: gate (`mayAct`, mode, build is a release, helper marker present, newer release), `CreateUpdateAttempt` (this is the single-flight), `WriteRequest`; if the write fails, `FinishUpdateAttempt(... failed ...)` before returning the error, so no attempt is left open. Audit with `s.auth.Auditor().Act(ctx, identity, "update.controller", "controller", id, detail)` through `c.systemAudit` or the controller's auditor, keeping paths out of the payload.
- [ ] **Step 4: Run** `go test ./internal/controller/ ./internal/auth/ ./internal/docs/`. Expected: PASS. Mutation checks: remove the `mayAct` gate (the fenced test fails); remove the unique-conflict mapping (the second-request test fails).
- [ ] **Step 5: Commit** "Let the controller ask for its own update, and close each attempt however it ends".

### Task 3.3: The problems

**Files:**
- Modify: `internal/controller/problems.go` (extend `updateProblems`, `problemAudience`), `internal/controller/problems_test.go`, `docs/problem-codes.md` (runtime table), `docs/privacy.md` (the sentence that says nothing is downloaded), `docs/upgrading.md` (the sentence that says a controller upgrade does not remotely replace binaries, now true only without the helper)

**Interfaces:**
- Produces: codes `controller.update_helper_missing` (warning, platform) and `controller.update_failed` (error, platform); `controller.update_available`'s fix names Settings → Updates when the mode is not `off` and the helper is ready.

- [ ] **Step 1: Write the failing tests:** `TestAFailedControllerUpdateRaisesAProblemWithTheHelpersSentence` (and it is cleared by a later success, and by a new attempt opening); `TestAMissingHelperRaisesAPlatformProblemOnlyWhenTheModeIsNotOff`; `TestUpdateProblemsCarryNoRemedy`; `TestAnUpdateProblemIsNotShownToTheFleetAudience`; `problems_audience_test.go` already requires an audience row for each new code.
- [ ] **Step 2: Run** `go test ./internal/controller/ -run 'Problem|Audience'`. Expected: FAIL.
- [ ] **Step 3: Implement.** `updateProblems()` takes no context and does no I/O; it reads the loop's in-memory status. Text from a helper is rendered as prose, never with backticks. Add the rows to `problem-codes.md` in this same pull request.
- [ ] **Step 4: Run** `go test ./internal/controller/ ./internal/docs/`. Expected: PASS. Mutation check: set a `Remedy`; the no-remedy test must fail.
- [ ] **Step 5: Commit** "Raise a problem when a controller update fails or its helper is missing".

### Task 3.4: The check and controller routes

**Files:**
- Modify: `internal/api/handlers_updates.go` (`handleCheckUpdates`, `handleUpdateController`, a `failUpdate(w, r, err)` mapper), `internal/api/router.go`, `internal/api/errors.go` (eight constants), `api/openapi.yaml` (paths, schemas, the `ErrorCode` enum with the eight values, `x-zoomies-role: platform` on the controller route, and a line in the spec's roles section saying the `platform` role exists), `internal/api/sse.go` (a per-subscriber filter `updatesFor`), `internal/controller/updates_views.go` (`func (v UpdatesView) For(platform bool) UpdatesView` blanking `Controller.Error`), `internal/api/api_test.go` (`routeTable`), `internal/api/updates_test.go`, `docs/api-surface.md`, `web/src/lib/api/client.ts`
- Generate: both generators

**Interfaces:**
- Produces: `POST /api/v1/updates/check` (admin, 200 with the status) and `POST /api/v1/updates/controller` (platform, body `{"tag"?: string}`, 202 with the status); codes `update.mode_off`, `update.check_disabled`, `update.helper_missing`, `update.in_progress`, `update.not_a_release`, `update.nothing_newer`, `update.host_cannot_update`, `update.rollout_halted` (all 409).

- [ ] **Step 1: Write the failing tests:** the role matrix for each route through `routeTable` (the controller route: platform 202, admin 403, operator 403, viewer 403; check: admin 200, operator 403); `TestEachUpdateRefusalCarriesItsStableCode` (table of sentinel → 409 and code); `TestAnUnknownFieldInTheBodyIsRefused` (422); `TestTheControllerRouteRecordsAnAuditRowForTheCaller`; `TestTheStatusWithholdsTheHelpersTextBelowPlatform` for `GET /updates` and for the stream (`updatesFor`), so a frame never carries it to an administrator; `TestEveryOperationRecordsItsRole` and `TestTheSpecCoversTheRouter` keep passing.
- [ ] **Step 2: Run** `make build-nogui && go test ./internal/api/`. Expected: FAIL.
- [ ] **Step 3: Implement** modelled on `handleTakeBackup` and `handleAcceptHostCheck`: `decodeOptional`, the controller call, the mapper, `Act`, `writeJSON(w, http.StatusAccepted, view)` after the audit row. Add the eight codes to the closed enum in the same three places `limit_reached` occupies; fix the conventions list in `api-surface.md`, which names eight of the ten existing codes.
- [ ] **Step 4: Run** the same command, then `go test ./internal/docs/`. Expected: PASS.
- [ ] **Step 5: Commit** "Add the routes that check for a release and update the controller".

### Task 3.5: The CLI

**Files:**
- Modify: `cmd/zoomies/updates.go` (`status`, `check`, `apply`), `cmd/zoomies/types.go` (partial structs), `docs/cli.md`, `cmd/zoomies/updates_test.go`

- [ ] **Step 1: Write the failing tests** with `jsonRoutes` and `runCLI`: `status` prints a table and `-o json` emits the raw body; `check` posts and prints the new sentence; `apply` posts `{"tag": "..."}` when `--version` is given and nothing otherwise, refuses without `--yes` when not on a terminal, and exits non-zero on a 409 with the server's message; text the helper wrote goes through `plain()` (add a hostile-output case beside `hostile_output_test.go`).
- [ ] **Step 2: Run** `go test ./cmd/zoomies/ -run Updates`. Expected: FAIL.
- [ ] **Step 3: Implement** with `registerClientFlags(fs, false)` for the acting commands and `true` for `status` and `check`, as `hostsList` and `hostsDrain` do.
- [ ] **Step 4: Run** `go test ./cmd/zoomies/ ./internal/docs/`. Expected: PASS.
- [ ] **Step 5: Commit** "Add zoomies updates status, check and apply".

### Task 3.6: The button, the confirmation and the restart

**Files:**
- Modify: `web/src/lib/settings/UpdatesPanel.svelte` (the Update button, `platform` only), `web/src/lib/settings/RestartWait.svelte` (generalise: props for where to return to and what proves the restart; it is hard-wired to `/settings/backups` and limited to 45 s and 120 s), `web/src/lib/updates/words.ts`, `web/src/lib/api/client.ts` (done in 3.4), `web/tests/updates.spec.ts`, the seed (`seed_updates.go` points the controller at the temporary update folder named by `ZOOMIES_SEED_UPDATES` and writes a `helper.json` marker into it; the spec reads and writes the same folder)
- Create: `web/src/lib/updates/UpdateControllerDialog.svelte`

- [ ] **Step 1: Write the failing tests.** Unit: `words.ts` for each attempt state. Playwright: platform sees the button and a confirmation that names the version, that the controller restarts, that running jobs continue and that a pre-migration copy is kept; an administrator sees no button and the reason; pressing it posts and shows the in-flight state; the spec then writes a failed `result.json` into the seeded folder and the panel shows the helper's sentence; mobile and the accessibility pass.
- [ ] **Step 2: Run** `cd web && npm run test:unit`. Expected: FAIL.
- [ ] **Step 3: Implement.** The confirmation is a `ConfirmDialog` with `tone="default"` and `consequences`. After the post the panel watches the stream state and the build, not `/healthz`, because a fast restart can fall between two polls; success is the attempt closing or the running version changing. A pre-flight failure arrives with the stream still live. The `upgrade.svelte.ts` module reloads a visible tab only on the next navigation; the panel does not depend on it.
- [ ] **Step 4: Run** `cd web && npm run lint && npm run test:unit`, then `make build VERSION=dev && cd web && npx playwright test tests/updates.spec.ts --project=updates --project=updates-mobile`. Expected: PASS.
- [ ] **Step 5: Commit** "Add the Update button for the controller, with a confirmation and a restart state".
- [ ] **Step 6:** Update the ZF-232 row; commit "Record the controller update".

---

## Part 4 — Updating a host

Pull request 4. Exit: a host moves to the controller's release with a job running
on it, and the job survives; a failed or lost task never fails a runner.

### Task 4.1: The agent's wire additions

**Files:**
- Modify: `internal/agent/protocol.go`, `internal/agent/daemon.go` (`validateTask`), `internal/agent/protocol_test.go`, `api/openapi.yaml` (`AgentTaskKind`, `AgentTask`, `AgentHeartbeatRequest`), generated files

**Interfaces:**
- Produces: `const FeatureSelfUpdate = "self-update"`; `const TaskUpdateAgent TaskKind = "update_agent"`; `Task.UpdateID string \`json:"update_id,omitempty"\`` and `Task.UpdateTag string \`json:"update_tag,omitempty"\``; `type UpdateReport struct{ID string; OK bool; Tag, From, To, Error string; FinishedAt time.Time}`; `HeartbeatRequest.Update *UpdateReport \`json:"update,omitempty"\``. `ProtocolVersion` stays 1: a bump would mark every older host incompatible.

- [ ] **Step 1: Write the failing tests:** `TestValidateTaskAcceptsAnUpdateTaskWithAnIDAndAValidTag`; `...RefusesABadTag` and `...RefusesNoID`; `TestTheNewFieldsAreOmittedFromOlderShapes` (a `Task` without them encodes exactly as before; a heartbeat without `Update` encodes without the key).
- [ ] **Step 2: Run** `go test ./internal/agent/ -run 'Update|Validate'`. Expected: FAIL.
- [ ] **Step 3: Implement** the additions; give `validateTask` a case that checks `UpdateID` is non-empty and `updates.ValidTag(UpdateTag)`. Edit the three OpenAPI schemas and regenerate.
- [ ] **Step 4: Run** `make build-nogui && go test ./internal/agent/ ./internal/api/`. Expected: PASS.
- [ ] **Step 5: Commit** "Add the update task and the result an agent reports on its heartbeat".

### Task 4.2: The agent writes the request and reports the result

**Files:**
- Create: `internal/agent/update.go`, `internal/agent/update_test.go`
- Modify: `internal/agent/daemon.go` (`Options.UpdateDir func() (string, bool)`; a dispatch case beside `TaskFillToolCache`, before the runner claim; the heartbeat literal), `internal/agent/memoryvalve.go` (`features()`), `cmd/zoomies/agent.go` (wire `Options.UpdateDir` from `channel.Locate`)

**Interfaces:**
- Consumes: Tasks 2.1 and 4.1.
- Produces: `func (a *Agent) handleUpdate(ctx context.Context, task Task)`; `func (a *Agent) updateReport() *UpdateReport`; `func (a *Agent) markUpdateDelivered(id string)`.

- [ ] **Step 1: Write the failing tests** with `newHarness(t, capacity)` and the fake transport:
  - `TestAnAgentAdvertisesSelfUpdateOnlyWhileItsHelperIsReady`: a marker in the folder puts `self-update` in the next heartbeat's features; removing it takes it out; the embedded agent never advertises it.
  - `TestAnUpdateTaskWritesTheRequestAndReportsItWritten`: `request.json` has the task's id and tag; the result is `OK`.
  - `TestAnUpdateTaskForADowngradeWritesNothing`: the agent runs `1.4.0`, tag `v1.3.5`; reported `OK` as already done, no file.
  - `TestAnUpdateTaskIsRefusedWhileMutationsArePaused` and `TestAnUpdateTaskReportsAFailureWhenTheFolderCannotBeWritten` (Review Focus 1).
  - `TestTheHeartbeatCarriesTheHelpersResultUntilOneSucceeds`: the first beat fails, so the second carries the report again; the third does not.
  - `TestARestartedAgentSendsTheResultOnce`: a new `Agent` over the same folder reports the undelivered `result.json` on its first beat.
  - `TestAnUpdateTaskNeverReportsRunnerFailure`: the result's `RunnerID` is empty and nothing calls `reportFailure`.
- [ ] **Step 2: Run** `go test ./internal/agent/ -run Update`. Expected: FAIL.
- [ ] **Step 3: Implement.** Without a dispatch case the task reaches `runTask`, matches nothing and is never reported, so add the case beside `TaskFillToolCache` and answer through a reporter of its own, as `handleToolFill` does. Do not apply `refuseNewWork`: an incompatible host is exactly the one that needs the update. Do honour `a.mutationsPaused`. `updateReport()` reads `channel.ReadResult` and returns a report only if its id has not been marked delivered in this process and it finished within the last hour; truncate `Error` with `truncateRunes`.
- [ ] **Step 4: Run** `go test ./internal/agent/`. Expected: PASS. Mutation check: drop the dispatch case; the task test must fail.
- [ ] **Step 5: Commit** "Let an agent write an update request and report how the update went".

### Task 4.3: The controller updates a host

**Files:**
- Create: `internal/controller/updates_hosts.go`, `internal/controller/updates_hosts_test.go`
- Modify: `internal/controller/agents.go` (`updateLease` and `requeueAfter`; a branch in `ReportResult` before `if res.RunnerID == ""`; the heartbeat hook after the `if changed {…}` block), `internal/controller/views.go` (`HostView.Update *HostUpdateView`), `internal/controller/problems.go`, `internal/controller/status.go` (`statusExempt`), `docs/problem-codes.md` (rows, and the exceptions paragraph beside the table), `internal/controller/invariants_test.go` (the lease table), `internal/controller/compatibility_test.go`, `api/openapi.yaml` (`Host.update`), generated files

**Interfaces:**
- Produces: `func hostCanSelfUpdate(h *store.Host, target string) (can bool, why string)`, the single place that says whether a host can update itself and, if not, why in a sentence — used by the view, the route and, in Part 5, the planner's snapshot; `func (c *Controller) RequestHostUpdate(ctx context.Context, by UpdateActor, hostID string) (*HostView, error)` (`ErrUpdateHostCannotUpdate` when the host lacks `self-update`, is embedded, is ahead, or has no target); `type HostUpdateView struct{State, Reason string; CanUpdate bool; AttemptID string}`; codes `host.update_failed` (error) and `host.update_unavailable` (info), both fleet-audience and in `statusExempt`.

- [ ] **Step 1: Write the failing tests:**
  - `TestAHostBehindTheControllerGetsAnUpdateTask`, `...OnlyWhenItAdvertisesSelfUpdate`, `...NeverWhenEmbedded`, `...NeverWhenAheadOfTheController`, `...NotWhileFenced`.
  - `TestAHostCannotBeUpdatedWhenTheControllerIsNotARelease`: running `main-sha-abc1234` gives `ErrUpdateNotARelease`, the host's reason says why, and its `upgrade_command` is unchanged.
  - `TestAnUpdateTaskIsNotLifecycleWork`: `lifecycleTask(agent.TaskUpdateAgent)` is false; a failed update result never fails a runner.
  - `TestAnUpdateTaskHasALease`: `requeueAfter(agent.TaskUpdateAgent) > 0`; the lease table in `invariants_test.go` gains a row.
  - `TestAHostSucceedsWhenItHeartbeatsTheTargetVersion`: host `1.3.4` then heartbeat `1.3.5` with target `v1.3.5` closes the attempt and publishes the host.
  - `TestARepeatedHeartbeatResultIsANoOp`, `TestAFailedResultOnTheHeartbeatClosesTheAttempt`.
  - `TestAHostAttemptTimesOutAfterNinetyMinutes`; `TestAnUnhealthyHostKeepsItsOpenAttemptUntilTheTimeout`.
  - `TestADeletedHostsOpenAttemptIsCancelled` (Review Focus 3).
  - `TestTheHostViewCarriesNoElapsedTime`: two consecutive renders of the same state are byte-identical.
  - `TestHostUpdateProblemsAreExemptFromThePublicStatusPage`.
- [ ] **Step 2: Run** `go test ./internal/controller/ -run 'HostUpdate|UpdateTask|TheHost'`. Expected: FAIL.
- [ ] **Step 3: Implement.** The queue is in memory, so the pass re-derives the task from the open attempt and re-queues it. `enqueue` is the bare form (no runner stamp); check `mayAct`, `h.Supports(agent.FeatureSelfUpdate)` and `!h.Embedded` yourself. `HostView.Update` is read from the loop's in-memory status under a read lock, with no query per host, because the view is built per host in list handlers and in the derived diff. Keep values stable: the card must not repaint on every heartbeat, so nothing elapsed goes in it.
- [ ] **Step 4: Run** `go test ./internal/controller/ ./internal/docs/ ./internal/api/`. Expected: PASS. Mutation check: remove the `requeueAfter` case; the lease test must fail.
- [ ] **Step 5: Commit** "Let the controller update a host, and see it done when the host reports the new version".

### Task 4.4: The host route, and the copy that said it never would

**Files:**
- Modify: `internal/api/handlers_hosts.go` (`handleRequestHostUpdate`), `internal/api/router.go` (`POST /{id}/update` under `auth.ActionHostsUpdate`, scope `hosts:update`), `internal/auth/rbac.go` and tests, `api/openapi.yaml` (operationId **`requestHostUpdate`**, because `updateHost` is taken), `internal/api/updates_test.go`, `internal/api/api_test.go`, `docs/api-surface.md`, `docs/configuration.md` (the "it never will" passage), `docs/upgrading.md` (the sentence that a controller upgrade does not remotely replace binaries), `api/openapi.yaml` (the description that says the controller never does), `web/src/lib/api/client.ts`
- Generate: both generators

- [ ] **Step 1: Write the failing tests:** the role matrix (admin 202; operator 403); `TestAnUnknownHostIs404`; `TestAnEmbeddedHostIs409WithItsCode`; `TestTheBodyIsTheHostViewWithItsUpdateBlock`; an audit row for the caller.
- [ ] **Step 2: Run** `make build-nogui && go test ./internal/api/`. Expected: FAIL.
- [ ] **Step 3: Implement** like `handleAcceptHostCheck`.
- [ ] **Step 4: Run** `go test ./internal/api/ ./internal/docs/ ./internal/auth/`. Expected: PASS.
- [ ] **Step 5: Commit** "Add the route that updates one host".

### Task 4.5: The host card

**Files:**
- Create: `web/src/lib/hosts/update.ts`, `web/unit/host-update.test.ts`, `web/src/lib/hosts/HostUpdateDialog.svelte`, `web/tests/host-update.spec.ts`
- Modify: `web/src/lib/hosts/HostCard.svelte` (a row **outside** the folded `<details class="upgrade">`, gated `canAdmin`, with a `Badge` and a `Button` whose `ariaLabel` names the host; the disabled reason as visible text; the copied command stays), `web/src/lib/hosts/actions.ts` (non-optimistic, `fleet.ingestHosts`), `web/src/routes/Hosts.svelte` (dialog state; the bulk action arrives with Task 5.5), `web/src/routes/HostDetail.svelte`, `web/src/lib/hosts/no-report.ts` ("Zoomies never updates a host itself" and "never runs it for you" no longer hold) with `web/unit/host-no-report.test.ts:51-61` and `host-health.spec.ts`, `web/src/lib/problems/targets.ts` for `host.update_*`, `web/src/lib/api/client.ts`, `docs/hosts-and-pools.md`

- [ ] **Step 1: Write the failing tests.** Unit: `update.ts` maps each state to a tone, a label and whether it can be pressed, and keeps "behind" a neutral fact, not a status colour. Playwright: join a real host with version `1.2.0` and features `["self-update"]` against the seeded controller at `1.3.0`; the card shows Update; an operator sees the command only; pressing it confirms with `tone="default"`, posts, and the card shows the in-flight state; a heartbeat with `1.3.0` closes it; a host without the feature shows why it cannot; mobile and accessibility.
- [ ] **Step 2: Run** `cd web && npm run test:unit`. Expected: FAIL.
- [ ] **Step 3: Implement.** Do not put the button inside `<summary>`. A fleet-wide change in `fleet-shape.ts` repaints every grid, so keep the host `update` block out of anything that changes per heartbeat.
- [ ] **Step 4: Run** `cd web && npm run lint && npm run test:unit`, then the Playwright spec on both projects. Expected: PASS.
- [ ] **Step 5: Commit** "Add the Update button to a host's card, and keep the command beneath it".

### Task 4.6: The drill — an update with a job running

**Files:**
- Create: `test/drill/agent_update_test.go` (`//go:build drill`)
- Modify: `test/drill/harness.go` (`builtBinary()` can build with `-X …version.Version=1.0.0` and `1.0.1`; `spawn` takes the binary), `test/drill/budget.go`

- [ ] **Step 1: Write the drill**, modelled on `agent_restart_test.go`: start `newFleet(t)` with the controller at `1.0.1` and the remote agent at `1.0.0`; the controller runs with `ZOOMIES_UPDATE_MODE=manual` and the check interval `0`; the test acts as the root helper — writes `helper.json`, waits for `update/request.json`, kills the agent, respawns the `1.0.1` binary with the same environment, writes `result.json`. Assert: the same runner pid is still alive, the same host id, the host view's version is now `1.0.1`, the attempt is `succeeded`, and the job completes normally.
- [ ] **Step 2: Run** `make test-drill`. Expected: PASS within the drill budget; if the budget test complains, add the wait to `budget.go`.
- [ ] **Step 3: Run it once with the agent's adoption removed** (comment out the start-up adoption call) and see the drill fail; restore. State this in the pull request (delivery rule 14).
- [ ] **Step 4: Commit** "Drill an agent update that runs while a job is running".
- [ ] **Step 5:** Update the ZF-232 row; commit "Record the host update".

## Part 5 — Automatic updates and rollouts

Pull request 5. Exit: the planner's tables hold; a halted rollout starts
nothing; the soak holds a release back; switching to `off` cancels a pending
rollout.

### Task 5.1: The rollouts table

**Files:**
- Create: `internal/store/migrations/0083_update_rollouts.sql` (next unused prefix at the time), `internal/store/update_rollouts.go`, `internal/store/update_rollouts_test.go`
- Modify: `internal/store/migrations_test.go`, `internal/store/ids.go` (`PrefixUpdateRollout = "rol"`), `internal/store/update_attempts.go` (`UpdateAttempt.RolloutID`; the migration adds `rollout_id TEXT NOT NULL DEFAULT ''` to `update_attempts` as an additive `ALTER TABLE`, as `0076` did), the prune job from Task 3.1

**Interfaces:**
- Produces: `type UpdateRollout struct{ID, Target, Trigger, State, StartedBy, HaltedReason string; StartedAt time.Time; FinishedAt *time.Time}`; constants `RolloutRunning`, `RolloutHalted`, `RolloutDone`, `RolloutCancelled`; `func (s *Store) CreateUpdateRollout(ctx context.Context, r *UpdateRollout) error` (`ErrConflict` while one is running or halted); `func (s *Store) OpenUpdateRollout(ctx context.Context) (*UpdateRollout, error)` (`ErrNotFound` when none); `func (s *Store) HaltUpdateRollout(ctx context.Context, id, reason string) (bool, error)`; `func (s *Store) ResumeUpdateRollout(ctx context.Context, id string) (bool, error)`; `func (s *Store) FinishUpdateRollout(ctx context.Context, id, state string) (bool, error)`; `func (s *Store) PruneUpdateRollouts(ctx context.Context, before time.Time) (int, error)`.

- [ ] **Step 1: Write the failing tests:** `TestOnlyOneRolloutIsOpenAtATime` (a second create is `ErrConflict` whether the first is running or halted); `TestHaltingAndResumingAreGuardedByState` (halting a halted rollout returns false; resuming a running one returns false); `TestFinishingARolloutIsFinal`; `TestPruneSparesAnOpenRollout`; `TestAnAttemptRemembersItsRollout`.
- [ ] **Step 2: Run** `go test ./internal/store/`. Expected: FAIL.
- [ ] **Step 3: Implement.** "At most one open" is a partial unique index on a constant expression over `state IN ('running','halted')`, or a check inside `s.tx` if the index is awkward in SQLite; either surfaces as `ErrConflict`.
- [ ] **Step 4: Run** the same command. Expected: PASS. Mutation check: allow a second open rollout; the first test must fail.
- [ ] **Step 5: Commit** "Record rollouts, one open at a time".

### Task 5.2: The planner

**Files:**
- Create: `internal/updates/plan.go`, `internal/updates/plan_test.go`

**Interfaces:**
- Consumes: Task 1.2.
- Produces:
  - `type Attempt struct{ID, Scope, HostID, To, State string; RequestedAt time.Time}`; `type Rollout struct{ID, Target, State, HaltedReason string}`.
  - `type HostFacts struct{ID, Name, Version, GOOS, GOARCH string; Embedded, Healthy, CanSelfUpdate bool; WhyNot string; ActiveRunners int; Open *Attempt; Failures int; LastFailedAt time.Time; FailedInRollout string}` (`FailedInRollout` is the error of this rollout's failed or timed-out attempt for the host, or empty).
  - `type Snapshot struct{Now time.Time; Mode Mode; Soak time.Duration; Running string; GOOS, GOARCH string; Releases []Release; HelperReady, Fenced bool; Controller *Attempt; ControllerFailures int; ControllerLastFailedAt time.Time; Rollout *Rollout; Hosts []HostFacts}`.
  - `type ActionKind string` with `ActionRequestController`, `ActionStartRollout`, `ActionUpdateHost`, `ActionTimeOut`, `ActionHalt`, `ActionFinishRollout`, `ActionCancelRollout`; `type Action struct{Kind ActionKind; HostID, AttemptID, Tag, Reason string}`; `type Plan struct{Actions []Action; Sentence string}`; `func Decide(Snapshot) Plan`; `const AttemptTimeout = 90 * time.Minute`, `RetryAfter = 30 * time.Minute`, `MaxFailuresPerTag = 2`.

- [ ] **Step 1: Write the failing tests** as one table-driven `TestDecide...` per behaviour, each with a built `Snapshot`:
  - `TestDecideDoesNothingWhenFenced` (sentence says the controller is fenced).
  - `TestDecideDoesNothingWhenOffExceptCancelAPendingRollout` (Review Focus 5): mode `off` with a running rollout yields exactly `ActionCancelRollout`; with none, no actions.
  - `TestDecideRequestsTheControllerOnceTheSoakHasPassedInAuto` and `...NotBefore` (the sentence is `updates.Choose`'s); `...NeverInManual`.
  - `TestDecideWaitsForTheHelperAndSaysSo`: `HelperReady` false in `auto` yields no action and a sentence naming `sudo zoomies updates helper install`.
  - `TestDecideStartsNoHostWhileTheControllerAttemptIsOpen`.
  - `TestDecideStartsARolloutInAutoWhenAHostIsBehindTheControllersRelease` and `...NotWhenTheControllerHasNoTarget` (running `main-sha-abc1234`).
  - `TestDecideUpdatesTheHostWithTheFewestRunnersFirst` (three hosts with 3, 0, 0 runners: the 0-runner host with the earlier name) and `TestDecideStartsOnlyOneHostAtATime`.
  - `TestDecideSkipsEmbeddedAheadUnhealthyAndCannotUpdateHosts`, each with its reason; an agent that reports a later release is never touched.
  - `TestDecideHaltsAfterAFailureAndStartsNothing` (`FailedInRollout` set → one `ActionHalt` carrying the host and the error; a halted rollout yields no host actions and a sentence that says to resume or cancel).
  - `TestDecideTimesOutAnAttemptAfterNinetyMinutes` (89m → none, 91m → `ActionTimeOut`), for controller and host.
  - `TestDecideWaitsThirtyMinutesAfterAFailure` and `...ForAnOperatorAfterTwoFailuresOfOneTag`.
  - `TestDecideTargetsTheControllersReleaseNotLatest`: a newer release exists, the controller has not updated; hosts are brought to the controller's, not the newer.
  - `TestDecideFinishesARolloutWhenNoHostIsBehind`.
  - `TestDecideIsDeterministic`: shuffling `Hosts` and `Releases` yields an identical `Plan`.
- [ ] **Step 2: Run** `go test ./internal/updates/ -run Decide`. Expected: FAIL.
- [ ] **Step 3: Implement.** One pure function, no clock read: time comes from `Snapshot.Now`. Order of rules: fenced; off; time-outs; the controller; the rollout. A start-up check for "behind" uses `version.CompareBuilds(host.Version, target) == version.SkewBehind`, never string equality. Sentences are English for a person, British spelling, and copy `Choose`'s for the controller's wait.
- [ ] **Step 4: Run** `go test ./internal/updates/`. Expected: PASS. Mutation checks: remove the one-at-a-time rule; the one-host test fails. Remove the halt; the halted case fails.
- [ ] **Step 5: Commit** "Decide what automatic updating does next, as a pure function with its reasons".

### Task 5.3: The loop that applies the plan

**Files:**
- Modify: `internal/controller/updates_loop.go` (the snapshot builder `updatesSnapshot`, the applier, wake on a heartbeat that changes a version and after an attempt closes), `internal/controller/updates_hosts.go`, `internal/controller/updates_apply.go`, `internal/auth/auth.go` (`AutoUpdateIdentity()`: `{Kind: KindSystem, ID: "auto-update", Name: "zoomies auto-update", Role: store.RolePlatform}`), `internal/controller/updates_views.go` (`Rollout *UpdatesRollout{ID, Target, State, HaltedReason string; Done, Total int; Current string}` and the planner's sentence as `Reason`), `internal/controller/updates_loop_test.go`

**Interfaces:**
- Consumes: Tasks 3.2, 4.3, 5.1, 5.2.
- Produces: `func (c *Controller) StartHostRollout(ctx context.Context, by UpdateActor, hostIDs []string) (*UpdatesView, error)` (`ErrUpdateModeOff`; `ErrUpdateRolloutHalted` while one is halted; `ErrUpdateInProgress` while one is running; `ErrUpdateNothingNewer` when no host is behind); `func (c *Controller) ResumeRollout(ctx context.Context, by UpdateActor) (*UpdatesView, error)`; `func (c *Controller) CancelRollout(ctx context.Context, by UpdateActor) (*UpdatesView, error)`.

- [ ] **Step 1: Write the failing tests** with the controller harness, `h.advance`, `h.restart` and a fake agent:
  - `TestTheLoopStartsARolloutAndUpdatesOneHostThenTheNext` (auto; two hosts behind and advertising the feature; the second is queued only after the first heartbeats the target).
  - `TestARolloutHaltsOnTheFirstFailureAndResumesWhenAnOperatorSaysSo` (halted → no queue; `ResumeRollout` → continues).
  - `TestARestartMidRolloutCarriesOn` (`h.restart()` between queueing and the heartbeat; the attempt completes and the next host follows).
  - `TestAHostDeletedMidRolloutDoesNotHangTheRollout` (Review Focus 3): its attempt is `cancelled` and the rollout moves on, or finishes.
  - `TestSwitchingToOffCancelsAPendingRolloutButLetsAnInFlightAttemptFinish` (Review Focus 5).
  - `TestAutoUpdatesAreAuditedAsTheAutoUpdateActor`, `TestAnOperatorsRolloutIsAuditedAsTheOperator` and `TestResumeAndCancelAreAudited`.
  - `TestARolloutNeverChangesAnOperatorsCordon`: a cordoned host is updated and is still cordoned afterwards.
  - `TestTheLoopDoesNothingWhileFenced`.
  - `TestTheStatusFrameCarriesTheRolloutAndTheSentence`.
- [ ] **Step 2: Run** `go test ./internal/controller/ -run 'Loop|Rollout'`. Expected: FAIL.
- [ ] **Step 3: Implement.** `updatesSnapshot` reads the settings from `c.cfg()`, the releases and helper readiness from the controller, the open attempts and rollout from the store, and the hosts through `ListHosts` (which fills `ActiveRunners`); `CanSelfUpdate` and `WhyNot` come from `hostCanSelfUpdate` (Task 4.3), the same function the host view uses, so the button and the planner never disagree. The applier takes `c.mayAct()` first and executes `Plan.Actions` in order; each effect is the same method a button calls, with the system identity. After any change it calls `publishDerived`.
- [ ] **Step 4: Run** `go test ./internal/controller/`. Expected: PASS. Mutation check: let the applier run while fenced; the fenced test fails.
- [ ] **Step 5: Commit** "Run the planner on its own loop and apply what it decides".

### Task 5.4: The rollout routes and the CLI

**Files:**
- Modify: `internal/api/handlers_updates.go` (`handleStartRollout`, `handleResumeRollout`, `handleCancelRollout`), `internal/api/router.go`, `internal/auth/rbac.go` (`ActionUpdatesRollout`, admin) and tests, `api/openapi.yaml`, `internal/api/updates_test.go`, `internal/api/api_test.go`, `docs/api-surface.md`, `cmd/zoomies/updates.go` (`apply --hosts`, `apply --host <name|id>`, `resume`, `cancel`), `cmd/zoomies/updates_test.go`, `docs/cli.md`, `web/src/lib/api/client.ts`
- Generate: both generators

- [ ] **Step 1: Write the failing tests:** the role matrix for `POST /updates/hosts` (admin 202; operator 403), `POST /updates/rollout/resume` (admin 202) and `DELETE /updates/rollout` (admin 200); `update.rollout_halted` when starting while one is halted; `update.nothing_newer` when no host is behind; CLI tests for each new form.
- [ ] **Step 2: Run** `make build-nogui && go test ./internal/api/ ./cmd/zoomies/`. Expected: FAIL.
- [ ] **Step 3: Implement** like Task 3.4.
- [ ] **Step 4: Run** the same command and `go test ./internal/docs/`. Expected: PASS.
- [ ] **Step 5: Commit** "Add the routes and commands that start, resume and cancel a rollout".

### Task 5.5: Rollout progress in the UI

**Files:**
- Modify: `web/src/routes/Hosts.svelte` (an "Update N hosts" action in `PageHeader`, and an admin-only panel for progress, resume and cancel, in the pattern of the existing admin-only panel), `web/src/lib/settings/UpdatesPanel.svelte` (the planner's sentence and the rollout), `web/src/lib/updates/words.ts`, `web/tests/updates.spec.ts`, `web/tests/host-update.spec.ts`, `docs/ui.md`

- [ ] **Step 1: Write the failing tests.** Unit: words for `running`, `halted` (names the host and says to resume or cancel), `done`. Playwright: with two seeded hosts behind, an administrator starts a rollout, sees progress, sees it halt on a failed attempt, resumes, cancels; the planner's sentence appears in the panel for `auto`; mobile and accessibility.
- [ ] **Step 2: Run** `cd web && npm run test:unit`. Expected: FAIL.
- [ ] **Step 3: Implement** with existing tokens and components. Halted is "draining"-coloured (held until an operator acts); failed is `danger`; done is `idle`; "behind" stays a neutral fact (`docs/ui-guidelines.md`).
- [ ] **Step 4: Run** `cd web && npm run lint && npm run test:unit`, then the Playwright specs on both projects. Expected: PASS.
- [ ] **Step 5: Commit** "Show a rollout's progress, and let an administrator resume or cancel it".
- [ ] **Step 6:** Update the ZF-232 row; commit "Record automatic updates".

---

## Part 6 — Drills, documentation and screenshots

Pull request 6. Exit: the drills run in CI; the documentation says what each
mode does and what the helper is allowed to do.

### Task 6.1: A rollout across a restart, and the helper against a fake systemctl

**Files:**
- Create: `test/drill/update_rollout_test.go` (`//go:build drill`), `test/upgrade/helper-check.sh`
- Modify: `Makefile` (`test-upgrade` also runs `helper-check.sh`), `test/drill/budget.go`

- [ ] **Step 1: Write the drill:** with the controller in `auto`, soak `0`, two stub agents behind, `kill` and restart the controller after the first host's task is queued and before it reports; assert the first host's attempt closes as `succeeded`, the second follows, both hosts report the new version, and no runner changed pid.
- [ ] **Step 2: Write `helper-check.sh`:** a fake `systemctl` on `PATH` that records its argv, a local HTTP server standing in for the release host (`ZOOMIES_BASE_URL`) serving a tag, its binary and `checksums.txt`, an installed old binary, a request file; run `zoomies updates helper run`; assert the binary is replaced, `zoomies.previous` has the old bytes, `systemctl restart` was called for the unit, and `result.json` is `ok` with the tag. A second case: a wrong checksum leaves the binary untouched and `result.json` says why.
- [ ] **Step 3: Run** `make test-drill` and `make test-upgrade`. Expected: PASS.
- [ ] **Step 4: Run each once with its rule removed** (the controller restart that drops the in-memory queue; the checksum comparison) and see it fail; restore; say so in the pull request.
- [ ] **Step 5: Commit** "Drill a rollout across a controller restart, and run the helper against a fake systemctl".

### Task 6.2: The documentation and the screenshots

**Files:**
- Modify: `docs/upgrading.md` (a new "Updating from the web UI" section: what each mode does, the helper and its consent, what happens to running jobs, the soak, how to roll back by hand with `zoomies.previous` and the pre-migration copy; and the first-upgrade note that hosts which predate the feature need one manual upgrade — keep every inbound anchor listed in the notes), `docs/architecture.md` (a Mermaid sequence for the flow beside the task-queue one, and rows for `internal/updates` and its `channel` package in the components table; keep `#why-the-agent-connects-outbound`), `docs/hosts-and-pools.md` (a pointer from the skew paragraph), `docs/privacy.md` (what is contacted and what is downloaded, and by whom), `docs/configuration.md`, `docs/ui.md` (the two screenshots, each with its own alt text), `GLOSSARY.md` (helper, soak, rollout), `web/tests/support/screenshots.mjs` (`settings-updates` and `hosts-update` entries), `docs/screenshots/*.webp` (both themes), `ROADMAP.md` (move the ZF-232 entry from section 8 to section 6 with one line and the pull requests; change record 3.6), `roadmap/progress.md` (`done`, with the evidence)

- [ ] **Step 1: Write the pages.** Plain prose in the repository's voice; diagrams in Mermaid; no mention of a plan, a tier or an operated instance (delivery rule 15).
- [ ] **Step 2: Run** `go test ./internal/docs/ ./internal/naming/` and, where `mkdocs` is installed, `mkdocs build --strict`. Expected: PASS; CI runs the strict build otherwise.
- [ ] **Step 3: Capture** with `make screenshots` (needs Pillow) and commit both themes of each new image.
- [ ] **Step 4: Run** `make lint && make test`. Expected: PASS.
- [ ] **Step 5: Commit** "Document updating from the web UI, and photograph it".
