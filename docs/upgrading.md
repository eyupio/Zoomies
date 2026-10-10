---
icon: material/arrow-up-bold-circle-outline
title: Upgrading a Zoomies fleet
description: >-
  What an upgrade actually does, what happens to running jobs, how far a
  controller and its agents may drift apart, and why there is no downgrade.
---

# Upgrading

Use one command on the installed host:

```sh
sudo zoomies upgrade
```

It checks the existing deployment, downloads and verifies the new binary,
refreshes cached stock runner images, updates the controller or agent service,
and checks the result. Native hosts with both controller and agent services
upgrade both when they share the installed binary. An active native host-health
reporter using that binary is restarted too. Use `--mode controller` or
`--mode agent` to select one service explicitly.

For Compose and single-container deployments, the service image is pulled and
compared with the image the running container uses. If they are identical, the
container is kept running without a restart. An image pulled earlier still
replaces a container using an older build. A stopped container is started even
when its image has not changed.

At a colour-capable terminal, a block-letter Zoomies banner lights up from
blue to cyan, catches a bright sweep, then holds briefly before the stages
begin. The intro lasts about three seconds and plays once, including when the command
continues in a downloaded binary. Use `--no-animation` or set
`ZOOMIES_NO_ANIMATION=1` to skip it. `NO_COLOR`, `TERM=dumb`, `--check`,
`--non-interactive` and redirected output also skip the animation. Small terminals
skip it too. True-colour terminals use the brand colours; other terminals use
256- or 16-colour equivalents.

The same banner welcomes initial controller and remote-host installs through
`zoomies init`, including `install.sh --mode agent`, and agent template setup
with `zoomies agent install`. It runs once when the binary takes over setup,
with the caption “Ready. Set. Install.”. `install.sh --no-animation` carries
through to setup, including when setup runs via sudo. Answer-file installs stay
unanimated.

The terminal shows four short stages: **Binary**, **Deployment**, **Verify**
and **Host health**, followed by the upgrade result. Host health is read-only
and never opens a tuning menu. Run `zoomies doctor` afterwards to review
warnings, or `sudo zoomies tune` to review individual fixes. Upgrade approval,
including `--yes`, does not approve OS tuning.

`zoomies update` and `zoomies deployment update` remain compatibility aliases
for the same complete flow. They now include the binary update. Use
`--no-download` when you deliberately want to apply the installed binary.
A failed download stops the upgrade and gives a retry command; it does not
silently continue with an older executable.

