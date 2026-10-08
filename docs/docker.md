---
icon: material/docker
title: Self-hosted GitHub Actions runners in Docker
description: >-
  Running GitHub Actions runners in Docker: what one runner container gives you,
  three ways a job can get Docker, and how Zoomies makes an autoscaling fleet.
---

# Self-hosted runners in Docker

Putting a GitHub Actions runner in a container is the easy first step. The hard
parts come after: a fresh runner for every job, more runners when jobs queue,
none when they do not, and jobs that need Docker themselves without being handed
the host. This page walks through those, and where Zoomies fits.

## One runner in one container

A runner image — [myoung34/docker-github-actions-runner](https://github.com/myoung34/docker-github-actions-runner)
is the best-known — starts GitHub's runner inside a container and registers it
with your repository or organisation. It is a good way to try self-hosting, and
for one repository with a steady trickle of jobs it may be all you need.

What it leaves to you:

- **Registering.** The container needs a credential to register with. A
  personal access token or a GitHub App key in its environment is available to
  every job that runs there, and the myoung34 README says as much: environment
  variables "are not safe from exfiltration", so workflow changes should be
  gated. A short-lived registration token avoids that, but has to be renewed by
  hand.
- **Starting fresh.** A runner registered as ephemeral takes one job and exits,
  so something has to start the next one. A runner that is not ephemeral keeps
  whatever the last job left behind — files, caches, credentials — for the next.
- **Scaling.** The container does not know how many jobs are queued. You run as
  many as you guess you need, and they sit idle or leave jobs waiting.
- **More than one machine.** Each host is its own set of containers, started
  and watched separately.

## Three ways to give a job Docker

A job that runs `docker build`, uses `docker/build-push-action`, or has a
`container:` or `services:` block needs a Docker daemon. A runner in a container
has three ways to get one, and they are not equally safe.

| Approach | What the job can reach | The cost |
| --- | --- | --- |
| No daemon | Nothing | Docker steps fail. Right for most jobs, which never build an image |
| Docker-in-Docker | A private daemon in a privileged sidecar | A container escape from the privileged sidecar reaches the host |
| The host's `docker.sock` mounted in | The host's own daemon | **Any job can become root on the host**, and see and stop every other container on it |

Zoomies calls these `docker_mode: none`, `dind` and `host-socket`, set per pool.
`none` is the default and `dind` is the one to prefer when a pool builds
images; a pool on `dind` or `host-socket` raises a `pool.dangerous` warning in
the UI's problems drawer, and `zoomies pools create` and `pools edit` print it too.
[Security](security.md#6-the-dangerous-toggles) says what each costs in full,
and [Jobs that build container images](configuration.md#jobs-that-build-container-images)
covers the runner image a pool is switched to when it asks for a daemon.

## From containers to a fleet

Zoomies is the part that sits above the containers: a controller that watches
GitHub for queued jobs and an agent on each Docker host that starts and removes
runner containers.

| | A runner container on its own | Zoomies |
| --- | --- | --- |
| A fresh runner per job | Only if registered as ephemeral, and something restarts it | Every job, by default; the container is destroyed afterwards |
| Credential in the container | A token or App key in its environment | A single-use just-in-time registration, unset before the job starts |
| Scaling with the queue | By hand | From `workflow_job` webhooks, down to zero |
| Several hosts | Each managed on its own | One controller, any number of agents, which dial out |
| Docker for jobs | However you mount it | `none`, `dind` or `host-socket`, per pool, with the risky two raised as problems |
| Seeing what is happening | `docker logs` | A live [web UI](ui.md), [metrics](metrics.md) and an audit log |

It runs on the same Docker you already have, and on
[rootless Docker or Podman](runtime-compatibility.md) too. Runners use
[published images](naming.md#the-runner-image) for Ubuntu, Debian, Fedora and
Rocky Linux, or an image of your own.

## Getting started

The installer detects Docker and, when a `compose` command is available,
defaults to a Docker Compose deployment:

```sh
curl -fsSL https://zoomies.sh/install.sh | sh
```

Or start from the [compose file](compose.md) in the repository. A compose
deployment runs the controller with an embedded agent, so the machine it is on
is already a runner host; **Hosts → Add a host** gives you one line to paste on each further
machine. The [quick start](quickstart.md) goes from a fresh host to a running
job in five steps.

## Coming from runner containers you run yourself

You do not have to change any workflows. Give a Zoomies pool the label your
containers already register with, and the same `runs-on` lines reach the new
fleet; then stop the old containers as the work moves across.
[Coming from your own static runners](migration.md#coming-from-your-own-static-runners)
has the details, and the [migration wizard](migration.md) handles repositories
still on GitHub's own runners.

## Where to go next

- [What self-hosted runners cost](costs.md): whether running your own pays.
- [Zoomies and actions-runner-controller](actions-runner-controller.md): if you
  are weighing a Kubernetes-based setup instead.
- [Security](security.md): what a self-hosted runner exposes, and what each
  setting that weakens the defaults costs.

## Persistent caches

Runner ephemerality does not require cold caches. See [Persistent caches](persistent-caches.md) for scoped dependency/tool caches and registry-backed BuildKit reuse with isolated DinD sidecars.
