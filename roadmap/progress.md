# Work-package record

The one status and evidence record for the active
[ROADMAP.md](../ROADMAP.md). Every package defined there has a row here, and
the row is where its delivery state lives: the roadmap says what work is
authorised and in what order; this file says where it has got to. This is not
a second roadmap: do not add, reorder or authorise work here. Keep it current
in the same pull request as the work.

## How to read a row

**Status** is one of:

| Status | Meaning |
| --- | --- |
| `not_started` | Nothing merged. |
| `in_progress` | Started, not finished. Some of its pull requests may already have merged; the Evidence cell says what remains. |
| `implemented` | Merged, CI green, tests present. The gate it serves may still be pending. |
| `done` | Every pull request the package names is merged and its acceptance holds. Stronger than `implemented`, which says only that what shipped is sound; weaker than `validated`, which needs evidence in [validation/](validation/) that a real run met the criteria. |
| `validated` | Its acceptance criteria met with evidence linked from [validation/](validation/). |
| `blocked` | Waiting on something named in the row. |
| `superseded` | The need is met by something else; the row links to it and to the evidence that it meets the same criteria. |
| `withdrawn` | The roadmap stopped authorising it (ROADMAP v3.0, 19 September 2026), with the reason and what the withdrawal costs in ROADMAP.md section 7. The code, harnesses and records it produced stay where they are. |

**Classification** is what the reconciliation (ZF-001) found on `main` at
`6d12a72`: *existing, needs validation* means the capability is there and
the work is proving it; *extension* means the seam exists and the work
builds on it; *new* means there is nothing to extend.

**Session** records the Claude model and effort that did the work, whether it
ran as one session or an orchestration, and how many review rounds it took
before merge, so that [agent-models.md](agent-models.md) can be judged on
evidence.

