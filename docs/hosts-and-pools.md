# Hosts and pools

The [quick start](quickstart.md) leaves you with one host and one pool, which is
a whole working fleet. This page is the next step: a second machine, a second
pool, and the rules that decide which runner lands where.

Two things make up a fleet, and they are deliberately separate:

* A **host** is a machine an agent runs on. It contributes capacity, the
  backends it can actually service, and labels describing what it is.
* A **pool** is a named group of interchangeable runners: which labels a
  workflow must ask for, what image and backend to build them from, and how many
  may exist.

Neither owns the other. A pool can be placed on any host that suits it, and a
host can carry runners from every pool at once. That is what lets you add a
machine without touching a pool, and add a pool without touching a machine.

## Adding a host

![The Hosts page: a card per host with its health, slots in use, detected backends and labels, above the join tokens panel](screenshots/hosts-dark.webp#only-dark){ .zoomies-shot }
![The Hosts page: a card per host with its health, slots in use, detected backends and labels, above the join tokens panel](screenshots/hosts-light.webp#only-light){ .zoomies-shot }

**Hosts → Add a host** in the UI does the whole thing on one page: it mints a
join token and prints the command to paste on the new machine, already filled in
with the address your browser reached the controller on and the labels your
pools select hosts by. From a terminal, the same token comes from the CLI:

```sh
zoomies hosts join-token create --ttl 1h --capacity 8 --labels arch=arm64
```

It prints the command to run on the new host, the token, and when it expires.
The token is shown once — only its hash is stored — and it may be redeemed once.

On the new machine, either install and join in one line:

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- \
  --mode agent \
  --controller https://zoomies.example.com \
  --join-token zoojoin_...
```

or, if the binary is already there, join with the shorter form:

```sh
zoomies agent join https://zoomies.example.com --token zoojoin_... \
  --capacity 8 --labels gpu=true,zone=eu-west
```

`join` redeems the token, writes the credentials, and installs the service that
keeps the host enrolled; `--no-service` joins without one. A private controller
takes `--ca-file` — prefer it over `--insecure`, which trusts anything on the
path.

The agent connects outbound only, both to long-poll for tasks and to post
results back, so a host behind NAT or a strict firewall needs no inbound rule.
See [Architecture](architecture.md#why-the-agent-connects-outbound) for why the
connection runs that way round.

### What a host brings with it

| What | Where it comes from | Why it matters |
| --- | --- | --- |
| Capacity | `agent.capacity`, `--capacity`, or half the CPU count | A hard ceiling the scheduler respects, per host. |
| Backends | Probed by the agent at startup, and again as sockets appear | A pool is only placed on a host that offers its backend. |
| Labels | `agent.labels` or `--labels` | What a pool's `host_selector` matches against. |
| OS, arch, version | The agent | Shown in **Hosts**, and how you tell an arm64 box from an x64 one. |
| Health | A heartbeat every `agent.heartbeat_interval` | A host silent for 90 seconds — three times the default interval — is unhealthy, and takes no new runners until it checks in again. |

A host that offers no backend is connected, healthy and useless: nothing will
ever be scheduled on it. `zoomies hosts list` says so rather than printing a
dash, and repeats the agent's own explanation for each backend it could not
use — usually a Docker socket that is not readable by the account the agent runs
as. [Configuration](configuration.md#agentdocker_host) covers that diagnosis.

The controller itself counts as a host. On a single-VM install the controller
runs an agent inside its own process, which is why the fleet works before you
have added anything.

## Living with more than one host

```sh
zoomies hosts list                  # health, free capacity, backends, platform
zoomies hosts cordon host_k3f9qz2m  # keep its runners, accept no new ones
zoomies hosts uncordon host_k3f9qz2m
zoomies hosts delete host_k3f9qz2m  # refused while runners are still on it
```

**Cordon before maintenance.** A cordoned host keeps everything it is already
running — no job is ever interrupted by a cordon — and accepts nothing new. Once
its runners have finished, reboot the machine, upgrade Docker, do whatever you
came to do, then uncordon it. The scheduler says `cordoned` in its reasons while
that is true, so the pause is visible rather than mysterious.

**Delete only a host that is gone for good.** `delete` is refused while live
runners remain; `--force` deletes anyway and leaves their GitHub registrations
orphaned, which is the right trade only when the machine itself has gone away.

Capacity and labels are edited in the UI under **Hosts**, or with a `PATCH` to
`/api/v1/hosts/{id}` — see [the API surface](api-surface.md#hosts-and-agents).
Relabelling a host changes which pools can select it on the very next scheduler
pass; the runners already on it stay where they are.

## Adding a pool

![The Pools page: each pool's labels, target, backend, busy-against-live bar, queue depth, idle timeout, lifetime and Docker mode](screenshots/pools-dark.webp#only-dark){ .zoomies-shot }
![The Pools page: each pool's labels, target, backend, busy-against-live bar, queue depth, idle timeout, lifetime and Docker mode](screenshots/pools-light.webp#only-light){ .zoomies-shot }

One pool is enough until the fleet has to answer two different questions. Add a
second when a job needs something the first cannot give it:

* **A different machine.** GPU boxes, arm64 builders, a host in another region.
  Label the hosts, and give the pool a `host_selector` that requires the label.
* **A different runtime.** A pool whose jobs build images needs `docker_mode`
  and a runner image carrying a Docker client; most pools want neither.
* **A different ceiling.** A noisy repository is easier to bound with its own
  pool and its own `max_runners` than with a shared one.
* **A different priority.** When the fleet is full, higher-priority pools
  receive create slots first.

The UI's wizard is the easiest way in, and the CLI takes the same fields:

```sh
zoomies pools create \
  --name zoomies-gpu \
  --labels zoomies-gpu \
  --installation inst_k3f9qz2m \
  --host-selector gpu=true \
  --max 4 \
  --dry-run
```

`--dry-run` validates exactly as the wizard's review step does — field errors and
the dangerous-setting warnings the pool would produce — without creating
anything. Drop the flag to create it for real. Every field is in
[Pool settings](configuration.md#pool-settings); the labels to choose are in
[The labels to give a pool](configuration.md#the-labels-to-give-a-pool).

A pool whose image is large is worth prewarming after you create it or point it
at a new image, so the first job of the day does not pay for the pull:

```sh
zoomies pools prewarm pool_k3f9qz2m
```

That pulls the pool's image on every host the pool could be placed on, and
reports what each one did.

**Disable rather than delete** a pool you may want back: a disabled pool drains
to zero and creates nothing, while its settings and history survive. Deleting
drains its runners first unless you pass `--force`.

## How a runner is placed

Every scheduler pass takes a snapshot — pools, runners, queued jobs, hosts — and
decides where new runners go. A host is eligible for a pool when all five of
these hold:

```mermaid
flowchart TD
    p["a pool wants<br/>one more runner"] --> h{"for each host"}
    h --> f{"free capacity<br/>left?"}
    f -->|no| no["not eligible"]
    f -->|yes| hl{"heartbeat<br/>recent?"}
    hl -->|no| no
    hl -->|yes| c{"uncordoned?"}
    c -->|no| no
    c -->|yes| b{"offers the pool's<br/>backend?"}
    b -->|no| no
    b -->|yes| s{"labels match the<br/>host selector?"}
    s -->|no| no
    s -->|yes| ok["eligible"]
    ok --> pick["the eligible host with<br/>the most room wins"]
```

Among eligible hosts the one with the most room left wins, so runners spread
across the fleet rather than piling onto whichever host answered first — one
busy machine should not become the fleet's single point of failure. Ties break
on host ID, so the same snapshot always produces the same plan.

Two ceilings apply at once, and both are hard: a pool never exceeds its
`max_runners`, and a host never exceeds its capacity. A pool's `max_runners`
is therefore only as real as the capacity available on the hosts it can select;
setting it to 20 across two hosts of capacity 4 buys nothing.

### When nothing can be placed

The scheduler says why, in one sentence, and the same reason appears in
**Scaling events**, the problems drawer and the CLI:

```
no host can take a new docker runner (1 cordoned, 2 at capacity)
```

Read the counts, because they name the fix:

| What it says | What to do |
| --- | --- |
| `at capacity` | Nothing is wrong. Wait for a job to finish, raise a host's capacity, or add a host. |
| `cordoned` | Uncordon the host, if the maintenance is over. |
| `unhealthy` | The agent is not heartbeating. Check that it is running on that machine and can reach this controller. |
| `without the docker backend` | Fix the socket on that host, or point the pool at a backend your hosts already offer. When every other host is out for that reason, the sentence carries the agent's own words about the socket, and names the backends it could move to. |
| `not matching the pool's host selector` | Relax the selector, or label a host to match. |

The distinction the reasons keep is between a fleet that is merely **full**,
which clears itself, and one that is **misconfigured**, which never will.
[The quick start's troubleshooting section](quickstart.md#a-job-that-sits-in-the-queue)
walks the same tree from a queued job's point of view.

## Worked shapes

**A GPU box beside the general fleet.** Label the machine on the way in, then
require the label:

```sh
zoomies agent join https://zoomies.example.com --token zoojoin_... --labels gpu=true
zoomies pools create --name zoomies-gpu --labels zoomies-gpu \
  --installation inst_k3f9qz2m --host-selector gpu=true --max 2
```

Workflows reach it with `runs-on: zoomies-gpu`. Nothing else lands there,
because every other pool's selector is empty and matches any host — including
this one, which is usually not what you want, so give the general pools a
selector too (`class=general`) once a specialised host exists.

**An arm64 builder.** Same shape with `arch=arm64`, and a pool whose image is an
arm64 runner image. The label on the host is what makes the placement correct;
`runs-on` naming the pool is what makes the workflow readable.

**Separating a noisy repository.** Give it a pool with its own labels and its own
`max_runners`. Note the limit of `repository_scale_up_limit` on a shared pool: it
throttles creation attributed to one repository, but GitHub may still hand a
queued job to any compatible idle runner. Strict isolation means a pool of its
own, with `runs-on` labels no other repository uses.

**Draining a machine for good.** Cordon it, wait for **Hosts** to show no
runners on it, then delete it and remove the agent's service. The controller
never dials an agent, so nothing has to be told to stop first.
