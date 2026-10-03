---
icon: material/home
# The header shows the page's title once the headline has scrolled out of view,
# and the headline is too long for it: on a phone it read "Give your GitHub Ac…".
title: Zoomies
social_title: Zoomies — free, open-source self-hosted GitHub Actions runners
description: >-
  Free, open-source self-hosted GitHub Actions runners: a fresh ephemeral
  runner for every job, autoscaling across cloud and private home-lab hosts
  with built-in Tailcat encrypted connections.
hide:
  - navigation
  - toc
---

<div class="zoomies-hero" markdown>

<p class="eyebrow">Free · open source · AGPL-3.0</p>

# GitHub Actions runners <br><span class="quiet">on machines you own.</span>

<p class="lede">
Zoomies starts a fresh runner for every queued job and destroys it when the job
is done. One binary, no Kubernetes, no database server.
</p>

<div class="zoomies-install" markdown>

```sh
curl -fsSL https://zoomies.sh/install.sh | sh
```

</div>

<p class="actions" markdown>
[Get started :material-arrow-right:](quickstart.md){ .md-button .md-button--primary }
[Try the demo — no GitHub needed](quickstart.md#just-looking){ .md-button }
</p>

<p class="prereq">Runs on Linux, with Docker or Podman. Needs a GitHub organisation or repository you own.</p>

<p class="popular" markdown="span">[See it running](ui.md) [Compared with ARC](actions-runner-controller.md) [What it costs](costs.md)</p>

</div>

The project's own CI runs on Zoomies: every job except the arm64 and Windows
legs, in containers the Docker backend started.
[What is and is not qualified](#what-is-qualified).
{ .zoomies-proof }

![The Overview: the activity matrix across the top, then four metric tiles with an hour of sparkline behind each, runner startup and registration times, a one-line problems summary, per-pool utilisation bars and a feed of the fleet's recent events](screenshots/overview-dark.webp#only-dark){ .zoomies-shot }
![The Overview: the activity matrix across the top, then four metric tiles with an hour of sparkline behind each, runner startup and registration times, a one-line problems summary, per-pool utilisation bars and a feed of the fleet's recent events](screenshots/overview-light.webp#only-light){ .zoomies-shot }

The Overview, on a fleet part-way through a morning — every number live, and
nothing to press to keep it that way. [See all twelve pages, in both
themes](ui.md).
{ .zoomies-shot-caption }

## Your home lab belongs in the pack.

**Turn your own private machines into GitHub Actions runner capacity.**
Built-in Tailcat connections bring your home lab and office hosts into the same
fleet as your cloud machines. No public host IP, router port forwarding,
Tailscale account or separate tunnel installation.

Choose **Private connection · Tailcat**, run one command, and watch the host
join the pack. Same web UI, same pools, same ephemeral runners. Your hardware,
your network, without a Zoomies per-minute platform fee.

[Connect your private hosts :material-arrow-right:](private-hosts.md){ .md-button .md-button--primary }

## How it works

Point Zoomies at a GitHub organisation, or at a repository on a personal
account. It watches for queued jobs, starts a fresh runner for each one, and
destroys the runner when the job finishes.

[One fleet for many repositories](many-repositories.md) explains organisation
and personal-account setups, and where runners can safely be shared.

```mermaid
flowchart LR
    q["a job is queued<br/>on GitHub"]
    w["workflow_job<br/>webhook"]
    d["the scheduler decides,<br/>and says why"]
    s["an agent starts<br/>a runner container"]
    a["GitHub hands<br/>the job to it"]
    e["the job finishes,<br/>the container is destroyed"]

    q --> w --> d --> s --> a --> e
```

## What you get

<div class="zoomies-grid" markdown>

<div markdown>
:material-monitor-dashboard:{ .icon }

### A live web UI
Thirteen pages, one job each, and every one of them updates in place from the
controller's event stream — you never have to press refresh, though there is a
button where you want to be sure. Light and dark, a command palette, and a log
viewer built for a hundred thousand lines.
[See every page](ui.md).
</div>

<div markdown>
:material-rabbit:{ .icon }

### Elastic CPU zoomies
Every runner keeps its guaranteed share of its host, and a busy one is lent
the CPU nobody else is using — with the next queued job's room held back, the
host's reserve untouched, and memory never moved. A compile that would run in
under two cores gets four, and gives them back the moment they are wanted.
[How it works](elastic-cpu.md).
</div>

<div markdown>
:material-recycle-variant:{ .icon }

### Ephemeral by default
One job per runner, then the container and its workspace are gone. Selected
downloads and build data can live outside the runner in an optional
[persistent cache](persistent-caches.md); credentials and workspaces are not
reused as caches.
</div>

<div markdown>
:material-shield-key-outline:{ .icon }

### No pasted tokens
Zoomies authenticates as a GitHub App and mints single-use JIT registrations
itself. There is no long-lived token sitting in a dotfile beside a runner.
</div>

<div markdown>
:material-webhook:{ .icon }

### Event-driven
`workflow_job` webhooks, with polling only as a fallback — so a misconfigured
webhook slows your fleet down instead of silently stopping it.
</div>

<div markdown>
:material-server-network-outline:{ .icon }

### Multi-host, multi-OS
One controller, any number of agents, each connecting outbound only — a host
behind NAT needs no inbound firewall rule. Runner images for Ubuntu, Debian,
Fedora and Rocky Linux, and a pool names the machine it needs.
[Which images](naming.md#the-runner-image).
</div>

<div markdown>
:material-chart-timeline-variant:{ .icon }

### Actually observable
SQLite for state, Prometheus metrics, structured logs, live log streaming, job
history with queue waits, and an audit row for every mutating action.
</div>

<div markdown>
:material-backup-restore:{ .icon }

### Backed up by itself
A consistent copy of the database nightly, kept to a ceiling — and sent on to
the S3-compatible destinations you name, sealed with that destination's
passphrase before it leaves the host. A bucket that was unreachable is caught up
with every backup it missed rather than quietly skipping them.
[Backup and restore](backup-and-restore.md).
</div>

<div markdown>
:material-shield-check-outline:{ .icon }

### Safe defaults
Loopback bind, authentication on, no Docker socket in your jobs, no root. Every
deviation is named at startup and in the UI's problems drawer. A self-hosted
runner still runs your repositories' code — [what this protects, and what it
does not](security.md).
</div>

<div class="wide" markdown>
:material-source-pull:{ .icon }

### One pull request per repository
The migration wizard rewrites `runs-on` across your repositories and opens a
pull request on each one — after showing you the exact diff, and only for the
jobs it is sure about. It never pushes to your default branch and never merges
anything. [How it works](migration.md).
</div>

</div>

## From a fresh host to a running fleet

The installer detects your OS, architecture, container runtime and init system,
then walks you through the rest: service user, encryption key, backend, TLS, the
GitHub App — created for you through the manifest flow with exactly the
permissions Zoomies needs, and no write access to your code unless you say you
want the migration wizard — and your first account.

<div class="zoomies-install" markdown>

```sh
curl -fsSL https://zoomies.sh/install.sh | sh
```

</div>

Piping a script into a shell deserves a second look, and this one is written to
survive one — [download it, read it, then run it](quickstart.md#1-install).

It can deploy three ways — the binary under systemd, a `docker compose` stack
with a fully populated `.env`, or a single container — and it will only offer
the ones your host can actually run. See the [quick start](quickstart.md).

## Run it from the browser. Reach it from anywhere.

The web UI is the way a Zoomies fleet is configured and operated: connect
GitHub, create a pool, add a host, watch a job, size a runner, turn on elastic
CPU — every one of those is a page or a wizard, live as it happens, and the
documentation describes each task from there first. Nothing on those pages is
special: they are clients of the REST API, and so is everything else.

| From | What it is for |
| --- | --- |
| [The web UI](ui.md) | Configuring and running the fleet, day to day. Thirteen pages, live. |
| [The command line](cli.md) | The same fleet from a terminal or a script: `zoomies status`, `zoomies pools create`, `zoomies runners drain`. |
| [Docker Compose](compose.md) | Starting the controller from a file rather than the installer, on a host you own. |
| [A PaaS](paas.md) or [a marketplace image](marketplace.md) | Starting a controller on a host you do not install on, and joining runner hosts to it. |
| [The API](api-surface.md) | Everything above, for your own tooling. |

## Moving what you have now

Two different journeys, and Zoomies answers them differently on purpose.

**Still on GitHub's runners, or renting somebody's?** The migration wizard reads
the workflows across the repositories your App can see, maps each hosted label
to one of your pools, and shows you the exact diff before it writes anything.
Approve it and you get one pull request per repository, each on its own branch.
It changes the `runs-on` lines and nothing else — comments, indentation,
quoting and line endings all survive byte for byte, because a pull request that
reformats the file hides the one line that actually changed. It never pushes to
your default branch, never merges, and never opens more than twenty-five at a
time.

**Already running your own static runners?** Then you do not need the wizard,
and it will deliberately leave those jobs alone — somebody already made that
decision and guessing at it would be rude. Give a Zoomies pool the label your
existing runners already advertise, and **not a line of any workflow changes**:
the same `runs-on` now reaches a fleet that gives every job a fresh runner.
Retire the old machines as the work moves across.

Either way, nothing is written until you have read it, and
[`zoomies demo`](quickstart.md#just-looking) gives you a whole fixture fleet to walk
the wizard through before you connect GitHub to anything.

[Migrate existing runners :material-arrow-right:](migration.md){ .md-button }

## What runs where

Three different questions, and conflating them is how a project ends up
promising a platform it has never run on.

| | Where it runs |
| --- | --- |
| **The controller and the agents** | Linux on x86-64 and arm64; the agent also on Windows x86-64, where a runner is a process rather than a container. macOS builds every release and is fine for running a controller while you develop against it. |
| **The runners** | Ubuntu 24.04, Ubuntu 26.04, Ubuntu 22.04, Debian 12, Debian 13, Fedora 42 and Rocky Linux 9, from `ghcr.io/eyupio/zoomies-runner`. Every image is built for x86-64 and arm64. [The catalogue](naming.md#the-runner-image) is generated from one table in the code, and a test holds this page to it, so neither can drift from what is published. |
| **The backends** | `docker` — the default — `podman`, including rootless, and `process`, which runs the runner straight on the host without a container. |

A pool names the machine its runners need, and the scheduler will not place it
anywhere else; when no host matches, the Overview says which machine to add.
A Windows host runs the agent on the `process` backend — no container, a fresh
work directory per job on a machine that keeps its state
([the details](hosts-and-pools.md#worked-shapes)) — and there is no macOS
runner image.

## What is qualified

This project keeps a
[support matrix](https://github.com/eyupio/zoomies/blob/main/roadmap/support-and-measurement.md)
that separates what a test has actually run on from what merely builds. Read it
before you put anything precious on this. Today, in short: the controller, the
agents, a join, and a queued job becoming a real workload on a real machine are
exercised on every pull request, on the `process` backend against a fake GitHub,
on amd64 and on arm64; the Docker backend is unit-tested against a fake Engine
API, and while no *test* here starts a container, this repository's own CI does
— every job but the arm64 and Windows legs runs on a Zoomies fleet, inside a
container the Docker backend started, through the build under test, and
[a test](https://github.com/eyupio/zoomies/blob/main/internal/docs/workflows_test.go)
fails if a job leaves the fleet; and the Windows agent is built, vetted and
unit-tested on a hosted Windows runner, and has not yet run a job on a Windows
host anyone kept.

That is not a reason to keep it off your own machines. It is the reason the
defaults are the careful ones, and the reason every claim on this page names
the thing that backs it.

## Why not something else

Zoomies is what you want when
[ARC](https://github.com/actions/actions-runner-controller) is too much
machinery — you have a VM or three, not a cluster — but a handful of
hand-registered long-lived runners is too little.

<div class="zoomies-compare" markdown>

| | Zoomies | ARC | A few static runners |
| --- | --- | --- | --- |
| Runs without Kubernetes | :material-check-bold:{ .yes } yes | :material-close:{ .no } no | :material-check-bold:{ .yes } yes |
| Ephemeral runners | :material-check-bold:{ .yes } default | :material-check-bold:{ .yes } yes | :material-minus:{ .partial } rarely |
| Autoscaling | :material-check-bold:{ .yes } yes | :material-check-bold:{ .yes } yes | :material-close:{ .no } no |
| Multi-host | :material-check-bold:{ .yes } yes | :material-check-bold:{ .yes } yes | :material-close:{ .no } no |
| Auth model | GitHub App | GitHub App | a PAT per runner |
| Web UI | :material-check-bold:{ .yes } yes | :material-close:{ .no } no | :material-minus:{ .partial } sometimes |
| Audit log | :material-check-bold:{ .yes } yes | :material-close:{ .no } no | :material-close:{ .no } no |
| To install | one command | Helm, CRDs, a cluster | manual |

</div>

[Zoomies and actions-runner-controller](actions-runner-controller.md) is the
longer comparison, including where ARC is the better choice.

[Choosing runners without Kubernetes](runners-without-kubernetes.md) starts
with the simpler options. If you would rather rent capacity, compare Zoomies
with [Blacksmith, WarpBuild and RunsOn](hosted-runner-services.md), including
when a runner service is the better choice.

## A word about self-hosted runners

A self-hosted runner executes code from your repositories, and on a public
repository that means anyone who can open a pull request. GitHub's own guidance
is blunt about this, and Zoomies does not change it. What it does is make the
blast radius of each execution as small as it reasonably can:

- one job per runner, then the container is destroyed;
- no reusable registration credential on the host;
- no Docker daemon reachable from the job unless you explicitly ask for one;
- a non-root user, dropped capabilities, sudo that only ever regains that same minimum.

Every setting that trades any of that away is named at startup, listed in the
UI, and documented in [Security](security.md) with what it actually costs you.

The honest advice is still to start it on workloads you already trust. [What is qualified](#what-is-qualified) says which parts have
been run and which have only been built.

<div class="zoomies-cta" markdown>

--8<-- "overrides/partials/animated-logo.html"

<div class="zoomies-cta__copy" markdown>

<p class="title">Give your CI the Zoomies.</p>

<p class="lede">
One command installs it, and the quick start takes you from a fresh host to a
running job in five steps. It is free, it is yours, and you can read every line
of it. Still deciding? The FAQ answers what people ask before they
self-host runners — what it costs, what it needs, and what it will not protect
you from.
</p>

<p class="actions" markdown>
[Install Zoomies :material-arrow-right:](quickstart.md){ .md-button .md-button--primary }
[See the web UI](ui.md){ .md-button }
[Browse the FAQ](faq.md){ .md-button }
</p>

</div>

</div>
