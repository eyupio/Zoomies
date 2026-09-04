# End-to-end test

This is the test that proves the whole thing works: it starts a real
controller, points it at a real GitHub App, creates a pool, triggers a real
workflow in a real repository, and asserts that an ephemeral runner appeared,
ran the job, and was destroyed afterwards.

It is skipped unless it has everything it needs, so `go test ./...` is safe to
run anywhere.

## Running it

You need a GitHub App installed on an organisation (or a repository) you are
willing to run workflows in, a Docker daemon, and a repository containing the
workflow in `testdata/e2e-workflow.yml`.

```sh
export ZOOMIES_E2E=1
export ZOOMIES_E2E_APP_ID=123456
export ZOOMIES_E2E_INSTALLATION_ID=987654
export ZOOMIES_E2E_PRIVATE_KEY_FILE=/path/to/app.private-key.pem
export ZOOMIES_E2E_TARGET=my-org                # or my-org/my-repo
export ZOOMIES_E2E_REPO=my-org/zoomies-e2e      # where the workflow lives
export ZOOMIES_E2E_TARGET_TYPE=org              # or repo

make test-e2e
```

It runs in polling mode, because a test host is not reachable from GitHub for
webhook delivery. That means it exercises the fallback path rather than the
webhook path; the webhook path is covered by the integration tests against the
fake GitHub in `internal/github` and `internal/controller`.

## What it asserts

1. The controller starts and reports healthy.
2. The installation verifies: credentials good, permissions sufficient.
3. A pool can be created through the API.
4. Triggering the workflow causes a runner to be created within the timeout.
5. The runner registers with GitHub and reaches `idle`, then `busy`.
6. The job completes successfully.
7. The runner is destroyed afterwards, and **no registration is left behind on
   GitHub** — the failure this catches is the one that quietly fills an
   organisation's runner list with dead entries.
8. The audit log records the pool creation.

## Cleaning up after a failure

If the test fails partway, it still tries to remove what it made. If it does
not manage:

```sh
zoomies runners list --include-removed
zoomies pools delete <pool-id> --force
```

and check the organisation's runner settings page for orphans.
