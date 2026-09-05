# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

Zoomies is a self-hosted GitHub Actions runner fleet controller: one Go binary
that watches for queued jobs, starts an ephemeral runner container per job, and
destroys it when the job finishes. SQLite for state, a Svelte 5 UI embedded with
`go:embed`, no Kubernetes and no database server.

`cmd/zoomies` is the only binary. It is the controller (`zoomies controller`),
the agent (`zoomies agent`), the installer (`zoomies init`) and the CLI, chosen
by subcommand. On a single VM the controller runs an agent inside itself, so the
whole system is one process and one SQLite file.

Read [docs/architecture.md](docs/architecture.md) before changing anything
structural — it explains the shape and the reasons for it.

## Commands

```sh
make build-nogui   # build without rebuilding the UI -- the fast inner loop
make build         # build the UI and embed it (needs npm)
make test          # go test -race -count=1 ./...
make lint          # go vet, gofmt check, staticcheck if present, UI lint
make fmt           # gofmt + prettier
make dev           # controller with auth off, on :8080, for UI work
make ui-dev        # Vite dev server proxying /api to that controller
make test-ui       # Playwright against a real built binary
make openapi       # regenerate the UI's TypeScript client from api/openapi.yaml
```

`go build ./...` fails on a clean checkout: `internal/api` embeds
`internal/api/webdist`, which is a build product. Run `make build-nogui` once —
it writes a placeholder — before any Go command that compiles that package. CI
does the same thing as its first step.

Go 1.26, Node 22. Node is a build-time dependency only; the shipped binary is
self-contained and static (`CGO_ENABLED=0`, pure-Go SQLite).

Run a single test with `go test -run TestName ./internal/pkg/`. `make test-e2e`
needs real GitHub credentials and skips itself without them.

## Package boundaries

These are load-bearing. Code that crosses them is the main thing to catch in
review.

| Package | Rule |
| --- | --- |
| `internal/store` | The **only** place SQL is written. Domain types, embedded migrations, every query. No other package imports `database/sql`. |
| `internal/scheduler` | **Pure.** `Decide` takes a snapshot and returns a `Plan`. No clock reads, no database, no network — that is what makes scaling behaviour testable, and it is where every decision's operator-facing *reason string* comes from. |
| `internal/api` | Transport only. A handler reads a request, asks the controller / auth / store, and renders the shape `api/openapi.yaml` promises. It has no opinions about the fleet. |
| `internal/controller` | Wiring: the reconcile loop, webhook ingest, the agent task queue, the log relay. |
| `internal/config` | `zoomies.yaml` + `ZOOMIES_*` overrides, and the validator. |
| `internal/github` | App auth, JIT configs, webhook validation, the fallback poller, and `fake.go`, a fake GitHub used by tests. |
| `internal/backend` | Docker, Podman, bare process. The Docker API is hand-rolled `net/http` against the Engine API on purpose (see below). |
| `internal/agent` | The runner-executing half and its outbound transport. |

Other invariants worth knowing before you edit:

* **One writer.** `store.Store` funnels writes through a single connection
  behind a mutex, with a separate pooled reader in WAL mode. Do not add a second
  writer; `database is locked` is designed out of this codebase.
* **The runner state machine is enforced in the store**, not the caller.
  `provisioning → registering → idle ⇄ busy → draining → removed`, plus
  `failed`. Go through `store.TransitionRunner`; an agent must not be able to
  report a nonsensical state and corrupt fleet accounting.
* **The controller never dials an agent.** Agents connect outbound only (long-poll
  for tasks, POST results), so a host behind NAT needs no inbound rule. That is
  why log streaming is inverted: the controller queues a `stream_logs` task and
  the agent opens a chunked POST that gets relayed to the browser's SSE stream.
* **Webhook deliveries are at-least-once and can arrive out of order.** The jobs
  upsert refuses to move a job backwards through its lifecycle. Keep it that way.
* **Sentinel errors** from the store: `ErrNotFound`, `ErrConflict`,
  `ErrInvalidTransition`. Match with `errors.Is`.
* **IDs are prefixed** (`pool_`, `run_`, `job_`, `usr_`…) via `store.NewID`, so a
  pasted ID is self-describing in a log line or bug report. Add new prefixes to
  `internal/store/ids.go`.

## Things CI will fail you on

Beyond tests and lint, several files must stay in sync with their sources. Each
has a CI job that diffs them:

* `internal/api/openapi_spec.go` is generated from `api/openapi.yaml`. After
  editing the spec, run `go run internal/api/gen_openapi.go` from the repo root.
* `web/src/lib/api/schema.d.ts` is generated from the same spec. Run
  `make openapi` and commit the result.
* `install.sh` at the repo root is copied verbatim to the site root — the script
  people `curl` is the script a contributor edits. Do not create a second copy.
* The app shell must stay under **200 KB gzipped** (`web/vite.config.ts`
  enforces it, the number is documented in `docs/ui-guidelines.md`). Route chunks
  are excluded, so move weight to a lazily loaded route rather than raising the
  budget.
* `go mod tidy` must leave `go.mod`/`go.sum` unchanged, and `gofmt -l` must be
  empty.
* `mkdocs build --strict` — a docs link that points nowhere fails the build. The
  site workflow also checks that `sitemap.xml` and `llms.txt` came out of it,
  both generated (by `overrides/sitemap.xml` and `hooks/seo.py`) rather than
  written, so a build that quietly stopped producing one would otherwise ship.

## Configuration

