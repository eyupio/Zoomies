# ZF-232: updating from the web UI — Implementation Plan

**Status**: being written. This commit holds the header, the constraints and the
review focus; the six parts, each one pull request, are added in the commits
that follow.

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
Read both before starting a task; this plan decides names and order, and the
spec decides behaviour.

## Global Constraints

* Go 1.27.1 or later, `CGO_ENABLED=0`, standard library only. This plan adds no
  dependency, so `docs/dependencies.md` does not change.
* `go build ./...` fails on a clean checkout until `make build-nogui` has run
  once; run it first. `make lint` and `make test` are green before every push.
* Only `internal/store` writes SQL. `internal/updates` stays pure — no clock
  read, database or network — and everything that touches a file, a command or
  the clock lives in `internal/updates/channel`, `internal/installer` or
  `internal/controller`.
* Settings live in the database and are read from the effective configuration,
  never from a `ZOOMIES_*` variable (`TestNoCodeReadsASettingFromTheEnvironment`).
* `updates.mode` is `off | manual | auto`, default `off`. `updates.soak` is a
  duration, default `24h`, `0` allowed, and applies to `auto` only. Both are
  platform-scoped and live.
* Roles: `platform` changes the mode and updates the controller; `admin`
  updates hosts and checks for a release; `operator` and `viewer` read. With the
  mode `off` every action refuses with `update.mode_off`.
* An eligible release has a tag matching `^v[0-9]+\.[0-9]+\.[0-9]+$`, is neither
  a prerelease nor a draft, and carries `checksums.txt` and the binary for the
  host's OS and architecture. `manual` takes the newest. `auto` takes it once
  it has been public for `updates.soak`, counted from `published_at`, and a
  newer release restarts the wait.
* A host's target is the controller's running release, never "latest". A build
  that is not from a release is never updated by this feature.
* The helper request carries `"v": 1`, is at most 4 KiB and holds no URL, path,
  command or flag. The helper allows at most two attempts per tag and one per
  ten minutes, keeps its state outside the shared folder, and never runs
  `zoomies upgrade` with `--yes`.
* An attempt times out after 45 minutes. After a failure the planner waits 30
  minutes; after two failures of one tag it waits for an operator.
* Every `*.updated` event is the resource's `GET` shape, published through the
  controller's helpers and never from a store row. The controller never dials an
  agent.
* A new migration takes the next unused prefix, alone, and is appended to
  `shippedMigrations`; a shipped migration is never edited or renamed.
* Every endpoint change updates `api/openapi.yaml`, regenerates
  `internal/api/openapi_spec.go` (`go run internal/api/gen_openapi.go`) and
  `web/src/lib/api/schema.d.ts` (`make openapi`), and touches
  `docs/api-surface.md`. Every new problem code, metric and command gets its row
  on the page `internal/docs` tests.
* Voice: British spelling; `--` in Go and TypeScript comments and `—` in
  Markdown; comments say why; error messages tell a person what to do; test
  names are sentences about the behaviour; commit messages are imperative
  sentences in plain prose with no prefix.
* UI: no raw colour and no repeated literal (tokens live in
  `web/src/lib/styles/tokens.css`), no new library, the app shell stays under
  200 KB gzipped, and every new surface has a Playwright spec that runs on
  desktop and mobile with the accessibility pass.
* Every behavioural test is run once against the code with its rule removed and
  seen to fail (delivery rule 14), and the pull request says which were checked.

## Review Focus

The spec says what the software must do. It is silent on these, and none is an
obvious happy path, so each is pinned by a test in the task that owns the code.

1. **The shared folder cannot be written when a button is pressed** — a
   container without the mount, a full disk, the wrong owner. Expect a refusal
   that names the folder and the fix, no attempt left open, and nothing
   half-written. Task 3.2.
2. **A `result.json` the controller did not ask for** — left from before a
   restart, for an attempt already closed, or for none. Expect it ignored: no
   crash, no attempt reopened or closed twice. A failed result for an open
   attempt closes it with the helper's sentence. Task 3.2.
3. **A host that goes away mid-rollout** — deleted, renamed, re-joined under a
   new id, or lost. Expect its open attempt to close, the rollout not to hang on
   it, and a failure to halt the rollout rather than start the next host. Tasks
   4.3 and 5.3.
4. **Two actors at once** — two administrators, or the planner and a button.
   Expect exactly one open attempt per controller or host, and `update.in_progress`
   for the second, never a second request file. Tasks 3.1 and 3.2.
5. **The mode is switched to `off`, or the helper removed, while something is in
   flight.** Expect in-flight attempts to finish and be recorded, nothing new to
   start, a pending rollout to be cancelled, and the status to say why. Tasks
   3.2 and 5.3.

---
