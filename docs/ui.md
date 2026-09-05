---
description: >-
  A tour of the Zoomies web UI, page by page: the Overview, the problems
  drawer, pools, runners, jobs, usage, hosts, installations, the migration
  wizard, the audit log and settings, in light and dark.
---

# The UI

Ten pages, one job each. Everything on them is live — there is no refresh
button anywhere, because every page updates in place from the controller's
event stream — and nothing is reachable from the UI that is not reachable from
the [REST API](api-surface.md). Light and dark follow your system until you
choose one, and the screenshots below follow this site's.

The fleet in them is the demo fixture the Playwright suite runs against: two
pools, three hosts, a dozen runners across every state the controller knows,
and a morning's worth of jobs. `ZOOMIES_SEED_DEMO=true` writes the same fleet
into an empty controller, so you can walk through these pages yourself before
connecting GitHub.

## Overview

The page that has to earn the second monitor. Four numbers with an hour of
shape behind them — queued jobs, running jobs, live runners, and the median
queue wait with its p95 — then how long runners take to start and to register.
Under them, each pool's busy runners against its live ones with the floor and
ceiling marked, what is running this moment, how the last jobs ended, and the
scheduler's decisions in its own words: *scaled zoomies-demo-linux-x64 4 → 5:
1 job queued > 30s*. When something needs a person it is one line and a
*Review* button, never a list that pushes the fleet below the fold.

