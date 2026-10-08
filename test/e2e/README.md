# End-to-end tests

Two scenarios, covering the two halves of the product a person actually meets.

**`ephemeral_runner_runs_a_real_job`** proves the fleet works: it starts a real
controller, points it at a real GitHub App, creates a pool, triggers a real
workflow in a real repository, and asserts that an ephemeral runner appeared,
ran the job, and was destroyed afterwards.

**`installer_provisions_and_removes_a_host`** proves setup works: it lets
`zoomies init` loose on a throwaway container, then asserts what it did to that
machine (the service account, the modes on `/etc/zoomies`, the sealed key, a
controller that actually serves) and that `zoomies uninstall` gives the
machine back.

The first creates things on somebody's real GitHub organisation, so most of
what follows is about making sure it never leaves them there. The second
creates nothing outside its own container, and needs no GitHub credentials at
all: a gate with no App installed still runs it.

## Running it

The installer scenario needs only a Docker daemon and a Linux build:

```sh
make build
ZOOMIES_E2E=1 make test-e2e      # the runner scenario skips, the installer one runs
```

The runner scenario needs more: a GitHub App installed on an organisation (or a
repository) you are willing to run workflows in, a Docker daemon, an
authenticated `gh` CLI, and a repository containing the workflow in
`testdata/e2e-workflow.yml`.

```sh
export ZOOMIES_E2E=1
export ZOOMIES_E2E_APP_ID=123456
export ZOOMIES_E2E_INSTALLATION_ID=987654
export ZOOMIES_E2E_PRIVATE_KEY_FILE=/path/to/app.private-key.pem
export ZOOMIES_E2E_TARGET=my-org                # or my-org/my-repo
export ZOOMIES_E2E_REPO=my-org/zoomies-e2e      # where the workflow lives
export ZOOMIES_E2E_TARGET_TYPE=org              # or repo

make build          # the test runs the built binary, it does not build one
make test-e2e
```

It runs in polling mode, because a test host is not reachable from GitHub for
webhook delivery. That means it exercises the fallback path rather than the
webhook path; the webhook path is covered by the integration tests against the
fake GitHub in `internal/github` and `internal/controller`.

## Skipped, blocked, or run

`make test-e2e` skips when a prerequisite is missing, which is what you want on
a laptop. A gate wants the opposite:

```sh
make test-e2e-required
```

In required mode a missing prerequisite is a **failure**, not a skip. This
matters more than it sounds: a skip is exit code zero, so before this a harness
that had never once talked to GitHub reported the same green as one that had
run the whole scenario.

Every run writes one JSON result per scenario, to `roadmap/validation/e2e/` or
to `ZOOMIES_E2E_RESULTS_DIR`. Its `category` is the only vocabulary a run
reports in:

| Category | Meaning |
| --- | --- |
| `passed` | The scenario ran and every assertion held. This is the only pass. |
| `failed` | The scenario ran and something did not hold. |
| `blocked` | It was asked for and could not start: a prerequisite was missing. |
| `not_run` | Nobody asked for it. |

The result also carries the commit, the run id, the dispatch marker, a link to
the workflow run, anything the run created and could not remove
(`residual_cleanup`, empty is the only good value), and anything the start-up
sweep cleared up after an earlier run.

## What the runner scenario asserts

1. The controller starts and reports healthy.
2. The installation verifies: credentials good, permissions sufficient.
3. A pool can be created through the API, and the audit log records it.
4. Triggering the workflow causes a runner to be created within the timeout.
5. The runner registers with GitHub and reaches `idle`, then `busy`.
6. The job completes successfully, and it is *this run's* job.
7. The runner is destroyed afterwards, and then, asking GitHub and the Docker
   daemon rather than Zoomies:
   * **no registration is left behind on GitHub**: the failure this catches is
     the one that quietly fills an organisation's runner list with dead
     entries; and
   * **no container is left behind on the host.**

   Both are asked of something other than Zoomies on purpose. Zoomies' own
   `/runners` endpoint says "removed" exactly when Zoomies believes it removed
   something, and that belief is the thing under test.

