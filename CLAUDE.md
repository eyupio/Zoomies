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
make screenshots   # recapture docs/screenshots from the real UI, both themes (needs Pillow)
```

`go build ./...` fails on a clean checkout: `internal/api` embeds
`internal/api/webdist`, which is a build product. Run `make build-nogui` once —
it writes a placeholder — before any Go command that compiles that package. CI
does the same thing as its first step.

Go 1.27.1 or later (`go.mod` sets the floor; CI builds with 1.27.1), Node 22 or
later. Node is a build-time dependency only; the shipped binary is
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
| `internal/api` | Transport only. A handler reads a request, asks the controller / auth / store, and renders the shape `api/openapi.yaml` promises. It has no opinions about the fleet. The resource views themselves (`HostView`, `PoolView`, …) are `internal/controller/views.go` types the handlers alias, because the event stream renders the same JSON and is fed from the controller. |
| `internal/provider` | The infrastructure-provider contract, a fake that obeys it, and `RunContractTests`, the conformance suite every provider passes. It may import `internal/store`'s domain types and `config.Finding` and **nothing else** -- two tests enforce that, and that it names nothing provider-specific. Renting a machine is not placing a runner: this is separate from `internal/backend` on purpose. |
| `internal/provider/proxmox` | The first provider. The Proxmox VE API is hand-rolled `net/http` for the same reason the Docker one is. |
| `internal/controller` | Wiring: the reconcile loop, the machine loop, webhook ingest, the agent task queue, the log relay, and every payload the event stream carries (`views.go`, `derived.go`). The machine loop is **not** part of `Reconcile`: a clone takes minutes and `reconcileMu` is held for a whole scheduling pass. |
| `internal/config` | `zoomies.yaml` + `ZOOMIES_*` overrides, and the validator. |
| `internal/github` | App auth, JIT configs, webhook validation, the fallback poller, and `fake.go`, a fake GitHub used by tests. |
| `internal/backend` | Docker, Podman, bare process. The Docker API is hand-rolled `net/http` against the Engine API on purpose (see below). |
| `internal/agent` | The runner-executing half and its outbound transport. |
| `internal/mcp` | The MCP tools and both transports (stdio for `zoomies mcp`, Streamable HTTP for `/mcp`). A tool calls the REST API through the `mcp.API` interface and nothing else -- no store, no controller -- so it can never do what the caller's token could not. |

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
* **Every `*.updated` event is the resource's `GET` shape.** The UI drops a frame
  straight into its cache, so a `host.updated` carrying a bare store row -- no
  `healthy`, no `free` -- repaints the host wrong. Publish through the
  controller's `publish*`/`Publish*` helpers, which render the view; never put a
  store row on the bus. `stats`, `problems.updated` and each host's view are
  computed after every pass and sent only when they change, so a new kind of
  problem -- or a host whose slots or heartbeat moved with no row written --
  needs no publish call of its own.
* **Sentinel errors** from the store: `ErrNotFound`, `ErrConflict`,
  `ErrInvalidTransition`, and `ErrJoinTokenUsed` / `ErrJoinTokenExpired` for
  the two ways a join token that exists is still refused. Match with
  `errors.Is`. Refusals the auth service makes for a reason the caller can
  act on are `auth.ErrInvalidInput`; the API answers those with a 422 and
  everything else with a 500 and a request ID.
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
* The runner image catalogue in `internal/naming/images.go` is the source for
  both workflows' build matrices, the Makefile's `variant.*` rows and the table
  in `docs/naming.md`. Adding or swapping an operating system is a row there and
  then `make generate`; editing any of the four by hand fails a test in
  `internal/naming`.
* `install.sh` at the repo root is copied verbatim to the site root — the script
  people `curl` is the script a contributor edits. Do not create a second copy.
* The app shell must stay under **200 KB gzipped** (`web/vite.config.ts`
  enforces it, the number is documented in `docs/ui-guidelines.md`). Route chunks
  are excluded, so move weight to a lazily loaded route rather than raising the
  budget.
* `go mod tidy` must leave `go.mod`/`go.sum` unchanged, and `gofmt -l` must be
  empty.
* `govulncheck ./...` must find nothing reachable. It runs in its own workflow,
  on every change and weekly against `main`, so a module found vulnerable after
  it merged still gets reported.
* Every `uses:` in `.github/workflows` is pinned to a commit with its release
  in a comment, and a workflow with more than one job grants no `write`
  permission at the top. Both are tested in `internal/docs`; Dependabot moves a
  pin and its comment together, and a new action gets the same treatment.
* `mkdocs build --strict` — a docs link that points nowhere fails the build. The
  site workflow also checks that `sitemap.xml` and `llms.txt` came out of it,
  both generated (by `overrides/sitemap.xml` and `hooks/seo.py`) rather than
  written, so a build that quietly stopped producing one would otherwise ship,
  and that the header names a release: `hooks/source.py` resolves the latest
  release, stars and forks from the GitHub API at build time, and an offline
  build is allowed to go without them where the published site is not.

## Configuration

**Settings live in the database.** The Settings page writes the
`instance_settings` table and nowhere else, and the controller layers it over
the defaults and `zoomies.yaml` at start (`config.Rebuild`); a `ZOOMIES_*`
variable is the operator's last-resort override on top, not where a setting is
kept. So code that needs a setting's value reads the effective configuration --
the running `*config.Config`, or `config.Effective` for a deployment that is
not this process (the installer upgrading one reads its database read-only) --
and never a `ZOOMIES_*` variable or a `.env` file's copy of one. A value read
from the environment is wrong the moment an operator has used the Settings
page. `TestNoCodeReadsASettingFromTheEnvironment` in `internal/docs` fails on
any such read outside `internal/config`; do not add to its allowlist to get a
change through. The one real exception is a remote agent, which has no
database: its own instance settings come from its file and environment.

The same goes for what the installer writes. A container controller's `.env`
and Compose file carry only what opens the database and what Compose reads
itself; `zoomies init` stores every answer in the database first
(`SettingsEnv`, then `zoomies config import-env` in a one-off container), and
`zoomies upgrade` moves an older deployment's out of its environment
(`internal/installer/envsettings.go`). Never add a stored setting's variable
to the `.env` template or the controller's `environment:` block --
`TestAControllersComposeFileHandsItNoSetting` and
`TestAControllersEnvFileHoldsOnlyWhatOpensItsDatabase` fail if you do.

Every setting is a row in the registry in `internal/config/settings.go`, which
gives it its `zoomies.yaml` key, its `ZOOMIES_*` override and its place on the
Settings page. Adding a setting means adding the row, plus a row in
`docs/configuration.md`.

`config.Validate` returns `Finding`s in three severities, and the distinctions
matter: **errors** stop startup with a message saying what to change;
**warnings** never stop anything but each one names a setting that weakens the
default posture; **info** findings are neither wrong nor risky, and exist for
the defaults that surprise people (`agent.none`, `tls.self_signed`). A few
codes choose their severity from the circumstances -- `auth.disabled` is a
warning on loopback and an error on a public bind.

The same list is printed at startup and rendered in the UI's problems panel,
alongside the problems the running controller raises. If you add a setting that
can make the deployment less safe, add the warning too -- silent dangerous
toggles are the thing this design exists to prevent. Every code needs a row in
`docs/problem-codes.md`, which `internal/docs` tests in both directions;
`docs/security.md` explains what the dangerous ones cost.

The safe configuration is the default: loopback bind, auth on, ephemeral
runners, no Docker socket in jobs, no root.

## The UI

`web/` is Svelte 5 (runes), Tailwind v4, Vite, TypeScript, built straight into
`internal/api/webdist` and embedded.

* **Never write a raw colour in a component, and never write a raw value that
  already has a token.** All design tokens live in
  `web/src/lib/styles/tokens.css`. A colour written by hand only works in one
  theme, so that half is absolute; the rest is a rule about repetition, and a
  value that appears twice belongs in the token file. Media query widths, a
  one-off measure in a component's own layout, and the log viewer's xterm
  bridge are the documented exceptions.
  [docs/ui-guidelines.md](docs/ui-guidelines.md) is the contract, and UI
  changes should keep it true.
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
roughly one test file for every source file — so a new behaviour is expected to
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
* **`--` in code, `—` in Markdown.** Go comments, TypeScript comments and
  terminal output use `--` for an em dash, because a source file is read in a
  terminal as often as in an editor and a literal em dash is one more thing to
  get wrong. Markdown prose uses the character itself: the site renders it, and
  a document is written to be read rather than to be greppable. Strings the UI
  renders are Markdown's side of that line, not code's.
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

```text
cmd/zoomies         the binary: controller, agent, gateway, init, CLI
internal/store      domain model, SQLite schema, every query
internal/config     zoomies.yaml + env, and the validator that warns
internal/scheduler  pure scaling decisions and label matching
internal/github     App auth, JIT configs, webhooks, the fallback poller
internal/backend    Docker, Podman and bare-process runner backends
internal/auth       identity, RBAC, tokens, audit, OIDC
internal/api        REST, SSE, metrics, and the embedded UI
internal/provider   the infrastructure-provider contract, its fake, and Proxmox
internal/controller the reconcile loop, the machine loop and the agent task queue
internal/agent      the runner-executing half
internal/gateway    zoomies gateway: a private provider's end of the tunnel
internal/installer  zoomies init / uninstall / agent join, and the unit,
                    compose and env templates they write
