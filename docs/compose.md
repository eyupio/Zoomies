---
icon: material/view-grid-outline
title: GitHub Actions runners with Docker Compose
description: >-
  Running Zoomies with Docker Compose: the compose file in the repository, the
  three values .env needs, what moves to the browser, and how to upgrade.
---

# Docker Compose

The web UI is how a Zoomies fleet is run day to day, and it does not care how
the controller under it was started. This page is for the people who would
rather start it with `docker compose up -d` than with the installer, because
that is how everything else on the host runs, because the deployment lives in a
repository, or because the installer's questions have already been answered
once and a file remembers them better than a person does.

There are two ways to get a compose deployment, and they meet at the same
place.

## From the installer

`zoomies init` offers **Docker Compose** whenever the host has a `compose`
command, and makes it the default. It writes a `docker-compose.yml` and a
**fully populated `.env`** (external URL, a freshly generated encryption key,
bind address, TLS mode, trusted proxies, backend, capacity, paths, the image
tag, the published port and the host's real docker group id) then brings the
stack up. Every variable carries a one-line comment saying what it is for; the
file is `0600` because it holds your encryption key; and it is written
atomically, so an interrupted install never leaves a half-written file that
compose would then read.

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- --deployment compose
```

Re-running it over an existing deployment is an upgrade, not a reinstall. It
**keeps the existing encryption key**, because minting a new one would make
every stored secret undecryptable, and backs the old file up beside it.

## From the repository's file

The `docker-compose.yml` at the repository root is the same deployment without
the installer, set up for a controller behind Cloudflare or another reverse
proxy that terminates TLS:

```sh
git clone https://github.com/eyupio/zoomies && cd zoomies
cp .env.example .env
$EDITOR .env          # ZOOMIES_EXTERNAL_URL, ZOOMIES_ENCRYPTION_KEY, DOCKER_GID
mkdir -p data && sudo chown 65532:65532 data   # the container runs as 65532
docker compose up -d
docker compose logs zoomies | grep 'setup token'
```

Three values are required, and compose refuses to start with any of them
missing, because each one left to a default produces a container that is
healthy and useless:

| Variable | What it is | Why it cannot default |
| --- | --- | --- |
| `ZOOMIES_EXTERNAL_URL` | The https address you and GitHub reach the controller at. | Webhooks are delivered there, the GitHub App's creation flow sends your browser back there, and the `https` is what makes the session cookie Secure. |
| `ZOOMIES_ENCRYPTION_KEY` | `openssl rand -base64 32`. | Everything secret in the database is sealed with it. Back it up separately from the database; without it the stored App key cannot be read. |
| `DOCKER_GID` | The gid that owns `/var/run/docker.sock`: `stat -c '%g' /var/run/docker.sock`. | It is not always the group called `docker`. With the wrong number the container comes up healthy and can start nothing. The Hosts page then says which group it holds and which line to change, and `docker compose up -d` recreates it. |

If `stat` prints `0`, the socket belongs to root's group, and the answer is
not to put the container in it: give the socket a group of its own
(`sudo groupadd docker`, then restart the daemon) or use a rootless daemon.

The database lives in `./data` beside the compose file, bind mounted, so a
backup is a copy of a directory and inspecting it needs no `docker volume
inspect`. It has to be owned by uid 65532: a bind mount keeps the host
directory's ownership, the container is not root, and Docker would create a
missing `./data` as root, so the file refuses to create it and fails with
"bind source path does not exist", which is easier to act on than SQLite's
error 14.

Every other `ZOOMIES_*` setting in [Configuration](configuration.md) can be
put in `.env` too, with no matching line in the compose file: it is passed
into the container whole. The explicit `environment:` entries in the file
still win.

## What moves to the browser

A container keeps its database in a volume nothing outside it can open, so the
three things the native installer does on the console happen in the web UI
instead, in this order:

1. **The first account.** Open the external URL and paste the setup
   token from the container's log. The token is what proves the instance is
   yours: the origin is reachable the moment the container starts, and an
   empty database is a thing a stranger can find too. It changes on every
   restart and stops being printed once an account exists.
2. **GitHub.** **Installations → Connect GitHub** creates the App through the
   manifest flow, with exactly the permissions Zoomies needs, and takes the
   private key and webhook secret directly.
3. **The first pool.** Nothing creates one for you on this path, and nothing
   runs until one exists. **Pools → Create a pool** opens the pool editor with a name
   and a label already filled in; [the quick start](quickstart.md#4-your-first-pool)
   walks the rest.

The Overview repeats these as a checklist that ticks itself off as you go,
ending with a test workflow to run once a pool exists.

Open the **https** address, not `http://<ip>`. The session cookie is marked
Secure because the external URL is https, a browser on a plain-http page
throws it away, and Zoomies refuses to sign you in from one and says why
rather than signing you in and out in the same second. To test over plain
http for a moment, set `ZOOMIES_COOKIE_SECURE=false` for the duration.

## The proxy in front

The file publishes port 80 and serves plain HTTP on it, with
`ZOOMIES_TRUSTED_PROXIES=cloudflare` so the audit log and the login rate
limiter see real client addresses rather than the proxy's. Zoomies warns at
startup that it is listening without TLS; in this deployment that is expected,
and the warning says so. Only the proxy should be able to reach the origin:
firewall it to Cloudflare's ranges, or use a Cloudflare Tunnel and publish no
port at all. [Behind Cloudflare (or any reverse
proxy)](configuration.md#behind-cloudflare-or-any-reverse-proxy) has the
three things that go quietly wrong here.

## The controller is also a host

The compose file runs the controller with an **embedded agent**, so this is one
container plus the runners it creates: the container holds the Docker socket,
and every pool on the `docker` backend can place runners on it. That socket is
Zoomies' own access to Docker, for creating runner containers; it is not
reachable from a job unless a pool's `docker_mode` says so.

`ZOOMIES_AGENT_CAPACITY` is the host's slot count, and it is the one value in
`.env.example` worth a second look: the example sets it to `4`, which is right
for a four-core box and wrong for most others. Leave it **empty** and the
agent works its capacity out from the machine it measures, which also sets the
share a pool sized by its host gives each runner; a constant pins every
deployment of the file to the same slot count however large the host is. The
installer's `.env` leaves it empty for that reason.

Every further host joins the same way it would on any other deployment:
**Hosts → Add a host** hands you one line to paste on the new machine. [Adding
a host](hosts-and-pools.md#adding-a-host).

## Operating it from a terminal

The CLI talks to the controller over the API, so it does not care that the
controller is in a container:

```sh
export ZOOMIES_URL=https://zoomies.example.com
export ZOOMIES_TOKEN=zoo_...          # Settings → API tokens, or zoomies tokens create
zoomies status
zoomies pools list
```

The deployment itself is driven with compose, and `zoomies deployment` wraps
the same commands using the file and container name recorded at install:

| Task | With compose | With the installer's record |
| --- | --- | --- |
| Logs | `docker compose logs -f zoomies` | `zoomies logs` |
| Restart | `docker compose restart zoomies` | `zoomies deployment restart` |
| Upgrade | `docker compose pull && docker compose up -d` | `zoomies deployment update`, which also pulls the cached runner images and rolls back on failure |
| Stop, keeping the database | `docker compose down` | `zoomies deployment down` |

`docker compose down -v` deletes the volume with the database in it, and is
the one command here that cannot be undone. [Upgrading a container
deployment](upgrading.md#upgrading-a-container-deployment) says what an
upgrade does to work in flight.

`zoomies config set` and `config unset` need the controller stopped, on every
deployment: they write the same settings table the **Settings** page does, and
writing under a process that has already read it would leave the two
disagreeing. On compose that is `docker compose stop zoomies` first, or, more
simply, use the Settings page, which is what it is for.

## What this is not

A compose deployment is a controller on a host you own, with that host's
Docker daemon running jobs. For a host you do not install on at all (a
platform that builds from source and gives a container no Docker socket) see
[Deploying on a PaaS](paas.md); for an instance booted from a provider's
marketplace image, see [One-click deployment](marketplace.md). Both deploy a
controller; the runner capacity is still yours.
