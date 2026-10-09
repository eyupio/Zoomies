---
icon: material/console
title: "Command line: drive a runner fleet from a terminal"
description: >-
  Every `zoomies` command, what it does and the flags it takes: running the
  controller and agent, driving a fleet from a terminal, and setting a host up.
---

# The command line

One binary, chosen by subcommand. `zoomies controller` is the control plane,
`zoomies agent` is a runner host, `zoomies init` sets a machine up, and the rest
drive a fleet over the same REST API the UI uses.

There are **no global flags**. Every command declares its own, so
`zoomies pools list --help` is the complete truth about that command and there
is nothing to learn about the others first. `zoomies help` lists the commands,
`zoomies <group> help` lists a group's, and `--help` on any command prints its
flags and an example.

Exit codes are the usual three (`0` it worked, `1` it ran and failed, `2` it was
invoked wrongly) plus one of the controller's own: `3` when it stopped because
the settings page asked it to restart, which is how a [staged
restore](backup-and-restore.md#from-the-settings-page) is applied.
Non-zero on purpose, so a service manager set to restart on failure starts it
again. [`zoomies why`](#zoomies-why) adds two of its own, `2` for a job this
fleet never saw and `3` for a job it could not class; it is never the
controller process, so its `3` and the controller's cannot meet.

## Talking to a controller

Everything under *Your fleet* below takes the same connection flags:

| Flag | Default | |
| --- | --- | --- |
| `--url` | | The controller's base URL. |
| `--token` | | An API token. `zoomies tokens create` mints one. |
| `--ca-file` | | A CA bundle, for a private certificate authority. |
| `--insecure` | `false` | Skip certificate verification. For a self-signed certificate you have decided to trust. |
| `--timeout` | `30s` | |
| `--output` | `table` | `table`, `json` or `yaml`. On commands that read something; a command that only acts has no output to shape. |

The listing commands add `--limit` (50), `--offset`, `--sort` and `--order`.

The CLI never follows a redirect. Go would turn a `PATCH` or `POST` into a `GET` on a `301`, `302` or `303` and drop its body, so a controller behind a proxy that forces `https` answered `pools edit` with the pool as it was and the command reported a change that was never sent, and the credential would have followed the `Location`. A redirect is an error that says where it pointed; use that address as `--url`.

Where the URL and token come from is, in order: the flags, then `ZOOMIES_URL` and
`ZOOMIES_TOKEN`, then `~/.config/zoomies/cli.yaml` (`url`, `token`, and optionally
`ca_file` and `insecure`; `ZOOMIES_CLI_CONFIG` points at another file). With none
of those, a CLI on the machine that runs the controller falls back to the controller
installed there, read from its `zoomies.yaml` (`/etc/zoomies/zoomies.yaml` as root), and
says on stderr which address it chose. It reads that file and nothing else: the CLI's
commands are pure API clients that open no database, so an address changed afterwards on
the Settings page is not in it. Put `url` in `cli.yaml` to choose, or to follow the
Settings page. It supplies an address only, never a token.

A host that joined a controller as an agent has the same file, and what it names there is
the controller it joined (`agent.controller_url`), so the CLI on that host uses it too,
with no flag. A host that joined over a private connection is the exception: its agent
reaches the controller through a tunnel of its own, there is no address a command can
dial, and the CLI says so and asks for `--url`. When a request cannot reach the controller,
the error names where its address came from (`--url`, `ZOOMIES_URL` or a file) because
an address nobody typed is the one that cannot be fixed without knowing.

On the machine that runs the controller you do not have to write that file.
`zoomies init` creates it, with the controller's address and no token, and
`zoomies upgrade` offers to when it is missing; it is listed with the other
additions an upgrade asks approval for, and `--yes` accepts it. The file is private
(`0600`), an address you already put there is never replaced, and a token is never
written for you: create one in the UI under your account's API tokens, or with
`zoomies tokens create --name this-host --role operator` once you have one, and add it
as `token:`. It is the file of whichever account ran the command, so under `sudo` it is
`root`'s.

## Running Zoomies

| Command | What it does |
| --- | --- |
| `zoomies controller [--config path] [--takeover]` | Run the control plane: the scheduler, the API, the web UI and the webhook endpoint. On a single VM it runs an agent inside itself. It refuses to start when another controller holds the database, naming which machine and process has it; `--takeover` starts anyway, for the case where you know the other one is gone and cannot be asked. |
| `zoomies agent [--config path]` | Run this host's agent: long-poll a controller for work, start and stop runners, report what happens. Also takes `--controller` and `--join-token` for a host configured entirely from flags, and `--log-file` to append the log to a file instead of stderr, which is how the Windows service keeps one. |
| `zoomies agent join <controller-url> --token <join-token> [--update-helper]` | Enrol this host: redeem the token, write the credentials, install the service. On a systemd host it then asks, as a question of its own that defaults to no, whether to add the [update helper](security.md#what-the-agent-owns-on-a-host). The web UI cannot update a host yet; a later release adds that, and installing the helper now only makes this host ready for it. `--yes` only replaces credentials this host already has and never answers it; `--update-helper` does, and a join with no terminal skips it and prints `sudo zoomies updates helper install`. |
| `zoomies agent install` | Install the agent's service on a machine about to become a provider's template, joined to nothing. The unit reads `/etc/zoomies/zoomies.env`, which the controller writes into each clone, and is left disabled for the controller to enable there. It refuses a machine that has already joined, because its `agent.json` would be cloned into every machine. See [Proxmox VE](proxmox.md#preparing-the-template). |
| `zoomies gateway --target <host:port>` | Publish a private provider API (a Proxmox cluster on your home network, say) to a controller over Tailcat. It prints the address the provider form asks for, keeps its identity in `--state-dir` so a restart keeps the same address, and forwards every connection that arrives through the tunnel to the one target. `--quiet` keeps the address out of a service's log. See [Private hosts and providers](private-hosts.md#private-providers). |

`agent join` takes the host's shape as flags, `--name` (at most 128 characters,
with no backtick or control character; it is refused before the token is spent
otherwise, see [Naming](naming.md)), `--capacity`,
`--labels`, `--backend`, `--docker-host`, plus the TLS trio (`--ca-file`,
`--client-cert`, `--client-key`), `--no-service` to skip installing one, and
`--non-interactive` with `--yes` for automation, and `--update-helper` to add the
[update helper](security.md#what-the-agent-owns-on-a-host) without being asked.

## Your fleet

### `zoomies status`

The Overview in a terminal: counts, pools, recent scaling and anything wrong.
`--window` (`1h`) sets the period the rates cover, `--scaling` (`5`) how many
recent decisions to print.

### `zoomies problems`

What the controller thinks is wrong, and (for the problems it has worked out a change
for) the change.

```sh
zoomies problems list                        # everything wrong now
zoomies problems list --proposals            # only what carries a proposed change, and its cost
zoomies problems apply host.slots_below_capacity --dry-run
zoomies problems apply pool.daemon_share_suggested --target pool_k3f9qz2m
```

A proposed change is a *remedy*: the pool's or host's own update, priced against the
fleet before it was proposed, with what it keeps and gains in the `cost` column. `apply`
names the problem and never the change, so only something the controller is proposing
*now* is made. It runs as you, through the same update as `pools edit` and `hosts edit`,
so it needs the `operator` role, it is refused when it would leave a pool with nowhere to
run, and it is on the audit trail. `--dry-run` says what it would change and changes
nothing; `--target` picks one when several pools or hosts have a proposal for the same
problem. The web UI's button on the problem and the MCP's `apply_remedy` do the same thing.

### `zoomies pools`

What runners to make, and how many.

| Command | What it does |
| --- | --- |
| `pools list` | Every pool, with its live counts and utilisation. |
| `pools get <pool-id>` | One pool in full, including any dangerous settings. |
| `pools create` | Create a pool. The server validates exactly as the UI's wizard does, so `--dry-run` gives you that verdict without creating anything. |
| `pools edit <pool-id>` | Change the settings you name. Anything you do not name is left alone. An edit that would leave the pool with nowhere to run is refused unless `--confirm`. |
| `pools delete <pool-id>` | Delete it. Its runners drain first unless `--force`. |
| `pools enable` / `pools disable` | Let a pool create runners, or stop it. Disabling interrupts nothing: existing runners drain as they go idle. |
| `pools prewarm <pool-id>` | Pre-pull the pool's image on every matching host. |
| `pools export [--file pools.yaml] [--format yaml\|json]` | Write every pool to a file another instance can import. Installations are named by what they cover, and no environment value is in it. See [moving pools](backup-and-restore.md#moving-pools). |
| `pools import <file> [--dry-run] [--skip <pool>,...]` | Create and change pools to match an export, pool by pool and setting by setting. Nothing is written while any pool is refused; `-` reads stdin. |

`create` and `edit` share one set of flags. The ones worth knowing:
`--name`, `--installation`, `--labels`, `--backend` (`docker`), `--image`,
`--min` (`0`), `--max` (`4`), `--idle-timeout` (`5m`), `--ephemeral` (`true`),
`--docker-mode` (`none`), `--run-as-root` (`false`), `--host-selector`, the
resource limits `--cpus`, `--memory-mb`, `--disk-gb`, `--size-from-host` to take
each runner's size from the host it lands on instead (a pool does that or states
a size, so it cannot be combined with `--cpus` or `--memory-mb`; moving an
existing pool to it clears the stated figures in the same request, and
`--size-from-host=false` hands it back to a slot's share of each host, see
[runner profiles](hosts-and-pools.md#runner-profiles-how-big-a-runner-is-on-one-host)),
and the [elastic CPU](elastic-cpu.md) policy, `--cpu-burst` (`off`, `observe` or `automatic`)
with `--cpu-burst-max` as its ceiling in cores; on a create the ceiling needs
the mode beside it. Both are read live, so an edit reaches runners already
running. `--cpu-burst-size-builds` (`true`) starts an `automatic` pool's runners
with Cargo, .NET and the JVM sized for that ceiling; it applies from the next
runner started.

The [elastic memory](elastic-memory.md) policy has the same shape:
`--memory-burst` (`off`, `observe` or `automatic`), `--memory-burst-max` for the
most memory one runner may hold, its own share included, and `--memory-burst-spill`
for the swap each container may use as the last resort, both in MiB; on a create
they need the mode beside it, and on an edit that names one of them the others
carry forward. A new Docker or Podman pool observes. It is the one policy a pool
the controller keeps takes from you as well.

`--tmpfs-work` keeps the runner's `_work` folder in memory instead of on the
host's disk, and `--tmpfs-tmp` does the same for `/tmp`; `--tmpfs-work-size` and
`--tmpfs-tmp-size` set each folder's ceiling in MiB, and `0` fits it to the
memory limit. Both are off unless typed, the folders are charged to the runner's
memory limit, and a size on a create needs the folder it is for. On an edit,
naming one carries the others forward as they stand. `--tmpfs-docker` and
`--tmpfs-docker-size` do the same for a Docker-in-Docker pool's image store, which
needs `--docker-mode dind`. See [keeping the work folder
in memory](hosts-and-pools.md#keeping-the-work-folder-in-memory).

`--tmpfs-auto` lets each runner decide whether the folders that are on are in memory (where it has room for one to be useful) or on disk, and is the recommended setting; `--tmpfs-auto=false` makes them always in memory.

`--daemon-share <percent>` sets how much of a host-sized slot a Docker-in-Docker
pool's daemon is given, 10 to 90, for CPU and memory alike, with the runner keeping
the rest; `0` is the even split. `--daemon-cpu-share` and `--daemon-memory-share` say
it for one resource and override `--daemon-share` for it: a build is CPU in the
daemon, so `--daemon-cpu-share 70 --daemon-memory-share 50` gives it the CPU while
the runner keeps the memory. Naming one carries the other forward as it stands.

`--min-cpus` and `--min-memory-mb` set the pool's smallest runner, per container: the
least a runner is given on a host whose share is smaller, so the host holds fewer of
them rather than none. A Docker-in-Docker pair needs it twice over, scaled by the
daemon's share, so a thin share multiplies it (a 1-core minimum and a 35% CPU share
charge every runner at least 2.9 CPUs). `0` follows the fleet's `runners.minimum_cpus` and
`runners.minimum_memory_mb`. Editing any size flag carries the pool's minimum forward;
it is never cleared by an edit that does not name it.

On `edit`, only the flags you actually type are sent; the defaults above are
not applied to a partial update, so editing a pool's image cannot silently reset
its ceiling.

A pool the [controller keeps for each size of host](auto-pools.md) is marked in
`pools get`, which says which hosts count towards it and what you have asked of
it, and `pools list` says whether it is out of use because you paused it or
because no host of its kind counts. On such a pool `edit` takes `--warm` (runners
to keep ready) and `--cap` (the most runners to allow, `0` for no cap) and
`--idle-timeout`; `pools disable` and `pools enable` are its pause, and `pools
enable` says what the pool is afterwards: resuming a pool whose hosts are gone
leaves it out of use, and the message says so. Any other flag is refused with the
reason, because the pool's minimum, maximum, labels and size are worked out from its
hosts and not typed, and the rest are set up one way for every such pool.

### `zoomies runners`

The runners that exist right now.

| Command | What it does |
| --- | --- |
| `runners list` | Terminal runners are hidden unless you ask with `--include-removed`. Filter with `--pool`, `--host`, `--state` (repeatable) and `--q`. |
| `runners get <runner-id>` | One runner, its current job and how it got here. |
| `runners drain <runner-id>...` | Stop taking new work and exit. A job still running is given five minutes to finish; if it takes longer the runner is stopped and GitHub marks that job failed, so draining a busy runner needs `--yes`. |
| `runners delete <runner-id>...` | Remove and deregister from GitHub. Drains first unless `--force`, so removing a busy runner needs `--yes`. |
| `runners logs <runner-id>` | Print the output. `--follow` keeps printing it, `--tail` (`1000`) sets how much history. |

### `zoomies jobs`

`jobs list` is job history newest first, filtered by `--repo`, `--workflow`,
`--pool`, `--state`, `--conclusion`, `--q`, and a window with `--since` and
`--until`; each of which takes a duration (`24h`) or a date. `--unmatched`
and `--failed` are the two questions worth a switch of their own.
`jobs get <job-id>` shows one.

`--ours` and `--theirs` split `--failed` by whose failure it was. GitHub records
both halves as `failure`, so this is the only place the question can be asked,
and only one half is anybody here's to fix. `--fault` narrows the fleet's half
to a category (`out_of_memory`, `image`, `registration`, `backend` and the rest)
and a category this build does not know is refused rather than quietly
matching everything.

```sh
zoomies jobs list --ours --since 24h       # what this deployment broke today
zoomies jobs list --fault out_of_memory    # and how much of it was memory
```

`--until` is not included in the window and `--since` is, so two windows cut at
one instant share no job. `--job-name` matches a job's name exactly, where `-q`
matches a substring, and keeps a comma in a matrix job's name such as
`test (ubuntu-latest, 3.12)`. `--controller-version` and `--host-id` narrow to
the jobs stamped with them, `--controller-version unknown` finds the ones that
were never stamped, and `--hosted=true` or `--hosted=false` keeps only jobs on
somebody else's runners, or only the rest.

To read further back than one page, pass the cursor a full page prints (or the
`next` field of `--output json`) back as `--before`. A cursor continues after
the last job of the page before it, so a job queued while you are reading
cannot repeat or hide a row, which `--offset` cannot promise. `--output json`
returns every job with its steps; `--include-steps=false` returns one short
summary per job, small enough to read a hundred of.

### `zoomies why`

`why <job>` is the one question "why did this job fail, stall or run slow",
answered from the controller's explanation: the sentence, then the **class**
from a closed set (`oom`, `timeout`, `cancelled`, `queued-unmatched`,
`queued-blocked`, `queued-capacity`, `queued`, `runner-startup-failure`,
`host-lost`, `disk`, `workflow-failure`, `held-by-github`, `running`,
`succeeded` or `unknown`) with how sure the fleet is and, when it is not sure,
what was missing; the **evidence** the class rests on, each a fact you can
check; the runner's last lines up to the one that decided it; the catalog code
to read on; and the next steps in order. `jobs why` is the same command, found
where jobs live.

`<job>` is a job ID, a GitHub run or job URL (`…/actions/runs/1234` or
`…/runs/1234/job/5678`, found through the jobs list by repository and run), or
`--latest-failed`, the most recently queued of the failed jobs, narrowed by
`--repo owner/name` or `--pool <pool-id>`.
`--logs N` is how many lines to quote (12 by default, at most 40) and
`--no-logs` quotes none. `--output json` is the explanation as the controller
sent it, the same document the job drawer renders.

```sh
zoomies why job_01abc
zoomies why https://github.com/acme/widgets/actions/runs/1234/job/5678
zoomies why --latest-failed --repo acme/widgets
```

Exit codes: `0` diagnosed, `1` another error, `2` no such job, `3` not enough
data (the class is `unknown`; the explanation is still printed, with what was
missing). A script can tell "the fleet broke it" from "the fleet could not say".

### `zoomies kennel`

`kennel` is [Kennel Club](kennel-club.md) in a terminal: how the repositories
this fleet serves measure up against what affects CI and the fleet. Five verbs
are thin readers of the routes the page uses, and one runs on a checkout with
no controller at all.

`overview` is the Overview: how many repositories are in each standing, what is
open by severity, which repositories to open first, and what is turned off.
`repositories` is the list, narrowed by the list's own filters (`--state`,
`--severity`, `--code`, `--q`, `--installation`, `--incomplete`, `--waived`,
`--active`, `--tracked`, `--limit`, `--offset`). `repository <id>` is one
repository: its standing, what could be read, and each finding with what to
change and where it was seen; `--prompts` prints each finding's prompt for a
coding agent, the same text the page's **Copy prompt** button copies. `checks`
is the catalogue, with what is turned off. `recheck <id>` asks for the
repository to be read again when the budget allows, and a second ask inside the
cooldown is told how long to wait. `--output json` on any of them is the
route's document as the controller sent it.

`check [path]` runs the workflow checks over `.github/workflows` of a checkout
on this machine (the checkout, or the workflows directory itself; the current
directory by default), with the same parser and the same evaluator the
controller runs, and **sends nothing anywhere**: there is no controller in the
loop unless `--controller owner/name` names one, and then that repository's
fleet-dependent findings are read from the API and listed beside the local
ones, marked as the controller's. What a laptop cannot know, the fleet, the run
history and the repository's tree as GitHub lists it, is listed as *not checked
here* rather than left silently absent, so "nothing found" means what it says.
A repository is assumed private, the milder reading, unless `--public` is given.
`--code a,b` keeps only those checks, `--severity error|warning|info` is a
floor (`info` by default), and `--prompts` renders a prompt on each finding.
`--output json` is a document a script or a skill reads, held byte for byte by
golden files in the repository's tests. A file is known by Git's blob SHA, so
a file checked here and the same file read from GitHub are known by one name.

```sh
zoomies kennel overview
zoomies kennel repositories --state attention
zoomies kennel repository kcr_k3f9qz2m --prompts
zoomies kennel check
zoomies kennel check --output json --prompts ~/src/widgets
```

Exit codes for `check`: `0` nothing found at the severity asked for, `1`
another error, including a path with no workflows, `4` findings. A script can
tell "something to fix" from "it broke".

### Size classes

With [size routing](auto-pools.md) on or being watched, `jobs get` says how a job
was classed and why, where it was sent, and which class of host took it, and
`jobs advice` lists the workflows whose `runs-on` could say something better,
the costliest first, each with what to write instead and the figures it rests
on: the p95 and the most any run used, of memory and CPU, over a window that
defaults to the fourteen days the class is decided over (`--window 7d`, `2w`
or `36h`; retention bounds it, and the note under the table says when it did),
and whether the fleet has a host of the class at all. A job with too few runs
is a row that says how many it has, not an absence. `--kind too_small`,
`unguaranteed` or `too_large` narrows it, as does `--repo owner/name`.
`zoomies size-pins` puts a job, or a
whole repository, in a class by hand, which reaches the jobs already waiting as
well as the ones that arrive; `zoomies auto-pools` says what the controller keeps
for each size of host and why a host is in none.

| Command | What it does |
| --- | --- |
| `jobs advice [--kind <kind>] [--repo <owner/name>] [--window <span>]` | What to change in the `runs-on` of jobs whose measured runs call for something other than what they ask for, with the p95, the max and the run count behind each, over the window asked for. |
| `size-pins list` | Every pin. |
| `size-pins set <owner/repo> --class <small\|medium\|large> [--workflow <w> --job <j>]` | Pin a repository, or one job in it, to a class. |
| `size-pins delete <owner/repo> [--workflow <w> --job <j>]` | Take a pin away; the jobs it covered go back to the class their runs say. |
| `auto-pools` | The two switches, what each class is, the pools the controller keeps and their hosts, the hosts that count towards none, and what it could not do. |

### Comparing releases

Every job a pool claims is stamped once with the controller version that
claimed it, and once more with the host and agent version that ran it. Jobs
recorded before that, and jobs no pool here claimed, have no stamp and are
reported as `unknown`.

`jobs stats` answers "did builds get faster and more stable" in one call:

```sh
zoomies jobs stats --group-by controller_version --since 30d
zoomies jobs stats --group-by day --job-name build --since 14d
zoomies jobs stats --group-by controller_version --group-by pool --hosted=false
```

`--group-by` takes `controller_version`, `day`, `host`, `pool` or `job_name`,
at most two. Each group shows how many completed jobs there were and how they
ended, how many were lost to the fleet rather than the workflow and the rate,
and p50 and p95 of duration, queue wait and runner startup. Duration leaves out
cancelled and skipped jobs, and a `-` means no job in the group could be
measured. It takes the `jobs list` filters `--repo`, `--workflow`, `--job-name`
and `--hosted`, and a window of at most `limits.job_stats_window` (90 days
unless set) that reaches back no further than `retention.jobs` has kept jobs.
Runner startup needs the runner's own record, which is kept for
`retention.runners`, so it is empty for older jobs.

`jobs rerun <job-id>` asks GitHub to run that run's failed jobs again, which is
the remedy for a job the fleet broke: nothing about the workflow has changed.
GitHub has no job-level re-run, so it re-runs **every** failed job in the run.
It refuses a job that has not finished and one that did not fail, and it does
not ask whose fault the failure was; if you have looked at one and decided to
run it again, that is your call to make.

### `zoomies hosts`

| Command | What it does |
| --- | --- |
| `hosts list` | The hosts that have joined. The `os health` column is the controller's count of each host's latest OS report, the same one the host's page, the problems and the metrics read: `ok`, `2 warnings`, `1 error, 2 warnings`, `reboot pending` (a pending reboot is counted once, as itself, and not as a warning as well), `unavailable` when every counted check was skipped, a trailing `, 2 accepted` when an operator has accepted warnings (they are in no other number), `partial` for a container's view of the host alone, `stale 14m` once the report is more than ten minutes older than the host's last heartbeat, and `-` for a host that is unreachable or has sent no report. Only the checks `zoomies doctor` counts by default are counted, so a suggestion from another tier never shows here; see [Host health](host-health.md). `--output json` and `--output yaml` carry the whole report, with `doctor.summary` beside `doctor.results`. A host with runner sizes of its own gets a line under the table saying what they are, whose they are, and how many slots they give. The size column says which [size class](auto-pools.md) a host is in once the controller works classes out, a line under the table lists the tags you have put on a host, and a host that counts towards no automatic pool says why. |
| `hosts edit <host-id>` | Change a host's capacity (`--capacity`), its reserve (`--reserve-cpus`, `--reserve-memory-mb`, `--reserve-disk-mb`) or how big a runner is on it: `--standard-cpus` and `--standard-memory-mb` for the size one runner is given, `--min-cpus` and `--min-memory-mb` for the least, `--burst-max-cpus` for the most CPU one may use, lent CPU included, and `--burst-max-memory-mb` for the most memory, [lent memory](elastic-memory.md) included. `--tmpfs-work-mb`, `--tmpfs-tmp-mb` and `--tmpfs-docker-mb` set the size each of a pool's [in-memory folders](hosts-and-pools.md#keeping-the-work-folder-in-memory) is asked for on the host, `--tmpfs-max-mb` caps any one of them, and `--tmpfs-off` is the tactical fallback that keeps them all off the host (`--tmpfs-off=false` lets them back). Zero follows the fleet's own setting again, and `--clear-profile` removes the whole profile. `--tag key=value` puts a tag on the host and `--untag key` takes one off, each repeatable, and a bare `--tag gpu` is `gpu=true`; a flag is one tag and is not split on commas, so `--tag rack=b4,b5` is a rack called `b4,b5`. The tags you do not name are kept, and the ones the controller derives from the machine are never written. `--untag` takes a name and not a value, and says so when the host has no such tag of its own, or when it is one the controller works out, rather than succeeding and changing nothing. Anything you do not name is left alone, including the rest of a profile you changed one figure of. An edit that would leave a pool with nowhere to run is refused unless `--confirm`. See [runner profiles](hosts-and-pools.md#runner-profiles-how-big-a-runner-is-on-one-host). |
| `hosts cordon <host-id>` | Stop scheduling new runners onto it. What it already has keeps running. |
| `hosts uncordon <host-id>` | Let it accept runners again. |
| `hosts drain <host-id> [--yes]` | Cordon it, then drain every runner on it, so it empties as its jobs finish. The order matters: draining an uncordoned host means the scheduler puts fresh runners on it while the old ones are still going. Each runner gets five minutes to finish what it is on; a longer job is stopped, which is what makes the host actually empty. A runner that is busy is only drained with `--yes`; without it that runner is refused and the host stays cordoned. |
| `hosts delete <host-id>` | Forget it. Refused while it has live runners, unless `--force`. |
| `hosts join-token create` | Mint a single-use join token: `--ttl` (`15m`), `--capacity` (`2`), `--labels`, `--controller`. Shown once; only its hash is stored. |

### `zoomies updates`

Release updates, and the helper that applies them. `zoomies upgrade` is still
how you upgrade a host by hand, and `zoomies update` is its old name, not part
of this group. `status`, `check` and `apply` talk to a controller like the rest
of the fleet commands; `helper` acts on this host only.

| Command | What it does |
| --- | --- |
| `updates status` | What an update would take: the mode and soak, the build that is running, the release the mode would take and why, whether the [update helper](security.md#what-the-agent-owns-on-a-host) is installed on the controller's host (with the command that installs it when it is not), and the controller's open or last update attempt. It changes nothing. `--output json` prints the controller's body as it is. Needs the viewer role; below `platform` the attempt's error is a fixed sentence for its state, because the real text can name a folder on the controller's host. |
| `updates check` | Read the list of releases from GitHub now and print the sentence the controller makes of it. At most one request a minute goes to GitHub, and asking again inside the minute answers the status as it stands. Refused while `updates.check_interval` is `0`, and with `update.check_failed` when GitHub does not answer. Needs the admin role. |
| `updates apply [--version tag] [--yes]` | Ask the controller to update **itself** through the root helper on its host, which replaces the binary and restarts the service. It is not `zoomies upgrade`: that upgrades the host you run it on and needs no controller, while this sends a request to the controller and returns when it is accepted. Needs the platform role, an update mode other than `off`, and a helper installed on the controller's host. Without `--version` the controller takes the newest release that can be installed on its system and the request carries no body; with it, the body is `{ "tag": "..." }`. It asks before it sends at a terminal; with no terminal it refuses unless `--yes` is given. A refusal (`update.mode_off`, `update.not_a_release`, `update.helper_missing`, `update.in_progress`, `update.nothing_newer`, or `conflict` while the controller is fenced or has lost its lease) exits non-zero and prints the controller's sentence with its code. Follow the attempt with `updates status`. |
| `updates helper install` | Make this host ready for the controller to update it. On the controller's host the platform role can then update the controller from Settings → Updates; the web UI cannot update other hosts yet, so on an agent host installing it now only makes the host ready for a later release. It makes the update folder (`update` under the state directory, or under the shared folder for a container), owned by the account Zoomies runs as, writes root's pointer to it in `/var/lib/zoomies-update` and, on a native install, the copy beside the configuration file that the service reads, then installs and starts `zoomies-update.path` and `zoomies-update.service`. The account, the state directory and the binary are the installed unit's (or the image's, for a container), and the binary is recorded by its real path. Everything `helper run` would refuse, such as a binary in a folder anyone but root can write, a service that runs as root, or a container that does not mount the shared folder, is refused here with the same sentence and nothing is installed. So is a container whose Docker daemon remaps user namespaces or runs rootless: it owns what the container writes under another uid than the image's, and the helper serves only the image's. Podman is not asked, and the helper refuses such requests when they come. Running it again changes nothing; a unit of the same name with other contents is refused rather than replaced. `--config-dir` names the deployment's configuration directory. |
| `updates helper remove` | Turn the helper's trigger off, so that no new update starts, and then, unless an update is running, remove its units, root's state and pointer in `/var/lib/zoomies-update`, the pointer beside the configuration, and `helper.json`, `result.json` and any waiting `request.json` from the update folder. Which folder that is comes from root's pointer, or from the installed unit, never from the pointer beside the configuration, which the service can replace. The folder goes too when that leaves it empty; anything else in it is left and named. While the helper is running an update, nothing is removed and the update is never stopped: wait for it to finish and run `remove` again. `remove` leaves `zoomies.previous`, the binary an upgrade kept for a manual rollback, where it is; only `zoomies uninstall` removes it. `zoomies uninstall` removes the helper the same way, and refuses as a whole, removing nothing, while an update is running. |
| `updates helper run` | Answer the request in the update folder: check it against the helper's limits, run `zoomies upgrade --version <tag> --non-interactive --config-dir <dir>` for it with the deployment's configuration directory (never `--yes`), and write `result.json` saying how it went. The `zoomies-update` unit runs it when a request arrives. It takes no flags: where the folder is, which account owns it, which binary to run and where `upgrade.lock` lives all come from root's copy of the pointer in `/var/lib/zoomies-update`, which only root can write. A release already installed, or a newer one, is answered as done and nothing runs. |
| `updates helper status` | Where the update folder is, whether the helper is installed, and the last result it wrote with the end of its log. When the new controller does not come back, this is where to look. |

`helper` is local and root-only: it acts on this host and never talks to a
controller, `helper install`, `helper remove` and `helper run` refuse to start
as any account but root, and `helper status` reads what it can without root and
says when it needs `sudo`. The controller's update mode never installs the
helper; only the host's owner does.

### `zoomies providers`

| Command | What it does |
| --- | --- |
| `providers list` | Every provider machines can be rented from, how many it has, and whether it may buy more. A provider that is buying nothing says why in a sentence below the table. |
| `providers kinds` | What this build can rent machines from, and the settings each kind asks for; the list `--setting` on the next command chooses from. |
| `providers add <kind>` | The wizard in one line; the UI's review step shows the exact line for what it holds, with a copy button. `--name`, `--endpoint`, `--nodes`, `--template`, `--storage` and `--bridge` are the Proxmox form's questions as flags; `--vmid-range 9000-9099`, `--max-machines`, `--capacity`, `--labels` and the machine's shape follow, and `--setting key=value` reaches anything else. The API token is asked for without echo, read from standard input when that is not a terminal, or taken from `--credential-file`; `--endpoint-ca-file` is the cluster's CA. It validates the answers first (a wrong one is named and nothing is saved) then creates the provider, then runs the live check and prints what the cluster would refuse. The ceiling is zero unless `--max-machines` says otherwise, so a connection test never starts an invoice. |
| `providers edit <name\|id>` | Change the settings you name and nothing else: `--max-machines 8` raises the ceiling, `--credential-file` rotates the token, `--storage ceph` changes one driver setting and keeps the rest. It takes the same flags as `add`. |
| `providers check <name\|id>` | The live preflight: ask the hypervisor what it would refuse, changing nothing there. Exits non-zero if anything would stop a machine being created, and prints what to fix in the same words the setup wizard uses. |
| `providers pause <name\|id>` | Stop buying new machines. `--reason` is kept for the card and the audit row. Machines that exist keep running, and drains, deletes and recovery carry on. Pressing it twice is not an error. |
| `providers resume <name\|id>` | Let it buy machines again. If something else is still holding it (the fence a restore sets, the configuration, a ceiling) the answer says so rather than claiming the fleet is buying. |
| `providers machines` | The machines that exist right now, filtered by `--provider` and `--state`, with `--include-deleted` for the ones whose resource is confirmed gone. A stuck machine prints the provider's own words and the task handle to paste into its console. |
| `providers orphans <name\|id>` | The three ways a row and a real resource can disagree: resources with no row, rows holding no resource, and machines nobody can vouch for. It deletes nothing; every line is for a person to decide. |

There is no `providers machines delete`. Destroying a machine destroys a VM
somebody is paying for, and the row is the only record that it exists, so it is
`DELETE /api/v1/machines/{id}` with a refusal to read first, see
[the API surface](api-surface.md#providers-and-machines).

### `zoomies installations`

`installations list` shows the GitHub App installations pools register with,
never any key material. `installations verify <installation-id>` asks GitHub
whether the credentials and permissions are still what Zoomies needs, which is
the first thing to run when registration starts failing.

### `zoomies export` and `zoomies import`

`export --installation <installation-id>` writes everything about one
installation (its pools, runners, jobs, deliveries, scaling events, runner
sessions and the audit rows that name them) as one archive (`--out`, by
default `zoomies-installation-<id>.json`, mode 0600). With
`--passphrase-file` the App's private key and webhook secret are sealed under
that passphrase, so the archive can be imported on another instance without
this one's key. `import <archive> --passphrase-file FILE` writes it onto this
instance, all of it or none. An archive can be large, so `import` waits up to
thirty minutes for its one request unless you set `--timeout`. See
[moving one installation](backup-and-restore.md#moving-one-installation-or-removing-its-history).

### `zoomies audit`

`audit list` is who did what, newest first, filtered by `--actor`, `--action`,
`--target-kind`, `--target`, `--q`, `--since` and `--until`. `audit tail` prints
the last few (`--limit`, `10`) and then follows the live stream until you
interrupt it.

### `zoomies diagnostics`

Collects a support bundle (this instance, its fleet, its configuration and
everything currently wrong, in one JSON document) and writes it to a file
named after the instant the controller took it. `--file` names the file
yourself, `--stdout` (or `--output json`) sends it to a pipe instead.

The terminal summary is there so you know what you are about to attach: how
many pools, hosts, runners and unfinished jobs went in, which sections the
controller could not gather, and which were shortened. No workflow log is in
it; the bundle names the runners whose logs a support case is likely to want
and the route that fetches each, so you choose what leaves the fleet.

It needs an admin token, because the document contains the settings section.

### `zoomies users` and `zoomies tokens`

| Command | What it does |
| --- | --- |
| `users list` | The accounts that can sign in. |
| `users create` | `--username` and `--role`; omit `--password` for an account that signs in through single sign-on. |
| `users passwd <user-id>` | Set a password. Read from the terminal without echo, or from stdin when piped, never a flag, because a password in a flag is a password in the shell history. |
| `users reset-two-step <user-id>` | Turn off two-step verification for somebody who has lost their authenticator and their recovery codes. Their sessions end and the reset is audited; see [Two-step verification](two-step.md#lost-your-phone). |
| `users delete <user-id>` | Refused if it would leave no enabled administrator. |
| `tokens list` | Metadata only. The value is not stored. Your own tokens; everybody's for an administrator. |
| `tokens create` | `--name`, `--role`, repeatable `--scope`, `--expires-in`. Printed once; only its hash is kept. Anybody signed in may mint their own, never above their own role. |
| `tokens revoke <token-id>` | Immediate. The row stays, marked revoked. |
| `tokens delete <token-id>` | Removes a revoked or expired token from the list for good. A token that still works is refused, revoke it first. The audit log keeps the revocation and the deletion, by prefix. |
| `tokens purge` | Deletes every revoked or expired token you own; `--user <id>` for one account's, `--all` for every one you can see. Tokens that still work are left alone. |

### `zoomies mcp-clients`

The OAuth clients that may ask somebody for an MCP connection. Claude registers
itself when it connects, so most controllers never need one made by hand; see
[Connect Claude to Zoomies](connect-claude.md).

| Command | What it does |
| --- | --- |
| `mcp-clients list` | Every client (made here, self-registered, or a client ID metadata document) and how many connections each holds. |
| `mcp-clients create` | `--name`, repeatable `--redirect-uri` (Claude's callback by default), and `--secret` for a confidential client. Prints the client ID, and the secret once. |
| `mcp-clients rotate-secret <client-id>` | A new secret, printed once; the old one stops working. |
| `mcp-clients revoke <client-id>` | The client can no longer ask, and every connection made with it ends. |

### `zoomies mcp`

Serves the fleet to a coding agent over the
[Model Context Protocol](https://modelcontextprotocol.io), on standard input
and output. The agent's MCP configuration starts it; it is not a command to
run by hand. It is an API client like every other command here: it takes the
same `--url`, `--token` and `ZOOMIES_*` credentials, calls the documented
routes, and has no authority beyond the token's. An agent that can reach the
controller over HTTPS can skip the binary and
[connect to the controller directly](#straight-to-the-controller) instead.

```sh
zoomies tokens create --name claude-code --role viewer --expires-in 720h
claude mcp add zoomies \
  -e ZOOMIES_URL=https://zoomies.example.com -e ZOOMIES_TOKEN=zoo_... \
  -- zoomies mcp
```

In PowerShell, run it as one line and quote the `--`: PowerShell does not take
`\` as a line continuation, and it drops a bare `--` before a script such as
`claude` sees it, which leaves `-e` to swallow `zoomies mcp` and fails with
`missing required argument 'commandOrUrl'`.

`zoomies mcp` runs on the machine the agent runs on, not on the controller, so
that machine needs the `zoomies` binary. The `dev` release carries `mcp` until a
versioned release does. On Windows, fetch it with `curl.exe` (plain `curl` in
Windows PowerShell is `Invoke-WebRequest`) and give its full path:

```powershell
New-Item -ItemType Directory -Force "$HOME\bin" | Out-Null
curl.exe -fL -o "$HOME\bin\zoomies.exe" https://github.com/eyupio/zoomies/releases/download/dev/zoomies_windows_amd64.exe
& "$HOME\bin\zoomies.exe" mcp --help
claude mcp add zoomies -e ZOOMIES_URL=https://zoomies.example.com -e ZOOMIES_TOKEN=zoo_... '--' "$HOME\bin\zoomies.exe" mcp
```

On macOS or Linux, the same file for the platform, `zoomies_darwin_arm64`,
`zoomies_darwin_amd64`, `zoomies_linux_amd64` or `zoomies_linux_arm64`:

```sh
mkdir -p ~/.local/bin
curl -fL -o ~/.local/bin/zoomies https://github.com/eyupio/zoomies/releases/download/dev/zoomies_darwin_arm64
chmod +x ~/.local/bin/zoomies
```

`checksums.txt` on the same release lists each file's SHA-256.

A token made on the **API tokens** settings page works the same way. `claude
mcp get zoomies` should then show `Command: zoomies` and `Args: mcp`; if an
earlier attempt stored something else, `claude mcp remove zoomies` and add it
again.

| Tool | What it answers |
| --- | --- |
| `fleet_status` | Queued and running jobs, outcomes, queue wait and pool utilisation over a window. `GET /stats`. |
| `list_problems` | Everything the controller thinks is wrong, with what to do. `GET /problems`. |
| `list_jobs` | Jobs, with the same filters as `zoomies jobs list`: `failed`, `ours`, `theirs`, `unmatched`, `repo`, `since`, `until`, `job_name`, `hosted`, `controller_version` and `host_id`. Each job is a summary without its steps unless `include_steps` is set, and a full page carries `next` to pass back as `before`. |
| `job_stats` | Completed jobs counted and timed over a window, grouped by up to two of `controller_version`, `day`, `host`, `pool` and `job_name`. `GET /jobs/stats`. |
| `get_job` | One job, its timeline and the controller's explanation, as one document: the class of failure, how sure, the evidence, the catalog code and the next steps. The runner's last lines, and any evidence quoted from them, come in a second block announced as untrusted, the way a runner's log does. |
| `get_runner_log` | The last lines of a runner's output, while the runner still exists. It asks the controller for just the end, holds what it returns to 256 KiB, and says so when it had to shorten it or could only read the start of a very long log. |
| `list_runners`, `list_pools`, `list_hosts`, `host_health` | The fleet's resources as their `GET` routes return them, with a host's tags and size class, a pool's automatic settings where the controller works them out, and the memory valve: a pool's `memory_burst`, a host's `memory_pool`, and a runner's `memory_resource` and `scratch`. A host's `doctor` is its OS report with the controller's own count, `doctor.summary`, which the tool tells the assistant to read before `doctor.results`; the text in the results is written by the host and is untrusted. |
| `label_advice` | What to change in the `runs-on` of workflows whose measured runs call for something other than what they ask for, with what to write instead. `GET /label-advice`. |
| `kennel_overview`, `kennel_repository`, `kennel_findings` | How the repositories this fleet serves measure up against what affects CI and the fleet: `GET /kennel`, `GET /kennel/repositories/{id}`, and `GET /kennel/repositories` narrowed by `code`, `severity`, `state` and `q`, and by the yes-or-no filters `tracked`, `active`, `incomplete` and `waived`, with `limit` (25 unless told) and `offset`. They only read: no tool waives a finding or asks for a repository to be read again, because that is a decision for a person. A finding's evidence, the pools and runs it is about, arrives in a block of its own after a notice that it is untrusted, and the list carries none. A response it cannot take apart is refused, not passed on. |
| `rerun_job` | Only with `--allow-actions`. `POST /jobs/{id}/rerun`; needs `operator`. |
| `drain_runner` | Only with `--allow-actions`. `POST /runners/{id}/drain`, never with `confirm`, so a busy runner is refused rather than having its job stopped; needs `operator`. |
| `update_pool` | Only with `--allow-actions`. `PATCH /pools/{id}` for a pool's smallest runner (`min_cpus`, `min_memory_mb`), its Docker sidecar's shares (`daemon_cpu_share_percent`, `daemon_memory_share_percent`), its scale (`min_runners`, `max_runners`, `idle_timeout`, `repository_scale_up_limit`), where its folders live (`tmpfs_work`, `tmpfs_tmp`, `tmpfs_daemon`: `auto`, `memory` or `disk`) and its burst valves (`cpu_burst_mode`, `cpu_burst_max_cpus`, `memory_burst_mode`, `memory_burst_spill_mb`). Every other setting of the pool is carried forward, because the API replaces `resources`, `cpu_burst`, `memory_burst` and `tmpfs` whole. Disabling a pool, its image, labels and environment are not offered: those stay with a person. The call must carry `expect`, the value it read for each setting it changes (0 or null where it followed the fleet's), like `remedy_id` on `apply_remedy`: the tool writes absolute values, so a setting a person has changed since is refused with what it holds now rather than written over. Never with `confirm`, so a change that would leave the pool with no host that could run it is refused; needs `operator`. |
| `apply_remedy` | Only with `--allow-actions`. `POST /problems/apply`: make the change a problem proposes, as `list_problems` shows it in the problem's `remedy`. You name the problem (`code`, `target_id`) and the proposal you read (`remedy_id`, required here, optional on the REST route for scripts), never the change; the controller applies what it proposes now, as the pool's or host's own update, so it needs that update's role and is refused when it would leave a pool with nowhere to run; needs `operator`, and the pools or hosts write scope too, or it is not offered. |
| `update_host` | Only with `--allow-actions`. `PATCH /hosts/{id}` for a host's capacity, reserve and runner sizes (`standard_cpus`, `standard_memory_mb`, `min_cpus`, `min_memory_mb`, `burst_max_cpus`), with the rest of its runner profile carried forward, and `expect` carrying what it read for each setting it changes, as for `update_pool`. A capacity of zero is refused: stopping a host taking runners is for a person to do by cordoning it; needs `operator`. |
| `edit_host` | Only with `--allow-actions --allow-admin`. `PATCH /hosts/{id}` for a host's `name`, `labels` (replaced whole) and `reserve_disk_mb`, and `POST /hosts/{id}/cordon`. Never with `confirm`. Needs an administrator token; over `/mcp` it also needs `security.mcp_admin_tools`. |
| `clear_host_throttle` | Only with `--allow-actions --allow-admin`. `POST /hosts/{id}/throttle/clear`. Same two conditions as `edit_host`. |
| `get_settings` | Only with `--allow-actions --allow-admin`. `GET /settings`, optionally under a `prefix`; a secret's value is never sent. Same two conditions. |
| `update_settings` | Only with `--allow-actions --allow-admin`. `PATCH /settings` for keys under `scheduler.`, `capacity_demand.`, `retention.`, `runners.`, `limits.`, `log.`, `metrics.`, `images.`, `status.` and `ui.`, and for `updates.check_interval`, only. The update mode and its soak (`updates.mode`, `updates.soak`) are refused with the security, sign-in, GitHub, provider, server, database, agent and backup settings, before anything is sent; a person changes those at the Settings page. Same two conditions. |

#### Straight to the controller

The controller serves the same tools itself at `/mcp`, over MCP's Streamable
HTTP transport, so an agent that can reach it (a cloud session, a hosted
agent, a laptop that would rather not install the binary) needs only the URL
and a token:

```sh
claude mcp add --transport http zoomies https://zoomies.example.com/mcp \
  --header "Authorization: Bearer zoo_..."
```

Or in a project's `.mcp.json`, reading the token from the environment so that
it is never committed:

```json
{
  "mcpServers": {
    "zoomies": {
      "type": "http",
      "url": "https://zoomies.example.com/mcp",
      "headers": { "Authorization": "Bearer ${ZOOMIES_TOKEN}" }
    }
  }
}
```

It takes a bearer token and nothing else, a browser session is refused, and
every tool is the documented route called with that token, so the controller
applies the token's role and scopes to each call exactly as it would to the
CLI's. There is no `--allow-actions` here: `rerun_job`, `drain_runner`, `update_pool`, `update_host` and `apply_remedy` are
offered when the token's role reaches them and not otherwise (the administrator tools `edit_host`, `clear_host_throttle`, `get_settings` and `update_settings` also wait for `security.mcp_admin_tools`, which is off until an administrator turns it on under Settings), so a `viewer`
token is a read-only agent. A client that signs in with OAuth, such as a custom
connector on claude.ai, needs no token at all: see
[Connect Claude to Zoomies](connect-claude.md).

Give it a `viewer` token, narrowed with `--scope` if the agent only needs some
of the fleet. `--allow-actions` offers the two actions to the agent but grants
nothing: a viewer token's re-run is still refused by the controller, and the
agent is told which role was missing.

A workflow's log, and its job, step and branch names, are text anyone who can
open a pull request can write, so they are prompt-injection material for the
agent reading them. The server tells the agent so when it connects, and
returns a log in a block of its own after a notice saying it is untrusted, so
the log's own text cannot pass itself off as Zoomies speaking. That is why the
actions are off by default, and why the token for `/mcp` should be a `viewer`
one unless the agent is meant to act: an agent that reads logs and can also
act is an agent a pull request can try to steer.

### The zoomies skill

`skills/zoomies` in the repository is a skill for a coding agent that drives the
fleet with this command line. It tells the agent where each kind of question
goes (this command for the live fleet, `gh` for GitHub itself), what to check
first (`zoomies status`, and which of four things is wrong when it fails: not
installed, no controller set, not signed in, not reachable), to ask the fleet
why before it reads a log, and which commands it may run.

Those are in three lists, and a test keeps every command in exactly one of
them. **Reads** change nothing and the agent runs them as a question needs.
**Changes** alter the fleet, write a file or handle a secret: the agent says
what the command will do, shows it, and runs it only after you agree to that
command, one yes for one command. **Leave to the user** are the commands that
run on or reshape the host Zoomies is installed on, start and stop the fleet,
or never return; the agent tells you the command and you run it. The skill
carries [the same command reference](https://github.com/eyupio/zoomies/blob/main/skills/zoomies/reference.md)
that `zoomies commands` prints.

To install it, copy the folder into the agent's skills directory:
`.claude/skills/zoomies/` in a project, or `~/.claude/skills/zoomies/` for every
project, for Claude Code. An installer that takes a repository and reads its
`skills/` folder takes `eyupio/zoomies`. The skill does not give an agent any
authority it did not have: the token in `ZOOMIES_TOKEN` still decides what the
controller will do, and a viewer token cannot make a change whatever the agent
is asked.

## Setting up and looking around

| Command | What it does |
| --- | --- |
| `zoomies demo [--port N] [--no-browser]` | Run a throwaway controller on this machine with a fleet already in it (two pools, hosts, runners and a morning's worth of jobs) and open it in your browser. Nobody has to sign in, because it listens on `127.0.0.1` only; GitHub is not involved, and nothing is asked of the outside world, including the daily check for a new release. Everything it writes is under a temporary directory that is deleted when you press Ctrl-C. Port 8080 where that is free and any free port where it is not; `--port` insists on the one you name. Whatever `ZOOMIES_*` variables this shell has are ignored for its duration, so a machine that already runs Zoomies is not disturbed. `install.sh --demo` is the same thing without installing the binary first. |
| `zoomies init [--update-helper]` | Set this host up: how it runs, backend, listener, GitHub App and the first account. `--answers` takes a file and implies `--non-interactive`; `--print-answers` writes one out from an interactive run so the next host can be identical. On a systemd host it ends by asking, as a question of its own that defaults to no, whether to add the [update helper](security.md#what-the-agent-owns-on-a-host). On the controller's host the platform role can then update the controller from Settings → Updates; the web UI cannot update other hosts yet, so on an agent host installing it now only makes the host ready for a later release. `--yes` never answers it, `--update-helper` (or `update_helper: true` in the answer file) does without asking, and an unattended run skips it and prints `sudo zoomies updates helper install`. See [Upgrading](upgrading.md#updating-from-the-web-ui). |
| `zoomies update [--check] [--yes \| --non-interactive] [--update-helper]` | Compatibility alias for `zoomies upgrade`; accepts the same flags. |
| `zoomies upgrade [--check] [--yes \| --non-interactive] [--update-helper] [--version <tag>] [--no-download]` | Download the newest release binary, verify it against the release's `checksums.txt`, and apply it with matching images to an existing native, Compose or Docker deployment. Never downgrades; `--no-download` applies the binary already installed. Keeps configuration and credentials; `--check` changes nothing. What this release expects and the deployment lacks (the shared folder, its mount, a deleted Compose file) is added at a terminal once you agree, with `--yes` without asking, and never with `--non-interactive`. On a systemd host that has no [update helper](security.md#what-the-agent-owns-on-a-host) it also asks, as a question of its own that defaults to no, whether to add one. On the controller's host the platform role can then update the controller from Settings → Updates; the web UI cannot update other hosts yet, so on an agent host installing it now only makes the host ready for a later release. `--yes` never answers it, `--update-helper` does without asking, and `--non-interactive` skips it and prints `sudo zoomies updates helper install`. `install.sh --upgrade` does the same download first and takes `--update-helper` too. See [Upgrading](upgrading.md#what-a-release-adds-to-the-host). |
| `zoomies logs` | Show the latest 100 controller log lines for the recorded Compose or Docker deployment. Alias for `zoomies deployment logs`. |
| `zoomies deployment <action>` | Operate the container deployment recorded by `zoomies init`: `status`, `logs`, `start`, `stop`, `restart`, `update`, or `down`. `update` is a compatibility alias for the complete `zoomies upgrade` flow, including the binary download. `down` keeps the database volume. |
| `zoomies uninstall` | Remove the service or container, the database, the encryption key and the configuration. A container deployment's data volume is kept unless you pass `--volumes` or answer yes, and so is the environment file that holds the key sealing it, the report names it, because a later install can only read the volume with that key. `--volumes` removes both. |
| `zoomies backup [--dir path] [--keep N] [--include-key] [--no-offsite]` | Take a consistent copy of this host's database into a timestamped directory, with a manifest recording the build, the migration ledger, the encryption key's fingerprint, what that key is needed for, and the blanked configuration. Reads the database file directly, so it works when the controller will not start. The copy is then sent to every destination this fleet has (those `backup.remotes` describes and those stored from the Backups page, which this command reads out of the database it has just copied) with `--remote <name>` for one and `--no-offsite` for none. A destination that refuses is reported without failing the backup, which is on the disk either way. See [Backup and restore](backup-and-restore.md). |
| `zoomies restore <backup-directory> [--replace]` | Put a backup's database back at `database.path`, after checking that the copy is sound, that this build can read its schema, and that this host's encryption key is the one that sealed it. Ends every session, removes unredeemed join tokens, and fences the fleet; `--revoke-api-tokens` and `--reset-agent-tokens` go further. `--replace` is required to overwrite an existing database, and moves it aside rather than deleting it. See [Backup and restore](backup-and-restore.md). |
| `zoomies restore --from-remote <name> [<id>\|latest]` | The same restore, on a host that has the configuration file and the encryption key and nothing else (a destination stored in the database is found too when there is a database to read: the copy is fetched from that remote into the backup directory, decrypted with the destination's passphrase) or `--passphrase-file FILE` for one sealed with a passphrase the configuration no longer carries (`--passphrase` still works but is deprecated, because a flag's value stays in the shell history and the process list), verified, and then restored. Naming no backup lists what the remote holds, because an operator in front of an empty machine has no way to know the ids. |
| `zoomies commands [--output json\|markdown]` | Every command and subcommand with the help text the binary prints for it, as Markdown by default or as JSON. `make generate` writes `skills/zoomies/reference.md` from it, and a test keeps that file equal to what the binary says, so an agent reading the reference reads this release's help and not a copy somebody once pasted. |
| `zoomies config check [--config path]` | Validate a file without starting anything. Warnings print and exit 0; errors exit 1. |
| `zoomies config print [--config path]` | The effective configuration (file, environment and defaults combined) with secrets blanked. `--output` is `yaml` or `json` here, and defaults to `yaml`. |
| `zoomies config list [--all]` | What this fleet has stored, and which layer each value came from. `--all` lists every setting, including the ones nobody has changed. |
| `zoomies config get <key>` | One setting's effective value, and whether it came from the defaults, the file, the database or the environment. |
| `zoomies config set <key> <value>` | Store one setting in the fleet's database; the same thing the settings page does, for when the settings page is the thing that is broken. |
| `zoomies config unset <key>` | Forget a stored setting, so the configuration file or the built-in default decides it again. |
| `zoomies config import-env [file]` | Store every setting an environment file sets (standard input when no file is given) in one write, and name the variables that cannot live in the database. The installer runs it in a one-off container to put a controller's settings in its database; see [Settings that were in `.env`](upgrading.md#settings-that-were-in-env). |
| `zoomies doctor [--verbose] [--json] [--tier safe\|aggressive\|dedicated] [--host <id>]` | Read host OS health, summary and recommendations. Exit 0/1/2 for no warnings/warnings/errors among every check in the tier you ran, so `--tier aggressive` exits 1 on an aggressive warning. The host's page, the problems and `zoomies hosts list` count only the safe tier and leave optional checks out, and count a pending reboot as a reboot rather than also as a warning, so the doctor's own warning count is one higher than theirs for that one check. A warning an operator has accepted on the controller (see [Accepting a check as deliberate](host-health.md#accepting-a-check-as-deliberate)) is left out of those counts and of the exit status of `--host`, and `--host` lists it under **Accepted**; a local run knows nothing of acceptances. There is no command to accept or revoke one: that is the host's page and the API. `--interactive` offers explicitly approved local fixes; `--watch --report-file PATH` runs the native read-only reporter. See [Host health and tuning](host-health.md). |
| `zoomies tune [--dry-run] [--yes] [--tier safe\|aggressive] [--dedicated] [--only ids] [--skip ids] [--revert] [--force]` | Review and apply local OS tuning, or restore recorded previous values. Safe and per-item confirmation by default. Dedicated hosts require explicit confirmation. No reboot and no Docker restart while jobs can run. |
| `zoomies healthcheck --url <url>` | Probe a controller's `/healthz`. Exit 0 when it answers. This is what the container image's `HEALTHCHECK` runs. |
| `zoomies version` | The version this binary was built from. `--short` or `--json`. |

`config set`, `config unset` and `config import-env` need the controller stopped: it holds the
database lock, and writing settings under a process that has already read them
would leave the two disagreeing with no way for either to find out. Everything
else in this group reads only.

`init` also accepts eight `--detected-*` flags. They are how `install.sh` passes
on what it already probed, and you will not normally type one.

The deployment commands read `deployment.json`, so they use the exact Compose
command, file, container name and image recorded during installation. Pass
`--config-dir` only for an installation outside the platform default. For a
custom controller image, `deployment update --image <ref>` names the intended
replacement explicitly.

`uninstall`'s `--deregister` and `--volumes` are three-state in practice: typed,
they mean what they say; untouched, they mean *ask*. In `--non-interactive` mode
name the ones you mean.

## Two asymmetries worth knowing

`users create --password` exists and `users passwd --password` deliberately does
not: creating an account is often scripted from a secret store, while changing
one is something a person does at a terminal, where the shell history is the
risk.

`healthcheck` declares its own connection flags rather than sharing the set
above, so its `--timeout` is `5s` rather than `30s` and it has no `--token`. It
is meant to run from a container's health check, where five seconds is already
generous and there is no token to hand.

A container's health check names `http://127.0.0.1:8080` whether or not the
controller serves TLS. When a loopback address answers that it was sent plain
HTTP by an HTTPS server, `healthcheck` asks the same address over `https`
without checking the certificate: the listener is this host's own, and its
certificate may be self-signed. `--ca-file` still verifies against that
certificate when given. An address off the host is never moved to `https`.
