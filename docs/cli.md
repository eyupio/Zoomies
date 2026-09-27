---
title: "Command line: drive a runner fleet from a terminal"
description: >-
  Every `zoomies` command, what it does and the flags it takes: running the
  controller and agent, driving a fleet from a terminal, and setting a host up.
---

# The command line

One binary, chosen by subcommand. `zoomies controller` is the control plane,
`zoomies agent` is a runner host, `zoomies init` sets a machine up, and the rest
drive a fleet over the same REST API the UI uses.

There are **no global flags**. Every command declares its own, so
`zoomies pools list --help` is the complete truth about that command and there
is nothing to learn about the others first. `zoomies help` lists the commands,
`zoomies <group> help` lists a group's, and `--help` on any command prints its
flags and an example.

Exit codes are the usual three — `0` it worked, `1` it ran and failed, `2` it was
invoked wrongly — plus one of the controller's own: `3` when it stopped because
the settings page asked it to restart, which is how a [staged
restore](backup-and-restore.md#from-the-settings-page) is applied.
Non-zero on purpose, so a service manager set to restart on failure starts it
again.

## Talking to a controller

Everything under *Your fleet* below takes the same connection flags:

| Flag | Default | |
| --- | --- | --- |
| `--url` | | The controller's base URL. |
| `--token` | | An API token. `zoomies tokens create` mints one. |
| `--ca-file` | | A CA bundle, for a private certificate authority. |
| `--insecure` | `false` | Skip certificate verification. For a self-signed certificate you have decided to trust. |
| `--timeout` | `30s` | |
| `--output` | `table` | `table`, `json` or `yaml`. On commands that read something; a command that only acts has no output to shape. |

The listing commands add `--limit` (50), `--offset`, `--sort` and `--order`.

## Running Zoomies

| Command | What it does |
| --- | --- |
| `zoomies controller [--config path] [--takeover]` | Run the control plane: the scheduler, the API, the web UI and the webhook endpoint. On a single VM it runs an agent inside itself. It refuses to start when another controller holds the database, naming which machine and process has it; `--takeover` starts anyway, for the case where you know the other one is gone and cannot be asked. |
| `zoomies agent [--config path]` | Run this host's agent: long-poll a controller for work, start and stop runners, report what happens. Also takes `--controller` and `--join-token` for a host configured entirely from flags, and `--log-file` to append the log to a file instead of stderr, which is how the Windows service keeps one. |
| `zoomies agent join <controller-url> --token <join-token>` | Enrol this host: redeem the token, write the credentials, install the service. |
| `zoomies agent install` | Install the agent's service on a machine about to become a provider's template, joined to nothing. The unit reads `/etc/zoomies/zoomies.env`, which the controller writes into each clone, and is left disabled for the controller to enable there. It refuses a machine that has already joined, because its `agent.json` would be cloned into every machine. See [Proxmox VE](proxmox.md#preparing-the-template). |
| `zoomies gateway --target <host:port>` | Publish a private provider API — a Proxmox cluster on your home network, say — to a controller over Tailcat. It prints the address the provider form asks for, keeps its identity in `--state-dir` so a restart keeps the same address, and forwards every connection that arrives through the tunnel to the one target. `--quiet` keeps the address out of a service's log. See [Private hosts and providers](private-hosts.md#private-providers). |

`agent join` takes the host's shape as flags — `--name`, `--capacity`,
`--labels`, `--backend`, `--docker-host` — plus the TLS trio (`--ca-file`,
`--client-cert`, `--client-key`), `--no-service` to skip installing one, and
`--non-interactive` with `--yes` for automation.

## Your fleet

### `zoomies status`

The Overview in a terminal: counts, pools, recent scaling and anything wrong.
`--window` (`1h`) sets the period the rates cover, `--scaling` (`5`) how many
recent decisions to print.

### `zoomies pools`

What runners to make, and how many.

| Command | What it does |
| --- | --- |
| `pools list` | Every pool, with its live counts and utilisation. |
| `pools get <pool-id>` | One pool in full, including any dangerous settings. |
| `pools create` | Create a pool. The server validates exactly as the UI's wizard does, so `--dry-run` gives you that verdict without creating anything. |
| `pools edit <pool-id>` | Change the settings you name. Anything you do not name is left alone. |
| `pools delete <pool-id>` | Delete it. Its runners drain first unless `--force`. |
| `pools enable` / `pools disable` | Let a pool create runners, or stop it. Disabling interrupts nothing: existing runners drain as they go idle. |
| `pools prewarm <pool-id>` | Pre-pull the pool's image on every matching host. |
| `pools export [--file pools.yaml] [--format yaml\|json]` | Write every pool to a file another instance can import. Installations are named by what they cover, and no environment value is in it. See [moving pools](backup-and-restore.md#moving-pools). |
| `pools import <file> [--dry-run] [--skip <pool>,...]` | Create and change pools to match an export, pool by pool and setting by setting. Nothing is written while any pool is refused; `-` reads stdin. |

`create` and `edit` share one set of flags. The ones worth knowing:
`--name`, `--installation`, `--labels`, `--backend` (`docker`), `--image`,
`--min` (`0`), `--max` (`4`), `--idle-timeout` (`5m`), `--ephemeral` (`true`),
`--docker-mode` (`none`), `--run-as-root` (`false`), `--host-selector`, the
resource limits `--cpus`, `--memory-mb`, `--disk-gb`, and the [elastic
CPU](elastic-cpu.md) policy, `--cpu-burst` (`off`, `observe` or `automatic`)
with `--cpu-burst-max` as its ceiling in cores; on a create the ceiling needs
the mode beside it. Both are read live, so an edit reaches runners already
running.

On `edit`, only the flags you actually type are sent — the defaults above are
not applied to a partial update, so editing a pool's image cannot silently reset
its ceiling.

### `zoomies runners`

The runners that exist right now.

| Command | What it does |
| --- | --- |
| `runners list` | Terminal runners are hidden unless you ask with `--include-removed`. Filter with `--pool`, `--host`, `--state` (repeatable) and `--q`. |
| `runners get <runner-id>` | One runner, its current job and how it got here. |
| `runners drain <runner-id>...` | Stop taking new work and exit. A job still running is given five minutes to finish; if it takes longer the runner is stopped and GitHub marks that job failed, so draining a busy runner asks first. |
| `runners delete <runner-id>...` | Remove and deregister from GitHub. Drains first unless `--force`. |
| `runners logs <runner-id>` | Print the output. `--follow` keeps printing it, `--tail` (`1000`) sets how much history. |

### `zoomies jobs`

`jobs list` is job history newest first, filtered by `--repo`, `--workflow`,
`--pool`, `--state`, `--conclusion`, `--q`, and a window with `--since` and
`--until` — each of which takes a duration (`24h`) or a date. `--unmatched`
and `--failed` are the two questions worth a switch of their own.
`jobs get <job-id>` shows one.

`--ours` and `--theirs` split `--failed` by whose failure it was. GitHub records
both halves as `failure`, so this is the only place the question can be asked,
and only one half is anybody here's to fix. `--fault` narrows the fleet's half
to a category — `out_of_memory`, `image`, `registration`, `backend` and the rest
— and a category this build does not know is refused rather than quietly
matching everything.

```sh
zoomies jobs list --ours --since 24h       # what this deployment broke today
zoomies jobs list --fault out_of_memory    # and how much of it was memory
```

`jobs rerun <job-id>` asks GitHub to run that run's failed jobs again, which is
the remedy for a job the fleet broke: nothing about the workflow has changed.
GitHub has no job-level re-run, so it re-runs **every** failed job in the run.
It refuses a job that has not finished and one that did not fail, and it does
not ask whose fault the failure was — if you have looked at one and decided to
run it again, that is your call to make.

### `zoomies hosts`

| Command | What it does |
| --- | --- |
| `hosts list` | The hosts that have joined. |
| `hosts cordon <host-id>` | Stop scheduling new runners onto it. What it already has keeps running. |
| `hosts uncordon <host-id>` | Let it accept runners again. |
| `hosts drain <host-id>` | Cordon it, then drain every runner on it, so it empties as its jobs finish. The order matters: draining an uncordoned host means the scheduler puts fresh runners on it while the old ones are still going. Each runner gets five minutes to finish what it is on; a longer job is stopped, which is what makes the host actually empty. |
| `hosts delete <host-id>` | Forget it. Refused while it has live runners, unless `--force`. |
| `hosts join-token create` | Mint a single-use join token: `--ttl` (`15m`), `--capacity` (`2`), `--labels`, `--controller`. Shown once; only its hash is stored. |

### `zoomies providers`

| Command | What it does |
| --- | --- |
| `providers list` | Every provider machines can be rented from, how many it has, and whether it may buy more. A provider that is buying nothing says why in a sentence below the table. |
| `providers kinds` | What this build can rent machines from, and the settings each kind asks for — the list `--setting` on the next command chooses from. |
| `providers add <kind>` | The wizard in one line — the UI's review step shows the exact line for what it holds, with a copy button. `--name`, `--endpoint`, `--nodes`, `--template`, `--storage` and `--bridge` are the Proxmox form's questions as flags; `--vmid-range 9000-9099`, `--max-machines`, `--capacity`, `--labels` and the machine's shape follow, and `--setting key=value` reaches anything else. The API token is asked for without echo, read from standard input when that is not a terminal, or taken from `--credential-file`; `--endpoint-ca-file` is the cluster's CA. It validates the answers first — a wrong one is named and nothing is saved — then creates the provider, then runs the live check and prints what the cluster would refuse. The ceiling is zero unless `--max-machines` says otherwise, so a connection test never starts an invoice. |
| `providers edit <name\|id>` | Change the settings you name and nothing else: `--max-machines 8` raises the ceiling, `--credential-file` rotates the token, `--storage ceph` changes one driver setting and keeps the rest. It takes the same flags as `add`. |
| `providers check <name\|id>` | The live preflight: ask the hypervisor what it would refuse, changing nothing there. Exits non-zero if anything would stop a machine being created, and prints what to fix in the same words the setup wizard uses. |
| `providers pause <name\|id>` | Stop buying new machines. `--reason` is kept for the card and the audit row. Machines that exist keep running, and drains, deletes and recovery carry on. Pressing it twice is not an error. |
| `providers resume <name\|id>` | Let it buy machines again. If something else is still holding it — the fence a restore sets, the configuration, a ceiling — the answer says so rather than claiming the fleet is buying. |
| `providers machines` | The machines that exist right now, filtered by `--provider` and `--state`, with `--include-deleted` for the ones whose resource is confirmed gone. A stuck machine prints the provider's own words and the task handle to paste into its console. |
| `providers orphans <name\|id>` | The three ways a row and a real resource can disagree: resources with no row, rows holding no resource, and machines nobody can vouch for. It deletes nothing — every line is for a person to decide. |

There is no `providers machines delete`. Destroying a machine destroys a VM
somebody is paying for, and the row is the only record that it exists, so it is
`DELETE /api/v1/machines/{id}` with a refusal to read first — see
[the API surface](api-surface.md#providers-and-machines).

### `zoomies installations`

`installations list` shows the GitHub App installations pools register with —
never any key material. `installations verify <installation-id>` asks GitHub
whether the credentials and permissions are still what Zoomies needs, which is
the first thing to run when registration starts failing.

### `zoomies export` and `zoomies import`

`export --installation <installation-id>` writes everything about one
installation — its pools, runners, jobs, deliveries, scaling events, runner
sessions and the audit rows that name them — as one archive (`--out`, by
default `zoomies-installation-<id>.json`, mode 0600). With
`--passphrase-file` the App's private key and webhook secret are sealed under
that passphrase, so the archive can be imported on another instance without
this one's key. `import <archive> --passphrase-file FILE` writes it onto this
instance, all of it or none. See
[moving one installation](backup-and-restore.md#moving-one-installation-or-removing-its-history).

### `zoomies audit`

`audit list` is who did what, newest first, filtered by `--actor`, `--action`,
`--target-kind`, `--target`, `--q`, `--since` and `--until`. `audit tail` prints
the last few (`--limit`, `10`) and then follows the live stream until you
interrupt it.

### `zoomies diagnostics`

Collects a support bundle — this instance, its fleet, its configuration and
everything currently wrong, in one JSON document — and writes it to a file
named after the instant the controller took it. `--file` names the file
yourself, `--stdout` (or `--output json`) sends it to a pipe instead.

The terminal summary is there so you know what you are about to attach: how
many pools, hosts, runners and unfinished jobs went in, which sections the
controller could not gather, and which were shortened. No workflow log is in
it — the bundle names the runners whose logs a support case is likely to want
and the route that fetches each, so you choose what leaves the fleet.

It needs an admin token, because the document contains the settings section.

### `zoomies users` and `zoomies tokens`

| Command | What it does |
| --- | --- |
| `users list` | The accounts that can sign in. |
| `users create` | `--username` and `--role`; omit `--password` for an account that signs in through single sign-on. |
| `users passwd <user-id>` | Set a password. Read from the terminal without echo, or from stdin when piped — never a flag, because a password in a flag is a password in the shell history. |
| `users reset-two-step <user-id>` | Turn off two-step verification for somebody who has lost their authenticator and their recovery codes. Their sessions end and the reset is audited; see [Two-step verification](two-step.md#lost-your-phone). |
| `users delete <user-id>` | Refused if it would leave no enabled administrator. |
| `tokens list` | Metadata only. The value is not stored. |
| `tokens create` | `--name`, `--role`, repeatable `--scope`, `--expires-in`. Printed once; only its hash is kept. |
| `tokens revoke <token-id>` | Immediate. |

### `zoomies mcp`

Serves the fleet to a coding agent over the
[Model Context Protocol](https://modelcontextprotocol.io), on standard input
and output. The agent's MCP configuration starts it; it is not a command to
run by hand. It is an API client like every other command here: it takes the
same `--url`, `--token` and `ZOOMIES_*` credentials, calls the documented
routes, and adds nothing to the controller — no route, no setting, no
authority beyond the token's.

```sh
zoomies tokens create --name claude-code --role viewer --expires-in 720h
claude mcp add zoomies \
  -e ZOOMIES_URL=https://zoomies.example.com -e ZOOMIES_TOKEN=zoo_... \
  -- zoomies mcp
```

In PowerShell, run it as one line and quote the `--`: PowerShell does not take
`\` as a line continuation, and it drops a bare `--` before a script such as
`claude` sees it, which leaves `-e` to swallow `zoomies mcp` and fails with
`missing required argument 'commandOrUrl'`.

`zoomies mcp` runs on the machine the agent runs on, not on the controller, so
that machine needs the `zoomies` binary. The `dev` release carries `mcp` until a
versioned release does. On Windows, fetch it with `curl.exe` — plain `curl` in
Windows PowerShell is `Invoke-WebRequest` — and give its full path:

```powershell
New-Item -ItemType Directory -Force "$HOME\bin" | Out-Null
curl.exe -fL -o "$HOME\bin\zoomies.exe" https://github.com/eyupio/zoomies/releases/download/dev/zoomies_windows_amd64.exe
& "$HOME\bin\zoomies.exe" mcp --help
claude mcp add zoomies -e ZOOMIES_URL=https://zoomies.example.com -e ZOOMIES_TOKEN=zoo_... '--' "$HOME\bin\zoomies.exe" mcp
```

On macOS or Linux, the same file for the platform — `zoomies_darwin_arm64`,
`zoomies_darwin_amd64`, `zoomies_linux_amd64` or `zoomies_linux_arm64`:

```sh
mkdir -p ~/.local/bin
curl -fL -o ~/.local/bin/zoomies https://github.com/eyupio/zoomies/releases/download/dev/zoomies_darwin_arm64
chmod +x ~/.local/bin/zoomies
```

`checksums.txt` on the same release lists each file's SHA-256.

A token made on the **API tokens** settings page works the same way. `claude
mcp get zoomies` should then show `Command: zoomies` and `Args: mcp`; if an
earlier attempt stored something else, `claude mcp remove zoomies` and add it
again.

| Tool | What it answers |
| --- | --- |
| `fleet_status` | Queued and running jobs, outcomes, queue wait and pool utilisation over a window. `GET /stats`. |
| `list_problems` | Everything the controller thinks is wrong, with what to do. `GET /problems`. |
| `list_jobs` | Jobs, with the same filters as `zoomies jobs list`: `failed`, `ours`, `theirs`, `unmatched`, `repo`, `since`. |
| `get_job` | One job, its timeline and the controller's explanation, as one document. |
| `get_runner_log` | The last lines of a runner's output, while the runner still exists. |
| `list_runners`, `list_pools`, `list_hosts` | The fleet's resources as their `GET` routes return them. |
| `rerun_job` | Only with `--allow-actions`. `POST /jobs/{id}/rerun`; needs `operator`. |
| `drain_runner` | Only with `--allow-actions`. `POST /runners/{id}/drain`, never with `confirm`, so a busy runner is refused rather than having its job stopped; needs `operator`. |

Give it a `viewer` token, narrowed with `--scope` if the agent only needs some
of the fleet. `--allow-actions` offers the two actions to the agent but grants
nothing: a viewer token's re-run is still refused by the controller, and the
agent is told which role was missing.

A workflow's log, and its job, step and branch names, are text anyone who can
open a pull request can write — so they are prompt-injection material for the
agent reading them. The server tells the agent so when it connects, and
returns a log in a block of its own after a notice saying it is untrusted, so
the log's own text cannot pass itself off as Zoomies speaking. That is why the
actions are off by default: an agent that reads logs and can also act is an
agent a pull request can try to steer.

## Setting up and looking around

| Command | What it does |
| --- | --- |
| `zoomies init` | Set this host up: how it runs, backend, listener, GitHub App and the first account. `--answers` takes a file and implies `--non-interactive`; `--print-answers` writes one out from an interactive run so the next host can be identical. |
| `zoomies update [--check] [--yes \| --non-interactive]` | Short, operator-friendly alias for `zoomies upgrade`. It accepts the same flags and keeps the existing spelling compatible with scripts. |
| `zoomies upgrade [--check] [--yes \| --non-interactive]` | Apply the installed binary and matching images to an existing native, Compose or Docker deployment. Keeps configuration and credentials; `--check` changes nothing. What this release expects and the deployment lacks — the shared folder, its mount, a deleted Compose file — is added at a terminal once you agree, with `--yes` without asking, and never with `--non-interactive`. To download the binary too, use `install.sh --upgrade`. See [Upgrading](upgrading.md#what-a-release-adds-to-the-host). |
| `zoomies logs` | Show the latest 100 controller log lines for the recorded Compose or Docker deployment. Alias for `zoomies deployment logs`. |
| `zoomies deployment <action>` | Operate the container deployment recorded by `zoomies init`: `status`, `logs`, `start`, `stop`, `restart`, `update`, or `down`. `update` pulls the controller image matching this binary plus cached runner images, recreates safely, and rolls back on failure. `down` keeps the database volume. |
| `zoomies uninstall` | Remove the service or container, the database, the encryption key and the configuration. |
| `zoomies backup [--dir path] [--keep N] [--include-key] [--no-offsite]` | Take a consistent copy of this host's database into a timestamped directory, with a manifest recording the build, the migration ledger, the encryption key's fingerprint, what that key is needed for, and the blanked configuration. Reads the database file directly, so it works when the controller will not start. The copy is then sent to every destination this fleet has — those `backup.remotes` describes and those stored from the Backups page, which this command reads out of the database it has just copied — with `--remote <name>` for one and `--no-offsite` for none. A destination that refuses is reported without failing the backup, which is on the disk either way. See [Backup and restore](backup-and-restore.md). |
| `zoomies restore <backup-directory> [--replace]` | Put a backup's database back at `database.path`, after checking that the copy is sound, that this build can read its schema, and that this host's encryption key is the one that sealed it. Ends every session, removes unredeemed join tokens, and fences the fleet; `--revoke-api-tokens` and `--reset-agent-tokens` go further. `--replace` is required to overwrite an existing database, and moves it aside rather than deleting it. See [Backup and restore](backup-and-restore.md). |
| `zoomies restore --from-remote <name> [<id>\|latest]` | The same restore, on a host that has the configuration file and the encryption key and nothing else — a destination stored in the database is found too when there is a database to read: the copy is fetched from that remote into the backup directory, decrypted with the destination's passphrase — or `--passphrase` for one sealed with a passphrase the configuration no longer carries — verified, and then restored. Naming no backup lists what the remote holds, because an operator in front of an empty machine has no way to know the ids. |
| `zoomies config check [--config path]` | Validate a file without starting anything. Warnings print and exit 0; errors exit 1. |
| `zoomies config print [--config path]` | The effective configuration — file, environment and defaults combined — with secrets blanked. `--output` is `yaml` or `json` here, and defaults to `yaml`. |
| `zoomies config list [--all]` | What this fleet has stored, and which layer each value came from. `--all` lists every setting, including the ones nobody has changed. |
| `zoomies config get <key>` | One setting's effective value, and whether it came from the defaults, the file, the database or the environment. |
| `zoomies config set <key> <value>` | Store one setting in the fleet's database — the same thing the settings page does, for when the settings page is the thing that is broken. |
| `zoomies config unset <key>` | Forget a stored setting, so the configuration file or the built-in default decides it again. |
| `zoomies config import-env [file]` | Store every setting an environment file sets — standard input when no file is given — in one write, and name the variables that cannot live in the database. The installer runs it in a one-off container to put a controller's settings in its database; see [Settings that were in `.env`](upgrading.md#settings-that-were-in-env). |
| `zoomies healthcheck --url <url>` | Probe a controller's `/healthz`. Exit 0 when it answers. This is what the container image's `HEALTHCHECK` runs. |
| `zoomies version` | The version this binary was built from. `--short` or `--json`. |

`config set`, `config unset` and `config import-env` need the controller stopped: it holds the
database lock, and writing settings under a process that has already read them
would leave the two disagreeing with no way for either to find out. Everything
else in this group reads only.

`init` also accepts eight `--detected-*` flags. They are how `install.sh` passes
on what it already probed, and you will not normally type one.

The deployment commands read `deployment.json`, so they use the exact Compose
command, file, container name and image recorded during installation. Pass
`--config-dir` only for an installation outside the platform default. For a
custom controller image, `deployment update --image <ref>` names the intended
replacement explicitly.

`uninstall`'s `--deregister` and `--volumes` are three-state in practice: typed,
they mean what they say; untouched, they mean *ask*. In `--non-interactive` mode
name the ones you mean.

## Two asymmetries worth knowing

`users create --password` exists and `users passwd --password` deliberately does
not: creating an account is often scripted from a secret store, while changing
one is something a person does at a terminal, where the shell history is the
risk.

`healthcheck` declares its own connection flags rather than sharing the set
above, so its `--timeout` is `5s` rather than `30s` and it has no `--token`. It
is meant to run from a container's health check, where five seconds is already
generous and there is no token to hand.

A container's health check names `http://127.0.0.1:8080` whether or not the
controller serves TLS. When a loopback address answers that it was sent plain
HTTP by an HTTPS server, `healthcheck` asks the same address over `https`
without checking the certificate: the listener is this host's own, and its
certificate may be self-signed. `--ca-file` still verifies against that
certificate when given. An address off the host is never moved to `https`.