Every setting is a `zoomies.yaml` key with a `ZOOMIES_*` environment override
registered in `applyEnv` (`internal/config/config.go`). Adding a key means
adding both, plus a row in `docs/configuration.md`.

`config.Validate` returns `Finding`s split into two kinds, and the distinction
matters: **errors** stop startup with a message saying what to change;
**warnings** never stop anything but each one names a setting that weakens the
default posture. The same list is printed at startup and rendered in the UI's
problems panel. If you add a setting that can make the deployment less safe, add
the warning too — silent dangerous toggles are the thing this design exists to
prevent. `docs/security.md` explains what each one costs.

The safe configuration is the default: loopback bind, auth on, ephemeral
runners, no Docker socket in jobs, no root.

## The UI

`web/` is Svelte 5 (runes), Tailwind v4, Vite, TypeScript, built straight into
`internal/api/webdist` and embedded.

* **Never write a raw hex, px or ms value in a component.** All design tokens
  live in `web/src/lib/styles/tokens.css` and are consumed by Tailwind through
  `@theme`. [docs/ui-guidelines.md](docs/ui-guidelines.md) is the contract, and
  UI changes should keep it true.
* Status colours are a fixed mapping (idle, busy, pending, draining, danger,
  neutral). Operators learn them; do not reuse them for anything else.
* No state-management library (runes are it), no client-side router
  (`web/src/lib/router.ts` is ours), no charting library (sparklines and bars are
  inline SVG). These are deliberate — see `docs/dependencies.md`.
* Nothing is reachable from the UI that is not reachable from the REST API. If a
  page needs data, it comes from a documented route in
  [docs/api-surface.md](docs/api-surface.md).
* Playwright specs in `web/tests/` run against the real binary, including
  accessibility and mobile passes.

## Dependencies

Every dependency carries a one-line justification in
[docs/dependencies.md](docs/dependencies.md), and that is enforced by review.
Before adding one: could the standard library or fifty lines of our own do it?
Is it maintained? What does it pull in transitively? What does it cost against
the shell budget if it ships to the browser? Then add the row, in the same
voice — *why*, not *what*.

Some omissions are deliberate and documented there, notably
`github.com/docker/docker` (the hand-rolled Engine API client in
`internal/backend/dockerapi.go` is a few hundred lines and gets Podman almost
free, because `podman.sock` speaks the same protocol).

## Testing

Table-driven and standard-library `testing` throughout; no assertion framework.
Tests use `:memory:` SQLite stores, injected clocks (`store.Options.Now`), and
the fake GitHub in `internal/github/fake.go` rather than network calls. Helper
constructors take `t` and call `t.Helper()` / `t.Cleanup`. Coverage is broad —
about 70 test files against 185 Go files — so a new behaviour is expected to
arrive with one.

Test names are sentences about the behaviour, not the method
(`TestOpenCreatesTheDatabaseDirectory`), and a test often carries a comment
explaining *why the behaviour matters*, not what the code does.

## Voice

The prose in this repository — comments, docs, error messages, commit messages —
has a consistent voice, and matching it is part of a change looking finished.

* **British spelling** in prose: *behaviour*, *authorisation*, *organisation*,
  *utilisation*, *licence*. (JSON field names mirroring GitHub's API stay
  American — `organization` in a webhook payload is correct as-is.)
* **Comments explain why, not what.** The interesting ones name the failure mode
  being designed out, or the trade-off taken. Follow that; do not narrate code.
* **Error and warning messages are written for a person to act on.** An operator
  who gets a 403 should be told which role they are missing. Findings say what
  to change; API errors carry a human message and a stable code.
* `--` is used for an em dash in Go comments.
* **Diagrams are Mermaid**, in a `mermaid` fenced block beside the prose they
  explain -- never ASCII art. The site renders them (`mkdocs.yml` registers the
  fence) and so does GitHub, so a diagram lives in the Markdown it belongs to and
  there is no exported image to go stale. Do not colour one by hand: Material
  themes it for light and dark, and a hard-coded fill is wrong in one of them.
* **Commit messages are imperative sentences in plain prose**, sentence case, no
  Conventional Commits prefix: *"Say why a pool has no host, and let a host say
  it can run again"*, *"Make both halves of 'no capacity' something an operator
  can do"*.

## Layout

```
cmd/zoomies         the binary: controller, agent, init, CLI
internal/store      domain model, SQLite schema, every query
internal/config     zoomies.yaml + env, and the validator that warns
internal/scheduler  pure scaling decisions and label matching
internal/github     App auth, JIT configs, webhooks, the fallback poller
internal/backend    Docker, Podman and bare-process runner backends
internal/auth       identity, RBAC, tokens, audit, OIDC
internal/api        REST, SSE, metrics, and the embedded UI
internal/controller the reconcile loop and the agent task queue
internal/agent      the runner-executing half
internal/installer  zoomies init / uninstall / agent join
internal/cryptox    AES-256-GCM at rest, argon2id, token hashing
internal/events     in-process pub/sub that the SSE endpoint fans out
internal/migrate    rewriting workflows' runs-on lines
web/                the Svelte 5 UI
api/openapi.yaml    the API contract both clients are generated from
deploy/             images, compose, systemd units
docs/               the zoomies.sh site, built by mkdocs.yml
overrides/          the site's theme overrides: sharing tags, structured data, sitemap
hooks/              the site's build-time SEO metadata: git dates and llms.txt
install.sh          the one-line installer, served from the site root
```
