# ZF-232: updating Zoomies from the web UI

**Status**: proposed. The owner approved the shape on 8 October 2026 and chose
the update path ([decision 0011](decisions/0011-an-operator-may-let-zoomies-update-itself.md));
this text is waiting for their review. Nothing is built.

An operator should not have to run `sudo zoomies upgrade` by hand on the
controller and then on every agent host. Updating becomes a choice with three
settings:

* **off**: as today: a notice that a release exists, and a command to copy;
* **manual**: an Update button in the web UI, for the controller and for hosts;
* **auto**: Zoomies does it, within limits an operator can read.

It is mostly orchestration. The engine already exists. What is missing is a way
to act with the right privilege, and a policy on top.

**Success looks like this.**

* With the mode `off`, nothing changes on any installation and nothing new is
  created.
* With `manual`, an administrator can bring the controller and every host that
  opted in to the newest release from the browser, and sees why a host cannot be
  brought.
* With `auto`, a fleet whose hosts opted in follows each release without anyone
  running a command. It never installs a release that was replaced within the
  soak, and it stops at the first failure with a sentence that says what to do.
* None of it interrupts a running job.

## What is already here

* **The notice.** The controller asks GitHub which release is current
  (`internal/controller/updates.go`, every `updates.check_interval`, 24 hours by
  default, `0` never asks) and raises `controller.update_available`. It
  downloads nothing.
* **The engine.** `zoomies upgrade` is `installer.SelfUpdate` and
  `installer.Upgrade`. The first downloads a release, checks its sha256 against
  the release's `checksums.txt`, runs the candidate once, replaces the binary
  atomically and refuses a downgrade. The second reviews the deployment's
  layout, restarts the service (systemd, launchd, Compose or Docker) and, for
  a controller, waits for `/healthz`.
* **The host command.** A remote host's card shows
  `sudo zoomies upgrade --mode agent --version <tag>`
  (`internal/controller/host_upgrade.go`), aimed at the controller's own
  release, so an agent is never ahead of its controller.
