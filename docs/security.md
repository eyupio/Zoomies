---
title: Security of self-hosted GitHub Actions runners
description: >-
  The Zoomies threat model, what a self-hosted runner exposes, and what each
  setting that weakens the safe defaults actually costs.
---

# Zoomies security

This document says what Zoomies protects, what it does not, and what every
dangerous setting actually costs you. Nothing here is hypothetical — each toggle
below is a real setting, and each one produces a named warning at startup and in
the UI's problems drawer when it is on.

---

## 1. The fundamental problem

**A self-hosted runner executes code from your repositories.** Anyone who can
cause a workflow to run — which, on a public repository, is anyone who can open
a pull request — can execute arbitrary code inside your runner.

GitHub's own guidance is blunt about this: do not use self-hosted runners with
public repositories. Zoomies does not change that. What it does is make the
blast radius of each execution as small as it can reasonably be:

* one job per runner, then the container is destroyed (ephemeral by default);
* no long-lived credential on the runner — a JIT registration is single-use;
* no Docker daemon reachable from the job unless you explicitly ask for one;
* a non-root user inside the container;
* capabilities dropped to a build-shaped minimum. The user keeps passwordless
  `sudo` — a great many workflows assume it, matching a GitHub-hosted runner —
  but `sudo` only ever regains that same minimum, never full root.

## 2. Threat model

Three places, and every credential that crosses between them:

```mermaid
flowchart TB
    subgraph net["the network"]
        gh["GitHub"]
        ops["operators, the CLI, Prometheus"]
    end

    subgraph ch["the controller host"]
        ctrl["zoomies controller"]
        db[("SQLite -- App keys, webhook and OIDC<br/>secrets sealed with a key kept outside it")]
    end

    subgraph rh["a runner host -- the blast radius of one job"]
        ag["zoomies agent"]
        run["runner container<br/>non-root, capabilities dropped,<br/>no docker.sock unless you asked for one"]
    end

    ops -->|"session cookie or zoo_ token, over TLS"| ctrl
    gh -->|"webhook, HMAC-SHA256, constant-time"| ctrl
    ctrl -->|"App JWT, installation token"| gh
    ctrl <--> db
    ag -->|"agent token -- reaches /api/v1/agent/* and nothing else"| ctrl
    ag -->|"a single-use JIT config, in the environment"| run
    run -->|"registers once, then runs your workflow"| gh
```

The runner container is the only place untrusted code runs, and the credential
it is given registers one runner and then expires. The host's own credential
reaches `/api/v1/agent/*` and nothing else: an agent can claim tasks and report
on its own runners, and cannot read pools, jobs, users or the audit log.

### In scope

| Threat | Mitigation |
| --- | --- |
| A malicious job tries to read another job's secrets or source | Ephemeral runners: the container that ran the previous job no longer exists |
| A malicious job tries to reach the host | Container isolation, non-root user, dropped capabilities, no `docker.sock` by default |
| A malicious job steals the runner registration credential and registers its own runner | JIT configurations are single-use and expire; there is no reusable PAT on the host. The entrypoint also unsets both the JIT configuration and the registration token before it starts `run.sh`, so neither is in the environment a workflow step inherits — which matters most on a pool with `ephemeral: false`, where the credential is an organisation-scoped registration token good for an hour rather than a single-use one |
| Someone forges a webhook to make Zoomies create runners | HMAC-SHA256 signature verification on every delivery, constant-time comparison |
| Someone reaches the controller's API | Authentication required by default; the listener binds to loopback unless told otherwise |
| Someone reaches a freshly deployed controller before its owner does | The first-run endpoint needs the setup token printed in the controller's log, not merely an empty database |
| A host enrolled with a join token tries to become a different host, or to advertise labels that win it another pool's work | Taking over an existing host by name needs that host's own agent token; join-token labels win over the agent's |
| A stolen browser session | Sessions are hashed at rest, `HttpOnly` + `SameSite=Lax` + `Secure` (when the external URL is https), and expire |
| A stolen API token | Tokens are stored as SHA-256 hashes, scoped by role, optionally expiring, individually revocable |
| Someone reads the database file or a backup | GitHub App private keys, webhook secrets and OIDC client secrets are AES-256-GCM sealed with a key held outside the database |
| An operator does something destructive | Every mutating action writes an audit row with actor, target, and a before/after diff with secrets redacted |
| A compromised agent | Agents can only claim tasks and report on their own runners; they cannot read pools, jobs, users or the audit log |

### Out of scope

* **A malicious job escaping the container.** Container isolation is the boundary.
  If you need a stronger one, run one agent per trust domain on separate hosts,
  or use the `process` backend inside a VM you are willing to lose.
* **Multi-tenancy between untrusted organisations.** One Zoomies instance is one
  team's fleet. Pools are not a security boundary between tenants.
