# Support matrix and measurement contract

The ZF-002 deliverable: one reference configuration the foundation is proved
on, an honest statement of what else is supported and to what degree, the
moments the platform must timestamp so that Gate F can be measured rather
than asserted, and the words *failure* and *severity* defined once.

Everything here is a proposal for the owner to ratify; the evidence it rests
on is in [validation/baseline-6d12a72.md](validation/baseline-6d12a72.md).

## RC1 measurement update

The RC1 fixes supersede the earlier measurement gaps below. First create task
issue is retained as `Runner.CreateTaskIssuedAt`, separately from the retry
clock. The new scheduling series pairs it with the actual job's eligibility;
GitHub approval holds are excluded, and an observed approval starts queue time.
Cleanup requires `HostRemovedAt` and `RegistrationDeletedAt`; its final stamp
is no longer a lower bound. Older estimates remain separately labelled.
`docs/metrics.md` defines the new series and the observations they exclude.

The owner has completed live qualification across selected repositories and
has explicitly removed a written qualification record as an RC1 prerequisite.
This does not manufacture a recorded seven-day observation window or claim
that the historical Gate F tables below have been measured.

## The reference configuration

| | Choice | Why |
| --- | --- | --- |
| Controller | Linux amd64, Ubuntu 24.04 LTS, `native` deployment under systemd | The only platform CI runs tests on, and what the installer was written against. |
| Agents | Linux amd64, Ubuntu 24.04 LTS, Docker backend, **rootless** daemon where the host offers one, rootful otherwise | The installer looks for a rootless socket first and the security posture assumes it; both must be exercised because most hosts still have only the rootful one. |
| Runner image | `ghcr.io/eyupio/zoomies-runner` at the pinned `actions/runner` release (2.337.0 on this commit), `pull_policy: if-not-present` | The image the docs tell people to use. |
| Topology under test | Two shapes, each in its own harness run: one VM with the embedded agent; a controller with one remote agent on a second host | The two deployment shapes the docs promise. The remote shape is what proves outbound-only agents, join tokens and host loss. |
| GitHub | github.com, a GitHub App installed on the owner's disposable organisation, with one repository-scoped and one organisation-scoped target | Both target kinds the docs support, because ZF-101's isolation tests need two installations. |
| Webhooks | A run with real delivery and a run on the poller alone | The harness today runs on the poller only. Real delivery needs a reachable URL; the tunnel or public host that provides it is part of the fixture. |

Everything else stays supported as it is documented today and is qualified
only to the degree a test proves:

