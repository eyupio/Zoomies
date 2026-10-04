---
icon: material/view-dashboard-outline
title: "Architecture: a runner controller without Kubernetes"
description: >-
  How Zoomies is put together: one Go binary, SQLite, a pure scheduler and
  outbound-only agents — and why a self-hosted runner controller needs no
  Kubernetes.
---

# Zoomies architecture

Zoomies is a self-hosted controller for GitHub Actions self-hosted runners. It
watches for queued jobs, creates a fresh runner for each one, and throws the
runner away when the job finishes.

It is one Go binary. `zoomies controller` runs the control plane; `zoomies agent`
runs the thing that actually starts containers. On a single VM the controller
runs an agent inside itself, so the whole system is one process, one SQLite file
and one systemd unit.

## Why this shape

The project this one is modelled on kept a handful of long-lived runners on one
machine, described by a YAML file, registered by hand with a token that expires
after an hour. That works until it doesn't:

| Problem | What Zoomies does instead |
| --- | --- |
| Runners are long-lived, so job state leaks between workflow runs | Ephemeral by default: one job per runner, then the container is destroyed |
| A personal access token in a dotfile next to each runner | GitHub App credentials, sealed at rest, and Zoomies mints short-lived JIT registrations itself |
| 5-minute polling | `workflow_job` webhooks, with polling only as a fallback |
| One host | A controller plus any number of agents, each connecting outbound |
| Runtime state in dotfiles inside runner directories | One SQLite database that can be queried, backed up and audited |
| No auth, no audit | Local users with argon2id or OIDC, RBAC, scoped API tokens, and an audit row for every mutating action |

## The picture

Follow the arrowheads: they are connections, not data. GitHub is the only thing
that ever dials in, and only as far as the controller's webhook endpoint.
Everything else reaches outward -- agents to the controller, runners to GitHub --
so a host that runs jobs needs no inbound firewall rule at all. Everything an
agent does travels on connections it opened itself: it long-polls for tasks, and
posts results and logs back the same way.

```mermaid
flowchart LR
    subgraph host["a runner host -- any number of them, behind NAT"]
        ag["zoomies agent<br/>docker | podman | process"]
        run["runner containers<br/>one job each, then destroyed"]
    end

    ctrl["zoomies controller<br/>API, UI, scheduler, SQLite,<br/>and an embedded agent on a single VM"]

    subgraph gh["GitHub"]
        ghapi["REST API"]
        ghq["Actions job queue"]
    end

    ag -->|"outbound HTTPS only"| ctrl
    ag -->|"start, inspect, destroy"| run
    ctrl -->|"App JWT, installation tokens,<br/>single-use JIT configurations"| ghapi
    ghapi -->|"workflow_job webhooks"| ctrl
    run -->|"the runner long-polls for its own job"| ghq
```

## Request and event flow

### A job arrives

One queued job, from the webhook that announces it to the row the controller
reaps when it is over:

```mermaid
sequenceDiagram
    autonumber
    participant GH as GitHub
    participant C as controller
    participant S as scheduler
    participant DB as SQLite
    participant A as agent
    participant R as runner container

    GH->>C: POST /webhooks/github -- workflow_job, queued
    C->>C: verify the HMAC, record the delivery
    C->>DB: upsert the job, never backwards
    C->>S: snapshot -- pools, runners, queued jobs, hosts
    S-->>C: plan -- "create 1 in zoomies-linux-x64: 1 job queued"
    C->>GH: ask for a single-use JIT configuration
    GH-->>C: JIT configuration
    C->>DB: runner row, state provisioning
    A->>C: long-poll for tasks
    C-->>A: create_runner, with the JIT configuration
    A->>R: start the container
    R->>GH: register, then wait for a job
    A-->>C: registering, then idle
    GH->>C: workflow_job, in_progress
    C->>DB: runner busy, job linked to the runner
    GH-->>R: the job runs here
    GH->>C: workflow_job, completed
    R->>R: an ephemeral runner exits after one job
    A-->>C: removed
    C->>DB: reap the row
    A->>R: delete the container and its scratch space,<br/>once the report is in and finished_retention has passed
```

In detail:

1. GitHub POSTs `workflow_job` (action `queued`) to `/webhooks/github`.
2. The controller verifies the HMAC signature, records the delivery, and upserts
   a `jobs` row. Deliveries are at-least-once and can arrive out of order, so
   the upsert refuses to move a job backwards through its lifecycle.
3. The reconcile loop wakes immediately (it also runs on a timer).
4. `internal/scheduler.Decide` is handed a snapshot -- pools, runners, queued
   jobs, hosts, and the tunables -- and returns a `Plan`. It is a pure function:
   no clock reads, no database, no network. That is what makes the scaling
   behaviour testable, and it is where every scaling decision's *reason string*
   comes from. Which host each new runner is placed on is decided here too --
   healthy, uncordoned, offering the pool's backend, *being the platform the
   pool asked for*, matching its host selector, with room left -- and
   [Hosts and pools](hosts-and-pools.md) is that rule in operator terms. A pool
   that names no platform, and a host whose agent has not reported one, both
   constrain nothing, so adding a platform narrows placement and never widens
   it.
   Eligible hosts are ranked by CPU and memory headroom after placement.
   Optional usage measurements arrive on agent heartbeats; the controller
   validates them and records its receipt time. The snapshot carries those
   measurements and pressure holds, so the scheduler remains pure. Reservations
   for pending and newly planned runners share one budget across pools.
