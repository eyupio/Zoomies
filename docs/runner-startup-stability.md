---
icon: material/timer-outline
title: Runner startup under host load
description: >-
  How Zoomies keeps runner startup reliable on a busy host: a health probe
  before admission, the CPU quota during registration, and the settings that
  control both.
---

# Runner startup under host load

Disabling `scheduler.default_runner_limits` improved startup in the reported
fleet. This points to constrained startup, but no production trace or
before/after load benchmark was available to establish a single root cause.

Two concrete problems compound host pressure: the startup queue released new
creates while a quota-limited DinD daemon was still initialising, and newly
created DinD pairs used the whole host share as their throttle base instead of
the per-container half recorded in labels. Restoring their quotas could double
the CPU reservation. Adoption after restart already used the correct half.

The backend now holds startup admission until a bounded `docker info` health
probe succeeds, tolerating transient failed probes and reporting early
OOM/exit. Failure uses the existing cleanup path. The runner image retains
its readiness check for older agents, host sockets and intervening failures.
No consumed JIT configuration is retried.

The per-container throttle base now matches creation and adoption. A bounded
CPU grace protects registration from an additional pressure reduction. A job
accepted during that grace also retains the normal quota. Neither CPU nor
memory limits are removed or raised.

## Defaults and controls

| Setting | Default | Control |
| --- | --- | --- |
| `scheduler.default_runner_limits` | `true`, unchanged | Keep host-share limits for automatic pools. |
| `agent.bootstrap_cpu_grace` | `2m`, new | 0s disables; maximum 10m. Restart each agent after configuring it. |
| `runners.docker_wait` | `3m`, previously 2m | Live for new tasks. 0s selects the 2m compatibility fallback; pool environment overrides accept 1–3600 whole seconds. |
| `scheduler.provision_timeout` | `20m`, unchanged | Includes queue time; increase for large bursts and cold pulls. |

The agent's outer 10m create budget still caps image preparation plus daemon
readiness. Existing stored values and environment overrides remain authoritative.
The controller's startup grace configures its embedded agent; standalone agents
need the setting on their own hosts. See [Configuration](configuration.md).

Automatic pool creation and editing reuse Settings findings. The one-time
button lists fleet-wide changes, requires an administrator, respects environment
pins and read-only settings, and identifies pending restarts. Settings renders
the same warning beside each affected item. Existing host-fit and capacity
warnings still diagnose undersized slots; grace cannot fix insufficient memory.

## Open-source comparison

Reviewed source snapshots:

| Project | Observed behaviour | Application here |
| --- | --- | --- |
| [ARC DinD configuration](https://github.com/actions/actions-runner-controller/blob/e8753bcf57db67d03293e52a6f6bbed167818226/charts/gha-runner-scale-set/values.yaml) | `docker info` startup probe; 24 attempts at five-second intervals. | Gate startup on daemon readiness. Zoomies uses Docker health state instead of Kubernetes startup probes. |
| [ARC ephemeral controller](https://github.com/actions/actions-runner-controller/blob/e8753bcf57db67d03293e52a6f6bbed167818226/controllers/actions.github.com/ephemeralrunner_controller.go) | Bounded 5/10/20/40/80-second failure backoff; quota refusal requeues. | Retain bounded recovery, without adding unsafe listener/JIT retries. |
| [GARM bootstrap supervision](https://github.com/cloudbase/garm/blob/0d35fe0db03b0fcc093976bc231d74e1ee9e57c6/workers/scaleset/scaleset.go) | Separates provider creation deadlines from reaping offline bootstrap instances. | Keep readiness in the owning create operation and GitHub authoritative for idle/busy state. |
| [GARM backoff](https://github.com/cloudbase/garm/blob/0d35fe0db03b0fcc093976bc231d74e1ee9e57c6/workers/common/backoff.go) | Per-key exponential delay, capped at five minutes, with jitter. | Supports bounded recovery; cross-host jitter is a possible follow-up, not implemented here. |

These sources do not establish that removing CPU limits is a universal fix.
Rollout still needs representative concurrent jobs, cold pulls and registration
timings on the affected hosts.

## Registration and background work

Credential minting now has a per-installation admission bound, controlled by
`scheduler.registration_concurrency` (default 1). API rate limits establish the
existing shared installation hold, without adding pool startup-failure backoff.
Excess demand remains with the scheduler rather than accumulating waiter goroutines.
Bootstrap retries revoke the unused previous credential before issuing another;
revocation and redemption are serialised in the store, and a failed revocation
aborts replacement.

Agent runtime recovery adds up to 25% positive jitter to its 5-second exponential
cooldown (maximum 75 seconds). Background prewarming has an independent budget and
stagger, configured by `agent.prewarm_timeout` and `agent.prewarm_jitter`. Neither
changes foreground pull policy. See [runtime compatibility and diagnostics](runtime-compatibility.md)
for the capability matrix, measurement fields and real-host validation procedure.
