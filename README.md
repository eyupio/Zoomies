<div align="center">

<img src="docs/brand/logo-master-dark.png" alt="Zoomies" width="260">

# Zoomies

**Off the lead, on the job.**

A lightweight, self-hosted GitHub Actions runner fleet controller.
Ephemeral runners by default, GitHub App auth, webhook-driven autoscaling,
multi-host agents, a live web UI, and a one-line installer.

Single Go binary. SQLite. No Kubernetes.

[zoomies.sh](https://zoomies.sh) ·
[Quick start](#quick-start) ·
[How it works](docs/architecture.md) ·
[Security](docs/security.md) ·
[Configuration](docs/configuration.md) ·
[API](docs/api-surface.md) ·
[Brand](docs/brand.md)

</div>

---

## What it does

You point Zoomies at a GitHub organisation. It watches for queued jobs, starts a
fresh runner for each one, and destroys the runner when the job finishes.

```
  job queued ──▶ webhook ──▶ scheduler decides ──▶ agent starts a container
                                                        │
  job done ◀── runner exits ◀── GitHub assigns the job ◀─┘
```

* **Ephemeral by default.** One job per runner. Nothing leaks from one workflow
  run to the next.
* **No pasted tokens.** Zoomies authenticates as a GitHub App and mints
  single-use JIT registrations itself. github.com and Enterprise Server.
* **Event-driven.** `workflow_job` webhooks, with polling only as a fallback so
  a misconfigured webhook does not silently stop your fleet.
* **Multi-host.** One controller, any number of agents. Agents connect outbound
  only, so a host behind NAT needs no inbound rule.
* **Actually observable.** SQLite for state, Prometheus metrics, structured
  logs, live log streaming, job history with queue waits, and an audit row for
  every mutating action.
* **Safe by default.** Loopback bind, auth on, no Docker socket in your jobs, no
  root. Every deviation is named at startup and in the UI.

## Quick start

Five minutes on a fresh Ubuntu, Debian, Fedora or Alpine host.

```sh
curl -fsSL https://zoomies.sh/install.sh | sh
```

The script detects your OS, architecture, container runtime and init system,
then hands off to `zoomies init`, which walks you through:

1. **Install mode** — single VM with an embedded agent, controller only, or
   agent only.
2. **Binary** — downloaded to `/usr/local/bin` and checksum-verified.
3. **Service user and directories** — a dedicated unprivileged `zoomies` user.
4. **Backend** — rootless Docker if it finds one, otherwise Docker, Podman or
   bare process, with the trade-off spelled out.
5. **Bind address and TLS** — loopback, a certificate you provide, a self-signed
   one, or reverse-proxy mode.
6. **GitHub App** — opens your browser at a pre-filled App manifest with exactly
   the permissions Zoomies needs and the webhook URL already set. Create it, and
   the credentials come back to the installer automatically.
7. **First admin account.**
8. **Service** — a hardened systemd unit (or launchd, or a printed
   `docker-compose.yml`), started and health-checked.

It finishes by telling you the URL, your login, and a first pool suggestion
based on the host's architecture and backend.

Prefer to read it first? That is the intended way:

```sh
curl -fsSLO https://zoomies.sh/install.sh
less install.sh
sh install.sh
```

Automating it? Every prompt has a flag:

```sh
sh install.sh --non-interactive --answers zoomies-answers.yaml
```

### Docker Compose instead

```sh
git clone https://github.com/eyupio/zoomies && cd zoomies
cp .env.example .env
$EDITOR .env          # set ZOOMIES_ENCRYPTION_KEY
mkdir -p data && chown 65532:65532 data   # the container runs as 65532
docker compose up -d
```

The database and the runners' work area live in `./data` beside the compose
file, so a backup is a copy of a directory. It has to be owned by uid 65532: a
bind mount keeps the host directory's ownership, and the container is not root.

The compose file is set up for running behind Cloudflare: the origin serves
plain HTTP on port 80 and Cloudflare terminates TLS, with `ZOOMIES_EXTERNAL_URL`
set to the https address so cookies, webhook URLs and links are all correct, and
Cloudflare's ranges in `ZOOMIES_TRUSTED_PROXIES` so the audit log records real
client addresses rather than Cloudflare's. Firewall the origin to Cloudflare, or
use a Tunnel and publish no port at all. See
[docs/configuration.md](docs/configuration.md#behind-cloudflare-or-any-reverse-proxy).

### Add another host

Generate a join token in the UI (**Hosts → Add a host**) or on the CLI, then run
the one line it gives you on the new machine:

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- \
  --mode agent \
  --controller https://zoomies.example.com \
  --join-token zoojoin_...
```

## Your first pool

A pool says what labels your runners answer to and how many may exist.
On a single-host install, setup creates this one for you once GitHub is
connected -- it is derived from what the host actually is, so the numbers below
are what you get on a 4-CPU Linux box with Docker:

| | |
| --- | --- |
| **Name** | `linux-x64` |
| **Labels** | `linux-x64` — what your workflows put in `runs-on` |
| **Backend** | Docker (rootless if available) |
| **Min / max** | `0` / `4` — nothing idle when nothing is queued; the max is the host's capacity |
| **Idle timeout** | `5m` |
| **Ephemeral** | yes |
| **Docker in jobs** | none |

Then in a workflow:

```yaml
jobs:
  build:
    runs-on: [self-hosted, linux-x64]
    steps:
      - uses: actions/checkout@v4
      - run: make test
```

Push it. Zoomies sees the `workflow_job` webhook, starts a runner, and you watch
the whole thing happen on the Overview page without refreshing.

## The UI

Eight pages, one job each: **Overview** (fleet health, queue depth, scaling
decisions in plain words, and a problems panel that is quiet when nothing is
wrong), **Pools**, **Runners**, **Jobs**, **Hosts**, **Installations**,
**Audit**, **Settings**.

Light and dark, keyboard-driven, a `⌘K` command palette, live everywhere, and a
log viewer that handles a hundred thousand lines. Design system in
[docs/ui-guidelines.md](docs/ui-guidelines.md), identity in
[docs/brand.md](docs/brand.md).

## The CLI

The CLI and the UI are both clients of the same REST API. Nothing is reachable
from one that is not reachable from the other.

```sh
zoomies status                       # the Overview, in a terminal
zoomies pools list
zoomies pools create --name linux-x64 --labels linux-x64 --max 8
zoomies runners list --state busy
zoomies runners drain run_k3f9qz2m
zoomies runners logs run_k3f9qz2m --follow
zoomies jobs list --repo acme/widgets --since 24h
zoomies hosts join-token create --ttl 15m
zoomies audit tail
```

```sh
export ZOOMIES_URL=https://zoomies.example.com
export ZOOMIES_TOKEN=zoo_...
```

## Configuration

One `zoomies.yaml`, every key overridable with a `ZOOMIES_*` environment
variable. It is validated on startup, and the errors tell you what to change:

```
configuration is not valid:
  - server.tls.mode: "selfsigned" is not a TLS mode
      fix: use "off", "self-signed" or "files".
```

Warnings are separate from errors and never stop startup, but each one names a
setting that weakens the default posture:

```
[warning] listening on 0.0.0.0:8080 without TLS -- session cookies, API tokens
and the GitHub App private key you paste during setup all cross the network in
cleartext. Fix: put a TLS-terminating reverse proxy in front, or set
server.tls.mode to self-signed or files.
```

The same list appears in the UI's problems panel. See
[docs/configuration.md](docs/configuration.md) for every key and
[docs/security.md](docs/security.md) for what each dangerous one costs.

## Requirements

* Linux (amd64 or arm64) for the controller and agents. macOS is supported for
  running the controller in development.
* Docker or Podman for the container backends — **rootless preferred**, and the
  installer looks for a rootless socket first.
* A GitHub App on github.com or GitHub Enterprise Server. The installer creates
  it for you.
* No database server, no Kubernetes, no message queue.

## Building from source

```sh
git clone https://github.com/eyupio/zoomies && cd zoomies
make build        # builds the UI and embeds it
./zoomies version
```

Go 1.26 and Node 22. Node is a build-time dependency only — the binary is
self-contained.

```sh
make test         # Go tests
make lint         # vet, gofmt, staticcheck, UI lint
make test-ui      # Playwright
make dev          # controller with auth off, for UI work
make ui-dev       # Vite dev server against it
```

## How it compares

Zoomies is what you want when [ARC](https://github.com/actions/actions-runner-controller)
is too much machinery — you have a VM or three, not a cluster — but a handful of
hand-registered long-lived runners is too little.

| | ARC | A few static runners | Zoomies |
| --- | --- | --- | --- |
| Needs Kubernetes | yes | no | no |
| Ephemeral runners | yes | rarely | default |
| Autoscaling | yes | no | yes |
| Multi-host | yes | no | yes |
| Auth model | GitHub App | a PAT per runner | GitHub App |
| Web UI | no | sometimes | yes |
| Audit log | no | no | yes |
| To install | Helm, CRDs, a cluster | manual | one command |

## Project layout

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
web/                the Svelte 5 UI
deploy/             images, compose, systemd units
docs/               the zoomies.sh site: architecture, security, UI guidelines,
                    configuration, brand
mkdocs.yml          how docs/ becomes zoomies.sh
```

## The website

[zoomies.sh](https://zoomies.sh) is built from `docs/` by MkDocs, so the site
cannot drift from the repository, and `install.sh` is copied to the site root
from the one in this repository's root -- the script you `curl` and the script a
contributor edits are the same file, and CI asserts they are byte-identical.

```sh
pip install -r docs/requirements.txt
mkdocs serve          # http://127.0.0.1:8000
mkdocs build --strict # a link that points nowhere fails the build
```

## Contributing

Read [docs/architecture.md](docs/architecture.md) first, then
[docs/dependencies.md](docs/dependencies.md) — every dependency needs a
one-line justification, and that is enforced by review. UI changes should keep
[docs/ui-guidelines.md](docs/ui-guidelines.md) true.

## Licence

MIT. See [LICENSE](LICENSE).
