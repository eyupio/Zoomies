# Stability and performance implementation

This change implements the first safety and performance tranche from the September 2026 review (SP-01–SP-29). Ephemeral runners remain the default. Persistent caches are independent, disposable acceleration data; a cache is neither a runner nor a workspace backup.

## Changes and priorities

| Priority | Review | Implemented behaviour | Reason |
|---|---|---|---|
| P0 | SP-01, SP-04 | Recovery fences and expired/lost controller authority stop new lifecycle dispatch, provider operations and CPU directives. New agents honour a heartbeat pause for local cleanup and queued lifecycle tasks. | Prevent destructive work without current authority. Already executing external operations cannot be recalled. |
| P0 | SP-02, SP-03 | A backend whose startup inventory failed must first adopt its recovered inventory. Missing workload observations remain until acknowledged. | A temporary outage must not turn owned workloads into orphans or lose terminal evidence. |
| P0 | SP-05 | Native metadata records PID birth identity and is replaced atomically. Stop, kill and status verify identity; uncertain inventory fails closed, while a proven identity mismatch reads as a gone runner. | Reduce the risk of signalling a reused PID. Verification and signalling are still separate system calls. |
| P0 | SP-06 | Controller task ACK follows required durable result writes. Failed writes retain the in-flight task. | Permit redelivery instead of silently forgetting an unapplied result. |
| P1 | SP-07, SP-08 | Durable, generation-checked enrichment queue; four bounded lookups per batch for readiness, run metadata and registration cleanup. | Keep remote GitHub latency out of webhook and heartbeat acknowledgements. |
| P1 | SP-09 | Backups use a read connection without the application writer mutex; job and event pruning use bounded batches. | Reduce ingestion stalls while retaining WAL snapshot semantics. Disk I/O can still contend. |
| P1 | SP-10 | Provider inventories run outside the machine pass lock, with four concurrent sweeps; lifecycle operations are capped at eight globally/four per provider. Inventory evidence is applied only to unchanged machine rows. | A slow remote provider must not block every machine pass or invalidate newer state. |
| P1 | SP-11–SP-13 | Transition-only job metrics; independent missed-event recovery and discovery loops; one runner snapshot query instead of one per pool. | Reduce duplicate metrics, discovery coupling and repeated reads. |
| P1 | SP-14–SP-16 | Resource samples do not invalidate table structure; browser replay is capped at 2,048 events with refresh after overflow; native diagnostic logs retain a 10 MiB tail plus current file, checked each minute; public password checks are capped at two concurrently. | Bound avoidable CPU, memory and disk growth. Log retention is periodic, not a hard filesystem quota. |
| P1 | SP-20–SP-22 | Versioned hashed cache identities; tool cache directories include resolved image identity; baked tools are linked into each disposable tool view. | Prevent namespace collisions and incompatible tool reuse without downloading baked tools again. |
| P1 | SP-23–SP-25 | Cache walks honour cancellation; images contain an installed-package/tool inventory; documented registry BuildKit cache workflow keeps DinD disposable. | Bound maintenance work and make reuse and included software inspectable. |
| P1 | SP-26, SP-27 | An independent monotonic watchdog expires CPU loans; failed boost withdrawals retry during controller outages; quota reductions precede increases; failed or pending updates suppress increases. | Avoid lending CPU before previous loans have been reclaimed. |
| P2 | SP-17, SP-28, SP-29 | Successful startup service time is recorded once; optional readiness placement uses recent host/pool/image history and pending starts, after existing hard fit checks. Versioned placement intent narrows boost start reservations for up to 30 seconds. | Prefer a host likely to admit new capacity sooner without moving running jobs or relaxing resource limits. |

## Persistent caches with ephemeral runners

See [Persistent caches](../persistent-caches.md) for storage boundaries, trust scope, DinD reuse and migration. The change does not enable shared writable caches for every pool automatically.

## Placement rollout

`scheduler.placement_mode` / `ZOOMIES_PLACEMENT_MODE` accepts:

- `headroom` (default): existing resource-aware placement.
- `shadow`: retain existing placement and log a readiness comparison at debug level.
- `readiness`: use estimated startup queue delay, then headroom for differences within five seconds.

Evidence is limited to the latest 1,000 successful starts in 24 hours, with at least three samples per host/pool/image. Service time includes image preparation and backend create; it excludes admission waiting and GitHub registration. Unknown history uses a one-minute prior. Estimates are bounded to 5 seconds–10 minutes. Existing connectivity, labels, backend, capacity, CPU, memory and disk checks still apply. Pending starts count even with missing host telemetry.

This is an admission heuristic, not a promise of job completion time. GitHub assigns jobs to eligible runners. Zoomies places new runners; it does not move jobs already assigned by GitHub. Use immutable image references when comparing generations: a mutable tag can describe changed image contents.

Start in shadow mode for a representative work week. Compare queue-to-start p50/p95/p99, failed starts, host skew, cache hit/miss and startup durations, provider latency, CPU throttling and memory/disk pressure. Enable readiness for a canary controller only when tail latency improves without worse failures, fairness or host pressure. Restore `headroom` immediately if those regress.

## Upgrade and rollback

