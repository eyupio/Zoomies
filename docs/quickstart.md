# Quick start

Five minutes, on a fresh Ubuntu, Debian, Fedora or Alpine host. macOS works too
for running a controller in development.

## 1. Install

```sh
curl -fsSL https://zoomies.sh/install.sh | sh
```

The script is POSIX `sh`, and it is written to be read before it is run — which
is the way we would rather you did it:

```sh
curl -fsSLO https://zoomies.sh/install.sh
less install.sh
sh install.sh
```

It works out your OS and architecture, your distribution and init system,
whether Docker or Podman is present and whether its socket is actually
reachable, whether that socket is rootless, which `compose` command you have,
whether ports 8080 and 443 are free, and whether Zoomies is already installed —
in which case it upgrades in place and says so. The download is verified against
the release's `checksums.txt`, and a mismatch refuses to install rather than
warning.

Everything it discovered is handed to `zoomies init`, so the interactive setup
never asks a question the script already answered.

## 2. Choose how it runs

`zoomies init` offers only what your host can do:

=== "Native"

    The binary under systemd, with a hardened unit. Leanest, starts fastest,
    and needs no container runtime for the controller itself.

=== "Docker Compose"

    Writes a `docker-compose.yml` and a fully populated `.env`, then brings it
    up. Easiest to upgrade and to move to another host.

=== "Docker"

    A single container. Fewest files, but you manage the run command yourself.

Whichever you choose, the containerised deployments write a **fully populated
`.env`** -- no placeholders to go back and fill in. Every variable carries a
one-line comment saying what it is for, the file is `0600` because it holds your
encryption key, and it is written atomically so an interrupted install never
leaves a half-written file that compose would then read.

Re-running the installer over an existing deployment is an upgrade, not a
reinstall: it **keeps the existing encryption key** (minting a new one would
make every stored secret undecryptable) and backs the old file up beside it.

Then it walks the rest: a dedicated service user and directories, an encryption
key (which it will tell you to back up, and say exactly what is lost without),
the runner backend — preferring a rootless Docker or Podman socket, and
spelling out the consequence of each alternative — the bind address and TLS, and
your first administrator account.

## 3. Connect GitHub

Zoomies creates the GitHub App for you through the manifest flow. It opens your
browser at a pre-filled form — and always prints the URL as well, so a headless
host still works — with exactly the permissions it needs and no more:

| Permission | Why |
| --- | --- |
| `organization_self_hosted_runners: write` | register and remove runners (org targets) |
| `administration: write` | the same, for a repository target |
| `actions: read` | read workflow runs and jobs for the fallback poller |
| `metadata: read` | required by GitHub for any App |
| `workflow_job` event | the webhook that makes scaling instant |

Create the App, install it on your organisation, and the credentials come back
to the installer automatically. The private key is sealed with your instance
encryption key before it touches the database, and is never returned by the API.

## 4. Your first pool

A pool says what labels your runners answer to and how many may exist.
On a single-host install, setup creates this one for you once GitHub is
connected -- it is derived from what the host actually is, so the numbers below
are what you get on a 4-CPU Linux box with Docker:

| | |
| --- | --- |
| **Name** | `zoomies-linux-x64` |
| **Labels** | `zoomies-linux-x64` — what your workflows put in `runs-on` — and `zoomies`, which every pool answers to |
| **Backend** | Docker (rootless if available) |
| **Min / max** | `0` / `4` — nothing idle when nothing is queued; the max is the host's capacity |
| **Idle timeout** | `5m` |
| **Ephemeral** | yes |
| **Docker in jobs** | none |

Decline it, or set `pool.skip` in an answer file, and the Pools page starts
empty; nothing runs until a pool exists. Either way, always set a maximum. It
is your only backstop against a runaway workflow.

## 5. Run something

```yaml
jobs:
  build:
    runs-on: zoomies-linux-x64
    steps:
      - uses: actions/checkout@v4
      - run: make test
```

One label is enough, and it is branded on purpose: a reviewer of the pull request
that introduces it can tell at a glance that the job has left GitHub's runners.
`runs-on: zoomies` works too, and means "anywhere in this fleet".

Push it. Zoomies sees the `workflow_job` webhook, starts a runner, and you watch
the whole thing happen on the Overview page without refreshing — including the
scheduler's reasoning, in its own words:

```
scaled zoomies-linux-x64 0 -> 1: 1 job queued > 30s
```

## Moving the rest of your repositories

Editing every workflow by hand is the part nobody does. **Migrate** in the
navigation reads the workflows in the repositories your App can see, rewrites
their `runs-on` lines, shows you the exact diff, and opens one pull request per
repository. See [Migrating repositories](migration.md).

## Adding another host

Generate a join token in the UI under **Hosts → Add a host**, or on the CLI, and
run the one line it gives you on the new machine:

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- \
  --mode agent \
  --controller https://zoomies.example.com \
  --join-token zoojoin_...
```

Join tokens are single-use and short-lived. The agent connects outbound only, so
the new host needs no inbound firewall rule.

## Unattended installs

Every prompt has a flag, and there is an answer file for the rest:

```sh
sh install.sh --non-interactive --answers zoomies-answers.yaml
```

`zoomies init --print-answers` writes a commented template. In non-interactive
mode a missing required answer is an error naming the key and what it is for,
never a silent default.

## If something is wrong

```sh
zoomies status          # the Overview, in a terminal
zoomies config check    # validate the config without starting anything
journalctl -u zoomies -n 50
```

The Overview's problems panel, `GET /api/v1/problems` and `zoomies status` all
render the same list, each entry with what is true, why it matters and what to
change. When there is nothing wrong it is one quiet line.

### A job that sits in the queue

Two different faults look the same from GitHub, and the problems panel tells
them apart:

* **No pool claims the job.** Its `runs-on` labels match no enabled pool. Change
  the workflow's labels, or the pool's.
* **No host can run the pool.** A pool is claiming the job and nothing in the
  fleet offers its backend, matches its host selector, or has room left. The
  panel names which, and repeats what the host's own agent said -- an
  unreadable `docker.sock` is the usual answer, and it is fixed on the host
  rather than in the pool.

A host whose Docker daemon was not up when the agent started re-probes as it
runs, so it starts taking work within a heartbeat of the daemon appearing. What
each host can currently run, and why it cannot run the rest, is on the Hosts
page.

## Next

- [Configuration](configuration.md) — every setting, including running behind Cloudflare
- [Security](security.md) — the threat model, and what each dangerous toggle costs
- [Architecture](architecture.md) — how the pieces fit
- [API](api-surface.md) — the REST surface the UI and CLI both use