## Phase 0: baseline

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-001 | Reconcile completed work and define the next slice | new | `done` | the improvement plan (done) | Claude Fable 5.1, `high`; one orchestration of 12 mappers, 12 adversarial verifiers and a document critic; 1 review round | [validation/baseline-6d12a72.md](validation/baseline-6d12a72.md); [ROADMAP.md](../ROADMAP.md) §6–8 carry every package, delivered and active **ROADMAP v3.0 (19 September 2026):** `done`; the acceptance held on 6 September and nothing was left to ship. |
| ZF-002 | Support matrix, invariants and measurement contract | mixed | `done` | ZF-001 | docs as ZF-001; code slice: Claude Fable 5.1, `high`, one session, 1 review round | [support-and-measurement.md](support-and-measurement.md); the code slice is merged: readiness names the schema, a held job's timeline says so with `waiting` and `approved`, and the completed count is split four ways. `validated` waits on the owner's reference host (decision 12) for the recorded OS and runtime versions **ROADMAP v3.0 (19 September 2026):** `done`. The reference-host version record decision 12 asked for is withdrawn. Extended on 16 September by `fault_kind` on jobs and runners (migrations `0034`, `0035`, #295), which made the contract's fleet-fault-versus-workflow-fault line a counted column. |
| ZF-003 | Supply-chain hygiene and three corrections | new; no behaviour change | `done` | nothing | Claude Fable 5.1, `high`, one session, 1 review round | every `uses:` under `.github/workflows/` is a commit with its release beside it; `.github/dependabot.yml` covers actions, Go modules, npm under `web/` and the images under `deploy/`, weekly and grouped; `govulncheck` runs on every change and weekly against `main`; the three pages say what the code does, and the payload carries `schema_version: 1`. `validated` when CI has run on the pins and the first Dependabot pull requests arrive **ROADMAP v3.0 (19 September 2026):** `done`: CI has run on the pins since 6 September and the first Dependabot pull requests (#78 to #81) arrived the same day; CodeQL, Scorecard, fuzzing and package cleanup followed ([validation/scorecard-hardening.md](validation/scorecard-hardening.md)). |
| ZF-004 | Six corrections from reading the instance as a service | new; behaviour narrowings | `done` | nothing | Claude Fable 5.1, `high`, one session | `IsDemoID` requires a fixture's shape, not only its prefix (`store.LooksGenerated`), and a test seeds every fixture and holds each to it; installation targets are folded on write and compared folded, with the older of two installations on one target winning every time; the scheduler's snapshot carries the GitHub rate-limit holds and a held pool creates nothing, says why, and raises `pool.github_rate_limited` when jobs wait; `/usage` carries `history_from` and the page says where each figure is complete from; `retention.audit` is `retention.scaling_events`, the old key honoured with `retention.audit_renamed`; an unsigned webhook delivery is refused before its body is read. Each has a test that fails with the change removed **ROADMAP v3.0 (19 September 2026):** `done`; the acceptance (a test that fails with each change removed) holds. |

## Phase 1: correctness and security under failure

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-101 | Enforce GitHub target boundaries everywhere | new (jobs carry no installation identity; label-only matching on all four paths) | `done` | ZF-002 | PR1 and PR2: Claude Opus 5, `ultracode`; one orchestration of 8 subsystem mappers and 8 adversarial verifiers for PR1, a smaller one of 3 and 3 for PR2, each followed by one session | PR1 merged as #99: a job carries the installation covering its repository, `scheduler.Eligible` asks enabled, then installation, then labels, and all four matching paths go through it. Migration `0012` (not `0010`: 0010 and 0011 shipped since the plan was written). PR2 merged as #100: poll freshness and the rate-limit hold are both per installation, freshness credited by the delivery's repository rather than by the secret that verified it. PR3 merged as #101: the runner-group fallback is a `pool.runner_group_unresolved` warning on the drawer and the pool's own page instead of a log line, and `docs/hosts-and-pools.md` says a pool belongs to one installation and that GitHub, not Zoomies, makes the final dispatch decision inside an organisation. Every behavioural test was run against the code with its rule removed and confirmed to fail first. All three pull requests are done: the two-installation tests pass on every path, and ineligible work carries its reason on the Jobs page through the drawer's note |
| ZF-102 | Make runner and agent reconciliation convergent | mixed (mechanics exist; adoption on restart and a controller lock do not; the log-relay host check landed with the security review) | `done` | ZF-002; N02 needs `main` deployed | PR1: Claude Fable 5.1, `xhigh`, one session. PR2: Claude Opus 5, `ultracode`; one orchestration of 3 subsystem mappers and 3 adversarial verifiers, then one session | All four pull requests are done. PR1 merged as #82: the "Reconciliation invariants" section in `docs/architecture.md` with `internal/controller/invariants_test.go`. PR2 merged as #103: a database lock and a controller lease, with `--takeover` and `controller.lease_lost`. **Half of PR2 was already done**: the log relay has checked the authenticated host since PR #90's security review, with a test (`TestLogRelayRefusesAnotherHostsStream`), so only the lock and lease were outstanding. PR3 merged as #104: an agent adopts every runner already on its host before its loops start, and the controller answers each heartbeat with the runners it has no row for so that only those are reaped. `docs/upgrading.md` no longer hedges: a restart keeps its runners on a single-VM install too. PR4 merged as #105: late reports settle the workload without resurrecting a terminal row, `task_issued_at` survives the queue a restart drops, `host.duplicate_agent` detects a shared credential by session alternation, and the restart-boundary table covers seven boundaries **ROADMAP v3.0 (19 September 2026):** since: GitHub calls no longer run under `reconcileMu` and the log relay no longer queues tasks for a host that never comes back (#299). |
| ZF-103 | Reserve host resources and enforce bounded admission | extension (slot model complete; no host resource reporting) | `done` | ZF-102 | 103a: Claude Opus 5, `ultracode`; one orchestration of 30 reviewers over the diff. 103b: Claude Opus 5, `high`, one session | 103a done and merged as #115: an agent reports its host's CPUs, memory and the disk behind its work directory, sized by the daemon that will run the runners rather than by the agent's own cgroup share, and a re-join keeps the operator's reserve. Migration `0017` -- the disk observations and the operator's reserves; `cpus` and `memory_mb` have been on the host row since `0011`, and `0016` is ZF-105's drain timeout. Its own adversarial review then found three test gaps in it, all confirmed by mutation and closed in #118: the byte-to-MB conversion was unpinned (raw bytes into `disk_total_mb` passed the suite), nothing connected measuring a host to reporting it (the two lines that put the figures on the wire could be deleted with everything green), and `f_bavail` versus `f_bfree` had no test. Only the disk pair reaches the Hosts page in #118: the card could already say what a machine is, because vCPUs and memory have been on the view since `0011`, but not what its runners have left to write to. **103b, the admission half, is done**: `scheduler.Reserve(pool, host)` is what one runner costs -- the pool's `resources` per field, doubled for a `dind` pool because the backend gives the sidecar the same limits, and one slot's worth of `Host.Allocatable()` for each field the pool left unset. The host set carries CPU, memory and disk beside the slot count, seeded from allocatable less the live runners already there, and `eligible()` asks the fit. `HostCanRun` gained `HostFits`, so the wizard's count, the capacity-demand signal and image prewarming inherited it in one place rather than four. The blocked reason gained "too small for this pool's limits", "short of memory", "short of CPU" and "low on disk", each with its own fix, because the four are four different things to do. Two decisions worth reading before the third pull request: the fallback share is why a fleet of pools with no limits admits exactly what it admitted before the upgrade, and free disk is charged only for the runners a pass adds, because the measurement already contains what the runners already there have written. The floors under a host's reserve (512 MB of memory, 2 GB of disk, no CPU floor) are what hold anything back until the third pull request gives `SetHostReserve` a route. **The verifier finding this closed**: the snapshot keeps failed rows and the slot count does not, so the reservation filters on `State.Live()` and a failed runner stops being charged for. **Still open from that list**: the agent samples each container's enforced memory limit and the controller drops it, an observed-against-reserved signal already on the wire. Twelve behavioural rules were each removed and seen to fail first, and one assertion the plan did not name was written because of it -- the fallback share survived deletion until a test put an unlimited pool and a 6 GB pool on the same host. **What the third pull request still owes**: `SetHostReserve` has no route and no caller outside tests, `PATCH /hosts` still takes only capacity and labels, and reserve, allocatable and reserved are on no view, so an operator can see what a host has and not what the fleet has promised away on it. The `pool.resources_unenforced` warning for a `process` pool that sets limits is owed there too: the reservation is bookkeeping on that backend, because nothing applies a cgroup. **The third pull request is done, and with it the package.** The reserve had no route and no caller outside tests, so the floors were the only reserve any host ever had: `PATCH /hosts` takes `reserve_cpus`, `reserve_memory_mb` and `reserve_disk_mb` now, each independent, and each **refused rather than clamped** when it would leave nothing to place on or is held back from a figure the host has never reported -- an operator who typed megabytes for gigabytes should be told, and a reserve the scheduler ignores should not be shown as though it were held. The reserve is written by its own statement, never by the path a heartbeat takes: a host cannot talk its way out of the room its operator kept for it. **The host view carries the three figures an operator needs together** -- the reserve, the allocatable and what the live runners have promised away -- and the third is *the scheduler's own sum*, recorded from the snapshot each pass decided on rather than recomputed for the page, because a figure on screen that disagreed with the one the pass placed against would be worse than none: it would be believed. It reads as unknown until a pass has run, since zero would read as an idle machine. The Hosts page draws committed against allocatable as CPU and memory bars, taking the same pending colour the nearly-full disk already takes, and a host whose agent never measured itself carries a "Size unknown" badge and an info-severity `host.resources_unknown` -- nothing is broken, it is placed by slots exactly as every host was before, and a card with every figure missing otherwise reads as broken rather than as old. **`pool.resources_unenforced` is the honest half**: a `process` pool's limits bind nothing, because that backend starts a runner as a plain process with no cgroup, and while the reservation still holds the room the room is bookkeeping. The pool page now says what an unlimited pool is charged -- one slot's worth of whatever host it lands on -- because "no limits" reading as "no reservation" is the misunderstanding the fallback exists to prevent. Two gauges join the fleet series, deliberately excluding cordoned and unhealthy hosts, since a cordoned machine's memory is not the fleet's to place into. Eight rules were removed and seen to fail. Section 10 sequenced this first in Assignment B **ROADMAP v3.0 (19 September 2026):** since: every runner has a size (`runners.default_cpus`, `runners.default_memory_mb`, #305), per-pool timing overrides (migration `0037`, #311), a stranding edit answers 409 (#302), a docker-in-docker pair is one slot (#314), the runner's resource sample reaches the API (`0038`, #344) and elastic CPU bursting (`0039`, #350). |
| ZF-104 | Verify control-plane access and secret boundaries | mixed (matrix and most tests exist; log relay unscoped to host; streams never re-check credentials) | `done` | ZF-101, ZF-102 | Claude Opus 5, `ultracode`; one orchestration of 10 surveyors over the nine assertions of PR1, then one session per pull request | All five done, in four pull requests. PR1 merged as #116 (the walks and the secret searches). **PR2 was already implemented**: the log relay has bound to the authenticated host since PR #90's security review, which the plan predates, with a test (`TestLogRelayRefusesAnotherHostsStream`) -- what was missing was that the test's indistinguishability check could not fail, and it now can. PR5 merged as #118 with the proxy fix and the join limiter. PR3 merged as #119; **PR4 did not** -- #119 merged carrying only the first commit pushed to its branch, and the hostile-input work went out in #120 along with the rest of the stack, so a reader sent to #119 for it finds nothing. Every behavioural test was run against the code with its rule removed and confirmed to fail; two assertions that could not be made to fail were deleted rather than shipped. `docs/security.md` §7 now names the test behind each claim **ROADMAP v3.0 (19 September 2026):** since: the OIDC state cache is capped (#317) and an impossible password hash fails closed (#309). |
| ZF-105 | Bound cleanup, retention and external failure handling | mixed (local cleanup exists; failure invisible; one listing-failure defect) | `done` | ZF-102, except the first pull request | first slice: Claude Fable 5.1, `high`, one session, 1 review round | All six pull requests done, in five merges: the listing-failure defect (#76), the cleanup record on the runner row with `runners.cleanup_failed` (migration `0015`, #108), orphaned sidecars (#110), the cache prune guard and the drain timeout (migration `0016`) together in #111, and the typed rate-limited error with a per-installation hold (#113). Of the verifier's three extra findings for the second: the work-directory leak is closed in #120 on the process backend, the only one where it is reachable, since nothing sets `Spec.WorkDir` and the Docker backend therefore never creates such a directory; the GitHub-failure-during-cleanup one is what `runners.cleanup_failed` answers; and **the third is open, not stale as this row said until 8 September**. The channel existed and was never used: `TaskBatch.Backoff` is on the wire, `api/openapi.yaml` publishes it and the agent waits on it at `daemon.go`, and no controller path set it, so a controller under load could not ask its agents to poll less often. **That is closed too, and the package is complete.** The controller counts the task polls it is holding, and above `pollShedThreshold` a poll that found nothing is answered with a wait that rises with the excess and is capped at fifteen seconds. Three decisions are the whole of it: the pressure being shed is the fleet's *size* rather than its workload, because an idle host still costs a held connection and a goroutine for the length of a long poll; a batch with work in it is **never** delayed, since the long poll exists so a task reaches its host in the instant it is queued; and the wait is jittered by the agent, because the controller sheds the whole fleet in the same instant and an unjittered backoff would bring it all back together and re-form the queue it was spreading. `zoomies_agent_polls_shed_total` is the only place the pressure shows -- the fleet keeps working, each host just hears a little later -- so it is a counter with a row in `docs/metrics.md`. Four rules were removed and seen to fail **ROADMAP v3.0 (19 September 2026):** since: a busy registration is deferred cleanup rather than a lost permission (#342), housekeeping recovers deferred registration cleanup (#344), and a cancelled run stops its provisioning runners (#348). |

## Phase 2: operable and usable

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-201 | Prove the first successful job through the UI | existing, needs validation, with two narrow extensions | `done` | ZF-101 | |  **The first pull request is done.** Its three defects were all places where the product said something that was not true, and the third was larger than the plan recorded. The bootstrap page claimed "Step 1 of 4" while the Overview's checklist draws five steps on a controller with no host of its own -- the state after every controller-only install, so the two disagreed exactly where somebody was following them; how many steps there are depends on the install, and that page runs before there is a session to ask with, so it names which end of the sequence it is at and leaves the counting to the checklist. The installer's remedy for a spent join token **never fired**: the join route is anonymous, so a refused token is a 422 rather than a 401 -- there is no credential to be unauthorised, what was rejected is the token in the body -- and the transport mapped only 401 to the sentinel the remedy keys on, so an operator whose token had expired was shown "returned HTTP 422". Only that route maps it; a 422 elsewhere is a malformed request. **And the verify dialog could not reach its own sentence about the poller**: `MissingRequirements` bundled the `workflow_job` subscription in with the permissions, and a probe returns an error when that list is not empty -- so an App with every permission it needs but no subscription was reported as a broken credential and recorded unhealthy, while the fleet it describes works on the fallback poller. Every other entry in that list is something the fleet cannot work without; this is something it works without, more slowly, which is a supported way to run. **A fourth defect came out of writing the dialog's test**: the demo shortcut lived at one call site, so pressing Verify on the seeded installation -- the one a new fleet has -- answered "the stored private key is not a PEM-encoded RSA key"; it is in the client cache now, where every path goes through it. The tests the pull request names are in: join refusals asserted at the API (garbage, expired and spent, the expired one forged directly because `CreateJoinToken` reads a non-positive TTL as "use the default") and through the browser, verify's message asserted to name the permission and the level, `/hosts/new` and `/pools/new` added to the accessibility audit, a phone-width pass over bootstrap and sign-in -- the two pages outside the app shell, which every phone rule had therefore skipped -- and a verify-dialog spec driven from the browser with no fake in the way. The end-to-end scenario also asserts the durations and the statistics shapes the Overview reads, though **that tier has still never run**: those assertions are compile-checked and nothing more. Seven rules were removed and seen to fail. **Two pull requests remain**: the verify dialog saying which repositories an installation can see, and a standalone fake GitHub for Playwright. **The second pull request is done too**: verify answers which repositories the credentials actually reach. `repository_selection` is GitHub's own `all` or `selected`, with the count and the first names beside it, because an App with every permission correct, installed on "only select repositories" and *not* on the one somebody pushes to, is a fleet where nothing ever queues and no page says why -- the quietest of the two common setup mistakes, and the one GitHub knew about all along. Listing them is best-effort: the credentials are already proven by the probe, so a failure there costs a line in the dialog rather than the answer the operator came for. Two rules removed and seen to fail. **One pull request remains**: the standalone fake GitHub for Playwright. **The third is done, and with it the package.** `test/fakegithub` is the existing fake with a port -- a test-only program, verifying no signature and minting nothing, which is what makes it useless to anything but a test and safe to point a browser at. A `connect` Playwright project runs it beside an empty controller, so the connect and verify pages are finally exercised where they live rather than at every layer beneath them, and the suite gains its **only fail-then-recover journey outside a wrong password**: connect with an unusable key, watch verify say so, fix it, watch verify pass. **Writing it found the gap that made "fix it" impossible**: the route to replace a private key has always existed and nothing surfaced it, so an operator who pasted the wrong `.pem` could only disconnect and start again -- which takes the installation's pools and their runner rows with it. A key is replaceable; a fleet should not have to be, and the card now offers it. Two harness defects were mine and worth the note: the fixture published the fake's address after the controller answered `/healthz`, so a spec could read the port from a previous run, and the fake exited the instant it printed that address because a defensive `os.Stdout.Sync()` fails on a pipe -- a guard that turned a working program into a dead one. Size M **ROADMAP v3.0 (19 September 2026):** the real-credential run of the Go end-to-end tier is withdrawn. Since: the pool wizard's automatic and advanced paths (#311) and the installer's Docker-or-Podman offer (#322). |
| ZF-202 | Actionable day-to-day diagnostics | mixed (substrate exists; no bundle, no per-job explanation) | `done` | ZF-102 | first two small fixes: Claude Opus 5, `high`, one session | **Two of the small fixes are done**, the ones that make what already exists trustworthy and need no API shape. The problems drawer now survives a section it cannot gather: every section is gathered on its own and a failure costs its own contents, with `controller.problems_partial` naming which sections are short. What it replaces is worse than an error -- one failing query returned a 500, and a drawer that will not load is indistinguishable from a fleet with nothing wrong. And the capacity-demand signing secret, the one the plan says the blanking misses, is blanked: it is what proves a notification came from this controller, so `zoomies config print` disclosed a forging key for as long as that feature has existed. The reflective test the plan asked for is what will catch the next one -- it walks the whole configuration, plants a value in every field whose name reads like a secret, and fails naming the field, so a secret added tomorrow fails on the day it is added rather than when somebody rereads a hand-written list. **The poller's visibility is done too**: `/meta` carries `poller_enabled` and `poller_last_poll_at`, and `poller.stale` and `poller.paused` are on the problems list. **The plan's parenthetical for this one was stale and is corrected**: it said both entries were "attributed to the whole poller because the pause is fleet-wide until ZF-101 changes it", and ZF-101 has landed -- the hold is per installation now -- so `poller.paused` names the installation and carries a `TargetKind` of `installation`. Telling an operator "the poller is paused" would send them looking for a fleet-wide fault that does not exist. The stamp is written at the end of `pollOnce` rather than in the loop, so a sweep that returned early because it could not read its own database leaves it where it was and `poller.stale` fires. **The runner page is done too**: `container_started_at` and `registered_at` are on the runner view and rendered as separate facts, the timeline names its stages instead of showing two rows that both read "Registering", and the host's last heartbeat is on the runner's own page. That is the package's "an operator can diagnose a stuck registration from the runner page alone" as far as code carries it; the Playwright spec the acceptance also names belongs with the test fixture, which is not written. **A mislabel went with it**: the facts panel called `started_at` "Registered", which is what `registered_at` is -- so a runner whose container came up and never reached GitHub read as one that had. It is "Workload up" now. **The small half is done**: a job GitHub is holding for a deployment review now says so in the drawer, with how long it has been held and that the wait for a runner starts at the approval -- it was the one kind of job that sat there with nothing said about it, because every panel that explains a wait keys on `queued`. Its "Queue wait" row says "Not queued yet" rather than a number: the time a held job spends is GitHub's, not the queue's, which is the same distinction the job timeline and the Gate F scheduling interval already make. **The fixture is built and the pins are in**: `ZOOMIES_SEED_STUCK=true`, on top of the demo seed, ages the demo's two starting runners past the point where `runners.not_progressing` says so, adds a pool whose host selector nothing answers, and adds a held job; a `diagnostics` Playwright project runs five specs against it, covering both stuck-runner shapes, the problems drawer's split detail and fix, the blocked pool's reason, and the held job's sentence. Two of them were checked by mutation -- renaming "Host last seen" and rewriting the held sentence each fail their spec. **CI then found the fixture's own flaw, which is the right way round**: a runner reads as stuck after half the provision timeout and is failed after the whole of it, so against the five-minute default the window was minutes wide, and this project runs last in a serial suite about ten minutes after its server booted. Both runners had been failed and replaced by the time the spec looked. The stuck server runs with a four-hour timeout now, so the window is an hour wide at its narrowest and how long the suite takes stops mattering; it was re-verified by running the whole suite serially rather than the one project. **Building it found a defect none of the three pull requests before it could have found**: a held job is unmatched by construction, because the claim happens on the approval, and `managedJobSQL` kept only `matched = 0 AND state = 'queued'` -- so every held job was hidden from the Jobs page by default and the sentence added for it was unreachable in the default view. It keeps `waiting` too now, for the reason its own comment already gave for `queued`: nothing has run it, so it is this fleet's to see. **`GET /jobs/{id}/explanation` is done**, the second of the two new things: one answer to "why is this job not running?", computed on the controller from the last plan, the pool that claimed it, and the runner and host behind it. It distinguishes `waiting` from `blocked` -- a fleet that is merely busy clears itself and a pool nothing can place never will, and the two need different advice -- and where the scheduler has already said why it could not place a runner, that sentence is what the endpoint returns rather than a second opinion computed here. It also catches the one case nothing else did: a job running on a host that has gone quiet, which looks exactly like a healthy long job. Four rules were removed and seen to fail first. **And both surfaces render it now**, which is what the endpoint was for: the job drawer's panel keeps the pool's live counts -- those are facts, and the panel is where an operator reads them -- and takes its sentence, detail and fix from the controller; `zoomies jobs get` prints the same three lines in place of the paragraph it used to reason its way to. The CLI's own test was replaced rather than deleted: the two cases it covered are still told apart, by the side that can see the fleet. **Caught by an existing spec**: the first rewrite of the panel dropped the counts along with the reasoning, and `jobs.spec.ts` failed on it -- the defect was two competing *reasons*, not two renderings of a count. **And the bundle is done, which finishes the package**: `GET /diagnostics/bundle` is one JSON document describing this instance -- the build and the process, the effective configuration and the validator's findings, everything currently wrong, the fleet's installations, pools, hosts and runners, the work in flight with the controller's own explanation for each of it, and the recent scheduler decisions. It is assembled from the same renderings the API already serves rather than from the store, which is what makes it trustworthy: every section is something the operator could have fetched themselves, so a section that is secret-free on its own route is secret-free here, and the configuration in it is `/settings`' key-by-key rendering, where a secret is absent rather than blanked -- a secret added to `config.Config` tomorrow cannot appear in a bundle by default. The route is on the secret-absence test's path list for that reason: a document assembled from secret-free sections is only secret-free while every section still is, and that test is what notices when one stops being. **Three rules were removed and seen to fail first**: assembly is section by section, so a cancelled context -- every store read failing at once -- produces a bundle whose `errors` array names each section and whose build, process and configuration survive, rather than a 500 on the one instance whose trouble somebody was trying to report; the log section names the runners that failed or are still trying to start, with the route that fetches each log, and never a body, because there is no redaction pass for a log and there cannot be a reliable one; and a bundle over its byte cap sheds sections and says which, explanations first because they are recomputable from the jobs beside them, never the fleet's own shape. **The action is admin rather than viewer**, and the reasoning is the package's own: the weakest role that covers a bundle is the strongest role inside it, because it carries the settings section and `settings.read` has always been admin. **`zoomies diagnostics` is the wrapper**: it writes the document to a file named after the instant the controller took it and prints what went in, and the bytes are the server's rather than a re-marshalling of the partial type the CLI declares -- a section added to the bundle tomorrow reaches a support case today, which is the rule its own mutation check holds. Size L; the failing tie-break test on the code this package extends was fixed as a Phase 0 defect, and `TestAStuckTieNamesTheRunnerWithNoContainer` holds it **ROADMAP v3.0 (19 September 2026):** `done`; every acceptance item holds (the fixture, the drawer-to-runner-page spec, the admin-only capped secret-free bundle). Since: fault categories and a rerun button (#295). |
| ZF-203 | Implement and prove backup and restore | new (prose only today) | `done` | ZF-102 for automatic unfencing only | first pull request: Claude Opus 5, `high`, one session | **The store primitives and the startup guards are done**, the first of the package's four pull requests. `Backup` is `VACUUM INTO` under the write mutex, which is what makes a copy whole: the documentation used to send operators to the `sqlite3` command line, a binary the container image does not ship, and the fallback it offered -- copy the file -- leaves every recent commit behind in the write-ahead log. The copy lands with no WAL beside it and mode 0600, because it holds every sealed secret and every password hash this instance has, and `VACUUM INTO` writes it with the process umask. It refuses an existing destination: a backup's destination is nearly always a name derived from the date, and the one time it is not, the file already there is somebody's older backup. `IntegrityCheck` is what makes a copy a backup rather than a file. **A read-only open exists**, and the reason is that opening read-write migrates as a side effect -- so a command that only wanted to look upgraded the schema on its way past, and verifying a backup changed it. The installer's `finished` check and the uninstall's deregistration sweep both take it. A write on such a store is `ErrReadOnly` rather than SQLite's "attempt to write a readonly database", which sends the reader to check permissions. **And the two startup guards decision 20 names are in.** The store refuses a database whose ledger names migrations this build does not have, naming them: SQLite has no opinion about columns nobody reads, which is exactly why the check has to exist -- an older binary comes up, looks healthy, and writes rows the newer one will not accept, silently, and rolling a release back is a thing people do under pressure. `docs/upgrading.md` said the opposite and is corrected. And a controller refuses to generate an encryption key over a database that holds sealed secrets: a restore that brought the database and left the key behind is indistinguishable from a first run at the key file, and the instance it used to produce started, reported itself healthy, and failed inside its first GitHub call. The wrong key cannot be refused at startup -- a key is only proven by opening something -- so that case is `crypto.key_mismatch` in the problems drawer, which is a separate code from `installation.unhealthy` because the fix is a file rather than anything on GitHub. Six rules were removed and seen to fail first. **And `zoomies backup` is done**, the second of the four. It opens the database file directly rather than talking to a controller, which is decision 20's shape and the right one: a backup is taken on the machine that holds the data, by whoever can read it, and it has to work when the controller will not start -- there is no route and no token to hold. One backup is one timestamped directory rather than a pair of files, which is what makes `--keep` retention have something whole to delete and will make restore take a single argument; retention only ever removes a directory this command made, name and manifest both, because the directory an operator points `--dir` at is often shared with somebody else's copies. **The manifest is the half that is not the data**, and everything in it answers a question asked during a restore and nowhere else: which build wrote this and the full migration ledger, since the store now refuses a database whose ledger it does not know; the key's fingerprint and where it came from, so an operator can tell whether the key file in their hand opens this database rather than finding out when the first GitHub call fails; what the key is needed for, installation by installation, never the secrets themselves; and the configuration through the same blanking `zoomies config print` uses. `Key.Fingerprint` is new for it -- twelve hex characters of a SHA-256 of the key material, enough to compare at a glance and far short of useful to anyone holding it. **The key is left out unless `--include-key` is passed, and the summary says which of the two backups was taken every time**, not only on the dangerous one: a backup with the key decrypts itself and has to be stored like the App's private key, and an operator who reads "backed up" and stops otherwise has a database nobody can ever decrypt. Three rules were removed and seen to fail first, and it was run for real against a live seeded controller -- the copy, the retention and the key-included path all behaved. The backup page's `sqlite3` instructions are gone, which also resolves the inconsistency the package's verifier found between that page and the security page: the key is kept once, wherever secrets are kept, and the fingerprint is what says the two belong together. **And `zoomies restore` is done**, the third of four. Its shape is one property repeated: every refusal happens *before anything is moved*. A corrupt copy, a backup from a newer release, and an encryption key that is not the one that sealed it are each otherwise discovered after the controller is running on the restored data -- the fleet live, the original possibly gone, and the symptom saying nothing about the restore that caused it. `--replace` is required to overwrite an existing database and moves it aside rather than deleting it, with its `-wal` and `-shm`: leaving those beside the restored file would replay one database's log into another, and the move is a raw file operation rather than a store backup because the live database may be the *newer* one, which the store now refuses to open -- the constraint the package's verifier found. **What it invalidates is split by cost**: every session and every unredeemed join token go by default, because a cookie from the day of the backup signs somebody in and an unused join token enrols a host; redeemed join tokens stay, because they cannot be used again and are the record of how each host got here; and revoking API tokens or resetting agent tokens are flags, because the first breaks whatever automation holds one and the second means walking every host. **The fence found its home**: `recovery.fenced` is a row in the restored database's own `settings` table -- the one the verifier noted shares a name with the settings API and none of its data -- rather than a line in `zoomies.yaml`, because it belongs to the data. A restored database is fenced wherever it is put, and a copy carried to a second machine arrives fenced too, which is the case the fence exists for. A value nobody can parse reads as fenced, since that is the safe side and a half-finished restore is what writes one. Six rules were removed and seen to fail first, and it was run end to end for real: back up a live seeded controller, refuse without `--replace`, restore with it, and refuse each of the wrong key, the missing key and the truncated copy. **One defect found on the way**: the read-only open's failure message still carried the read-write hint, so a corrupt backup was reported as a directory permission problem. **And the fence is done**, the last of the four, so ZF-203's code is complete and only the owner-run GitHub half of its acceptance remains. A fenced controller reads the mark at start -- before any loop, because one that began reconciling and then discovered it was fenced would already have created the runners the fence exists to prevent -- and then **decides as normal and applies none of it**: `apply` returns early, the reaper does not sweep (a restored database's idea of which runners are gone is as old as the backup, so acting on it is the one thing it must not do), and the poller does not poll (it exists to find work a fenced fleet will not run, and sweeping would spend the installation's GitHub quota to learn something it cannot act on). The plan is still computed and published, which is the point: an operator can see exactly what would happen the moment they lift it, and can tell "nothing to do" from "not allowed to". **Readiness answers 503 and liveness does not**, and the split is deliberate -- the container image's health check is `/healthz`, so a fenced controller is taken out of a load balancer's rotation and is *not* restarted by its own runtime, which would achieve nothing and lose the operator's session. Lifting is one audited admin route under a new `recovery.write` action, its own rather than `settings.write` because it is not a setting: it is a person saying a recovered fleet may act on the world again, and the audit log is where somebody later asks who decided that. Lifting an unfenced instance succeeds and changes nothing, because two operators recovering one fleet will both press it. Four rules were removed and seen to fail first. **And the mechanical drill the acceptance names is written**: `test/drill` now backs up a running fleet, stops it, restores into a clean state directory -- carrying the encryption key by hand, which is the step the docs describe and which the restore refuses to proceed without -- and asserts the integrity, the 503, the reason naming the backup, the pool that travelled, and the lift. It was checked by mutation too: with the readiness fence removed the drill fails on the 200. `roadmap/validation/drills.md` carries its first passing row. **Still owed for `validated`**: the GitHub half -- sign in, re-join a controlled agent, run a job after lifting the fence -- which is an owner action against a real organisation **ROADMAP v3.0 (19 September 2026):** `done`, and larger than written. The owner-run GitHub half of the acceptance is withdrawn. Landed 16 to 17 September, beyond the package's text and against its own cut list: the shared `internal/backup` package, scheduled copies (`backup.interval`, `backup.keep`), a staged restore applied by restart, eighteen `/backups*` routes under `backups.read`, `backups.write` and `backups.restore`, the Backups settings page, S3-compatible offsite destinations sealed under the instance key (migration `0036`), and a configuration export and import with a dry run (#297, #304, #306, #308, #312). Decision 20's 'not an API route' is recorded as overtaken. |
| ZF-204 | Safe releases, upgrades and version compatibility | mixed (skeleton exists; policy, skew, guards, backup-before-migrate and any upgrade test do not) | `done` | ZF-203 for the backup primitive; ZF-003 first | first pull request: Claude Opus 5, `high`, one session | **Compatibility is enforced and stated**, the first of six. The protocol is checked on every heartbeat now, not only at join: an agent that joined before a bump kept polling and receiving tasks it could not understand, because the check ran once and never again. **The answer is exclusion, not refusal** -- the host is flagged incompatible and excluded from placement exactly as a cordon excludes it, and nothing else. Refusing the heartbeat would send every agent in the fleet into its re-join path at the same moment, which is the outage the upgrade was meant to avoid; excluding leaves the runners working and the agent draining them, so a fleet mid-upgrade shrinks host by host rather than falling over. An agent old enough not to report a version is **not** judged: it is the one case this cannot decide, and guessing would empty a fleet the moment its controller learnt to ask. It heals on the next heartbeat after the binary is replaced, with no re-join. **The lifecycle-task predicate is an allowlist now**, which fixes the finding the package named: an agent one release behind does not ignore an unknown task kind, it reports the task failed, and the controller then marked a healthy runner failed for it. Only create, stop and remove are lifecycle; anything else leaves the runner alone. **And the verifier's cordon finding is fixed**, with a sharper answer than the plan proposed: the agent's flag was log-only, and it now refuses a *create* while cordoned or incompatible and serves every other kind -- gating the poll loop, as the plan suggested, would strand the runners a cordoned host still has to drain. **Two corrections to the plan**: "an agent may lag by one minor release" is not a rule any code here can enforce, because nothing compares release numbers -- what is enforced is that the protocol matches, and lag beyond that is a fact the Hosts page shows; and pull request 3's ledger refusal already landed in ZF-203, whose restore needed it, with no emergency override and none wanted. The protocol write is its own statement rather than a field of `UpdateHost`, for the reason the reserves have one: folding it in made a protocol change carry the free-disk drift the tolerance exists to ignore, which an existing test caught. Four rules were removed and seen to fail first. **And skew is visible**, the second of six: `host.version_behind` names the hosts on another release, and the host card carries a badge. **The verifier's finding that the two sides disagreed is fixed by making both use one comparison** -- the agent compared its version *with the commit* against the controller's while the host row stores the bare version, so two builds of one tag warned in the agent's log and matched on the Hosts page. What settled it is what "skew" means: a *release* difference. A rebuild of one tag is the same release, worth knowing in a bug report and not skew, and treating it as skew is what made every development fleet warn about itself. `version.CompareBuilds` is the single answer both sides now use, and **it refuses to order what it cannot parse** rather than guessing: a wrong order sends an operator to upgrade the wrong side, so a development build against a tag is "differs", which is still worth saying. A host *ahead* of its controller gets its own sentence and its own fix -- upgrade the controller -- because that is the direction the policy calls unsupported and telling somebody to upgrade the agent there would be telling them to make it worse. An incompatible host is left off the skew list, since it has a louder problem of its own and two entries about one machine is how a list stops being read. **Building it found a demo defect**: the demo seeded its hosts with the literal version "demo", so every one of them reported skew against the controller -- the demo exists to look like a fleet with nothing wrong, and it now seeds this build's own version. Three more rules were removed and seen to fail. **And schema safety is finished**, the third of six -- its first half having landed early in ZF-203, whose restore needed the ledger refusal first. What is added is the testing: a failing migration stops startup, leaves the ledger clean and applies on the re-run, each migration being its own transaction so a failure costs only itself; and a database at an older release opens on this binary and comes out at head, still able to take a row. **Two corrections came out of writing it.** There are two releases now, not the one the plan's reconciliation saw: `v0.1-alpha` shipped `0001_init.sql` alone and `v0.2-beta` shipped through `0010`, so the fixture covers both, which are the two points somebody's database is actually sitting at. And the fixture has to be a database *built* by applying the old migrations, not a current one with its ledger trimmed -- trimming leaves the columns the later migrations add, so re-applying them fails on a duplicate column and what is being tested is an upgrade from a database that never existed. The first attempt did exactly that, and the test caught it before it was committed. **And backup-before-migrate is done**, the fourth of six: a controller that starts and finds migrations pending on an existing database copies it first, keeping the last two. It is the rollback for the upgrade nobody planned to roll back -- migrations are one-way and the release that wrote a database refuses to open it once a newer one has moved it on, and an operator upgrading is not thinking about backups. **The naming is shared rather than merely matched**: the layout constants moved into `internal/store`, which owns them, so a copy taken automatically is restorable by exactly the command that restores one taken by hand -- a copy nobody can put back without knowing it is special is not a rollback. **The copies have their own `pre-migration/` directory**, because retention here deletes and a rule keeping "the last two" in the directory somebody points `zoomies backup --dir` at would eventually take one of theirs. A first start takes none, and neither does a restart with nothing pending; a ledger that exists and is empty -- what a crash between creating the table and applying the first migration leaves -- is its own case and its own test, because the fresh-file path never reads the ledger at all. **Running it for real found an interaction worth writing down**: `zoomies restore` opens the database it restored, so restoring a backup from an older release migrates it -- and now keeps a copy of it as it was first, so restoring an old backup no longer consumes it. Both pages say so; the migration on restore was invisible until this. Four rules were removed and seen to fail. **And the supply-chain work is done**, the fifth of six -- three of whose seven parts had already shipped, the plan's reconciliation predating the action pinning, the Dependabot configuration and the govulncheck workflow. What was missing is in: build-provenance attestations for the binaries and the controller image's digest, so a checksum saying the bytes match is joined by a signed statement of where they came from; OCI labels on both images, including the version, revision and creation date the runner images carried none of -- the images an operator is most likely to be holding when something is wrong were the ones that could not say what they were; a guard that refuses to rebuild a published tag, which is how `v0.1-alpha` came to have assets two days newer than the commit it names; prerelease marking for a tag with a hyphen, so an installer's "latest" cannot point at something nobody meant to ship; and a GitHub-hosted path selectable by dispatch on the release and site workflows, so a release is not impossible because one runner vendor is having a bad day. **Two of the verifier's findings went with it**: the release workflow granted `contents: write` to every job, and had no dispatch trigger for the runner-selection input to arrive through. **The pinning rule turned out to be a claim rather than a check** -- CLAUDE.md has said CI enforces it for some time and nothing did; `internal/docs` tests it now, with the permissions rule and the release workflow's own guards, and four rules were removed and seen to fail. **The image builds are written twice, once per vendor**, rather than once with a swapped action: this workflow runs only on a tag, so CI cannot tell anybody it has been broken until somebody needs a release, and the path that works today stays byte for byte what it was. **And the upgrade is checked against a real one**, the sixth of six: `make test-upgrade` installs the last published release the way an operator does, starts it, swaps this build in over the same state directory and checks what survived -- the version moved, `zoomies.yaml` is byte for byte what it was, the host row is the same row by id, and the pre-migration copy is on disk. Every other tier builds one binary and asks what it does; this is the only one that can ask whether an installation is still an installation afterwards, and it runs on every pull request. **It found that the documented one-line install did not work**: GitHub's /releases/latest only knows about full releases, every release here is a prerelease, and the redirect the installer reads landed on the release index with no tag in it. The installer's only CI coverage was a syntax check, so nothing could have caught it; the upgrade job is now what keeps it true. **The owner action this package carried is discharged with it** -- it asked for a fresh pre-release tag to upgrade from, and the last published release is the better fixture because it is what people are actually holding. **And a drill was written and thrown away**: an agent-upgrade drill on the runtime tier passed with adoption removed, because the reaping it claimed to prevent sits behind a two-minute constant a separate process cannot move, and a test that cannot fail is worse than no test. The gap it exposed was real -- every existing test called `adoptExisting` directly, so the call disappearing from the startup path was caught by nothing -- and that is pinned in-process now, where the clock can be moved. `hosts drain` composes the cordon and the drain in the only order that empties a host, which is what the upgrade runbook needed. Size L across small pull requests **ROADMAP v3.0 (19 September 2026):** the 'first real drill recorded in `roadmap/validation/`' clause is withdrawn; the CI upgrade job from the latest published release stands in for it. Since: assets upload one at a time (#315) and the `dev` channel survives a slow upload (#316). Releases: `v1.0.0` (13 September), `V1.1.0` (15 September, a full release with no assets because the capital letter defeated the workflow's `v*` guard; an owner input in ROADMAP section 11), `v1.2.0` (18 September, current, moved `:latest`). |
| ZF-206 | Windows runners | new; the matching vocabulary shipped and nothing behind it did | `implemented` | Gate F attempted first; ZF-105 and ZF-103a, both done | first pull request: Claude Opus 5, `high`, one session | **The first pull request is done**, the severable one that adds no behaviour and stops the product implying a platform it has not got: `runnerAsset` now names Zoomies as the half that is missing rather than claiming actions/runner has no Windows build, a pool whose `host_selector` says `os=windows` is refused at create and at the wizard's review step with a reason that says adding a host would not help, and `docs/hosts-and-pools.md` no longer uses `os=windows` as an example. Both rules were seen to fail with the rule removed. **The other three are done, on 12 September 2026, at the owner's instruction to implement Windows and arm runners and flag them as needing testing in the beta** -- ahead of Gate F, which section 10 had sequenced them after, and on the shape the roadmap recommended, recorded as decision 0003. Build and enrol: `windows/amd64` in `make dist` and the build matrix, `%ProgramData%\zoomies` as the one directory on Windows, a `windows` service kind driven through `sc.exe` with the `key= value` spelling unit-tested through the fake runner, and the agent answering the service control dispatcher so the manager does not kill it thirty seconds in, with `--log-file` because a service has no stderr. Run a runner: `win-x64` and `win-arm64` are release archives with `.zip` names, a zip unpacker with the tarball's refusals, digests for both in the generated table, `Runner.Listener.exe` and `config.cmd`, the Windows environment a .NET runner needs, a job object so that killing a runner kills what it started (with `taskkill /T` for a runner adopted after a restart), and a drain that is a kill and says so. Cleanup that converges: the directory removal retries against open handles for thirty seconds before it becomes `runners.cleanup_failed`. The `os=windows` refusal is lifted, and a Windows pool on a fleet with no Windows host now says which host to join rather than to label one. **What has not happened is any of it running on Windows**: the whole tree vets and the agent's packages test on a hosted `windows-latest` leg, and nothing has joined a host or run a job there; the support matrix row says so, and that is the beta-testing item the owner asked for. The two Windows digests were computed from the archives GitHub serves rather than read from the release notes, which this session could not reach; the runner-version workflow re-derives them from the notes on the next bump. Size L in four pull requests: process on a Windows host is the recommendation and weakens the ephemeral guarantee to a fresh work directory on a machine that keeps its state; Windows containers keep the guarantee and cost about five times as much **ROADMAP v3.0 (19 September 2026):** live qualification is withdrawn as a gate; the support-matrix row moves when a fleet joins a Windows host and runs a job, and that evidence is recorded when it arrives. `docs/index.md` still says Windows is not supported eleven lines above the paragraph that says the agent is built; ZF-005 corrected it on 19 September 2026. |
| ZF-205 | Usable telemetry and bounded history | mixed (substrate exists; four series, prune tests, a load fixture and the measurement do not) | `done` | ZF-002; ZF-101 and ZF-102 for the real latency series | | **All three slices are done.** *The series*: a queue-*age* gauge, because depth and age are different questions and only the second one distinguishes a fleet that is working from a fleet that has stopped -- ten jobs queued for four seconds and one job queued for forty minutes read the same in `zoomies_jobs_queued`; a reconcile-error counter, because a failed pass observes no duration and a controller deciding nothing therefore looked exactly like one with nothing to decide; a cleanup counter, the half of a runner's life that fails on the host rather than in the fleet and so appeared in no other series; and the rate-limit pause. **The pause is per installation, not the fleet-wide flag the plan described** -- the backoff has always been keyed by installation, and a fleet with two of them, one held, is a fleet half working that a fleet-wide flag could not describe. **The label rule now covers the whole registry**, and the obvious way of writing it did not work: gathering a scrape and inspecting what came back passed with a `repository` label added, because a vector with no observations reports nothing at all. It reads the descriptors, where a label exists as soon as it is declared. Retention has tests: three of its five delete queries had none, and a cutoff written the wrong way round deletes everything or nothing while both look like a working fleet. *The honesty*: the Overview's subtitle claimed waits covered the last hour and they covered a day -- worse, **which day depended on when you looked**, because a fetch of `/stats` defaulted to a day and a `stats` frame to an hour, so the tiles silently changed a second after each page load and the specification had said an hour throughout. One constant now, and the sentence is rendered from the payload. The scaling feed dedupes on the decision's id: a replay overlapping what a tab holds left two identical rows -- a scheduler apparently deciding the same thing twice, which is what flapping looks like -- and, the feed being keyed by that id, took the panel down rather than merely misleading. The time zone and freshness the plan also listed were already rendered from data; only the window was prose. *The measurement*: `make measure` builds ten hosts and ten thousand jobs through the store's own writer and times the reads a page makes while jobs are still being written, recorded in `roadmap/validation/` and never a threshold. **It found the audit log's action filter had no index** -- a scan of the one table Zoomies deliberately never prunes -- fixed in migration `0021`; it recorded the usage report at 30 ms as the slowest read in the product, which is the accounting rather than an index and so stands; and it recorded the problems drawer as the slowest thing on the Overview by an order of magnitude, for whoever takes that on. Before any of it, the net the plan asked for: offset pagination was untested at every layer, and its failures are the quiet ones -- a repeated row, a skipped row, a total that ignores the filter. And the bulk-prune storm is bounded: announcing thousands of deletions one at a time overran every subscriber's queue, so every open tab was cut off and refetched six endpoints -- the storm was the announcement, not the deletion. Size M in three slices **ROADMAP v3.0 (19 September 2026):** since: a fleet-chosen queue warning threshold (`ui.queue_warning_threshold`, #330), the usage report on the shared trend plot (#300), and the event bus numbering events where they are published and leaving a reconnecting subscriber room after its replay (#309). |
| ZF-207 | Two audiences for one instance | extension | `done` | ZF-202 | Claude sessions, 20–22 September 2026, one pull request per slice; acceptance spec and three fixes by one Claude session, 23 September | a `platform` role and action family above `admin` holding settings, diagnostics, recovery and the platform's problems; the problems list split into the platform's and the fleet's; copy that stops assuming the reader owns the process. Added in ROADMAP v2.33 from decision 27 **ROADMAP v3.0 (19 September 2026):** first in the delivery order, and larger than written. Migration `0031` and `internal/config/settings.go` made `server.bind`, `server.tls.*`, `server.trusted_proxies`, `security.disable_auth`, `oidc.*` and every timer instance settings any admin writes through `PATCH /settings`; `users.role` and `api_tokens.role` carry a `CHECK` naming the three roles (`0001_init.sql`), so the fourth role needs a table rebuild; `/settings/export` and `/settings/import` (#297) join the routes that move; backups move whole; a platform-owned token column; the oldest admin is promoted on upgrade. Rewritten in ROADMAP section 8. **Record, 23 September 2026:** `implemented`. All five slices are merged: the role and its migration ([#373](https://github.com/eyupio/zoomies/pull/373)), the actions ([#375](https://github.com/eyupio/zoomies/pull/375)), the upgrade that keeps what admins had ([#379](https://github.com/eyupio/zoomies/pull/379)), the settings scope ([#404](https://github.com/eyupio/zoomies/pull/404)), the problems split ([#405](https://github.com/eyupio/zoomies/pull/405)), and tokens, copy and audit ([#410](https://github.com/eyupio/zoomies/pull/410)), with API-level audience tests. Not yet `done`: the acceptance asks for a Playwright test per role, and writing it found three leaks, fixed in [#432](https://github.com/eyupio/zoomies/pull/432) — an administrator could create or promote a `platform` account or reset one's password, the database and key file paths reached an administrator, and audit frames on the event stream went unfiltered to every role. **Record, 23 September 2026 (later):** `done`. [#432](https://github.com/eyupio/zoomies/pull/432) merged with the per-role Playwright acceptance, its three fixes, and a fourth its CI run found: an administrator's support bundle carried the platform's problems, and with them the backup directory. |
| ZF-208 | Limits at the public and agent edges | extension | `done` | ZF-301b | — | per-host rate limits and one in-flight poll per host; fleet-wide ceilings as `limits.*` configuration; the create budget shared across priority tiers before priority decides within one; a one-megabyte webhook body cap. Added in ROADMAP v2.33 **ROADMAP v3.0 (19 September 2026):** second in the delivery order. The one edge limit delivered since is the OIDC pending-state cap (#317); within-tier fairness landed with #345, so the budget item is the tier boundary alone. Gains an outbound address guard for the six URLs a fleet admin can point at the process machine's metadata service or LAN. **Record, 23 September 2026:** `in_progress`: every item is open as its own pull request, none merged. Item 1, per-host limits at the agent edge and the hostile-agent drill: [#427](https://github.com/eyupio/zoomies/pull/427). Item 2, `limits.*`: [#430](https://github.com/eyupio/zoomies/pull/430). Item 3, the create budget shared across priorities: [#425](https://github.com/eyupio/zoomies/pull/425). Item 4, the one-megabyte webhook cap: [#426](https://github.com/eyupio/zoomies/pull/426). Item 5, the outbound address guard: [#429](https://github.com/eyupio/zoomies/pull/429), a warning at startup and a refusal on writes through the API, so an upgrade never stops a working install. **Record, 24 September 2026:** `done`: all five items merged — [#427](https://github.com/eyupio/zoomies/pull/427), [#430](https://github.com/eyupio/zoomies/pull/430), [#425](https://github.com/eyupio/zoomies/pull/425), [#426](https://github.com/eyupio/zoomies/pull/426) and [#429](https://github.com/eyupio/zoomies/pull/429). |
| ZF-209 | A metering ledger the usage report can stand on | extension | `done` | ZF-105, ZF-205 | — | `runner_sessions` written once at cleanup confirmation, `usage_daily` rolled up before the prune deletes its sources, `/usage` reading the roll-up for the days it covers. Added in ROADMAP v2.33; should land before ZF-303's beta **ROADMAP v3.0 (19 September 2026):** fourth in the delivery order. `/usage.csv` still carries no `history_from`; the retention keys become platform-scoped with ZF-207. **Record, 23 September 2026:** `in_progress`: all three items are open in [#431](https://github.com/eyupio/zoomies/pull/431), not merged — `runner_sessions` (migration `0047`), the `usage_daily` roll-up (`0048`) and `/usage` reading it, with `history_from` on `/usage.csv`. The 90-day equality test and the exactly-once session across a restart are in it. **Record, 23 September 2026 (later):** `done`: [#431](https://github.com/eyupio/zoomies/pull/431) merged with all three items and both acceptance tests. |
| ZF-210 | Unattended provisioning and lifecycle | extension | `done` | ZF-203, ZF-209 | — | first administrator from the environment when the users table is empty; bootstrap state on `/readyz`; export and purge of one installation's whole history; `zoomies init --answers`. Added in ROADMAP v2.33 **ROADMAP v3.0 (19 September 2026):** `in_progress`: 210a's fourth item landed before the package existed — `zoomies init --answers` with `--non-interactive` and `--print-answers`, refusing rather than guessing, with `mode: controller`, `github.skip` and `pool.skip` (`internal/installer/answers.go`; the marketplace bootstrap depends on it). Absent: the environment-driven first identity, a `bootstrap_required` field on `/readyz` (it is on `/meta`), an audit row for the answer-file administrator, and a controller-only template. 210b not started; gains a passphrase re-seal (decision 32). **Record, 23 September 2026:** 210a is open, not merged: items 1 and 2, the environment-driven first `platform` identity, audited on every path, and `bootstrap_required` on `/readyz`, in [#428](https://github.com/eyupio/zoomies/pull/428); item 3, the controller-only answers template, the docs and a drill that brings up a controller, its identity and a joined agent with no human step, in [#433](https://github.com/eyupio/zoomies/pull/433), stacked on #428. 210b not started. 210b is open in [#440](https://github.com/eyupio/zoomies/pull/440), stacked on #431, not merged: one installation's history exported as an archive, secrets re-sealed under a passphrase, imported onto a fresh instance, and a `purge=true` delete that leaves every other installation's rows byte-identical. **Record, 24 September 2026:** `done`: 210a merged in [#428](https://github.com/eyupio/zoomies/pull/428) and [#433](https://github.com/eyupio/zoomies/pull/433), 210b in [#440](https://github.com/eyupio/zoomies/pull/440). |

## Phase 3: real use, drills and Gate F

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-301 | Extend the real end-to-end harness | mixed (one polling-mode test; no result categories, no cleanup ledger, no real container in any test) | `done` | ZF-002; 301c on the owner's credentials | 301a and 301b: Claude Opus 5, `high`, one session each | Size L in three slices. 301a done: every prerequisite is checked before anything is created, a required mode makes a missing one `blocked` rather than a silent exit-zero skip, each scenario writes a JSON result carrying its category, commit, marker, run link and any residual cleanup, `test-e2e-required` runs the verifier over those records rather than trusting `go test`'s exit code, a ledger outside the temp directory records every resource before it exists and a start-up sweep clears what an earlier crashed run left on GitHub, the orphan check now asks GitHub and the host instead of asking Zoomies about Zoomies, per-run labels stop two runs colliding, and the waits fit the Makefile's timeout with an untagged test pinning that. 301b done: a `drill`-tagged tier runs the built binary as a real controller and a real remote agent joined by a join token, against an in-process fake GitHub and a stub runner on the `process` backend, and watches a workload actually appear on the machine and go away. Two drills, both green and on every pull request. **It found its own hole first**: written the obvious way the lifecycle drill passed with `deleteRegistration` commented out, because the ten-minute reaper's first sweep lands where it was looking -- it was testing the backstop. The sharp assertion moved to a second drill on the path where deleting the registration really is Zoomies' job, and that one fails in seventeen seconds when it stops. 301c (the real scenarios, owner-gated) remains **ROADMAP v3.0 (19 September 2026):** `done` for 301a and 301b; 301c, the credentialed real-GitHub scenarios and the nightly `e2e.yml`, is withdrawn. CI on the Zoomies fleet (#332, #335) exercises real delivery, real Docker and DinD on every pull request. |
| ZF-302 | Restart and recovery drills | mixed (the process-level tier landed with ZF-301b and two drills run on every pull request; fake-level tests cover every fault class but disk; none of the six fault drills is written, and the drill record is written to `roadmap/validation/drills.md`, a file no run commits) | `done` | ZF-301b, which is done | | Three pieces. The six fault drills on the tier ZF-301b built -- kill the controller mid-job, kill the agent, a dead Docker socket, a rate limit, a failing JIT endpoint, a small filesystem. The restore-and-rollback drill Gate F asks for. And retaining the drill rows: each run's rows reach that run's job summary and nowhere else, so the comparison the record exists for -- recovered cleanly forty times and then did not -- cannot be made. Retention first, before the fault drills start producing rows worth comparing. **Retention is done**, which was the piece that had to come first: the drill job appends its rows to `roadmap/validation/drills.md` and commits them, from the default branch only, so the comparison the record exists for -- recovered cleanly forty times and then did not -- can now be made from the file rather than from forty job summaries nobody kept. A pull request's rows stay in its own summary, because they describe a commit that may never exist. Each row gained the run that wrote it, since a row worth keeping is one that can be taken back to the logs behind it, and the three existing rows say `unrecorded` rather than pretending to a link. Two details are the ones that would have bitten: the step re-reads the default branch before appending, so a push that landed while the drills ran is followed rather than overwritten, and it appends from a saved copy of its own rows onto a clean tree, or a retry would write them twice. Four rules were removed and seen to fail. **Two of the six fault drills are done**, both injecting their fault while a job is actually running, because a fleet with nothing in flight recovers from anything: killing the controller mid-job leaves the runner running, the fleet still has it after a restart on the same address and database, and the job finishes and is cleaned up afterwards -- which it can only do if the agent found its way back on its own; killing the agent mid-job leaves the runner running (`detachRunner` gives it its own process group) and the agent comes back as the same host from the credentials in its work directory rather than as a second one. Both watch the runner for a few seconds rather than checking once, and check the pid file and signal 0 rather than the directory, because a death that follows its parent's arrives a beat later and the stub's marker file outlives the process that wrote it -- the first version of the assertion was satisfied by a directory. Three rules were removed and seen to fail: the agent's poll retry, the credentials a restart comes back on, and the process group that keeps a runner alive when its agent dies. **And the agent drill found something for the owner to decide**: the exit code is written by the parent that reaped the process, so an agent restarted mid-job finds its adopted runner gone with nothing recorded and calls that a failure, racing the removal the completed job set off -- so the same successful build is recorded `removed` on one run and `failed` on the next. The drill asserts what is true either way and puts the rest in the record's last column, because a drill that asserted one of the two would be a flaky test pretending to be a rule. **Two more fault drills are done, and one of them found a defect.** A failing JIT endpoint: the attempt is recorded as a failed runner carrying GitHub's own message rather than the pool silently sitting a runner short, nothing is left on the host or registered on GitHub, and the fleet places a runner by itself once the endpoint answers again -- both rules removed and seen to fail (deleting the row instead of recording the failure; an hour-long start backoff). A rate limit: the drill reads `zoomies_github_paused` on the controller's own scrape rather than its API, because that is the signal an operator's monitoring watches and a fleet out of quota looks exactly like an idle one everywhere else, and then watches it come back by itself when the reset passes -- a wall-clock deadline nothing in process can prove. **The defect it found on its first run**: the per-installation stand-down never engaged at all, because GitHub refuses the *installation token refresh* first, ghinstallation mints that inside the transport, and its refusal reached `classify` unclassified -- so the poller kept calling every two seconds while the quota was gone and the paused gauge stayed at zero, which is the exact failure ZF-105's hold exists to prevent, on the call every background sweep makes first. Fixed in `classify`, and only when the refusal's own headers say the quota is gone: a 403 for a missing permission comes back with quota to spare, and standing an installation down for fifteen minutes over that would be a wait that fixes nothing. Both halves were removed and seen to fail. **The restore-and-rollback drill is done, which is Gate F's sixth bullet.** Its two halves were in different tiers and neither was finished: the restore drill stopped at "the fence lifts and the controller answers", which proves a fleet starts rather than that it works, so it now runs a job on the restored database -- the agent is never told any of it happened, so a restore that lost the host row or its credentials would leave a fleet that looks healthy and can place nothing (removing the fence check from the reconcile loop's *lift* was seen to fail it). And the upgrade check now rolls back: the old binary is started again on the copy the new build took before it migrated. **Written the weaker way first, it passed with nothing restored** -- the published release predates the ledger check that refuses a database written by a newer build, so it comes up on the migrated one and looks healthy, which is the silent start `docs/upgrading.md` warns about and exactly what somebody under pressure would read as a successful rollback. The assertion is the schema now, not the fact that it started, and `docs/upgrading.md` says plainly that rolling back *to* `0.2-beta` is the one case where nothing will stop you. **The dead-socket drill is done, in the half that can be done honestly here**: a second agent joins with its Docker socket missing -- a second machine rather than a reconfigured first one, because the fault's shape in a real fleet is one host down and the others fine. It joins rather than refusing to start, which matters: an agent that would not start would take away the fleet's only way of saying anything about it. The host names the backend it cannot use and the socket it looked for, and the pool with work waiting raises `pool.no_capacity` at error severity, quoting the host's own sentence and a fix. Three rules were removed and seen to fail. The recovery half needs a daemon, and a tier that needed one could not run on every pull request, which is the one thing this tier is for -- it belongs on the reference host the roadmap asks the owner for, alongside the small-filesystem drill, which needs privileges CI does not give. Those two halves are all that remain of the package **ROADMAP v3.0 (19 September 2026):** `done` for what was authorised: five of the six fault drills and the restore-and-rollback drill run on every pull request on amd64 and arm64 (`make test-drill`). The small-filesystem drill and the daemon-stopped-then-restored half are withdrawn. Note that the parenthetical classification on this row is from 6 September; and since the scorecard hardening of 12 September the drill job no longer commits rows to `main` — they go to the job summary and a 90-day `drill-evidence-<sha>` artefact, as [validation/README.md](validation/README.md) says. |
| ZF-303 | Controlled beta and the readiness record | new; owner-gated | `withdrawn` | everything above; the owner's deployment, credentials and second operator | | Size M for the template and helper; the observation is the owner's **ROADMAP v3.0 (19 September 2026):** `withdrawn` with Gate F (ROADMAP section 7). The evidence helper's substance becomes ZF-223, a report per installation. |

## Open items carried over from the improvement plan

| ID | What | Where it goes |
| --- | --- | --- |
| N02 | Runners stuck in `registering` on a dev instance; not reproducible on `main` | The first observation in ZF-303 on a deployment of `main`; `runners.not_progressing` now names which half it is. |
| D16 | The brand descriptor drawn into the wordmark | The owner's; not roadmap work. |

## Log

* 2026-09-19: ZF-005 done in one pull request: the three pages corrected
  against this record, `deploy/marketplace/release.env` repinned to `v1.2.0`
  with `make marketplace-lock` rerun, and the release workflow's tag guard
  made loud -- only the `dev` prerelease is skipped by name now, so a tag
  such as `V1.1.0` fails its run with an error instead of building nothing.
  **Found on the way**: the OpenSSF Scorecard run had failed on every push
  to `main` since CI moved onto the fleet on 18 September, because
  Scorecard's publishing service accepts results only from GitHub's own
  Ubuntu runners and refused `zoomies-linux-x64` as an invalid runner label;
  with the publish refused no SARIF was written, so the code-scanning upload
  failed too, and nothing in the tree said so. The job is back on
  `ubuntu-latest` -- the one workflow that cannot dogfood -- and
  `TestScorecardPublishesFromAGitHubHostedRunner` keeps it there. ROADMAP
  version 3.1 records both.

* 2026-09-19: ROADMAP version 3.0. The owner set a new primary target -- an
  instance operated on a team's behalf, where the team connects its own
  runner hosts -- and withdrew the real-use qualification programme, because
  the product is proving itself by carrying this repository's own CI on a
  Zoomies fleet (#332, #335) and by three full releases in a week (`v1.0.0`,
  `V1.1.0`, `v1.2.0`). This record was read back against the fifty-six pull
  requests merged since 15 September (#293 to #350): ZF-203 and ZF-220 close
  as `done` and larger than written, ZF-202 closes, ZF-210 and ZF-213 move to
  `in_progress` because half of each landed under another name (the answer
  file; the settings export and import), ZF-214 is `implemented` with its
  live qualification withdrawn as a gate, ZF-301 and ZF-302 close on what CI
  runs, and ZF-211, ZF-219, ZF-303 and ZF-402 are `withdrawn`. Rows added for
  ZF-005, ZF-221, ZF-223 and ZF-401 to ZF-404b, which the record never had.
  **Found on the way**: `V1.1.0` is a published full release with no assets,
  because a capital letter defeated the release workflow's `v*` guard; and
  `deploy/marketplace/release.env` still pins `v1.0.0`. Nothing in this entry
  is runtime behaviour.

* 2026-09-14: `v1.0.0` published on 13 September, so ZF-218 proceeded: 218a-d
  implemented in one stacked change and the package is `implemented`. Its
  evidence record, [validation/marketplace-deployment.md](validation/marketplace-deployment.md),
  is written and every row reads `not run`. ZF-219 is now the next unblocked
  work and waits only on a disposable VPS, a DNS name and a GitHub
  organisation. ZF-220's row records which half of it `v1.0.0` carries, since
  the deployment package pins that release. Roadmap item 28 notes a container
  per job on Proxmox from the published runner image, recommending a spike
  before any package.

* 2026-09-12: ZF-004 done in one pull request; the package is `implemented`.
  ZF-207 to ZF-210 added from decision 27 (ROADMAP v2.33), all
  `not_started`; none waits on Gate F, and section 10 of the roadmap says
  where each fits beside Assignment B.

Newest first. One line per event that changed a row.

* 2026-09-12: ZF-206's remaining three pull requests, as one change, and an
  arm64 leg in CI -- both at the owner's instruction to implement Windows and
  arm runners and flag them as needing testing in the beta. That instruction
  is what moved ZF-206 ahead of Gate F, where section 10 had sequenced it, and
  the package's shape follows the roadmap's recommendation (process on a
  Windows host, not Windows containers), recorded as
  [decision 0003](decisions/0003-windows-runners-are-processes-on-a-host.md).
  **arm64 was already built and never run**: every release cross-compiles it
  and four of the five runner images publish it, and the support matrix said
  in so many words that no test had run on it. CI now runs the Go suite and
  the lifecycle drill on GitHub's hosted `ubuntu-24.04-arm`, so a queued job
  becomes a real process on an arm64 machine on every pull request; what is
  still not qualified is a container on arm64 and an arm64 host in a fleet
  anyone kept, and the row says so. **Windows is built, vetted and
  unit-tested, and has not run**: the agent's packages test on a hosted
  `windows-latest` leg, and nothing in this repository has joined a Windows
  host or run a job on one. The work itself is the package's own list: the
  build and the service, the archive and the job object, and the removal
  that retries. Two things worth knowing about how it was done. The Windows
  digests were computed from the archives GitHub serves rather than read from
  the release notes, which this session could not reach; the generator's
  format was kept by feeding it those values, and the runner-version
  workflow re-derives them from the notes on the next bump, so a wrong value
  here fails safe (the download is refused) and is corrected at the next
  release rather than trusted. And the `os=windows` refusal from the first
  pull request is lifted rather than kept, since its reason -- adding a host
  would not help -- stopped being true; in its place the wizard's no-host
  warning names the platform to join, because "label a host" for `os=windows`
  is advice that puts a Linux box in a Windows pool.

* 2026-09-09: RC1 triage reproduced cleanup-error loss and separates the
  failure sources in migration `0022`. At the owner's request the follow-on
  change also closes the process restart discrepancy and timing gaps:
  migration `0023` preserves first-create issue and confirmed cleanup times,
  approval holds no longer inflate observed queue time, and Docker cleanup
  errors cannot be mistaken for success. [rc1-triage.md](rc1-triage.md) records
  the fixes and the decision to proceed without a written owner-qualification
  record. Validation accompanies the pull request.

* 2026-09-09: ZF-201 is done, which leaves ZF-206 as the only Phase 2 package
  outstanding and it is deliberately sequenced after Gate F. The line worth
  keeping is from the last pull request: giving the suite's GitHub fake a port
  so a browser could reach it exposed something no layer beneath the browser
  could -- replacing a private key had a route and no way in, so recovering
  from the commonest credential mistake meant disconnecting the installation
  and losing its pools and runner rows with it.

* 2026-09-09: ZF-103's third pull request merged, which finishes the package
  and Phase 1's last gap. The line worth keeping is about where a number comes
  from: what the Hosts page shows a host has promised away is the scheduler's
  own sum, recorded from the snapshot the pass decided on, not a second
  calculation for the page. A figure that disagreed with the one placement used
  would be worse than showing nothing, because an operator would believe it.
  The reserve is refused rather than clamped when it would leave nothing to
  place on, and `pool.resources_unenforced` says the quiet part about the
  process backend: the reservation holds the room, but nothing enforces it.

* 2026-09-08: ZF-205 is done, which finishes Phase 2's unblocked work --
  ZF-201 waits on the owner's disposable organisation and ZF-206 on Gate F.
  Three findings worth keeping. The Overview's figures changed under the
  operator a second after every page load, because a fetch of `/stats`
  defaulted to a day and a `stats` frame to an hour; they are one constant now.
  A registry-wide label rule has to read descriptors rather than a scrape: a
  vector with no observations reports nothing, so the obvious version passed
  with a `repository` label added. And the load measurement earned its keep on
  the first run by finding that the audit log's action filter scanned the one
  table that is never pruned.

* 2026-09-08: ZF-204 is done -- its last pull request upgrades the last
  published release to this build on every pull request. Two findings worth
  keeping. `curl … install.sh | sh` did not work: GitHub keeps prereleases out
  of /releases/latest, every release here is one, and the redirect the
  installer reads landed on the release index with no tag in it. The installer
  had a syntax check and nothing else, so nothing could have caught it. And an
  agent-upgrade drill was written and thrown away: it passed with adoption
  removed, because the reaping it claimed to prevent sits behind a two-minute
  constant a separate process cannot move. Deleting it exposed the real gap --
  every test of adoption called the function directly, so removing its call
  from the startup path was caught by nothing -- and that is pinned in-process
  now.

* 2026-09-08: ZF-204's supply-chain pull request merged. Three of its seven
  parts were already done -- the plan's reconciliation predates the action
  pinning, Dependabot and govulncheck -- and the finding worth keeping from the
  rest is that the pinning rule was a claim rather than a check: CLAUDE.md said
  CI enforced it and nothing did. It is tested now. The image builds are
  written twice, once per runner vendor, rather than once with a swapped
  action, because a release workflow only runs on a tag: CI cannot tell anybody
  it has been broken until the moment somebody needs a release, so the path
  that works today stays byte for byte what it was.

* 2026-09-08: ZF-204's fourth pull request merged -- a controller copies the
  database before it migrates it. Two things worth keeping. The layout naming
  moved into the store rather than being duplicated, so a copy taken
  automatically is restorable by the command that restores one taken by hand: a
  copy nobody can put back without knowing it is special is not a rollback. And
  running it for real surfaced an interaction nothing had written down --
  `zoomies restore` opens the database it restored, so restoring a backup from
  an older release migrates it, and now keeps a copy of it as it was first.

* 2026-09-08: ZF-204's schema-safety tests merged, finishing its third pull
  request -- the first half of which had landed early in ZF-203, whose restore
  needed the ledger refusal first. The line worth keeping is about the fixture:
  an upgrade test has to start from a database *built* by applying the old
  migrations, not from a current one with its ledger trimmed. Trimming leaves
  the columns the later migrations add, so re-applying them fails on a
  duplicate, and the upgrade being tested is one from a database that never
  existed. The first attempt did that and the test said so.

* 2026-09-08: ZF-204's second pull request merged -- version skew is visible,
  and the two sides that used to disagree about it now share one comparison.
  The line worth keeping is what settled it: skew is a *release* difference,
  not a commit difference. Two builds of one tag are the same release, and
  treating a rebuild as skew is what made the agent warn in its log while the
  Hosts page said the fleet matched. The comparison refuses to order what it
  cannot parse, because a wrong order would send an operator to upgrade the
  wrong side -- and a host ahead of its controller is called out separately for
  exactly that reason. Building it also found that the demo seeded its hosts
  with the literal version "demo", so a demo fleet reported skew on every host.

* 2026-09-08: ZF-204's first pull request merged -- compatibility enforced and
  stated. The line worth keeping is the shape of the answer: a protocol
  mismatch excludes a host from placement the way a cordon does, rather than
  refusing its heartbeat, because a refusal would send every agent in the fleet
  into its re-join path at the same moment -- the outage the upgrade was meant
  to avoid. The same reasoning runs through the rest of it: an agent that
  reports no version at all is not judged, and an unknown task kind now leaves
  its runner alone instead of failing it. It also corrected the plan twice: "an
  agent may lag by one minor release" is not enforceable by anything here, and
  pull request 3's ledger refusal had already landed in ZF-203.

* 2026-09-08: the recovery fence merged, and ZF-203's code is complete -- only
  the owner-run GitHub half of its acceptance is left. Two distinctions the
  work sharpened. Readiness fails while fenced and liveness does not, because
  the container image's health check is `/healthz`: a fenced controller has to
  leave a load balancer's rotation and must *not* be restarted by its own
  runtime, which would achieve nothing and lose the operator's session. And the
  fence stops the fleet acting without stopping it deciding -- the plan is
  still computed and published, so an operator can tell "nothing to do" from
  "not allowed to", which is the one thing a fenced fleet otherwise cannot
  show. The mechanical drill the acceptance names is written and passing, and
  was itself checked by mutation.

* 2026-09-08: `zoomies restore` merged, ZF-203's third of four. Its whole shape
  is one property repeated: every refusal happens before anything is moved. A
  corrupt copy, a backup from a newer release, and the wrong encryption key are
  each otherwise found after the controller is already running on the restored
  data, when the original may be gone and the symptom says nothing about the
  restore that caused it. The fence also found its home in the writing:
  `recovery.fenced` is a row in the restored database rather than a line in
  `zoomies.yaml`, because it belongs to the data -- a copy carried to a second
  machine has to arrive fenced, and that is the case the fence exists for.

* 2026-09-08: `zoomies backup` merged, ZF-203's second of four. Two shapes
  settled in the writing. A backup is one directory rather than a database file
  beside a manifest file -- retention then has something whole to delete, and
  restore will take one argument. And the summary states the encryption key's
  presence or absence on every run rather than only on the dangerous one,
  because the person the sentence exists for is the one who reads "backed up"
  and stops. The manifest itself is the part that cannot be recovered later:
  which build wrote the copy, the ledger that says which release can open it,
  the key's fingerprint, and what that key is needed for -- never the secrets
  themselves.

* 2026-09-08: ZF-203's first pull request merged -- the store primitives and the
  startup guards backup and restore need before there is a command to run. The
  line worth keeping is why the read-only open exists at all: opening a
  database read-write applies every pending migration as a side effect, so a
  command that only wanted to look -- has this install finished, which runners
  does GitHub still hold -- upgraded the schema on its way past, and verifying
  a backup would have changed the thing being verified. The two guards are the
  other half. A database newer than the binary is refused by name, because
  SQLite has no opinion about columns nobody reads and the older binary would
  otherwise come up looking healthy; and a new encryption key is refused over a
  database that holds sealed secrets, because a restore that left the key
  behind is indistinguishable from a first run at the key file and used to
  produce an instance that failed hours later inside a GitHub call. The wrong
  key, as opposed to a missing one, cannot be caught at startup -- a key is
  proven only by opening something -- so it is a problem code instead.

* 2026-09-08: the support bundle merged and **ZF-202 is finished**. It is one
  JSON document from one admin route, and the property that makes it worth
  attaching to a public issue is that it is assembled from the renderings the
  API already serves rather than from the store: every section is something the
  operator could have fetched themselves, so the secret-absence guarantee each
  route already carries is the bundle's guarantee too, and the route is on the
  secret-absence test's own path list so that a section which stops being
  secret-free is caught here rather than in a support case. Two decisions are
  worth keeping. The action is admin rather than viewer, because the weakest
  role that covers a bundle is the strongest role inside it -- it carries the
  settings section, and that has always been admin; a document assembled from
  admin-only material does not become viewer material by being assembled. And
  the byte cap sheds sections rather than refusing the request, explanations
  first because they are recomputable from the jobs beside them and the fleet's
  own shape last because it is what a bundle is for: a short bundle answers
  some questions and a 500 answers none. The same reasoning covers a section
  that will not gather -- it lands in `errors` by name, which was pinned by
  building a bundle with a cancelled context and finding the build, the process
  and the configuration still in it.

* 2026-09-08: the drawer and the CLI stopped reasoning for themselves. Both now
  render `GET /jobs/{id}/explanation`, so the two surfaces cannot give an
  operator different answers to one question, and neither answer is limited to
  what the job row can see. The line worth keeping is where the boundary fell:
  the panel's pool counts stayed, because they are facts about the pool and the
  drawer is where they are read, and only the sentence moved. The first attempt
  dropped both and `jobs.spec.ts` failed on it -- an existing test protecting
  the thing that was not the defect, which is what they are for.

* 2026-09-08: `GET /jobs/{id}/explanation`. The question it answers had two
  answers before it: the drawer worked one out in the browser from the pool's
  live counts, and the CLI could only say "unmatched" -- and neither could see
  the scheduler's own reason for refusing to place a runner, because whether a
  runner can be placed at all is a question about hosts that no count on a pool
  answers. The endpoint returns that sentence where there is one, and the four
  facts around it. The distinction it is built on is `waiting` against
  `blocked`: a fleet that is merely busy clears itself, a pool whose selector
  matches nothing never will, and telling an operator to wait for the second is
  telling them to wait for ever. It also catches a case nothing else did -- a
  job running on a host that has gone quiet, which looks exactly like a healthy
  long job right up until the fleet marks it lost. The repository's own route
  table caught the route being documented and not listed, which is the guard
  working.

* 2026-09-08: ZF-202's test fixture, and what building it found. The suite's
  shared fixture is a fleet with nothing wrong with it -- the demo keeps its
  starting runners young on purpose -- so every page that explains a fault had
  nothing to render and none of them was tested. `ZOOMIES_SEED_STUCK` breaks
  three things in that fleet on request, and a `diagnostics` project runs five
  specs against it. **The fixture earned its keep before it was even finished**:
  a job GitHub is holding cannot be matched, because the claim happens on the
  approval, and the default Jobs view kept unclaimed jobs only while they were
  `queued`. So every held job was hidden from the page, and the explanation
  shipped for one the day before was unreachable in the default view. The
  filter keeps `waiting` too now, for the reason its own comment already gave
  for `queued`. That is the second defect in two days found by opening a page
  rather than by reading it, which is what the acceptance asking for a fixture
  was about.

  A postscript worth keeping: CI then failed the fixture itself. The runners
  were aged against the default five-minute provision timeout, and this project
  runs last in a serial suite, so by the time the spec looked they had been
  failed and replaced. A fixture whose meaning depends on how long the suite
  before it takes is not a fixture; the stuck server runs with a four-hour
  timeout now, and it was re-verified the way the failure happened rather than
  the way the first run passed.

* 2026-09-08: ZF-202's small half is complete. The last of it is the kind of
  job that had nothing said about it at all: one GitHub is holding for a
  deployment review. Every panel in the drawer that explains a wait keys on
  `queued`, and a held job is not queued, so it fell through all of them --
  the operator saw a job sitting there and no reason. It now says it is held,
  how long for, and that the wait for a runner starts when somebody approves
  it. Its queue wait reads "Not queued yet" rather than a number, because the
  time a held job spends is GitHub's and not this fleet's, which is the same
  distinction `Job.EligibleAt` and the timeline already make. **The debt this
  leaves is named rather than carried**: neither this nor the runner page has
  a Playwright pin, because the opt-in fixture the acceptance describes does
  not exist and the demo seed carries no stuck runner, no blocked pool and no
  held job. That fixture is the next piece of ZF-202, ahead of the two large
  ones, so the pins land rather than being deferred a third time.

* 2026-09-08: ZF-202's runner page. A runner stuck in `registering` has one
  symptom and two unrelated causes -- a container that never started is a
  backend or image problem on the host, one that started and never reached
  GitHub is a credential, network or GitHub problem -- and the page could not
  tell them apart. `container_started_at` and `registered_at` are on the view
  and rendered as separate rows, the timeline names its stages rather than
  showing three rows that all say "Registering", and the host's last heartbeat
  is on the runner's page because "is the agent even alive?" is the next
  question and it used to need a different page. **Found on the way**: the
  facts panel labelled `started_at` "Registered", which is what `registered_at`
  is, so a runner whose container came up and never registered displayed a
  registration time. It reads "Workload up" now.

* 2026-09-08: ZF-202's poller visibility. The fallback poller is the safety net
  for a fleet whose webhooks have stopped arriving, and both of its failure
  modes were silent by construction: a sweep that has stopped happening looks
  exactly like a sweep with nothing to find, and an installation standing down
  from GitHub's rate limit looks exactly like one with no queued jobs. Both
  were only in the log, and nobody reads the log of a fleet that appears to be
  fine. `/meta` now carries `poller_enabled` and `poller_last_poll_at`, and
  `poller.stale` and `poller.paused` are on the problems list. **The plan's
  own parenthetical was stale**: it asked for both entries "attributed to the
  whole poller because the pause is fleet-wide until ZF-101 changes it", and
  ZF-101 landed a fortnight of commits ago -- the hold has been per
  installation since #100 -- so `poller.paused` names the organisation and
  targets the installation instead. **And the mutation pass found a gap of the
  exact shape ZF-103a's review found**: the line that writes the stamp could be
  deleted with the whole suite green, because the tests set the field directly.
  It is now written at the end of `pollOnce`, where every existing poller test
  drives it, and one test pins that a completed sweep moves it.

* 2026-09-08: ZF-202's first two small fixes. Both are about a diagnostic
  telling the truth when something else has already gone wrong. The problems
  drawer returned a 500 if any single query behind it failed, so the page an
  operator opens *because* something is wrong was the page that would not load
  -- and an empty drawer reads as a healthy fleet, which is the one thing it
  must never say by accident. Each section is now gathered on its own and
  `controller.problems_partial` names what is missing. The second is a
  disclosure: `capacity_demand.signing_secret` is what proves a notification
  came from this controller, and `zoomies config print` printed it in full for
  as long as that feature has existed, because `blankSecrets` is a
  hand-written list and nobody notices a field missing from a list. It is
  blanked, and the test that guards it no longer reads the list: it walks the
  configuration, plants a value in every secret-shaped field and fails naming
  the one that leaked. The rest of the small half needs an OpenAPI change and
  both generated clients, and the two new things -- the support bundle and the
  job explanation -- are untouched.

* 2026-09-08: ZF-103b, the admission half, merged. Placement now costs
  something: a runner is charged the pool's `resources` against what its host
  reported, and a host that cannot cover the charge takes no more work however
  many slots it has left. The acceptance case the package named holds -- a
  32 GB host admits eight 4 GB runners and refuses the ninth -- and so does the
  one that decides whether it is safe to ship, which is that a fleet of pools
  with no limits admits exactly what its slot counts admitted before. That
  second one is what the fallback share is for, and it is also where the
  discipline earned its keep: the share could be deleted with the whole suite
  green, because a pool charged nothing and a pool charged one slot's worth are
  both bounded by slots. What separates them is a host carrying both kinds at
  once, and that test was written because the deletion passed. Free disk is a
  gate rather than a budget and is charged only for the runners a pass adds:
  the measurement already contains what the runners already there have written,
  and charging it again would count the same bytes twice. The verifier's
  first finding is closed -- the reservation filters on `State.Live()`, so a
  failed runner stops being charged for, exactly as the slot count does. **The
  third pull request is still open**, and with it the reserve's route: until
  it lands, the floors (512 MB of memory, 2 GB of disk) are the only thing
  holding anything back.

* 2026-09-08: ZF-206's first pull request, the severable one. It adds no
  behaviour: it stops three places implying Windows support. `runnerAsset`'s
  refusal said "actions/runner ships for Linux and macOS", which sends an
  operator looking for a GitHub gap that is ours -- `win-x64` and `win-arm64`
  have shipped for years, and what is missing is on this side: no agent binary,
  a `.zip` nothing unpacks, no digests to verify it against. A pool whose
  `host_selector` asked for `os=windows` was accepted and then matched nothing,
  so a fleet with no Windows host reported itself short of capacity for a
  platform it has never had; it is refused now at create and at the wizard's
  review step, with a reason that says adding a host would not help, and the
  `os=windows` example is out of `docs/hosts-and-pools.md`. Both rules were run
  with the rule removed and seen to fail. **The rest of ZF-206 is not
  authorised**: decision 26 has not been taken and section 10 sequences the
  other three pull requests after Gate F.

* 2026-09-08: ZF-206, Windows runners, added at the owner's request, with
  decision 26 to choose its shape before any of it is written. The reason it
  needs a decision rather than a start: `process` on a Windows host is the
  small path and it cannot give a job a container, so the ephemeral guarantee
  becomes a fresh work directory and a single-use registration on a machine
  that keeps its state -- the weakening the `process` backend already carries
  on Linux, which every page claiming "the container is destroyed" would have
  to stop claiming for those pools. Windows containers keep the guarantee and
  cost a second image catalogue, a second build matrix and images in
  gigabytes. Sequenced after Gate F either way, because the support matrix
  only moves a row right when a test runs on the thing, and a platform added
  while the project is proving the one it has widens the gate rather than
  passes it. **What the survey found on the way is that the vocabulary shipped
  without the platform**: `naming.OSWindows` is legal, `windows` is in
  `store.ImplicitLabels`, and `docs/hosts-and-pools.md` used `os=windows` as a
  pool example, so an operator can declare a platform that matches nothing and
  is told nothing about why. That is the package's first pull request, and it
  is severable from the decision.

* 2026-09-08: Assignment A is finished. Its last stack merged as #120, and
  this pass reconciled both documents against the code rather than against
  each other -- an audit of every checkable claim in them, each correction
  confirmed against the file it describes before it was written. What it found
  is the shape of the thing this record exists to prevent. `docs/architecture.md`
  still told a reader that an agent adopts a workload "when a task arrives for
  it, and at no other time", two pull requests after ZF-102 made it adopt at
  start-up as well -- a session trusting the invariants table would have
  deleted the start-up pass and destroyed every job on the host at the next
  restart. The measurement contract still said `Job.EligibleAt` and
  `Runner.CleanedUpAt` were missing and would land with ZF-101 and ZF-105,
  when all three of Gate F's timestamps now exist; that is the same document,
  and the same kind of error, that cost this assignment a package. Decision 15
  and the Phase 1 preamble had ZF-103's halves the wrong way round from how
  they shipped. The qualification table listed every unproven configuration
  except the default one, so Docker read as the proven case. And ZF-105's row
  called its third verifier finding stale for want of a poll-interval channel:
  `TaskBatch.Backoff` is on the wire, published in the OpenAPI document and
  waited on by the agent, and no controller path sets it, so that finding is
  open. Section 12 now carries one instruction per assignment with Assignment
  A's marked spent, because a fresh session handed this document would
  otherwise have been told, in the imperative, to do work that finished 158
  commits ago.

* 2026-09-08: the About page carried the wrong dog. The logo component's size
  ladder had three rungs where the brand system has four, so the original
  circular mark -- the primary standalone artwork -- was rendered nowhere in
  the product, and the one identity slot with room for it fell through to the
  head/swish, which `ASSET_MANIFEST.json` records as a reconstruction rather
  than supplied artwork. The assets were already in `web/public/brand/` and
  referenced by nothing. The card also never said what Zoomies is, which the
  panel header does not answer. Merged as #122.

* 2026-09-07: ZF-103a, the reporting half, merged as #115. An agent measures
  its host and says so on join and on every heartbeat: CPUs and memory from the
  daemon that will actually run the runners rather than from the agent's own
  cgroup -- a containerised controller, which is the usual deployment,
  described a sixty-four-core machine as a two-core one, because a runner
  started through Docker is a sibling on the machine and outside that cgroup
  entirely -- and the disk behind the work directory, which is the resource
  that runs out first and said nothing when it did. Migration `0017` adds the
  disk observations and the operator's reserves; the heartbeat writes the
  observations and never the reserve, and a re-join no longer quietly drops it.
  Free space moves every beat, so it is written only when it has moved past a
  tolerance, or a fleet costs one row write per host per heartbeat for a number
  that is never twice the same. Three test gaps its own adversarial review
  found were closed in #118, where the disk figures also reached the Hosts
  page. **The admission half did not ship**: nothing in `internal/scheduler`
  reads a resource, so the figures are reported and displayed and no placement
  decision consults them.

* 2026-09-07: the Gate F readiness note, which is what section 10 says ends
  Assignment A. Phase 1 is complete as far as Assignment A carries it --
  ZF-101, ZF-102, ZF-104 and ZF-105 are done, and ZF-103 has landed 103a, the
  reporting half, which is the only part of it section 10 sequences into this
  assignment. **ZF-103 stays `in_progress`**: placement is still the slot
  count, and nothing yet refuses a host for its CPUs, memory or disk. The note
  says which of Gate F's eight targets can be measured at all today.
  **Writing it found a code gap inside Assignment A's own
  scope, and closed it**: the scheduling-latency target had no start. The
  measurement contract said `Job.EligibleAt` landed with ZF-101 and ZF-101 was
  marked done, but what ZF-101 delivered was `scheduler.Eligible` as a function
  -- the definition of eligibility, not the moment it was reached. The column
  exists now (migration `0019`), stamped the first time an enabled pool claims
  the job's labels and never moved afterwards, so the interval no longer starts
  at `queued_at` and no longer charges the platform for a job that was
  ineligible, held for a deployment review, or waiting on a scale-up delay it
  was configured to wait for. The end of that interval (`Runner.TaskIssuedAt`)
  and both ends of cleanup convergence (`Runner.CleanedUpAt`) already existed.
  The series that consumes it is ZF-205's, in Assignment B. The note also records that the largest
  untested surface is the Docker backend at runtime -- nothing in this
  repository has ever started a container -- and that the drill record is
  written to a file no run ever commits, so the history its own comment exists
  for does not accumulate.
* 2026-09-07: ZF-105 done. Five of its six pull requests had already landed in
  earlier sessions -- the cleanup record, sidecars, the cache guard, the drain
  timeout and the rate-limit hold are all in the code, which the package's own
  status had not caught up with. What was genuinely outstanding was one of the
  verifier's extra findings for the second: a work directory that leaks when its
  workload goes away out of band. **Half of that finding is not reachable**: the
  Docker backend learns the directory from the container's label, so losing the
  container loses the path, but nothing sets `Spec.WorkDir`, so the backend
  never creates one and there is nothing to leak. The process backend's half is
  live and is fixed here. Create makes the runner's directory and clones the
  tools tree into it -- hundreds of megabytes -- before it writes the metadata,
  and `List` skipped any directory whose metadata would not read, so an agent
  killed in that window left the tree on the host with nothing that would ever
  look at it again. Such a directory is now listed as a workload that is gone,
  and the agent's existing orphan path removes it under the same grace and the
  same "only after a successful poll" rule as a container nothing claims. It is
  reported only once nothing has written to it for fifteen minutes, which is
  what tells an abandoned clone from one in progress: the clone touches the
  directory with every file it lays down. Both halves are pinned -- skipping the
  directory fails the test, and so does dropping the grace.
* 2026-09-07: ZF-104 done, in four pull requests rather than five. The package
  was seventy percent "tests that already exist", so the first work was finding
  out which of its claims anything actually held up: ten surveyors over the nine
  assertions of PR1 found that **PR2 was already implemented** -- the log relay
  has bound to the authenticated host since ZF-102 -- and that its test's
  indistinguishability check was `err.Error() == ""`, which is never true for a
  non-nil error. The agent route walk covered three of five routes, leaving the
  task poll and the log relay with no negative test; the scoped-token gate had
  one test across sixty-six routes, and that one was really testing a handler's
  second check on its body; secret absence was proved for seven admin reads on
  the success path only. All of it is now walked, with a coverage guard on each
  table so a new route or action that arrives without a row fails rather than
  ships. Two departures from "tests only", both stated in the pull requests: a
  body over the limit now answers 413 rather than 400, which is what the webhook
  endpoint has always answered for the same condition, and the test harness
  captures the controller's log because the secret-absence claim names it and
  there was no seam. **Two assertions were deleted rather than shipped**: a
  needle matched against marshalled JSON cannot match a PEM whose newlines the
  encoder escaped, and "the log relay is exempt from the body limit" was
  unfalsifiable where it was first written, because the relay answers 404 before
  it reads a byte. PR5's proxy bug was real: `X-Forwarded-For` has always been
  believed only from a trusted proxy and `X-Forwarded-Proto` was believed from
  anyone, in three places, which let a caller decide whether an `https://` Origin
  counted as same-origin and whether the response carried HSTS. One deliberate
  deviation: the package says to reuse the login limiter on the join route, and
  it got a counter of its own at the same setting instead -- sharing would let an
  attacker hammering `/agent/join` lock the administrators out of the page they
  would use to stop it. PR3 and PR4 close the last two: a live stream re-checks
  its credential on every heartbeat and ends when it no longer stands, and a
  Playwright spec proves a name carrying markup is text and a runner's output
  cannot retitle the page, clear it or open a dialog. **Not tested**: the log
  viewer's link predicate (the terminal draws to a canvas and there is no UI unit
  runner) and the client's `end` listener (the shipped heartbeat is twenty
  seconds); each is named in the pull request that carries it -- the listener
  in #119, the predicate in #120 -- rather than glossed.
* 2026-09-07: two reported UI defects, both of which turned out to be one layer
  deeper than the page. The Overview's tiles counted every job GitHub reported
  because `StatsSince` had no scope at all, while the panels below them already
  filtered and already carried the switch -- so the two halves of one page were
  answering different questions. The usage report had the same hole, and its own
  comment acknowledged the hosted-runner jobs as the reason for a `COALESCE`.
  Both now share `managedJobSQL()` with the Jobs page, so a repository's
  runner-hours and its job list are about the same jobs. The Overview keeps a
  switch; usage does not, because it answers what this fleet consumed and
  somebody else's hosted runner has no answer to contribute. Both scopes travel
  in one stats payload rather than behind a query parameter, because the same
  numbers arrive over the event stream and that is one frame for every viewer;
  migration `0018` records both on every sample for the same reason.
* 2026-09-07: ZF-301b, the drill tier, and the first time anything in this
  repository has run the product as an operator gets it: the built binary as a
  controller, a second copy of it as a remote agent that joined with a join
  token, and a real workload on the machine running the test. GitHub is the
  in-process fake; the backend is `process` with a stub runner staged on disk,
  which the backend accepts because it skips its download when the tools tree is
  already there. So the tier needs no credentials, no Docker daemon and no
  network, which is what lets the lifecycle drill run on every pull request.
  It also had to learn that the fallback poller only finds *queued* jobs, so the
  drill drives the fake and delivers signed webhooks -- which is what puts the
  timing in the drill's hands, and what ZF-302's fault drills will need.
  The finding worth keeping: the obvious version of the lifecycle drill passed
  with `deleteRegistration` commented out. The reaper's first sweep is a minute
  after the controller starts, which is where the drill was looking, so it was
  testing the backstop. It cannot be sharpened in place either -- an ephemeral
  runner that finishes its job exits by itself and nothing of Zoomies' deletes
  the registration, correctly, because real GitHub removes a just-in-time runner
  itself and the fake does not model that. The sharp assertion is a second
  drill, on an operator removing an idle runner, where GitHub tidies nothing
  and Zoomies must; it fails in seventeen seconds with the deletion removed.
  Both drills name the runner they assert about rather than counting what is
  left, because while a job is queued the scheduler is right to replace a
  removed runner immediately. Each run appends its row to
  `roadmap/validation/drills.md` with timings, so a pass that quietly becomes a
  different pass is visible. **Not done here**: the six fault drills (kill the
  controller mid-job, kill the agent, a dead Docker socket, a rate limit, a
  failing JIT endpoint, a small filesystem) and the Docker backend's own
  runtime qualification, which needs a daemon this container does not have.
* 2026-09-07: ZF-301a, the harness's honesty. The end-to-end harness could
  not have failed: a skip is exit code zero, so a run that had never once
  talked to GitHub reported the same green as one that had run the scenario --
  and it looked for the `gh` CLI in the middle of the scenario, after it had
  already made an installation and a pool on a real organisation, then skipped
  and left them there. Its waits totalled twenty-seven minutes behind a
  twenty-minute Makefile timeout, so it could never reach its own last
  assertion; that assertion asked Zoomies whether Zoomies had cleaned up, which
  is not evidence, and the README described a cleanup-on-failure that did not
  exist. Now: preflight before anything is created, a required mode where a
  missing prerequisite is `blocked`, a JSON result per scenario, a ledger
  outside the temp directory with a sweep for what an earlier crash left, the
  orphan and container checks asked of GitHub and the Docker daemon, per-run
  labels, and a `verify` command so the gate reads the records rather than an
  exit code. The budget guard carries no build tag, so it runs in ordinary CI
  rather than only where credentials exist -- it reproduced the timeout defect
  against the real Makefile before the fix. **No real run has happened**: this
  slice makes the harness capable of failing honestly, and the credentials it
  needs are the owner's (ZF-301c).
* 2026-09-07: ZF-101 is done. Its third pull request landed with ZF-102's, and
  the row had been left `in_progress`; all three are merged and the acceptance
  holds.
* 2026-09-07: ZF-102's fourth and last pull request, and the package is done.
  Four things. A runner given up as lost whose host comes back with the
  container still running is no longer dropped as an illegal transition: the
  terminal row stands, because the fleet has already told an operator and the
  job's timeline that the runner was gone, but the message is corrected, a new
  `runner_returned` timeline entry withdraws the `runner_lost`, and the
  workload is left alone while a job is still on it and removed once none is.
  Before this it ran for ever -- the reaper only takes untracked workloads and
  the retention sweep only finished ones, so nothing in the system would ever
  have collected it. `task_issued_at` on the runner row keeps the one fact the
  in-memory queue must not lose across a restart, stamped again on every
  redelivery so the provision timeout is not counted from an attempt the host
  never received. `host.duplicate_agent` sees two agents sharing one host id by
  the one thing they cannot hide -- handing a session back and forth, which a
  single agent cannot do because it never reuses one -- and reports without
  refusing, per decision 14. The restart-boundary table covers seven boundaries
  and would fail on all seven if a late result were dropped. Two departures
  from the package text, both deliberate: the code is `host.duplicate_agent`
  rather than `hosts.*`, to sit with `host.unhealthy` and
  `host.cordoned_with_work`; and the session travels as a header rather than a
  field on three messages, because the runner report's body is a bare JSON
  array by design and one middleware sees every authenticated agent call.
* 2026-09-07: ZF-102's third pull request, the package's one genuine defect.
  A fresh agent knew nothing, and the reconciler removes what nothing claims,
  so every job running on a host was destroyed two minutes after its agent came
  back -- on the default single-VM install, that is every controller restart.
  The fix is small because `adopt` already existed for the redelivered-create
  path; what was missing was calling it for everything on the host before the
  loops start.
  
  Adoption alone would have traded one fault for another: an agent that adopts
  everything can no longer tell litter from live work, so a workload whose
  runner the controller deleted while the agent was down would leak forever.
  Hence the second half, and the rule behind it -- the agent never decides on
  its own that something is litter, because it cannot tell "the controller
  deleted this" from "I have forgotten it", and only one of those costs
  somebody a job. The controller names the runners it has no row for, and only
  those are released. A runner belonging to another host is deliberately never
  named: that is a different fault, and answering it would invite one host to
  remove another's work.
  
  `TestWithoutAdoptionARestartWouldReapALiveRunner` is kept deliberately: it
  asserts the old behaviour still destroys the fixture, so the test above it
  cannot quietly stop testing anything.

* 2026-09-07: ZF-102's second pull request, one controller per database. Its
  first half was already merged: the plan says "the log relay takes the
  authenticated host id", and `AcceptLogStream` has compared the reporting
  host against the stream's owner since PR #90 closed the auth and web-UI
  security review, with a test that an intruding host is refused and told
  nothing it did not already know. The plan predates that merge. What was
  outstanding is the lock and the lease, and they are two different guards on
  purpose: an advisory lock on a file beside the database catches a second
  process on this host and is dropped by the kernel however the process dies,
  while the lease row catches the restored copy on a shared filesystem or the
  controller on another machine, which no lock here can see. A lost lease is
  reported and never cleared, because there is no state this process reaches
  on its own that makes two schedulers safe again.

  The lease nearly shipped with a defect worth recording, because the next
  slice touches the same restart path: the service unit is `Restart=always`,
  so a controller that is killed returns within seconds under a new holder id,
  finds its predecessor's lease renewed moments ago, and refuses to start --
  a restart loop for the whole lease window after every crash. A lease naming
  this same host is therefore reclaimed at once. That is sound only because
  the caller already holds the file lock, which proves nothing else on this
  host has the database; the cross-host case, the one the lease exists for,
  is untouched.

* 2026-09-07: ZF-101's third pull request, and the package's last. The runner
  group falling back to GitHub's Default was a log line; it is now a standing
  warning, because Default is the group every repository the installation
  covers can reach, so a pool that asked to be fenced into a group and quietly
  was not is running its jobs somewhere wider than its operator asked for. Two
  things the plan did not name but the code needed: the three fallbacks say
  which one happened, because "the group is not there", "GitHub would not say
  which groups exist" and "this is a repository, and groups are an
  organisation's" have three different fixes; and an unresolved name is
  deliberately not cached, so that the warning clears by itself on the next
  create once an operator makes the group, at the cost of one API call per
  runner on a pool that is misconfigured.

* 2026-09-07: ZF-101's second pull request, the poller made per-installation.
  The freshness question is answered from the delivery's repository, as the
  first pull request's record said it would have to be: a delivery credits the
  installation that owns the repository named in it, never the one whose secret
  verified it, so an installation whose secrets have drifted cannot be told to
  stand down on its neighbour's traffic. The rate-limit hold became a map and
  the sweep continues past a held installation. One thing worth knowing for the
  later slices: go-github caches an exhausted quota on the client, so a fake
  that sets the rate-limit headers globally rate-limits every installation at
  once; a test that wants one installation held has to use the 429 secondary
  form. The first draft of that test did not, and passed for the wrong reason.

  Two deliberate departures from the package's wording, recorded because the
  plan is the contract and the next reader should not have to rediscover them.
  The plan says "`LastAcceptedDeliveryAt` takes an installation"; instead it is
  left alone and a sibling added, because `Controller.Start` and the
  `pollingOnly` flag that ZF-202 keeps fleet-wide both still need the fleet-wide
  answer, and one query per sweep beats one per installation. The sibling is
  `InstallationsFreshSince`, bounded by the caller's own cutoff: asking for the
  last delivery per installation groups over every accepted row inside the
  retention window, which measured as a full scan of a week of history every
  thirty seconds, where the cutoff makes it an index range. And the plan says
  the tests need "two fakes"; one serves, because the fake's error injection is
  matched on the request path, so a repository-target installation can be
  rate-limited by its own path while its neighbour is not.

  One asymmetry the query inherits from the attribution rule, worth knowing
  before somebody reads it as a bug: where an organisation installation and a
  repository installation both cover a repository, only the repository one is
  credited, so an organisation whose only traffic is for a repository-scoped
  sibling looks silent and keeps being polled. That is the conservative
  direction -- poll rather than stand down -- and it is the same precedence
  every other part of ZF-101 uses.

* 2026-09-07: ZF-101's first pull request, the installation boundary. Three
  decisions taken inside decision 13's frame and recorded here because the
  later pull requests inherit them: a job is attributed to the installation
  covering its **repository**, never the one whose secret verified the
  delivery, which verification deliberately allows to be another's;
  `webhook_deliveries.installation_id` holds the **verifying** installation,
  because that is the one fact about a delivery which cannot be recomputed
  afterwards, and ZF-101's second pull request must therefore derive
  per-installation poll freshness from the repository rather than from that
  column; and the migration clears `matched` and `pool_id` on waiting and
  queued rows it re-attributes but leaves in-progress rows alone, because a
  running job's pool is the record of where it actually ran. The Jobs page
  carries the reason it can derive from the row itself -- the repository no
  installation covers -- and the cross-installation sentence reaches the
  operator through the problems drawer, which is where the scheduler's plan
  is read; a per-job reason on the Jobs page would be a new API field and is
  not in this pull request.

* 2026-09-06: ZF-102's first pull request, the invariants written down and pinned; the package is `in_progress`.

* 2026-09-06: ZF-003 done in one pull request; the package is `implemented`.

* 2026-09-06: ZF-002's code slice done; the package is `implemented`.

* 2026-09-06: ZF-105's first slice, the listing-failure defect in the agent
  reconciler, fixed with a test; the package is `in_progress`.

* 2026-09-06: every Phase 1 to 3 package classified from the package-by-package
  review (12 mappers, 12 adversarial verifiers, one document critic); ZF-003
  added; ZF-001 implemented; two Phase 0 defects fixed in the same pull
  request (the stuck-runner tie-break, the compatibility paragraph).
* 2026-09-06: record created from the reconciliation of the follow-on roadmap
  against `main` at `6d12a72`.

## September competitive improvements

Planning rows added 13 September 2026. No runtime implementation is claimed.
Existing package status above remains unchanged; ROADMAP.md section 10 controls
ordering. ZF-210a is bootstrap/readiness (ZF-203 and ZF-207); ZF-210b is
export/purge (also ZF-209). The parent completes only when both are accepted.

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-211 | Qualification and reproducible performance | extension | `withdrawn` | ZF-002, ZF-301/302 | Planning only | [September comparison](competitive-review-2026-09.md); acceptance in [ROADMAP.md](../ROADMAP.md) section 7 **ROADMAP v3.0 (19 September 2026):** `withdrawn`: the measurement half. The documentation half — the Docker row and `docs/index.md` understate what runs since CI moved onto the fleet, and `docs/proxmox.md` overstates it — is ZF-005, done 19 September 2026. |
| ZF-212 | Cache performance and bounded retention | extension/new | `not_started` | ZF-105, ZF-205 | Planning only | [September comparison](competitive-review-2026-09.md); acceptance in [ROADMAP.md](../ROADMAP.md) section 8 **ROADMAP v3.0 (19 September 2026):** demand-gated; reduced to recipes for the caches that exist. The S3-compatible cache storage half is not authorised. |
| ZF-213 | Pool configuration as code and portable migration | extension | `done` | ZF-201, existing API | Planning only | [September comparison](competitive-review-2026-09.md); acceptance in [ROADMAP.md](../ROADMAP.md) section 8 **ROADMAP v3.0 (19 September 2026):** `in_progress`: the settings half landed under another name — `GET /settings/export` and `POST /settings/import` (#297) with a version field, a per-key plan, dry run, skip and idempotent apply. The pool half remains and is the package now; the reusable-workflow and matrix clause is met by the migration planner's stated refusal. **Record, 23 September 2026:** `in_progress`: the pools half is open in [#442](https://github.com/eyupio/zoomies/pull/442), not merged — `GET /pools/export` and `POST /pools/import` modelled on the settings pair, planned by pool name as create, change, unchanged or refused, dry run, skip, one change or none, with the CLI verbs and the Pools page dialog. Pool environment values are never carried; their names are. **Record, 24 September 2026:** `done`: [#442](https://github.com/eyupio/zoomies/pull/442) merged with the export and import pair, the CLI verbs and the Pools page dialog; the export also carries `no_default_labels` from [#459](https://github.com/eyupio/zoomies/pull/459). |
| ZF-214 | Shared provider lifecycle, Proxmox first (214a–c) | extension/new boundary | `implemented` | 214a: ZF-210a contract; 214b: ZF-207/208, ZF-210a, ZF-404 ownership/drain and recovery gates; 214c: qualified 214b | [PR #263](https://github.com/eyupio/zoomies/pull/263) | Owner selected Proxmox on 13 September 2026. 214a complete: the contract, its fake and the conformance suite, the `providers`/`machines` schema and eleven-state machine, the durable reconciler on its own loop, the pure machine-demand calculation, and the capacity signal extended to fire when no host can run a pool at all. 214b complete: the Proxmox VE client, ownership marks, preflight, discovery, the provider itself, the REST and CLI surface, the Providers and Machines pages, and the runbook in [docs/proxmox.md](../docs/proxmox.md). **Live qualification is written but not run, and is not claimed.** The 20-cycle procedure version 2 wrote into [ROADMAP.md](../ROADMAP.md) (withdrawn as a gate in version 3.0, section 7) is implemented as an `e2e`-tagged harness behind `make test-e2e-proxmox`, whose budget, ledger, preflight, inventory check and evidence record are compiled and tested in ordinary CI; every row of [proxmox-qualification.md](validation/proxmox-qualification.md) still reads `not run`, because it needs a disposable cluster this work has no access to. Fixture success is not qualification. **214c not started**: it needs demonstrated demand and live access to a second platform. Optional GARM experiment untouched and non-blocking **ROADMAP v3.0 (19 September 2026):** `implemented`: 214a done, 214b implemented; the twenty-cycle live qualification is withdrawn as a gate (the harness stays, and a fleet with a cluster is asked for its rows); 214c demand-gated. `docs/proxmox.md` says 'qualified' where the record says not run; ZF-005 corrected it on 19 September 2026. |
| ZF-215 | Capacity fallback and scheduled readiness | extension | `not_started` | ZF-208; qualified ZF-214b for provider fallback | Planning only | [September comparison](competitive-review-2026-09.md); acceptance in [ROADMAP.md](../ROADMAP.md) section 8 **ROADMAP v3.0 (19 September 2026):** demand-gated; reduced to a time-bounded `min_runners` with a timezone and an opt-in `fallback_to` list. |
| ZF-216 | Staged platform, GPU and VM coverage | validation/new | `not_started` | ZF-211; lifecycle gates for new backends | Planning only | [September comparison](competitive-review-2026-09.md); acceptance in [ROADMAP.md](../ROADMAP.md) section 9 **ROADMAP v3.0 (19 September 2026):** the validation halves are withdrawn; GPU admission and a disposable-VM backend survive as demand-gated product work (ROADMAP section 9). |
| ZF-217 | GitHub scale-set integration assessment | design | `not_started` | ZF-211 | Planning only | [September comparison](competitive-review-2026-09.md); acceptance in [ROADMAP.md](../ROADMAP.md) section 9 **ROADMAP v3.0 (19 September 2026):** demand-gated (ROADMAP section 9). |

## One-click deployment foundation

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-218 | VPS marketplace one-click deployment foundation (218a-d) | new | `implemented` | the published `v1.0.0` release | Claude Opus 5, `high`; one session, stacked per slice | `v1.0.0` was published on 13 September 2026, which is the baseline the package is written against, so 218a-d proceeded. All four slices merged together: `deploy/marketplace/` carries the pinned release contract and its generated image lock, the provider-neutral cloud-config and its renderer, the non-interactive answer template, the first-boot script and the certificate holder; [docs/marketplace.md](../docs/marketplace.md) is the operator and partner guide and `SUPPORT.md` the escalation boundary. Tests in `internal/docs` run the real bootstrap's rendering half and assert the answer file it writes is one `zoomies init` accepts, that each of the three certificate arrangements produces a listener and a holder that agree, that the rendered cloud-config carries every file the instance will look for, and that the image lock still covers the runner catalogue. **No deployment has been run.** Every row of [validation/marketplace-deployment.md](validation/marketplace-deployment.md) reads `not run`; rendering that is checked by tests is not a deployment that worked. **ROADMAP v3.0 (19 September 2026):** `deploy/marketplace/release.env` still pins `v1.0.0` while `v1.2.0` is current; the repin is ZF-005's third item, done 19 September 2026. |
| ZF-219 | One-click verification and friendly-provider pilot readiness | validation | `withdrawn` | implemented ZF-218; a disposable VPS, DNS and GitHub resources; an authorised pilot provider | Planning only | Starts now that ZF-218 is implemented. Needs resources this work has no access to: a pristine Ubuntu 24.04 VPS, a real DNS name and certificate, and a GitHub organisation to connect. Acceptance in [ROADMAP.md](../ROADMAP.md) section 7. **ROADMAP v3.0 (19 September 2026):** `withdrawn` (ROADMAP section 7); the rendering tests and the evidence template with every row `not run` stay. |

## Resource-aware allocation for release review

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-220 | Resource-aware host allocation and pressure admission | extension | `done` | ZF-103, ZF-202 | Codex, one implementation session; independent review pending | Implementation and tests present in this PR: Linux usage sampling and optional heartbeats, receipt-time validation, shared pending-start accounting, proportional CPU/memory placement, pressure holds and recovery, host-card explanations and monitoring gauges. Go short and race tests passed in every package except the Tailcat private-connection test; unchanged `main` at `3cea153` fails too, and a direct probe confirms this workspace refuses netlink access (`operation not permitted`). `make lint`, staticcheck v0.8.1, govulncheck v1.7.0, Svelte checks, 18 UI unit tests and the UI build passed. MkDocs passed `build --strict` with the hash-pinned dependencies; rendered guidance, sitemap, llms.txt and badge checks passed. UI, architecture, troubleshooting, upgrade and contributor documentation is current. Desktop/mobile Playwright coverage is added; local Chromium downloads timed out or returned HTTP 502, so execution awaits CI. Review, CI and live fleet qualification remain outstanding; the owner authorised the GitHub commit and PR on 13 September. No release publication or live infrastructure changes. **What `v1.0.0` carries, recorded here because the deployment package pins that release:** the tag sits at the merge of #260, so it contains the first slice (#258) and none of the follow-ups. The pressure holds, the default runner limits, the utilisation chart and the machine loop waiting out a hold rather than buying machines for it are on `main` after the tag. A marketplace instance therefore runs the release without them until the package is repinned. **ROADMAP v3.0 (19 September 2026):** `done`; the live-fleet qualification clause is withdrawn. Since 13 September: the throttle ladder (`0029`), host history (`0032`), reserves that scale with the machine and a stranding edit answering 409 (#302, #311), a busy daemon retried and then avoided (#338), `agent.bootstrap_cpu_grace` (#343), the docker-in-docker share (#336) and elastic CPU (#350). The session cell is from the first slice. |

## A status view for the unauthenticated audience

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-222 | A status view for the audience that cannot sign in | extension | `implemented` | owner decision 29; ZF-207's problem split; ZF-202 | Claude Opus 5.5, `high`, one session, branch `claude/roadmap-status-view-p03zh1` | **Implemented, as ROADMAP section 8 specifies it, with decision 29 written as adopted.** `status.mode` (`off` by default, `authenticated`, `public`; `ZOOMIES_STATUS_MODE`; platform-scoped, applied per request) gates `GET /api/v1/status`, `GET /status` and `GET /status.svg`, and while it is off all three answer 404 to everyone, signed in or not (`TestTheDefaultLeavesEveryStatusRouteA404`). The projection is `controller.ProjectStatus`, a pure function over the problems list and the Overview's numbers that reads only the fleet half of ZF-207's audience split (`Audience.For(false)`), so a platform finding cannot reach it (`TestTheStatusCannotCarryAPlatformProblem`): `state` from the worst fleet severity, `since`, `version`, `queued`/`running` as `none`/`few`/`many`/`backed_up`, median and p95 wait in whole minutes, `reasons` of `code`, `severity` and `since` only, one per code, and `explanations`, the fixed public sentence per code. Every fleet-audience code has a public sentence and no platform code has one (`TestEveryFleetCodeHasAPublicSentence`); the sentences sit in a new "What the status page says" table in `docs/problem-codes.md`, held to `publicSentences` word for word in both directions by `internal/docs` (`TestEveryPublicSentenceIsOnTheProblemCodesPage`). **The fixture-name test**: `TestTheStatusNamesNothingAndTellsTheFourWaitsApart` builds each of the four waits for real on a fleet whose installation, pool, host, repository, job, workflow and runners are named distinctively -- no capacity (`pool.no_capacity`), unmatched labels (`jobs.unmatched`), a lost permission (`installation.unhealthy`), a webhook secret that stopped verifying (`webhook.rejected`) -- plus a busy fleet with runners, searches each body for every name and ID read back from the store, and asserts each wait carries its own code and none of the other three's; `TestTheStatusBodyNamesNothingInTheFleet` repeats the search over HTTP, and `tests/status-page.spec.ts` searches the rendered `/status` page for every pool, host, installation, runner, repository, workflow and job name of the seeded diagnostics fleet. `public` raises `status.public` (warning) and, on a non-loopback bind with `server.tls.mode: off`, `status.public_no_tls` (error, naming `server.tls.mode`, `server.bind` and `status.mode`), with rows in `docs/problem-codes.md` and a section in `docs/security.md` (`TestTheStatusModeRaisesWhatItCosts`). The page is its own document, `web/status.html`, built by a second Vite pass (`vite build --mode status`) with its own 30 KB budget (18.2 KB); it polls every thirty seconds and never opens `/api/v1/events`, and the app shell stays at 97.2 KB gzipped, exactly what it was. Its three states map onto `--z-danger`, `--z-pending` and `--z-idle` with their shapes, recorded in `docs/ui-guidelines.md`. `/status.svg` is a self-contained badge on the `docs/badge.svg` pattern. Paperwork: `api/openapi.yaml` with both clients regenerated, rows in `docs/api-surface.md` and `docs/configuration.md`, the page in `docs/ui.md`. Playwright: a `status` project with its own controller (authentication on, diagnostics fleet seeded, `status.mode: public`) covers the page signed out, on a Pixel 7 and through the accessibility checks -- six tests, passing locally. Seen to fail with the rule removed (rule 14): the fixture-name test and the platform test (audience filter dropped, operator title put into a reason), and the docs sentence test (one word changed on the page). |

## The primary target (ROADMAP v3.0)

Rows added 19 September 2026 for the packages ROADMAP.md version 3.0 added,
re-scoped or had never given a row. ROADMAP.md section 10 controls ordering.

| ID | Package | Classification | Status | Depends on | Session | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| ZF-005 | Corrections from reading the documentation against the fleet, and the marketplace repin | documentation; one pin | `done` | nothing | one Claude session, 19 September 2026, one pull request | **Done in one pull request.** The three pages agree with this record now: `docs/index.md` says a Windows host runs the agent on the `process` backend rather than that Windows is unsupported, and that while no *test* starts a container this repository's own CI runs inside containers the Docker backend started on a Zoomies fleet, naming `TestCIDogfoodsZoomiesWithRecoveryForEveryJob` as what keeps it so; the support matrix's Docker row moved right on the same evidence, its arm64 row says the backend has started no container *on arm64* rather than on any architecture, and the Windows and Proxmox rows stayed where they were; `docs/proxmox.md` says the supported combination is the one the harness is written for, that the harness has not been run against a cluster, and asks a fleet that runs it for its rows. `deploy/marketplace/release.env` pins `v1.2.0` — the controller image by digest, the agent version, and the installer fetched from the tag with its checksum recomputed — and `make marketplace-lock` was rerun; the lock also moved `caddy:2-alpine` to the digest its tag resolves to today, and the proxy pin follows it because `TestEveryPublishedRunnerVariantIsLocked` holds the two together. With it, section 11's one-line change: the release workflow skips only the `dev` prerelease by name, so a tag such as `V1.1.0` now fails its run with an error rather than building nothing, and `TestTheReleaseWorkflowRefusesAMalformedTagOutLoud` was seen to fail against the old guard. `mkdocs build --strict`, the `internal/docs` tests and the lock test pass on the new pin. The `V1.1.0` release itself is still the owner's to delete or keep. |
| ZF-221 | Runner resilience and efficient startup | correctness fixes and bounded extensions | `in_progress` | ZF-220 (done) | Codex and Claude sessions, 14 to 19 September; see the pull requests | Merged: every bullet version 2.40 listed as prepared, plus per-installation registration admission (`scheduler.registration_concurrency`), the FIFO start queue with a jittered cooldown, Docker readiness before a runner registers (`runners.docker_wait`), queued starts that expire at the provision deadline, owned-container conflict recovery, deferred registration cleanup recovered by housekeeping, the runner's resource sample with `sampled_at` on the API and `usage_fresh` on the host, prewarm telemetry, and `docs/runner-startup-stability.md` and `docs/runtime-compatibility.md` (#299, #310, #311, #337, #342, #343, #344, #345, #347). Remaining: a durable runtime-incident record with visible recovery on the host card and in the drawer, and an image-pull failure signal; a configurable active-create budget only if the operated instance's figures ask for it. The burst campaign and the sidecar-budget evaluation are withdrawn. **Record, 23 September 2026:** `in_progress`: item 1, durable runtime incidents and the image-pull signal, merged in [#434](https://github.com/eyupio/zoomies/pull/434) (migration `0049`). Item 2 is not authorised on speculation and is untouched. |
| ZF-224 | Auto-recovery on a lost runner | extension | `done` | nothing; [decision 0004](decisions/0004-the-fleet-may-re-run-a-job-it-broke.md) | one Claude session, 22 September 2026, one pull request | **Done in one pull request.** `scheduler.auto_rerun` (off by default) asks GitHub to run a job again when this fleet is what broke it, and `scheduler.auto_rerun_limit` (1-5, default 1) bounds how many times one workflow run may be re-run automatically. The bound is read from GitHub's own run attempt rather than a counter of ours, so it survives a controller restart and a re-run an operator asked for by hand counts against it. Only `FleetFailed` jobs are eligible -- a test that failed is never re-run -- and a duplicate delivery cannot buy a second re-run, because a completion that does not change the job's state never reaches the branch. The GitHub call, the timeline entry and the metric are shared with the button #406 added, so the two cannot drift; an automatic one is written *via recovery* rather than as an operator's doing. Turning it on raises `scheduler.auto_rerun_on`, a warning that names what it costs -- the installation's GitHub minutes, and a job that ran may have had side effects its author expected once -- and a limit outside 1-5 is an error. `zoomies_job_reruns_total{pool,trigger}` counts both triggers. Verified: a five-case table (off, on, the workflow's own failure, the bound reached, a higher bound), a repeated-delivery test, the validator's range test, and both halves seen to fail with the call removed. `docs/configuration.md`, `docs/problem-codes.md`, `docs/metrics.md` and `docs/troubleshooting.md` carry it; the troubleshooting page's claim that GitHub has no job-level re-run was corrected in the same change, stale since #406. |
| ZF-223 | A report per installation | new | `done` | ZF-209 | — | Planned only: ZF-303's evidence helper as a product feature — per-installation counts and exact percentiles from the stored timestamps and `fault_kind`, answering past retention from ZF-209's roll-up. Acceptance in [ROADMAP.md](../ROADMAP.md) section 8. **Record, 23 September 2026:** `in_progress`: open in [#441](https://github.com/eyupio/zoomies/pull/441), stacked on #431, not merged — `GET /installations/{id}/report`, exact percentiles from stored timestamps, the counts kept past retention by an `installation_daily` roll-up (migration `0051`), and a section on the Usage page. **Record, 24 September 2026:** `done`: [#441](https://github.com/eyupio/zoomies/pull/441) merged with the report route, the exact percentiles, the `installation_daily` roll-up and the Usage page section. |
| ZF-401 | Onboarding a private host | extension | `implemented` | ZF-207 for the audience | the Tailcat work predates this record's window; #327 and #329 by Claude sessions | Delivered: the private connection built into agent and controller (`internal/agent/tailcat.go`, `internal/api/tailcat.go`, migration `0027`), the Add-a-host choice, the observed connection badge, `hosts join-token create --connection tailcat`, two real-relay tests in the ordinary suite, `docs/private-hosts.md`; a private host re-announces itself on every dial (#327) and a missing address names its fix (#329). Remaining: a `tailcat.unavailable` problem and a relay re-expansion on start failure, and one sentence on the page. The home-lab confirmation run [tailcat-private-hosts.md](tailcat-private-hosts.md) listed is withdrawn. **Record, 23 September 2026:** `implemented`: the remainder merged in [#438](https://github.com/eyupio/zoomies/pull/438): `tailcat.unavailable` with the platform audience, relay expansion re-run with the sealed region first, recovery without a restart, a slower check while healthy, and the private-hosts sentence. |
| ZF-402 | Resumable operation ids, cancellation and the optional SSH bootstrap | new | `withdrawn` | — | — | Withdrawn in ROADMAP v3.0: the enrolment fails closed (a tunnel that will not start mints no token; an unused token can be revoked; a re-join needs `--yes`), and delivery rule 16 forbids the platform dialling a fleet's host. |
| ZF-403 | Drain and revoke a private host from the UI | existing | `implemented` | — | — | The acceptance is met by merged, tested code: cordon and remove on the host card, `zoomies hosts drain`, join-token revocation, and a host deletion that revokes the agent's credential because the token hash lives on the host row. Follow-on, demand-gated: rotating the shared tunnel identity as one audited platform action. |
| ZF-404 | What the agent owns on a host, written down | extension; documentation and one default | `implemented` | decision 25 | [PR #436](https://github.com/eyupio/zoomies/pull/436) | The code is non-invasive already (no package upgrade, firewall change, reboot or container, image or volume prune anywhere in the tree); the one exception is the daemon-wide builder-cache prune `agent.docker_build_cache_mb` (default 5120) performs every five minutes, which decision 25 keeps and names. Remaining: the 'what the agent owns' section on the security and private-hosts pages, linked from Add-a-host. **Record, 23 September 2026:** `implemented`: merged in [#436](https://github.com/eyupio/zoomies/pull/436) — one "What the agent owns on a host" section in `docs/security.md`, linked from the private-hosts page and the Add-a-host command step, with backend and docs tests pinned to it. |
| ZF-404b | Opt-in dedicated-host stewardship | new | `blocked` | decision 25; deferred indefinitely | — | Nothing built. Deferred indefinitely in ROADMAP v3.0: the hosts are a fleet's own machines and a platform upgrading or rebooting them is a liability, not a feature. |
