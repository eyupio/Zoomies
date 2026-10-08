# 0011: An operator may let Zoomies update itself, through a helper on each host

**Status**: accepted, at the owner's instruction. Asked on 8 October 2026 how a
host's service should get the privilege to replace its own binary, the owner
chose a root-owned helper on each host over a service that rewrites its own
binary and over a documentation-only recipe, and approved the design in
[in-product-updates.md](../in-product-updates.md). The reading of delivery
rule 12 below was put to the owner in the same words and approved with it. The
narrowing of decision 25 was found while this record was being written and has
not been put to the owner separately; their request to update remote hosts from
the web UI is the instruction it rests on. If they would rather keep decision 25
whole, a new record supersedes this one.

**Date**: 2026-10-08, by the designing session at the owner's instruction.

## Context

An operator who wants a newer Zoomies runs `sudo zoomies upgrade` on the
controller's host and again on every agent host. The controller already says
when a release exists (`controller.update_available`), and a remote host's card
prints the command to run on it, but
[docs/upgrading.md](../../docs/upgrading.md) says outright that a controller
upgrade "does not remotely replace binaries across the fleet". The owner asked
for updating to be selectable: off, a button in the web UI, or fully automatic.

A service cannot do it for itself. Both systemd unit templates run the process
as the unprivileged `zoomies` user under `ProtectSystem=strict`, with only the
state directory writable. The binary is root-owned in `/usr/local/bin`, and
restarting a unit needs root. Something root-owned on the host has to act.

Four things already in this repository bear on that.

* Delivery rule 9: a new capability starts disabled, and a disabled one creates
  nothing on an installation that did not ask.
* Delivery rule 12: "Never auto-upgrade a host, remove a customer-owned machine
  or weaken a trust check as a recovery shortcut." Without its last clause the
  rule forbids this outright.
* Delivery rule 16: the platform's hand on a fleet's host is the agent and
  nothing else; no SSH, no controller-initiated dial, no OS package, firewall
  or reboot.
* Decision 25 deferred ZF-404b indefinitely. ROADMAP.md section 7 describes it
  as "dedicated-host maintenance windows, controller-driven agent upgrades and
  retention-driven cleanup of a host", and decision 25 gives the reason: a
  platform upgrading or rebooting a fleet's machines "is a liability, not a
  feature".

What this record allows is narrower than that package and different in kind. It
updates Zoomies' own binary, which the agent's service already owns, and
nothing of the machine. There is also precedent for a consented, disclosed
root-side addition: `zoomies-host-health.service` is offered through the
upgrade's layout review (`internal/installer/host_health.go`), and enrolment's
one-off `zoomies-delegate.conf` is described in
[docs/security.md](../../docs/security.md).

## Decision

Let an operator opt in to updating Zoomies from the web UI, off by default. Put
a root-owned systemd path unit and oneshot service on each host that wants it,
installed only with that host's consent, which on a request naming one published
release tag runs the existing `zoomies upgrade --version <tag> --non-interactive`
and does nothing else. Read rule 12 as binding recovery logic and trust checks,
not an operator's own policy: no recovery path (the machine loop, the
host-lost reclaim, the handling of an incompatible host) may start an update,
and no update may skip a check to get a stuck host moving. Keep rule 16: every
request reaches a host as a task over its agent's own outbound connection, the
controller never dials a host, and the helper is listed in "What the agent owns
on a host". Narrow decision 25 to match: updating the agent's own binary, on a
host that opted in, is allowed; maintenance windows, operating-system upgrades,
reboots and retention-driven cleanup stay deferred.

## Consequences

* ROADMAP.md points here from rule 12, decision 25 and section 9; their wording
  is otherwise unchanged.
* The helper is a new root-owned thing on a host. It is optional and consented
  per host: a host whose owner did not install it cannot be updated by the
  controller, whatever `updates.mode` says. That is also the per-host opt-out.
* `docs/security.md` gains a row in "What the agent owns on a host", and
  `internal/docs/agent_owns_test.go` a fact to pin; the Add-a-host page's
  wording follows.
* An unattended update is trusted exactly as far as `zoomies upgrade` is today:
  a sha256 taken from the same GitHub release. Unattended, that is a larger bet,
  so the first mitigation is a soak delay, and verifying the release's
  provenance attestation is recorded as follow-up rather than done.
* An assistant connected over MCP cannot switch updating on. `update_settings`
  may write any setting under `updates.` today, and that narrows to
  `updates.check_interval`, so choosing a mode stays with a person.
* Hosts that predate the feature need one last manual upgrade to get the helper.
* A service that replaces its own binary, and a documentation-only recipe, were
  both considered and not taken. If the helper proves too heavy, a new record
  supersedes this one.