## What the installer scenario asserts

It needs a Docker daemon and a Linux build of the binary, and nothing else.
The binary is bind-mounted into a `debian:stable-slim` container
(`ZOOMIES_E2E_INSTALLER_IMAGE` overrides it) and `zoomies init` is run there
unattended from an answer file, at the **default system paths**.

The default paths are the point. Several of the installer's guards are keyed to
them (`serviceUserToRemove` refuses to delete the service account unless the
install really is the one that owns `/etc/zoomies`) so a test pointed at a
temporary directory proves nothing about the code that runs for an operator.
Installing for real is only safe because the machine is disposable, which is
what the container is for.

1. `zoomies init` succeeds unattended, and its summary names the encryption key
   the operator now has to back up.
2. `/etc/zoomies` and `/var/lib/zoomies` exist, mode 0750, owned by `zoomies`.
3. The encryption key is mode **0600** and the configuration is 0640, asked of
   the filesystem, not of the installer's own summary.
4. The key is in its own file and `encryption_key` in `zoomies.yaml` is empty.
   A key written into the config would be copied by every backup and every
   configuration-management run that touches it.
5. A real system account exists and **cannot be logged into**.
6. The database was created.
7. The controller that install configured **actually serves**: `/healthz`
   answers 200 over the published port, and `/api/v1/pools` answers **401**.
   The safe configuration is the default, so an install that left the API open
   is a failed install however healthy it reports.
8. `zoomies uninstall` reports a clean removal: and then, asking the machine
   rather than believing the report, both directories and the service account
   are gone.

What it does not cover is the service manager: a plain container has no
systemd, so the installer correctly chooses "no supervisor" and the unit-file
path is not exercised here. The templates it would write are covered by
`internal/installer`'s own tests.

## One run cannot collide with another

Each run mints a run id and creates a pool whose label carries it
(`zoomies-e2e-<run-id>`), and dispatches the workflow asking for that label. Two
runs against one organisation therefore cannot take each other's jobs, adopt
each other's runners, or clean up each other's leavings.

## Cleaning up

Before anything is created, the run writes a **ledger** to
`~/.zoomies/e2e-ledgers` (or `ZOOMIES_E2E_LEDGER_DIR`) naming what it is about
to make. Every resource is appended as it is created, and the ledger is deleted
only once they have all been removed.

It lives outside the test's temp directory deliberately: `t.TempDir` is removed
when the test ends however it ends, which is exactly the moment a record of
what was *not* cleaned up becomes useful.

Cleanup runs however the test ends (pass, fail, or fatal) and in the order
that makes each step possible: force-delete the pool, wait for its runners to
go, delete the installation, then take off GitHub directly anything Zoomies
could not be made to remove. What it still cannot remove is written to the
result's `residual_cleanup`.

A run that is killed outright (`SIGKILL`, a timeout, a lost machine) cannot run
its cleanup. That is what the ledger is for: the **next** run sweeps the open
ledgers for the same target, deletes the runner registrations they left on
GitHub, and reports what it found in `swept_from_earlier_runs`. A sweep that
keeps finding things is a harness that keeps crashing, which is why it reports
rather than tidying quietly.

To check by hand:

```sh
ls ~/.zoomies/e2e-ledgers                       # open ledgers = unfinished runs
gh api /orgs/<org>/actions/runners              # registrations still on GitHub
docker ps -a --filter label=io.zoomies.managed=true
```

## Timeouts

Every scenario's waits are constants in `budget.go`, and **their total** must
fit inside the `-timeout` the Makefile passes; the scenarios share one test
binary and one timeout, so a suite whose scenarios each fit but whose sum does
not is killed partway through the last one. `budget_test.go` checks that, and it
carries no build tag so it runs in ordinary CI: the harness previously asked for
twenty-seven minutes of waiting behind a twenty-minute timeout, so it could
never have reached its own last assertion, and a run that was going to fail on
a leftover registration would have been reported as a timeout instead.