The one-line installer remains available for older installations:

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- --upgrade
```

The first controller start applies schema migrations. Upgrade does not run
setup again or require a new join token. A remote host is upgraded locally, with
the command on its card, unless its agent offers to update itself: that is so only
once the update helper is installed on the host (`sudo zoomies updates helper
install`), and then an administrator can update it with the **Update** button on
its card on **Hosts** (or `POST /api/v1/hosts/{id}/update`) when `updates.mode`
is `manual` or `auto`. The
controller only asks: the helper on that host does the work, and the controller does
not replace the binary of any host itself. Without the helper, a controller upgrade does
not remotely replace binaries across the fleet. On the controller's own host the
same helper can replace the controller's binary, with `zoomies updates apply` or the
Update button on Settings → Updates (the platform role). [Updating from the web
UI](#updating-from-the-web-ui) is all of it.

For a **remote agent**, copy the upgrade command from its card on **Hosts**.
That command targets the controller's published version instead of blindly
installing the newest release. A newer agent tells you to upgrade the
controller first. A local or unpublished controller build has no matching
published command. The `dev` channel moves, so it can contain a build newer
than the controller by the time you run the command.

The upgrade keeps configuration, credentials, host identity, data volumes,
ports and runtime options. It refreshes locally cached stock runner images
under their existing tags, including Docker-enabled and full variants; running runners
keep their current images. It does not retag a pool's pinned or custom image.
A custom **service** image needs an explicit `--image <reference>`.

Container upgrades allow up to twenty minutes for the old service to stop.
This is a ceiling, not an added delay: the agent finishes admitted runner
creation and cleanup, reports the result, then exits. Running CI jobs keep
running in their existing containers; the upgrade does not wait for them to
finish. A cold image pull can therefore make a restart take longer than an
idle upgrade. Remote agents keep sending heartbeats during this wait.

The upgrade command supplies this timeout even for older Compose files;
newly generated Compose files also set `stop_grace_period: 20m` for manual
restarts, and a single-container (`docker`) deployment is created with
`--stop-timeout 1200` so a plain `docker stop` or `docker restart` waits as
long. A single-container deployment created by an older binary keeps Docker's
ten-second default until it is recreated with `zoomies init`; until then, stop it
with `docker stop --time 1200 zoomies`. Existing native systemd units are preserved during upgrades. If
yours has a shorter `TimeoutStopSec`, use `systemctl edit zoomies` (or
`zoomies-agent` on an agent host) and set `[Service]` / `TimeoutStopSec=1200s`
before upgrading. New systemd installations use that budget by default.

When first upgrading from an older binary, its shorter internal shutdown
limit still applies to that one stop. To avoid interrupting a creation on
that transition, cordon the controller's host in **Hosts**, wait for its
provisioning runners to finish starting, then upgrade and uncordon it.
Existing running jobs can continue throughout.

For a custom installation, pass `--config-dir <directory>` and, where needed,
`--installed-binary <path>` (`--prefix <binary-directory>` for `install.sh`). Upgrade uses `deployment.json` for container
installs and the existing systemd or launchd service for native ones. It
refuses an unrecognised deployment instead of guessing at a replacement.

After the restart the upgrade waits for the controller to answer before it
reports success. A controller serves nothing until its migrations finish, and
on a large database that can take minutes, so the upgrade says it is waiting
and repeats the controller's latest log line every thirty seconds. A container
is asked through its own health check, run in it straight away; a native
service through `/healthz` on the listener its settings name. The wait lasts up
to thirty minutes. If the controller stops during it, or is still starting at
the end, the upgrade exits with an error that says which and points at its
logs. It does **not** roll back then: the new release may already have migrated
the database, which the old one cannot open. An agent serves nothing to ask, so
an agent's upgrade does not wait.

A failed image pull stops before a restart. If a replacement container cannot
start, the upgrade attempts to restore the previous container or Compose image.
That restores the process, **not a migrated database**; the rollback rules
below still apply. An interrupted upgrade can leave `upgrade.lock` in its
configuration directory: remove it only after checking no upgrade is running.

## What a release adds to the host

A release sometimes needs more of the host than the one you installed: a
folder, a mount, a file the installer wrote and somebody has since removed,
and a Compose file written by an older release, or by hand, can lack a mount
this one would write. Before it pulls or restarts anything, an upgrade works
out what this deployment should have, compares it with what it has, lists what
is missing, and adds it only with your approval.

What the deployment should have depends on what it runs, and that is read from
its **settings, in its database** (the same place the Settings page writes)
layered exactly as the controller layers them at start. The upgrade opens the
database read-only beside the running controller, as a backup does; for a
container deployment it asks the container where its data volume is. So a
controller whose embedded agent you turned off on the Settings page is not
offered a runner host's folder and mounts, and one you turned on is. When the
database cannot be read (Docker Desktop keeps volumes inside its own VM), the
upgrade says so and offers what a host that runs runners needs.

| Missing | Offered to | What the upgrade offers |
| --- | --- | --- |
| The [shared folder](configuration.md#the-shared-folder), or a folder in it | hosts that run runners: an agent, or a controller with its embedded agent on | Create it, owned by the account Zoomies runs as, uid 65532 in the container images, the state directory's owner for a native install. A shared folder owned by someone else is given back to that account. |
| The shared folder's mount | the same, in a container | `/var/lib/zoomies/shared:/var/lib/zoomies/shared` |
| A pool's cache folder | the same, in a container, for each pool whose cache has a size limit and lives in a host folder outside the shared folder | The folder at its own path. The host's daemon mounts it into runners on its own, so the cache works without it, but the agent keeps the limit by measuring the folder, and without the mount it finds nothing to measure and never evicts. A folder the container already sees, through a folder above it bound at its own path, is left alone. |
| The runtime's socket, and the group that owns it | the same, on the Docker or Podman backend | The socket at its own path, and its owning group as `group_add` so the image's account can use it |
| The TLS certificate and key | a controller serving TLS from files | Both, read-only, at their own paths |
| `docker-compose.yml` itself | every Compose deployment | Write it again from `deployment.json` and the deployment's settings. |

Everything missing from a Compose file is added in one edit: the file is
copied to `docker-compose.yml.bak.<time>` first, and edited rather than written
again, so your own services, labels and comments stay; blank lines between them
do not survive the edit. On a `docker` deployment the replacement container is
created with the missing binds and group as well as everything the old one had.

A running host checks the part that matters most for itself: a containerised
agent that runs runners without the shared folder mounted from the host keeps
no tool cache rather than hand runners a folder the host's daemon cannot see,
and says so as
[`host.shared_folder_unmounted`](problem-codes.md). One whose container was
never given the runtime's socket says that, and names the line to add, rather
than telling you to install Docker.

How the approval is given depends on how the upgrade runs:

* **At a terminal**, it asks `Add them now? [Y/n]`. `install.sh --upgrade`
  asks on the terminal even when the script itself arrived through a pipe.
* **With `--yes`** (`zoomies upgrade --yes`, or `install.sh --upgrade --yes`),
  it adds them without asking.
* **Unattended** (`--non-interactive`, or no terminal at all) it adds
  nothing, finishes the upgrade as before, and ends by listing what is missing
  and the command that adds it: `zoomies upgrade --yes`. The shared folder is
  only needed by pools that keep a [tool cache](configuration.md#keeping-a-tool-cache),
  so a fleet without one loses nothing by waiting.

A missing `docker-compose.yml` is the exception: there is nothing to bring
the new image up with, so an unattended upgrade stops before it changes
anything and says so. `--check` lists what the real run will offer and adds
none of it.

`zoomies upgrade --check` checks the existing deployment without modifying it.
`zoomies update` is a compatibility alias and accepts the same flags. Both commands
fetch the newest release (or the rolling `dev` build, for a host running one),
verify its checksum, replace the installed binary and carry on in the new one,
so a host upgraded this way gets the new release's checks and prompts too. They
never downgrade, and a download that cannot be verified is not installed.
`--version v1.4.0` pins a tag and `--no-download` applies the binary already
on disk.

The parts of that worth knowing before you do it are what happens to work in
flight, how far the pieces may drift apart, and the one direction you cannot
go back in.

## Updating from the web UI

`sudo zoomies upgrade` on each machine is still how a fleet is upgraded, and it
stays the way for any host whose owner has not agreed to more. `updates.mode`
lets the controller start the same upgrade instead, through an *update helper*
that the owner of each host installs:

| Mode | What it does |
| --- | --- |
| `off` (the default) | Nothing new. The Overview says that a newer release exists, each host's card keeps its command to copy, and the mode creates no folder or unit. The Update buttons on the Hosts page are drawn but cannot be pressed, with the reason beside them. |
| `manual` | **Update** buttons: for the controller on Settings → Updates, on the card of each host whose helper is installed, and **Update N hosts** on the Hosts page for every host behind the controller whose helper is installed (N counts the hosts whose card offers Update). Nothing moves without a click. |
| `auto` | The controller takes the newest release once it has been public for `updates.soak`, then takes every host behind it to the release it runs, one at a time. The buttons stay, and mean "now, without the wait". |

Only the `platform` role changes the mode and the soak, on the Configuration
page ([`updates.mode`](configuration.md#updatesmode-what-is-done-about-a-newer-release));
everyone can read them on Settings → Updates. The `platform` role updates the
controller, and an administrator updates hosts and starts, resumes and cancels a
rollout. With the mode `off` each of those is refused with `update.mode_off`, and
a controller that is fenced for recovery or has lost its lease does none of
them. Every button is also a command, in [`zoomies updates`](cli.md#zoomies-updates).

A release is offered only once it is complete: a tag of the form `vX.Y.Z`,
neither a prerelease nor a draft, with `checksums.txt` and the binary for the
machine's system attached. GitHub makes a release public before all its files
are uploaded, so the newest release is not always one that can be installed. A
controller built from `main`, or from a checkout of your own, is never updated
this way: it is usually ahead of the newest release, and an update would take it
back.

### Adding the update helper

On a systemd host that has no update helper yet, an upgrade asks a question of
its own once the deployment's additions above have been reviewed:

```text
Add the update helper? [y/N]
```

The [update helper](security.md#what-the-agent-owns-on-a-host) is what lets
the web UI update the host. The update helper runs `zoomies upgrade` as root for a validated request, so that the web UI can update this host (and on the controller's host, `zoomies updates apply` can too). Only the host's owner installs it, and `sudo zoomies updates helper remove` takes it away.

It is a pair of systemd units, and a request names a release and nothing else.
The account Zoomies runs as can trigger an upgrade by writing a request, which
is why it is a question of its own and never part of the batch above.

* **`--yes` does not answer it.** `zoomies upgrade --yes` still adds the layout
  additions and moves the settings, and still leaves this question at its
  default.
* **At a terminal**, Enter, `n` and the end of the input all mean no. Only `y`
  or `yes` installs it.
* **`--update-helper`** (`zoomies upgrade --update-helper`, or
  `install.sh --upgrade --update-helper`) installs it without asking.
* **Unattended** (`--non-interactive`, or no terminal at all) it adds nothing
  and prints `Add later: sudo zoomies updates helper install`, with
  `--config-dir` appended when the upgrade ran against a configuration
  directory other than the default.
* **Once the helper is installed** the upgrade it runs for the web UI adds
  nothing and prints nothing about the helper.

The question comes after the deployment's additions are reviewed and before
anything is pulled or restarted. It is not asked, and nothing about the helper
is said, on a host without systemd, on macOS (launchd), for a private
provider's own service, for a service that runs as root (which can upgrade
itself and needs no helper), or once the helper is installed. When a service
cannot be resolved for a reason you can put right (an unreadable unit, an
account that no longer exists, a unit that names another `--config` directory),
an upgrade at a terminal says so in one line.

A container that does not yet mount the shared folder gets the mount from the
restart in the same upgrade, and the helper cannot be installed before then.
So in that run the question is not asked: the upgrade says that the helper
needs the shared folder mounted, which the restart adds, and to run
`sudo zoomies updates helper install` afterwards.

If the install is refused (the binary sits under a folder a group can write, a
path is under `/home`, Docker remaps user namespaces) the upgrade does not
fail: it prints the helper's own sentence, says the helper was not added, and
finishes. Put the refusal right and run `sudo zoomies updates helper install`.
`--check` asks nothing and adds nothing.

Where the helper can never be installed, the web UI does not suggest it. A
controller on macOS or Windows, on a Linux host that systemd does not run, in a
container that runs no runners (and so does not mount the shared folder), or in
a container under a rootless runtime shows its helper as **unsupported** on
Settings → Updates, with the reason and `sudo zoomies upgrade` to copy instead
of the install command, and raises no `controller.update_helper_missing`. On
Windows, which has no `sudo` and no service `zoomies upgrade` can update, there
is no command to copy, and the reason says how to replace the binary by hand. A host
behind the controller for one of the same reasons (its agent says which on its
heartbeat; an agent too old to say is known only by an operating system other
than Linux) shows **Update by command** on its card with the reason and no
button, and the upgrade command beneath it is the way. A Windows host shows
**Update by hand** instead: there is no command for it, and the foot of its card
gives the steps and links to [upgrading an agent host](#upgrading-an-agent-host).

The same question is asked once more, in the same words, at the end of a fresh
`zoomies init` and of `zoomies agent join`, once the service is installed (for a
container deployment, once it is up and has its shared folder). It follows the
same rules:

* **It defaults to no, and `--yes` does not answer it.** `zoomies init --yes`
  and `zoomies agent join --yes` leave it at its default; on `agent join`,
  `--yes` means only "replace the credentials this host already has".
* **`--update-helper`** (on `zoomies init` or `zoomies agent join`) adds the
  helper without asking. In an [answer file](quickstart.md#unattended-installs)
  the key is `update_helper: true`.
* **Unattended** it adds nothing and prints
  `Add later: sudo zoomies updates helper install`.
* **A controller-only container** is not asked, since it has no shared folder
  for the helper to watch.
* **If the install is refused**, the install or join still finishes: it prints
  the helper's own sentence and the command to retry.

`zoomies init` on a host where Zoomies is already installed only upgrades it in
place and does not ask; `zoomies upgrade` is where the question is asked then.

### What the helper does with a request

The service, which runs as an account without root, writes `request.json` into
the *update folder*: `update` under the state directory on a native install, or
under the shared folder in a container. The request names an attempt and a
release tag, and nothing else: no address, path, command or flag. Its arrival
starts `zoomies-update.service` as root, which treats everything in the folder
as hostile. It refuses a request that is not a plain file owned by the service's
account, a tag of any other shape, an attempt it has already seen, a third try
at one release, or a second request inside ten minutes, and it does not start
while `upgrade.lock` says an upgrade is already running. A release already
installed, or an older one, is answered as done and nothing runs.

Otherwise it runs the upgrade you would run, unattended, as
`zoomies upgrade --version <tag> --non-interactive`, and never with `--yes`.
That downloads the release's binary, checks it against the release's
`checksums.txt` and asks the downloaded binary to check this deployment before
anything is replaced, so a release that needs something the deployment lacks
stops with the binary and the service as they were. A required addition stops
it, with the command that adds it; an optional one is skipped and named. Then
the binary is replaced, the one it replaced is kept beside it as
`zoomies.previous`, and the service restarts. The helper writes `result.json`
saying how it went, which the controller reads, and
`sudo zoomies updates helper status` shows it with the end of the log.

### Updating the controller

**Update to** the release, on Settings → Updates, or `zoomies updates apply`,
asks the helper beside the controller to take it to the newest release that can
be installed (with `--version`, to that one). The controller records the
attempt, writes the request, and restarts as the new release. The page follows
the restart and says the update worked only once the controller does: the new
process closes the attempt when it finds the helper's result, or finds itself
running the release. A
controller that runs its agent inside itself updates that agent with it.

If the new controller does not come back, nothing in the web UI can say so. On
the host, `sudo zoomies updates helper status` and the journal of
`zoomies-update.service` can, and [rolling back](#rolling-back-by-hand) says
what to do.

### Updating hosts, and rollouts

A host is updated by its own agent, never by the controller reaching into it:
the controller hands the agent an `update_agent` task on the agent's own
long-poll, the agent writes the request into its host's update folder, and the
helper there does the rest. The agent offers this only while the helper's
marker is in an update folder it can write to, so a host without the helper is
never asked, and its card says why and keeps the command. A host is always taken
to the release its **controller** runs, never to the newest release, so an agent
is never ahead of its controller ([version skew](#version-skew)). An update has
worked when the host's heartbeat reports that release, not when the task is
answered. The agent repeats the helper's answer on its heartbeats until the
controller has recorded it, so an answer that arrives while the controller is
fenced, or cannot write its database, is recorded on the first beat after.

**Update** on a host's card updates that host. **Update N hosts** on the Hosts
page, or `zoomies updates apply --hosts`, starts a *rollout* of every host behind
the controller (`--host` names some): one host at a time, the one running the
fewest jobs first, then by name. No host is cordoned or drained for it. The first
update in a rollout that fails or times out **halts** it, and a halted rollout
starts nothing until an administrator resumes or cancels it, on the Hosts page or
with `zoomies updates resume` or `zoomies updates cancel`. Resuming moves on to
the next host while the one that failed waits out its retry. Cancelling starts
nothing more; an update a helper has already been handed finishes by itself and
is recorded. While any update is open, neither a rollout nor `auto` starts
another, so automatic updating never updates the controller under a host or a
host under the controller. A person pressing a button is not held back the same
way: only a second update of the same machine is refused.

### What auto does, and the soak

In `auto` the controller updates itself first, once the newest complete release
has been public for `updates.soak` (24 hours by default), counted from when GitHub
published it. A newer release restarts the wait, so a release replaced within the
soak is never installed: `v1.3.2` published four hours after `v1.3.1` means
`v1.3.1` is passed over and `v1.3.2` is taken a day after its own publication.
The cost is that a project publishing faster than the soak would never be taken
by `auto`, and Settings → Updates says so. Then it starts a rollout of the hosts
behind the release it now runs. A controller that is already behind a release
older than the soak when you switch to `auto`, with its helper installed, is
updated on the next pass, and the hosts are rolled after it. The soak is `auto`'s alone: a person pressing a
button has decided.

Auto acts only through helpers that are there. While a release is due and the
helper beside the controller is not installed, it waits, and updates no host
either, because no host may go ahead of its controller. Where that helper can
never be installed (see above), the controller is left to a person and hosts
follow the release it runs. A rollout cancelled by hand is not started again for
the same release, however short `retention.update_attempts` is: the newest
rollout a person cancelled for each release is kept when older history is
pruned. Switching to `manual` cancels a rollout `auto` started, and
switching to `off` cancels any open rollout; an update a helper is already
running finishes either way, and is recorded.

### What happens to running jobs

Nothing. An update restarts a service, and [a restart does not touch a running
job](#what-happens-to-work-in-flight): the job runs in its runner, the new agent
adopts every runner it finds, and the controller's restart only delays the
reporting. A rollout therefore does not wait for a host to be idle, and the
confirmation says so. A drill updates an agent while a job runs on its host and
holds the job to finishing.

### When an update fails

An attempt that has not finished in 90 minutes is recorded as timed out (the
engine's own worst case is fifty minutes, so a shorter limit would fail an
update that was still working). A failure or a time-out leaves the machine on the
release it had, raises `controller.update_failed` or `host.update_failed` (see
[Problem codes](problem-codes.md)), with the helper's own sentence for the
`platform` role on Settings → Updates or the host's card, and in a rollout halts it. After a failure Zoomies waits 30
minutes before it tries that machine again by itself, and after two failures of
one release it leaves that release on that machine to a person. Those failures
are kept while that release is still the one the machine would be taken to, so
`retention.update_attempts` never counts them back down. The helper keeps its
own count as well, in `/var/lib/zoomies-update`, where the service cannot reset
it.

### Rolling back by hand

There is no automatic rollback, because migrations are one-way ([there is no
downgrade](#there-is-no-downgrade)). What an update leaves makes one by hand
possible:

* **The binary it replaced**, kept beside the installed one as
  `zoomies.previous` (`/usr/local/bin/zoomies.previous` on a default install).
  Each update replaces it, and `sudo zoomies updates helper remove` leaves it;
  only `zoomies uninstall` removes it.
* **The database as it was**, which the new controller copied to
  `pre-migration/zoomies-<timestamp>/` beside the database before it applied a
  migration, if the release had any.

On a native controller, stop the service, put `zoomies.previous` back in place of
`zoomies`, and start it. If the older binary refuses to start because the
database has migrations it does not have, stop it again and put the pre-migration
copy back with [`zoomies restore`](backup-and-restore.md#upgrades-copy-the-database-first)
first. That has two costs: the restore fences the fleet and ends every session,
and anything written since the update is lost, the update's own attempt rows
among it. A host's agent has nothing to migrate, so the binary is all it needs. Turn
the mode to `off` or `manual` before you start, or `auto` will take the release
again once it is due. A container deployment's service is its image, so there
roll back by running the previous release's `vX.Y.Z` image, under the same rule
about the database.

### The first update is by hand

A machine that runs a release from before updating from the web UI cannot be
updated from it: its agent does not know the task, and its binary has no helper
to install. Upgrade each one by hand once, controller first, with
`sudo zoomies upgrade`. Answer yes when it asks to add the update helper (or pass
`--update-helper`), and from then on the web UI can update it. A service that
runs as root can upgrade itself and needs no helper, but the web UI cannot
update it either, and Settings → Updates may still offer
`sudo zoomies updates helper install`, which then refuses.

## Settings that were in `.env`

A controller's settings live in its database, where the Settings page changes
them. A container deployment installed by an older release carries them in its
`.env` instead (the external URL, bind address, TLS, trusted proxies, the
embedded agent's backend, socket and capacity, log settings) and a `ZOOMIES_*`
variable wins over the database, so each one shows on the Settings page as
locked, and a change made there never takes effect.

An upgrade finds them (in the running container's own environment, which is
what the controller reads, whether it came from `.env`, from a literal in the
Compose file or from a `docker run` long ago) lists them with the setting
each one sets, and with the same approval as above moves them:

1. It pulls the new image and stops the controller, which holds the database's
   lock.
2. A one-off container of the new image, on the deployment's own volume, runs
   `zoomies config import-env` and stores every value in the database in one
   write. The values reach it on standard input, never as arguments. A value
   the controller could not run with stores nothing at all.
3. Each line in `.env` is commented out, with a line above it naming the
   setting it moved to; a credential's value is not kept in the comment. On a
   Compose deployment the variables leave the service's `environment:` too,
   and anywhere else the file used them (an older file mounted the
   certificate as `${ZOOMIES_TLS_CERT_FILE}`) gets the value written in. The
   Compose file is copied to `docker-compose.yml.bak.<time>` first. On a
   `docker` deployment the replacement container is created without them.
4. The controller comes back up on the same values, from the database.

If the new image cannot store them, nothing is edited and the upgrade goes on
with the settings where they were. If the recreate fails, `.env` and the
Compose file are put back as they were before the move, because the image
being rolled back to may predate reading them from the database.

What stays in `.env` is what is needed before the database can be opened (the
encryption key, the database path, the state directory) and what Compose or
`docker run` reads itself: the image, the published address and port, the
docker group. An agent keeps its whole environment: it has no database.

`docker compose pull && docker compose up -d` upgrades the image and moves
nothing. For a Compose file you wrote yourself, such as the repository's
[`docker-compose.yml`](compose.md), stop the controller, run
`docker compose run --rm --no-deps -T zoomies config import-env < .env`, take
the lines it stored out of `.env` and the file's `environment:`, and bring it
up again.

## Host usage during a rolling upgrade

Resource-aware allocation adds migration `0028_host_usage.sql` and an optional
heartbeat field. Upgrade the controller first, then the agents, to make their
new measurements available for placement. Older agents can keep running: a
host with no fresh usage reading retains configured capacity and reservation
checks. Local Linux agents begin reporting memory on their first sample and
CPU after a second sample. Unsupported or remote runtime measurements are
shown as unavailable, never as zero usage.

The [pressure rules](hosts-and-pools.md#current-usage-and-automatic-holds) affect
new runner starts and do not clear operator cordons. No new configuration is
required. This remains a schema migration, so the backup and rollback rules
below apply.

## Default allocations and throttling during a rolling upgrade

Migration `0029_host_throttle_and_runner_allocation.sql` adds the throttle
column to hosts and the allocation columns to runners, and the release that
carries it changes what a runner is given. Both halves are **on by upgrade**:
`scheduler.default_runner_limits` and `scheduler.host_throttling` default to
true, and each is a warning when turned off. What that means for a fleet, in
the order it will be noticed:

* **A pool that sets no `cpus` or `memory_mb` no longer gets unlimited
  containers.** Its runners are created with one slot's share of their host's
  allocatable CPU and memory as a real cgroup limit; the same share the
  scheduler was already charging them. A job that used to have the whole
  machine to itself on a quiet host now has its share of it, and a job that
  needed more memory than its share is killed for exceeding a limit nobody
  typed. The runner's message says the limit was the host's default share and
  names the two ways out: set `memory_mb` on the pool, or lower the host's
  capacity so each runner's share is larger. `host.overprovisioned` says
  before any job does when a host's capacity gives each runner less than a
  core or under 2 GB. [Default
  allocations](hosts-and-pools.md#default-allocations) has the rules.
* **The host's CPU reserve now has a floor** of half a core, or a twentieth of
  the machine on a large one, held back for the daemon. A fleet whose explicit
  pool CPU limits summed to exactly the host's CPUs (four pools of 2 CPU on
  an 8-CPU box, say) takes **one runner fewer per host** than it did, because
  the last one no longer fits; pools with no limits simply get a slightly
  smaller share. A host's `allocatable_cpus` in the API and its committed CPU
  bar on the card show the figure the floor leaves. An operator's own
  `reserve_cpus` replaces the floor where it is larger.
* **Runners created before the upgrade keep whatever limit their pool set**,
  none where it set none, but carry no recorded allocation and an empty
  `allocation_source`: the allocation is written when a runner is made, and
  nothing is applied to a live container retroactively. Two things follow
  while such a runner is on a host. It is counted among the host's
  `unlimited_runners` whatever its pool's limit, so a sustained CPU hold there
  can still throttle the host; and the throttle cannot slow it, because the
  agent lowers only a quota whose allocation was recorded with the container,
  so its job runs at full speed and only the host's smaller effective capacity
  applies. Both end when its pool's next runner replaces it, which for an
  ephemeral runner is after one job.
* **Upgrade the agents before expecting either half.** The controller gives a
  default only where the host's daemon has said it can apply the limit, and an
  agent from before the probe has not said, so its hosts get no defaults and
  `host.limits_unverified` names them until the agent is upgraded. Throttling
  needs the agent's measurements (including the load average, which older
  agents do not send) and needs the agent to understand the throttle
  directive in the heartbeat response; an older agent ignores it, its running
  jobs are never slowed, and only the smaller effective capacity applies.
  Everything else about an older agent holds as before: a host with no fresh
  usage is placed by its configured capacity, and a throttle whose host stops
  measuring is lifted after ten minutes rather than left standing.

To turn either half off: `scheduler.default_runner_limits: false`
(`ZOOMIES_DEFAULT_RUNNER_LIMITS=false`) restores unlimited containers for pools
that set no limits, and `scheduler.host_throttling: false`
(`ZOOMIES_HOST_THROTTLING=false`) stops the controller stepping hosts down and
lifts any throttle already standing. Each is warned about at startup and in the
problems drawer for as long as it stands, because both are the thing that keeps
a host's Docker daemon answering. The CPU floor has no switch; a fleet that
wants every core placed has a smaller reserve than the daemon needs.

## A docker-in-docker slot is one runner again

A `dind` pool runs two containers per runner: the runner, and the daemon its
builds run inside. A pool that **types** its own CPU and memory has both given
those figures (the build would gain nothing from a limit on the container that
is not building) so the host is charged twice, as it has been since the
release that started charging for the pair at all.

What changes here is the pool that leaves its size to the host. Its runner and
its daemon now **split one slot** between them, and the host is charged one. So
a host set to eight slots carries eight runners of such a pool, the same as any
other, where the last release made it four, and an operator who followed the
`pool.host_overcommitted` advice to "adjust the host to the slots its machine
can back" was walked down to fewer slots each time they took it, arriving at
one slot, which held nothing: one slot is the whole machine's share, doubled is
the whole machine again, and a machine always measures a little less free than
it is allocatable.

For such a pool that means **twice as many runners per host** as the last
release, each with half the machine it had: the pair divides the slot rather
than taking two. Nothing changes for a pool that is not `dind`, nothing changes
for one that typed its own figures, and no job is failed by it. A slot too
small to give both halves what a runner needs (under half a core, or under a
gigabyte, after the reserve) is refused with a sentence naming the capacity
that divides it, rather than divided into a runner and a daemon with no limit.
`host.overprovisioned` now counts a slot as a pair only for pools that typed
their limits.

## Size classes and automatic pools arrive off

[Size routing and automatic pools](auto-pools.md) are two switches,
`scheduler.size_routing` and `scheduler.auto_pools`, and both are `off` after an
upgrade. Nothing is classed, no pool is made, no pool is changed, and every column
the migrations add holds the value an existing row already had, so no host, pool or
job is different until somebody sets one. No agent needs upgrading and the protocol
version is unchanged: an agent that sends no CPU throttling counters only means its
jobs are classed on memory alone.

There is one change that is not behind a switch, and it is a repair. A host that
joins again (a rebuilt machine, an agent restarted with its old credentials) now
keeps the labels an operator added since it first joined, and the size class it
held. Before, the join built the row from the token and the agent alone and quietly
dropped every edit, which the comment on that code said it did not. The join
token's labels still win, then what the agent declares now, then what was stored: a
label the agent declares is the agent's, so an operator's edit of it is put back,
and one the agent's configuration dropped stays until `zoomies hosts edit --untag`
removes it.

Two things in the UI are not behind a switch, because they cost nothing while the
feature is off. A host's card and its dialog call the labels on it *tags*, and list
beside them the ones the controller works out from the machine, `os` and `arch`;
what a pool's host selector matches is unchanged. And the Overview's feed gains an
**Automatic changes** kind, on by default and empty until a switch is set, which
**Settings → Events** turns off.

## Kennel Club arrives off

[Kennel Club](kennel-club.md) is one switch, `kennel.enabled`, and it is `off`
after an upgrade. Nothing is read from GitHub, nothing is evaluated, and no
problem is raised, so no repository is different until somebody sets it. AI
Context, which now sits under the same **Kennel Club** heading in the UI, carries on
exactly as it was, and its old `/ai-context` address still works.

The migrations add three tables and change nothing that exists: `0080` adds the
evaluation and the waiver, and `0082` adds the record of a repository Kennel Club
has been told not to look at. They apply with the rest, and the usual backup and
no-downgrade rules cover them. Turning it on the first time reads the repositories
this fleet serves, within a GitHub request budget of its own (`kennel.api_budget_percent`,
20 per cent of the limit an installation reports by default), and the first
evaluations arrive over the next few minutes, not at once.

## The usage ledger

Migrations `0047_runner_sessions.sql` and `0048_usage_daily.sql` add two
tables and change none. The first records a session for every runner already
cleaned up when the upgrade runs, reading the runners table without rewriting
it, so the usage report's runner history begins at the oldest runner row the
database still had (about a week back with the default `retention.runners`)
rather than at the upgrade. Runners pruned by an earlier build are gone, and no
migration can bring their hours back; `history_from.runners` on `/usage` says
where the ledger begins.

From then on every runner gets one session when its cleanup is confirmed, or
when the prune takes a row whose cleanup never was, and the prune pass rolls
whole UTC days into `usage_daily` before anything they were computed from can
go. `retention.runner_sessions` keeps sessions for a year by default and never
deletes one the roll-up has not absorbed; the roll-up itself is not pruned. No
configuration is required. The new migrations mean the backup and rollback
rules below apply.

## The per-installation report

Migration `0051_installation_report.sql` adds the daily roll-up of the
[per-installation report](metrics.md#per-installation-report)'s counts and two
columns to `runner_sessions`: the runner's first create task and its job's
eligibility, the two ends of the scheduling interval. Sessions already recorded
get them where the runner and job rows are still there to copy from; the rest
give no scheduling or registration sample, which the report counts as missing
rather than zero. The first roll-up starts from the oldest job or runner row
the database still has, and `counts_from` on the report says so.

From then on the prune pass holds the jobs prune and the sessions prune behind
that roll-up, so a job row is never deleted before the day it belongs to has
been counted. A job still unfinished when `retention.jobs` would take it is
counted as it stood. No configuration is required.

## What happens to work in flight

A restart does not touch a running job. The runner is a container on its host,
the job is executing inside it, and neither is talking to the controller while
that happens, GitHub is. That holds on a host with its own agent, and it now
holds on a single-VM install too, where the agent runs inside the controller:
an agent lists what is already on its host before it starts its loops and
adopts every runner it finds, so a restart finds its own work rather than a set
of containers nobody claims.

It is the controller that says what may be cleaned up. An agent reports the
runners it adopted, and the controller answers with the ones it has no record
of; a runner deleted while the agent was down, say. Only those are removed.
The agent never decides on its own that something is litter, because it cannot
tell "the controller deleted this" from "I have forgotten it", and only one of
those should cost somebody a job.

What a restart interrupts is the *reporting*: webhook deliveries during the gap
are missed, and the fallback poller catches up when the controller returns,
which is one of the reasons to leave it on.

The task queue does not survive a restart either, and that is deliberate: every
task is derived from state the database already holds, so persisting it would
add a second source of truth that could disagree with the runners table. An
agent that finishes work across the gap reports a result for a task the new
controller never issued, and it is applied anyway; the agent did the work, and
the row is the only place that fact can land. Each runner row also records when
its task was last handed to its host, so a restart does not lose how long the
host has actually had it.

One thing a late result cannot do is bring a runner back. If the host was quiet
long enough to be given up on, the fleet has already told an operator, and the
job's timeline, that the runner was gone; a success arriving afterwards does not
make that untrue. Instead the runner keeps its terminal row and the *workload*
is settled: while a job is still running on it the container is left alone to
finish, and once nothing is, it is removed. The job's timeline gains a **runner
returned** entry, so a job that completes normally after its runner was written
off does not read as a contradiction.

Agents keep working while the controller is down. They long-poll, so a
connection that fails is retried with backoff, and a host that cannot reach the
controller does not stop the runner it already started.

Two consequences follow:

* **Upgrade whenever you like.** There is no drain-first ritual. A fleet with
  fifty jobs running is as safe to upgrade as an idle one.
* **A host that stays silent past `store.HeartbeatTimeout`, ninety seconds,
  is counted unhealthy.** A controller that is down for longer than that will
  show every host as unhealthy for a moment when it comes back, until the next
  heartbeat arrives. That is the display catching up, not a fault.

## Version skew

The controller and its agents are separate binaries on separate machines, and
they do not have to match.

The policy, in three rules:

* **The protocol version must match.** It is checked when an agent joins (a
  mismatch is refused there, because an agent that cannot join has nothing
  running to strand) and on **every heartbeat** after that, because an agent
  that joined before a bump would otherwise keep polling and receiving tasks it
  could not understand.
* **An agent may lag the controller by releases**, as long as the protocol
  matches. This is the normal state during a rolling upgrade. The Hosts page
  shows what each host is running.
* **A newer agent against an older controller is unsupported.** It usually
  works, because the controller's API is additive, but it is not a direction
  anyone tests. Upgrade the controller first: it owns the schema and the API,
  and an agent has nothing to migrate.

### What happens when the protocol stops matching

The host is **excluded from placement, exactly as a cordon excludes it**, and
nothing else. Its runners keep working, its agent keeps draining and stopping
them, and the fleet shrinks host by host as it goes.

It is deliberately not a refusal. Answering a heartbeat with an error would
send every agent in the fleet into its re-join path at the same moment, which
is the outage the upgrade was meant to avoid.

The host says `incompatible` on the Hosts page with both protocol versions, a
pool that can no longer place says "running an agent this controller cannot
talk to" with the fix, and the agent logs an error about itself on every
change. Upgrading that agent clears it on the next heartbeat, with no re-join.

An agent old enough not to report a protocol version at all is **not** judged.
It is the one case the controller cannot decide, and guessing would empty a
fleet the moment its controller learnt to ask.

### A task kind an agent does not know

An agent handed a task kind it does not understand reports that task as failed,
with a message saying to upgrade it. The controller treats an unrecognised kind
as **not** lifecycle work, so the runner the task concerned is left alone; a
runner that is running a job is not made to fail by a message neither side can
name. Only `create_runner`, `stop_runner` and `remove_runner` are lifecycle,
and that list is an allowlist so a kind added in a later release is safe on an
older controller by default.

### Two agents as one host

Copying a VM, or a state directory, to a second machine gives two agents one
host identity. Both hold a valid token, both report real work, and each sees
only half of that host's tasks, which looks like a fault almost anywhere else.

Zoomies notices. An agent takes a fresh session each time it starts and never
returns to an old one, so a session a host has already moved on from can only
be a second agent still running, and `host.duplicate_agent` says so. Neither
session is refused: both are executing real jobs, and picking one would end the
other's. Stop the agent on the machine that should not be there and re-join it
with its own join token; the problem clears itself an hour after the sessions
stop swapping.

## The platform role, and what your administrators keep

This release adds a fourth role, `platform`, above `admin`, for whoever runs
the process rather than the fleet. Two things move behind it: lifting the
recovery fence, and taking, downloading and restoring backups. A backup is
the whole database (every account's password hash and every sealed
credential, under the key this host holds) so it belongs to whoever operates
the instance.

**Nobody loses anything on the way through.** Every account that held `admin`
comes up holding `platform`, and so does every API token minted at `admin`
that has not been revoked. That is the same authority as before and not one
action more: `platform` is `admin` plus the two things above. A nightly
`zoomies backup` running on an administrator's token keeps working.

What is carried across is what held `admin` *before* the role existed. If you
track `main` and have already started a build that added the role, an account
or token you have made at `admin` since then stays `admin`; you made it
knowing what `admin` no longer reaches, and an upgrade should not overrule
that. Upgrading from a release, the two steps run seconds apart on the same
start, so this excludes nothing you have.

You do not have to do anything. On an instance one team runs, the change is
invisible: everyone who could take a backup yesterday can take one today, and
the Backups page looks the same.

The role earns its keep on the other shape; an instance one team operates
while another uses the fleet. There you separate the two deliberately, by
giving the fleet's people `admin` and keeping `platform` for whoever runs the
process. An upgrade will not do that to you on its own.

The last enabled `platform` account cannot be demoted, disabled or deleted,
for the same reason the last administrator could not be: an instance nobody
can operate is one only a shell can rescue.

## Schema migrations

Migrations are embedded in the binary, run on first start, and recorded in a
ledger keyed by file name. Two rules the code enforces and a test holds:

* A shipped migration's file name never changes. Renaming one re-applies its
  DDL to every existing database.
* A new migration takes the next unused numeric prefix, alone.

They run in one transaction each, in lexical order, and a failure stops startup
with the name of the file that failed. Nothing is applied twice.

Two migrations change rows rather than shape. `0010_docker_pools_get_a_client`
moves a pool whose `docker_mode` is not `none` from the stock runner image
under a moving tag (`latest`, `main`, or none) to
`ghcr.io/eyupio/zoomies-runner-docker` under the same tag, because the stock
image has no client for the daemon that mode gives it; the API makes the same
change to every pool saved from then on. A pool pinned to a `sha-<commit>` tag is not
touched, and neither is a pool on a digest or on an image of its own. What the
migration left behind, the controller resolves as it makes a runner: every tag
the running build publishes (the channels and the operating-system aliases,
`vX.Y.Z` among them) is swapped there, and a pool on a reference that cannot be
swapped raises `pool.docker_client_missing` rather than failing its jobs one at
a time. Idle runners made from the old
image are drained and replaced on the first scheduler pass. See [Jobs that
build container images](configuration.md#jobs-that-build-container-images).

`0012_job_installation` is the other. It records on every unfinished job which
GitHub App installation covers its repository, matching an installation on the
repository itself before one on the organisation that owns it, and it then
unclaims any waiting or queued job whose pool turns out to belong to a
different installation. Before it, a job carried no installation identity at
all and a pool was chosen for it on labels alone, so on a controller with more
than one installation a job could be, and deterministically was, matched to a
pool in the wrong GitHub target. Those matches are the ones it takes back: the
pool would never have run the job, and the next scheduler pass decides again.
A job in a repository no installation here covers keeps no installation and is
unclaimed for that reason, which the Jobs page and the problems drawer both
say. Jobs that have already finished are not touched, and neither is a job
already in progress: its runner exists, and where it ran is a fact worth more
than a tidy row. **A controller with one installation sees no change**, because
every pool on it belongs to that installation.

## There is no downgrade

**Migrations are one-way.** There are no down migrations, and there is no
command that removes one.

An older binary started against a newer database **refuses to start**, and says
which migrations it does not have. That check arrived after `0.2-beta`, so
rolling back *to* `0.2-beta` itself is the one case where nothing stops you:
that release will come up on a migrated database and look perfectly healthy.
Put the pre-upgrade copy back alongside the binary, which is what the rest of
this section is about. SQLite itself does not object (it has no
opinion about columns nobody reads) which is exactly why the check exists:
without it, the older binary comes up, looks healthy, reads columns whose
meaning it does not know and writes rows the newer one will not accept, and
does all of it silently. Rolling a release back is a thing people do under
pressure, and this is the moment to be told that the database went forward
with it.

So the rollback plan is a copy of the database from before the upgrade, and
**the controller takes one for you**. Whenever it starts and finds migrations
pending on an existing database, it copies the database to
`pre-migration/zoomies-<timestamp>/` beside it before applying anything, and
keeps the last two. It is the same layout `zoomies restore` takes, so putting
one back is one command.

Take your own as well before an upgrade you are unsure about: the automatic one
is beside the database, and a disk that fails takes both.
[Backup and restore](backup-and-restore.md) is the subject.

## Which image tag to run

Four images are published, and the tag says where the build came from rather
than only how new it is.

| Tag | Means | Moves |
| --- | --- | --- |
| `:latest` | the newest full release | when a release is published |
| `v1.2.3` | that release, and only that | never |
| `:dev` | the newest commit on `main` | on every merge |
| `:main` | the same as `:dev` | on every merge |
| `:sha-abc1234` | one commit | never |

```text
ghcr.io/eyupio/zoomies                 the controller
ghcr.io/eyupio/zoomies-agent           an agent, for a host that runs one in a container
ghcr.io/eyupio/zoomies-runner          the runners a pool starts
ghcr.io/eyupio/zoomies-runner-docker   the same, with a Docker client
```

`:latest` on every image means the newest **full release**. It used
to mean the newest commit on `main`, which cost more than a name: both the merge
and the release wrote it, so whichever ran last won, and an operator who pulled
it could get an unreleased build stamped `main-sha-abc1234`. CI now keeps a
rolling `dev` prerelease asset beside the `:dev` images, so the Add Host command
can install the same channel as a controller tracking `main`. Run `:dev` when
you want `main`; it says so.

A prerelease (a tag with a hyphen in it, `v0.1-alpha`, `v1.0-rc1`) is
published under its own tag and does **not** move `:latest`. Name it to run it.

Runner images use the same split. Their default `:dev` follows `main`, while
`:latest` follows the newest full release. Per-platform development tags use
the explicit form `ubuntu-2404-dev`; release builds use
`ubuntu-2404-v1.2.3`. Pin either form on a pool when it must not cross channels.

## What a release carries

Every published binary and the controller image carry a **build-provenance
attestation**: a signed statement that these bytes were built by this
repository's release workflow, from this commit. `install.sh` already checks
the checksum, which says the bytes match what the release names; provenance
says where they came from.

```sh
gh attestation verify zoomies_linux_amd64 --repo eyupio/zoomies
gh attestation verify oci://ghcr.io/eyupio/zoomies:v1.2.3 --repo eyupio/zoomies
```

The attestation is attached twice. `zoomies-provenance.sigstore.json` is the
Sigstore bundle `gh attestation verify` reads; the signed statement together
with the certificate and transparency-log entry that say who signed it.
`zoomies-provenance.intoto.jsonl` is the same signed statement on its own, in
the DSSE envelope that SLSA tooling and the OpenSSF Scorecard recognise as
provenance. Both describe every binary in the release; neither is needed to
install, and `install.sh` checks neither.

Every image says what it is without being started, in the standard OCI labels,
`org.opencontainers.image.version`, `.revision` and `.created`. That includes
the runner images, which until recently carried no version at all.

A tag with a hyphen in it (`v0.1-alpha`, `v1.0-rc1`) is published as a
**prerelease**. GitHub keeps prereleases out of `/releases/latest`, so while
every release so far is one, `install.sh` with no `--version` asks the API for
the newest release of any kind instead. Once there is a full release, that is
what "latest" means and prereleases stop being offered. Either way, name the
tag with `--version v1.2.3` when it matters which one you get.

A published full release cannot be rebuilt: the release workflow refuses.
A published prerelease can, because it is still explicitly not finished. A
released tag is a promise about specific bytes, and replacing them behind
people who have already downloaded them is not an upgrade anyone can reason
about.

## Upgrading an agent host

A controller upgrade is one machine. A fleet is not: each agent host runs jobs
that belong to somebody, and the sequence below is the difference between an
upgrade nobody notices and a wave of failed builds.

```sh
# 1. Stop new work arriving, and let what is here finish.
zoomies hosts drain hst_k3f9qz2m

