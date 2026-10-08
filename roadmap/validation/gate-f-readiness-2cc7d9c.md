# Gate F readiness at `2cc7d9c`

What Gate F still needs, recorded at the end of Assignment A. This is not a
claim that the gate is met: no attempt has been made, and several of its
targets cannot yet be measured at all. It says which, and why.

Written per [README.md](README.md)'s rules: *passed*, *not run* and *blocked*
are kept apart, and a test that skipped itself is *not run*.

| | |
| --- | --- |
| Commit | `2cc7d9c` on `claude/roadmap-implementation-1tui5x`, merged to `main` in #120. Every count below was measured there. Two passages were amended two commits later, at `b3326ac` on the same branch and in the same pull request, and each says so where it stands; the file keeps the name of the commit the run was made at, because [README.md](README.md) asks that a later change be recorded in place rather than by renaming. |
| Go | 1.26.0 (CI: 1.26) |
| Node | 22.22.2 (CI: 22) |
| Machine | The session's container, Linux amd64, no Docker daemon. CI: `blacksmith-2vcpu-ubuntu-2404` |
| Packages under test | 23 Go packages, `-race`; 275 Playwright cases across `chromium`, `mobile` and `first-run`, of which **260 passed and 15 skipped**. *Corrected 8 September: this row first called those skips cases the container could not run, and they are not.* Eight are the phone-layout cases standing down in `chromium` and seven are desktop cases standing down in `mobile`; each of the fifteen runs in the project it was written for, so nothing here is unmeasured and this directory's *not run* rule (which is about a missing credential or a missing daemon) does not bite. 2 runtime drills plus a budget test |

## The targets, and whether they can be measured

| Target | Measurable today? | What is missing |
| --- | --- | --- |
| Observation: 7 days, 200 attempts, ≥20 failures, ≥20 cancellations, ≥3 bursts over capacity | **No** | A deployment with real GitHub behind it. Owner's (decision 12, ZF-301c). |
| Denominator: completed jobs on non-demo installations whose runner this controller created | **Yes** | Nothing. `jobs.installation_id` (migration `0012`) and the runner's own row carry it. `retention.runners` defaults to 7 days and must be raised on the reference instance, or evidence exported daily. |
| Outcome rate ≥ 99%, a Zoomies fault being `Job.RunnerFault` or a pre-registration failure | **Yes** | Nothing. `RunnerFault` is recorded and the four-way completed split exists. |
| **Scheduling latency p95 ≤ `scheduler.interval` + 2 s** | **Both ends recorded; no series yet** | This note found the start missing and it was closed in the same pull request: `Job.EligibleAt` (migration `0019`) is stamped the first time an enabled pool claims the job's labels, and never moved afterwards. `Runner.TaskIssuedAt` (the end) came with ZF-102. What remains is the series itself; the existing `zoomies_runner_queued_to_create_seconds` still measures from `Job.QueuedAt` and is still a proxy, and replacing it with the real interval is ZF-205's "real latency series", which is Assignment B. Exporting it from the store meanwhile is not simply reading the two columns: `jobs.eligible_at` is on the API's job shape, but `runners.task_issued_at` is on the runners table alone (on no view and in no OpenAPI schema) and it holds the last lifecycle task handed to the host, re-stamped on every redelivery and again by the stop and the remove, so a terminal row no longer carries the create's time. Until ZF-205 computes the interval as it happens, an export must read `task_issued_at` while the runner is still provisioning, or pair `eligible_at` with the runner's `created_at`, and say which end it used. |
| **Cleanup convergence ≤ 5 min** | **Yes, but the end is stamped early** | `Runner.CleanedUpAt` landed with ZF-105. *Corrected 8 September: this row said it was set only when all three resources were confirmed gone, and it is not.* It is stamped at whichever comes first (GitHub confirming the registration deleted while the row carries no cleanup failure, or the agent reporting a stop or a remove done) and `removeRunner` queues the agent's task and then deletes the registration in line, so in the ordinary path the stamp is written before the host has said anything about the workload or the work directory; a stop, in particular, leaves the work directory where it was. Read the interval as a lower bound on convergence. A cleanup that never converged is not in it at all: those rows carry `CleanupError` and raise `runners.cleanup_failed`, and must be counted beside the series rather than left out of it. |
| Restore, ≤ 30 min on a clean environment | **No** | ZF-203, which is Assignment B: there is no backup command yet. |
| Second operator completes setup and diagnoses an injected failure | **No** | A person. Owner's (ZF-303). Cannot be produced here. |
| Defects: zero unresolved critical or high, zero unexplained lost jobs, zero cross-scope access | **Partly** | Cross-scope access is proved by tests (see below). "Unexplained lost jobs" needs the observation window; "unresolved critical or high" needs a triage record that does not exist yet. |

