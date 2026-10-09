# Agent guidance

Shared guidance for coding agents working in this repository.

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
structural; it explains the shape and the reasons for it.

## Commands

```sh
make build-nogui   # build without rebuilding the UI (the fast inner loop)
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
`internal/api/webdist`, which is a build product. Run `make build-nogui` once,
it writes a placeholder, before any Go command that compiles that package. CI
does the same thing as its first step.

Go 1.27.1 or later (`go.mod` sets the floor; CI builds with 1.27.2), Node 22 or
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
| `internal/scheduler` | **Pure.** `Decide` takes a snapshot and returns a `Plan`. No clock reads, no database, no network; that is what makes scaling behaviour testable, and it is where every decision's operator-facing *reason string* comes from. |
| `internal/updates` | **Pure.** `Choose` takes a list of releases, the mode, the soak and the instant, and picks the release the mode would take and says why. It imports the standard library and `internal/version` and reads no clock; `purity_test.go` enforces both. The planner for automatic updates will join it. |
| `internal/updates/channel` | The impure half of `internal/updates`: finds the update folder, writes a request into it and reads what the helper left. It is the only place the service touches that folder, and it never creates it (the installer does, with the service account's ownership). |
| `internal/api` | Transport only. A handler reads a request, asks the controller / auth / store, and renders the shape `api/openapi.yaml` promises. It has no opinions about the fleet. The resource views themselves (`HostView`, `PoolView`, …) are `internal/controller/views.go` types the handlers alias, because the event stream renders the same JSON and is fed from the controller. |
| `internal/provider` | The infrastructure-provider contract, a fake that obeys it, and `RunContractTests`, the conformance suite every provider passes. It may import `internal/store`'s domain types and `config.Finding` and **nothing else**; two tests enforce that, and that it names nothing provider-specific. Renting a machine is not placing a runner: this is separate from `internal/backend` on purpose. |
| `internal/provider/proxmox` | The first provider. The Proxmox VE API is hand-rolled `net/http` for the same reason the Docker one is. |
| `internal/controller` | Wiring: the reconcile loop, the machine loop, webhook ingest, the agent task queue, the log relay, and every payload the event stream carries (`views.go`, `derived.go`). The machine loop is **not** part of `Reconcile`: a clone takes minutes and `reconcileMu` is held for a whole scheduling pass. |
| `internal/config` | `zoomies.yaml` + `ZOOMIES_*` overrides, and the validator. |
| `internal/github` | App auth, JIT configs, webhook validation, the fallback poller, and `fake.go`, a fake GitHub used by tests. |
| `internal/backend` | Docker, Podman, bare process. The Docker API is hand-rolled `net/http` against the Engine API on purpose (see below). |
| `internal/agent` | The runner-executing half and its outbound transport. |
| `internal/mcp` | The MCP tools and both transports (stdio for `zoomies mcp`, Streamable HTTP for `/mcp`). A tool calls the REST API through the `mcp.API` interface and nothing else (no store, no controller), so it can never do what the caller's token could not. |

Other invariants worth knowing before you edit:

* **The controller never dials an agent.** Agents connect outbound only (long-poll
  for tasks, POST results), so a host behind NAT needs no inbound rule. That is
  why log streaming is inverted: the controller queues a `stream_logs` task and
  the agent opens a chunked POST that gets relayed to the browser's SSE stream.
* **Webhook deliveries are at-least-once and can arrive out of order.** The jobs
  upsert refuses to move a job backwards through its lifecycle. Keep it that way.
* **Every `*.updated` event is the resource's `GET` shape.** The UI drops a frame
  straight into its cache, so a `host.updated` carrying a bare store row (no
  `healthy`, no `free`) repaints the host wrong. Publish through the
  controller's `publish*`/`Publish*` helpers, which render the view; never put a
  store row on the bus. `stats`, `problems.updated` and each host's view are
  computed after every pass and sent only when they change, so a new kind of
  problem (or a host whose slots or heartbeat moved with no row written)
  needs no publish call of its own.
Read the scoped guidance when working in these directories:

* [`internal/store/AGENTS.md`](internal/store/AGENTS.md): one writer, the runner state machine, sentinel errors, IDs.
* [`internal/config/AGENTS.md`](internal/config/AGENTS.md): configuration: settings live in the database.
* [`web/AGENTS.md`](web/AGENTS.md): the UI: tokens, status colours, no new libraries.
* [`internal/hosttune/AGENTS.md`](internal/hosttune/AGENTS.md): host health and tuning: consent, never tune from an upgrade, never restart Docker under work.
* [`.github/AGENTS.md`](.github/AGENTS.md): "Things CI will fail you on": the files CI diffs against their sources.

## Dependencies

Every dependency carries a one-line justification in
[docs/dependencies.md](docs/dependencies.md), and that is enforced by review.
Before adding one: could the standard library or fifty lines of our own do it?
Is it maintained? What does it pull in transitively? What does it cost against
the shell budget if it ships to the browser? Then add the row, in the same
voice, *why*, not *what*.

Some omissions are deliberate and documented there, notably
`github.com/docker/docker` (the hand-rolled Engine API client in
`internal/backend/dockerapi.go` is a few hundred lines and gets Podman almost
free, because `podman.sock` speaks the same protocol).

## Testing

Table-driven and standard-library `testing` throughout; no assertion framework.
Tests use `:memory:` SQLite stores, injected clocks (`store.Options.Now`), and
the fake GitHub in `internal/github/fake.go` rather than network calls. Helper
constructors take `t` and call `t.Helper()` / `t.Cleanup`. Coverage is broad
(roughly one test file for every source file) so a new behaviour is expected to
arrive with one.

Test names are sentences about the behaviour, not the method
(`TestOpenCreatesTheDatabaseDirectory`), and a test often carries a comment
explaining *why the behaviour matters*, not what the code does.

## Voice

The prose in this repository (comments, docs, error messages, commit messages)
has a consistent voice, and matching it is part of a change looking finished.

* **British spelling** in prose: *behaviour*, *authorisation*, *organisation*,
  *utilisation*, *licence*. (JSON field names mirroring GitHub's API stay
  American: `organization` in a webhook payload is correct as-is.)
* **Comments explain why, not what.** The interesting ones name the failure mode
  being designed out, or the trade-off taken. Follow that; do not narrate code.
* **Error and warning messages are written for a person to act on.** An operator
  who gets a 403 should be told which role they are missing. Findings say what
  to change; API errors carry a human message and a stable code.
* **No em dashes.** Do not write `—` anywhere in this repository: prose,
  docs, comments, error messages, UI strings, commit messages or pull request
  text. Use a comma, a colon, a semicolon or parentheses, or split the sentence.
  Do not use `--` as a stand-in either. The one exception is a lone `—` as the
  UI's placeholder for an absent value, which `web/unit/absent-value.test.ts`
  guards. `internal/docs/no_em_dash_test.go` fails the build on a spaced one.
* **Diagrams are Mermaid**, in a `mermaid` fenced block beside the prose they
  explain, never ASCII art. The site renders them (`mkdocs.yml` registers the
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
internal/updates    which release an update mode would take, and why; pure
internal/updates/channel  the update folder: the service's side of the helper's files
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
skills/             what a coding agent installs: the zoomies skill, with a command
                    reference generated from the binary
install.sh          the one-line installer, served from the site root
nixpacks.toml       the build a Nixpacks-based PaaS runs; see docs/paas.md
```

## Instruction hierarchy

`AGENTS.md` contains shared engineering guidance. Claude imports it through
`CLAUDE.md`. Scoped `AGENTS.md` files add detail beside the code; their Claude
wrappers import the same rules. Fix contradictions rather than relying on load
order. Keep entry points concise and put retrieval detail in the context guide.

## Host health implementation

Follow the consent rules in `internal/hosttune/AGENTS.md` and
`docs/host-health.md`. Upgrades are doctor-only. Container health comes from
the native host binary; no privileged container mounts. All host-tuning tests
use injected filesystems and commands. Preserve reversal records on uninstall.

<!-- zoomies-ai-context:start -->

## Zoomies AI Context

Before using prepared repository context, read `.zoomies/AI_CONTEXT.md`. It explains available access routes, revision checks and selective retrieval.

<!-- zoomies-ai-context:end -->