![The Overview: four metric tiles with sparklines, runner startup and registration times, a one-line problems summary, per-pool utilisation bars and the recent scaling feed](screenshots/overview-dark.webp#only-dark){ .zoomies-shot }
![The Overview: four metric tiles with sparklines, runner startup and registration times, a one-line problems summary, per-pool utilisation bars and the recent scaling feed](screenshots/overview-light.webp#only-light){ .zoomies-shot }

## The problems drawer

Reachable from the count in the top bar on every page. Cordoned or silent
hosts, failed registrations, a queued job no pool will run, a job whose runner
stopped under it, and every configuration setting that weakens the default
posture — worst first, each saying what is true, why it matters and what to
change, with a link to the page where you change it.

![The problems drawer open over the Overview, listing a cordoned host with queued work and a job that lost its runner, each with the fix the controller suggests](screenshots/problems-dark.webp#only-dark){ .zoomies-shot }
![The problems drawer open over the Overview, listing a cordoned host with queued work and a job that lost its runner, each with the fix the controller suggests](screenshots/problems-light.webp#only-light){ .zoomies-shot }

## The command palette

`Ctrl+K` (`⌘K` on a Mac) jumps to any page, pool, runner or host by name, and
runs the quick actions — drain a runner, cordon a host, create a
pool — without leaving the keyboard.

![The command palette matching hosts, pools, runners and quick actions for the word "demo"](screenshots/command-palette-dark.webp#only-dark){ .zoomies-shot }
![The command palette matching hosts, pools, runners and quick actions for the word "demo"](screenshots/command-palette-light.webp#only-light){ .zoomies-shot }

## Pools

What runners to make. Each pool's labels, GitHub target, backend, floor and
ceiling, idle timeout, whether its runners are ephemeral and whether jobs get a
Docker daemon — and a risk badge on any pool that trades some of the default
safety away, so the trade is visible from the list.

![The Pools grid: two pools with their labels, backend, busy-against-live bars, queue depth, idle timeout, lifetime and Docker mode](screenshots/pools-dark.webp#only-dark){ .zoomies-shot }
![The Pools grid: two pools with their labels, backend, busy-against-live bars, queue depth, idle timeout, lifetime and Docker mode](screenshots/pools-light.webp#only-light){ .zoomies-shot }

A pool's own page shows its runners and recent jobs, the exact `runs-on:` line
a workflow writes to land here, and its configuration with the warnings — if
any — that the settings earn it.

![A pool's page: its runners and their states, recent jobs, the runs-on line to copy, and its configuration](screenshots/pool-dark.webp#only-dark){ .zoomies-shot }
![A pool's page: its runners and their states, recent jobs, the runs-on line to copy, and its configuration](screenshots/pool-light.webp#only-light){ .zoomies-shot }

## Runners

Every runner that exists right now and what each one is doing. Removed runners
are hidden by default, because a busy fleet makes and destroys thousands of
them and they are all history. Rows select for bulk drain or delete, and the
state filter is a real filter: it narrows the set rather than repainting it.

![The Runners grid: state, name, pool, host, current job, age, jobs handled, CPU and memory for each runner](screenshots/runners-dark.webp#only-dark){ .zoomies-shot }
![The Runners grid: state, name, pool, host, current job, age, jobs handled, CPU and memory for each runner](screenshots/runners-light.webp#only-light){ .zoomies-shot }

A runner's page carries the job it is on, a timeline of how long it spent in
each state — provisioning, registering, idle, busy — its resource usage as the
host's agent last reported it, and the live log.

![A busy runner's page: its current job, a timeline of its states, details and resource usage](screenshots/runner-dark.webp#only-dark){ .zoomies-shot }
![A busy runner's page: its current job, a timeline of its states, details and resource usage](screenshots/runner-light.webp#only-light){ .zoomies-shot }

## Jobs

Everything this fleet claims, runs or is waiting to run, with each job's queue
wait and duration. The filters — repository, workflow, pool, label, outcome,
state, dates — live in the URL, so a view can be pasted into a chat. A queued
job that no enabled pool claims is called out at the top of the page, because
it is almost always a typo in `runs-on`.

![The Jobs grid with a warning that one queued job has no pool to run it](screenshots/jobs-dark.webp#only-dark){ .zoomies-shot }
![The Jobs grid with a warning that one queued job has no pool to run it](screenshots/jobs-light.webp#only-light){ .zoomies-shot }

Opening a job says where it went wrong first: the step that failed and how
long it ran, with a link to that step's log on GitHub — or, when the runner
died under it, that the failure is the fleet's and the workflow did nothing
wrong.

![A failed job's drawer: the failing step named at the top, then the job's details, its steps with timings and a link to the run](screenshots/job-dark.webp#only-dark){ .zoomies-shot }
![A failed job's drawer: the failing step named at the top, then the job's details, its steps with timings and a link to the run](screenshots/job-light.webp#only-light){ .zoomies-shot }

## Usage

Runner-hours, jobs and queue waits over a date range, grouped by pool,
repository, workflow or installation, with an estimated cost wherever an
administrator has given a pool a rate. Zoomies embeds no cloud prices. The
table exports as CSV.

![The Usage report grouped by pool: runner-hours, jobs queued, started and completed, average queue wait and peak concurrency](screenshots/usage-dark.webp#only-dark){ .zoomies-shot }
![The Usage report grouped by pool: runner-hours, jobs queued, started and completed, average queue wait and peak concurrency](screenshots/usage-light.webp#only-light){ .zoomies-shot }

## Hosts

Where runners can go. Each machine's heartbeat, its slots in use, the backends
its agent found — and the exact command to run when one is missing — and the
labels pools select it by. A cordoned host keeps its runners and takes no new
ones. *Add a host* mints a join token and prints the one line to paste on the
new machine.

![The Hosts page: three host cards showing health, slots in use, detected backends with a fix for the missing one, and labels, above the join tokens panel](screenshots/hosts-dark.webp#only-dark){ .zoomies-shot }
![The Hosts page: three host cards showing health, slots in use, detected backends with a fix for the missing one, and labels, above the join tokens panel](screenshots/hosts-light.webp#only-light){ .zoomies-shot }

## Installations

The GitHub App connections: which organisation or repository, the App and
installation IDs, how much of the API rate limit is left, and every webhook
delivery GitHub has made, accepted or rejected — so an empty list beside a
running workflow says the deliveries are not arriving, which is the fault that
otherwise looks like a slow fleet.

![The Installations page: one connected organisation with its App, installation, API, pools and rate limit, above the webhook delivery log](screenshots/installations-dark.webp#only-dark){ .zoomies-shot }
![The Installations page: one connected organisation with its App, installation, API, pools and rate limit, above the webhook delivery log](screenshots/installations-light.webp#only-light){ .zoomies-shot }

## Migrate

The wizard that moves repositories onto the fleet: choose an installation,
tick repositories, map each hosted-runner label to a pool, and review the exact
diff before one pull request per repository is opened. Jobs it will not touch —
a `${{ matrix.os }}` expression, a runner that is already self-hosted — are
listed with the reason, here and in the pull request body. [How it
works](migration.md).

![The migration wizard's review step: a unified diff changing runs-on from ubuntu-latest to the pool's label, and the jobs left alone with their reasons](screenshots/migrate-dark.webp#only-dark){ .zoomies-shot }
![The migration wizard's review step: a unified diff changing runs-on from ubuntu-latest to the pool's label, and the jobs left alone with their reasons](screenshots/migrate-light.webp#only-light){ .zoomies-shot }

## Audit

Every change made through this controller and who made it — users, API tokens
and the system itself — with the target and the source address. Open an event
to see exactly what changed. Secrets were redacted when the row was written,
so nothing here can leak one.

![The Audit grid: when, actor, action, target and source address for each change](screenshots/audit-dark.webp#only-dark){ .zoomies-shot }
![The Audit grid: when, actor, action, target and source address for each change](screenshots/audit-light.webp#only-light){ .zoomies-shot }

## Settings

Accounts and their roles, API tokens, appearance, the configuration this
controller is actually running with, and its version. Zoomies refuses any
change that would leave no enabled administrator.

![Settings: the signed-in account, and the Users tab listing one administrator](screenshots/settings-dark.webp#only-dark){ .zoomies-shot }
![Settings: the signed-in account, and the Users tab listing one administrator](screenshots/settings-light.webp#only-light){ .zoomies-shot }

## On a phone

Read-only monitoring from a phone is a stated requirement, so it is tested. The
navigation moves to the bottom edge, the tiles stack, and everything still
updates in place.

![The Overview on a phone: the metric tiles stacked, the problems count in the top bar and the navigation along the bottom edge](screenshots/overview-phone-dark.webp#only-dark){ .zoomies-shot .zoomies-phone }
![The Overview on a phone: the metric tiles stacked, the problems count in the top bar and the navigation along the bottom edge](screenshots/overview-phone-light.webp#only-light){ .zoomies-shot .zoomies-phone }

The design system behind all of this — tokens, status colours, components and
the accessibility checklist — is in [UI guidelines](ui-guidelines.md).
