---
icon: material/play-circle-outline
title: "Try the demo fleet (no GitHub needed)"
description: >-
  Open the Zoomies web UI on a seeded fleet — two pools, hosts, a dozen runners
  and a morning of jobs — with one pasted block: no GitHub App, no Docker, no
  account and no real jobs. For looking around only, so authentication is off.
---

# Try the demo fleet

The quickest way to find out whether the UI is as good as its screenshots is to
open it, and that needs no GitHub organisation, no Docker host and no server.
This block installs the binary and starts a controller whose database is
already full: the fleet the screenshots on [the UI page](ui.md) were taken from.

```sh
curl -fsSL https://zoomies.sh/install.sh | sh -s -- --no-init
ZOOMIES_SEED_DEMO=true ZOOMIES_DISABLE_AUTH=true ZOOMIES_BIND=127.0.0.1:8080 \
  ZOOMIES_ENCRYPTION_KEY=$(openssl rand -base64 32) ZOOMIES_DB_PATH=/tmp/zoomies-demo.db \
  zoomies controller
```

Then open <http://localhost:8080> in your browser. It wants Linux or macOS, with
`curl` and `openssl`, and port 8080 free; to use another, change it in
`ZOOMIES_BIND` and in the address you open.

## What you are looking at

Two pools, three hosts, a dozen runners in every state the controller knows, and
a morning's worth of jobs, with a little trouble in it on purpose so that the
problems panel has something to say. Your own machine turns up as one more host,
because a controller runs an agent inside itself. [The UI, page by
page](ui.md) says what each screen is for.

None of it is real. The hosts, runners and jobs are rows the controller writes
into its database the first time it starts: no job runs, no runner is ever
started and no GitHub App is involved, so there is nothing to create on GitHub
and no Docker to install. The log will warn that no external URL is set and that
authentication is off — and, on a machine without Docker, that Docker is not
available. In the demo all of it is expected.

The first line installs the binary and stops: `--no-init` skips the setup
questions, so there is no service and no service user. It shows you what it is
about to do, asks once, and may ask for your password to write to
`/usr/local/bin`. The second starts the controller in the foreground. Its
encryption key is made up on the spot, because there is nothing in the demo
worth keeping a key for.

## Authentication is off

The demo has no sign-in: anyone who can reach its port is an administrator. That
is why the block binds `127.0.0.1`, so that only this machine can reach it, and
why the controller will not start without authentication if it is given any
other bind address, an external URL or a trusted proxy. Keep the demo on
loopback, and do not reuse the block for anything real — the
[quick start](quickstart.md) is the real install, with accounts and
authentication on.

## Taking it away

Ctrl-C stops the controller. The fleet is one file, so
`rm -f /tmp/zoomies-demo.db*` removes it along with its lock file, and running
the block again after that gives you a fresh morning. The installer left the
binary at `/usr/local/bin/zoomies`, and the controller's built-in agent keeps a
small state file, `work/agent.json`, in a `zoomies` folder under your user
configuration directory: `~/.config/zoomies` on Linux,
`~/Library/Application Support/zoomies` on macOS, `/var/lib/zoomies` if you ran
the demo as root. It holds nothing but the demo agent's own credential.

When you want a fleet of your own, the [quick start](quickstart.md) takes about
five minutes.