internal/backup     one copy of the database: taking, listing, verifying, archiving,
                    encrypting, shipping it to an S3-compatible remote and restoring it,
                    shared by the CLI, the scheduler and the API
internal/cryptox    AES-256-GCM at rest, argon2id, token hashing
internal/events     in-process pub/sub that the SSE endpoint fans out
internal/migrate    rewriting workflows' runs-on lines
internal/mcp        the fleet over MCP: tools, stdio and Streamable HTTP
web/                the Svelte 5 UI
test/e2e            the Docker end-to-end test, behind the `e2e` build tag
api/openapi.yaml    the API contract both clients are generated from
deploy/             the controller and runner images, and the runner entrypoint
docker-compose.yml  the compose deployment, at the root so it is the one people find
docs/               the zoomies.sh site, built by mkdocs.yml
overrides/          the site's theme overrides: sharing tags, structured data, sitemap,
                    the header's repository facts
hooks/              the site's build-time metadata: git dates, llms.txt, and the
                    latest release and star count the header shows
ROADMAP.md          the follow-on roadmap, and the decisions it asks the owner to take
roadmap/            what supports it: the work-package record, decision records,
                    gate evidence, the model guidance and the source document
install.sh          the one-line installer, served from the site root
nixpacks.toml       the build a Nixpacks-based PaaS runs; see docs/paas.md
```
