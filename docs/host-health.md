---
icon: material/stethoscope
title: Host health checks and tuning
description: Read host OS health reports, approve individual tuning changes, and restore recorded settings with Zoomies doctor and tune.
---

# Host health checks and tuning

`zoomies doctor` gives a short host-health summary and up to three findings
that need attention. `--verbose` shows every check, its current value,
recommendation and explanation. Both wrap to the terminal width. Doctor is
read-only by default and finishes without a tuning prompt. Use `--interactive`
to review eligible fixes individually, or run `sudo zoomies tune` separately.
Ubuntu 24.04, Ubuntu 26.04 and Debian 13 are the supported tuning platforms. Other
Linux distributions are report-only; on a non-Linux host the checks are skipped
and the page says that Zoomies does not tune it. A
refusal names the distribution and release the host reported, so you can tell an
unsupported release from an unsupported distribution.

```sh
zoomies doctor
zoomies doctor --verbose
zoomies doctor --json
zoomies doctor --tier aggressive
sudo zoomies doctor --interactive
sudo zoomies tune --dry-run
sudo zoomies tune
sudo zoomies tune --revert
```

Doctor exits **0** without warnings, **1** with warnings, and **2** when a check
errors, counting every check in the tier you ran: `--tier aggressive` exits 1 on
an aggressive warning. Skipped checks explain the missing access or host
capability. Doctor works without root; checks whose inputs cannot be read are
skipped rather than silently assumed healthy. `--interactive` shows the report
and offers eligible fixes individually, showing each file change and command
before `[y/N]` consent. It cannot be combined with `--json`, `--watch` or a
remote host selection.

## Continuous reporting uses the installed host binary

A native agent collects read-only OS observations in its existing daemon. For
an installed Docker or Compose deployment, **the native binary on the host**
runs `zoomies-host-health.service`. This supplies the full OS report to the
container through the existing shared directory. There is no privileged
container mount and no incoming connection to an agent.

The service samples every minute. Agent heartbeats carry the latest report to
the controller, which persists it and sends `host.updated` events. Host badges
and the host detail checks table update without reloading the page.

