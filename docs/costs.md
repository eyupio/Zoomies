---
icon: material/currency-usd
title: What self-hosted GitHub Actions runners cost
description: >-
  GitHub-hosted runner prices, free minutes per plan, the postponed
  $0.002-a-minute self-hosted charge, and the sums for deciding whether to run
  your own.
---

# What self-hosted runners cost

GitHub does not charge for minutes on self-hosted runners. You pay for the
machines, and for the time it takes to look after them; Zoomies itself is free
under the [AGPL-3.0](https://github.com/eyupio/zoomies/blob/main/LICENSE).
Whether that works out cheaper than GitHub's own runners depends on how many
minutes you use, on which operating system, and whether your repositories are
public.

The figures on this page are GitHub's published prices as of September 2026.
They change; [GitHub's pricing reference](https://docs.github.com/en/billing/reference/actions-runner-pricing)
is the one to trust over this page.

## What GitHub charges for its own runners

Public repositories on standard GitHub-hosted runners are free. Private
repositories get a monthly allowance of minutes with the plan, and pay per
minute after that
([GitHub Actions billing](https://docs.github.com/en/billing/concepts/product-billing/github-actions)).

| Plan | Minutes included each month |
| --- | --- |
| GitHub Free, and Free for organisations | 2,000 |
| GitHub Pro | 3,000 |
| GitHub Team | 3,000 |
| GitHub Enterprise Cloud | 50,000 |

| Standard GitHub-hosted runner | Price per minute |
| --- | --- |
| Linux, 2 cores (x64) | $0.006 |
| Windows, 2 cores (x64) | $0.010 |
| macOS, 3 or 4 cores | $0.062 |

Larger runners cost more, and are not free even on public repositories.

## What GitHub charges for self-hosted runners

Nothing, today. GitHub's billing documentation lists self-hosted runners
alongside public repositories as free.

In December 2025 GitHub announced a "GitHub Actions cloud platform charge" of
$0.002 a minute for self-hosted runners, starting on 1 March 2026, and
later postponed it "to take time to re-evaluate our approach"
([the announcement and its update](https://github.blog/changelog/2025-12-16-coming-soon-simpler-pricing-and-a-better-experience-for-github-actions/),
[GitHub's summary of the 2026 changes](https://github.com/resources/insights/2026-pricing-changes-for-github-actions)).
Postponed is not cancelled. If it comes back at the announced rate, 20,000
minutes a month would cost $40 — a third of the Linux hosted rate, and paid on
top of your own machines.

Zoomies adds no charge of its own either way: it is software you run, not a
service you rent.

## Working out whether it pays

The comparison that matters is what GitHub would charge you for the minutes
beyond your allowance, against what your own machines cost you each month.
GitHub's side of it is the minutes you use, less the minutes your plan
includes, times the price per minute.

Two examples on the GitHub Team plan, all on Linux 2-core runners:

| Minutes a month | Beyond the 3,000 included | Hosted cost at $0.006 |
| --- | --- | --- |
| 20,000 | 17,000 | $102 |
| 100,000 | 97,000 | $582 |

Turned round, a machine costs its monthly price divided by $0.006 in Linux
minutes before it pays for itself: one you rent for $30 a month breaks even at
5,000 minutes past the allowance. A machine you already own — a home-lab box,
a spare server, an idle VM — starts ahead, which is what
[private hosts with Tailcat](private-hosts.md) is for.

Windows minutes cost two-thirds more than Linux ones on GitHub's runners, so
the sums tip sooner there. macOS minutes cost ten times as much, but Zoomies
has no macOS runner image, so it does not help with those.

## What running your own costs that a price list does not show

- **Your time.** Someone keeps the hosts patched, the runner images current and
  the controller upgraded. Zoomies keeps that small — one binary to
  [upgrade](upgrading.md), and a web UI that names what is wrong and what to
  change — but it is not zero.
- **Idle machines.** A pool scales to zero runners by default, so nothing
  starts until a job is queued, but the hosts themselves are still yours to
  pay for while they wait.
- **Security.** A self-hosted runner runs your repositories' code on your
  machine. On a public repository that is anyone who can open a pull request,
  and GitHub's advice is not to do it. For public repositories GitHub's own
  runners are free and the safer choice. [Security](security.md) has the rest.
- **Shared hosts.** GitHub gives each hosted job its own virtual machine.
  Zoomies gives each job a fresh container with a guaranteed share of its host,
  and can lend a busy one the CPU nobody else is using — see
  [hosts and pools](hosts-and-pools.md) and
  [elastic CPU zoomies](elastic-cpu.md).
- **What is qualified.** Linux runners are what Zoomies is built and tested
  for. The Windows agent builds and is unit-tested but
  [has not yet run a job on a real Windows host](index.md#what-is-qualified).

## Where to go next

- [Quick start](quickstart.md): a fresh host to a running job in five steps.
- [Migrating repositories](migration.md): moving `runs-on` off GitHub's runners,
  one pull request per repository.
- [Zoomies and the runner services](hosted-runner-services.md): Blacksmith,
  WarpBuild and RunsOn, side by side.
- [FAQ](faq.md): what Zoomies needs, and what it will not protect you from.