# 2. Wait for it to empty. Each runner gets five minutes to finish what it is
#    on, so this takes about that, not as long as the longest job.
zoomies hosts list

# 3. Swap the binary and restart the unit.
curl -fsSL https://zoomies.sh/install.sh | sh -s -- --no-init
sudo systemctl restart zoomies-agent

# 4. Let it take work again.
zoomies hosts uncordon hst_k3f9qz2m
```

`hosts drain` cordons before it drains, and the order is the whole point:
draining a host that still accepts work means the scheduler puts a fresh runner
on it while the old ones are finishing, and the count goes down and back up
while an operator watches and concludes the drain failed.

Step 2 is optional, and skipping it is safe rather than merely tolerated: an
agent that restarts over running work adopts it rather than reaping it, which is
what makes a binary swap non-disruptive at all. Draining first is what makes it
*predictable* (a host with nothing on it cannot surprise you) and on a host
whose jobs are short it costs a few minutes.

On a Windows host the same sequence is `zoomies hosts drain`, replace
`zoomies.exe` in place, then `sc.exe stop zoomies-agent` and
`sc.exe start zoomies-agent` from an elevated prompt, and `zoomies hosts
uncordon`. A Windows host's card gives these steps in place of a command, since
`zoomies upgrade` knows no Windows service. A Windows agent runs the `process` backend, so the runner release it
downloads is pinned by the pool's `runner_version` and the digests in the
binary, and an agent behind the controller's release may not know a digest the
controller's default asks for; upgrade the agent first on that platform.

An agent that comes back on a release the controller does not recognise is
excluded rather than refused: it keeps heartbeating, its running work finishes,
and no new runner is placed on it. The Hosts page says so on the card. See
[What happens when the protocol stops matching](#what-happens-when-the-protocol-stops-matching).

## Upgrading a container deployment

```sh
docker compose pull
docker compose up -d
```

The database lives in a named volume rather than in the container, so replacing
the container keeps it. `docker compose down` is safe; `down -v` deletes the
volume with the database in it, which is the one command on this page that
cannot be undone.

## After an upgrade

`zoomies version` says what is running, and `zoomies status` says whether the
fleet is happy with it. If a setting was removed or renamed in the release, the
validator says so by name at startup rather than ignoring it: an unknown key in
`zoomies.yaml` is refused, so a setting that silently does nothing is not a
state this can get into.

## RC1 runner timing and restart reporting

An agent restarted over a process runner may not know its eventual exit code,
because the process belonged to the old agent. That outcome is now explicit:
the runner is removed with an unknown-exit message, while GitHub remains the
source of the job result. An exit code the agent did record still distinguishes
a clean exit from a failure.

The RC1 migrations retain older cleanup timestamps as estimates. Confirmed
cleanup needs both host and GitHub observations, so an older row may show
“Awaiting confirmation” alongside its earlier estimate. Upgrade the agents as
well as the controller to receive autonomous host-cleanup confirmations.

## Host health after an upgrade

Upgrades finish with a read-only host health summary and point to `zoomies
doctor` when warnings appear. They never apply OS tuning, including with `--yes`. Container
deployments are offered a native read-only health service through the existing
layout review; approval is required to add it. See [Host health and tuning](host-health.md).