| Configuration | Qualified for | Not qualified for | What would qualify it |
| --- | --- | --- | --- |
| Linux arm64 controller and agent | Build: CI cross-compiles and builds the arm64 images. Runtime: the Go suite and the lifecycle drill run on GitHub's hosted `ubuntu-24.04-arm` on every pull request, so a queued job becomes a real process on an arm64 machine and is cleaned up | Runtime with a container, and a real arm64 host in a fleet: the drill's runner is the stub, and the Docker backend has started no container on arm64 | An arm64 host in the real-runtime tier of the harness, which is a beta-testing item: the row moves when a job runs on an arm64 host somebody kept |
| Windows agent, `process` backend | Build: `windows/amd64` is cross-compiled in CI and shipped in every release; the whole tree vets on Windows, and the packages the agent is made of run their tests on GitHub's hosted `windows-latest` on every pull request. Shape: the `.zip` unpacker and its refusals, the digests for `win-x64` and `win-arm64`, the service manager's command line and its `sc.exe` calls, the removal retry | **Runtime: nothing in this repository has run actions/runner, joined a host, or started the service on a Windows machine.** The job object, the service control dispatcher, the disk and memory queries and the ProgramData defaults compile and have not been exercised. Windows on arm64 has digests and no build | A Windows host in the real-runtime tier of the harness, which is a beta-testing item: the row moves when a Windows host joins from the documented command (`install.ps1`, ZF-238) and a queued job runs on it, with the evidence in [the Windows checklist](#windows-evidence-checklist) |
| **Docker backend** | Shape: the Engine API client, the create arguments, the DinD refusal, the cache suffix and the permission checks are unit-tested against a fake Engine. Runtime: this repository's own CI runs on a Zoomies fleet through the build under test; every job but the arm64 and Windows legs runs inside a container the Docker backend started, on every pull request and every push to `main`, and `TestCIDogfoodsZoomiesWithRecoveryForEveryJob` (`internal/docs`) fails if a job leaves the fleet | Runtime from a test: no test in this repository starts a container, so the drill tier's runner is still the stub and the daemon-stopped-then-restored half of the dead-socket drill has nowhere to run; on arm64 the backend has started nothing | A container started from a test would be a Docker daemon in the drill tier, which ROADMAP v3.0 withdrew as a gate (ZF-301c); the arm64 half moves with the arm64 row. It was the largest single gap in this table until CI moved onto the fleet, and it stays first because the default backend is the one to be honest about |
| Podman backend | Shape: probe, defaults, DinD refusal and the SELinux cache suffix are unit-tested | Runtime: no test starts a Podman daemon | A Podman host in the harness |
| `process` backend | Shape: layout, signals, process groups, archive tamper check, JIT config off argv. Runtime: the drill tier runs the built binary as a real remote agent on this backend and watches a workload appear on the machine and go away | Runtime with the real runner: the drill's workload is a stub staged on disk, so nothing has yet run `actions/runner` itself | A bare host in the harness with the real runner tree; it is the least isolated backend and the docs already say so |
| macOS controller | Build; launchd plist rendering | Everything else; the docs say development only | Nothing planned |
| Debian, Fedora, Alpine hosts | Installer parses under `dash` and `shellcheck`; detection is unit-tested | No install runs on them in CI | One real install per distribution in the installer tier of the harness |
| `compose` and `docker` deployments | Templates are unit-tested; images build in CI | No CI job brings a deployment up and joins an agent to it | A compose-up smoke test in the harness |
| GitHub Enterprise Server | `github.api_base_url` is validated | No test speaks to a GHES | Access to a GHES; not in the first assignment |

Cross-compilation is not qualification. A row moves right only when something
runs on the thing; a test, or this repository's own CI.

## Windows evidence checklist

`install.ps1` and the Windows command in Add a host (ZF-238) are tested on a
hosted Windows runner against a real controller, which proves the script, the
service registration and the upgrade and uninstall paths. It does not prove a
job runs. The Windows row moves only when a maintainer has done all of this on a
real **Windows 11 x64** machine and attached the evidence. None of it has been done.

1. **Join over a direct connection.** Paste the command from Hosts, Add a host,
   Windows, Direct, into an elevated PowerShell window.
2. **Join over Tailcat.** The same with Private connection. Confirm `tailcat://`
   works from Windows; if it does not, record the failure here.
3. **Create a pool** with `--host-selector os=windows` and the `process` backend
   in a private test repository.
4. **Run a workflow** on it that prints `systeminfo` and finishes green.
5. **Run the installer again** as an upgrade, with the same command. Confirm the host
   is the same one, its credentials are unchanged and the service is running.
6. **Uninstall** with `-Uninstall`. Confirm the service, the install folder and the
   `PATH` entry are gone and that what it said it left behind is there.

Record, for the row to move: the job run URL, the release or `dev` build that was
installed (`zoomies.exe version`), and a screenshot of the Hosts page showing the host
as `windows` on the `process` backend. Also note what Microsoft Defender and
SmartScreen did with the unsigned binary, and which account the service ran as
(LocalSystem unless reconfigured).

## The moments to timestamp

Gate F asks for two timings and one convergence, and each needs a start and an
end that the store records. The baseline lists the fields as they stood at
`6d12a72`; every gap it recorded has since been closed, so the table below is
the current answer and the baseline is history.

| Interval | Start | End | Today |
| --- | --- | --- | --- |
| **GitHub queue wait** (not Zoomies' time) | GitHub's `started_at` on the job, absent for a queued job | `Job.QueuedAt` as first seen here | Reported by `JobEvent.Source`; the poller-only case inflates it and must be labelled |
| **Scheduling latency** | First observed eligibility after any GitHub approval hold | First create task issued in the host poll response | `Job.EligibleAt` and `Runner.CreateTaskIssuedAt` persist the exact endpoints. The new histogram uses the actual job/runner association; retries do not overwrite the end. Missing and prewarmed intervals are excluded. |
| **Configured scaling delay** (not Zoomies' time) | eligible | the scheduler's hold expires (`scheduler.scale_up_delay`, the start-failure backoff) | Reasons are in `ScalingEvent`. The new scheduling histogram includes this delay; report it separately when judging platform overhead |
| **Provisioning** | task issued | `Runner.ContainerStartedAt` | Exists; `Runner.ImagePullDuration` separates the pull where the backend can |
| **Registration** | `Runner.ContainerStartedAt` | `Runner.RegisteredAt` | Exists |
| **Assignment** (GitHub's decision, not Zoomies') | `Runner.RegisteredAt` | `Job.StartedAt` | Exists; GitHub may hand the job to a different idle runner, which the contract accepts and reports as *ran elsewhere in the fleet* |
| **Execution** | `Job.StartedAt` | `Job.CompletedAt` | Exists |
| **Cleanup convergence** | `Runner.FinishedAt`, with configured retention reported separately | Both host removal and GitHub registration absence confirmed | `HostRemovedAt`, `RegistrationDeletedAt` and `CleanedUpAt` provide positive confirmations. Historical `CleanupEstimatedAt` values are estimates only. |

Two smaller gaps the reconciliation found belonged to Phase 0 itself because
they needed no migration, and the code for both has landed. A job GitHub holds
for a deployment review is `waiting`, and the hold and its release now have
timeline kinds of their own (`waiting` and `approved`, written by the existing
job-change path) so a held job's timeline says whose time the review was. The
RC1 change moves the queue start when an observed approval changes a waiting
job to queued, and keeps that boundary on replays. `Stats` is split four ways
(succeeded, failed, cancelled and unknown) so a `stale` or empty conclusion
is no longer counted as a success.

Two rules go with the table. Every rate has a denominator written beside it,
and an observation the platform did not see (a job that completed while the
controller was down and was learned about later) is *unknown*, never
*success*. And a failure is attributed to Zoomies only when the platform's own
record says so: `Job.RunnerFault` set, a `runner_lost` event, a runner that
went `failed` before a job started, or a cleanup that did not converge. A
workflow whose steps failed is the workflow's business.

## Counters

The metrics page already exposes fleet counts. The contract adds, per pool
and per installation: jobs observed, eligible, created for, ran here, ran
elsewhere in the fleet, ran on another provider, failed with a Zoomies fault,
cleanup pending, cleanup converged. Names and label cardinality are ZF-205's
to settle; the counts are what Gate F is computed from.

## Severity

| Severity | Definition | Gate F |
| --- | --- | --- |
| **Critical** | Compromise of a credential, a host or another job's data; loss of fleet state | Zero unresolved |
| **High** | A job the platform should have run did not, or was lost, with no reasonable workaround; a recovery path that needs database surgery | Zero unresolved |
| **Medium** | A reasonable workaround exists, or the effect is confined to diagnostics, timing or one deployment shape | Counted and listed |
| **Low** | Cosmetic, copy, or a limitation the docs already state | Listed |

## Gate F targets as adopted

| Target | As proposed | Adopted | Note |
| --- | --- | --- | --- |
| Observation | 7 consecutive days, 200 representative attempts | 7 days, 200 attempts, of which at least 20 intended failures and 20 cancellations, and at least three bursts in which the queued jobs exceed the fleet's total capacity | 200 attempts over 7 days is about 29 a day and never exercises contention on its own; the bursts are what prove scheduling under load rather than elapsed time |
| Denominator | "eligible controlled attempts" | completed jobs on non-demo installations whose runner this controller created; `waiting` jobs and jobs GitHub dispatched elsewhere are counted and reported but excluded | The seeded demo installation has no GitHub behind it and must never appear in a count; `retention.runners` defaults to 7 days, so raise it on the reference instance or export evidence daily |
| Outcome rate | ≥ 99% of eligible attempts reach the expected terminal outcome without a Zoomies-caused failure | same; a Zoomies-caused failure is `Job.RunnerFault` set, or a runner that failed before registration for a job that then ran elsewhere or expired | 200 attempts allows two Zoomies faults; report the count, not only the percentage |
| Scheduling latency | p95 ≤ 10 s from eligible to create task issued, with capacity and a prepared image | p95 ≤ `scheduler.interval` + 2 s from the later of the delivery's receipt and eligibility to the create task being issued, reported beside the interval it ran at | With the default 10 s interval, the wait for the next tick alone is uniform over 0 to 10 s and its p95 is about 9.5 s before any processing, so the figure as proposed fails by construction or passes by noise. The reference run may use a 5 s interval and keep 10 s if the owner prefers the round number |
| Cleanup convergence | within 5 min of the retention deadline with dependencies healthy; offline hosts converge within 5 min of recovery | same; use confirmed `Runner.CleanedUpAt`, never the historical estimate | Subtract configured retention from the finish-to-confirmation interval when assessing this target. |
| Restore | demonstrated on a clean environment; reference target 30 min | same | Record the achieved time and the backup's age |
| Second operator | completes setup and diagnoses an injected failure from the docs and UI | same | Cannot be produced by the agent; the gate stays pending until a person does it |
| Defects | zero unresolved critical or high; zero unexplained lost jobs; zero cross-scope access | same | |

None of these are commitments to anyone outside the project. They are what
"good enough to call it a trusted-workload beta" means here.