* **Compromise of the GitHub App itself.** If the App's private key leaks, the
  attacker can register runners against your org, and — because the App is
  created with the [migration wizard's](migration.md) permissions — commit to a
  branch and open a pull request on the repositories it is installed on. It
  cannot merge one. Rotate the key in GitHub and re-enter it in Zoomies; a fleet
  that will never migrate anything can drop those three permissions on the App's
  **Permissions & events** page.
* **Denial of service.** `repository_scale_up_limit` can best-effort throttle
  new runner creation attributed to one repository, but it is not a security
  boundary or strict concurrency quota: GitHub can assign that repository's
  jobs to compatible idle runners. Use `max_runners` as the pool-wide backstop;
  strict separation requires repository-specific pools and workflow labels.

---

## 3. Credentials and how they are stored

| Credential | Where it lives | Protection |
| --- | --- | --- |
| GitHub App private key (PEM) | `installations.private_key_enc` | AES-256-GCM, key from env or key file |
| Webhook HMAC secret | `installations.webhook_secret_enc` | Same |
| OIDC client secret | `instance_settings` | AES-256-GCM, sealed with the same key, then base64 |
| Registry credential | `instance_settings` | Same |
| Capacity-demand signing secret | `instance_settings` | Same |
| User passwords | `users.password_hash` | argon2id, 64 MiB × 2 passes × 4 lanes, 16-byte salt |
| Session cookies | `sessions.token_hash` | SHA-256 of a 32-byte random token |
| API tokens | `api_tokens.token_hash` | SHA-256; the plaintext is shown exactly once. Revoked with the account they belong to: disabling or deleting a user revokes their tokens, and one whose owner is disabled or gone is refused even if it was not |
| Agent tokens | `hosts.token_hash` | SHA-256; issued once at join |
| Join tokens | `join_tokens.token_hash` | SHA-256, single-use, short TTL. One minted for a rented machine is scoped to that machine's name as well, so a token read out of a guest cannot enrol anything else |
| Provider credentials | `providers.credentials_enc` | AES-256-GCM, key from env or key file. Unsealed only for the life of one API client, and never sent to a guest, an API response, an audit row or a log line |
| Provider private connection address | `providers.tailcat_address_enc` | Same. The address of a `zoomies gateway` is a lasting capability to open connections to the hypervisor's API, so it is handled exactly as the credential beside it: every read reports `connection: tailcat` instead |
| JIT runner configs | Never stored | Passed to the agent in a task and to the container in its environment |

### The instance encryption key

32 bytes, base64 or hex, supplied by one of:

1. `ZOOMIES_ENCRYPTION_KEY` (preferred for containers)
2. `security.encryption_key_file` — a `0600` file; Zoomies refuses to read a key
   file that is group- or world-readable
3. `security.encryption_key` in `zoomies.yaml` — **warned about**, because
   anything that can read your config (backups, configuration management, a
   support bundle) can then decrypt every stored secret

**Back this key up.** Losing it means re-entering the GitHub App private key, the
webhook secret, and any credential held as a setting — the single sign-on client
secret, the registry credential, the capacity-demand signing secret. It does not
mean losing the fleet's state: pools, runners, jobs, the audit log and every
setting that is not a credential are not encrypted.

Rotation: change the key and restart; Zoomies will fail to decrypt the existing
installation rows and tell you which ones to re-enter. There is no automatic
re-encryption in v1.

### What a rented machine is given, and what it is not

A machine Zoomies rents from an infrastructure provider receives exactly one
credential: a join token, single use, valid for minutes, and scoped to that one
machine's name. All it can do is create one host row with labels an operator
chose. It cannot read the provider's API, enumerate machines, or delete
anything.

The provider credential stays on the controller. It is sealed in
`providers.credentials_enc`, unsealed only inside the call that builds one API
client, and there is no code path that copies it into a guest — the type that
describes what goes into a machine has no field it could travel in, and a test
plants a recognisable token in the provider row and scans every byte of every
bootstrap payload for it.

That matters because the guest is the least trusted thing in this picture. It
runs other people's workflow jobs. A credential that could destroy virtual
machines must never be one job's `cat` away, which is also why the enrolment
token is written into the guest through the hypervisor's own guest agent rather
than into the VM's cloud-init metadata, where any Proxmox user with audit rights
could read it back.

The privileges the credential itself holds are yours to scope, and
[Proxmox VE](proxmox.md#the-api-token) lists exactly which ones are needed and
why each one is there.

### An installation archive

`zoomies export --installation` is the one path by which a GitHub App's private
key leaves the instance. It leaves only when the exporter gives a passphrase,
sealed under a key derived from it with argon2id, and never as the
instance-sealed column — a copy of that ciphertext would outlive a purge and be
one leaked instance key away from readable. The export is admin-only
(`installations.export`) and audited, the archive is written mode 0600, and the
import seals the key again under the receiving instance's own key before it
writes a row. Treat an archive with a passphrase as you would the key itself,
and keep the passphrase somewhere else.

### What the agent owns on a host

Running the enrolment command on a machine hands Zoomies a small, fixed part
of it, and nothing else. The agent does not upgrade packages, change a
firewall, reboot, open a port or accept a connection — it only ever dials out
to the controller. This is the whole list, as the code does it:

| What | Where | What the agent does with it |
| --- | --- | --- |
| Its service and user | The `zoomies-agent` service (a systemd unit on Linux), running as the `zoomies` system user when the command runs as root, otherwise as the user who ran it | Created by `zoomies agent join`, removed by `zoomies uninstall`. The unit is sandboxed: `NoNewPrivileges`, `ProtectSystem=strict`, and write access to its state directory only. Where the container socket belongs to a group, the service joins that group rather than running as root. |
| `agent.work_dir` | `work/` under its state directory — `/var/lib/zoomies/work` for a root install | Its credentials (`agent.json`, mode `0600`) and the runners' working directories. |
| Containers carrying its labels | Every container it creates is labelled `io.zoomies.managed=true`, with its runner and pool | It lists, reaps and removes only containers with that label; a container without it is never touched. Removing one also removes that container's anonymous volumes. |
| Its per-pool cache | A named volume `zoomies-cache-<identity>`, or a directory `<source>/<identity>` when a pool's cache source is an absolute path — with scope, immutable pool ID and repository hashed into the versioned identity | A cache directory with a size limit is trimmed back under it, oldest entries first, before a runner starts. A named cache volume is never deleted. |
| Its kept tool cache | A directory `cache/tools/<identity>/image-<digest>` in the host's shared folder, `/var/lib/zoomies/shared` — separated by cache scope and resolved runner image — when the pool sets `cache.tools` | Never trimmed or deleted by Zoomies; it is a cache, so emptying it costs the next job its downloads and nothing else. Created writable by every account, under a shared folder only Zoomies' own account can enter. After a toolchain scan Zoomies fills it too, in a container of the pool's image as its runner user, from each toolchain's publisher — nodejs.org, go.dev, Adoptium and GitHub's `actions/python-versions` — with the publisher's checksum checked where it lists one. Runners get it read-only, beside a folder of their own in `cache/tool-runs/<runner>` that is removed with the runner, so a job cannot change what the next job runs. |
| The Docker builder cache | The Docker daemon the agent uses | See below. |

It never removes an image, a volume it did not create with a container, or a
container without its label, and a test in the agent's own suite fails if
that ever widens.

**The builder cache is the one thing that is not only Zoomies'.** With the
`docker` backend, the agent asks the daemon every five minutes, and at start,
to prune **unused** builder cache down to `agent.docker_build_cache_mb`
(default `0`, disabled). Docker never prunes cache that a build in progress is
using, and the prune is bounded by that target — but it is daemon-wide: on a
daemon something else also builds with, the cache it trims is partly somebody
else's. On a shared daemon, set `agent.docker_build_cache_mb: 0` (or
`ZOOMIES_AGENT_DOCKER_BUILD_CACHE_MB=0`) and the agent prunes nothing. Podman
and the `process` backend have no builder cache for it to touch.

Enrolment itself does one more thing, once, on a host whose rootless Docker or
Podman cannot apply a CPU, memory or pids limit: run as root on a cgroup v2
systemd host, it writes
`/etc/systemd/system/user@<uid>.service.d/zoomies-delegate.conf` to delegate
those controllers to that user's slice and restarts that user's daemon. It
says so when it does, and a daemon that already applies limits is left alone.

---

## 4. Authentication and authorisation

### Identities

* **Local users** — argon2id passwords. The first account is created by
  `zoomies init` on the console, or by the one-time bootstrap endpoint, which
  refuses to run once any user exists **and** requires the setup token the
  controller prints in its log while the instance is empty. "No account exists
  yet" is a condition a stranger can satisfy too, so on its own it would hand a
  freshly deployed controller to whoever loaded the page first; the token is
  proof that the caller can read the controller's log. It is minted per process,
  so a restart prints a new one, and the line stops appearing once an account
  exists. An unattended deployment creates the account from the environment
  instead: `ZOOMIES_BOOTSTRAP_ADMIN` with a password or token file, read once
  at startup on an empty database. Whoever sets a process's environment already
  operates it, so that path needs no token; the files must not be readable by
  group or other, a supplied token is stored only as its hash, and once any
  account exists the variables are ignored — they can never add a platform
  account to an instance somebody has claimed — and `bootstrap.ignored` says
  so until they are removed. Every path writes an `auth.bootstrap` audit row
  naming how the account was made.
* **OIDC users** — optional. Linked by `sub`, and by username only for an
  account that has no local password: adopting one that does would let whoever
  holds that username at the identity provider take over the local account, so
  it takes `oidc.link_by_username`. An `email` claim is used as a username only
  when the provider says `email_verified`.
* **API tokens** — `zoo_<prefix>_<secret>`, sent as `Authorization: Bearer`.
  Carry a role and optionally a narrower scope list. A token can mint tokens,
  but never one wider than itself: the role is capped at the caller's, a
  scoped token can only mint within its scopes, and the result belongs to the
  same account, so a leaked token narrowed to one resource cannot be turned
  into an unscoped one that outlives its revocation.
* **Agents** — a separate credential class that can only reach `/api/v1/agent/*`,
  and only for their own host: an agent may report on its own runners and write
  into its own log relay, and gets the same "no such stream" answer for anybody
  else's. A join token enrols a machine; it does not let that machine take over
  an existing host by claiming its name, which needs the agent token the
  previous registration was issued. Labels pinned by the join token win over
  labels the agent declares for itself, because host labels are what decide
  which pools' work — and which pools' runner registrations — a host is offered.

### Roles

| Role | May |
| --- | --- |
| **viewer** | Read pools, runners, jobs, hosts, the audit log and metrics. Never sees a secret value — including a pool's `env`, where a registry or proxy credential ends up: a viewer is sent the variable names with empty values, on the API and on the event stream alike. |
| **operator** | Everything a viewer may, plus act on the fleet: create and edit pools, drain/delete/restart runners, cordon hosts. |
| **admin** | Everything an operator may, plus manage users, API tokens, installations, join tokens and settings, and take a support bundle. |
| **platform** | Everything an administrator may, plus the two things that belong to whoever runs the process rather than the fleet: lifting the recovery fence, and taking, downloading and restoring backups. A backup is the whole database — every account's password hash and every sealed credential, under the key this host holds — so `backups:read` on a token is the instance, not merely the fleet. The role exists for the instance where one team runs the controller and another uses it; where one team does both, the account that installed it holds this role and nothing looks any different. |

The mapping from every individual API action to its minimum role is a table in
`internal/auth/rbac.go`, and a test walks the full action list — so a new
endpoint cannot be added without deciding who may call it.

The API refuses to remove or demote the last enabled admin. Nobody may grant a
role above their own, or change, disable, delete or reset the password of an
account that outranks them — so an administrator cannot make a platform
account, or become one.

### How one request is authorised

```mermaid
flowchart LR
    req["a request"] --> who{"which credential?"}
    who -->|"session cookie"| csrf["Origin and Sec-Fetch-Site<br/>must be same-origin"]
    who -->|"Bearer zoo_ token"| scope["role, plus any scopes<br/>the token was narrowed to"]
    who -->|"agent token"| only["/api/v1/agent/* only"]
    who -->|"none"| pub["the handful of<br/>unauthenticated routes"]
    csrf --> rbac{"the minimum role<br/>for this action"}
    scope --> rbac
    rbac -->|"met"| h["the handler runs, and a mutating<br/>one writes an audit row"]
    rbac -->|"not met"| deny["403 naming the role you are missing"]
```

The action-to-role table is `internal/auth/rbac.go`. A mutating handler that
succeeds writes an audit row naming the actor, the target and a redacted
before/after; a refused login writes one too, because a burst of those is
something you want to see.

### Sessions

`HttpOnly`, `SameSite=Lax`, `Secure` when the external URL is https or TLS is
terminated by Zoomies. Default lifetime seven days. Changing a password
invalidates every other session for that user.

Login is rate limited per source address (default 10/minute) and, more loosely,
per account, so a pool of addresses does not buy an unbounded budget against one
person. An unknown username runs the argon2 KDF anyway, so response timing does
not enumerate accounts either. Nor does the answer: "this account is disabled"
is only said to somebody who has already given the right password, and an
SSO-only account — which has no password to prove anything with — is refused
exactly as an unknown username is. The reason is in the log and the audit trail
for whoever is diagnosing it.

A failed sign-in records the submitted username only when it names an account
that exists. An unrecognised one is stored as a short fingerprint instead: a
password typed into the username field is a common slip, and the audit log is
readable by every viewer on the instance.

Single sign-on binds its `state` to the browser that started the handshake with
a short-lived cookie, so a callback obtained by an attacker signing in as
themselves cannot be replayed into somebody else's browser.

---

## 5. Webhooks

Every delivery is verified with HMAC-SHA256 against the installation's secret,
compared in constant time. A delivery with a bad signature is recorded as
`rejected` and shows up on the Installations page — a burst of them means
somebody is probing you, and you should be able to see that.

The webhook endpoint is the one unauthenticated route that mutates state. It:

* accepts only `POST` with a body under 5 MiB;
* only acts on `workflow_job` and `ping`;
* records every delivery, accepted or not;
* never trusts a repository or label value beyond matching it against pools you
  configured.

If your controller is not reachable from GitHub, turn on `github.poll_fallback`
(it is on by default). Polling is slower but it is not less safe.

---

## 6. The dangerous toggles

Each of these is off by default and is named when it is on, and each is listed
here with what it actually costs. The instance-wide settings are warned about at
startup and shown in the UI's problems drawer -- except `server.bind` without
TLS, which is printed at startup and by `zoomies config check` but kept off the
drawer, because it is true of every fleet behind a TLS-terminating proxy and a
count that is always amber is a count nobody reads. The per-pool settings are
not startup matters at all: they are shown on the pool's own page and in the
drawer for as long as the pool has them.

This section is about what each dangerous setting *costs*.
[Problem codes](problem-codes.md) is the other half: every code Zoomies can
raise, dangerous or not, with its severity and what to do about it.

### `pool.docker_mode: host-socket`

Bind-mounts the host's `docker.sock` into every runner in the pool.

**Any job on this pool can start a privileged container, mount the host's root
filesystem, and become root on the host.** It also sees, and can stop, every
other container on the host — including other runners and Zoomies itself.

Use it only when every repository that can reach this pool is as trusted as the
host. Prefer `dind`.

The runner is not root in its container, so Zoomies also adds the group that
owns the socket to it. That is what makes the mount usable, and it is worth
knowing that it is the whole of the access control here: nothing else stands
between a job and that daemon.

### `pool.docker_mode: dind`

Runs a privileged `docker:dind` sidecar per runner, sharing a network namespace.

The job gets a real Docker daemon it can build with, and it cannot see the
host's containers. But the sidecar itself is `--privileged`, so a container
escape *from the sidecar* reaches the host. This is a real improvement on
`host-socket` and still not a security boundary you should bet a production
host on.

Either mode gives the job a daemon; the client comes from the image, and a pool
on the stock runner image is switched to its Docker variant as it asks for one.
An image of your own has to carry the client itself. See [Jobs that build
container images](configuration.md#jobs-that-build-container-images).

A fleet built to produce container images decides about that privileged sidecar
once, and a row per pool per pass about a decision already taken is what teaches
an operator to skim the problems list — so `security.docker_in_docker_expected`
stops `dind` being listed among the dangerous settings. It changes nothing about
the runners, and it silences that sentence alone: `host-socket` and persistent
runners still say what they cost, and the setting itself is listed as
`dind.expected` so the fleet's own record says what it chose not to be told.

### `pool.ephemeral: false`

Runners persist across jobs.

Job N+1 inherits everything job N left behind: cloned source, build caches,
environment variables, credentials written to disk, background processes. This
is the single largest isolation regression available in the product. It exists
because some workloads genuinely need a warm cache.

### `pool.cache.scope: repository` under an organisation installation

The pool names the repository its cache is for, but its runners register to
the organisation, and GitHub gives a runner any queued job whose `runs-on`
matches its labels. A job from another repository that asks for this pool's
labels lands on one of its runners and reads and writes the cache — the
sharing the scope exists to prevent, held off only by a discipline kept in
other people's workflow files.

Give such a pool a branded label that only that repository's workflows use.
Zoomies warns about the combination on the pool's page and in the problems
panel, because the cache's privacy depends on something it cannot see. A
repository-targeted installation registers runners that only that
repository's jobs can reach, so there the cache is as private as it looks.

### `pool.run_as_root: true`

The job runs as UID 0 inside the container. Combined with any Docker mode, or
with a container escape, this is materially worse than the default.

### `agent.backend: process`

No container at all. Workflow steps run directly on the host as the agent's
user, sharing its filesystem, package manager, network and SSH agent.

If the agent runs as root, every workflow step from every matched repository
runs as root on that host. Zoomies warns about this combination specifically.

On Windows this is the only backend, and the service runs as LocalSystem unless
you reconfigure it, so the same sentence applies with "root" read as
"LocalSystem". File modes mean nothing there, so what keeps another local
account away from the agent's credentials is the ACL `zoomies agent join` sets
on `%ProgramData%\zoomies`: SYSTEM and Administrators only. There is no Windows container to destroy: what "ephemeral" means
there is a fresh work directory and a single-use registration on a machine that
keeps its state, and a drain is a kill of the runner's process tree rather than
an interrupt.

### `server.bind: 0.0.0.0` with `server.tls.mode: off`

Session cookies, API tokens and the GitHub App private key you paste during
setup all cross the network in cleartext.

This is legitimate *behind a TLS-terminating reverse proxy* — which is why it is
a warning rather than an error. If that is your setup, also set
`server.trusted_proxies` so audit entries record the real client address rather
than your proxy's. The word `cloudflare` stands for Cloudflare's published
ranges when Cloudflare is the proxy.

### `server.allowed_origins: ["*"]`

Switches the origin check off. Browser requests that change state are normally
accepted only from this controller's own origin (or an origin listed here), so
that a page on some other site an operator happens to visit cannot use their
session cookie to create pools, drain runners or mint tokens. `"*"` accepts any
origin, which is exactly that cross-site request forgery. List the origins that
actually host the UI instead. An `http://` origin on an `https://` controller
is warned about for a related reason: a plaintext page can be rewritten in
transit, and whatever rewrites it inherits the permission the entry grants.

### `server.trusted_proxies: [0.0.0.0/0]`

Believes `X-Forwarded-For` from every address. It is what makes a header-based
setup "just work", and what it costs is that any client can choose the address
the audit log records for it and defeat login rate limiting by rotating the one
it claims. List your proxy's own range, or the word `cloudflare`.

### `security.rate_limit_logins: 0`

Turns the sign-in rate limit off, so a password can be guessed from one address
as fast as the controller answers. The default of ten attempts per address per
minute is generous for a person and hopeless for a dictionary.

### `oidc.link_by_username: true`

Lets the first single sign-on login by a username take over an existing local
account of that name, password and role included. Off, a username alone links
only to an account created for SSO — one with no password — so an identity
provider whose users can influence their own username claim cannot hand someone
the local `admin` account. Turn it on for the migration from local passwords to
SSO, when every account is known and the provider is trusted to spell names
correctly, and turn it off again afterwards; or link the accounts by hand and
leave it off.

### `oidc.issuer: http://…`

Discovery, the token exchange and the client secret all travel to the identity
provider over plaintext HTTP, where anything on the path can read or replace
them and sign in as anyone. Use the issuer's `https://` address; a loopback
issuer for local development is not warned about.

### `security.disable_auth: true`

Every request is treated as an administrator.

Zoomies **refuses to start** with this set unless the controller looks
unreachable from anywhere but this machine — which means a loopback bind *and*
no `server.external_url` *and* no `server.trusted_proxies`. A loopback bind on
its own is not enough: loopback behind a reverse proxy is the deployment this
documentation recommends, and it is reachable by the whole internet. With
nothing in front of it, this is a warning, because it is genuinely useful for
local development.

### `server.allowed_origins: "*"`

The cross-origin check is off. Any site a signed-in operator visits can make
state-changing calls to this controller with their session; the `SameSite=Lax`
cookie still refuses most of them, but it becomes the only thing left. List the
origins you actually serve the UI from instead.

### `agent.allow_insecure_http: true`

The agent talks to a remote controller over plain HTTP. Its token and the JIT
runner configuration in every create task — a live registration credential for
your runner group — cross the network in the clear. Without this, a plaintext
controller URL that is not on loopback is refused outright.

### A backup remote with no `passphrase`

The archive the fleet uploads is the whole of it: every repository and job it
has seen, every account and password hash, every API token's hash, and the
GitHub App's private key and webhook secrets — sealed with the instance
encryption key, which is the one thing in the file that is not readable, and
only until somebody who has the bucket also has that key. In a bucket, with no
passphrase, it is a file that opens for whoever can read the bucket: the
provider, anyone holding a key to it, and anyone who finds it left public.

Setting a passphrase on the remote seals the archive with argon2id and
AES-256-GCM before it leaves the host, so the bucket holds something its owner
cannot open. Keep the passphrase where you keep the encryption key — nothing on
the controller can recover a lost one, which is the same trade the encrypted
download makes. The startup validator raises `backup.remote_plaintext` for as
long as a remote has none.

### A backup remote reached over `http://`

The access key, the request signature and the archive itself cross the network
in the clear. Whoever is on the path reads the fleet and, holding the key,
writes to the bucket afterwards — which includes replacing a backup with one of
their own choosing, for somebody to restore later. Loopback and a network you
own end to end are the only endpoints this is reasonable for, which is why the
warning (`backup.remote_insecure`) excludes loopback and nothing else.

### `security.allow_private_egress: true`

Six settings make the controller send a request to a URL an administrator
typed: `oidc.issuer`, `github.api_base_url`, `capacity_demand.destination_url`,
`agent.runner_download_url`, a backup remote's endpoint and a provider's. Left
unchecked, each is a way to aim this process at its own neighbourhood — the
cloud metadata service at `169.254.169.254`, an admin port bound to loopback, a
service on the LAN that trusts anything inside it — and read the answer back
out of an error message. Those are the platform's resources, not the fleet's,
so by default each of them is refused when it is written through the API
— `PATCH /settings`, a settings import, a backup remote, a direct provider or
an installation's own API base URL — if it names loopback, an unspecified address, link-local (IPv4 and `fe80::/10`),
RFC 1918, carrier-grade NAT (`100.64.0.0/10`), IPv6 unique-local
(`fc00::/7`), the deprecated site-local and IPv4-compatible ranges, reserved,
broadcast and multicast addresses, an IPv4 address carried inside IPv6
(`::ffff:10.0.0.1`, NAT64, 6to4), `localhost` and anything under
`.localhost`, or an address in a spelling other parsers read as IPv4
(`2130706433`, `0x7f000001`, `127.1`).

The same value in `zoomies.yaml` or a `ZOOMIES_*` variable is only a warning
(`egress.private_target`), and never stops the controller starting. The file
and the environment belong to whoever runs the process, who is already trusted
with everything this guard protects; the person the guard is for is somebody
with settings rights typing a URL into a form. And an upgrade must not stop an
install that has been talking to a LAN Enterprise Server or identity provider
for a year: it starts, warns, and names the key.

The switch is platform-scoped: it changes what the process may dial from its
own machine, so on an instance one team runs for another the fleet's
administrators cannot turn it on. On a single-team instance whose identity
provider, Enterprise Server or Proxmox cluster is on the LAN, or whose backups
go to a MinIO beside the controller, turning it on is the expected answer, and
the refusal names it.

Know its limits. The check reads the address it is given and **resolves
nothing**: a validator cannot answer DNS honestly, because the answer at
startup need not be the answer at the next request, and a name somebody else
controls can be pointed at `10.0.0.1` afterwards. Any host name other than the
local ones above is admitted on trust, and the clients that dial these URLs do
not re-check the address they connect to. The refusal happens where a value
is written, so a destination saved in the database before this check existed
is not refused until it is next edited. A provider
reached through a [Tailcat gateway](#tailcat-private-connections) is not
checked, because its endpoint is dialled from the gateway's network, which is
the private network it was installed to reach. Where the controller runs
somewhere with a metadata service worth protecting, a host firewall rule that
drops its traffic to `169.254.169.254` is the complement that also covers
names.

### `provider.insecure_skip_verify: true`

Zoomies talks to a hypervisor without verifying its certificate. The API token
— which can create, configure, start, stop and destroy virtual machines, and
run commands inside them — crosses the network to whoever answered. It is a
per-provider setting, warned about on the provider and in the problems drawer
for as long as it is on.

There is almost never a reason to leave it on. A Proxmox cluster signs its own
certificates with a CA at `/etc/pve/pve-root-ca.pem`; paste that into the
provider's CA field and the connection is verified properly. See
[Proxmox VE](proxmox.md#tls).

### `oidc.link_by_username: true`

A successful SSO login adopts an existing account that still has a local
password. Whoever controls a username at your identity provider then controls
the local account of the same name, `admin` included. Turn it on for the one
migration where that is what you mean, then turn it off again.

### `metrics.public: true`

`/metrics` served without authentication. No repository or workflow name is a
label — the code stopped putting them there — but pool names, backend kinds,
runner and host states, the id of each GitHub App installation a call was made
for, and the build's version and commit are, and together they tell a stranger
what you run and how busy it is. Prefer giving Prometheus a viewer API token.

### `status.mode: public`

The fleet status at `/status`, the badge at `/status.svg` and the projection at
`/api/v1/status` answer anyone who can reach the controller. It exists for the
developer whose job has queued and who has no account: GitHub tells them only
"queued", and the controller knows whether it is capacity, labels, a lost
permission or a webhook secret that stopped verifying.

What it discloses is how the fleet is doing, not who it is. It carries no pool,
host, repository, runner or job name — a test searches its body for every name
a fixture fleet has — only the fleet's state, the release the controller runs,
banded counts of queued and running jobs, the queue wait rounded to the minute,
and the codes of the fleet's current problems with a fixed sentence each.
Problems about the controller itself — its backups, its lease, its own release
— are never in it. Together that still tells a stranger that a Zoomies fleet is
here, which version it runs and when it is struggling, so it is a warning for
as long as it is on.

On a listener that is not on loopback and does not terminate TLS it is refused
at startup: `public` invites people to an address that would carry every
sign-in in the clear. `authenticated` shows the same page to anyone signed in,
whatever their role, and costs nothing that the rest of the interface does not.

### `server.allow_indexing: true`

`robots.txt` invites search engines into the interface, advertises
`/sitemap.xml`, and the page's own directive changes from `noindex, nofollow` to
`index, follow`.

Nothing behind authentication becomes readable — the API still refuses a request
without a session — but the sign-in page, this controller's address and the fact
that it is a Zoomies fleet all become public knowledge, findable by anyone
searching for exactly that. Leave it off unless the instance is deliberately
public.

### `agent.allow_unverified_runner_download: true`

**What it does.** Lets the process backend install an `actions/runner` archive
whose SHA-256 Zoomies does not know.

**What it costs.** The runner is downloaded and then executed on the host as the
agent's user. Without a digest to check, anything between the host and the
download source -- a mirror, a proxy, a compromised network -- can substitute its
own archive, and the agent will run it. Zoomies ships the digests for the
release it pins, so the setting is only ever needed for a release it does not
know about.

**Do this instead.** Pin the release with `github.runner_version` and put its
digest, from the actions/runner release notes, in `agent.runner_sha256`.

### `agent.insecure_skip_verify: true`

The agent does not verify the controller's TLS certificate. Anything on the
network path can impersonate the controller and hand the agent arbitrary
containers to run. Pin the CA with `agent.ca_file` instead.

### `agent.extra_ca_file`

**What it does.** Mounts a PEM root CA read-only into every container runner
on the host, and into its docker-in-docker sidecar, and adds it to what they
trust, so jobs work behind a proxy that re-signs TLS.

**What it costs.** Whoever holds that CA's private key can read and alter every
TLS connection a job makes, including its traffic to GitHub. Behind an
intercepting proxy that is already true of the network, and this only makes
the runners agree to it; with any other CA it is an interception you have
invited. It is reported as information (`agent.extra_ca`) rather than warned
about, because on the networks that need it there is no safer alternative.
[The configuration page](configuration.md#agentextra_ca_file-behind-a-proxy-that-re-signs-tls)
has the mechanics.

---

## 7. What is tested

The claims above are not assertions of intent. Each of these is a test that was
run against the code with its rule removed and confirmed to fail before it was
kept; a rule that could not be made to fail did not ship.

| Claim | Where |
| --- | --- |
| Every route refuses the roles below it, and every scoped token refuses the routes outside its resource | `TestRouteAuthorisation`, `TestScopedTokenRouteAuthorisation` (`internal/api`) |
| A scope narrows a role and never widens it | `TestScopesAreNarrowerThanRoles` (`internal/api`) |
| All five agent routes refuse a user credential, an anonymous caller and a revoked host | `TestAgentRoutesRefuseAUserCredential`, `TestADeletedHostsAgentTokenIsRefused` (`internal/api`) |
| A host cannot report a runner, a task result or a log chunk that is not its own | `TestAHostCannotReportOnAnotherHostsRunner`, `TestAHostCannotReportATaskResultForAnotherHostsRunner`, `TestLogRelayRefusesAnotherHostsStream` (`internal/controller`) |
| Secrets are absent from responses, error bodies, audit rows, event frames, metrics and the controller's log | `TestSecretsAreNeverInAResponse`, `TestSecretsAreNeverInAFailure` (`internal/api`) |
| A task carries the runner's registration credential and no controller secret | `TestACreateTaskCarriesNoControllerSecret` (`internal/controller`) |
| A `process` runner's environment is built, not inherited | `TestProcessChildEnvironmentIsBuiltNotInherited` (`internal/backend`) |
| Disabling an account ends its sessions, not merely its access | `TestDisablingAUserEndsItsSessionsAtTheAPI` (`internal/api`) |
| A cross-origin sign-in is refused, and a non-browser client is not | `TestCSRFRefusesACrossOriginLogin` (`internal/api`) |
| `X-Forwarded-Proto` is believed only from a trusted proxy | `TestForwardedProtoIsBelievedOnlyFromATrustedProxy` (`internal/api`) |
| Enrolment is rate limited, on a counter of its own | `TestAgentJoinIsRateLimited` (`internal/api`) |
| A live stream ends within one heartbeat of its credential being revoked, and keeps running while it stands | `TestAStreamEndsWhenItsCredentialIsRevoked`, `TestALogStreamEndsWhenItsCredentialIsRevoked`, `TestALiveStreamSurvivesItsOwnHeartbeat` (`internal/api`) |
| A name carrying markup is rendered as text, and a runner's output cannot retitle the page, clear it or open a dialog | `web/tests/hostile-input.spec.ts` |
| A link a runner printed is followed only when it is http or https, and then with `noopener` | `followableLink` in `web/src/lib/logs/LogViewer.svelte` (see the note in ZF-104's fourth pull request) |
| A body over the limit is refused, and the log relay is exempt | `TestOversizeRequestBodyIsRefused`, `TestARunnerThatPrintsMoreThanTheBodyLimitIsNotCutOff` (`internal/api`) |

### The supply chain

The [OpenSSF Scorecard](https://scorecard.dev/viewer/?uri=github.com/eyupio/zoomies)
on the README is the outside view of the same discipline, re-run weekly by
`.github/workflows/scorecard.yml`. Most of what it measures is in this
repository and checked by it: every action pinned to a commit, workflow tokens
that grant nothing at the top, Dependabot on all three ecosystems, CodeQL on
every pull request, `govulncheck` on every change, a fuzz workflow, and a
release that ships its build provenance twice — a Sigstore bundle for
`gh attestation verify` and the same statement as `.intoto.jsonl` for SLSA
tooling. `osv-scanner.toml` at the root is the one place an advisory is
ignored, and only when `govulncheck` has already shown the package is not in
the import graph and no version fixes it; an entry there names why.

Three of its checks are repository settings rather than files, so no pull
request can change them. They are recorded here so the next person to read the
score knows what it is measuring:

* **Branch-Protection** wants `main` to refuse a direct push and a force push,
  and to require the CI status checks before a merge.
* **Code-Review** counts merged pull requests approved by someone other than
  their author. A project with one maintainer scores zero here until it has
  two, and the score is honest about that.
* **CII-Best-Practices** looks for a badge earned at
  [bestpractices.dev](https://www.bestpractices.dev/projects/14604), a
  questionnaire a maintainer fills in about the practices this document
  describes. The project holds the passing badge; Scorecard picks it up on
  its next weekly run.

---

## 8. Hardening a production install

1. Create the first account before anyone else can. If you deployed with
   the compose file rather than the installer, the controller is listening the
   moment it starts: read the setup token out of its log
   (`docker compose logs zoomies`) and finish the first-run page — or, when no
   person is at the console, have the provisioner create the account from
   `ZOOMIES_BOOTSTRAP_ADMIN` and a 0600 token or password file, so the
   instance is never reachable without an owner. Keep the
   origin firewalled to your proxy as well, as the compose file's comments say.
1. Run the controller as a dedicated unprivileged user
   (`zoomies init` creates one).
2. Use a **rootless** Docker or Podman socket. The installer detects and prefers
   one, and on a systemd, cgroup v2 host it delegates the `cpu`, `cpuset`, `io`,
   `memory` and `pids` controllers to that daemon's user slice for you, so a
   pool's CPU quota or memory limit is not refused there. A host still on
   cgroup v1 needs `systemd.unified_cgroup_hierarchy=1` on the kernel command
   line and a reboot first — setup warns rather than guessing at that for you.
3. Terminate TLS with a certificate GitHub trusts — either in Zoomies
   (`tls.mode: files`) or in a reverse proxy, and then set `trusted_proxies`.
4. Keep `ephemeral: true` and `docker_mode: none` on every pool you can.
5. Set `max_runners` on every pool. It is your only backstop against a runaway
   workflow.
6. Give automation scoped API tokens with expiry, not admin tokens.
7. Put the encryption key in `ZOOMIES_ENCRYPTION_KEY` or a `0600` file, and back
   it up somewhere that is not the same backup as the database.
8. Watch the audit log. `zoomies audit tail` and the Audit page both work.
9. Keep the runner image current — it carries the `actions/runner` release and
   its .NET dependency, and GitHub deprecates old runner versions.

## 9. Reporting a vulnerability

Open a [private security advisory][advisory] on the repository rather than a
public issue. Please include the version (`zoomies version`), the configuration
with secrets removed — `zoomies config print` produces it already blanked — and
what an attacker gains.

`SECURITY.md` in the repository root says the same thing, and is what GitHub
reads to offer "Report a vulnerability" on the repository's own security tab.
It also draws the line this document is the long form of: a dangerous setting
behaving dangerously is not a vulnerability, and a way to reach one without
setting it is.

[advisory]: https://github.com/eyupio/zoomies/security/advisories/new


## Tailcat private connections

Private tunnels expose only the agent API and retain Zoomies join-token and
agent-token authentication. No administrator route, SSH service, arbitrary
port forwarding or subnet routing is enabled. Tunnel identities are encrypted
in SQLite; agent capability addresses live in mode-0600 agent credentials.
Enrolment commands contain secrets and must not be published. Disabling
`server.tailcat_enabled` and restarting closes private connectivity; it does
not rotate the saved identity. See [private host credential handling and relay
limitations](private-hosts.md#restarts-and-credential-protection).
