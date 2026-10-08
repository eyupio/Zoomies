# Baseline: `main` at `6d12a72`, 6 September 2026

The reconciled starting point for the follow-on roadmap (ZF-001). Everything
below was run or read on this commit; nothing is inferred from an older one.

## What this commit is

| | |
| --- | --- |
| Commit | `6d12a72be9477ef123e6e16e8b5fc57e5bde203f`, the merge of PR #71 |
| Reported version | `zoomies 0.1-alpha-299-g6d12a72`, i.e. 299 commits past the only tag, `v0.1-alpha` (4 September 2026) |
| History | 341 commits between 4 and 6 September 2026. The project is three days old. |
| Improvement plan | `IMPLEMENTATION_PLAN.md`: Waves 1, 2, 3, 4a, 4b, 4c and 4d all merged (PRs #62 to #68). Open: **D16**, a brand decision for the maintainer; **N02**, runners stuck in `registering` on a dev instance, not reproducible on `main`, waiting on a deployment of `main` to look again. |
| Schema | Eleven migration files, `0001_init.sql` through `0009_jobs_waiting_state.sql`. Two prefixes are used twice (`0005`, `0006`); both pairs shipped and must never be renamed. The next migration is `0010`. *Overtaken by later work: prefixes from `0010` onwards have since shipped, some of them from outside this roadmap. This row records the schema as it stood at `6d12a72` and says nothing about where it stands now; a new migration takes the next unused prefix in `internal/store/migrations/`, which is the schema rule in section 3 of ROADMAP.md and is what that document names in place of a number.* |
| Agent protocol | `agent.ProtocolVersion = 1` (`internal/agent/protocol.go`). Task kinds: `create_runner`, `stop_runner`, `remove_runner`, `stream_logs`, `cancel_logs`, `prewarm_image`. |
| Deployment models | `native` (systemd or launchd), `compose`, `docker` (`install.sh`, `internal/installer`). |
| Backends | `docker`, `podman`, `process`. |
| Public API | 62 paths in `api/openapi.yaml`; the router and the document are diffed both ways in tests. |
| Licence | AGPL-3.0. |

## What was run here

Sandbox: Linux 6.18, 4 vCPU, 15 GB, Go 1.25.0 (the `go.mod` floor; CI builds
with 1.26), Node 22.22.2, npm 10.9.7, Docker 29.3.1 present but not used by any
test below.

| Check | Command | Result |
| --- | --- | --- |
| Build without the UI | `make build-nogui` | passed |
| Vet | `go vet ./...` | passed, no output |
| Go tests with race detection | `go test -race -count=1 ./...` | **passed**: 16 packages ok, 0 failed. `internal/logging` has no tests. `test/e2e` reports *no test files* because it is behind the `e2e` build tag: the default gate cannot see it at all. |
| UI lint, type check and build | `npm run lint && npm run check && npm run build` in `web/` | see the UI row below |

Longest packages under race detection: `internal/api` 93 s, `internal/controller`
86 s, `internal/auth` 36 s, `internal/store` 29 s.

## What CI ran on this commit

CI run 169 on `main`, https://github.com/eyupio/zoomies/actions/runs/34033962435,
concluded **success** at 12:47 UTC on 6 September 2026. It covers what this
sandbox cannot: `staticcheck`, the generated-client freshness check, the
Playwright suite on Chromium and the Pixel 7 profile, the four-target
cross-compile, the `install.sh` syntax checks under `dash` and `shellcheck`,
and the image builds. Two of the three preceding runs on `main` were cancelled
by the concurrency group when the next merge landed, and run 161 (PR #67)
failed; PR #69 fixed the flaky Playwright test that failed it.

## What was not run

| Check | Why not | What it would take |
| --- | --- | --- |
| `make test-e2e` | Needs a GitHub App, an installation, a repository carrying `test/e2e/testdata/e2e-workflow.yml`, and a Docker daemon. Without them the test skips. | The owner's designated test App and repository, as environment variables named in `test/e2e/README.md`. |
| `staticcheck` | Not installed in the sandbox. | Covered by CI run 169. |
| `mkdocs build --strict` | Not installed in the sandbox. | Covered by the site workflow on any change under `docs/`. |
| Podman, `process` and macOS runtime tests | The unit tests for these backends run against fakes; no test starts a real Podman daemon, a real bare-process runner or a launchd service. | A host with each runtime, and a real-runtime tier in the harness (ZF-301). |
| arm64 | CI cross-compiles `linux/arm64` and `darwin/arm64`, and builds the arm64 images under QEMU on `main`. Nothing runs a test on arm64. | An arm64 runner in the harness. |

## Support claims versus evidence

| Claim in the docs | Evidence on this commit | Status for ZF-002 |
| --- | --- | --- |
| Linux amd64 controller and agent, Docker backend | Unit and integration tests against fakes; Playwright against the real binary; CI on Ubuntu 24.04 amd64. No real-Docker test ran on this commit anywhere. | **Reference configuration** once ZF-301's real-Docker tier has run. |
| Linux arm64 | Cross-compile and image build only. | Qualified for build, not for runtime. |
| macOS controller "for development" | Cross-compile; launchd plist rendering is unit-tested. | Development only, as the docs say. |
| Podman backend | Unit tests for probe, defaults, DinD refusal, SELinux cache suffix. | Qualified for shape, not for runtime. |
| `process` backend | Extensive unit tests including process-group leadership and the tamper check on the runner archive. | Qualified for shape, not for runtime. |
| Ubuntu, Debian, Fedora, Alpine hosts | `install.sh` detects them; CI parses the script under `dash` and `shellcheck`. No install runs on any of them in CI. | Installer syntax only. |
| GitHub Enterprise Server | `github.api_base_url` is configurable and validated. No test speaks to a GHES. | Configurable, not qualified. |

## Lifecycle timestamps that exist today

For the measurement contract in ZF-002. All are on `internal/store/models.go`
unless noted.

| Moment | Field | Source |
| --- | --- | --- |
| Job observed | `Job.QueuedAt` | webhook delivery or poller |
| Job claimed by a pool | `JobEvent{Kind: claimed}.At` | controller |
| Job left unmatched | `JobEvent{Kind: unmatched}.At` | controller |
| Runner row created | `Runner.CreatedAt` | controller, on the create task |
| Image pull | `Runner.ImagePullDuration` | agent, where the backend can separate it |
| Container started | `Runner.ContainerStartedAt` | agent |
| Registered with GitHub | `Runner.RegisteredAt` | agent report |
| Runner became idle | `Runner.LastIdleAt` | controller |
| Job started on the runner | `Job.StartedAt`, `JobEvent{Kind: started}` | webhook `in_progress` |
| Job completed | `Job.CompletedAt`, `JobEvent{Kind: completed}` | webhook `completed` |
| Runner lost mid-job | `JobEvent{Kind: runner_lost}`, `Job.RunnerFault` | controller |
| Runner finished | `Runner.FinishedAt` | agent report |
| Scheduler decision | `ScalingEvent.CreatedAt` with the reason string | controller |

`JobEvent.Source` records who saw each moment (`webhook`, `poller`, `agent`,
`controller`), which is what separates a fleet that is receiving webhooks from
one living on the poller. `Job.RunnerFault` is what tells "the tests failed"
from "the runner died", and `store.FailedConclusions` is the one definition of
a failed job.

Not recorded today: the moment a job became *eligible* for scheduling as
distinct from observed; the moment the create task was *issued* to the agent
as distinct from the runner row's creation; and the moment cleanup of a
runner's registration, container and work directory *completed*, as distinct
from the runner row reaching `removed`. Those three are the gaps the Gate F
timings need closed.

*Since superseded.* All three moments are recorded now, and all three landed
during Assignment A. `runners.task_issued_at` came with ZF-102 in migration
`0014` and `runners.cleaned_up_at` with ZF-105 in `0015`; each with the
package the measurement contract expected to own it. `jobs.eligible_at`, in
migration `0019`, did not: the contract had it landing with ZF-101, and what
ZF-101 delivered was `scheduler.Eligible`, the definition of eligibility as a
function rather than the moment it was reached. The column was written only
when the Gate F readiness note went looking for it and found nothing.
