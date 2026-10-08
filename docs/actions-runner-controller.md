---
icon: material/kubernetes
title: "Zoomies vs actions-runner-controller (ARC)"
description: >-
  How Zoomies compares with actions-runner-controller: what each needs, how each
  scales and isolates jobs, where each is better, and how to move between them.
---

# Zoomies and actions-runner-controller

[Actions Runner Controller](https://github.com/actions/actions-runner-controller)
(ARC) is GitHub's Kubernetes operator for self-hosted runners, and if you
already run Kubernetes it is very likely the right choice. Zoomies is for the
case where you do not: a VM or three, or a home lab, where a cluster, two Helm
charts and a set of custom resources are more machinery than the problem needs,
but hand-registering a few long-lived runners is not enough.

Both give each job a fresh runner that is destroyed afterwards, both register
runners with single-use just-in-time configurations, and both scale to zero.
The difference is almost entirely in what they run on and what you are left
to operate.

## Side by side

ARC here means its runner scale sets mode, the one GitHub supports. The ARC
column is taken from
[GitHub's ARC documentation](https://docs.github.com/en/actions/concepts/runners/actions-runner-controller);
where it is silent, so is the table.

| | ARC (runner scale sets) | Zoomies |
| --- | --- | --- |
| Runs on | A Kubernetes or OpenShift cluster | Linux machines with Docker, Podman, or no container runtime at all |
| Installed with | Two Helm charts, `gha-runner-scale-set-controller` and `gha-runner-scale-set` | [One command](quickstart.md), a [compose file](compose.md), or a container |
| Keeps its state in | The Kubernetes API, as custom resources | One SQLite file |
| Learns about queued jobs | A listener pod long-polls GitHub | `workflow_job` webhooks, with polling as a fallback |
| Scales to zero | Yes, by default | Yes, by default (`min_runners: 0`) |
| Ephemeral runners | Always | By default |
| Authenticates as | A GitHub App, or a personal access token (classic or fine-grained) | A GitHub App |
| Jobs that build images | `dind` mode, which needs a privileged container | A pool with `docker_mode: dind`, a privileged sidecar per runner |
| Container jobs in their own pod | `kubernetes` mode, through runner container hooks | Not applicable: there are no pods |
| Runner operating systems | Linux containers; the docs do not cover Windows | [Ubuntu, Debian, Fedora and Rocky Linux](naming.md#the-runner-image); a Windows agent that is [not yet qualified](index.md#what-is-qualified) |
| Machines behind NAT or at home | Must join the cluster as nodes | Agents dial out, so nothing is opened; [Tailcat](private-hosts.md) brings in private hosts |
| Seeing the fleet | Prometheus metrics from the controller and listeners | Prometheus [metrics](metrics.md), a live [web UI](ui.md), an audit log and live job logs |
| Moving workflows onto it | Change each `runs-on` to the scale set's name | A [migration wizard](migration.md) that opens one pull request per repository |
| GitHub Enterprise Server | Supported, with its own documentation per release | Configurable, [not yet tested against one](faq.md#does-it-work-with-github-enterprise-server) |
| Who supports it | GitHub, for the scale sets mode; the Kubernetes side stays yours | This project, in the open |

## When ARC is the better choice

- **You already run Kubernetes**, and someone on the team is comfortable
  operating it. GitHub's own
  [support statement](https://docs.github.com/en/actions/concepts/runners/support-for-arc)
  is clear that cluster setup, networking, storage and policy stay with you,
  and that container orchestration expertise is a prerequisite.
- **You want a vendor to call.** GitHub supports the scale sets mode of ARC.
  Zoomies is an open-source project with no support contract.
- **You run GitHub Enterprise Server in production.** ARC is documented for
  each Enterprise Server release; Zoomies is designed for it but no test has
  yet run against one.
- **Your container jobs should each get their own pod**, scheduled across the
  cluster, which is what ARC's `kubernetes` mode does.

## When Zoomies is the better choice

- **There is no cluster, and you would rather not start one** for CI. Zoomies
  is one Go binary and a SQLite file; a VM with a container runtime is the whole
  requirement. See the [architecture](architecture.md) for why it was built
  that way.
- **Your spare capacity is not in a data centre.** An agent on a home-lab box
  or an office machine dials out to the controller, so it joins the fleet
  without port forwarding or a public address.
- **You want to see and run the fleet from a browser.** The
  [web UI](ui.md) shows every pool, runner and job live, says in plain words
  why the scheduler did what it did, and records every change in an audit log.
- **You are moving off GitHub's own runners.** The
  [migration wizard](migration.md) rewrites `runs-on` across your repositories
  and opens a pull request for each one, after showing you the diff.
- **You want a busy job to borrow idle CPU.** Each runner keeps a guaranteed
  share of its host, and a pool can lend a busy one what nobody else is using,
  see [elastic CPU zoomies](elastic-cpu.md).

## What is the same either way

A self-hosted runner, from either, runs your repositories' code on machines you
own. On a public repository that is anyone who can open a pull request, and
GitHub's guidance is not to do it. Neither tool changes that; both make each
execution short-lived. [Security](security.md) covers what Zoomies does about
the rest.

Neither charges for itself. GitHub does not charge for self-hosted runner
minutes today, whichever controller starts them,
[what self-hosted runners cost](costs.md) has the details, including the
postponed platform charge.

## Moving from ARC to Zoomies

The migration wizard deliberately leaves jobs that already run on self-hosted
runners alone, because someone chose that on purpose. Moving off ARC is
therefore a `runs-on` change you make yourself: point each job at a Zoomies
pool's label instead of the scale set's name, one repository at a time, and
uninstall the scale set once nothing targets it.

Zoomies can also give a pool any label you choose, which is how
[static runners are replaced](migration.md#coming-from-your-own-static-runners)
without editing workflows. Whether GitHub routes a job to an ARC scale set or
to ordinary runners when both answer to the same name is not something this
project has tested, so retire the scale set before giving a pool its name.

Running both side by side is fine: pools and scale sets with different labels
never see each other's jobs.

## Further reading

DevToolLab's [guide to CI acceleration services](https://devtoollab.com/blog/ci-acceleration-services)
features Zoomies alongside the other ways of getting faster, cheaper runners,
if you are weighing self-hosting against a hosted service.

## Where to go next

- [Quick start](quickstart.md): a fresh host to a running job in about five
  minutes.
- [Hosts and pools](hosts-and-pools.md): how Zoomies decides where a runner
  goes.
- [FAQ](faq.md): what Zoomies needs, and what it will not protect you from.