1. Take and verify a database backup. Migrations 0055 and 0056 add enrichment state and startup service time.
2. Drain native-process hosts before upgrading agents: legacy PID-only metadata cannot prove process ownership. Resolve old processes explicitly; do not delete metadata to force adoption or kill an unverified PID.
3. Upgrade controller and agents together. Old agents ignore the new pause field, so local cleanup suppression requires new agents. Controller-side dispatch fencing applies independently.
4. Expect cold caches in the new namespace. Old caches are not copied into new trust/image scopes or deleted automatically. Remove old generations only after all their runners and maintenance tasks have drained.
5. Verify the image/package matrix before publishing runner images. Revert readiness configuration independently of code. Database rollback requires the usual compatible binary/backup procedure, not deleting migration records.

## Release evidence and remaining work

These are explicit follow-ons, not claims delivered by this patch:

- **P1, SP-06/17:** durable agent result outbox across restart, explicit credential-age/admission metrics and safe renewal; current redelivery still depends on existing task leases and reconciliation.
- **P1, SP-08/12/13:** per-installation fairness/rate budgets, batched readiness caching, keyset recovery and compact historical scheduler reads. The new bounded enrichment worker and split loops provide isolation, not complete fleet-scale optimisation.
- **P1, SP-23/24:** per-filesystem byte/inode admission, retained-cache quotas, capability-tested DinD digest/version matrix and cache telemetry. Existing cache limits are eviction targets; workloads can still fill a filesystem. Do not mount one live Docker data-root into multiple daemons.
- **P1, SP-27:** explicit desired/applied generation acknowledgements. Reductions-first and retry are necessary but do not yet constitute a distributed capacity transaction.
- **P2, SP-18:** leave Tailcat connection reuse disabled until relay/session failure tests demonstrate safe pooling; no speculative keepalive change.
- **P2, SP-25/28:** complete software smoke matrix and SBOM publication, cache-warmth inventory, job-duration/reliability models, cold-host exploration and percentile prediction. Current history is bounded mean startup service time.

No percentage speedup is asserted without production measurements. Expected improvements are fewer lost observations and unsafe cleanup attempts, shorter ingestion stalls during remote outages, fewer cache misses caused by incompatible or hidden tool caches, and more informed placement on heterogeneous hosts. Correct cache isolation may initially lower hit rates because it intentionally starts fresh namespaces.

### Repeatable release drills (SP-19)

Run normal CI, targeted race tests and platform-native integration tests. Use disposable hosts for these drills, preserving task, runner and provider IDs in the evidence:

| Drill | Required observation |
|---|---|
| Lose controller lease while cleanup/create tasks are queued | No new task dispatch; observations still ingest; recovery does not duplicate starts. |
| Restart Docker during agent adoption, then restore it | Owned workloads adopted before orphan reaping. |
| Lose the terminal report response and retry it | One terminal transition; observation retained until ACK. |
| Stall provider List while creating a machine | Other machine passes continue; old inventory does not mark the new machine missing. |
| Return GitHub 429/5xx during readiness and webhook traffic | Ingestion stays responsive; bounded enrichment retries survive controller restart. |
| Fail quota reduction while another runner requests a boost | No new increase in that batch; withdrawal retries after recovery and controller outage. |
| Run cold/warm dependency and Docker builds, then delete caches | Equivalent outputs; cache absence costs time only; fork workflows cannot publish trusted cache. |
| Fill cache filesystem and exhaust inodes on a disposable host | Record actual admission behaviour and recovery; do not infer protection from eviction limits. |
| Native agent restart and PID reuse on Linux/macOS/Windows | Logging survives restart; unverified PID never signalled; bounds apply to diagnostic files. |
| Compare identical workload replays across headroom/shadow/readiness | Record p50/p95/p99, failure rate, host skew, throughput and cost with equal resource budgets. |

The implementation tests and local validation results are recorded in the pull request. Image builds, real provider/relay drills and native process integration require environments with their actual runtimes; unit tests do not substitute for them.

### Validation for this tranche

- Controller suite: all cases except the cleanup timing assertion passed on the first final run. That assertion was updated to check durable host removal before ACK and registration cleanup after the enrichment worker; its rerun passed. The test harness now mocks public release checks as well as its GitHub API.
- Store, scheduler, configuration and backend package tests passed. Documentation consistency checks passed after documenting the new setting and problem code.
- Agent race tests passed, including monotonic boost expiry and retry. Targeted controller/provider, store and scheduler race tests passed. The broad controller/store race run exceeded its four-minute budget; no race was reported before timeout.
- Go vet passed for the changed runtime packages. Backend tests cross-compiled for Windows amd64 and macOS arm64.
- UI type check, lint and production build passed; 200 unit tests passed with `TZ=UTC`. One existing chart tick test failed under the environment's implicit timezone.
- Full-repository test execution was not completed: automatic approval review blocked its unverified external GitHub requests. Subsequent controller verification used a mocked release transport. Native integration cases requiring matching PID and proc namespaces are skipped in this execution environment and must run on CI/native hosts.
- Docker image builds, registry cache performance, real remote-provider/relay failure drills and production performance comparisons remain release gates.