* **Restart safety.** An agent restarted over running jobs adopts them
  ([upgrading](../docs/upgrading.md#what-happens-to-work-in-flight)). It
  advertises capabilities in `Features`, and a task kind it does not know is
  failed without harming the runner (`lifecycleTask` is an allowlist).
* **The settings group.** `updates.*` is platform-scoped (decision 31), and the
  registry already has enums.
* **A pattern for a consented root unit.** `zoomies-host-health.service` runs the
  host's own binary beside a container deployment and is offered through the
  upgrade's layout review.

## What stops it being a button

1. **A service cannot update itself.** Both unit templates
   (`internal/installer/templates/`) run the process as an unprivileged user,
   `zoomies` by default, with `ProtectSystem=strict` and only the state
   directory writable. The binary is root-owned in `/usr/local/bin`, and restarting a unit
   needs root.
2. **The delivery rules, and a deferral.** Rule 9 (off by default), rule 12 (no
   auto-upgrade as a recovery shortcut), rule 16 (the platform's hand on a host
   is the agent) and decision 25's deferral of controller-driven agent
   upgrades. [Decision 0011](decisions/0011-an-operator-may-let-zoomies-update-itself.md)
   reads them: this is opt-in policy, never recovery, and it updates Zoomies'
   own binary only.
3. **Releases are superseded within hours, and are public before they are
   complete.** `v1.3.1` was published at 14:59 on 25 September and `v1.3.2`
   replaced it at 19:23. The release workflow uploads assets one at a time, and
   `V1.1.0` is a published full release with no assets at all. "Latest" is not
   "ready".
4. **Migrations are one way.** There is no automatic rollback
   ([upgrading](../docs/upgrading.md#there-is-no-downgrade)), so an unattended
   update has to find what would stop it before it replaces anything.
5. **Hosts that predate the feature** cannot update themselves. One last manual
   upgrade installs the helper.

## 1. The three modes

| Mode | What it does |
| --- | --- |
| `off` (the default) | Nothing new. The notice and the copyable command stay. No helper is created. |
| `manual` | The Update buttons appear: the controller, one host, or every host that is behind. Nothing moves without a click. |
| `auto` | The controller takes the newest release once it has stood unreplaced for the soak, and hosts then follow the controller's release, one at a time. The buttons stay, and mean "now, skip the soak". |

**Settings**, both platform-scoped and live, so only the `platform` role changes
them:

* `updates.mode`: `off | manual | auto`, default `off`.
* `updates.soak`: a duration, default `24h`, `0` allowed. It applies to `auto`
  only: a person pressing the button is the soak.

`updates.check_interval` is unchanged and is still the air-gap switch. With `0`
this feature has nothing to go on, so a mode other than `off` beside it is a
warning (`updates.mode_without_check`). `off` also keeps today's single
`releases/latest` request, byte for byte: the release list, which is larger, is
fetched only when the mode is not `off`. `auto` raises an info finding that
names the setting (`updates.auto`), so unattended updating is never silent (the
rule in [internal/config/CLAUDE.md](../internal/config/CLAUDE.md)). An info
finding reaches the startup banner, `zoomies config check` and the Settings
page, though not the problems drawer. `auto` with a soak of `0` also raises a
warning (`updates.auto_without_soak`), because that removes the only wait. A mode
the registry does not know, or a negative soak, is an error that stops startup.

**Who may press what.** `platform` changes the mode and updates the controller;
`admin` updates hosts; `operator` and `viewer` read. With the mode `off` nobody
can press anything: the platform decides whether the capability exists, and a
fleet's administrators use it. Every action also refuses while the controller is
fenced for recovery or has lost its lease, as other mutations do (`mayAct`). The
mode and soak are registry rows, so the `platform` role sees them on the
Configuration page; an administrator sees them through the Updates panel, which
reads `GET /api/v1/updates`.

**Which release.** A *complete* release is a tag of the form `vX.Y.Z` (so the
stray `V1.1.0` is out), not a prerelease or draft, that carries `checksums.txt`
and the binary for the host's OS and architecture.

* `manual` takes the newest complete release, newest by version.
* `auto` takes it too, but only once it has been public for `updates.soak`,
  counted from its `published_at`. A newer release restarts the wait, so a
  release that is replaced quickly is never installed. With a 24-hour soak,
  `v1.3.2` arriving four and a half hours after `v1.3.1` means `v1.3.1` is
  skipped, and `v1.3.2` is taken a day after its own publication. The cost is
  that a project publishing faster than the soak would never be updated by
  `auto`; the status sentence says so, and the button still works.
* A build that is not from a release (`main-sha-…`, a local describe) is never
  updated by this feature. The existing development notice stays.

**Hosts follow the controller.** A host's target is the controller's running
release, never "latest", so an agent is never ahead of its controller (the skew
policy in [upgrading](../docs/upgrading.md#version-skew)). It is `v` followed by
`version.Release(version.Version)`, and only if that matches
`^v[0-9]+\.[0-9]+\.[0-9]+$`. `version.InstallTag` is the wrong test: it answers
`dev` for a build from `main`, which the helper would refuse. A controller on
`dev`, `main-sha-…` or a local describe has no target, and its hosts keep the
copyable command, which still names `dev` as it does today. Release binaries
report `1.3.5` without the `v`, so every comparison of a host's version with a
tag goes through `version.CompareBuilds`, never equality.

## 2. The helper

A pair of systemd units on a host that wants to be updatable:

```ini
# zoomies-update.path
[Path]
PathExists=<update folder>/request.json
Unit=zoomies-update.service

# zoomies-update.service -- runs as root, one request at a time
[Service]
Type=oneshot
ExecStart=<binary> updates helper run
TimeoutStartSec=2h
```

The paths are rendered from the host's real update folder and binary. The
service's hardening is as tight as an upgrade allows. It must write the binary,
unit files and Compose files, and pull images with the credentials root holds,
so it cannot use `ProtectSystem=strict` and sets `ProtectHome=read-only`, but it
keeps `NoNewPrivileges`, `PrivateTmp` and the kernel protections. The two hours
are a backstop. The engine's own worst case is fifty minutes (twenty to stop
the old service and thirty to wait for the controller to answer) and a limit
shorter than that would kill the run midway and leave `upgrade.lock` behind. The
helper consumes the request, removing the file once it has been read, so the path
unit fires once per request.

**The channel is a folder, not a socket, and its location is recorded rather
than recomputed.** The service account writes `request.json` into the *update
folder*, and the helper writes `result.json` beside it. On a native install the
folder is `update` under the state directory, which the unit lets the service
write. In a container it is `update` under the shared folder, which a container
that runs runners already mounts at the same path on both sides.

The two sides cannot be left to work the path out for themselves. A non-root
service with no `ZOOMIES_STATE_DIR` resolves `config.SharedDir()` under its own
`$HOME/.config`, because `StateDir()` never reads systemd's `STATE_DIRECTORY` and
the unit templates set no environment, while the installer, running as root,
resolves `/var/lib/zoomies/shared`. So on a native install the helper's installer
writes `update-helper.json` into the configuration directory, naming the folder,
the binary and the account, and the service reads it from beside its own
`--config` file. It is root-written, and the unit's sandbox keeps `/etc`
read-only for the service. A container has no such directory and uses
`<shared>/update`.

Nothing creates the folder until the helper is installed. It is deliberately not
in `config.SharedLayout`, which every agent start creates: that would put a new
folder on every host in mode `off`, and offer every existing host a layout change
it never asked for. A `helper.json` in the folder says the helper is installed,
which is how the controller knows to offer the button.

**The request names one thing:**

```json
{ "v": 1, "id": "upd_k3fqz2mx7abcd", "tag": "v1.3.5",
  "requested_by": "user:alice", "requested_at": "2026-10-08T09:00:00Z" }
```

The id is the attempt's, minted by `store.NewID`.

**The root side is the trust boundary, and it fails closed.** The service is the
less privileged party, so the helper treats everything in the folder as hostile.

* The update folder is opened with `os.OpenRoot` and the request is read once
  through one descriptor. It must be a regular file, at most 4 KiB, owned by the
  account recorded when the helper was installed (the state directory's owner on
  a native install, uid 65532 in the images) with known fields only. What was
  read is validated, not the path, so there is nothing to swap between the check
  and the use. A rootless or user-namespace-remapped container host owns files
  under another uid, so the check fails closed there and the helper does not
  serve it.
* `tag` must match `^v[0-9]+\.[0-9]+\.[0-9]+$`. The request carries no URL, path,
  command or flag, and the release source comes from the helper's own
  root-owned environment (`ZOOMIES_BASE_URL` and `ZOOMIES_REPO`, as
  `SelfUpdate` reads them), never from the request.
* Before anything runs it compares the installed version with the tag itself.
  Equal or newer is answered as done. A build that is not from a release is
  refused, because `CompareBuilds` cannot order a `main-sha-…` build against a
  tag. `SelfUpdate`'s own downgrade guard is not enough: it only skips the
  download, and `zoomies upgrade` would still restart the services and pull
  images.
* It looks for `upgrade.lock` and refuses while one exists, so a manual upgrade
  in progress is not run over. It never takes the lock itself, because the
  `zoomies upgrade` it starts does.
* It keeps its own state (the ids it has seen and the attempts per tag) in a
  root-owned directory outside the update folder, as `zoomies-host-tune` does,
  so the service cannot reset it. At most two attempts per tag and one per ten
  minutes; a repeated id is refused.
* It writes `result.json` through the same root handle, creating a new file and
  renaming it, and never follows a link the service might have planted. A
  request it refuses is answered too, with the reason.

**Then it runs the existing engine, unattended, as a child process:**
`zoomies upgrade --version <tag> --non-interactive`, never `--yes`. It cannot run
in-process, because the engine re-executes itself into the new binary. A
*required* addition stops the run, and the engine's own sentence is the error:
"the upgrade cannot go on without this: …; run `zoomies upgrade --yes`". Optional
additions are skipped, as in any unattended upgrade, and the log tail lists them.

**Two changes to the engine, which manual upgrades get as well:**

* *Pre-flight with the candidate.* Today the binary is replaced first and the
  new release's own layout checks run second, so an unattended run can swap the
  binary and then stop on a change the new release needs. `SelfUpdate` is split
  into fetch (download and verify to a temporary file) and install (replace).
  Between the two the candidate is run as
  `upgrade --check --non-interactive --installed-binary <installed>`, with the
  flags that select the deployment passed through, so it applies its own
  expectations. A failed check leaves the old binary in place and the service
  untouched. `install.sh` already does this for its own path.
* *The previous binary is kept* as `zoomies.previous` beside the installed one,
  so a manual rollback has something to go back to.

**Consent lives on the host.** Nothing creates the helper while the mode is
`off`, and the mode cannot create it. Offering it is a question of its own that
defaults to no, not an item in the upgrade's batch of additions: that batch is
approved by Enter or by `--yes`, and neither is consent to a root unit.

* `zoomies upgrade` asks it, and takes `--update-helper` for an unattended run.
* `zoomies init` and `zoomies agent join` ask it at install, with the same flag,
  and `init` takes an answers-file key.
* `sudo zoomies updates helper install` does it directly.
* `helper remove` and `zoomies uninstall` take it away, with `zoomies.previous`.

A host whose owner did not install it is never updated, whatever the controller's
mode says. The helper is listed in
[What the agent owns on a host](../docs/security.md#what-the-agent-owns-on-a-host),
and that section's lead-in changes with it, because the helper is root-owned and
is not the agent. A container deployment must already mount the shared folder, as
every host that runs runners does. A controller-only container does not, and is
not offered the helper.

**It outlives releases only if a release says so.** The unit calls a stable entry
point, and the request and result carry `"v": 1`. A release that changes either
has to refresh the unit. Nothing in this package does, any more than the
host-health unit is refreshed today.

## 3. Orchestration

### The controller

```mermaid
sequenceDiagram
    autonumber
    participant O as operator or planner
    participant C as controller
    participant S as update folder
    participant H as helper, root
    participant U as zoomies upgrade

    O->>C: update to v1.3.5
    C->>C: check mode, helper and release, then record the attempt
    C->>S: write request.json with an id and the tag
    S-->>H: the path unit fires
    H->>H: read once, validate, refuse anything else
    H->>U: zoomies upgrade --version v1.3.5 --non-interactive
    U->>U: download and verify, then check this deployment with the candidate
    U->>U: replace the binary, restart the service, wait for it to answer
    U-->>H: healthy, or the reason it is not
    H->>S: write result.json with the id, ok and a sentence
    C->>S: the new process reads the result at start
    C->>C: close the attempt and publish updates.updated
```

The old process watches for `result.json` after writing the request, so a
pre-flight failure shows in seconds rather than after a timeout. The new process
reads it at start and closes the attempt. With no result and no change of
version in 90 minutes the attempt is failed, loudly. The engine's own worst case
is fifty minutes, so a shorter limit would fail an update that was still working.

If the new controller never comes up, nothing in the UI can say so. The unit's
journal, `result.json` and `zoomies updates helper status` can, and the previous
binary and the pre-migration copy of the database are where
[upgrading](../docs/upgrading.md#there-is-no-downgrade) says they are.

### A host

```mermaid
sequenceDiagram
    autonumber
    participant C as controller
    participant A as agent
    participant S as update folder on the host
    participant H as helper, root

    C->>C: the planner picks the next host behind the controller
    C-->>A: update_agent with the tag and attempt id, on the agent's own long-poll
    A->>A: validate the tag and refuse a downgrade
    A->>S: write request.json
    A-->>C: task result, request written
    S-->>H: the path unit fires
    H->>H: validate, then run zoomies upgrade --version tag
    H->>S: the upgrade restarts the agent, then result.json is written
    A->>C: heartbeat with the new version and the result
    C->>C: attempt succeeded, the planner moves to the next host
```

* The controller queues `update_agent` only to a host whose agent advertises a
  new `self-update` feature, which an agent does only when its helper is ready,
  and never while the controller is fenced. The task has a lease of its own: a
  kind with none stays in flight, and the queue would refuse to queue it again.
  An older agent fails an unknown task kind harmlessly, and the host card says
  why it cannot update itself.
* Success is the host heartbeating the target version, compared with
  `version.CompareBuilds`, not a task result. A task lost to a restart changes
  nothing, and neither does the controller's own restart, which empties the
  in-memory queue: the next pass sees a host still behind and queues the task
  again, within the attempt limits.
* A failure reaches the controller on the agent's heartbeat, which carries the
  last `result.json`. There is no acknowledgement field and none is needed: the
  agent sends the report until a heartbeat succeeds, and again after a restart,
  and the controller treats a repeat as a no-op.
* A restarting host turns unhealthy after 90 seconds, and its runners are failed
  after five minutes of silence. The restart itself takes seconds, because the
  image pulls come before it, but the planner still counts only the attempt's own
  timeout as failure and never reads short silence as one.

### The rollout

* One host at a time, the one with the fewest active runners first, then by
  name. No cordoning: a restart is safe with jobs running, and the controller
  never touches an operator's cordon.
* The first failure or timeout *halts* the rollout and raises
  `host.update_failed` until an operator resumes or cancels it. A halted rollout
  starts nothing.
* No host update starts while the controller's own attempt is open.
* An embedded agent is updated with the controller, not as a host.
* A host that is deleted mid-rollout has its open attempt closed as `cancelled`,
  so the rollout never waits on a host that is gone.

### The planner

`internal/updates.Decide(snapshot) Plan` is pure, as the scheduler is: no clock
read, no database, no network. The snapshot carries the mode and soak, the
running version, the releases GitHub listed, the helper's state, the open
attempts and rollout, and each host's version, features, health and runner
count. The plan is the actions to take and the sentence the UI shows, for
example "Waiting: v1.3.5 has been public for 6 hours and auto waits for 24; it
can be taken in 18 hours." That is where every decision's reason string comes
from, as in the scheduler.

It is level-triggered. Each pass recomputes from rows, so a missed event costs
one pass and a restart loses nothing. It runs on its own loop with a one-slot
wake channel, outside `reconcileMu`, as the machine loop and the automatic pool
reconciler do, and every action it takes first checks `mayAct`.

### What is stored

Two small tables, written only by `internal/store`:

* `update_attempts`: id (`store.NewID` with the prefix `upd`), scope
  (`controller` or `host`), host, from_version, to_version, trigger (`manual` or
  `auto`), requested_by, state (`requested`, `succeeded`, `failed`, `timed_out`,
  `cancelled`), error, requested_at, finished_at. A partial unique index allows
  one `requested` row per target, so a button and the planner cannot both open
  one. The host column has no foreign key: a re-join deletes and recreates the
  host row, and a cascade would erase the history it belongs to.
* `update_rollouts`: id (prefix `rol`), target, trigger, state (`running`,
  `halted`, `done`, `cancelled`), started_by, halted_reason, started_at,
  finished_at.

A new `retention.update_attempts` key, 90 days by default, prunes them, and
spares every open row. After a failure the planner waits 30 minutes, and after
two failures of one tag it waits for an operator.

## 4. Surfaces

**REST first** (rule 4). The UI and the CLI are clients of it.

| Route | Role | Does |
| --- | --- | --- |
| `GET /api/v1/updates` | viewer | The status: mode, soak, running and eligible release, the helper, the open attempt and rollout, and the planner's sentence. Text a helper wrote is withheld below `platform`. |
| `POST /api/v1/updates/check` | admin | Ask GitHub now; at most once a minute; refused when `check_interval` is `0`. |
| `POST /api/v1/updates/controller` | platform | Update the controller to the newest eligible release, or to `tag`. |
| `POST /api/v1/updates/hosts` | admin | Start a rollout of every host behind the controller, or of `host_ids`. |
| `POST /api/v1/updates/rollout/resume` | admin | Resume a halted rollout. |
| `DELETE /api/v1/updates/rollout` | admin | Cancel it. |
| `POST /api/v1/hosts/{id}/update` | admin | Update one host. |

Refusals carry a stable code: `update.mode_off`, `update.check_disabled`,
`update.helper_missing`, `update.in_progress`, `update.not_a_release`,
`update.nothing_newer`, `update.host_cannot_update`, `update.rollout_halted`. The
envelope's `code` is a closed enum that the UI switches on, so these are added to
it, as `limit_reached` was, rather than carried in another field.

* **Events.** `updates.updated` carries the status in the `GET` shape. Below
  `platform` the text a helper wrote is withheld, in the `GET` and in the stream
  alike, so the stream filters per subscriber, as it does for problems. A host's
  `update` block (state, whether it can update, the reason, the attempt) rides on
  `host.updated`, beside the `upgrade_command` that stays as the fallback. It
  holds nothing that changes with every heartbeat, or every card would repaint on
  every beat.
* **Problems**, each with its row in `docs/problem-codes.md`:
  `controller.update_available` (exists), `controller.update_helper_missing` and
  `controller.update_failed` for the `platform` audience, and
  `host.update_failed` and an info `host.update_unavailable` for a host that is
  behind and cannot update itself, for the fleet's. The host codes are exempt
  from the public status page, or a failed host update would turn it degraded.
  None of them carries a `Remedy`: the autopilot applies every remedy a problem
  carries, and an update must never be one it applies.
* **Audit.** A row for every request, resume and cancel. The planner's carry the
  system identity as `system:auto-update`.
* **CLI.** `zoomies updates status | check | apply | resume | cancel` and
  `zoomies updates helper install | remove | status`, which are clients of the
  routes. `helper run` is what the unit calls. `zoomies upgrade` is untouched and
  is still the engine.
* **UI.** A **Settings → Updates** section: the mode with a sentence of
  consequence under each choice, the soak, the running and eligible release, the
  helper's state with the install command to copy, and the last result. The
  existing notice links to it; a problem can carry no button, because the only
  action a problem has is a remedy. On **Hosts**, an Update button on each card
  and one for every host that is behind, with the copied command kept beneath as
  the fallback. An open tab already picks up the new build on its
  next navigation (`web/src/lib/state/upgrade.svelte.ts`).
* **MCP.** Nothing in this package. An assistant does not get a tool that
  restarts the controller, and `update_settings` may not set `updates.mode`: its
  allowlist names the whole `updates.` section today, and narrows to
  `updates.check_interval`.
* **Metrics.** Attempts by `kind` (`controller` or `host`) and `result`, and
  whether an eligible release is waiting. Names follow `docs/metrics.md`; the
  metrics test allows a fixed set of labels, which has `kind` and not `scope`.

## 5. When it goes wrong

| What goes wrong | What happens | What the operator sees |
| --- | --- | --- |
| No helper on the host | The button is disabled | The reason, and the install command |
| The helper refuses the request | Nothing runs | `result.json` says why; the attempt fails |
| The download or checksum fails | `zoomies upgrade` stops before replacing anything | The attempt fails with the message |
| The candidate fails its pre-flight, or needs a required addition | The old binary stays and the service is not restarted | "Needs an operator", with the command |
| An optional addition is missing | The upgrade finishes without it | The log tail lists it |
| Something else holds `upgrade.lock` | The helper refuses before anything runs | The attempt fails; the planner retries after its wait |
| The controller is fenced or has lost its lease | Every action refuses | The existing fence message |
| The new controller does not come up | The agents keep working | Only the host can say; see section 3 |
| A host is not back at the target version in 90 minutes | The attempt times out | `host.update_failed`; the rollout halts |
| The controller restarts mid-rollout | The planner reads the rows and carries on | Nothing |
| GitHub is unreachable | The check keeps its last answer | An update attempt fails at the download |

## 6. Security

| Threat | How it is bounded |
| --- | --- |
| A compromised controller asks root for something | It can name a tag in a fixed shape, never a URL, path or flag. The source is the helper's own environment, and a downgrade is a no-op. At most two attempts per tag and one per ten minutes. The worst it can do is move a host to a published release, which an administrator could do with the button. |
| A forged or compromised release | Trusted as far as `zoomies upgrade` is today: a sha256 from the same release, over TLS. Unattended, that is a larger bet. The soak and the completeness rule are this package's mitigations. Each release already carries a provenance attestation that nothing checks inside the product; verifying it, or a signature, is the recorded follow-up. |
| A planted symlink, or an oversized or swapped file | `os.OpenRoot`, one descriptor, a size limit, an owner check, known fields only. `result.json` is created through the same handle. |
| A replayed request | The ids seen are kept in state the service cannot write. |
| A forged `helper.json` or `result.json` | The service can write the folder, so it can forge both. They decide only what the UI shows, and nothing the helper does depends on them. On a native install the pointer to the folder is root-written, in a directory the unit's sandbox keeps read-only. |
| An assistant switching updating on | `update_settings` may not write `updates.mode`, so a connected assistant cannot enable unattended updating. |
| A host that did not agree | The helper exists only where someone on the host installed it. |
| A runaway loop | The helper's limits, the planner's wait, and a halted rollout. |

## 7. Testing

Each behavioural test is run once against the code with its rule removed and
seen to fail (rule 14).

* `internal/updates`: table tests for the soak edges, the `v1.3.1` then `v1.3.2`
  sequence (the first is never taken), an incomplete release, the uppercase tag,
  a prerelease, a non-release build, the mode gates, one at a time, idle first,
  halt, timeout, resume, an embedded host, an ahead agent and a missing helper.
* `internal/installer`: request validation (a symlink, a directory, an oversized
  file, the wrong owner, an unknown field, a bad tag, a repeated id, the rate
  limit); unit rendering; the helper offered with consent and skipped
  unattended; a failed pre-flight leaving the binary; `zoomies.previous` kept.
* `internal/controller`: a fake release list and an injected clock; the attempt
  lifecycle, including recovery after a restart; the event equal to the `GET`
  shape.
* `internal/agent`: the task validated, the feature advertised only with a ready
  helper, the request written, the result carried on the heartbeat.
* `internal/api`: the role matrix and every error code.
* `internal/mcp`: `update_settings` refuses `updates.mode`.
* `internal/docs`: the problem-code rows, the new fact in `agent_owns_test.go`,
  the settings table.
* `test/drill`: an update with a job running keeps the job, and a controller
  restart mid-rollout carries on. `test/upgrade`: the helper driven with a fake
  `systemctl`.
* `web/tests`: Playwright, desktop and mobile with the accessibility pass, for
  Settings → Updates and the host button. The binary under test is a `dev` build,
  which no release comparison accepts, so a seed supplies a release list, a ready
  helper and a running version.

## 8. Delivery

Six pull requests, in this order, each shippable alone because the mode defaults
to `off`. Each carries its own API, client and documentation changes (rule 6)
and its progress-row update. The last adds the drills and the narrative page.

| # | Behaviour | Exit criterion |
| --- | --- | --- |
| 1 | The mode and soak are stored; the release list and the eligible release are computed and shown, with `internal/updates` starting as the pure eligibility rule; the status route and a read-only panel; the MCP allowlist narrowed | A mode is stored and shown and nothing acts; the soak edge is a table test |
| 2 | The helper: request and result, the units, install and remove, the layout offer, the pre-flight, `zoomies.previous` | A request makes an upgrade run against a fake `systemctl`; every refusal has a test |
| 3 | Controller update from the button | An attempt is recorded and closed across a restart; the failure problem carries the helper's sentence |
| 4 | Host update: the feature, the task, the heartbeat result, the route and the button | A host moves to the controller's release with a job running on it, and the job survives |
| 5 | `auto`: the planner, the rollout, the loop, halt and resume | The planner's tables hold; a halted rollout starts nothing; the soak holds a release back |
| 6 | Drills, `docs/upgrading.md` and the screenshots | The drills run in CI; the page says what each mode does |

## 9. Not in this package

| Left out | Why | Revisit when |
| --- | --- | --- |
| A maintenance window | The registry has no schedule type, and the soak covers the commonest reason for one | Someone asks |
| A per-host hold | Not installing the helper on a host is already one | A host must be pinned and still be managed |
| Automatic rollback | Migrations are one way; `zoomies.previous` and the pre-migration copy make a manual one possible | It is its own package: `zoomies upgrade --rollback` |
| Verifying the release's attestation or a signature | A new dependency or a signing key, and a decision about which | Before `auto` is recommended to anyone |
| Updating several hosts at once | One at a time is the safe default, and each upgrade also pulls runner images | A fleet is large enough that a rollout takes too long |
| Separate modes for the controller and the hosts | One selector is what was asked; `updates.hosts` would be additive | Someone wants hosts to follow a controller they update by hand |
| macOS, Windows, PaaS | A launchd helper needs `WatchPaths`, `SelfUpdate` skips Windows, and a PaaS redeploys itself | A fleet runs one and asks. They show as manual, with the reason |
| Dev builds | A build from `main` is tracked on purpose | Not planned |
| Controller-only container deployments | They do not mount the shared folder, and every mount path is gated on running runners | Someone runs one and asks |
| Rootless and user-namespace-remapped container hosts | The owner check would see another uid and fail closed | A fleet runs one and asks |
| Refreshing an installed helper unit | The host-health unit is not refreshed either, and nothing in the request or result has changed | A release changes either |
| Serving binaries to air-gapped hosts | Those fleets set `check_interval: 0` and stay manual | Someone with an air-gapped fleet asks |

## 10. Open points for the plan

* The CLI group name. `zoomies updates` sits next to the `update` alias for
  `upgrade`; `zoomies release` is the alternative. Recommend `updates`, for
  parity with the `/updates` routes, and a help text that says which is which.
* The helper's exact hardening list. Its own state lives in
  `/var/lib/zoomies-update/`, mirroring `zoomies-host-tune`.
* How many releases to list. Ten covers the last month at the current cadence,
  and one request a day is a small share of the unauthenticated rate limit.
* The Settings → Updates layout, which wants its own pass against
  `docs/ui-guidelines.md`.
