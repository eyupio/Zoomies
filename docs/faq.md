---
title: Frequently asked questions
description: >-
  Answers to what people ask before self-hosting GitHub Actions runners: cost,
  Kubernetes, ARC, ephemeral runners, scaling to zero, public repositories, and
  what Zoomies needs to run.
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
a GitHub organisation; it watches for queued jobs, starts a fresh runner
container for each one, and destroys the runner when the job finishes. It is a
single Go binary with SQLite for state and a web UI built in.

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
short version.

## What does "ephemeral by default" actually buy me?

Each runner takes exactly one job and is then destroyed. Nothing survives from
one workflow run to the next -- not a clone, not a cache, not a credential a
previous job left on disk. It is the single most useful property a self-hosted
runner can have, and it is why it is the default rather than an option.

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

## Which platforms does it run on?

Linux on x86-64 and arm64 is what the controller, the agents and the runner
images are built for. macOS works for running a controller in development.
Windows runners are not supported.

## Does it work with GitHub Enterprise Server?

Yes. Point `github.api_base_url` at your Enterprise Server instance; the App
authentication, the JIT configurations and the webhooks are the same.

## How do I move my existing workflows onto it?

The migration wizard rewrites `runs-on` across your repositories and opens one
pull request per repository, showing you the exact diff before it opens
anything, and only touching the jobs it is sure about. See
[migrating repositories](migration.md).

## Can I see what the scheduler is doing, and why?

Yes -- every scaling decision carries a reason string written for a person
("1 job queued > 30s"), and it is shown in the UI next to the pool it applies
to. Scheduling is a pure function of a snapshot, which is what makes those
reasons reliable enough to print.

## What does it cost?

Nothing. Zoomies is free and open source under the MIT licence. You pay for the
machines you run it on.

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
        "text": "Zoomies is a self-hosted GitHub Actions runner fleet controller. You point it at a GitHub organisation; it watches for queued jobs, starts a fresh runner container for each one, and destroys the runner when the job finishes. It is a single Go binary with SQLite for state and a web UI built in."
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
      "name": "Does Zoomies work with GitHub Enterprise Server?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Yes. Point github.api_base_url at your Enterprise Server instance; the App authentication, the JIT configurations and the webhooks are the same."
      }
    },
    {
      "@type": "Question",
      "name": "What does Zoomies cost?",
      "acceptedAnswer": {
        "@type": "Answer",
        "text": "Nothing. Zoomies is free and open source under the MIT licence. You pay only for the machines you run it on."
      }
    }
  ]
}
</script>
