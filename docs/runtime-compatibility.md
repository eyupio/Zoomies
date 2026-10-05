---
icon: material/puzzle-outline
title: "Runtime compatibility: Docker, Podman and cgroups"
description: >-
  Which Docker and Podman setups Zoomies supports, rootful or rootless, on
  cgroup v1 or v2, what each can enforce, and how to read a runner's resource
  measurements.
---

# Runtime compatibility and startup diagnostics

Zoomies uses Docker API **v1.41** for Docker and Podman's compatibility service.
A successful connection is not proof that a daemon can enforce quotas or provide
Docker-in-Docker. The agent reports the daemon's CPU, memory and PID limit
capabilities; admission must use those reported capabilities.

| Runtime | Resource limits | Docker-in-Docker | Readiness and measurements |
| --- | --- | --- | --- |
| Rootful Docker, cgroup v1 or v2 | As reported by `/info` | Supported, privileged sidecar | Container health must report `healthy`; CFS counters are optional |
| Rootless Docker, cgroup v2 | Requires delegated controllers; inspect the reported capabilities | Subject to host and runtime support | A running sidecar alone is insufficient; validate the actual deployment |
| Rootless Docker, cgroup v1 | Do not assume CPU quotas work | Subject to host and runtime support | Unsupported quotas remain explicit in the capability report |
| Rootful or rootless Podman | As reported by its compatibility service and delegated controllers | Refused; use a supported alternative | Absent CFS data means unmeasured, not zero throttling |
| Custom DinD image | Uses the same limits and readiness budget as the standard sidecar | Docker backend only | Must provide `docker` and a daemon answering `docker --host=tcp://127.0.0.1:2375 info` |
| Process backend | Host/process capabilities apply | No sidecar | Container-specific timings and CFS counters remain absent |

`TestRuntimeCapabilityMatrix` checks Docker/Podman, rootful/rootless and cgroup
v1/v2 protocol fixtures. Existing backend tests cover quota updates, Podman's
DinD refusal, custom sidecar configuration, healthy/unhealthy startup, cancellation,
OOM and unavailable daemons. These are contract tests against a fake engine;
they do **not** certify a real engine/kernel combination. Missing health reporting
is identified in the bounded readiness failure instead of being accepted as ready.

## Interpreting measurements

Runner API responses include `resource_sample.sampled_at` for the last successful
sample. The agent samples approximately every 30 seconds, with a bounded 20-second
sampling pass. If sampling fails, values and their original timestamp are retained.
An absent timestamp means freshness is unknown, including older agents. The runner
details page shows the timestamp and cumulative CPU quota counters when supported.

`cpu_throttling` contains periods, throttled periods and throttled nanoseconds.
Compare **deltas for the same workload**; counters reset when the container is
recreated. Absence means unsupported or unmeasured. A newly created DinD runner
reports the runner and sidecar as one logical sample, because the nested daemon
does the build's work; older containers without the mode label report the runner
container only. These are never whole-host measurements. OOM outcomes continue
through existing lifecycle faults and sidecar failure messages.

Live elastic CPU needs Docker or Podman's resource-update endpoint and an agent
advertising `elastic-cpu`. Unsupported and older agents remain at their creation
quota. The controller records what each agent advertises (`features` on the
host), so a host's card and the pool editor say which hosts would honour an
elastic pool before a runner lands there. Host-pressure reductions take
precedence over boosts, and neither path moves memory on a live workload.

The Prometheus histograms `zoomies_runner_startup_queue_seconds` and
`zoomies_runner_dind_ready_seconds` separate admission delay from sidecar creation
and readiness. They are recorded on successful creates and retain the existing
bounded pool/backend label scheme. Structured `startup admitted` and
`Docker sidecar readiness completed` logs also show these stages; readiness logs
include success/failure. No runner ID is added as a metric label.

Background preparation logs distinguish `cache_hit` and `refresh`, with
duration, resolved digest and success. The Prometheus counter
`zoomies_image_prewarms_total` and histogram
`zoomies_image_prewarm_duration_seconds` carry the same bounded outcome signal
back through the controller for cache-efficiency and latency comparisons. The
bounded 128-entry cache keys include
backend, native platform, requested image reference, pull policy and DinD dependency.
Digest-pinned references retain their immutable identity. Tag entries expire after
45–60 seconds; failure never populates the cache. Foreground `always` pulls retain
their existing semantics. This is metadata coalescing, not a second image store;
existing runtime image/build-cache retention remains responsible for disk usage.

## Representative host validation

Before raising concurrency or shortening startup budgets, test cold and warm
bursts on each deployed runtime/kernel combination. Repeat with normal load, CPU
saturation and memory pressure, using limits on and off only in an isolated test
fleet. Record registration failure rate, p50/p95 time to registration, startup queue
and DinD readiness histograms, sample age, CFS deltas and OOM outcomes. Include the
five-second sidecar health-probe overhead in that comparison. Recovery tests should
also interrupt the runtime and registry, then verify cancellation, staggered retry
and foreground priority. Production performance improvements require those real-host
measurements; unit tests do not establish them.
