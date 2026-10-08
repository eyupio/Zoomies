---
icon: material/database-clock-outline
title: Caching for ephemeral GitHub Actions runners
description: >-
  Keep Go, npm, pip, Maven and Docker build caches between ephemeral runners.
  Configure Zoomies cache scopes and reuse builds safely across jobs and hosts.
---

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

## Enable a pool cache

Open the pool's cache settings when creating or editing a pool. Enable the
cache, choose its scope and source, and leave space for runner images and
workspaces. These are **pool settings**, not top-level keys to paste into
`zoomies.yaml`; the [configuration reference](configuration.md#the-pool-cache)
explains every field.

```yaml
cache:
  enabled: true
  scope: pool
  source: ""
  size_limit: 0
  tools: false
```

An empty `source` selects a daemon-managed volume. A non-zero `size_limit`
requires an absolute host path; zero sets no size target. In a container deployment, put a size-limited cache under the
shared folder, `/var/lib/zoomies/shared/cache/pools`, which the controller's
container already mounts at its own path: the agent enforces the limit by
measuring the folder, so it has to see it. A folder anywhere else needs the same
mount, which `zoomies upgrade` offers to add. Use a pool cache only
for repositories that may share its data. For a repository-target installation,
`scope: repository` selects that repository automatically; an organisation
target also needs `repository: owner/name`, and matching labels still decide
which jobs GitHub can assign. Read the trust limits below before sharing it.

The mount is `/opt/zoomies-cache`. Enable `tools: true` as well to retain
setup-action toolchains; [keeping a tool cache](configuration.md#keeping-a-tool-cache)
explains its image scope and how it differs from package downloads.

## Local cache scopes

Use the pool's existing cache scope and source settings, described in [Hosts and pools](hosts-and-pools.md). Persistent host caches help subsequent runners on the same host; they do not follow a runner to another pool host automatically. A repository-scoped cache name is not a security boundary on an organisation installation: GitHub may assign a different matching repository's job to that runner. Use repository-target installations or explicit trust-separated pools for strong isolation.

Cache identities now hash the full scope, immutable pool ID and canonical repository tuple, with a readable prefix. Tool generations also hash the resolved image reference. Renaming a pool does not relocate its cache; changing image identity creates a new tool generation. Old cache namespaces remain untouched for active runners and must be retired after they drain.

The writable per-runner tool view links to retained tools read-only. The entrypoint also exposes missing image-baked tools there, including completion markers used by setup actions. Existing retained tool entries take precedence. The image inventory is `/usr/local/share/zoomies/installed-software.json`; it lists installed distribution package versions and completed tool-cache entries, not a complete dependency SBOM.

Set cache size targets and leave disk reserve for image pulls, workspaces, daemon metadata and logs. Maintenance honours cancellation but eviction is not a quota and a busy cache may defer eviction. Monitor both free bytes and inodes. Cache and Docker filesystems may differ from the agent work filesystem.

## Keeping the cache in memory

The pool cache cannot be a tmpfs of each runner's own. It is kept *between*
runners, and a tmpfs belongs to one container and is gone when it is, so every
runner would start with an empty cache, which is no cache. The form that works
is a directory the **host** mounts in memory, given as the cache's source. It is
bind-mounted into every runner like any other host path, so sharing, the in-use
check and eviction all keep working.

```sh
# on each host that should keep it in memory; add it to /etc/fstab to survive a reboot
sudo mount -t tmpfs -o size=8g,mode=0777 tmpfs /var/lib/zoomies/shared/cache/pools
```

```yaml
cache:
  enabled: true
  scope: pool
  source: /var/lib/zoomies/shared/cache/pools   # the mounted tmpfs
  size_limit: 6442450944                        # 6 GiB, inside the mount's 8g
```

Three things are different from a cache on disk:

- **Set a size limit.** Memory is the host's disk here, and the limit is the only
  thing evicting from it. With none, the cache grows until the host has no memory
  left, which takes every runner and the agent with it. A source under `/dev/shm`
  with no limit is warned about as `pool.cache_memory_unbounded`. Keep the limit
  inside the mount's own `size=`, with room to spare.
- **It is lost at a reboot.** A cache is disposable accelerator data and a missing
  one is recreated empty, so nothing breaks; the first runner after a restart
  just pays for the downloads again.
- **It is not charged to a runner's memory limit.** The mount belongs to the host,
  so a job's cgroup is not where it is counted, but page cache a job dirties may
  be. Leave memory beyond the limit for the runners themselves.

A tool cache lives under the host's shared folder rather than the cache source,
so it stays on disk unless that folder is the mount.

## Using the pool cache from a workflow

Zoomies mounts the cache and nothing more: it does not know which package
manager a job runs, so a workflow has to point its tools at
`/opt/zoomies-cache`. The recipes below do that in one step, and each keeps the
rule the cache is built on; a missing or broken cache makes a job slower and
never makes it fail. Every one creates its own folder under the cache and only
uses it if that folder turned out writable, so the same workflow still runs on
GitHub's hosted runners, on a pool with the cache off, on a cache folder whose
permissions went wrong, and on the morning an operator emptied it.

The pool's `scope` names the cache namespace; it does not restrict which job
GitHub assigns to an organisation runner. A workflow cannot turn a shared mount
into a security boundary. Keep untrusted jobs on a separate pool without access
to a trusted cache.

**Go.** The module and build caches are both safe to share between concurrent
jobs; the Go toolchain locks and verifies them itself.

```yaml
- name: Use the pool cache for Go
  run: |
    d=/opt/zoomies-cache/go
    if mkdir -p "$d/mod" "$d/build" 2>/dev/null && [ -w "$d/mod" ] && [ -w "$d/build" ]; then
      echo "GOMODCACHE=$d/mod" >> "$GITHUB_ENV"
      echo "GOCACHE=$d/build" >> "$GITHUB_ENV"
    fi
```

Set it before `actions/setup-go`, and give that action `cache: false`, so the
job does not also upload the same modules to GitHub's cache.

**npm.** npm's cache is content-addressed and tolerates concurrent writers.
Keep `npm ci`: it still installs `node_modules` fresh, it only stops downloading
the tarballs again.

```yaml
- name: Use the pool cache for npm
  run: |
    d=/opt/zoomies-cache/npm
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then
      echo "npm_config_cache=$d" >> "$GITHUB_ENV"
    fi
```

**pip.** pip keeps downloads and built wheels in one folder, and a wheel built
once is not built again.

```yaml
- name: Use the pool cache for pip
  run: |
    d=/opt/zoomies-cache/pip
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then
      echo "PIP_CACHE_DIR=$d" >> "$GITHUB_ENV"
    fi
```

**Maven.** The local repository is the one of the four that two jobs writing at
once can corrupt, so the recipe turns on the file locking Maven 3.9 and later
ship. On an older Maven, or a pool with `max_runners` above one and no locking,
prefer `actions/cache` instead. It adds to any `MAVEN_OPTS` an earlier step set
rather than replacing it.

```yaml
- name: Use the pool cache for Maven
  run: |
    d=/opt/zoomies-cache/maven
    if mkdir -p "$d" 2>/dev/null && [ -w "$d" ]; then
      echo "MAVEN_OPTS=${MAVEN_OPTS:+$MAVEN_OPTS }-Dmaven.repo.local=$d -Daether.syncContext.named.factory=file-lock -Daether.syncContext.named.nameMapper=file-gav" >> "$GITHUB_ENV"
    fi
```

**BuildKit.** A pool with `docker_mode: dind` gives each runner a daemon that
dies with it, so its layer cache goes too. Buildx can export that cache to a
folder and read it back on the next runner. The `local` exporter needs a
`docker-container` builder, which is what `docker/setup-buildx-action` creates.

The exporter writes a whole cache at once, and two jobs exporting to one folder
(two runs, or two jobs of one matrix, which share a run ID) can leave a
half-written one. So each job exports to a folder `mktemp` made for it alone,
and when its build succeeds it publishes that folder by renaming a link over
`current`, which is atomic: a reader sees the old cache or the new one, never
half of either. When two jobs finish together the last one to rename wins, and
the other's export is simply not used. `ignore-error=true` keeps a failed
export from failing a build that otherwise succeeded, and a reader that finds
its cache replaced mid-build gets a slower build, not a failed one.

The build itself always runs. Only the cache flags depend on the cache being
usable, and a pull request reads the cache without writing to it (see below).

```yaml
- uses: docker/setup-buildx-action@<pinned commit>
- name: Build, with the pool cache when it is usable
  run: |
    base=/opt/zoomies-cache/buildkit/app
    args=() src="" out=""
    if mkdir -p "$base" 2>/dev/null && [ -w "$base" ]; then
      if [ -f "$base/current/index.json" ]; then
        src=$(readlink -f "$base/current")
        args+=(--cache-from "type=local,src=$src")
      fi
      if [ "$GITHUB_EVENT_NAME" != pull_request ] && out=$(mktemp -d "$base/export.XXXXXX"); then
        args+=(--cache-to "type=local,dest=$out,mode=max,ignore-error=true")
      fi
    fi
    docker buildx build "${args[@]}" --tag app:ci .
    if [ -n "$out" ] && [ -f "$out/index.json" ]; then
      if ln -s "$out" "$out.link" && mv -T "$out.link" "$base/current"; then
        if [ -n "$src" ]; then rm -rf "$src"; fi
      else
        rm -rf "$out" "$out.link"
      fi
    elif [ -n "$out" ]; then
      rm -rf "$out"
    fi
```

Name the folder after the image and, if the pool builds for more than one
platform, the platform too, so two builds do not replace each other's cache.
The cache is not pruned by Buildx; the pool's `size_limit` is what bounds it.
To share a cache across hosts rather than across runners on one host, use a
registry cache instead, [the recipe below](#dind-and-buildkit) shares layers
without sharing a live daemon.

**Pull requests.** Everything a job writes to the cache is read by the jobs
after it, so a pull request that can change the build can put something in the
cache a later build on the default branch will use. GitHub's own cache stops
that by letting a pull request read its base branch's entries and write only
its own; a folder cannot. For workflows you control, the BuildKit recipe above reads but does not
export on a pull request. Add `if: github.event_name != 'pull_request'` to the
package-manager steps to leave those jobs using their normal cache folders.
These are workflow conventions, not access controls: code with a writable
mount can bypass them. Keep untrusted pull requests on a separate pool with no
trusted cache mounted. [Security](security.md) explains why a public repository
needs particular care with self-hosted runners.

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
