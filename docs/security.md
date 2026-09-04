# Zoomies security

This document says what Zoomies protects, what it does not, and what every
dangerous setting actually costs you. Nothing here is hypothetical — each toggle
below is a real setting, and each one produces a named warning at startup and in
the UI's problems panel when it is on.

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
* capabilities dropped to a build-shaped minimum, `no-new-privileges` set.

## 2. Threat model

### In scope

| Threat | Mitigation |
| --- | --- |
| A malicious job tries to read another job's secrets or source | Ephemeral runners: the container that ran the previous job no longer exists |
| A malicious job tries to reach the host | Container isolation, non-root user, dropped capabilities, no `docker.sock` by default |
| A malicious job steals the runner registration credential and registers its own runner | JIT configurations are single-use and expire; there is no reusable PAT on the host |
| Someone forges a webhook to make Zoomies create runners | HMAC-SHA256 signature verification on every delivery, constant-time comparison |
| Someone reaches the controller's API | Authentication required by default; the listener binds to loopback unless told otherwise |
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
  attacker can register runners against your org. Rotate it in GitHub and
  re-enter it in Zoomies.
* **Denial of service.** There is no per-repository quota in v1. A repository
  that queues thousands of jobs will pin your pools at their maximum, which is
  what `max_runners` is for.

---

## 3. Credentials and how they are stored

| Credential | Where it lives | Protection |
| --- | --- | --- |
| GitHub App private key (PEM) | `installations.private_key_enc` | AES-256-GCM, key from env or key file |
| Webhook HMAC secret | `installations.webhook_secret_enc` | Same |
| OIDC client secret | config / `settings` | Sealed when stored in the database |
| User passwords | `users.password_hash` | argon2id, 64 MiB × 2 passes × 4 lanes, 16-byte salt |
| Session cookies | `sessions.token_hash` | SHA-256 of a 32-byte random token |
| API tokens | `api_tokens.token_hash` | SHA-256; the plaintext is shown exactly once |
| Agent tokens | `hosts.token_hash` | SHA-256; issued once at join |
| Join tokens | `join_tokens.token_hash` | SHA-256, single-use, short TTL |
| JIT runner configs | Never stored | Passed to the agent in a task and to the container in its environment |

### The instance encryption key

32 bytes, base64 or hex, supplied by one of:

1. `ZOOMIES_ENCRYPTION_KEY` (preferred for containers)
2. `security.encryption_key_file` — a `0600` file; Zoomies refuses to read a key
   file that is group- or world-readable
3. `security.encryption_key` in `zoomies.yaml` — **warned about**, because
   anything that can read your config (backups, configuration management, a
   support bundle) can then decrypt every stored secret

**Back this key up.** Losing it means re-entering the GitHub App private key and
webhook secret. It does not mean losing the fleet's state — pools, runners, jobs
and the audit log are not encrypted.

Rotation: change the key and restart; Zoomies will fail to decrypt the existing
installation rows and tell you which ones to re-enter. There is no automatic
re-encryption in v1.

---

## 4. Authentication and authorisation

### Identities

* **Local users** — argon2id passwords. The first admin is created by
  `zoomies init` or by the one-time bootstrap endpoint, which refuses to run once
  any user exists.
* **OIDC users** — optional. Linked by `sub`. Group claims map to roles.
* **API tokens** — `zoo_<prefix>_<secret>`, sent as `Authorization: Bearer`.
  Carry a role and optionally a narrower scope list.
* **Agents** — a separate credential class that can only reach `/api/v1/agent/*`.

### Roles

| Role | May |
| --- | --- |
| **viewer** | Read pools, runners, jobs, hosts, the audit log and metrics. Never sees a secret value. |
| **operator** | Everything a viewer may, plus act on the fleet: create and edit pools, drain/delete/restart runners, cordon hosts. |
| **admin** | Everything an operator may, plus manage users, API tokens, installations, join tokens and settings. |

The mapping from every individual API action to its minimum role is a table in
`internal/auth/rbac.go`, and a test walks the full action list — so a new
endpoint cannot be added without deciding who may call it.

The API refuses to remove or demote the last enabled admin.

### Sessions

`HttpOnly`, `SameSite=Lax`, `Secure` when the external URL is https or TLS is
terminated by Zoomies. Default lifetime seven days. Changing a password
invalidates every other session for that user.

Login is rate limited per source address (default 10/minute). An unknown
username runs the argon2 KDF anyway, so response timing does not enumerate
accounts.

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

Each of these is off by default, each produces a startup warning and a UI
problems-panel entry when on, and each is listed here with what it actually
costs.

### `pool.docker_mode: host-socket`

Bind-mounts the host's `docker.sock` into every runner in the pool.

**Any job on this pool can start a privileged container, mount the host's root
filesystem, and become root on the host.** It also sees, and can stop, every
other container on the host — including other runners and Zoomies itself.

Use it only when every repository that can reach this pool is as trusted as the
host. Prefer `dind`.

### `pool.docker_mode: dind`

Runs a privileged `docker:dind` sidecar per runner, sharing a network namespace.

The job gets a real Docker daemon it can build with, and it cannot see the
host's containers. But the sidecar itself is `--privileged`, so a container
escape *from the sidecar* reaches the host. This is a real improvement on
`host-socket` and still not a security boundary you should bet a production
host on.

### `pool.ephemeral: false`

Runners persist across jobs.

Job N+1 inherits everything job N left behind: cloned source, build caches,
environment variables, credentials written to disk, background processes. This
is the single largest isolation regression available in the product. It exists
because some workloads genuinely need a warm cache.

### `pool.run_as_root: true`

The job runs as UID 0 inside the container. Combined with any Docker mode, or
with a container escape, this is materially worse than the default.

### `agent.backend: process`

No container at all. Workflow steps run directly on the host as the agent's
user, sharing its filesystem, package manager, network and SSH agent.

If the agent runs as root, every workflow step from every matched repository
runs as root on that host. Zoomies warns about this combination specifically.

### `server.bind: 0.0.0.0` with `server.tls.mode: off`

Session cookies, API tokens and the GitHub App private key you paste during
setup all cross the network in cleartext.

This is legitimate *behind a TLS-terminating reverse proxy* — which is why it is
a warning rather than an error. If that is your setup, also set
`server.trusted_proxies` so audit entries record the real client address rather
than your proxy's.

### `security.disable_auth: true`

Every request is treated as an administrator.

Zoomies **refuses to start** with this set unless the listener is on loopback.
On loopback it is a warning, because it is genuinely useful for local
development.

### `metrics.public: true`

`/metrics` served without authentication. Repository names, workflow names and
pool names appear in metric labels. Prefer giving Prometheus a viewer API token.

### `agent.insecure_skip_verify: true`

The agent does not verify the controller's TLS certificate. Anything on the
network path can impersonate the controller and hand the agent arbitrary
containers to run. Pin the CA with `agent.ca_file` instead.

---

## 7. Hardening a production install

1. Run the controller as a dedicated unprivileged user
   (`zoomies init` creates one).
2. Use a **rootless** Docker or Podman socket. The installer detects and prefers
   one.
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

## 8. Reporting a vulnerability

Open a private security advisory on the repository rather than a public issue.
Please include the version (`zoomies version`), the configuration with secrets
removed, and what an attacker gains.
