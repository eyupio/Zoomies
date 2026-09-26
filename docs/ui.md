---
description: >-
  A tour of the Zoomies web UI page by page: the Overview, pools, runners,
  jobs, hosts, providers and the machines they rent, the migration wizard and
  settings, light and dark.
---

# The UI

Twelve pages, one job each. Everything on them is live — every page updates in
place from the controller's event stream, so you never have to press refresh,
though the same button sits at the top of each one for when you want to be sure
— and nothing is reachable from the UI that is not reachable from the
[REST API](api-surface.md). Light and dark follow your system until you
choose one, and the screenshots below follow this site's.

The fleet in them is the demo fixture the Playwright suite runs against: two
pools, three hosts, a dozen runners across every state the controller knows,
and a morning's worth of jobs. `ZOOMIES_SEED_DEMO=true` writes the same fleet
into an empty controller, so you can walk through these pages yourself before
connecting GitHub.

## Signing in

Before any of them, the page everyone meets first — and often the only one met
by people who did not install the controller. The form is on the right, under
the address of the instance being signed in to, so telling a staging controller
from a production one does not rest on reading the address bar. On the left, in
Zoomies Black in either theme, is what this is: a line on what Zoomies does,
three facts true of every installation, and links to [zoomies.sh](https://zoomies.sh),
the source on GitHub and [EyUp.io](https://eyup.io), who make it. A failed
sign-in says which kind of failure it was — wrong credentials, too many
attempts, or a controller that cannot be reached — and caps lock is called out
before it costs an attempt. On a phone the logo becomes a band above the form
and the links move below it.

![The sign-in page: a Zoomies Black panel on the left holding the lockup, a one-line description, three facts and links to zoomies.sh, GitHub and EyUp.io; the form on the right, under the address of the instance being signed in to](screenshots/sign-in-dark.webp#only-dark){ .zoomies-shot }
![The sign-in page: a Zoomies Black panel on the left holding the lockup, a one-line description, three facts and links to zoomies.sh, GitHub and EyUp.io; the form on the right, under the address of the instance being signed in to](screenshots/sign-in-light.webp#only-light){ .zoomies-shot }

## Overview

The page that has to earn the second monitor. It opens on the activity
matrix, showing today by the hour: a row of twenty-four squares coloured by
what finished, greener as more jobs finish and red the moment any fail. Widen
it to the year and the same band becomes the fleet's days laid out the way a
contribution graph is — a column per week, a row per weekday, the month named
above the week it begins in. Hover a square and it says everything
it holds: how many jobs were queued, started and finished, how they ended,
the runner time they used and the pool-minutes spent at capacity. Select one
and the day opens under the grid, hour by hour, with links to that day's jobs
and its usage report. A select recolours the same squares by queued jobs,
runner time or how often a pool was blocked on capacity, so a queue that
backs up every Monday is a shape rather than a table, and the quick ranges
— 1d, 7d, 30d, 90d, 1y — cut the window: today and the last week are drawn by
the hour, a row of twenty-four squares per day, which is the punch card that
shows when the fleet is busy. The last 30 and 90 days are the calendar with
each day cut into squares side by side — six of four hours, or three of eight
— so a month fills the band rather than sitting in five columns at the left of
it, and shows whether the work lands in the morning or overnight. Select one
of those and its day opens hour by hour with the square's own hours picked
out. It opens on today, and the range you choose instead is remembered. Every
range is drawn whole at the width of the screen, its squares sized to fit — a
year is twelve months on a phone too — and the grid is one tab stop: the arrow
keys walk it, Enter selects.

Under it, four numbers with an hour of shape behind them — queued jobs,
running jobs, live runners, and the median queue wait with its p95 — then how
long runners take to start and to register. Then each pool's busy runners
against its live ones with the floor and ceiling marked, what is running this
moment, and a feed of what has happened to the fleet lately: the scheduler's
decisions in its own words — *scaled zoomies-demo-linux-x64 4 → 5: 1 job
queued* — beside how each job ended and the step it stopped at, a runner
that failed and a runner that came up ready for work, a runner lent spare CPU
— *Squirrel spotted — maximum zoomies* — or slowed because its host is under
pressure, a host that went quiet, a machine a provider is renting and a pool
somebody changed. What went right is in it as much as what went wrong. Which of those it carries is yours to choose, one switch per
kind on **Settings → Events**, and the panel counts what it is showing rather
than quietly leaving the rest out. When
something needs a person it is one line and a *Review* button, never a list
that pushes the fleet below the fold. The
*Other runners* switch says whether these numbers count only the jobs this
fleet ran or every job GitHub reported on an installed repository — the
default is this fleet's own work, because that is the question an operator is
usually asking.

![The Overview: the activity matrix across the top, then four metric tiles with an hour of sparkline behind each, runner startup and registration times, a one-line problems summary, per-pool utilisation bars and a feed of the fleet's recent events](screenshots/overview-dark.webp#only-dark){ .zoomies-shot }
![The Overview: the activity matrix across the top, then four metric tiles with an hour of sparkline behind each, runner startup and registration times, a one-line problems summary, per-pool utilisation bars and a feed of the fleet's recent events](screenshots/overview-light.webp#only-light){ .zoomies-shot }

![The activity matrix with a day selected: the day's figures, its outcomes hour by hour, and links to its jobs](screenshots/activity-dark.webp#only-dark){ .zoomies-shot }
![The activity matrix with a day selected: the day's figures, its outcomes hour by hour, and links to its jobs](screenshots/activity-light.webp#only-light){ .zoomies-shot }

## The problems drawer

Reachable from the count in the top bar on every page. Cordoned or silent
hosts, failed registrations, a queued job no pool will run, a job whose runner
stopped under it, a provider that could not be reached or a machine that never
became a host, and every configuration setting that weakens the default
posture — worst first, each saying what is true, why it matters and what to
change, with a link to the page where you change it: the provider, the machine,
the pool or the runner it is about, each of which has one.

![The problems drawer open over the Overview, each entry saying what is true, why it matters and what to change](screenshots/problems-dark.webp#only-dark){ .zoomies-shot }
![The problems drawer open over the Overview, each entry saying what is true, why it matters and what to change](screenshots/problems-light.webp#only-light){ .zoomies-shot }

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

![The Pools page: queue pressure and configured headroom above each pool's runner and configuration details](screenshots/pools-dark.webp#only-dark){ .zoomies-shot }
![The Pools page: queue pressure and configured headroom above each pool's runner and configuration details](screenshots/pools-light.webp#only-light){ .zoomies-shot }

The wizard that creates one is seven steps on its advanced path, which every
step of the simple path offers, and the **Size** step is where
the pool says how much machine one runner gets: *one share of each host*,
with what every host in the fleet would give a runner listed underneath, or a
fixed size on every host. Under the shared size sit the two fields of
[elastic CPU zoomies](elastic-cpu.md) — **Elastic CPU**, off, observe only or
automatic boost, and **Boost ceiling** — so lending a busy runner the host's
spare CPU is a choice made beside the guarantee it builds on. The verdict at
the foot of each step is the controller's own, asked as you type, so a pool
that no host could run is refused with the reason before it is saved.

A pool's own page shows its runners and recent jobs, the exact `runs-on:` line
a workflow writes to land here, and its configuration with the warnings — if
any — that the settings earn it. **Edit** reopens the same wizard on the same
pool. A size typed there applies to the next runner it creates; the elastic
CPU policy is read on every heartbeat, so that change reaches runners already
running.

![A pool's page: its runners and their states, recent jobs, the runs-on line to copy, and its configuration](screenshots/pool-dark.webp#only-dark){ .zoomies-shot }
![A pool's page: its runners and their states, recent jobs, the runs-on line to copy, and its configuration](screenshots/pool-light.webp#only-light){ .zoomies-shot }

## Runners

Every runner that exists right now and what each one is doing. Removed runners
are hidden by default, because a busy fleet makes and destroys thousands of
them and they are all history. Rows select for bulk drain or delete, and the
state filter is a real filter: it narrows the set rather than repainting it.
Above the grid, the runner lifecycle is the controller's own state machine
drawn out — provisioning, registering, idle, busy, draining — with how many
runners are at each step this moment and a link into each; under it, a bar of
the same states whose segments are links too, and say their share when you
hover them.

![The Runners page: job queue depth, runner state totals, provisioning demand and lifecycle composition above the runner grid](screenshots/runners-dark.webp#only-dark){ .zoomies-shot }
![The Runners page: job queue depth, runner state totals, provisioning demand and lifecycle composition above the runner grid](screenshots/runners-light.webp#only-light){ .zoomies-shot }

A runner's page carries the job it is on, a timeline of how long it spent in
each state — provisioning, registering, idle, busy — its resource usage as the
host's agent last reported it, and the live log. Beside the usage is the
**allocation**: the CPU and memory the runner was created with, and whether the
pool set them or the host gave it its default share of the machine. The source
is the thing to read when a runner was killed for exceeding its memory: a limit
from the pool is raised on the pool, and a host's share is raised by lowering
the host's capacity or by giving the pool a `memory_mb` of its own. Beside the allocation is its **CPU state**: the guaranteed, current and
ceiling CPU together, under a label that says what is happening to the quota
right now — *Squirrel spotted — maximum zoomies* for a runner lent most of a
host, *Rabbit spotted — extra zoomies* for a smaller boost, *Steady paws —
guaranteed pace* at its share, *Nose to the wind — watching spare CPU* for a
pool that measures without moving, and *Leash tightened — host under
pressure* when the throttle has taken it below its guarantee. The last is
shown for any limited runner, elastic or not, because a job running at three
quarters of its allocation is slow for a reason the runner itself cannot
show. [Elastic CPU zoomies](elastic-cpu.md) says what each state means and
how the decision is made.

![A busy runner's page: its current job, a timeline of its states, details and resource usage](screenshots/runner-dark.webp#only-dark){ .zoomies-shot }
![A busy runner's page: its current job, a timeline of its states, details and resource usage](screenshots/runner-light.webp#only-light){ .zoomies-shot }

The expandable activity panel shows the queued jobs, running jobs, idle runners
or live runners over the last day, six hours or hour — it opens on the day, and
a wider window folds
the minutes into intervals that carry their peak, so a spike is never averaged
away. Hover the line for every figure at that moment, or inspect it with the
timeline control; gaps indicate missing samples. Fleet context is independent
of grid filters, while selecting a pool scopes the headline runner metrics.

![Runner history expanded, with a one-hour trend and minute-by-minute coverage](screenshots/runner-history-dark.webp#only-dark){ .zoomies-shot }
![Runner history expanded, with a one-hour trend and minute-by-minute coverage](screenshots/runner-history-light.webp#only-light){ .zoomies-shot }

## Queue

Provisioning demand has its own view, with ready, expedited, paused and removed
counts alongside filtering and bulk controls. Job queue depth can remain high
when provisioning demand is paused; these are separate measures. Removing an
item is the exception: an operator who takes work out of the queue has said it
is not for this fleet, so it stops counting as queued everywhere — the
Overview's queue depth and pool bars, `zoomies_jobs_queued` and the queue age
with it. Restoring it from the Removed view puts it back.

![The Queue page: provisioning demand composition, status filters and bulk controls](screenshots/queue-dark.webp#only-dark){ .zoomies-shot }
![The Queue page: provisioning demand composition, status filters and bulk controls](screenshots/queue-light.webp#only-light){ .zoomies-shot }

## Workflows

What GitHub's Actions tab lists, in this fleet's terms: one row per workflow
run — the *#1009* beside a workflow's name on GitHub — with the jobs GitHub
reported under it summed up. A run's state and conclusion are worked out the
way GitHub's own run page works them out, over the latest attempt of each job:
running while any job is, queued while any waits for a runner, and once every
job has finished, the worst outcome among them — so a run whose failed job was
re-run to success reads as a success. The row says how many jobs the run has
and how they are getting on, when it was queued, how long it waited and how
long it took, and links to the same run on GitHub.

Each row opens in place — press it, or the chevron, or the right arrow — to
the jobs inside the run: every job with its state, the pool that claimed it,
the runner that ran it, the step it failed at, its queue wait and duration,
and a link to it on GitHub. Earlier attempts are listed too, marked with their
attempt, because the attempt that failed is usually why somebody is looking.
Pressing a job opens the same drawer the Jobs page opens. Nothing here is a
second copy of anything: a run is derived from its jobs, and the jobs under it
are the same rows, so the two cannot disagree.

A run's row carries the [Queue](#provisioning-queue) page's controls for every
queued job of the run at once — **Run now**, **Pause** and **Resume**, one
press each, and **Cancel** beside them where the deployment allows it — and
each job inside an opened run carries the same three for itself. They are the
Queue page's actions under the Queue page's names, through the same endpoint,
and mean exactly what they mean there: Run now raises the run's queued jobs
ahead of the rest of their pool's queue within its priority tier and skips the
scale-up delay, Pause holds their demand, and neither changes what GitHub
thinks of the run. A run-level action leaves alone a job an operator removed
from the queue, which was stood down on purpose and comes back from the Queue
page's Removed view; and removal itself is not offered on a run, because taking
work out of the queue is a decision about one job. An action already in force —
Pause on a run whose every queued job is paused — is disabled and says why, and
the row badges what has been done, **Paused** or **Run now**, with a count where
it is only some of the run's jobs.

The status views and the filters are the Jobs page's, read at the run's
level — a status names the run's own, and any other filter keeps a run
whenever one of its jobs matches, so a run arrives whole rather than reduced
to the job that matched. **Every job** switches to the Jobs page with the same
filters in force.

## Jobs

The Workflows page one step down: every job on a row of its own, where
Workflows has the run each belongs to. It is not in the navigation — Workflows
is — but every link that names a job lands here, and **Workflow runs** at the
top goes back up with the same filters in force. Everything this fleet claims,
runs or is waiting to run is listed, with each job's queue wait and duration.
Status is the filter this page is opened for, so it is a row of buttons above
the grid — **Running**, **Queued**, **Failed**, **Finished**, **All** — and
the page opens on *Running*, which is the question an operator arrives with. That default is for a bare visit only: every link into this page
that already carries a filter keeps it, so the problems drawer's unmatched link
and the Overview's outcome links still show what they promised.

The rest of the filters — repository, workflow, pool, label, outcome, dates —
live in the URL alongside it, so a view can be pasted into a chat. A queued job
that no enabled pool claims is one filter away — *Unmatched only* — and the
problems drawer links straight to it: on an organisation that also rents
runners elsewhere, most such jobs are somebody else's rather than a fault.

*Queued* and *Running* mean work this fleet actually has in hand. Both leave
out a job whose workflow run has been cancelled, and *Queued* also leaves out
anything an operator removed from the queue — the same sets the Overview's two
tiles count, and a chip above the grid says so where a filter is in force.

None of those jobs is hidden: they are in the history under *All*, badged
**Cancelling**, **Removed** or **Paused**. GitHub goes on calling all three
`queued` or `in_progress`, because Zoomies can neither unqueue a job nor
conclude one, and a row that showed only GitHub's word for it left the
operator's own decision invisible — and the fleet reporting work nobody was
going to do. `?provisioning=deleted` and `?cancelling=true` narrow to each on
its own, and the [Queue](#queue) is where removed work is restored.

A cancellation is the sharpest case, because the gap is GitHub's rather than
this fleet's. GitHub accepts the request at once; its completion delivery,
which settles the conclusion, can be minutes behind. Zoomies stops the work
immediately — queued demand paused, runners taken back — and records that it
did, so the tiles and the lists agree from that moment rather than from
whenever GitHub gets round to it.

![The Jobs page: queue depth, running jobs, success rate, P95 wait and outcome composition above the job grid](screenshots/jobs-dark.webp#only-dark){ .zoomies-shot }
![The Jobs page: queue depth, running jobs, success rate, P95 wait and outcome composition above the job grid](screenshots/jobs-light.webp#only-light){ .zoomies-shot }

Opening a job says where it went wrong first: the step that failed and how
long it ran, with a link to that step's log on GitHub — or, when the runner
died under it, that the failure is the fleet's and the workflow did nothing
wrong.

![A failed job's drawer: the failing step named at the top, then the job's details, its steps with timings and a link to the run](screenshots/job-dark.webp#only-dark){ .zoomies-shot }
![A failed job's drawer: the failing step named at the top, then the job's details, its steps with timings and a link to the run](screenshots/job-light.webp#only-light){ .zoomies-shot }

## Usage

Runner-hours, jobs and queue waits over a range — today, until you change
it — grouped by pool, repository, workflow or installation, with an estimated cost wherever an
administrator has given a pool a rate. Zoomies embeds no cloud prices. The
table exports as CSV.

The quick ranges beside the grouping are the last hour, six hours and twelve
hours up to now, or today and the last seven, thirty and ninety days, with the
one in force pressed. The From and To fields take a time of day as well as a
date, so a report can be cut to the hour an incident began; editing either
lets go of the quick range. Either way the range is in the address, so the
report is a link.

The range is drawn as a chart with the same hand as the Overview's fleet
activity: a line per figure, chosen by chip — the jobs queued and how they
ended, or executing runner time against the time allocated to hold the runner
— and the moment under the pointer read off every line at once, in the rows
beneath as well as beside the crosshair, so a figure never lives only in a
card that a finger's lift takes away. Where a pool had work and nowhere to put
a runner, the intervals are shaded behind the lines. An interval that has not
happened yet is a gap rather than a zero: a report to the end of today is a
window with hours still in it.

The same activity matrix as the Overview's draws the
chosen range in the squares the Overview uses for a window that long — a
square per hour for up to a fortnight, each day cut into six four-hour
squares up to eight weeks and three eight-hour ones up to sixteen, and a
square per day laid out as a calendar beyond — cut to the grouping and the
group in focus, so a repository's bad week is a red row of squares rather
than a column of numbers. Selecting a square moves the chart's crosshair to
the interval it falls in.

![The Usage dashboard: runner-hours, job outcomes and queue wait as tiles, the range drawn as a line per figure with the interval's readings beneath it, the largest consumers of runner time, and the activity matrix, grouped by pool](screenshots/usage-dark.webp#only-dark){ .zoomies-shot }
![The Usage dashboard: runner-hours, job outcomes and queue wait as tiles, the range drawn as a line per figure with the interval's readings beneath it, the largest consumers of runner time, and the activity matrix, grouped by pool](screenshots/usage-light.webp#only-light){ .zoomies-shot }

## Hosts

Where runners can go. Each machine's heartbeat, its slots in use, the disk its
runners have left to write into, what the fleet has already committed of its CPU
and memory against what may be placed on it, the backends its agent found — and
the exact command to run when one is missing — and the labels pools select it
by. Slots and the committed bars answer different questions: the first is
whether the fleet will place another runner here, the second whether the machine
can carry it, and a host with free slots and no memory left takes nothing.
Each of the two settings a host has is reached from the thing it describes.
*Adjust*, beside the slot bar, owns the resources: how many runners the host may
hold, and the reserve — the cores, memory and disk the scheduler leaves alone
for the machine's own sake. Each sits on a slider with the recommendation marked
on it, worked out from the machine's size and the largest ask across your enabled
pools, and *Set to recommendations* puts all four back in one press; a setting
past its mark warns rather than refuses. *Edit*, beside the labels, sets the
labels and nothing else. A cordoned host keeps its runners and takes no new
ones. *Add a host* mints a join token and prints the one line to paste on the
new machine.

**Agent connected** describes the heartbeat. Recent **CPU usage**,
**memory available** and the one-minute **load** describe the machine's actual
load, separately from its committed resources. A pressure warning says whether
new starts are held or limited to one at a time; recovery happens automatically
and keeps any manual cordon. A missing or stale reading is shown as unavailable.
See
[current usage and automatic holds](hosts-and-pools.md#current-usage-and-automatic-holds)
for the thresholds and the limits of these measurements.

A host the controller has stepped down after sustained pressure wears a
**Throttled** badge, with the step in its title, and its slots line reads
"*n* of *m* slots in use · throttled from *capacity*": the smaller figure is
what the host is taking right now, and the configured capacity is untouched.
The notice under it is the throttle's own sentence — what was taken, which
measurement did it, what the running jobs are getting, and that it lifts one
step after five minutes of calm — and **Lift the throttle** beside it clears the
throttle by hand once the cause is fixed. The adjust dialog's note about the
floors under a reserve names the CPU floor too: half a core, or a twentieth of
the machine, held back for the daemon whatever the operator sets.

![The Hosts page: fleet health and eligible slots, and the capacity map — every host's measured and committed utilisation on one chart over the last day, with the hosts and measurements to draw switched on and off beneath it](screenshots/hosts-dark.webp#only-dark){ .zoomies-shot }
![The Hosts page: fleet health and eligible slots, and the capacity map — every host's measured and committed utilisation on one chart over the last day, with the hosts and measurements to draw switched on and off beneath it](screenshots/hosts-light.webp#only-light){ .zoomies-shot }

## Providers

Where machines come from. A provider is one place Zoomies may rent a machine —
a hypervisor, today [Proxmox VE](proxmox.md) — and its card leads with what is
being spent: the machines it holds against the ceiling it may not pass, the
shape it builds, and, in the controller's own sentence, why no new machine may
be bought this moment. A new provider starts at a ceiling of zero and rents
nothing, so turning a provider on and saying how much of it you are willing to
pay for are the same act. *Check* runs the preflight against the real provider
— read-only there, audited here — and a provider nobody has ever checked says
so rather than looking healthy. Under the cards is every machine across every
provider, narrowed by a state filter that lives in the URL, so a link to one
part of the lifecycle is a link somebody else can open.

![The Providers page: machines owned against the ceiling that may not be passed, the lifecycle band from planned to draining with the switch that pauses new machines beside it, and the card for the one provider this fleet rents from](screenshots/providers-dark.webp#only-dark){ .zoomies-shot }
![The Providers page: machines owned against the ceiling that may not be passed, the lifecycle band from planned to draining with the switch that pauses new machines beside it, and the card for the one provider this fleet rents from](screenshots/providers-light.webp#only-light){ .zoomies-shot }

Adding one is five steps — where it is and how we sign in, where a machine is
built, what shape it is, what it may spend, and what the controller makes of it
— and none of them creates anything: the draft is sent to the controller for a
verdict as it is typed, so a rejection appears beside the answer that caused it
while there is still a reason to change it. [Adding a Proxmox
provider](proxmox.md#the-provider) walks through the form. The credential is
sealed as it is stored and never comes back, so editing a provider shows an
empty credential box and leaving it empty keeps the stored one.

A provider's own page ends on the orphan review: resources at the provider
wearing this fleet's naming that no machine of ours accounts for, machines that
hold no resource, and machines nothing has confirmed are ours. **Nothing there
deletes anything.** Its one button forgets a row Zoomies holds and leaves the
resource behind it alone, because something we cannot prove is ours may be
somebody's hand-made VM that happens to be named like ours, and destroying that
is the one mistake that cannot be undone. The sweep behind the list is the
expensive part of answering, so it runs when the tab is opened and not before,
and the whole tab needs the administrator role.

A machine's page is written for the ten minutes when something is taking longer
than it should. Its timeline is one row per phase the machine actually reached,
the last row still counting while the machine is on its way, because a clone
that never finished and a guest that cloned and never enrolled are faults in
different systems. While an operation is in flight the page carries the
provider's own handle for it, to paste into the hypervisor's task log and read
the other half of the story, and it keeps the provider's complaint and the
guest's separately, so a success at one does not erase the other's.

![A machine's page: where it is at the provider, which pool's unmet demand asked for it, whether a delete would be safe, and a timeline of the phases it has reached with the last one still counting](screenshots/machine-dark.webp#only-dark){ .zoomies-shot }
![A machine's page: where it is at the provider, which pool's unmet demand asked for it, whether a delete would be safe, and a timeline of the phases it has reached with the last one still counting](screenshots/machine-light.webp#only-light){ .zoomies-shot }

The same lifecycle band sits on the Hosts page, above the hosts these machines
become, because an operator short of hosts is asking whether more are on their
way. The switch beside it stops new machines being bought across every
provider, and it is the only control there on purpose: it survives a restart,
and it holds nothing else back — a drain finishes, a delete completes, and a
machine already being built is still followed to wherever it ends up — so
pressing it during an incident stops the spending without stranding a VM. A
host Zoomies rented says so on its card, and its remove action sends you to
delete the machine instead: removing the host here would leave the machine
running, and on the bill.

There is no way to make a machine by hand, here or anywhere else. A machine
exists because a pool had queued work and nowhere to put it, and a second
source of supply would be a second thing the reconciler would then decide to
delete. An operator who wants more machines raises a ceiling. [What a provider
has to implement](providers.md) is the contract behind all of this.

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

![The migration wizard's review step: the exact diff for one repository, changing runs-on from ubuntu-latest to the pool's labels, and the jobs it will not touch](screenshots/migrate-dark.webp#only-dark){ .zoomies-shot }
![The migration wizard's review step: the exact diff for one repository, changing runs-on from ubuntu-latest to the pool's labels, and the jobs it will not touch](screenshots/migrate-light.webp#only-light){ .zoomies-shot }

## Audit

Every change made through this controller and who made it — users, API tokens
and the system itself — with the target and the source address. Open an event
to see exactly what changed. Secrets were redacted when the row was written,
so nothing here can leak one.

![The Audit grid: when, actor, action, target and source address for each change](screenshots/audit-dark.webp#only-dark){ .zoomies-shot }
![The Audit grid: when, actor, action, target and source address for each change](screenshots/audit-light.webp#only-light){ .zoomies-shot }

## Settings

A section of pages rather than a page of tabs, with its own rail beside them:
your account, appearance and which events the Overview's feed shows; the
accounts that can sign in and their roles, and the API tokens; the
configuration this controller is actually running with, its backups, and what
it is. Each page has an address of its own, so a
settings page is a link. Zoomies refuses any change that would leave no
enabled administrator, and the pages that need that role are listed for
everybody, marked rather than hidden.

![Settings: the section's rail beside the Users page, listing one administrator](screenshots/settings-dark.webp#only-dark){ .zoomies-shot }
![Settings: the section's rail beside the Users page, listing one administrator](screenshots/settings-light.webp#only-light){ .zoomies-shot }

### Backups

The one settings page that is a tool rather than a form, because a backup is
the one thing here you find out you needed at the worst possible moment.

The schedule is at the top — where copies go, how often, and how many are kept
— and under it the destinations those copies are sent on to: as many
S3-compatible buckets as you want to name, each added, tested and rotated here
without editing a file or restarting anything. Each says what it holds, when it
last heard from the service, and what it refused with if it refused. A
destination can also be listed and a copy brought back out of it, which is the
path a controller takes when the disk it was backing up is gone.

Then every copy this controller knows about, with what took it and whether it
verifies. From a row you can take one now, verify it, download it plain or
sealed with a passphrase, upload one taken somewhere else, and stage a restore
that the next restart applies.

Naming one is not the same as remembering to press a button afterwards: every
copy the schedule takes is sent on by the same pass that took it, and a bucket
that was unreachable for two nights is caught up with both backups it missed
rather than starting from the newest. What a destination costs — who can read
the archive, and what a plain-HTTP endpoint gives away — is set out in
[Security](security.md#a-backup-remote-with-no-passphrase), and the whole of it
in [Backup and restore](backup-and-restore.md).

## The status page

`/status` is a page for the people whose jobs run on the fleet and who have no
account on it. GitHub tells a developer whose job has queued for twenty
minutes only "queued"; this page says whether the fleet is **healthy**,
**degraded** or **blocked**, since when, roughly how many jobs are waiting and
running, how long the typical and the longest waits have been, and one plain
sentence for each problem the fleet has — no room on any machine, labels
nothing here offers, a permission the fleet has lost on GitHub, notifications
that stopped verifying. It names no pool, host, repository, runner or job, and
it never shows a problem with the controller itself.

It is off unless `status.mode` says otherwise: `authenticated` shows it to
anyone signed in, whatever their role, and `public` to anyone who can reach the
controller, which is warned about — see
[Security](security.md#statusmode-public). While it is off, `/status`,
`/status.svg` and `/api/v1/status` answer 404.

The page asks for the status every thirty seconds and never opens the live
event stream the rest of the interface runs on, because every frame on that
stream is about a named thing. `/status.svg` is the state as a badge, to put in
a team's wiki or a repository README:

```markdown
![Fleet status](https://zoomies.example.com/status.svg)
```

The sentence the page shows for each problem code is listed in
[Problem codes](problem-codes.md#what-the-status-page-says).

## On a phone

Read-only monitoring from a phone is a stated requirement, so it is tested. The
navigation moves to the bottom edge, the tiles stack, and everything still
updates in place.

Every grid stays the table it is on a desktop — one line per runner, scrolling
sideways inside its own frame to the columns that do not fit, never taking the
page with it. If you would rather read a row downwards, the **Cards / Rows**
toggle above each grid gives every value a line of its own, with nothing cut
off; **Settings → Appearance** sets which of the two every grid starts in.

![The Overview: four metric tiles with an hour of sparkline behind each, runner startup and registration times, a one-line problems summary, per-pool utilisation bars and a feed of the fleet's recent events](screenshots/overview-phone-dark.webp#only-dark){ .zoomies-shot .zoomies-phone }
![The Overview: four metric tiles with an hour of sparkline behind each, runner startup and registration times, a one-line problems summary, per-pool utilisation bars and a feed of the fleet's recent events](screenshots/overview-phone-light.webp#only-light){ .zoomies-shot .zoomies-phone }

The design system behind all of this — tokens, status colours, components and
the accessibility checklist — is in [UI guidelines](ui-guidelines.md).

Nothing here needs a GitHub App to look at: the [quick start](quickstart.md)
takes about five minutes, and `ZOOMIES_SEED_DEMO=true` fills a fresh controller
with the same fleet these screenshots were taken from.

## Provisioning queue

Open **Queue** to manage the demand that causes Zoomies to create runners. Each
row represents a queued GitHub job's provisioning demand, rather than a runner
that already exists.

| Action | Effect |
| --- | --- |
| Pause | Stop counting the selected items towards new runner demand. |
| Resume | Restore normal demand and clear Run now priority. Also restores deleted items. |
| Delete from queue | Suppress demand persistently, and stop the item counting as queued work anywhere. Use the Removed view to find and restore it. |
| Run now | Resume and expedite demand within the pool's priority tier, bypassing the scale-up delay. |

All four are buttons on the row itself, one press each, as well as on the bulk
bar for a selection. An action already in force is disabled and says why. On the
keyboard a row's buttons are one stop, with the arrow keys moving along them.
The first three are offered again on the [Workflows](#workflows) page, on a
run's own row for every queued job of the run and on each job inside an opened
run, under the same names and through the same controls.

These controls do not cancel GitHub jobs or retract provisioning tasks already
issued to agents. Pool minimums and normal runner lifecycle rules still apply;
existing runners may pick up a GitHub job whose provisioning demand is paused.
Run now respects disabled pools, host capacity, runner maximums, repository
limits, failure backoff and recovery fencing.

Filter by repository, workflow, pool, all required labels, exact branch, queued
dates, unmatched work and provisioning status. Multiple values within repository,
workflow, pool or status filters match any selected value; different filters
combine. Labels must all match. The four status cards retain the other filters
and show their counts independently of the status filter. Deleted items are
excluded from the initial view.

Checkboxes select individual rows; **Select all matching** snapshots up to 5,000
matching IDs across every page. The confirmation applies to those IDs only, so
later arrivals cannot be included silently. Items that start or finish before
the action commits are skipped, with individual results. Changing filters clears
the all-matching selection. Actions require the operator role and are audited;
API tokens need `provisioning:write`.

Filters live in the URL for sharing. **Save view** keeps up to 20 named filter
sets in this browser; saving the same name replaces it. Table sorting affects
presentation only.

### Provisioning order

Higher pool priorities receive capacity first. Within a priority tier, pools
with Run now demand come first; otherwise the least recently provisioned pool
gets the first turn. Each backlogged pool gets one slot per allocation round.
Provisioning history includes removed runners, so fast-finishing runners and
controller restarts do not reset fairness. Within each pool, Run now demand
comes before ordinary demand, then oldest queued time, then job ID to break ties.
Pool priority and an explicit Run now preference can defer ordinary work.

This is an order for provisioning capacity. GitHub chooses which compatible job
an available runner actually executes. Open a queue row for its current waiting
explanation, including explicit paused/deleted demand.