There are two staleness thresholds, for two different jobs. The host page greys
a badge once its report is more than three minutes old, or whenever the host is
unreachable: that is a display, and it changes nothing else. The badge says
which of the two it is. A host that is still connected with an old report reads
**Report stale**, and its hint says to restart the agent and check the host's
clock. A host that is not connected reads **Last known report**, and its hint
says the report is the last one it sent, not the machine's state now. Both are
neutral and both sit under the **Report stale** filter. Neither label says
"reboot pending": the flag may have cleared since, so the hint says that the
report said one was pending, and the check stays in the list on the host's
page. Zoomies raises the `host.health_stale` problem only after **ten** minutes
without a newer report from a host that is still sending heartbeats, so a
collector that is merely slow does not raise a problem. See
[What a host's report raises](#what-a-hosts-report-raises).

The host page says how old the report is under its title: **Report checked 40s
ago**, with what that means for this host. A connected host with an old report is
told it has sent nothing newer, a host that is not connected is told nothing newer
can arrive until it is, and a container's report is said to come from inside the
container. The line gives no interval, because the page cannot see how often an
agent sends a report. **Refresh** (or `R`) re-reads the host from the controller;
it does not ask the agent to check again, since Zoomies never reaches into a host
and the page already updates by itself when a newer report arrives. **Check now**
is the button that asks; see [Check now](#check-now).

When a newer report clears a check or makes one need attention, the page says so in
two places. A polite live region announces it to a screen reader, once per report,
and a **Changed since you opened this page** list under the report keeps the last
five changes with their times. The report on screen when you open the page is the
baseline and announces nothing, so does a report that is no newer than the last,
and so does one from a different build of the host or a container's partial one.
Only counted checks are compared, as the badge counts them, so the list cannot
disagree with it. A check that was skipped is not called OK. There is no toast: the
page changing is the notice.

The controller counts each report once, and every surface reads that count. It
is `doctor.summary`, beside `doctor.results` in the host's payload: the host's
badge, the problems, the metrics, the Overview feed and `zoomies hosts list` all
show it, so they cannot disagree about a host. Five integers: how many checks
count, and how many of those are warnings, errors or skipped, plus a separate
count of suggestions.

What counts is what `zoomies doctor` counts by default: the safe tier, without
optional suggestions. The monitor reports the aggressive and dedicated tiers as
well, but those are choices rather than faults, so the host page shows them as
suggestions, the summary counts them as `suggestions` and nowhere else, and
they never raise a problem. A stock Ubuntu host has `vm.swappiness=60` from its
first minute; that is a suggestion for ever and not a warning. The summary is
worked out from the results on every read. It is never stored, and an agent
cannot send one: a heartbeat whose report carries a `summary` has it ignored.

A pending reboot is counted **once**, as itself. The engine sets
`reboot_pending` from the `kernel.pending` warning, so a report carries the one
fact twice; the summary leaves the warning out and keeps the flag. A host whose
only finding is a reboot therefore reads **Reboot pending**, not "1 warning",
and raises `host.reboot_pending`, not `host.os_health`. `zoomies doctor` does
not make that exception, on purpose: it counts every check in the tier you ran,
exit code included, so on that host it counts the kernel as a warning, one more
than the badge does, and prints the reboot hint beside it. The `kernel.pending`
row stays in the checks table either way.

When more than one thing applies the badge names the worst first: health errors,
then warnings, then a pending reboot. A report that is only the container's
partial view reads **Partial report** and is given no verdict, as below. So does
a native report that skipped half or more of its counted checks (7 of the 14 or
more) and has no finding and no pending reboot: it says how many checks could not
run, because a green "Health OK" would vouch for checks nobody made. A healthy
Linux host skips between one and five. A stopped Docker daemon or an unreadable
work directory skips several at once. If there are findings or a reboot, the badge
names them and the hint adds how many checks could not run. The host page opens with a **Needs attention** list of the counted findings, with
the failing checks above the ones that pass.

A flagged row in the checks tables says what a person can do about it, in a word
beside its **Warning** or **Suggestion** badge. **Suggestion** still means the
row does not count towards the host's health, and the word beside it does not
change that. **Fixable** is whatever the report marks `actionable`, which needs
the reporting process to be root, so on a native host whose agent runs as the
service user most rows read **Advice** instead. **Advice** is neutral: it means
the report does not offer the row for `zoomies tune`, not that nothing can be
done about it. **Optional** is a trade-off to read about first, and it wins over
Fixable. A host whose report is read-only, a container or a non-Linux machine,
shows no word at all, because nothing on it can be tuned from here.

An operator or administrator also gets a **Copy preview command for** button on
each Fixable row, named for the check's id. It copies text for a person to run on
the host, for example `sudo zoomies tune --only inotify.watches --dry-run`. An
aggressive row adds `--tier aggressive` and a dedicated one adds `--dedicated`,
because `tune` refuses either without its flag. The dry run prints each file
before and after and each command, writes nothing, asks nothing, and still needs
root. Zoomies never runs it: the controller does not dial the host, and the host's
name and the check's title are never part of what is copied. A check id that does
not look like `family.name` gets no button. A viewer sees the word and no button.

For an operator the host page opens one step earlier, with a **Next step**
panel, whenever the host has a counted finding or a pending reboot, and also
while the host is cordoned, so that a host brought back from its reboot and left
cordoned still has its way back. It shows how many runners are on the host,
whether the host is cordoned, and **Cordon this host** or **Uncordon this host**.
Once the host is connected, cordoned and has no runner on it, it says **Idle and
cordoned: safe to reboot now**. A cordoned host with nothing waiting on it says
so, offers only **Uncordon this host** and has no command to run. Otherwise a
**Copy command** button copies `sudo zoomies doctor --interactive`, under a line
naming the host and its address so that you know which machine to run it on; the
host's name is never part of what is copied. A runner kept warm for a pool
counts as a runner and never finishes by itself, so a count above zero does not
mean a job is running: the panel says so and links to the host's runners that
are busy. A host that is not connected is never called safe, because the count
the controller holds for it is the last one it heard. A viewer does not see the
panel; the report tells them what to ask an operator for.
Tuning and reversal refresh the shared report immediately where it exists;
the next heartbeat carries that result.

| Deployment | Collector | Observation file |
| --- | --- | --- |
| Native controller/agent | Existing agent daemon | `<agent.work_dir>/host-doctor.json` |
| Docker/Compose installed on a systemd host | `zoomies-host-health.service` using the native binary | `/var/lib/zoomies/shared/host-health/report.json` |

A manually deployed container without the host collector reports only checks
it can inspect. Its report says it is partial. The host-installed binary is
still available for `zoomies doctor`. For a continuously published report you
can run it under your supervisor:

```sh
sudo zoomies doctor --watch --tier dedicated \
  --work-dir /actual/host/path/to/runner/work \
  --report-file /var/lib/zoomies/shared/host-health/report.json
```

The installer creates the root-owned `host-health` directory before starting
the service. Publishing a report writes observation data only; the service
cannot tune, reboot or restart Docker. Native agents on remote hosts send the
same data through their existing outbound connection. From the controller CLI:

```sh
zoomies doctor --host host_example --url https://zoomies.example.com
```

This reads the latest report, including its timestamp. It does not execute a
remote root command. Apply fixes locally on that host. The web UI is read-only
for OS tuning. On a host card the health badge is a link, 24 pixels high and ending
in a chevron, and under it one line names the worst checks (`File watches, Docker log
rotation and 1 more`), in the order the host page lists them. The link opens the host
page at the first of those checks. No line is shown for a stale report, a container's
partial report or a host whose only finding is a reboot, so a card never lists
last-known checks as if they were current. The host
page can cordon the host, which is a scheduling action on the controller's side
and changes nothing on the machine, and Zoomies never runs a command on a host:
the command it offers is for you to run there.

A report that cannot be acted on says so. For a non-Linux host the page's subtitle
and its Safe tier description say that Zoomies does not change the host, and it
offers no command to review changes. For an unsupported distribution or a
container it names `sudo zoomies doctor` as the way to read the checks (on the
host itself, with the native binary, for a container) and says that `zoomies tune`
will not change it, and the **Next step** panel keeps its cordon and reboot advice
without the copyable doctor command. An unsupported distribution's own
`environment` warning does not open that panel by itself; it stays in **Needs
attention**, where the controller counts it.

The command needs the controller's address and, unless authentication is off, a
token, and you should not have to find either first. The host's page has a panel,
**Read this report from a terminal**, with the command already written: the
controller's address in it and, when you press **Make a command**, a token made
on the spot. That token is yours, can only read hosts (`hosts:read`), and stops
working after 15 minutes; it is shown once, and the command goes from the page
when it ends. It is listed under your API tokens as `Terminal: doctor <host-id>`
until you delete the expired ones. On a controller with authentication off the
command needs no token, and the panel says so. On the host itself none of this is
needed: `zoomies doctor --verbose` reads it directly.

## A host with no report

A host that has never sent an OS report gets a sentence chosen from what the page
already knows, because the causes need different actions:

| What the page sees | What it says |
| --- | --- |
| The host is not connected | No report can reach the controller until the agent is back; check it is running and can reach the controller, and when its last heartbeat was. |
| The agent is an earlier release than the controller, or a different build | It **may be** too old to send OS reports. An operator gets the controller's own upgrade command to copy, and a viewer is told to ask one. The page cannot detect the age, only the mismatch, so it never says "is". |
| The host joined in the last five minutes | The first report is on its way and the page fills in by itself. |
| The controller's own agent | It is the same program as the controller, so updating it cannot help; the page says it cannot tell why. |
| The agent names no release | Zoomies cannot tell whether it is new enough, and offers no command, since it does not know which release to give. |
| Anything else | Zoomies cannot tell why, and offers the read-only `sudo zoomies doctor` to run on the host, to everyone. |

The only command taken from the controller is the upgrade command it made itself,
copied exactly; nothing is built from a host's name or anything else an agent
wrote. Zoomies never runs either command for you. A container install without the
host health service does not land here: it sends a partial report and reads
**Partial report** (see [what it cannot see](#what-it-cannot-see)).

## Check now

**Check now** asks the host's agent to run its OS checks once and send the report
back, instead of waiting for the next periodic one. An operator presses it on the
host page; a viewer sees one sentence saying the operator role is needed. The same
request is `POST /api/v1/hosts/{id}/health-check`, with the scope `hosts:check`
(see [the API](api-surface.md)). It is in the REST API and on the host page only:
there is no `--now` on the CLI and no MCP tool.

The controller still never dials an agent. It queues a `check_host` task, and the
agent takes it on the long poll it already holds open. The agent runs the checks
through the same monitor that makes its periodic report, sends the report in the
task's result, and the controller ingests it exactly as it would a heartbeat's. The
page shows **Checked just now** and any changed findings by changing, as it does
for every report. A result is not a heartbeat: it records that a report arrived and
nothing else, so a wedged heartbeat loop cannot look alive because someone pressed
the button.

**The check is read-only on the host.** It runs the same detection the periodic
report does, and the only thing it writes is the monitor's own cache file. Nothing
on the host is tuned, installed or restarted.

### Which installs can answer

| Install | Answers? | Why |
| --- | --- | --- |
| Native systemd agent | Yes | Its monitor reads the host itself. |
| Bare-process agent | Yes | The same. |
| A natively installed controller's own agent | Yes | It is the same program, on the same host. |
| Docker or Compose, **including** Compose with `zoomies-host-health.service` | No | The container only re-reads a file the host service wrote on its schedule; it has no way to ask that service for another, and a trigger file for a root unit would be a separate security design. |
| Any agent on an operating system other than Linux | No | The checks are Linux's. |
| An agent older than this release | No | It does not know the task, so the controller never sends it. |

Zoomies decides this from the flag the agent advertises (`host-check`), never from
what the report says. A Compose host's service reports `container=false`, because
the report is the host's own, so the report cannot tell you whether the *agent*
can be asked. The agent advertises the flag only when it runs natively on Linux.

A host that cannot answer shows the button off, with the reason in a line beside
it rather than in a tooltip. So does a host that is not connected and one whose
agent is incompatible; the last report stays on screen, marked with its age.

### How often, and what it costs

A host can be asked once every 15 seconds. The window counts from the request, is
held in the controller's memory and is not cleared when a check fails, so a token
in a loop cannot hammer an agent that fails quickly. A repeat press inside it
changes nothing, and the page counts down. A request that is still waiting is not
queued twice. If the agent does not take the task within 20 seconds the controller
drops it and the page says so; if it takes it and does not answer, the page says
that after a minute. A late answer is still used, since it is fresh read-only data.

Treat the cost below as a floor. In container sandboxes where 26 of the 30
checks skip, one run took about 0.3 to 0.9 seconds of wall time (2.3 seconds
cold), about 0.6 seconds of CPU and 21 to 40 process executions. Peak memory was
not measured. A real host with a live Docker daemon runs checks those sandboxes
skipped, so it costs more; no figure for one has been taken. The 15 second window
is a judgement, not a measurement.

An unchanged report still moves **Checked**. The page then says "Nothing has
changed since the last report", the controller records that a report arrived
without rewriting the stored body, and the host reads as checked just now.

When a report arrives, the controller stores its body only if it says something
the stored one does not, or at least every five minutes, and records that a report
arrived in the heartbeat's own write. A report that says nothing new therefore
costs no rewrite of the stored report and no frame of its own, and the host still
reads as checked just now. Free-space figures move on every report and do not
count as news while their verdict holds, so the disk figure on the host page can be
up to five minutes older than the **Checked** time beside it. **Check
now** publishes the host itself where the periodic path would not, because
someone is waiting for the answer.

## What a host's report raises

A report is not only something to open a page and look at. The controller reads
the count described above and tells you about a host the way it tells you about
everything else that needs attention. The controller never changes a host's
operating system, so none of these carries a one-click fix. A change is made on
the host itself, with your consent, by `zoomies doctor --interactive` or
`zoomies tune`.

| Problem | Severity | Raised when |
| --- | --- | --- |
| `host.os_health` | **Error when a counted check could not run, warning otherwise** | A connected host's latest report has a counted warning or error. One entry per host, naming up to three of the checks and how many more. |
| `host.health_stale` | Warning | A connected host's newest report is more than ten minutes old by its own timestamp. It clears with the next report. |
| `host.reboot_pending` | Info | The report says an update is waiting for a reboot. The entry says whether there is a runner on the host: if not, it can be rebooted now. If there is, cordon the host and reboot it when `zoomies runners list --host <host-id> --state busy` shows nothing, because a runner kept warm for a pool never finishes by itself. |

A warning is a setting below the recommendation, and a nearly full disk is one.
An error is a check that could not run properly: a `/etc/docker/daemon.json`
that is not valid JSON, `df` output it could not read, or a read that failed
unexpectedly. That is why only errors raise the problem's severity. The
aggressive and dedicated tiers and anything optional are suggestions, and never
raise anything. Each entry's fix is a sentence, not a button. On the host, run
`sudo zoomies doctor --verbose` to see what every check found and
`sudo zoomies doctor --interactive` to be offered each fix it can make, with the
change shown first; from anywhere else, the host's page has the command to read
the report, with a short-lived token already in it. Not every counted warning has a fix to
offer: the checks marked report only or advice only in the safe table below,
the kernel checks and the distribution check (`environment`) clear only when
you change the machine yourself, and they count all the same. A reboot is only
ever a note, because the host keeps working until it restarts. Zoomies never
reboots a host itself.

Nothing is raised for:

* a host that has sent no report, whether it is an older agent or has not
  reported yet;
* a host that is offline. `host.unhealthy` already speaks for it, and its last
  report describes a machine nobody can see;
* a container's partial report. It skips most checks and warns about the
  image's distribution for ever, so Zoomies draws no conclusion from it. See
  [what it cannot see](#what-it-cannot-see).

A stale report does not silence `host.os_health` or `host.reboot_pending`. A
collector that has stopped does not fix a setting, and going quiet would turn
"I can no longer see this host" into "all clear". None of the three reaches the
public status page: they say whether a machine's settings match a
recommendation, not whether a job will run, and a stock fleet carries some of
them for ever.

The same count reaches the other places you look:

* **Metrics.** `zoomies_host_os_checks` (by `host` and `state`),
  `zoomies_host_reboot_pending` and `zoomies_host_health_report_age_seconds`.
  They are counts and never name a check. Each is absent for a host the
  problems are not allowed to judge: one that is offline, has sent no report or
  sent only a container's partial one. A host with a clean report shows zeroes,
  so a missing series is never an all-clear. On a native agent the age is how
  an alert notices a collector that has stopped. On a Docker or Compose
  installation the series disappears instead, so alert on its absence too (see
  [what it cannot see](#what-it-cannot-see)). See [Metrics](metrics.md) for
  what to alert on.
* **The Overview feed.** A line when a host starts to need attention, when it
  has a health error, when it no longer needs attention, and when it is waiting
  for a reboot. Only a change is news: a host's first report is a baseline and
  says nothing, a refreshed report with the same verdict says nothing, and an
  error easing to a warning says nothing, because the host already needs
  attention. A stopped collector has no line of its own; `host.health_stale`
  reaches you as a problem.
* **`zoomies hosts list`.** An `os health` column: `ok`, `2 warnings`,
  `1 error, 2 warnings`, `reboot pending`, `unavailable` when every counted
  check was skipped, `partial`, `stale 14m`, or `-` for a host that is
  unreachable or has sent no report. See [the CLI](cli.md).
* **Assistants.** MCP's `list_hosts` tells the assistant to read
  `doctor.summary` before `doctor.results`, and `list_problems` returns the
  three problems like any other. `list_hosts` takes `include_checks=false` to
  leave the results out when the question is about capacity, and `host_health`
  returns one host's report in full.

### What it cannot see

A stopped `zoomies-host-health.service` on a Docker or Compose installation is
**not detected**. The container reads the host's report from the shared
directory, but only while it is less than three minutes old. When the service
stops, the container falls back to a report of its own, made afresh every
minute, which is partial and is marked as one. A fresh partial report is not a
stale one, so `host.health_stale` is never raised, and the OS metrics
disappear rather than climb. The only signs are the **Partial
report** badge and `partial` in `zoomies hosts list`. On those installations,
check the service on the host with `systemctl status zoomies-host-health.service`
if either appears where you expected a full report.

A native agent is different. Its report is the host's own, so a monitor that
stalls, or a clock that runs behind, shows as `host.health_stale`.

## Accepting a check as deliberate

Some warnings are a choice. A host that keeps `relatime` on purpose would otherwise
read "2 warnings" for ever, which is how a page teaches people to stop looking at
it. An operator can accept one counted warning on one host as deliberate, and the
rest of the fleet is unaffected: there is no fleet-wide accept and no bulk, so
thirty hosts that all keep the same setting take thirty decisions.

On a warning's row the page offers **Accept**. It asks for a reason of 10 to 500
characters and an end date, from 7 days to a year, with a year as the default.
Every acceptance ends: there is no way to accept a check for ever.

An accepted check:

* keeps its row, after the unaccepted findings, with an **Accepted** badge, who
  decided, when it ends and the reason, so nobody has to wonder why a host is green;
* leaves **Needs attention**, the counts, `host.os_health`, the pill and the
  warning state of `zoomies_host_os_checks`, and is counted as `state="accepted"`
  instead, so the silence is visible. The pill still reads Health OK and says how
  many accepted checks do not count towards it;
* is listed by `zoomies hosts list` as, for example, `ok, 2 accepted`, and
  `zoomies doctor --host` puts it in its own **Accepted** section, so the sections
  add up to the summary line.

An acceptance ends by itself, and the row counts again with a note saying why, when:

* the check no longer reads what it read when you accepted it (the controller keeps
  that text and compares it with each report);
* its end date passes; or
* the check becomes an error, because it could not run.

If the value comes back to what was accepted inside the term, the acceptance holds
again. **Revoke** ends one by hand, and counts the check again straight away.

Only a counted warning on a check this build has can be accepted. These are
refused, each with the reason:

* an error or a skipped check, because there is nothing deliberate to accept;
* a suggestion from a tier that does not count;
* `kernel.pending`, because a pending reboot ends when the host reboots;
* `disk.space` and `disk.inodes`, because free space moves, and an acceptance could
  not tell 12% free from 2%.

Accepting changes only what Zoomies counts. The controller never runs a command on
a host, so `zoomies doctor` and `zoomies tune` on the host know nothing of it and
still list the check. Accepting needs the operator role (the `hosts:accept` token
scope), both decisions are audited with the reason, and an assistant cannot do it:
there is no MCP tool, so a prompt-injected sentence cannot silence an alarm.

## Who can read the check detail

Anyone who can read hosts can read the check detail: any signed-in viewer, or
an API token with one of the scopes below. There is no separate permission for
it and no setting that narrows it. The detail is `doctor.results`, which gives
each check's current value, recommendation and reason, so it includes the
running kernel's exact version, filesystem paths and which services are
enabled. It reaches a reader by three routes, each opened by its own scope (a
scope on a resource, such as `hosts:cordon`, includes reading it):

* `hosts:read`, over the REST API in `GET /api/v1/hosts` and
  `GET /api/v1/hosts/{id}`, and through MCP's `list_hosts` and
  `zoomies doctor --host`, which use the caller's own token and so can never
  show more than that token could;
* `runners:read`, in the host that `GET /api/v1/runners/{id}` carries beside
  the runner;
* `events:read`, on the event stream, in `host.updated`. It is also a viewer
  permission, and a token scoped to it alone sees the same frames.

The problems show less, by a fourth scope. `stats:read` opens
`GET /api/v1/problems` and MCP's `list_problems`, and the same list arrives in
`problems.updated` on the event stream. A `host.os_health` entry in it names up
to three of a host's failing checks by title, and says which could not run,
with no value. So a token that must not see any of it needs none of
`hosts:read`, `runners:read`, `events:read` or `stats:read`.

An accepted check adds one more thing to the detail: the name of the operator who
accepted it and the reason they gave. That text is written by a person, not the
host, and everyone who can read the host can read it, so write it for them and put
no secret in it. It is never copied into a problem, a metric or the status page.

The support bundle carries it for admins only. The public status page never
carries it, and `/metrics` carries counts and no check's name. With
`metrics.public` on, those counts are readable by anyone who can reach the
endpoint.

On a fleet that several teams share, the detail is a map of what is unpatched,
so give the viewer role, and tokens with any of those scopes, accordingly. The
text in the results is written by the host, so give it to a model as data and
not as instructions.

## Safe checks

| Check ID | Check and recommendation | Files or units tune can change |
| --- | --- | --- |
| `inotify.watches` | At least 524,288 file watches | `fs.inotify.max_user_watches` in `/etc/sysctl.d/90-zoomies.conf`; matching live sysctl |
| `inotify.instances` | At least 1,024 watch instances | `fs.inotify.max_user_instances` in the same drop-in; matching live sysctl |
| `files.maximum` | At least 2,097,152 system file descriptors | `fs.file-max` in the same drop-in; matching live sysctl |
| `files.service` | Native Zoomies service `LimitNOFILE` at least 65,536 | `/etc/systemd/system/zoomies-agent.service.d/90-zoomies.conf` or `zoomies.service.d/90-zoomies.conf`; `systemctl daemon-reload` |
| `docker.logs` | JSON log rotation, normally `max-size=10m`, `max-file=3` | Merge only `/etc/docker/daemon.json`; preserve unrelated settings and back up the original |
| `docker.storage` | Docker storage driver `overlay2` | Report only |
| `cgroup.version` | Control groups version 2 | Report only |
| `work.filesystem` | Local ext4/XFS and `noatime` | Report and advice only |
| `docker.filesystem` | Docker root on local ext4/XFS and `noatime` | Report and advice only |
| `disk.space` | At least 10% and 10 GiB free in the work filesystem | Report only |
| `disk.inodes` | At least 10% free inodes | Report only |

The free-space figure on the host's card measures the same filesystem as `disk.space`, and goes amber by the same rule (under 10% or under 10 GiB). The card follows each heartbeat and the check runs about once a minute, so the two can differ by a minute or two.

Existing sysctl values that meet the recommendation are left alone. A setting
found in another `sysctl.d` file or `/etc/sysctl.conf`, or a configuration
management marker, blocks competing changes. Rootless and custom Docker
endpoints are reported separately; their policy is not written to a host daemon
configuration that may not control them. Container/LXC and read-only kernel
settings are skipped with a reason.

Service file limits take effect at the next service start. Docker log settings
need a Docker restart and affect **newly created containers**, not existing
ones. Zoomies asks before restarting Docker (or accepts your explicit `--yes`),
and refuses when containers run or an active native agent could admit new work.
Drain and stop the agent first, or let Zoomies do it with a maintenance restart.

### Maintenance restart: `--force`

On a host that is never quiet, a restart left pending is a restart that never
happens. `--force` is you asking for the quiet moment to be made:

```sh
sudo zoomies tune --force                       # restart Docker for a pending change
sudo zoomies doctor --interactive --force       # the same, after reviewing fixes
sudo zoomies tune --force --wait 30m            # give running jobs longer to finish
sudo zoomies tune --force --kill-running        # ...and stop whatever is left
```

It asks first (or accepts `--yes`), then:

1. records which of `zoomies-agent.service` and `zoomies.service` are running, and
   stops those, so nothing new starts;
2. waits up to `--wait` (default 10 minutes) for every running container to finish —
   the host's own and anyone else's, because a Docker restart ends them all;
3. restarts Docker and waits for it to answer;
4. starts the services it stopped, in the reverse order, and clears the pending restart.

**It never ends a job on its own.** If containers are still running when `--wait` is
over, the host is put back in service, nothing is restarted, the change stays pending,
and the message says so. `--kill-running` is the separate, named permission to stop
what is left and restart anyway; it lists the containers first, and their jobs fail.
If the restart itself fails, the services are still started again — a host left out of
service is worse than a restart that did not happen.

On a host that runs the controller, `zoomies.service` is the control plane: it is
stopped for the wait, so jobs queue rather than run until it is back. A service whose
state cannot be read is a refusal before anything is stopped, as is a custom or
rootless Docker endpoint, which is restarted by hand.

A host that runs Zoomies in a container (a `docker` or `compose` deployment) is refused
too, with `--force`, `--background` and `--restart-pending` alike. The maintenance
restart takes a host out of service by stopping systemd units, which a container
deployment does not have: the controller's own container would be counted as running work
and, with `--kill-running`, stopped and not started again. The message names the container
and the three commands to do it by hand when the host is quiet: `docker stop`,
`systemctl restart docker`, `docker start`.

A maintenance restart that is killed outright -- `SIGKILL`, a crash, the machine going
down -- cannot run its restore, so it writes down what it is about to stop first
(`maintenance_stopped` in the tuning state) and clears the record only once everything
is started again. The next restart finds the record, says that an earlier one was
interrupted, and starts those units at the end of its own. If the state file cannot be
written, nothing is stopped.

A stop that outlasts the command is not a stop that failed: the agent's unit allows 20
minutes to finish what it admitted, so the command waits that long for it, and a unit
that was being stopped when the command gave up is started again on the way out like any
other. A closed terminal (`SIGHUP`) is treated as ctrl-C, so a dropped SSH session
restores the host instead of leaving it out of service.

### In the background: `--force --background`

Draining a host that is busy for hours is hours of queued jobs. `--background` takes the
other approach: leave the host in service and keep looking for the gaps ephemeral
runners leave between jobs.

A runner that is up but holds no job -- a pool's minimum keeps some warm -- is not work,
for either kind of restart. It is recognised by having no `Runner.Worker` process, which
the runner starts for each job, and is stopped together with its Docker-in-Docker sidecar
just before Docker restarts; the controller starts it again afterwards. A container whose
processes cannot be read counts as work.

```sh
sudo zoomies tune --force --background
sudo zoomies doctor --interactive --force --background --give-up-after 12h
```

It applies the fixes you approve now, then hands the restart to a transient systemd unit,
`zoomies-docker-restart.service`, so it outlives your terminal and the services it stops.
The task checks every 30 seconds. While anything is running it does nothing at all. When
the host is quiet it stops the Zoomies services, confirms nothing started in that moment,
restarts Docker and starts them again. If a job did arrive in that moment, everything is
put back and it carries on looking; nothing is ever stopped to make room, so there is no
`--kill-running` here. It gives up after `--give-up-after` (default 24 hours) and the
change stays pending for the next `tune` to try.

```sh
journalctl -u zoomies-docker-restart -f      # follow it
systemctl stop zoomies-docker-restart        # cancel it
```

There is one task per host: asking again while it waits is refused. On a host without
systemd, run `sudo zoomies tune --restart-pending` in a terminal multiplexer instead; it is
the same loop in the foreground, and the only thing the background unit runs. It restarts
Docker for a change already made and nothing else: it never reviews or applies a fix, so a
task you started with approval cannot approve something nobody was asked about.

`--force` never overrides the dependency or managed-configuration guards, and `upgrade`
and `update` never use it: upgrades do not tune.

## Aggressive checks

Use `--tier aggressive` explicitly. These are not offered by the installer.

| Check ID | Effect | Files or units tune can change |
| --- | --- | --- |
| `cpu.governor` | Use the performance governor; more power and heat | Each CPU policy's live `scaling_governor`; `/etc/systemd/system/cpupower.service.d/90-zoomies.conf`; enablement of the existing distribution `cpupower.service` |
| `memory.swappiness` | Recommend `vm.swappiness=10` unless already lower | `/etc/sysctl.d/90-zoomies.conf`; matching live sysctl |
| `tmp.tmpfs` | Advice by default; explicit selection can prepare a memory-backed `/tmp`, capped at 25% RAM | `/etc/systemd/system/tmp.mount.d/90-zoomies.conf` and enablement of an existing `tmp.mount` unit |

CPU tuning requires an existing distribution-supplied `cpupower.service` and
performance-governor support. Guests without CPU frequency controls are skipped.
`/tmp` is never mounted over active files. Explicit selection prepares the next
planned boot only; hosts without a restorable `tmp.mount` unit receive advice.

```sh
sudo zoomies tune --tier aggressive --only tmp.tmpfs --dry-run
```

## Kernel checks appear in every tier

`kernel.running` warns below Linux 6.8. `kernel.pending` compares the running
kernel with installed kernels and reports a pending reboot. `kernel.hwe` checks
whether Ubuntu 24.04's official `linux-generic-hwe-24.04` package is installed
or available in the locally cached package metadata. No package index is
updated by doctor. Debian skips the Ubuntu-specific check. A pending reboot is
one fact that a report carries twice, as this warning and as `reboot_pending`;
the controller counts it once, as
[described above](#continuous-reporting-uses-the-installed-host-binary), and
`zoomies doctor` counts the warning.

Zoomies never changes CPU vulnerability mitigations and never installs a
mainline or third-party kernel. It never reboots. Cordon the host, wait until
nothing on it is running a job, reboot during your maintenance period, and check
doctor before allowing new jobs.

## Dedicated host tuning

!!! warning "Only for a host running nothing but Zoomies"
    Dedicated changes can remove services needed by other applications. They
    are never part of safe or aggressive defaults. Use `--dedicated` and type
    `THIS HOST RUNS ONLY ZOOMIES`, or explicitly pass `--dedicated --yes`.
    Review the dry run first.

Services are disabled and masked, **never purged**. Original enablement and
running state are recorded. A dependency outside the allowed service families
blocks that item. Unverifiable dependencies are skipped. No SSH, time sync,
networking, firewall or journald service is disabled.

| Check ID | Unit or file | Guard |
| --- | --- | --- |
| `service.snapd` | `snapd.service`, `snapd.socket`, `snapd.seeded.service`, `snapd.apparmor.service`, `snapd.autoimport.service` | Only core snaps installed; verified dependencies |
| `service.ModemManager` | `ModemManager.service` | Verified dependencies |
| `service.multipathd` | `multipathd.service`, `multipathd.socket` | Verified dependencies |
| `service.apport` | `apport.service` | Verified dependencies |
| `service.motd-news` | `motd-news.service`, `motd-news.timer` | Verified dependencies |
| `service.udisks2` | `udisks2.service` | Verified dependencies |
| `service.fwupd` | `fwupd.service` | Confirmed VM only, plus verified dependencies |
| `service.cloud-init` | `cloud-init.service`, `cloud-init-local.service`, `cloud-config.service`, `cloud-final.service` | Cloud-init completed, plus verified dependencies |
| `service.apt-daily` | `apt-daily.timer` | Implemented security-update maintenance window required |
| `service.apt-daily-upgrade` | `apt-daily-upgrade.timer` | Same requirement |
| `journal.size` | `/etc/systemd/journald.conf.d/90-zoomies.conf`, `SystemMaxUse=1G` | Existing managed policy blocks changes; journald is not restarted |
| `memory.swap-off` | Active fstab swap entries and their `/etc/systemd/system/<name>.swap.d/90-zoomies.conf` drop-ins | Explicit `--only`, at least 32 GiB RAM; custom/generated swap policies skipped |
| `kernel.hwe-install` | Official Ubuntu HWE packages | Explicit `--only`; package simulation must show new official Ubuntu packages only, with no upgrades or removals |

**Security-update maintenance is not implemented in this pass.** The check's
`SecurityMaintenanceReady` contract must prove a configured maintenance window
runs security updates with no active jobs. It defaults to false and has no
user-facing bypass. A TODO marks the future integration. Consequently the apt
timers remain enabled and doctor explains why.

Swap-off is optional because workloads can run out of memory without swap.
HWE installation is also optional. Its exact new package names and versions are
recorded; reversal removes those new packages without upgrading, purging or
removing pre-existing packages. It refuses if that kernel is running, or a
simulated reversal would affect unrelated packages. Package-manager caches,
logs and regenerated boot metadata are not byte-for-byte filesystem snapshots.

## Review and reversal

```sh
sudo zoomies tune --only inotify.watches,inotify.instances --dry-run
sudo zoomies tune --skip docker.logs
sudo zoomies tune --revert --dry-run
sudo zoomies tune --revert
```

The default is interactive safe tuning with a confirmation for every item.
`--yes` approves the chosen tier and items. `--only`/`--skip` accept
comma-separated check IDs. Optional items require `--only`; selecting a check
from another tier also requires that tier's explicit flag.

Every change is recorded before it starts in the root-owned mode-0600
`/var/lib/zoomies-host-tune/state.json`. The directory is mode 0700. Backups
live under `/var/lib/zoomies-host-tune/backups/`. Records identify the applying
user, timestamp, previous/new values, exact file bytes and permissions, unit
commands and reversal commands. A lock prevents concurrent tuning. An
interrupted change remains recoverable with `--revert`.

Re-running tuning is idempotent. Reversal unwinds changes in reverse order,
restores original files and live sysctls, and removes drop-ins created by
Zoomies. Selective reversal of a shared drop-in requires its later edits to be
reverted first. Subsequent administrator edits are refused rather than
silently overwritten. Restart-dependent settings and `/tmp` can require a
later drained restart or planned reboot to restore their live behaviour.

Uninstall removes the health reporting service but preserves tuning records.
Revert OS tuning **before uninstalling the binary**, or keep a binary available
to revert it afterwards.

## Install and upgrade behaviour

At the end of a fresh installation doctor runs, then interactive setup asks:
`Apply recommended safe tuning? [y/N]`. `install.sh --tune` explicitly approves
safe changes; `--no-tune` suppresses the offer. Unattended setup and `--yes`
alone never imply tuning. Aggressive and dedicated tiers are mentioned only.

`zoomies upgrade` finishes with one read-only health status and a command to
review findings. It never opens a doctor/tune menu or applies tuning, including
with `--yes`. `zoomies update` and `zoomies deployment update` use the same
flow; `install.sh --upgrade` delegates to it. Existing container deployments
are offered the read-only native health service through the normal,
consent-based upgrade layout review. This service reports observations.

Tune shows its selected tier, numbers the changes it presents, keeps exact
file/command previews before each approval, and finishes with counts of
applied and declined changes. `--dry-run` reports the eligible changes without
writing anything. Advice-only and optional findings are available through
`zoomies doctor --verbose` rather than repeated during every tuning review.