5. For each `create` action the controller picks the installation, resolves the
   runner image from the pool's own image or its platform, asks GitHub for a JIT
   configuration, writes a `runners` row in `provisioning`, and queues a task for
   the chosen host's agent. A JIT configuration registers exactly one
   runner and cannot be replayed, which is what makes it safe to hand to a
   container in its environment; a non-ephemeral pool has to run `config.sh`, so
   it gets a registration token instead.
6. The agent long-polls, picks up the task, and starts a container with the JIT
   config in its environment. It reports back: `registering`, then `idle`.
7. GitHub hands the job to the runner. A `workflow_job` `in_progress` webhook
   moves the runner to `busy` and links the job row to the runner row.
8. On `completed`, the ephemeral runner exits by itself. The agent notices,
   reports `removed`, and the controller reaps the row.
9. The host cleans up after itself. The controller has no reason to send a
   task for a runner it already considers gone, so the agent deletes the
   finished workload on its own -- the exited container with the runner's log,
   its docker-in-docker sidecar, any scratch directory Zoomies made for it, or
   the process backend's runner directory -- once the controller has accepted
   the report and [`agent.finished_retention`](configuration.md#agentfinished_retention)
   has passed. The window is what keeps a finished runner's output readable
   from the Runners page for a while; the report gate is what keeps the exit
   code from being deleted before anyone has heard it.

Every one of those steps publishes on the event bus, and the UI is watching a
Server-Sent Events stream, so the operator sees it happen without refreshing.
So does every change an operator makes through the API, and the two things
that are computed rather than stored -- the Overview's statistics and the
problems list -- are worked out again after every pass and sent when they
differ. Each frame is the resource's `GET` shape, rendered by the same code
(see [api-surface.md](api-surface.md#sse-event-kinds)).

### Runner starts on one host

See [Runner startup under host load](runner-startup-stability.md) for controls,
the allocation fix and the upstream comparison.

Each agent admits one runner create or image prewarm at a time, shared across
all pools and backends on that host. Runner starts are FIFO and take priority
over waiting background refreshes. The slot covers image preparation, the
DinD daemon readiness, runner creation and failure cleanup. The sidecar must
pass a bounded `docker info` health probe before the runner is created; a
running container alone no longer releases the next startup. Running jobs still use the
host's configured capacity, and different hosts start independently.

Waiting starts do not hold lifecycle slots, so stop and removal requests can
still run. A queued create superseded by a stop or removal is skipped before
touching the backend. Shutdown cancels waiting tasks, and a cordon is checked
again when a create reaches the front.

Runtime transport failures hold subsequent starts for five seconds, doubling
to at most a minute. A successful startup clears the hold. This is an
agent-local cooldown, not a Docker restart; existing jobs continue. A create
or pull that merely outran its own budget does not open the hold: only a
daemon that stopped answering does.

An uncertain inventory lookup never authorises replacement. A create asks the
host three more times over about seventeen seconds, and if it still cannot
tell whether the runner exists, what happens next depends on which delivery
this is. The controller stamps an `attempt` on every task: on the first
delivery no workload of that runner's can exist, so the create fails out loud
and the scheduler replaces it within seconds. On a redelivery, or from a
controller too old to stamp one, the task stays unacknowledged for the
bounded redelivery path, because the workload it would replace by name may be
mid-job behind a daemon that is only temporarily silent. The controller's
provision timeout still applies and may expire before that redelivery.

The backend's create timeout starts after admission. The controller's
`scheduler.provision_timeout` still covers the whole provisioning period,
including queue time. Hosts with long cold pulls should prewarm images and
allow enough provisioning time for their expected burst.

Prewarming a DinD pool prepares both its runner image and the agent's
configured sidecar image. Equivalent successful background preparations are
reused for one minute across pools; runner creates still apply their own pull
policy. A failed dependency preparation is never cached.

The stock runner image waits for the pool's Docker daemon before registering
when the pool provides one. `ZOOMIES_DOCKER_WAIT` accepts 1–3600 seconds and
defaults to 120 in the image; the fleet default is 180 seconds and sets it with `runners.docker_wait` on the Settings
page, which the controller renders into every Docker pool's runners, and a
pool's `env` can name it to say otherwise. `runners.env` reaches every runner
the same way, under the pool's own `env` — dockerd in a fresh
sidecar sets up its storage driver and firewall rules before it listens, and
on a host that is extracting images for the runners queued behind it that
takes longer than the thirty seconds the first version allowed. Each probe is
bounded to at most five seconds. A daemon that never answers exits the runner
with code 69 before its listener can accept a job, and an unusable setting
exits it with 78; the agent turns both into a sentence on the Runners page
that points at the container's log. An image with no Docker client is a
warning in that log, not an exit: the controller swaps the stock image for its
Docker variant under every tag this build publishes, and refuses a reference it
cannot know the variant exists for — a digest, or a pin from another build —
because moving a pool onto a tag the registry lacks would stop every job on it,
including the ones that never touch Docker. A pool left on one of those raises
`pool.docker_client_missing`, so the log is not the only place it is said.
Existing images acquire this behaviour only when rebuilt and deployed.

New runners retain their normal CPU quota for `agent.bootstrap_cpu_grace`
(default 2m, 0s disables it, at most 10m) before pressure throttling applies.
Memory limits remain unchanged. The same age-based grace applies to adopted
workloads using their original start time. A DinD pair created from a host
share records each container's half as its throttle base, matching adoption;
restoring its quota must never turn one reserved slot into two.

Resource sampling runs independently of lifecycle reconciliation, with at most
four concurrent requests, five seconds per sample and twenty seconds per pass;
a non-streaming Docker stats call takes over a second by design, so a tighter
budget made every sample on a loaded host fail together. Failed samples
retain the previous value; the current wire format does not yet expose sample
age, so these values must not be read as proof of freshness.

### When webhooks cannot reach you

If `github.poll_fallback` is on (the default), a poller lists queued jobs on an
interval and feeds the same code path. It records jobs it first finds already
running on one of the fleet's own runners too, because an idle runner takes a
job within seconds — far inside the interval — and without a webhook nothing
else would tell the controller that runner had gone busy. The UI's problems
drawer says plainly when the controller is running on polling alone, because a
fleet that silently stopped receiving webhooks looks exactly like a quiet
fleet.

Both of the poller's decisions are made per installation, and that is
load-bearing on a controller serving more than one. It skips an installation
whose own webhooks are arriving — deliveries are credited to the installation
that owns the repository named in them, not to whichever secret verified them —
so one organisation's working deliveries cannot silence the poller for an
organisation whose webhooks reach nobody, which is the exact failure the poller
is the safety net for. And when GitHub rate-limits one installation the poller
stands that installation down and carries on with the rest, because the quota is
per installation and abandoning the sweep would let one organisation out of
quota stop every other one from scaling.

The stand-down lasts as long as GitHub asked: every rate-limited response says
when the quota returns, either as a reset instant or as a retry-after, and the
error carries the number rather than formatting it into a message. A fixed wait
is only the fallback for a response that said nothing — waiting a flat quarter
of an hour for a quota that came back in two minutes wastes the difference on
every sweep, and for one that returns in fifty spends the rest of the window
rediscovering the same refusal. It is capped at an hour, because GitHub's window
is an hour and anything beyond it is a clock out of step rather than an answer.

The hold belongs to the installation rather than to the poller, so the
registration reap honours it too and sets it the same way. The reap is the other
sweep that spends quota on its own schedule, and it stops working down an
installation's list at the first refusal: every remaining call would be refused
identically, and each one is another call against a quota that is already gone.

### One controller per database

Two controllers over one database is not a supported topology and never was,
but nothing used to stop it. SQLite's busy timeout serialises writes; it does
not stop a second scheduler minting runner credentials, reclaiming hosts it
thinks are lost and reaping workloads the first one created. Every symptom of
that looks like a bug somewhere else.

A controller therefore takes two things before it starts. An advisory lock on
a file beside the database catches a second process on the same host, and the
kernel drops it however the process exits, so a controller killed with
`SIGKILL` leaves nothing to clean up. A lease row in the database catches what
no lock on this host can see: a restored copy on a shared filesystem, or a
controller on another machine pointed at the same file. A controller refused by
either says which host and process is holding it, and `--takeover` starts
anyway for the case where the other one is known to be gone.

A controller restarting on the same host reclaims its predecessor's lease at
once rather than waiting the lease out — the service unit restarts always, and
the lock it already holds proves nothing else here has the database. The
cross-host case, which is the one the lease exists for, is refused as normal.

The holder renews its lease on a timer. If a renewal finds somebody else
holding it, the controller raises `controller.lease_lost` and keeps raising it:
two schedulers are now running, and this is not a state a process recovers from
by trying again.

## Components

| Package | Responsibility |
| --- | --- |
| `internal/store` | The only place SQL is written. Domain types, embedded migrations, every query. Enforces the runner state machine. |
| `internal/config` | The settings registry, and the four layers that fill it: defaults, `zoomies.yaml`, the fleet's database, `ZOOMIES_*`. Splits findings into errors that stop startup and warnings that name every dangerous setting. |
| `internal/cryptox` | AES-256-GCM for secrets at rest; argon2id for passwords; SHA-256 for bearer tokens. |
| `internal/backup` | One copy of the database: taking it with `VACUUM INTO`, the manifest that says what it is, listing, verifying, archiving and passphrase-encrypting it, copying it to the S3-compatible destinations `backup.remotes` names or an administrator stored — a hand-rolled client, for the reason the Docker one is, and one merge rule the controller, the API and the CLI all resolve through — and the restore that puts it back. Shared by `zoomies backup`, the controller's scheduled copies and the settings page, so all three write and read one layout. |
| `internal/scheduler` | Pure scaling decisions, label matching and platform fit. No I/O. |
| `internal/naming` | The `zoomies-*` naming grammar for pools and hosts, and the runner image catalogue. No I/O; see [Naming and platforms](naming.md). |
| `internal/machine` | What host this process is running on: distribution, release, and how much machine the cgroup actually allows. |
| `internal/github` | App auth, JIT configs, registration tokens, webhook validation, the fallback poller, and a fake GitHub for tests. |
| `internal/backend` | How a runner becomes a real process: Docker, Podman, bare process. |
| `internal/auth` | Identity, RBAC, sessions, API tokens, join tokens, OIDC, audit. |
| `internal/api` | The REST API, SSE, `/metrics`, and the embedded UI. |
| `internal/provider` | The contract an infrastructure provider implements, a fake that obeys it, and the conformance suite every provider must pass. No SQL, no HTTP, no opinions about the fleet. |
| `internal/provider/proxmox` | The first provider: a hand-rolled Proxmox VE API client, and the machine lifecycle over it. |
| `internal/controller` | Wiring: the reconcile loop, the machine loop, the automatic pool reconciler, webhook ingest, the agent task queue, the log relay. |
| `internal/agent` | The runner-executing half and its transport to the controller. |
| `internal/gateway` | `zoomies gateway`: the provider-side end of a private connection. Runs beside a hypervisor the controller cannot reach and forwards the tunnel to that one API. |
| `internal/installer` | `zoomies init`, `zoomies uninstall`, the GitHub App manifest flow, service installation. |
| `internal/events` | In-process pub/sub that the SSE endpoint fans out. |
| `internal/mcp` | The fleet over the Model Context Protocol: the tools, and both transports — stdio for `zoomies mcp`, Streamable HTTP for the controller's `/mcp`. Every tool is a documented REST route called with the caller's own token, so it adds no authority. |
| `internal/migrate` | Rewriting a workflow's `runs-on` line, and nothing else in the file. |

## The runner state machine

```mermaid
stateDiagram-v2
    [*] --> provisioning: the reconcile loop wrote the row
    provisioning --> registering: the agent started the container
    registering --> idle: online at GitHub, waiting
    registering --> busy: GitHub assigned a job straight away
    idle --> busy: job assigned
    busy --> idle: job finished, and the pool is not ephemeral
    idle --> draining: idle timeout, scale-down, or an operator
    busy --> draining: finish this job, then stop
    provisioning --> draining: cancelled before it started
    registering --> draining: cancelled before it came online
    provisioning --> failed: the agent could not start it
    registering --> failed: it never came online
    draining --> removed: the container is gone
    failed --> removed
    removed --> [*]
```

* **provisioning** -- the row exists, the agent has not started the workload.
* **registering** -- the container is up, the runner has not yet appeared online.
* **idle** -- registered with GitHub, waiting for a job.
* **busy** -- executing a job.
* **draining** -- told to stop taking work and exit. A busy runner in
  `draining` is given five minutes to finish the job it is on; if it takes
  longer the runner is stopped and GitHub marks that job failed. That is what
  lets a drained host actually empty rather than waiting on the longest job
  somebody started, and it is why draining a busy runner asks first.
* **removed** / **failed** -- terminal. A failed runner stays on the Runners
  page for ten minutes before it is removed, because its message is the only
  record of why it failed, and the scheduler reads the failures still on the
  page to decide how long to wait before creating another
  ([how the wait works](hosts-and-pools.md#when-runners-keep-failing-to-start)).

Every live state can also go straight to `failed` or `removed`: a host that
vanishes takes its runners with it, and the drawing leaves those two edges out
of each state to stay readable. Everything else it draws, including the two
ways a runner can be told to stop before it ever comes online — a pool
disabled or deleted underneath it, or an operator draining the host. The
allow-list itself is `validRunnerTransitions` in `internal/store/models.go`.

Transitions are validated in `store.TransitionRunner`, not in the caller. An
agent cannot report a nonsensical state and corrupt the fleet's accounting.

## The machine state machine

A **machine** is a host Zoomies rented rather than one somebody built. Its row
exists before the resource does, and outlives it by exactly as long as it takes
to prove the resource is gone.

```mermaid
stateDiagram-v2
    [*] --> planned: demand asked for a machine
    planned --> creating: identity written down, then built
    creating --> starting: the resource exists
    creating --> bootstrapping: it was already running
    starting --> bootstrapping: it is up
    bootstrapping --> enrolling: the agent has its credential
    enrolling --> ready: it joined, and is a host
    ready --> draining: idle long enough, or an operator
    draining --> ready: demand came back before the delete started
    ready --> deleting: an operator, or its host is gone
    draining --> deleting: its last runner finished
    deleting --> deleted: the provider confirms it is gone
    planned --> failed: the create was never issued
    creating --> failed: it could not be built
    bootstrapping --> failed: the guest never came up
    enrolling --> failed: it never joined
    failed --> deleting: a sweep found a resource after all
    creating --> quarantined: the outcome could not be resolved
    ready --> quarantined: it is not ours after all
    deleting --> quarantined: the delete could not be confirmed
    quarantined --> deleted: an operator released it
    deleted --> [*]
```

* **planned** -- the reservation exists and nothing has been created.
* **creating** -- the identity is written down, and a create may or may not have
  happened. This is the state a lost response leaves behind.
* **starting**, **bootstrapping**, **enrolling** -- the resource exists and is
  being made into a host: powered on, given the agent, then waiting for it to
  join.
* **ready** -- a host row exists and the scheduler places runners on it.
* **draining** -- cordoned, waiting for its runners to finish. Reversible, which
  is why demand returning cancels a drain rather than paying for a new machine.
* **deleting** -- a delete was issued and the resource is not yet confirmed gone.
  A 200 from the delete call is not that confirmation; an inspect that cannot
  find it is.
* **deleted** -- the only state that is finished with.
* **failed** -- the lifecycle was given up on. Not terminal: the resource may
  still exist, and a later sweep can find it.
* **quarantined** -- ownership could not be proved. Nothing automatic ever
  touches one of these again, because the alternative is destroying a machine
  somebody else is using.

Every arrow out of `creating` is an **observation**, never an intent. When the
answer to a create is lost, the reconciler asks who owns the identity it chose
in advance; it never asks for a second machine. A timeout is not evidence that
creation failed.

The allow-list is `validMachineTransitions` in `internal/store/machines.go`, and
`store.TransitionMachine` enforces it — the same discipline as runners, for the
same reason.

### Reconciliation invariants

The rules that keep the diagram true when something goes wrong are spread
over four packages, and each constant was chosen against one in another. This
is the list, with the constant and the package that owns it, so that a change
to one is made knowing the rest. Three tests in `internal/controller`
(`invariants_test.go`) fail if the relationships at the end stop holding.

| Rule | Constant | Owner |
| --- | --- | --- |
| A task is delivered at least once. The controller keeps it in flight under a lease, and a result that has not arrived when the lease expires puts the task back on the host's queue. A log relay is the exception: it is tied to a browser that has since gone, so it is never re-queued. | `createLease` 20 min, `stopLease` 10 min, `removeLease` 5 min | `internal/controller` (`taskQueue.sweep`) |
| A task is offered at most three times, so a host is given up on after three leases: an hour for a create, thirty minutes for a stop, fifteen for a remove. A dropped create is noticed by the runner's provision timeout, which says so on the Runners page; a dropped stop would be noticed by nothing, so the sweep fails that runner itself and the next pass frees its slot. | `maxTaskAttempts` = 3 | `internal/controller` (`sweepTasks`) |
| A host's queue is in memory. A controller restart loses every task in flight; the runner a lost create was for waits out the provision timeout and is replaced with a freshly minted credential. Removal intent from completed jobs and failed host cleanup is recovered from database rows until the host confirms removal. | — | `internal/controller` |
| A host is late, then unhealthy, then lost, in that order. The controller hands the agent its heartbeat interval on every heartbeat, and the validator warns when that interval is more than half the timeout, so one late heartbeat never counts. After ninety seconds without one the host is unhealthy and the scheduler places nothing new on it. After five minutes it is lost: every runner still recorded as live on it is failed, and a job one was running is marked the fleet's failure. Both judgements are made on the thirty-second housekeeping tick. | `agent.heartbeat_interval` 30 s; `config.MaxQuietHeartbeatInterval` 45 s; `store.HeartbeatTimeout` 90 s; `hostLostAfter` 5 min; `housekeepingTick` 30 s | `internal/config` (`Validate`); `internal/store` (`Host.Healthy`); `internal/controller` (`checkHostHealth`, `reclaimLostRunners`) |
| A runner that has not come online in time is failed. The timeout covers both `provisioning` and `registering`; there is no second "registration timeout", and the `runners.not_progressing` problem is raised at half of it. The maximum lifetime retires only a runner that is idle; nothing the scheduler does touches a busy one. | `scheduler.provision_timeout`, 20 min by default; `scheduler.max_runner_lifetime` | `internal/scheduler` (`Decide`); `internal/controller` (`Problems`) |
| A failed runner stays for ten minutes, then goes. It holds no host slot, its message is the only record of why it failed, and the failures still on the page decide how long the pool waits before creating another: ten seconds, doubling to five minutes, and never longer than the retention, because the failures the wait is computed from are gone by then. | `failedRetention` 10 min; `startBackoff` 10 s; `maxStartBackoff` 5 min | `internal/scheduler` |
| A repeated state is legal and a wrong one is dropped. `CanTransition(s, s)` is true, so an agent that reports what the row already says changes nothing, not even the idle clock the scale-down reads; a transition outside the allow-list is ignored with a log line, never forced. `finished_at` is stamped once, on the first entry to a terminal state, and is what the failed retention counts from. | `validRunnerTransitions` | `internal/store` (`TransitionRunner`); `internal/controller` (`applyRunnerState`) |
| Capacity is derived from rows. A host's free capacity is its `capacity` minus the runners recorded on it in a state other than `removed` or `failed`, counted on every read; no counter is kept anywhere, and every scheduling decision starts from rows, the clock and the policy. A dead host's rows therefore count until they are failed, which is what the host-lost reclaim is for. | `Host.ActiveRunners` | `internal/store` (`ListHosts`); `internal/scheduler` (`HostCanRun`) |
| The pools the controller keeps are recomputed from the hosts as they are, never from what changed. `PlanAutoPools` is pure: given the hosts and the pools it says what each pool should be, so a second pass changes nothing, a missed event costs at most a pass, and an event is only a hint to look sooner. The reconciler runs on its own loop -- a timer and a one-slot wake channel -- one pass at a time, and holds neither `reconcileMu` nor anything the scheduling pass takes, because everything it writes is a narrow statement on columns the scheduler only reads. `ApplyAutoPool` names only the columns the controller owns, touches only a pool with an `auto_key`, and applies only while the operator's warm count, cap and pause are what the pass worked from, so an edit saved while a pass was working is never put back. A host's silence is counted from the later of its last heartbeat and the controller's start, above the fleet's own `hostLostAfter`, so neither a brief network drop nor a controller restart reshuffles a pool. | `autoPoolInterval` 1 min; `scheduler.auto_pools_host_grace` 10 min; `scheduler.size_class_hold` 10 min | `internal/scheduler` (`PlanAutoPools`); `internal/controller` (`ReconcileAutoPools`); `internal/store` (`ApplyAutoPool`) |
| So is the resource reservation, from the same rows and the same filter. One runner is charged the pool's `resources` and one slot's worth of the host for each field the pool left unset, so a fleet of pools with no limits admits exactly what its slot counts admitted. A `dind` pool is charged for both of its containers where the figure is one an operator typed, because the daemon its builds run in is given that figure too. Where the figure is a slot's share the pair splits the slot and is charged one, so a slot is one runner whatever it brought with it. Both halves have to move together: charging a defaulted pair once while giving each container a share let a host read as half committed while its runners' quotas covered every core it had, and charging two while giving one is the mirror -- eight slots holding four runners, and an overcommit fix that cost room every time it was taken. There is no reservations table: a second source of truth would be a second writer, and it would disagree with the rows on the first restart. CPU and memory are charged for the live runners already on the host; free disk is not, because it is a measurement that already contains what they have written, and charging it again would count the same bytes twice. | `scheduler.Reserve`; `Host.Allocatable` | `internal/scheduler` (`resources.go`, `newHostSet`) |
| A runner is given what it was charged. A pool's own `resources` become its runners' cgroup limits, and each field the pool left unset becomes one slot's share of the host's allocatable machine — the same share `Reserve` charges — as a limit of its own, so the books and the cgroups say the same thing. The share is given only where it would bind: on a container backend whose daemon has said in its probe that it can apply that field, on a host that has reported its size, and never on the `process` backend. The CPU share is floored to the hundredth rather than rounded, so a host's shares never sum past the machine; the reserve under them has a floor of half a core or a twentieth of the machine, so the daemon keeps answering while every runner is flat out. A `dind` pair charged twice — a figure the pool typed — receives that same figure on each container, so the pair and the books agree; a pair charged one share instead splits it between the two, because a slot is one runner however many containers it takes to run it. What a runner was given, and whether the pool or the host gave it, is written on its row at create and never recomputed, because an OOM kill needs to say which limit it was. | `scheduler.Allocation`, `scheduler.HostShare`; `store.MinHostReserveCPUs` 0.5, `store.MinHostReserveCPUFraction` 0.05; `store.AllocationFromPool`, `store.AllocationFromHost`; `scheduler.default_runner_limits` | `internal/scheduler` (`allocation.go`); `internal/store` (`Host.CPUReserve`, `Host.Allocatable`); `internal/controller` (`createRunner`); `internal/backend` (`stampResourceLabels`) |
| An overwhelmed host is stepped down a ladder, never switched off. The pressure holds refuse new starts while a fresh sample says CPU or memory is acutely short and release on the next sample that does not; the throttle is what outlasts a sample. Each rung takes a quarter of the host's slots and, down to half, a quarter of every runner's CPU quota. It climbs one rung at a time, no sooner than two minutes after the last, while the host stays overwhelmed — a CPU hold with a runner no quota binds, a load average at twice the cores, or memory at the reserve — and comes down one rung after five minutes of unbroken calm. A host whose samples stop is lifted after ten minutes, since a host nobody can measure is placed by its configured capacity. The effective capacity never falls below one, because a host taking nothing is a cordon and a cordon is the operator's. The controller owns every field of the throttle and decides it from what the host measured; a heartbeat carries measurements, never a decision, so a host cannot talk its way off its own rung. The agent applies the CPU factor to every runner it tracks on each heartbeat, idempotently, and never touches a live container's memory. | `scheduler.ThrottleStep` 2 min; `scheduler.ThrottleRecovery` 5 min; `scheduler.StaleThrottleReset` 10 min; `scheduler.LoadPerCPUOverwhelmed` 2.0, `scheduler.LoadPerCPUCalm` 1.0; `store.MaxThrottleLevel` 3; `store.MinCPUFactor` 0.5; `store.CPUHoldWindow` 30 s; `scheduler.host_throttling` | `internal/scheduler` (`NextThrottle`, `ThrottleReason`); `internal/store` (`HostThrottle`, `Host.EffectiveCapacity`, `SetHostThrottle`); `internal/controller` (`settleThrottle`, `ClearHostThrottle`); `internal/agent` (`applyThrottle`); `internal/backend` (`UpdateResources`) |
| An agent asserts no state for a live runner. It says `registering` when its create succeeded and `removed` or `failed` when the workload exited, read from the exit code; in between it reports the phase and leaves the state empty, because whether GitHub has handed the runner a job is not the host's call. The controller reads a running phase on a `registering` row as `idle`, and the webhook is what makes it `busy`. A runner's end is reported once. | — | `internal/agent` (`ReconcileOnce`); `internal/controller` (`applyReports`) |
| A host may only speak for its own runners. A task result or a runner report from a host that does not own the runner is refused. A host that joins again under its name keeps its id, gets a new token, and drops every runner row recorded against the old one. | — | `internal/controller` (`ReportResult`, `applyReports`, `Join`) |
| The agent reaps only what nothing claims, and only after it has asked. A managed workload it has no record of is removed two minutes after it was first seen, and never before the agent has completed one task poll, so a controller that is down can never look like "nobody owns these runners". A runner created moments ago is not declared gone for a minute, because the backend may not list it yet. | `orphanGrace` 2 min; `missingGrace` 1 min | `internal/agent` (`reapOrphan`, `ReconcileOnce`) |
| The agent deletes a finished workload after the controller has acknowledged its end and the finished retention has passed. Completed GitHub jobs also trigger ephemeral runner removal; failed removals are retried until confirmed by the host. | `agent.finished_retention`, 0 s by default | `internal/agent` (`ReconcileOnce`) |
| An agent stopping leaves its runners running. Shutdown starts no new task, waits up to thirty seconds for the ones in flight, sends one last report, and never stops or removes a runner; a create, stop or remove already under way runs to its own timeout on a context shutdown cannot cancel. | `shutdownGrace` 30 s | `internal/agent` (`Run`) |
| A host reports what it is, and the hosts table has room for what an operator will hold back from it (nothing sets that yet; ZF-103's third pull request adds the route, and until then the floors under it are what hold anything back). How much machine it has is the daemon's answer where there is one: a runner started through Docker or Podman is a sibling on the host, outside whatever cgroup holds a containerised agent, so the agent's own view would describe a fleet at a fraction of its size. Where no daemon answers, a runner is a child of the agent and its cgroup share is the honest bound. The agent measures its work directory's filesystem and sends the size and the free space on join and on every heartbeat; the controller records those and never the reserve, which is the operator's the way capacity is. Free space is the one figure that moves on its own, so it is written only when it has moved by more than a twentieth or by 256 MB, whichever is larger -- otherwise a fleet takes one row write per host per beat for a number that is never twice the same. Whether a reading was taken and what it said are separate questions with separate encodings: the size is what says a measurement happened, so a disk with nothing left records a free space of zero, while an agent too old to look, or a platform with no portable way to ask, sends no size and leaves what was known alone. | `diskFreeToleranceFraction` 0.05; `diskFreeToleranceFloorMB` 256 | `internal/agent` (`hostSize`, `workDirSpace`); `internal/backend` (`Probe`); `internal/controller` (`diskFreeMoved`, `Heartbeat`); `internal/store` (`SetHostReserve`) |
| The agent keeps nothing across a restart but its credentials: `agent.json` holds the join token, and the set of runners it manages is in memory. It rebuilds that set before its loops start, by listing every backend and adopting each workload whose labels carry a runner id, and adopts again whenever a task arrives for a workload it does not know. Both passes are load-bearing: without the start-up one the reconciler's first sweep sees an empty set and removes every job running on the host, which on a single-VM install is every controller restart; without the per-task one a create redelivered after a restart would build a second workload for a runner that already has one. | `StateFile` | `internal/agent` (`adoptExisting`, `adopt`) |
| Registrations are compared with rows a minute after start and every ten minutes after that. A registration with the `zoomies-` prefix that GitHub does not report busy and that no live row explains is an orphan, untidy rather than urgent, and one API call per installation is what the comparison costs. | `reapInterval` 10 min | `internal/controller` (`reap`) |

What holds them together, pinned by the four tests:

* **The silence ladder is ordered.** `store.HeartbeatTimeout` is at least three
  default heartbeat intervals, so one slow heartbeat never makes a host
  unhealthy, and `hostLostAfter` is later than `store.HeartbeatTimeout`, so
  runners are never failed on a host the scheduler is still placing new ones
  on.
* **Every lease outlasts the work it covers.** The create lease exceeds the
  time the agent gives itself for a create, the stop lease exceeds the stop
  timeout the controller hands out plus the agent's margin, and the remove
  lease exceeds the agent's remove timeout. A shorter lease would offer the
  task to the host again while the first attempt was still running.
* **A runner is given long enough to start by every half that bounds it.**
  `scheduler.provision_timeout` is the only one of the three bounds on a
  runner's start that gives up, so it is the most patient: longer than
  `agent.CreateTimeout` plus the wait the runner image does for its Docker
  daemon, and no longer than the create lease, so a runner is never failed with
  its create neither redelivered nor abandoned. The `runners.not_progressing`
  threshold at half of it therefore lands after that Docker wait rather than
  during it, so a runner doing exactly what it was configured to do is not
  reported as stuck.
* **The throttle ladder is paced against the measurements it climbs on.**
  `scheduler.ThrottleRecovery` is longer than `store.HostUsageMaxAge`, so a
  single calm sample cannot step a host down; `scheduler.ThrottleStep` is at
  least `store.CPUHoldWindow`, so the ladder never climbs on pressure the hold
  had not yet called sustained; `scheduler.StaleThrottleReset` is later than
  `hostLostAfter`, so a late host is not handed its slots back before it has
  even been given up on; and `store.MinCPUFactor` is at least a half, so a
  throttled job finishes late rather than timing out.

What follows from the constants as they stand, and is not yet what an
operator would want:

* The agent's tracked set does not survive its restart, and adoption happens
  only when a task arrives. On the single-VM install the controller runs the
  agent inside itself, so a controller restart builds a fresh agent whose
  first poll succeeds at once, and the workloads already running on that host
  become orphans two minutes later. No test exercises this yet; the
  [upgrading page](upgrading.md) is hedged accordingly, and the roadmap's
  adoption-on-start change and its drill tier are what settle it.

## Why the agent connects outbound

The controller never dials an agent. Agents long-poll for tasks and POST
results. This means:

* a host behind NAT or a strict firewall needs no inbound rule;
* adding a host is one command with a short-lived join token;
* the blast radius of a compromised agent is bounded -- it can claim tasks for
  itself, not reach into the controller.

The cost is that log streaming has to be inverted. Nothing can ask the agent for
a runner's output, so the request travels as a task and the output comes back as
a POST that the controller relays to whoever is watching:

```mermaid
sequenceDiagram
    participant B as browser
    participant C as controller
    participant A as agent
    participant R as runner container

    B->>C: GET /api/v1/runners/{id}/logs -- SSE
    C->>C: open a stream, remember who is watching
    A->>C: long-poll for tasks
    C-->>A: stream_logs, with the stream ID
    A->>R: read the container's output
    A->>C: POST /api/v1/agent/logs/{stream_id}, chunked
    C-->>B: relayed as SSE events
    B->>C: the last viewer goes away
    C-->>A: cancel_logs
```

For the embedded agent this is all in-process. A second viewer following the
same runner joins the stream that already exists rather than starting a second
one, and the last one to leave is what tells the agent to stop reading. Each
viewer's queue is bounded, and a viewer that falls behind loses bytes rather
than growing it: a backgrounded tab nobody is reading must not make the
controller hold a compiler's output for ever.

## Storage

SQLite via `modernc.org/sqlite`, so there is no cgo and the binary stays static.

Access is split deliberately: **one** writer connection behind a mutex, and a
pooled reader in WAL mode. SQLite permits exactly one writer, and funnelling
writes through a single connection is what keeps `database is locked` -- the
usual reason small SQLite services fall over -- out of the codebase.

```mermaid
flowchart LR
    api["API handlers"] --> st["store.Store"]
    recon["reconcile loop"] --> st
    autopools["automatic pool reconciler"] --> st
    hooks["webhook ingest"] --> st
    agents["agent task queue"] --> st
    st -->|"every write, serialised by a mutex"| w["one writer connection"]
    st -->|"reads, concurrent"| r["pooled readers"]
    w --> db[("zoomies.db -- WAL")]
    r --> db
```

Only `internal/store` imports `database/sql`, so there is one place to look
when a query is slow, and one place a second writer could be added by mistake.

Migrations are embedded and applied on startup, recorded in a
`schema_migrations` ledger.

## Security posture

The default configuration is the safe one: loopback bind, authentication on,
ephemeral runners, no Docker daemon exposed to jobs, no root.

Every deviation from that is named. `config.Validate` returns `Finding`s with a
severity, a title, why it matters and how to fix it; the same list is printed at
startup and rendered in the UI's problems drawer. See [security.md](security.md)
for the threat model and each individual toggle.

## What this is not

* Not a Kubernetes operator. [ARC](https://github.com/actions/actions-runner-controller)
  already exists and is the right answer if you have a cluster.
* Not a cloud provisioner, though it can rent a host. Zoomies creates and
  destroys machines only through a narrow, ownership-verified
  [provider contract](providers.md), only from providers an operator configured,
  and only within limits they set — and it will not delete a resource it cannot
  prove it created. That is a different thing from a provisioner: there is no
  catalogue, no marketplace of plugins, and no attempt to manage infrastructure
  you did not ask it to rent. An external autoscaler driven by the
  [capacity-demand receiver](capacity-demand-receiver.md) remains a first-class
  alternative for fleets that would rather own that half themselves.
* Not VM-per-job isolation. A rented machine hosts the same agent and the same
  runner backend as a machine you built yourself, so it runs several runners at
  once and they are containers, not virtual machines. The `backend.Backend`
  interface — create, inspect, log, remove — is the shape of a runner on a host
  the agent already has, and a backend that put each job in its own VM could be
  added behind it without the agent learning anything new. Renting the host is
  the other contract, on the controller's side, and the two are deliberately
  separate.
* Not multi-tenant across unrelated organisations. One Zoomies is one team's
  fleet.


## Private agent connections

[Private hosts with Tailcat](private-hosts.md) preserve the outbound agent
protocol. The controller accepts an additional userspace listener restricted
to the agent API. `internal/api/tailcat.go` seals and resumes its identity through
the existing settings store; `internal/agent/tailcat.go` dials it using the same
HTTP protocol, without proxies or redirects. No OS listener, SSH service,
subnet route or Docker endpoint is created. The server-observed `connection`
field travels with the normal host view and `host.updated` event.

The Go library brings WireGuard and the userspace network stack into the binary.
This is a deliberate binary-size and toolchain trade-off for installation with
no separate Tailcat binary. It has no browser dependency or shell-budget cost.

## Verified AI Context snapshots

AI Context verification runs outside the fleet reconciliation lock. A separate
bounded loop checks explicitly submitted setup proposals; administrators can
also recheck existing output, or ask GitHub to run the managed workflow when it
has not caught up (a request that admits nothing: the result still arrives
through verification). Before admission, the controller verifies setup
merge, current installation access, repository identity, the trusted default
branch and exact managed operational files. GitHub reads pin immutable source
and output commits. A valid publication must have the complete managed output
layout, bounded UTF-8 source, matching manifest/configuration identity and
original regular Git blob hashes for every included file. Approved exclusions
are enforced again on admission.

Both-mode snapshots live in SQLite with their freshness record, so publication,
retention and availability change in one transaction. Existing backups include
source, and deleting a configured repository cascades to its snapshots. The
256 MiB retained-payload guard excludes SQLite/WAL overhead and backup copies.
Verification failure closes availability and retains the previous valid
snapshot; that snapshot never becomes a fallback for failed live verification.
Source REST/MCP routes enforce explicit caller grants and revalidate credentials
before and after live verification. Encoded response budgets account for JSON
escaping; continuation requests pin the returned source commit. Polling is recovery and status reconciliation, not an
independent source authorisation boundary. Repository-only live retrieval,
Zoomies-only uploads and generated-branch push enqueueing remain follow-on work.