## What is proved, and by what

Every behavioural rule below was run against the code with its rule removed and
confirmed to fail before the test was kept.

| Property | Where |
| --- | --- |
| Two installations never cross-allocate; eligibility asks enabled, then installation, then labels | ZF-101, `internal/scheduler`, `internal/controller` |
| One controller per database; an agent adopts its runners on restart; late reports do not resurrect terminal rows | ZF-102, `internal/controller/invariants_test.go` |
| A host reports the machine its runners will actually get, and the disk they will actually write to | ZF-103a, `internal/agent` |
| Every route refuses the roles and the scopes outside it; the five agent routes refuse every credential but their host's; no secret reaches a response, an error body, an audit row, an event frame, `/metrics` or the log | ZF-104, `docs/security.md` §7 names each test |
| A live stream ends within one heartbeat of its credential being revoked | ZF-104 PR3 |
| Cleanup failure is recorded on the row and raised as `runners.cleanup_failed`; an abandoned work directory is reaped | ZF-105 |
| A queued job becomes a real workload on a real machine and is cleaned up; removing a runner deletes its registration | `test/drill`, on every pull request |

## Not run: no test has ever exercised these

Cross-compilation is not qualification; these rows move only when something
runs on the thing.

* **The Docker backend at runtime.** Nothing has started a container. The drill
  tier uses the `process` backend with a stub runner, because this container
  has no daemon. This is the largest single gap: Docker is the default backend
  and the one the docs tell people to use.
* **Podman, arm64, macOS, GHES, and the `compose` and `docker` deployments**,
  as [support-and-measurement.md](../support-and-measurement.md) already
  records.
* **Real GitHub.** `test/e2e` is credential-gated and has never had them here.
  The fake is thorough and now drives real webhook deliveries in the drills,
  but it is a fake.
* **The six fault drills** ZF-302 names: controller killed mid-job, agent
  killed mid-job, dead Docker socket, rate limit and reset, failing JIT
  endpoint, small filesystem. The tier that could run them exists; the drills
  do not.

## Blocked on the owner

* A disposable organisation, an App on it with one organisation-scoped and one
  repository-scoped target, a repository carrying the scenario workflows,
  secrets in a protected environment, and a tunnel or public host for the
  webhook run (ZF-301c, Gate F).
* A reference Ubuntu 24.04 amd64 host with `main` deployed natively, its
  versions recorded here (ZF-002, still outstanding from Phase 0).
* A second operator for one setup-and-diagnose session (ZF-303).
* The pre-release tag at the end of Assignment A (decision 9).

## Two things this note found

The first was a code gap in Assignment A's own scope, and writing it down is
what closed it: the measurement contract said `Job.EligibleAt` landed with
ZF-101, ZF-101 was marked done, and the column did not exist. What ZF-101 had
delivered was `scheduler.Eligible` as a *function*; the definition, not the
timestamp. It is recorded now.

The second is still open.

The drill record writes `roadmap/validation/drills.md` and CI appends it to the
job summary, but the file is never committed, and every run starts from a fresh
checkout. So the history its own comment describes ("a drill that has
recovered cleanly forty times and then did not is a finding that only exists if
the forty are there to compare against") does not accumulate. Each run's rows
live in that run's summary and nowhere else. Retaining them is ZF-302's record
work, and it is worth doing before the fault drills start producing rows worth
comparing.
