---
icon: material/server-network
title: GitHub Actions runners without Kubernetes
description: >-
  Choose between a simple runner, a Docker container, Zoomies and a runner
  service. Run ephemeral GitHub Actions jobs on your own hosts without a cluster.
---

# Self-hosted GitHub Actions runners without Kubernetes

You do not need Kubernetes to run GitHub Actions on machines you own. A single
runner can be enough for a small, trusted workload. A fleet controller becomes
useful when jobs need fresh environments, more capacity as they queue, or
several hosts you can manage from one place.

Zoomies is a free, open-source controller for that second case: one Go binary,
SQLite, ephemeral runners by default and a live web UI. You still provide and
maintain the hosts. It is Linux that the project runs and tests; check the
[qualification limits](index.md#what-is-qualified) before choosing a platform.

## Choose the amount of machinery you need

| Your situation | Start with | What you still operate |
| --- | --- | --- |
| One repository, trusted jobs and steady demand | GitHub's runner application on a dedicated machine | Registration, updates, capacity and cleanup |
| The same workload, with the runner packaged in a container | A runner image and your container runtime | Starting the next runner, credentials and scaling |
| Fresh runners per job, bursty demand or several machines | Zoomies | Hosts, controller, runner images and capacity limits |
| You would rather buy capacity than maintain machines | A hosted runner service | Workflows and the service's configuration and bill |
| You already operate Kubernetes and want CI inside it | Actions Runner Controller (ARC) | Cluster operations and the runner scale sets |

These choices can coexist. A workflow chooses its runner per job, so a Linux
build on your own host does not require moving a macOS job off a hosted service.

## One runner can be the right answer

[GitHub's self-hosted runner](https://docs.github.com/en/actions/concepts/runners/self-hosted-runners)
connects a machine to your repository or organisation. If the jobs are trusted,
the machine is dedicated and its capacity is enough, a controller may add
little value.

Decide what survives between jobs. A long-lived runner keeps machine state;
an ephemeral runner handles one job and exits, so something must start its
replacement. Packaging the runner in Docker does not supply that lifecycle
management on its own. [Runners in Docker](docker.md) explains containers,
registration and the choices for jobs that need Docker themselves.

## When Zoomies earns its place

Use Zoomies when you want to:

- Start fresh runners as jobs queue, then remove them after each job.
- Set limits per pool and see why a job has no runner yet.
- Join several hosts to one fleet, including
  [private machines behind NAT](private-hosts.md).
- Serve [many repositories](many-repositories.md), with explicit installation
  targets and trust boundaries.
- See pools, hosts, jobs and logs in the [web UI](ui.md), or use the
  [CLI](cli.md) and [API](api-surface.md).

The usual installation is a Linux machine with Docker or Podman. The
controller can run an embedded agent on that machine, or agents on other hosts
can connect outbound. The [quick start](quickstart.md) covers the first job;
[Docker Compose](compose.md) is another way to deploy the controller.

There is also a process backend, which executes jobs on the host. Its work
directory is disposable, but it does not give a job a container's isolation.
Choose it only for trusted workflows on a suitable machine; read the
[backend's security limits](security.md#agentbackend-process).

## When buying runners is simpler

Maintaining hosts has a cost even when the software is free. A service can be
the better choice when you need capacity you do not own, an operating system
Zoomies does not qualify, or a support arrangement this project does not offer.

[Zoomies vs Blacksmith, WarpBuild and RunsOn](hosted-runner-services.md)
distinguishes capacity on a vendor's machines from software in your own cloud
account. [What runners cost](costs.md) works through machine costs, included
minutes and the time spent maintaining a fleet. Compare the whole workload,
including storage and idle hosts, rather than only a per-minute rate.

## When Kubernetes is already there

ARC is worth considering when your team already operates a cluster. Avoiding
Kubernetes is useful when it removes work; it is less useful if it creates a
second infrastructure stack beside one you already know.

The [ARC comparison](actions-runner-controller.md) covers installation,
scaling, isolation and support, including cases where ARC is the better fit.

## Before moving a real workload

Check four things: the runner operating system and architecture, the tools the
workflow needs, the trust of code it executes, and enough capacity for the
jobs that must run together. Ephemeral runners reduce retained job state;
they do not make arbitrary code safe on your infrastructure.

Try one trusted repository first. Then use the
[migration wizard](migration.md) to review `runs-on` changes across repositories.
If the first job waits, the [queued-job checklist](queued-job.md) identifies
whether routing, capacity or registration is holding it up.
