# Persistent caches for ephemeral runners

Keep runners ephemeral. Retain selected cache data outside the runner's writable filesystem so the next runner can reuse downloads and build outputs. Losing a cache must only make a build slower; workspaces, credentials, runner registration and job state must not depend on it.

| Data | Lifetime and location | Sharing rule |
|---|---|---|
| Runner registration and writable workspace | One ephemeral runner/job | Never reused as cache |
| Package downloads | Host directory or managed volume selected by the pool cache configuration | Scope by repository/trust boundary; package managers must tolerate concurrent writers |
| Setup-action toolchains | Agent-kept cache, read-only in runners, separate writable link view per runner | Scope by pool/repository and resolved runner image |
| Preinstalled image tools/libraries | Runner image layers | Immutable image reference; inventory inside image |
| DinD daemon state | One runner's isolated sidecar lifetime | Do not share live `/var/lib/docker` between daemons |
| BuildKit build cache | External registry, or another explicitly managed BuildKit cache service | Separate cache reference and write credentials per repository and trust boundary |

## Local cache scopes

Use the pool's existing cache scope and source settings, described in [Hosts and pools](hosts-and-pools.md). Recipes for pointing Go, npm, pip, Maven and a local BuildKit cache at the pool cache are in [the configuration reference](configuration.md#using-the-pool-cache-from-a-workflow). Persistent host caches help subsequent runners on the same host; they do not follow a runner to another pool host automatically. A repository-scoped cache name is not a security boundary on an organisation installation: GitHub may assign a different matching repository's job to that runner. Use repository-target installations or explicit trust-separated pools for strong isolation.

Cache identities now hash the full scope, immutable pool ID and canonical repository tuple, with a readable prefix. Tool generations also hash the resolved image reference. Renaming a pool does not relocate its cache; changing image identity creates a new tool generation. Old cache namespaces remain untouched for active runners and must be retired after they drain.

The writable per-runner tool view links to retained tools read-only. The entrypoint also exposes missing image-baked tools there, including completion markers used by setup actions. Existing retained tool entries take precedence. The image inventory is `/usr/local/share/zoomies/installed-software.json`; it lists installed distribution package versions and completed tool-cache entries, not a complete dependency SBOM.

Set cache size targets and leave disk reserve for image pulls, workspaces, daemon metadata and logs. Maintenance honours cancellation but eviction is not a quota and a busy cache may defer eviction. Monitor both free bytes and inodes. Cache and Docker filesystems may differ from the agent work filesystem.

## DinD and BuildKit

A runner's DinD sidecar stays disposable. To benefit across runners and hosts, configure workflow-level BuildKit cache export/import to a registry. Example for an already authenticated registry and a Buildx builder:

```sh
# Set these to repository-specific references under your registry account.
: "${IMAGE_REF:?image reference required}"
: "${CACHE_REF:?repository-specific cache reference required}"
docker buildx build --push \
  --tag "$IMAGE_REF" \
  --cache-from "type=registry,ref=$CACHE_REF" \
  --cache-to "type=registry,ref=$CACHE_REF,mode=max" \
  .
```

This example requires a Buildx builder supporting the registry cache backend. Keep the cache reference separate from the image output. Grant cache publication credentials only to trusted workflows; untrusted pull requests should have no trusted-cache write credentials, and must not populate a cache consumed by privileged builds. Use distinct references for relevant architecture/toolchain/platform differences. Multiple concurrent writers to one reference can replace each other's cache manifest; use branch/job-specific write references and explicit shared trusted read references where needed.

Do not put secrets in build arguments, copied layers or cached outputs. Use BuildKit secret mounts for build-time credentials. Set registry retention separately from local Docker build-cache targets: the agent cannot prune a remote registry's cache.

The existing `agent.docker_build_cache_mb` policy acts on the outer daemon and defaults to zero (disabled); it does not manage isolated DinD caches or registry retention. Enabling daemon-wide pruning on a shared host needs an explicit ownership decision. Avoid broad `docker system prune` as an automatic cache policy.

## Verify the benefit

Measure identical cold and warm jobs on the same host and across hosts. Record dependency download time, image preparation, BuildKit cached steps, queue-to-start time and total job time. Delete the cache and rerun to verify correctness. Compare storage/registry costs with time saved before expanding retention or baking more libraries into the image.
