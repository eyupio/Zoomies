---
icon: material/timer-sand
title: GitHub Actions job stuck queued on a self-hosted runner
description: >-
  Diagnose a queued GitHub Actions job: check labels, runner groups, Zoomies
  pool limits, host capacity, image startup and registration in order.
---

# GitHub Actions job stuck queued on a self-hosted runner

A queued job needs a runner that matches its routing and is available to take
it. In Zoomies, check the job's matched pool, that pool's eligible hosts, then
the runner's startup and registration. Increasing a pool's maximum only helps
when its ceiling is the thing holding the job back.

Start in **Jobs** and open the job, then read the Overview's problems drawer.
For a runner managed by another tool, start with GitHub's runner status and
routing checks below; the pool and host steps are specific to Zoomies.

## 1. Check what GitHub is waiting for

Open the workflow run and read the job's status and requested runner labels.
An unmet `needs` dependency, an environment approval or a
[workflow concurrency limit](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)
can hold work before runner capacity is the issue. Adding machines does not
resolve those waits.

If the job is waiting for a runner, compare its `runs-on` with the labels and
group available to that repository. A label list requires a runner matching
**every** label, and a group adds its own eligibility constraint. GitHub's
[routing documentation](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/use-in-a-workflow)
shows both forms.

For example, a workflow using the pool label from the Zoomies quick start has:

```yaml
runs-on: zoomies-linux-x64
```

Use your actual pool label. Adding `self-hosted`, another operating system or
an extra custom label changes the match; it is not an instruction to create a
runner with those labels.

## 2. Confirm that Zoomies sees and claims the job

Use the configured CLI connection, or read the same information in the UI:

```sh
zoomies status
zoomies jobs list --state queued --since 24h
zoomies jobs list --unmatched --since 24h
zoomies pools list
```

Use a longer `--since` window for an older job. If Zoomies sees the job but no
enabled pool claims it, compare the workflow labels with the pool's labels,
installation target and enabled state. A pool deliberately limited to zero
runners cannot create capacity; check why that limit was set before changing it.
[Hosts and pools](hosts-and-pools.md) explains those settings.

For organisation runners, also check that the runner group permits this
repository. A personal-account repository needs a repository-target
installation. [Many repositories](many-repositories.md) explains that boundary.

If the job is absent, open **Installations** and check that the App can see the
repository. Review its `workflow_job` webhook deliveries and the controller's
external URL. The fallback poller can discover queued work when webhooks fail;
if both routes are unavailable, no scheduler change can create the missing job.
The [problem codes](problem-codes.md) name external URL and polling faults.

## 3. Read the reason capacity was not created

Open the matched pool and its eligible hosts, or run:

```sh
zoomies hosts list
zoomies runners list
```

| What you find | What to check next |
| --- | --- |
| The pool is at `max_runners` | Whether existing jobs will finish soon, or the pool needs a higher ceiling |
| No host matches the selector | The host's labels, operating system, architecture and the pool's selector |
| A host is disconnected or cordoned | Its last heartbeat and the reason it is unavailable |
| No matching host offers the backend | The agent's backend probe and socket permissions on that host |
| The host has no usable capacity | CPU and memory reservations, disk space, live pressure and any hold |
| A runner already exists | Its startup facts and logs, in the next step |

Read the host's own reason before reducing reserves or raising limits. A busy
host can have nominal slots left and still lack the resources to start a job.
The [host-pressure troubleshooting section](troubleshooting.md#the-agent-is-connected-but-new-runners-are-held)
explains reservations and measured load.

For a socket-permission fault, use the remedy the agent reports for **its own
account**. Adding your interactive user to a group may change nothing for a
service account, and a containerised agent has different group settings. The
[backend troubleshooting section](troubleshooting.md#a-job-that-sits-in-the-queue)
explains both installations.

## 4. Separate workload startup from GitHub registration

Open the runner's detail page and inspect **Container started**,
**Registered with GitHub** and **Host last seen**:

| Startup evidence | Where to look |
| --- | --- |
| No workload start reported | Agent connectivity, backend creation and image pull errors on that host |
| Workload started, no registration | Runner logs, outbound GitHub access and registration errors |
| Runner registered | GitHub eligibility, runner availability and the job's current routing |

With the runner ID copied from Zoomies:

```sh
zoomies runners get <runner-id>
zoomies runners logs <runner-id> --tail 100
```

On a native systemd controller installation, read recent decisions with
`journalctl -u zoomies -n 50`. Read a remote agent's logs on its host instead;
the controller log is not a substitute for a failed image pull there.

A first pull may take longer than a warm start. Change
`scheduler.provision_timeout` only after checking whether the runner is making
progress. [Runner startup under host load](runner-startup-stability.md) and
[runtime compatibility](runtime-compatibility.md) cover admission and resource
limits. For a published Zoomies image failing with `manifest unknown`, see
[image recovery](development/image-recovery.md).

In GitHub's **Settings → Actions → Runners**, check whether a registered runner
is available, executing another job or offline. GitHub's
[runner troubleshooting guide](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/monitor-and-troubleshoot)
explains the states, runner diagnostics and connectivity checks.

## 5. Verify one change against one job

Watch the same job move from queued to running and confirm that its runner
belongs to the expected pool and host. The quick start shows this example
scaling decision:

```text
scaled zoomies-linux-x64 0 -> 1: 1 job queued
```

That line means Zoomies decided to create capacity; it does not prove that the
runner registered or took the job. Check those stages separately. With
ephemeral runners, removal after the job is expected behaviour.

If a job remains queued despite an eligible, available runner, retain its
workflow URL, routing, pool and runner IDs, timestamps and relevant logs for
an [issue](https://github.com/eyupio/zoomies/issues). Remove credentials and
private payloads before sharing. Avoid deleting a busy runner or resetting the
fleet while diagnosing a single waiting job.
