---
icon: material/help-circle-outline
title: Frequently asked questions
description: >-
  What Zoomies costs, what it needs, which platforms it runs on, and what
  self-hosted runners will and will not protect you from.
---

# Frequently asked questions

<!--
  The answers below are also emitted as FAQPage structured data at the bottom of
  this file, which is what puts them in a search result and in front of an
  assistant answering the same question. Two copies of a sentence is the price;
  the alternative is a page a person reads and a machine cannot.

  If you edit an answer, edit both. Keep the structured copy to one plain
  paragraph -- it is read aloud, not rendered.
-->

## What is Zoomies?

Zoomies is a self-hosted GitHub Actions runner fleet controller. You point it at
a GitHub organisation, or at a repository on a personal account; it watches for
queued jobs, starts a fresh runner container for each one, and destroys the
runner when the job finishes. It is a single Go binary with SQLite for state and
a web UI built in.

## Do I need Kubernetes?

No. Zoomies is a process on a machine. There is no cluster, no operator, no
custom resources and no database server -- a VM with a container runtime is the
whole requirement. See the [architecture](architecture.md).

## How is this different from actions-runner-controller?

[ARC](https://github.com/actions/actions-runner-controller) is excellent if you
already run Kubernetes. Zoomies is for the case where you do not: a VM or three,
where a Helm chart and a set of CRDs are more machinery than the problem needs,
but hand-registering a few long-lived runners is not enough. The
[comparison table](index.md#why-not-something-else) on the home page is the
short version, and [Zoomies and actions-runner-controller](actions-runner-controller.md)
the long one.

## What does "ephemeral by default" actually buy me?

Each runner takes exactly one job and is then destroyed. Nothing survives from
one workflow run to the next -- not a clone, not a cache, not a credential a
previous job left on disk. It is the single most useful property a self-hosted
runner can have, and it is why it is the default rather than an option.

A finished runner's container and Zoomies-created scratch directory are removed
once the controller has recorded its end. The default
[`agent.finished_retention`](configuration.md#agentfinished_retention) is `0s`;
operators can retain stopped containers briefly for local log inspection.
Completed, failed and cancelled GitHub jobs also trigger ephemeral runner
removal, with retries until the host confirms cleanup. Shared host-socket
images and job-created volumes are separate; unused Docker builder cache has
its own [size target](configuration.md#agentdocker_build_cache_mb).

## Do I have to paste a runner registration token anywhere?

No. Zoomies authenticates as a GitHub App and mints a single-use JIT
registration for each runner itself. A JIT configuration registers exactly one
runner and cannot be replayed, so there is no long-lived credential sitting in a
dotfile next to a runner. The App's private key is encrypted at rest with
AES-256-GCM.

## Does it scale to zero?

Yes, and that is the default: a pool with `min_runners: 0` runs nothing at all
while its queue is empty. Set `min_runners` above zero only when you want warm
capacity waiting, and always set `max_runners` -- it is the backstop against a
runaway workflow. See [configuration](configuration.md).

## Is it safe to run self-hosted runners on public repositories?

No, and Zoomies does not change that. A self-hosted runner executes code from
your repositories, which on a public repository means code from anyone who can
open a pull request; GitHub's own guidance says not to do it. What Zoomies does
is make each execution's blast radius as small as it reasonably can: one job per
runner, no reusable registration credential on the host, no Docker daemon inside
the job unless you ask for one, a non-root user with dropped capabilities.
[Security](security.md) says what every setting that trades this away costs.

## What happens if the webhook is misconfigured?

The fleet gets slower, not stuck. `workflow_job` webhooks are how Zoomies finds
out about a queued job, and a fallback poller runs behind them, so a delivery
that never arrives means a job picked up on the next poll rather than a job that
sits there forever. Webhook deliveries are at-least-once and can arrive out of
order, and the jobs upsert refuses to move a job backwards through its
lifecycle.

## Can runners run on more than one machine?

Yes. One controller and any number of agents, on any number of hosts. Agents
only ever connect outbound -- they long-poll the controller for tasks and post
results and logs back on connections they opened -- so a runner host behind NAT
needs no inbound firewall rule at all.

## Which container runtimes work?

Docker and Podman, including rootless, and a `process` backend that runs the
runner directly on the host without a container. Zoomies talks to the Engine API
over a socket and Podman's socket speaks the same protocol, so both are the same
code path.

## Can jobs build container images?

Yes, with one setting on the pool: `docker_mode: dind` gives each of its runners
a private Docker daemon. The stock runner image has no Docker CLI on purpose
(most pools never build an image, and the client is cold-start time they would
pay for nothing) so a pool that asks for a daemon is switched to
`ghcr.io/eyupio/zoomies-runner-docker`, the same image plus a client, as it is
saved, under whichever tag it was on. An image of your own is left alone and has
to carry `docker` itself, as is a digest or a tag from a build that may never
have published the variant, and a pool left on one of those says so, with
`pool.docker_client_missing`, rather than failing its jobs one at a time. See
[Jobs that build container
images](configuration.md#jobs-that-build-container-images), and
[self-hosted runners in Docker](docker.md) for how the three ways of giving a
job Docker compare.

## Which platforms does it run on?

Linux on x86-64 and arm64 is what the controller, the agents and the runner
images are built for, and the reference configuration is Linux amd64. macOS
works for running a controller in development. A Windows host can join as an
agent and run jobs with the `process` backend (actions/runner's own Windows
build, as a process on the machine, with no container) which is new in 1.0
and [not yet qualified](index.md#what-is-qualified): it is built and
unit-tested, and the first test that runs on a real Windows host is what moves
it. The [runner image catalogue](naming.md#the-runner-image) is the list of
Linux distributions, and it is generated from the code that publishes them.

## Does it work with GitHub Enterprise Server?

It is designed for it, and has not yet been verified against one. Point
`github.api_base_url` at your Enterprise Server instance and the App
authentication, the JIT configurations and the webhooks are all the same code
paths; the setting is validated, and nothing about the design assumes
github.com. What is missing is evidence: no test, fake or real run in this
project has ever spoken to an Enterprise Server. If you try it, please say how
it went.

## Do I need a GitHub organisation?

No. When you connect GitHub, the target is either an organisation or a single
repository written as `owner/name`. A repository target is how a personal
account is used: the App is created on your own account and installed there,
scoped to that repository, and its runners register at the repository level --
GitHub offers no account-wide runners for personal accounts. Each repository you
want to run jobs for is one installation and one pool, and the installer and
the Connect GitHub dialog both offer the choice.
[One fleet for many repositories](many-repositories.md) covers both cases.

## How do I move my existing workflows onto it?

The migration wizard rewrites `runs-on` across your repositories and opens one
pull request per repository, showing you the exact diff before it opens
anything, and only touching the jobs it is sure about. See
[migrating repositories](migration.md).

## Does a runner get the whole machine, or a fixed slice of it?

Neither, by default: it gets a guaranteed share (the host's allocatable CPU
and memory divided by its slot count, applied as a real cgroup limit) and, on
a pool with [elastic CPU zoomies](elastic-cpu.md) switched on, a busy runner is
lent the CPU the rest of the host is not using on top of that. The guarantee is
never reduced by another runner, one queued job's worth of CPU is held back
before anything is lent. Memory is lent on different terms: a pool with
[elastic memory](elastic-memory.md) on raises the limit of a job that is about
to be killed for it, out of memory the host has not promised to anyone, and
never lowers one. A pool made in the UI starts by measuring what it would lend
and publishing that to Prometheus; switch it to *Automatic boost* in the pool's
**Size** section once the numbers say the host has room. A pool whose jobs need
the same size everywhere can type a fixed one instead.

## Can I see what the scheduler is doing, and why?

Yes -- every scaling decision carries a reason string written for a person
("1 job queued", or "3 jobs queued > 30s" once a scale-up delay is set), and it is shown in the UI next to the pool it applies
to. Scheduling is a pure function of a snapshot, which is what makes those
reasons reliable enough to print.

## Why did my job fail, and how do I find out?

Ask the fleet before you read a log. `zoomies why <job>` in a terminal, the
**Why** section of the job's drawer, and `get_job` over MCP give one answer
from the controller's own explanation: a **class** from a closed set (out of
memory, a timeout, a runner that never started, a host that was lost, a
workflow that failed on its own, and the rest), how sure the fleet is, the
evidence that decides it, the catalogue code whose entry says what to change
and how to see that it worked, and the next steps in order. A class that the
fleet caused is named as such, so "the fleet broke it" and "the fleet could
not say" are told apart by the exit code of `zoomies why` as much as by the
words. See [A job stuck in the queue](queued-job.md#6-ask-the-fleet-why) and
[`zoomies why`](cli.md#zoomies-why).

## Can a coding agent check my workflow files without sending them anywhere?

Yes. `zoomies kennel check` runs [Kennel Club](kennel-club.md)'s workflow
checks over `.github/workflows` of a checkout on the machine it runs on, with
the same parser and the same checks the controller runs, and sends nothing
anywhere: timeouts, pins, token permissions, a `pull_request_target` workflow
that checks out a stranger's code, a secret on a command line. What a laptop
cannot know, which is what the fleet observed, the run history and the
repository's tree as GitHub lists it, is listed as *not checked here* rather
than left silently absent, so "nothing found" means what it says. The
[`zoomies-kennel` skill](cli.md#the-zoomies-kennel-skill) wraps it for an
agent: it summarises the findings with security first, asks which to act on,
and edits nothing without a selection. A controller joins in only when
`--controller owner/name` names a repository to read from it.

## What does it cost?

Nothing. Zoomies is free and open source under the GNU Affero General Public
License (AGPL-3.0). You pay for the machines you run it on.
[What self-hosted runners cost](costs.md) works through the sums against
GitHub's hosted runner prices.

## Should I self-host runners at all?

Sometimes. [Should you self-host GitHub Actions runners?](should-i-self-host.md)
lists what a runner fleet has to do, which parts Zoomies handles and which it
leaves to you, and when renting runners is the better choice.

## Are GitHub self-hosted runners still free?

Yes. GitHub charges nothing for minutes on self-hosted runners. In December 2025 it
announced a $0.002-a-minute platform charge for them from 1 March 2026, then
postponed it to re-evaluate; it has been postponed, not cancelled.
[What self-hosted runners cost](costs.md) has the details and the links.

## Will GitHub's minimum runner version stop my runners?

Not on the stock images. From 25 September 2026 GitHub stops sending jobs to
self-hosted runners older than 2.329.0, and to any runner that has not updated
within 30 days of a new `actions/runner` release. That applies to github.com and
GitHub Enterprise Cloud, not to Enterprise Server.

Zoomies starts every runner with `--disableupdate`, so it does not wait for the
runner to update itself. Instead, the runner images carry the current
`actions/runner` release, and a weekly job in this repository opens a pull
request to bump it when a new one ships. Each ephemeral runner is created fresh
from the image, and
[`images.refresh_interval`](configuration.md#imagesrefresh_interval-keeping-a-moving-tag-current)
pulls a newer image in the background every hour. So a fleet on the stock images
keeps up without anyone touching it.

Three setups do need you:

- A pool that pins `runner_version` or an image digest. Zoomies never replaces
  a pin, so move the pin forward yourself.
- The `process` backend, including Windows hosts. It downloads the release that
  the Zoomies binary pins, so upgrade the controller and the agents.
- `images.refresh_interval` set to `0`. Hosts then keep the image they pulled
  first until you pull a newer one.

A runner that is too old exits with code 7. Zoomies reports that as a
configuration failure and backs off rather than retrying in a loop;
[troubleshooting](troubleshooting.md#runner-exits-with-code-7) has the fix.

## Why does a runner controller give AI assistants repository context?

Because AI Context needs what Zoomies already has: a GitHub App installation,
sign-in and roles, an audit log and an MCP server that Claude connects to by
address. It prepares a repository with a reviewed pull request, and lets an
assistant read the verified source through Zoomies, only for the people and
connections you choose. It is the first feature that is not about runners, and
it leaves the fleet alone: it starts no runners and uses no pool. See
[AI Context](ai-context.md).

## Can I run Zoomies as a service for other people?

Yes. The licence has one thing to say about it: if you change Zoomies and let
other people use your changed version over a network, you have to offer those
people the source of what you are running. That is the clause the AGPL adds to
the GPL, and it is why Zoomies uses it. Running it for your own organisation,
modified or not, asks nothing of you. The full text is in
[LICENSE](https://github.com/eyupio/zoomies/blob/main/LICENSE).

<script type="application/ld+json">
{
  "@context": "https://schema.org",
  "@type": "FAQPage",
  "mainEntity": [
    {
      "@type": "Question",
      "name": "What is Zoomies?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Zoomies is a self-hosted GitHub Actions runner fleet controller. You point it at a GitHub organisation, or at a repository on a personal account; it watches for queued jobs, starts a fresh runner container for each one, and destroys the runner when the job finishes. It is a single Go binary with SQLite for state and a web UI built in."
      }
    },
    {
      "@type": "Question",
      "name": "Does Zoomies need Kubernetes?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "No. Zoomies is a process on a machine. There is no cluster, no operator, no custom resources and no database server -- a VM with a container runtime is the whole requirement."
      }
    },
    {
      "@type": "Question",
      "name": "How is Zoomies different from actions-runner-controller?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "ARC is excellent if you already run Kubernetes. Zoomies is for the case where you do not: a VM or three, where a Helm chart and a set of custom resources are more machinery than the problem needs, but hand-registering a few long-lived runners is not enough."
      }
    },
    {
      "@type": "Question",
      "name": "What does an ephemeral runner buy me?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Each runner takes exactly one job and is then destroyed, so nothing survives from one workflow run to the next -- not a clone, not a cache, not a credential a previous job left on disk. In Zoomies it is the default rather than an option."
      }
    },
    {
      "@type": "Question",
      "name": "Do I have to paste a runner registration token?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "No. Zoomies authenticates as a GitHub App and mints a single-use JIT registration for each runner itself. A JIT configuration registers exactly one runner and cannot be replayed, so no long-lived credential sits beside a runner, and the App's private key is encrypted at rest with AES-256-GCM."
      }
    },
    {
      "@type": "Question",
      "name": "Does Zoomies scale runners to zero?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Yes, and that is the default: a pool with min_runners set to 0 runs nothing at all while its queue is empty. Raise min_runners only when you want warm capacity waiting, and always set max_runners as a backstop against a runaway workflow."
      }
    },
    {
      "@type": "Question",
      "name": "Is it safe to use self-hosted runners on public repositories?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "No, and Zoomies does not change that: a self-hosted runner executes code from your repositories, which on a public repository means code from anyone who can open a pull request. Zoomies makes each execution's blast radius as small as it can -- one job per runner, no reusable registration credential on the host, no Docker daemon inside the job unless you ask for one, a non-root user with dropped capabilities."
      }
    },
    {
      "@type": "Question",
      "name": "What happens if a Zoomies webhook is misconfigured?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "The fleet gets slower, not stuck. workflow_job webhooks are how Zoomies finds out about a queued job, and a fallback poller runs behind them, so a delivery that never arrives means a job picked up on the next poll rather than a job that sits there forever. Webhook deliveries are at-least-once and can arrive out of order, and the jobs upsert refuses to move a job backwards through its lifecycle."
      }
    },
    {
      "@type": "Question",
      "name": "Can Zoomies run runners on more than one machine?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Yes. One controller and any number of agents, on any number of hosts. Agents only ever connect outbound, so a runner host behind NAT needs no inbound firewall rule."
      }
    },
    {
      "@type": "Question",
      "name": "Which container runtimes does Zoomies support?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Docker and Podman, including rootless, and a process backend that runs the runner directly on the host without a container. Podman's socket speaks the Docker Engine API, so both are the same code path."
      }
    },
    {
      "@type": "Question",
      "name": "Can Zoomies jobs build container images?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Yes, with one setting on the pool: docker_mode: dind gives each of its runners a private Docker daemon. The stock runner image has no Docker CLI on purpose, because most pools never build an image and the client is cold-start time they would pay for nothing, so a pool that asks for a daemon is switched to the zoomies-runner-docker image, the same image plus a client, as it is saved. An image of your own is left alone and has to carry docker itself."
      }
    },
    {
      "@type": "Question",
      "name": "Which platforms does Zoomies run on?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Linux on x86-64 and arm64 is what the controller, the agents and the runner images are built for. macOS works for running a controller in development. A Windows host can join as an agent and run jobs as processes with actions/runner's Windows build; that is new in 1.0 and not yet qualified on real hardware."
      }
    },
    {
      "@type": "Question",
      "name": "Does Zoomies work with GitHub Enterprise Server?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "It is designed for it, and has not yet been verified against one. Point github.api_base_url at your Enterprise Server instance and the App authentication, the JIT configurations and the webhooks are the same code paths, and the setting is validated. What is missing is evidence: no test, fake or real run in this project has ever spoken to an Enterprise Server."
      }
    },
    {
      "@type": "Question",
      "name": "Does Zoomies need a GitHub organisation?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "No. The GitHub target is either an organisation or a single repository written as owner/name. A repository target is how a personal account is used: the App is created on your own account and installed there, scoped to that repository, and its runners register at the repository level, because GitHub offers no account-wide runners for personal accounts. Each repository is one installation and one pool."
      }
    },
    {
      "@type": "Question",
      "name": "How do I move my existing workflows onto Zoomies?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "The migration wizard rewrites runs-on across your repositories and opens one pull request per repository. It shows you the exact diff before it opens anything, and only touches the jobs it is sure about."
      }
    },
    {
      "@type": "Question",
      "name": "Can I see what the Zoomies scheduler is doing, and why?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Yes. Every scaling decision carries a reason string written for a person, such as 1 job queued, and it is shown in the UI next to the pool it applies to. Scheduling is a pure function of a snapshot, which is what makes those reasons reliable enough to print."
      }
    },
    {
      "@type": "Question",
      "name": "What does Zoomies cost?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Nothing. Zoomies is free and open source under the GNU Affero General Public License (AGPL-3.0). You pay only for the machines you run it on."
      }
    },
    {
      "@type": "Question",
      "name": "Are GitHub self-hosted runners still free?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Yes. GitHub charges nothing for minutes on self-hosted runners. In December 2025 it announced a $0.002-a-minute platform charge for them from 1 March 2026, then postponed it to re-evaluate; it has been postponed, not cancelled."
      }
    },
    {
      "@type": "Question",
      "name": "Will GitHub's minimum runner version stop my Zoomies runners?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Not on the stock images. From 25 September 2026 GitHub stops sending jobs to self-hosted runners older than 2.329.0, and to any runner that has not updated within 30 days of a new actions/runner release. Zoomies runner images carry the current release, a weekly job bumps it when a new one ships, and every ephemeral runner is created fresh from the image, which is refreshed hourly in the background. You need to act only if a pool pins runner_version or an image digest, if you use the process backend (upgrade the controller and agents), or if you have switched the image refresh off."
      }
    },
    {
      "@type": "Question",
      "name": "Why does a runner controller give AI assistants repository context?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Because AI Context needs what Zoomies already has: a GitHub App installation, sign-in and roles, an audit log and an MCP server that Claude connects to by address. It prepares a repository with a reviewed pull request, and lets an assistant read the verified source through Zoomies, only for the people and connections you choose. It is the first feature that is not about runners, and it leaves the fleet alone: it starts no runners and uses no pool."
      }
    },
    {
      "@type": "Question",
      "name": "Can I run Zoomies as a service for other people?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Yes. Zoomies is licensed under the GNU Affero General Public License, so if you modify it and let other people use the modified version over a network, you must offer those people the source of what you are running. Running it for your own organisation, modified or not, asks nothing of you."
      }
    }
  ]
}
</script>
