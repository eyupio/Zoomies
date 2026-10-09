---
title: Should you self-host GitHub Actions runners?
description: What a self-hosted GitHub Actions runner fleet actually has to do, what Zoomies handles, what it leaves to you, and when a managed runner service is the better choice.
---

# Should you self-host GitHub Actions runners?

Sometimes. Installing the GitHub runner application takes minutes. Running a
fleet of them, safely, for months, is a different job. This page lists what
that job involves, says plainly which parts Zoomies handles and which it leaves
to you, and is honest about when renting runners is the better choice.

*Last checked: 9 October 2026. The pricing facts below need refreshing if
GitHub's pricing changes.*

## The short answer

Self-hosting tends to make sense when:

- you already own hardware (a home lab, an office server, spare cloud credit)
  and want to use it for CI;
- you need to reach private networks, special hardware or a specific operating
  system that a managed runner cannot give you;
- your build volume is high enough that per-minute billing outweighs the cost
  of running machines and the time you spend on them.

It tends not to make sense when your CI volume is low, when you have no one who
wants to own the fleet, or when you need macOS runners (Zoomies has no macOS
runner image). In those cases a managed service may be simpler. See
[Compared with runner services](hosted-runner-services.md) for the trade-offs.

## What a runner fleet has to do

Whatever tool you use, a production fleet needs to cover the same ground. Here
is each item, who owns it with Zoomies, and where the limits are.

### 1. Start capacity quickly and scale for bursts

Zoomies is event-driven: a `workflow_job` webhook queues work, the scheduler
decides, and an agent starts a fresh runner. Pools set how many runners may
exist and how many stay idle. Scaling happens across the hosts you give it.
**You own the capacity.** If your hosts are full, jobs wait. The Overview says
which machine to add.

### 2. Keep runner images current

Zoomies publishes runner images for Ubuntu 22.04, 24.04 and 26.04, Debian 12
and 13, Fedora 42 and Rocky Linux 9, each for amd64 and arm64, from
`ghcr.io/eyupio/zoomies-runner` (the [catalogue](naming.md#the-runner-image)
is the full list). Upgrades refresh locally cached stock images, but not a
pool's pinned or custom image. **You own host patching**, and any image you
pin or build yourself. See [Upgrading](upgrading.md).

### 3. Isolate one job from the next

Each runner handles one job, then its container and workspace are destroyed.
By default jobs get no Docker daemon, run as a non-root user, and have all
Linux capabilities dropped. This is container-level isolation, not a virtual
machine per job: containers share the host kernel. The `process` backend runs
the job straight on the host with no container boundary at all; it is the only
backend on Windows, and an option on Linux. Read
[Security](security.md) for what this protects and what it does not.

### 4. Clean up after jobs and failures

By default registrations are single-use, minted by Zoomies as a GitHub App, so
there is no long-lived registration token left on a host. Runner containers are
destroyed when the job ends, and a completed job triggers removal of its
runner whether it passed, failed or was cancelled.

Two things clean up after the rest. A runner that never finishes registering is
marked failed after `scheduler.provision_timeout` (20 minutes by default). And
every ten minutes the controller compares its runners with GitHub's and deletes
registrations that are orphaned: ones named with Zoomies' `zoomies-` prefix
whose runner it knows is gone, or which GitHub reports offline and Zoomies has
no record of. It leaves other tools' runners alone, and any registration that is
online and unknown or still running a job. A delete that fails is recorded on
the runner and retried. It cannot clean up what it cannot reach: if GitHub is
rate limiting the installation, cleanup waits.

### 5. Keep logs and metrics after the runner is gone

The controller keeps state in SQLite, exposes Prometheus metrics, records job
history with queue waits, and writes an audit row for every mutating action.
Job history is kept for 30 days by default and finished runner rows for 7;
audit rows are never pruned. Both windows are settings under `retention`. See
[Metrics](metrics.md).

Zoomies does not store job or runner log text. A runner's log is streamed live
from its host while the runner exists. By default the container is removed as
soon as the controller has acknowledged its final report, so its log goes with
it; `agent.finished_retention` keeps a finished container (and so its log) on the
host for a window you choose, at the cost of host disk. For a failed step, the
Jobs page links to that step's log on GitHub, which is where the workflow's
logs live. If you need logs kept for a set period, ship them out of your
workflows or hosts yourself.

### 6. Control credentials and access

Zoomies authenticates as a GitHub App with only the permissions it needs. There
are no pasted personal access tokens. Authentication is on by default, the
controller binds to loopback by default, and every setting that weakens the
defaults is named at startup and in the problems drawer (with one exception,
`server.bind` without TLS, which is printed at startup but kept off the drawer).

Zoomies does not filter outbound network traffic from jobs. The nearest settings
do something else: `security.allow_private_egress` limits which addresses the
controller itself will dial, and `agent.network` attaches runners to a Docker
network you have already set up. If you need egress rules for jobs, enforce them
at the host or network level.

### 7. Plan capacity across platforms and sizes

The controller and agents run on Linux (amd64 and arm64). A Windows x86-64 agent
exists and runs jobs as a process with no container, but the project says it
has not yet run a job on a Windows host anyone keeps. macOS is fine for running
a controller while you develop, but there is no macOS runner image. Size classes, automatic pools and elastic CPU and
memory help you fit jobs to hosts. See [What is qualified](index.md#what-is-qualified)
for exactly what has and has not been run.

### 8. Be the on-call

With a self-hosted fleet, you are. Zoomies tries to make that easier: a
problems drawer that says what is true, why it matters and what to change, and
[problem codes](problem-codes.md) you can look up. It cannot take the pager
from you.

## What about GitHub's pricing?

In December 2025 GitHub announced a $0.002 per minute platform charge for
self-hosted runner usage, due from March 2026, alongside cuts of up to 39% to
GitHub-hosted runner prices. GitHub then postponed the self-hosted charge to
re-evaluate its approach, and its own announcement carries that notice:
[Pricing changes for GitHub Actions](https://github.com/resources/insights/2026-pricing-changes-for-github-actions).
As of the date above, [our cost page](costs.md) records it as postponed rather
than cancelled, and some older articles still describe it as active. Check GitHub's current
pricing page before you decide, because this can change.

For our own numbers, see [What it costs](costs.md).

## A 30-day trial worth running

Before committing, run a real month, not one warm machine:

- include your busiest week of pull requests;
- note queue waits, failed jobs and how long you spent fixing the fleet
  (the Usage page and job history give you the first two);
- count idle capacity, since machines you own cost money even when nothing is
  queued;
- include the time of whoever will be on call.

If the month looks good, you have your answer. If it looks like a second job,
a managed service may suit you better.

## Where to go next

- [Quick start](quickstart.md): from a fresh host to a running job.
- [Compared with ARC](actions-runner-controller.md): if you already run
  Kubernetes.
- [Runners without Kubernetes](runners-without-kubernetes.md): the simpler
  options first.
- [Compared with runner services](hosted-runner-services.md): when renting is
  the better choice.
- [FAQ](faq.md).
