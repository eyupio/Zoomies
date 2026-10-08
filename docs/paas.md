---
icon: material/cloud-outline
description: >-
  Deploy the Zoomies controller from source on Coolify, Dokploy, Railway or any
  Nixpacks platform, then join the hosts that actually run your jobs.
---

# Deploying on a PaaS

There is a `nixpacks.toml` in the repository root, so any platform that builds
with [Nixpacks](https://nixpacks.com) -- Coolify, Dokploy, Railway, Zeabur,
Easypanel -- can deploy Zoomies straight from the source. Point it at
`github.com/eyupio/zoomies`, set the handful of variables below, give it a
volume, and it builds the UI, builds the binary and starts the controller.

Nothing about the build is special to any one platform: it is the same two
steps as `deploy/Dockerfile`, in the order that matters. `internal/api` embeds
`internal/api/webdist`, so the Svelte UI is built first and the Go build embeds
what it produced. Nixpacks' own Node and Go providers would each find half of
that and neither would wait for the other, which is why every phase is written
out in the file rather than detected.

## What you get, and what you do not

**A controller.** The API, the web UI, the webhook endpoint, the scheduler and
the database.

**No runners on that host.** A PaaS does not give a container a Docker socket,
and Zoomies will not pretend otherwise: `nixpacks.toml` sets
`ZOOMIES_AGENT_EMBEDDED=false`, so the controller comes up knowing it runs
nothing itself. Jobs would otherwise queue against a host that can never start
a container -- which the Overview would tell you, eventually, in the problems
panel. Runners come from agents you join on machines that do have a container
runtime; see [Adding a host](#adding-a-host-that-can-run-jobs) below.

That split is a reasonable shape, not a consolation prize: the controller is
the part you want always-on, backed up and reachable from GitHub, and the
runner hosts are the part you want close to your own network and cheap to
replace.

## Set these

| Variable | Why |
| --- | --- |
| `ZOOMIES_EXTERNAL_URL` | The https address the platform gives you. Webhooks are delivered to `$ZOOMIES_EXTERNAL_URL/webhooks/github`, the session cookie's `Secure` flag is derived from it, and every link in the UI is built from it. |
| `ZOOMIES_ENCRYPTION_KEY` | 32 random bytes, base64: `openssl rand -base64 32`. Everything secret in the database is sealed with it. Set it explicitly and store it in the platform's secret manager -- see [the volume](#the-volume) for what happens if you leave it to be generated. |
| `ZOOMIES_TRUSTED_PROXIES` | The address range of the platform's ingress proxy. Without it every audit row records the proxy rather than the person, and the login rate limiter throttles every caller as one client. |

`nixpacks.toml` sets the rest: `ZOOMIES_TLS_MODE=off` because the platform
holds the certificate, `ZOOMIES_STATE_DIR` and `ZOOMIES_CONFIG_DIR` at `/data`,
JSON logs, and a bind address taken from `$PORT` when the platform sets one.
Every one of them is an ordinary `ZOOMIES_*` variable from
[Configuration](configuration.md), so anything you set in the platform's own
environment wins over the file's default.

Zoomies will warn at startup that it is listening without TLS. In this
deployment that warning is expected, for the same reason it is expected behind
Cloudflare: the controller cannot see the proxy from behind it, so it has no
way to tell a terminated connection from an origin genuinely exposed in the
clear. [Behind Cloudflare (or any reverse
proxy)](configuration.md#behind-cloudflare-or-any-reverse-proxy) is the longer
version of this paragraph, and applies here unchanged.

## The volume

Mount the platform's persistent volume at **`/data`**, or set
`ZOOMIES_STATE_DIR` and `ZOOMIES_CONFIG_DIR` to wherever it did mount one.

`/data` holds the SQLite database, the runners' work area, and -- if you did
not supply `ZOOMIES_ENCRYPTION_KEY` -- the encryption key Zoomies generated on
first start. Without a volume, a redeploy gives you an empty database and a new
key, and the new key cannot decrypt the GitHub App private key the old one
sealed. That failure is silent until the next time Zoomies needs to
authenticate, which is why it is worth getting right before you connect GitHub
rather than after.

A backup is a copy of that directory, plus the encryption key if you kept it
somewhere else -- [Backup and restore](backup-and-restore.md) is the whole
procedure, and it is the same one here.

## Adding a host that can run jobs

Once the controller is up, create the first account in the UI, connect
GitHub, then generate a join token (**Hosts → Add a host**, or
`zoomies hosts join-token create --ttl 15m`) and run the line it gives you on a
machine with Docker or Podman:

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- \
  --mode agent \
  --controller https://zoomies.example.com \
  --join-token zoojoin_...
```

Agents connect outbound only, so that machine can sit behind NAT with no
inbound rule -- including on a laptop, a home server, or a box in the office
the PaaS could never reach.

Then make a pool. This path has no `zoomies init`, so nothing creates one for
you, and nothing runs until one exists: the **Pools** page, or
[Hosts and pools](hosts-and-pools.md) for what the settings mean.

## Upgrading

Redeploy. The platform rebuilds from the current `main`, the new binary starts,
and the first start applies any schema migrations -- there is nothing else to
run. [Upgrading](upgrading.md) is what that does to work in flight, and how far
a controller and its agents may drift apart, which matters more here than on a
single VM because the agents are upgraded separately from the controller.

## Building the same image yourself

```sh
make image-nixpacks        # needs the nixpacks CLI
```

It builds exactly what the platform builds, which is the way to check a change
to `nixpacks.toml` before pushing it.

## When not to use this

The published image is smaller and starts faster. A Nixpacks image carries the
Go and Node it built with, where `ghcr.io/eyupio/zoomies` is a static binary on
distroless and nothing else. If your platform can run a prebuilt image, run
that one and skip the build:

```
ghcr.io/eyupio/zoomies:latest
```

That tag is the newest full release. Pin `ghcr.io/eyupio/zoomies:vX.Y.Z` if you
would rather decide when your platform moves, or run `:dev` to track `main`.
[Which image tag to run](upgrading.md#which-image-tag-to-run) has the rest.

The variables above are the same either way. Nixpacks earns its place when the
platform builds from a repository and you would rather not maintain a second
answer to "how is this built".
