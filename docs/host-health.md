---
icon: material/stethoscope
title: Host health checks and tuning
description: Read host OS health reports, approve individual tuning changes, and restore recorded settings with Zoomies doctor and tune.
---

# Host health checks and tuning

`zoomies doctor` explains the host's current OS settings, warnings, skipped
checks and recommended changes. It starts with a summary, then a table. Nothing
is tuned unless you explicitly approve it. Ubuntu 24.04 and Debian 13 are the
first supported tuning platforms. Other Linux distributions are report-only;
non-Linux hosts report unsupported checks.

```sh
zoomies doctor
zoomies doctor --json
zoomies doctor --tier aggressive
sudo zoomies doctor --interactive
sudo zoomies tune --dry-run
sudo zoomies tune
sudo zoomies tune --revert
```

Doctor exits **0** without warnings, **1** with warnings, and **2** when a check
errors. Skipped checks explain the missing access or host capability. Doctor
works without root; checks whose inputs cannot be read are skipped rather than
silently assumed healthy. `--interactive` shows the report and offers eligible
fixes individually, showing each file change and command before `[y/N]` consent.
It cannot be combined with `--json`, `--watch` or a remote host selection.

## Continuous reporting uses the installed host binary

A native agent collects read-only OS observations in its existing daemon. For
an installed Docker or Compose deployment, **the native binary on the host**
runs `zoomies-host-health.service`. This supplies the full OS report to the
container through the existing shared directory. There is no privileged
container mount and no incoming connection to an agent.

The service samples every minute. Agent heartbeats carry the latest report to
the controller, which persists it and sends `host.updated` events. Host badges
and the host detail checks table update without reloading the page. Reports
older than three minutes or from unreachable hosts are labelled stale.
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
for OS tuning; its health badge links to a tier-grouped checks table.

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
Drain and stop the agent first. **`--force` never overrides that refusal.**

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
updated by doctor. Debian skips the Ubuntu-specific check.

Zoomies never changes CPU vulnerability mitigations and never installs a
mainline or third-party kernel. It never reboots. Cordon/drain the host, wait
for jobs to finish, reboot during your maintenance period, and check doctor
before allowing new jobs.

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
| `service.snapd` | `snapd.service` | Only core snaps installed; verified dependencies |
| `service.ModemManager` | `ModemManager.service` | Verified dependencies |
| `service.multipathd` | `multipathd.service` | Verified dependencies |
| `service.apport` | `apport.service` | Verified dependencies |
| `service.motd-news` | `motd-news.service` | Verified dependencies |
| `service.udisks2` | `udisks2.service` | Verified dependencies |
| `service.fwupd` | `fwupd.service` | Confirmed VM only, plus verified dependencies |
| `service.cloud-init` | `cloud-init.service` | Cloud-init completed, plus verified dependencies |
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

`zoomies update`, `zoomies upgrade` and `install.sh --upgrade` run doctor only.
**They never apply tuning**, including with `--yes`. New warnings print a line
pointing to `zoomies tune`. Existing container deployments are offered the
read-only native health service through the normal, consent-based upgrade
layout review. The service is reporting, not host tuning.
