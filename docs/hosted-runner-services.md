---
icon: material/cloud-check-outline
title: "Zoomies vs Blacksmith, WarpBuild, RunsOn and Depot"
description: >-
  How running your own runners with Zoomies compares with Blacksmith, WarpBuild,
  RunsOn and Depot: where jobs run, what each costs, and when each is the better
  choice.
---

# Zoomies and the runner services

Several companies sell faster or cheaper GitHub Actions runners, and they are
good at it. They come in two kinds: services that run your jobs on their own
machines and charge by the minute, and software you run in your own cloud
account for a fee. Zoomies is a third kind: free software you run on machines
you already have, wherever they are.

The prices below are from each company's own pricing page, as of September
2026 (Depot's, as of 9 October 2026). They change often; check the linked page
before relying on a number.

## At a glance

| | Where your jobs run | What you pay | Runner systems |
| --- | --- | --- | --- |
| [GitHub-hosted](https://docs.github.com/en/billing/reference/actions-runner-pricing) | GitHub's virtual machines | Per minute, beyond your plan's included minutes | Linux, Windows, macOS |
| [Blacksmith](https://www.blacksmith.sh/pricing) | Blacksmith's infrastructure | Per minute, with 3,000 free minutes a month | Linux x64 and arm64, Windows, macOS |
| [WarpBuild](https://www.warpbuild.com/pricing) | WarpBuild's cloud | Per minute | Linux x64 and arm64, Windows, macOS |
| [WarpBuild BYOC](https://www.warpbuild.com/docs/ci/byoc) | Your AWS, Google Cloud or Azure account | $0.002 a minute to WarpBuild, plus your cloud bill | Linux; Windows on AWS and Azure |
| [Depot](https://depot.dev/pricing) | Depot's infrastructure (single-tenant EC2 instances), or your own AWS account | A monthly plan (Developer $20 with 2,000 minutes, Startup $200 with 20,000), then $0.006 a minute | Linux x86 and Arm, Windows; macOS on Startup and Business plans |
| [RunsOn](https://runs-on.com/pricing/) | Your AWS account, on EC2 | A yearly licence from €300 (free for non-commercial use), plus your AWS bill | Linux x64 and arm64, Windows |
| Zoomies | Machines you own or rent, anywhere | Nothing, [AGPL-3.0](https://github.com/eyupio/zoomies/blob/main/LICENSE), plus the machines | Linux x64 and arm64; Windows [not yet qualified](index.md#what-is-qualified) |

For the per-minute services, the smallest runner of each kind:

| Price per minute | Linux x64 | Linux arm64 | Windows | macOS |
| --- | --- | --- | --- | --- |
| GitHub-hosted | $0.006 (2 cores) | see GitHub's page | $0.010 (2 cores) | $0.062 (3 or 4 cores) |
| Blacksmith | $0.004 (2 vCPU) | $0.0025 | $0.008 | $0.08 (M4) |
| WarpBuild | $0.004 (2 vCPU) | $0.003 (2 vCPU) | $0.016 (4 vCPU) | $0.08 (M4 Pro, 6 vCPU) |

Sizes differ between services, so compare the machine as well as the price.

## When a runner service is the better choice

- **You need macOS.** Zoomies has no macOS runner image, RunsOn does not run
  macOS, and WarpBuild's BYOC pricing lists Linux and Windows only. Blacksmith,
  WarpBuild's own cloud and Depot (on its Startup and Business plans) do run
  macOS.
- **You want the fleet run for you, with a cache built in.** Depot's
  [GitHub Actions runners](https://depot.dev/docs/github-actions/overview) run
  each job on a fresh EC2 instance that is never reused and, per its docs, are
  integrated with Depot's cache with no configuration; egress filtering is listed
  on its Business plan, which Zoomies does not offer. Depot's runners only
  support repositories owned by a GitHub organisation, so a personal-account
  repository needs something else; Zoomies works with a personal-account
  repository too ([FAQ](faq.md#do-i-need-a-github-organisation)).
- **You do not want machines to look after.** A per-minute service has no hosts
  to patch, no disks to fill and no images to keep current. That is most of what
  you are paying for, and it is worth paying for if nobody on the team wants the
  job.
- **Your load is bursty and large.** A service or your cloud account can grow
  far past the machines you own; a fleet of your own hardware is only as big as
  what is in it.
- **You want what they sell on top.** Blacksmith advertises faster hardware
  than GitHub's and faster cache downloads, with Docker layer caching and
  static IPs as paid add-ons; an enterprise plan comes with an SLA and support.
  Zoomies has a per-pool cache and none of the rest.

## When your own cloud account is the better choice

- **You already run on AWS**, and want CI inside the same account, network and
  bill. RunsOn launches EC2 instances in your account at spot prices with no
  per-minute markup, for a yearly licence; WarpBuild's BYOC does the same across
  AWS, Google Cloud and Azure for a per-minute fee. Depot's docs say its
  runners can also run in your own AWS account.
- **You want a machine per job.** RunsOn gives each job its own EC2 instance.
  Zoomies gives each job a fresh container on a shared host, see
  [hosts and pools](hosts-and-pools.md).

## When Zoomies is the better choice

- **You have machines already.** A server in a rack, a home lab, spare office
  PCs, a Proxmox cluster: Zoomies turns them into runner capacity with no
  per-minute fee and no licence. [Runners in your home lab](home-lab.md) has the
  planning.
- **You are not tied to one cloud.** Hosts can be anywhere an agent can dial out
  from, several clouds, your own hardware, or both in one fleet.
- **You want to read and change the code.** Zoomies is AGPL-3.0 and developed in
  the open; running it for your own organisation, changed or not, asks nothing
  of you.
- **You want to run the fleet from one place.** A live [web UI](ui.md), an audit
  log, and [metrics](metrics.md) for every host, pool and job.

## Using more than one

The choice is per job, not per organisation, because every one of these is
selected by `runs-on`. Keep macOS jobs on a service, run Linux builds on your
own machines, and move a workflow between them by changing one line.

Coming from a service to Zoomies, the [migration wizard](migration.md) reads
Blacksmith and WarpBuild labels as well as GitHub's, proposes a pool with the
same operating system and architecture for each, and opens one pull request per
repository after showing you the diff. [What self-hosted runners
cost](costs.md) works through whether it pays.

## Where to go next

- [Quick start](quickstart.md): a fresh host to a running job in about five
  minutes.
- [Zoomies and actions-runner-controller](actions-runner-controller.md): the
  Kubernetes-based option.
- [Security](security.md): what a self-hosted runner exposes that a hosted one
  does not.
