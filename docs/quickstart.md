---
icon: material/rocket-launch-outline
title: "Install self-hosted GitHub Actions runners: quick start"
description: >-
  Install Zoomies and run your first job on a self-hosted ephemeral runner —
  one curl command, one GitHub App, no Kubernetes.
---

# Quick start

On a fresh Ubuntu, Debian, Fedora or Alpine host. macOS works too for running a
controller in development.

One command installs it and asks the questions only a terminal can answer.
Everything after that — GitHub, the first pool, the first job, every host you
add later — happens in the **web UI**, which is where a Zoomies fleet is run.
Prefer a file or a terminal? Each step below says where its Docker Compose,
CLI or API equivalent is, and [Other ways in](#other-ways-in) collects them.

!!! note "Before you start"

    * **A Linux host you can reach.** A small cloud VM, a home server or a spare
      machine. Jobs run in Docker or Podman by default; without either, the
      `process` backend runs them straight on the host.
    * **A GitHub organisation, or a repository on your own account, that you
      own.** Creating the GitHub App and installing it takes an owner's rights,
      so if that is not you, have the owner on hand for step 3.
    * **An address GitHub can reach.** GitHub delivers webhooks to the
      controller, and an App's webhook address is fixed when the App is created.
      A cloud VM's hostname or a tunnel does it. If the controller can only be
      reached from your own network, Zoomies falls back to polling GitHub, which
      reacts in tens of seconds instead of at once; [step 3](#3-connect-github)
      says how.

## Try it first

Not ready to put anything on a server? The demo is the real controller and the
real web UI over a made-up fleet: pools, hosts, runners and a morning's worth of
jobs. It needs no GitHub, no container runtime and no account, it listens on this
machine only, and nothing it creates survives Ctrl-C.

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- \
  --no-init --yes --version dev --prefix "$HOME/.local/bin"
"$HOME/.local/bin/zoomies" demo
```

<!-- `--version dev` is here because `zoomies demo` is newer than the latest
     release. Once a release carries it, drop the flag, so the page does not
     point evaluators at a development build. -->

The first command installs the binary and nothing else, and still checks the
download against the release's `checksums.txt`. The second starts the demo and
opens your browser. Press Ctrl-C to throw it all away, then carry on from step 1.

## 1. Install

<div class="zoomies-install" markdown>

```sh
curl -fsSL https://zoomies.sh/install.sh | sh
```

</div>

The script is POSIX `sh`, and it is written to be read before it is run — which
is the way we would rather you did it:

```sh
curl -fsSLO https://zoomies.sh/install.sh
less install.sh
sh install.sh
```

??? note "What the installer checks, and what it asks first"

    It works out your OS and architecture, your distribution and init system,
    whether Docker or Podman is present and whether its socket is actually
    reachable, whether that socket is rootless, which `compose` command you have,
    whether ports 8080 and 443 are free — where `ss` or `netstat` exists to tell
    it, and it says so when neither does — and whether Zoomies is already
    installed, in which case it upgrades in place and says so.

    The download must verify. It is checked against the release's
    `checksums.txt`, and every way that can fail — a mismatch, no entry for this
    asset, no hashing tool on the host, a checksums file that could not be
    fetched — refuses the install rather than warning about it. A private mirror
    that publishes no checksums is the one supported exception, with
    `--allow-unverified`.

    Before it changes anything it prints what it is about to do — the version,
    where the binary goes, whether it needs `sudo`, and what it leaves alone —
    and asks once. `--yes` and `--non-interactive` skip the question.

    Everything it discovered is handed to `zoomies init`, so the interactive
    setup never asks a question the script already answered.

Not installing on a host you own? A platform that builds with Nixpacks --
Coolify, Dokploy, Railway, Zeabur -- can deploy the controller from the source
instead, with agents joined from machines that have a container runtime. See
[Deploying on a PaaS](paas.md).

## 2. Choose how it runs

`zoomies init` offers only what your host can do:

=== "Native"

    The binary under systemd, with a hardened unit. Leanest, starts fastest,
    and needs no container runtime for the controller itself. **Setup finishes
    here**, including the GitHub App, your administrator and your first pool.

=== "Docker Compose"

    Writes a `docker-compose.yml` and a fully populated `.env`, then brings it
    up. Easiest to upgrade and to move to another host. It is the default
    whenever you have a `compose` command.

=== "Docker"

    A single container. Fewest files, but you manage the run command yourself.

!!! note "The containerised options move the last three steps to the browser"

    A container keeps its database in a volume the installer cannot open, so on
    the compose and docker paths the administrator, the GitHub App and the first
    pool are created in the browser afterwards rather than in the terminal. The
    closing summary prints all three with their exact addresses, and the
    Overview repeats them as a checklist that ticks itself off as you go.

Then it walks the rest: a dedicated service user and directories, an encryption
key (which it will tell you to back up, and say exactly what is lost without),
the runner backend — preferring a rootless Docker or Podman socket, and
spelling out the consequence of each alternative — the bind address and TLS, the
address GitHub will use to reach the controller, and your first account.

??? note "What setup does for you, in more detail"

    Whichever you choose, the containerised deployments leave nothing to fill in.
    A controller's answers are stored in its database before it first starts,
    where the Settings page changes them; the `.env` holds only what opens that
    database and what Compose reads itself. Every variable in it carries a
    one-line comment saying what it is for, the file is `0600` because it holds
    your encryption key, and it is written atomically so an interrupted install
    never leaves a half-written file that compose would then read.

    Re-running the installer over an existing deployment is an upgrade, not a
    reinstall: it **keeps the existing encryption key** (minting a new one would
    make every stored secret undecryptable) and backs the old file up beside it.

    Setup does not assume the service account can reach the container socket, it
    checks. The account's access is worked out from the socket's own owner and
    mode rather than from a group called `docker`, which is the wrong group on a
    Podman socket or a distribution that names it something else; the account is
    added to whichever group that is; and the check runs **again** afterwards, so
    an install only reports success it has verified. Where joining a group cannot
    help — a socket with no group permissions at all — it says so and names the
    two ways out, instead of leaving you with a fleet that comes up unable to run
    anything.

## 3. Connect GitHub

!!! note "Before you click Connect GitHub"

    GitHub is told where to deliver webhooks when the App is created, and cannot
    be told again afterwards. So Zoomies will not create an App until it knows an
    address GitHub can reach: not `localhost`, and not an address only your own
    network can see. If it does not know one yet, the **Connect GitHub** dialog
    says so and lets you set it there. The address takes effect when the
    controller next restarts, so the dialog shows the command for that and
    unlocks by itself when the controller is back.

    No public address, as in a home lab? Use the dialog's **Existing App** tab
    to connect an App you already have instead. Zoomies then polls GitHub for
    queued jobs, which reacts in tens of seconds rather than at once.
    [Runners in your home lab](home-lab.md#where-the-controller-goes) says more.

Zoomies creates the GitHub App for you through the manifest flow. It opens your
browser at a pre-filled form — and always prints the URL as well, so a headless
host still works — with exactly the permissions it needs and no more:

| Permission | Why |
| --- | --- |
| `organization_self_hosted_runners: write` | register and remove runners (org targets) |
| `administration: write` | the same, for a repository target (a single repository, which is also how a personal account is used) |
| `actions: write` | read workflow jobs and let operators cancel workflow runs from Zoomies; set `github.allow_workflow_cancellation: false` to request read-only access instead |
| `metadata: read` | required by GitHub for any App |
| `contents: write` | read and rewrite workflow files for the [migration wizard](migration.md) |
| `pull_requests: write` | open the migration wizard's pull request |
| `workflows: write` | GitHub requires it specifically to change files under `.github/workflows` |
| `workflow_job` event | the webhook that makes scaling instant |

The last three are the migration wizard's, and they are asked for now rather
than later because later is expensive: adding a permission to an App that
already exists is held by GitHub until the account's owner accepts it on the
installation, and until they do the wizard cannot read a workflow at all. If you
never migrate anything, remove them on the App's **Permissions & events** page —
nothing else in Zoomies writes to a repository.

Create the App, install it on your organisation -- or, for a repository target,
on your own account scoped to that repository, which is how a personal account
is used -- and the credentials come back to the installer automatically. The
private key is sealed with your instance encryption key before it touches the
database, and is never returned by the API.

## 4. Your first pool

A pool says what labels your runners answer to and how many may exist.
On a single-host install made by `zoomies init`, setup creates this one for you
once GitHub is connected -- it is derived from what the host actually is, so the
numbers below are what you get on a 4-CPU Linux box with Docker. The
repository's `docker-compose.yml` has no installer, so on that path you create
it yourself with **Pools → Create a pool**, which opens the same wizard on the
same defaults:

| | |
| --- | --- |
| **Name** | `zoomies-linux-x64` |
| **Labels** | `zoomies-linux-x64` — what your workflows put in `runs-on` — and `zoomies`, which every pool answers to |
| **Platform** | What the host is: Ubuntu 24.04, amd64. It picks the runner image, and it keeps this pool off hosts running something else |
| **Backend** | Docker (rootless if available) |
| **Size per runner** | One share of each host — the machine divided by its slots, as a real limit. A pool made in the wizard also starts with **Elastic CPU** on *Observe only*; the installer's pool starts with it off |
| **Min / max** | `0` / `4` — nothing idle when nothing is queued; the maximum starts at the room those hosts have and stops following once you type your own |
| **Idle timeout** | `5m` |
| **Ephemeral** | yes |
| **Docker in jobs** | none |

Decline it, or set `pool.skip` in an answer file, and the Pools page starts
empty; nothing runs until a pool exists. Either way, always set a maximum. It
is your only backstop against a runaway workflow.

The defaults are the ones to run your first job on. How a pool is sized, Elastic
CPU, the operating system and Docker in jobs can all wait until it has run: they
are under [After your first job](#after-your-first-job).

## 5. Run something

```yaml
jobs:
  build:
    runs-on: zoomies-linux-x64
    steps:
      - uses: actions/checkout@v4
      - run: make test
```

One label is enough to reach a pool, and `runs-on: zoomies` works too, because
every pool answers to that as well. [The labels to give a
pool](configuration.md#the-labels-to-give-a-pool) says why they are branded, and
what to write before anyone has decided which pool a repository belongs in.

Push it. Zoomies sees the `workflow_job` webhook, starts a runner, and you watch
the whole thing happen on the Overview page without refreshing — including the
scheduler's reasoning, in its own words:

```text
scaled zoomies-linux-x64 0 -> 1: 1 job queued
```

The Jobs page keeps the record: how long each job waited, how long it ran,
which runner took it, and — for anything that failed — the step it failed at.

![The Jobs page: queue depth, running jobs, success rate, P95 wait and outcome composition above the job grid](screenshots/jobs-dark.webp#only-dark){ .zoomies-shot }
![The Jobs page: queue depth, running jobs, success rate, P95 wait and outcome composition above the job grid](screenshots/jobs-light.webp#only-light){ .zoomies-shot }

Every one of those rows is also a flag on `zoomies pools create`, and
`--dry-run` gives the wizard's own verdict without creating anything:

```sh
zoomies pools create --name zoomies-linux-x64 --labels zoomies-linux-x64 \
  --installation ins_k3f9qz2m --os ubuntu --os-version 24.04 --max 4 --dry-run
```

## After your first job

The defaults ran it. These are the settings worth a look now that something has.

### Size per runner

This is the row that decides how many jobs run at once, and the default answer is
to leave it to the host: each runner is given one slot's share of whichever
machine it lands on, as a real limit rather than a promise, so the same pool is
sized correctly on a 16-core box and on a 64-core one. There is no “unlimited” —
a runner with no limit takes whatever the machine has while the fleet still
counts it as one slot — and a pool whose jobs need a particular amount of machine
everywhere can say so instead, on the advanced path. Its sliders open on the
figures the whole fleet starts from, under **Settings → Configuration**
(`runners.default_cpus`, `runners.default_memory_mb`).

### Elastic CPU

This is the row to come back to. A pool sized by its host can *observe*: on every
heartbeat the controller works out how much of the host's spare CPU a busy runner
could have been lent, with every other runner's guarantee and the next queued
job's room held back, and publishes the answer to Prometheus without moving
anything. A pool made in the wizard starts there; the installer's pool starts
with it off, and **Edit** on the pool's page turns it on. Switch it to *Automatic
boost* once the numbers say there is room, and a job that was compiling inside
two cores gets the rest of the machine until something else wants it.
[Elastic CPU zoomies](elastic-cpu.md) is the whole of it.

### Operating system

The other row worth a look. It picks which `zoomies-runner` variant the pool
boots — [the catalogue](naming.md#the-runner-image) lists what is published — and
it stops the scheduler putting these runners on a host running something else.
Leave it as *Any* and the pool takes the controller's default image and any host
that fits otherwise. See [Naming and platforms](naming.md).

### Docker in jobs

This stays `none` until a workflow needs a daemon — a `docker` step, a
`container:` or a `services:` block — and then `dind` is the one to choose. That
one setting is enough: the pool is switched to a runner image with a Docker
client as it is saved. [Jobs that build container
images](configuration.md#jobs-that-build-container-images) says what it costs.

### The names the wizard offers

The wizard does not make you invent the first two rows. It opens with a name
already in the field — the brand and the shape of the pool, so
`zoomies-ubuntu-2404`, or a name from the kennel before a host has connected —
and a label derived from that name, so the pool is reachable by a workflow before
you have typed anything. The dice beside the field roll another name; type over
it and the wizard leaves the name and the label alone from then on. Every name it
offers starts with `zoomies-`, which is what tells you a runner in GitHub's own
settings is one of yours.

## Moving the rest of your repositories

Editing every workflow by hand is the part nobody does. **Migrate** in the
navigation reads the workflows in the repositories your App can see, rewrites
their `runs-on` lines, shows you the exact diff, and opens one pull request per
repository. See [Migrating repositories](migration.md).

## Adding another host

**Hosts → Add a host** does the whole thing on one page. It comes filled in --
the address your browser reached the controller on, the labels your pools
already select hosts by, capacity left for the agent to decide -- and hands you
one line to paste into a shell on the new machine:

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- \
  --mode agent \
  --controller https://zoomies.example.com \
  --join-token zoojoin_...
```

Leave the page open. It watches for the host and says the moment it has
joined: what the machine is, which backends it offers, and which pools can
place runners on it. If the binary is already on that machine, the same page
offers the shorter `zoomies agent join` form, and the CLI can mint a token too
with `zoomies hosts join-token create`.

Join tokens are single-use and short-lived. The agent connects outbound only, so
the new host needs no inbound firewall rule.

[Hosts and pools](hosts-and-pools.md) takes it from here: what a host brings with
it, cordoning one for maintenance, when a second pool is worth having, and the
rules that decide which host a runner lands on.

## Other ways in

The UI is a client of the REST API, and so is everything else, so nothing on
this page needs a browser. Where the same thing lives elsewhere:

| In the UI | Elsewhere |
| --- | --- |
| The installer's questions | [`zoomies init`](cli.md#setting-up-and-looking-around) with `--answers`, or the repository's [`docker-compose.yml`](compose.md) and a three-line `.env` |
| **Installations → Connect GitHub** | The native installer does it on the console; there is no CLI form, because the manifest flow needs a browser |
| **Pools → Create a pool** | [`zoomies pools create`](cli.md#zoomies-pools), or `POST /api/v1/pools` |
| **Hosts → Add a host** | [`zoomies hosts join-token create`](cli.md#zoomies-hosts), then `zoomies agent join` on the new machine |
| **Settings → Configuration** | `zoomies.yaml`, a `ZOOMIES_*` variable, or [`zoomies config set`](cli.md#setting-up-and-looking-around) with the controller stopped |
| The Overview, the Jobs page, a runner's log | [`zoomies status`](cli.md#zoomies-status), `zoomies jobs list`, `zoomies runners logs --follow` |

[The command line](cli.md) lists every command, and [the API](api-surface.md)
every route with the role it needs.

## Unattended installs

Every prompt has a flag, and there is an answer file for the rest:

```sh
sh install.sh --non-interactive --answers zoomies-answers.yaml
```

`zoomies init --print-answers` writes a commented template. In non-interactive
mode a missing required answer is an error naming the key and what it is for,
never a silent default.

## If something is wrong

Very little of it should be a mystery. The UI, `zoomies status` and
`GET /api/v1/problems` are three windows onto one list of problems, and on a
healthy fleet that list is a single quiet line.

[Troubleshooting](troubleshooting.md) is the page for the rest: the commands to
run first, the five things that go wrong on a first run, and what a job sitting
in the queue is telling you.

## Next

- [The UI](ui.md) — every page you now have, in both themes
- [Hosts and pools](hosts-and-pools.md) — a second machine, a second pool, and how placement is decided
- [Configuration](configuration.md) — every setting, including running behind Cloudflare
- [Deploying on a PaaS](paas.md) — Coolify, Dokploy, Railway and anything else that builds with Nixpacks
- [Troubleshooting](troubleshooting.md) — when a first run does not work, and when a job sits in the queue
- [Security](security.md) — the threat model, and what each dangerous toggle costs
- [Upgrading](upgrading.md) — what an upgrade does to work in flight, and why there is no way back
- [Backup and restore](backup-and-restore.md) — the two files, and bringing a controller back elsewhere
- [Architecture](architecture.md) — how the pieces fit
- [API](api-surface.md) — the REST surface the UI and CLI both use
- [Command line](cli.md), [Problem codes](problem-codes.md), [Metrics](metrics.md) — the reference tables
- [FAQ](faq.md) — what it costs, what it needs, and what it will not protect you from
