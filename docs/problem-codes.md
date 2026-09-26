---
description: >-
  Every problem code Zoomies can raise, with its severity and what to do about
  it: the startup validator's findings and the running controller's, in one
  list.
---

# Problem codes

Zoomies never reports a problem without a code. The code is stable across
releases and across languages of complaint — it is what you search for, alert
on, and quote in a bug report — while the sentence beside it is written for the
person reading it and may improve over time.

Problems come from two places and behave differently:

* **The startup validator** reads `zoomies.yaml` and the `ZOOMIES_*`
  environment before anything else happens. Its findings are printed at startup
  and shown in the UI's problems panel for as long as the setting stands.
* **The controller** raises the rest while running, after every reconcile pass.
  These come and go with the fleet: a host that starts answering again clears
  `host.unhealthy` by itself.

Each code also has an **audience**, which decides who is shown it on an
instance where one team operates the controller and another uses the fleet:

* The **platform's** are what the process itself is doing wrong — its lease,
  its loops, its backups, the release it could be running — together with every
  finding from the startup validator, because each of those names a setting
  that says what the process binds, trusts, stores or logs.
* The **fleet's** are what its own pools, hosts, runners and jobs are doing
  wrong. These reach everyone who can read the list.
* One code is **both**: `controller.problems_partial`, because a list that
  could not be fully gathered has to say so to whoever is reading it.

On a single-team instance the account that installed it holds the platform
role, so the list is undivided and this changes nothing. The audience of each
code is recorded once, in `problemAudience` in
`internal/controller/problems.go`, and a test fails if a code is raised without
one — it is deliberately not repeated in the table below, because a second
copy of an answer is a second copy to get out of step.

Both use the same three severities:

| Severity | What it means |
| --- | --- |
| **Error** | Startup stops, or the fleet cannot do the thing being asked of it. A configuration error is fatal; a runtime error is work that is not happening. |
| **Warning** | Nothing stops. Something is weaker or slower than the default, and the entry says what that costs. |
| **Info** | Neither wrong nor risky, but worth knowing — usually a default that surprises people. |

A few codes change severity with the circumstances, and each says so below.
`security.disable_auth` is the clearest case: on loopback it is a warning, and
on a public address the same setting is an error.

## Configuration: the listener

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `bind.empty` | error | `server.bind` | Give it a `host:port`. Nothing can start without one. |
| `bind.malformed` | error | `server.bind` | It is not a `host:port` address. A bare port needs the colon: `:8080`. |
| `bind.public_no_tls` | warning | `server.bind` | Sessions and API tokens cross the network in the clear. Put TLS in front of it, or bind to loopback and tunnel. |
| `proxy.untrusted` | info | `server.trusted_proxies` | Client IPs in the audit log are the socket's, not the header's. Correct behind nothing; wrong behind a proxy, which is what the setting is for. |
| `proxy.bad_cidr` | error | `server.trusted_proxies` | An entry is not an IP address or a CIDR block. |
| `proxy.trust_everyone` | warning | `server.trusted_proxies` | Any client can claim any address, so the audit log's IPs mean nothing. Name your proxy's range instead. |
| `origins.any` | warning | `server.allowed_origins` | Any website can act with a signed-in operator's session. Name the origins you serve from. |
| `origins.insecure` | warning | `server.allowed_origins` | A plaintext origin is allowed to act on this controller. |
| `indexing.allowed` | warning | `server.allow_indexing` | Search engines are invited to index a fleet controller. Deliberate for a demo, rarely otherwise. |
| `tls.mode_unknown` | error | `server.tls.mode` | Not a TLS mode. The modes are `off`, `self_signed` and `files`. |
| `tls.files_missing` | error | `server.tls` | `mode: files` needs both a certificate and a key. |
| `tls.file_unreadable` | error | `server.tls.*` | The file is named but cannot be read. Usually ownership after an install as another user. |
| `tls.self_signed` | info | `server.tls.mode` | Browsers and agents will not trust it without being told to. Fine for a private network, not for anything else. |
| `external_url.missing` | warning | `server.external_url` | GitHub cannot be told where to deliver webhooks, so scaling falls back to the poller and reacts in tens of seconds. |
| `external_url.malformed` | error | `server.external_url` | Not an absolute URL. |
| `external_url.insecure` | warning | `server.external_url` | GitHub will deliver webhooks over plaintext, so the payloads and their signatures cross the internet unencrypted. |

## Configuration: GitHub

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `github.api_base_missing` | error | `github.api_base_url` | Empty. Leave it unset for github.com, or give GitHub Enterprise Server's API base. |
| `github.api_base_malformed` | error | `github.api_base_url` | Not an absolute URL. |
| `poll.disabled` | warning | `github.poll_fallback` | If a webhook delivery is lost or misconfigured, jobs queue for ever with nothing to notice. Leave it on unless you monitor delivery yourself. |
| `poll.too_fast` | warning | `github.poll_interval` | Polling this often will consume the App's API rate limit. The poller is a safety net, not the primary path. |

## Configuration: storage and secrets

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `db.path_missing` | error | `database.path` | Empty. |
| `db.parent_not_dir` | error | `database.path` | The parent exists and is not a directory. |
| `crypto.no_key` | warning | `security.encryption_key_file` | One will be generated on first start, and it is the only copy. Back it up: without it the stored GitHub App private key and webhook secrets cannot be decrypted. |
| `crypto.key_in_config` | warning | `security.encryption_key` | The key is in the config file, so anything that reads the file — a backup, a support bundle — reads every stored secret. Point at a file instead. |

## Configuration: the settings in the database

These are raised while assembling the configuration, after the database is open
and before anything is built from it. They are about the rows themselves rather
than about what any one setting says.

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `settings.stored_invalid` | warning | the setting named | A stored value will not parse, so the layer underneath it is in force — the configuration file, or the built-in default. Set it again on the settings page, or clear it with `zoomies config unset <key>`. |
| `settings.stored_unknown` | info | — | Settings are stored that this version does not have, usually because a newer one set them and this is a rollback. They are kept untouched, so upgrading again picks them up where it left off. |
| `settings.stored_unreadable` | error | — | A stored credential was sealed with a different encryption key. Starting anyway would run the fleet with credentials silently missing. Restore the key it was sealed with, or clear the setting and set it again. |
| `settings.imported_from_file` | info | — | The settings your `zoomies.yaml` spells have been copied into the database, once, on the first start after upgrading. Nothing about what the controller runs has changed; the file is still the layer underneath them. |

## Configuration: authentication

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `auth.disabled` | **error wherever the controller is reachable, warning on loopback with nothing in front** | `security.disable_auth` | Every request is treated as an administrator. Acceptable for local development; never on a host others can reach. An external URL or a trusted proxy counts as reachable, because a loopback bind behind a proxy is not private. |
| `dind.expected` | info | `security.docker_in_docker_expected` | This fleet has said that a pool giving its jobs their own Docker daemon is expected, so that pool no longer raises `pool.dangerous` for the privileged container the daemon runs in. Nothing about the runners changed — a warning a fleet has already decided about, repeated for every such pool on every pass, is what teaches an operator to stop reading the list. The host Docker socket and persistent runners still warn. Turn the setting off to be told again. |
| `auth.cookie_insecure` | warning | `security.cookie_secure` | Session cookies go out without `Secure`, so one plain-HTTP request to this host hands over a live session. Set an https `server.external_url`, or `security.cookie_secure` if TLS is terminated in front. |
| `auth.no_login_limit` | warning | `security.rate_limit_logins` | Password guessing is not rate limited. |
| `bootstrap.incomplete` | error | — | `ZOOMIES_BOOTSTRAP_ADMIN` needs exactly one of `ZOOMIES_BOOTSTRAP_PASSWORD_FILE` or `ZOOMIES_BOOTSTRAP_TOKEN_FILE`, and neither file means anything without it. Set all that is missing, or unset all three and use the setup token. |
| `bootstrap.ignored` | warning | — | The bootstrap variables are set on an instance that already has accounts, so this start ignored them — they only ever create the first account. The file they name is a credential kept for nothing: remove the variables and delete it. |
| `auth.session_ttl` | error | `security.session_ttl` | Must be positive. |
| `auth.session_ttl_long` | warning | `security.session_ttl` | A stolen session cookie stays useful for this long. |
| `oidc.incomplete` | error | `oidc` | Single sign-on is enabled with no issuer or no client id. |
| `oidc.no_redirect` | error | `oidc.redirect_url` | Needed, and it must be an address the identity provider can reach. |
| `oidc.insecure_issuer` | warning | `oidc.issuer` | The token exchange happens in the clear. |
| `oidc.link_by_username` | warning | `oidc.link_by_username` | A first single sign-on login can take over an existing password account with the same name. |
| `oidc.open_signup` | warning | `oidc.allow_signup` | Anyone your identity provider authenticates gets an account here. Narrow it at the provider, or turn signup off and create accounts yourself. |
| `egress.private_target` | warning | the URL's own key | A URL the controller dials — `oidc.issuer` (when OIDC is on), `github.api_base_url`, `capacity_demand.destination_url`, `agent.runner_download_url` or a backup remote's endpoint in the file — names this machine, a link-local address such as the cloud metadata service at `169.254.169.254`, or a private range (RFC 1918, carrier-grade NAT, IPv6 unique-local), in any spelling that reaches one. At startup it is a warning and never stops the controller: that value came from whoever runs the process, and an upgrade must not stop an install that works. Written through the API instead — `PATCH /settings`, a settings import (the preview marks the row), a backup remote, a direct provider or an installation's own API base URL — the same sentence is a 422 on that field. Use the service's public address, or, when it really lives on a network you own, set `security.allow_private_egress`. See [security](security.md#securityallow_private_egress-true). |

## Configuration: the agent

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `agent.backend_missing` | error | `agent.backend` | No runner backend selected. |
| `agent.backend_unknown` | error | `agent.backend` | Not a backend. They are `docker`, `podman` and `process`. |
| `agent.capacity` | error | `agent.capacity` | Must be at least 1, or the host can never take a runner. |
| `agent.workdir` | error | `agent.work_dir` | Empty. |
| `agent.process_backend` | warning | `agent.backend` | The process backend gives jobs no container isolation: a job can read and write anything the agent user can. |
| `agent.process_root` | warning | `agent.backend` | The process backend is running as root, so every job is root on the host. |
| `agent.root` | warning | `agent` | The agent process is running as root. Raised only where an agent actually runs. |
| `agent.insecure_tls` | warning | `agent.insecure_skip_verify` | The agent does not verify the controller's certificate, so anything on the path can impersonate it. |
| `agent.insecure_http` | warning | `agent.allow_insecure_http` | The agent talks to the controller over plain HTTP, so its token and every runner's credentials cross the network in the clear. |
| `agent.unverified_runner_download` | warning | `agent.allow_unverified_runner_download` | The process backend may install a runner archive whose checksum it could not confirm. |
| `agent.extra_ca_invalid` | error | `agent.extra_ca_file` | Not an absolute path to a readable PEM file with at least one certificate. Point it at your organisation's root CA in PEM form, or clear it. |
| `agent.extra_ca` | info | `agent.extra_ca_file` | Container runners on this host trust an extra CA as well as the image's own roots. Right for your proxy's CA; clear it otherwise. |
| `agent.docker_build_cache_mb` | error | `agent.docker_build_cache_mb` | Must be between 0 and 1048576 MiB; 0 disables automatic Docker builder-cache cleanup. |
| `agent.bootstrap_cpu_grace_short` | warning | `agent.bootstrap_cpu_grace` | Less than 2m of normal CPU quota before pressure throttling; registration can slow under load. |
| `runners.docker_wait_short` | warning | `runners.docker_wait` | Less than the recommended 3m for a loaded DinD daemon to become ready. |
| `agent.bootstrap_cpu_grace` | error | `agent.bootstrap_cpu_grace` | Must be between 0s and 10m; 0s applies pressure throttling immediately. |
| `agent.finished_retention` | error | `agent.finished_retention` | Cannot be negative. |
| `agent.finished_retention_long` | warning | `agent.finished_retention` | Finished runners stay on the host this long, holding disk and, for a non-ephemeral pool, whatever the job left behind. |
| `agent.heartbeat_interval_long` | warning | `agent.heartbeat_interval` | Hosts heartbeat less often than the controller's timeout, so a healthy host will be counted lost. |
| `agent.none` | info | `agent.embedded` | This controller hosts no runners itself, so at least one standalone agent has to join it. |

## Configuration: the scheduler and the rest

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `scheduler.placement_mode` | error | `scheduler.placement_mode` | Use `headroom`, `shadow` or `readiness`. |
| `scheduler.interval` | error | `scheduler.interval` | Must be positive. |
| `scheduler.burst` | error | `scheduler.max_creates_per_tick` | Must be at least 1. |
| `scheduler.lifetime_short` | warning | `scheduler.max_runner_lifetime` | Idle runners are recycled sooner than a long job takes, so work may be interrupted. |
| `scheduler.provision_timeout_short` | warning | `scheduler.provision_timeout` | Runners are failed sooner than they may legitimately take to start. An agent allows itself fifteen minutes for a create, because a cold image pull on a slow link is minutes rather than seconds, and a runner on a pool that provides Docker then waits for that daemon before it registers. A timeout inside the two together condemns runners that are still coming up, and the replacement pulls the same image over the same link. Set it above both, such as `20m`. |
| `scheduler.default_runner_limits_off` | warning | `scheduler.default_runner_limits` | A pool that sets no `cpus` or `memory_mb` gets runners with no cgroup limit at all, so a host's worth of them can each take every core and all of the memory — the shape that stops Docker answering. Leave it on, or set both on every pool. |
| `scheduler.auto_rerun_on` | warning | `scheduler.auto_rerun` | A job whose runner died under it is sent back to GitHub automatically, without anybody asking. That spends the installation's GitHub minutes, and a job that got as far as running may have had side effects its author expected to happen once. Turn it off to leave the re-run to the button on the job, or lower `scheduler.auto_rerun_limit`. |
| `scheduler.auto_rerun_limit` | error | `scheduler.auto_rerun_limit` | The bound on automatic re-runs is outside 1–5. Zero would make `scheduler.auto_rerun` read as on while doing nothing; a large one turns a fault the fleet causes every time into a bill. |
| `scheduler.host_throttling_off` | warning | `scheduler.host_throttling` | An overwhelmed host is never stepped down: the pressure holds still refuse new starts while CPU or memory is acutely short, but nothing outlasts a sample, and the runners already on the host are never slowed. Leave it on unless something outside Zoomies manages the hosts' load. |
| `runners.docker_wait` | error | `runners.docker_wait` | The runner image accepts a wait of one second to one hour and exits with a configuration error for anything else, so no runner on a Docker pool would take a job. Use a duration in that range, or 0 to leave the image's default. |
| `runners.env_reserved` | error | `runners.env` | It names a variable the controller writes for each runner individually — its JIT configuration, name, labels, group or credentials. One value for the whole fleet is wrong for every runner in it; remove it. |
| `updates.interval_negative` | error | `updates.check_interval` | Must not be negative. Use a duration, or 0 to never ask. |
| `updates.interval_too_fast` | warning | `updates.check_interval` | Releases are published far less often than this, and the check is unauthenticated. |
| `github.toolchain_scan_negative` | error | `github.toolchain_scan_interval` | Must not be negative. Use a duration such as `24h`, or 0 to scan only when asked. |
| `github.toolchain_scan_too_fast` | warning | `github.toolchain_scan_interval` | Every workflow in every repository is read more often than hourly. Each scan spends calls from the GitHub quota the scheduler uses to find queued jobs, and workflows change far less often than that. |
| `images.refresh_negative` | error | `images.refresh_interval` | Must not be negative. Use a duration, or 0 to leave images alone. |
| `images.refresh_too_fast` | warning | `images.refresh_interval` | Every pool's image is checked on every host far more often than an image is built. |
| `images.refresh_off` | info | `images.refresh_interval` | Nothing refreshes runner images, so a pool naming a moving tag keeps whatever its hosts pulled first. Expected on an air-gapped fleet, or one that pins every pool to a digest. |
| `retention.audit_renamed` | info | `retention.audit` | The key was renamed to `retention.scaling_events`, which is all it ever bounded; audit rows are never pruned. The value is still honoured. Rename it. |
| `backup.remote_incomplete` | error | `backup.remotes` | A remote has no endpoint or no bucket, so nothing would be copied to it and nothing would say so. Finish it, or set `disabled: true` until it is ready. |
| `backup.remote_endpoint` | error | `backup.remotes` | A remote's endpoint is not an HTTP URL. Write the service's own, such as `https://s3.eu-west-2.amazonaws.com` or `http://minio:9000`; the scheme is what decides whether the connection is encrypted. |
| `backup.remote_credentials` | error | `backup.remotes` | A remote has no access key or no secret key, so every request to it would be refused and the copies would pile up unsent. |
| `backup.remote_name` | error | `backup.remotes` | A remote's name is not usable: it is a path component in the API and a word in a log line, so it is lower-case letters, digits and dashes. |
| `backup.remote_duplicate` | error | `backup.remotes` | Two remotes share a name, so one of them would be unaddressable by the Backups tab, the log and the problems drawer alike. |
| `backup.remote_insecure` | warning | `backup.remotes` | A destination is reached over plain HTTP, so the access key, the signature and the backup itself cross the network in the clear — anyone on the path can read the fleet and write to the bucket afterwards. Use `https://` unless the endpoint is on this host or a network you own end to end. Raised by the validator for a destination in the file and by the controller for one added on the Backups page; loopback is not warned about. |
| `backup.remote_plaintext` | warning | `backup.remotes` | A destination is sent the backup unencrypted. A backup is the whole fleet — every repository and job it has seen, every account, and the sealed GitHub App credentials — and in a bucket it is a file anyone who can read the bucket can open. Set a passphrase on it and keep that wherever you keep the encryption key; nothing here can recover a lost one. Raised about a destination in the file and about one added on the page alike. |
| `backup.remote_passphrase_short` | warning | `backup.remotes` | A remote's passphrase is shorter than the eight characters the Backups tab's encrypted download accepts. The archive is only as private as this, and it is typed once, into a file. |
| `backup.no_remote` | info | `backup.remotes` | Backups are taken on a schedule and nothing in `zoomies.yaml` says where a copy goes. That is a backup against a mistake, not against the disk, the machine or the datacentre. Add a destination on the Backups page or under `backup.remotes`, or keep shipping the directory yourself — the point of the entry is that one of the three is somebody's job. It is raised from the file, so the Configuration page drops it once the fleet has a destination stored from the Backups page, which the validator cannot see. |
| `capacity_demand.url` | error | `capacity_demand.destination_url` | Not an absolute HTTP URL. |
| `capacity_demand.secret` | error | `capacity_demand.signing_secret` | Empty, so deliveries could not be signed and a receiver could not tell them from anyone else's. |
| `capacity_demand.cooldown` | error | `capacity_demand.cooldown` | Must be positive. |
| `capacity_demand.timeout` | error | `capacity_demand.timeout` | Must be positive. |
| `metrics.public` | warning | `metrics.public` | The metrics endpoint answers without authentication, so anyone who can reach it learns the shape of the fleet. |
| `status.public` | warning | `status.mode` | The fleet status at `/status`, `/status.svg` and `/api/v1/status` answers without an account. It names nothing, but anyone who can reach the controller learns that the fleet exists, which release it runs, whether it is blocked, roughly how busy it is and the codes of its current problems. Set `status.mode` to `authenticated` if only people with an account should see it. |
| `status.public_no_tls` | error | `status.mode` | `status.mode` is `public` on a listener that is not on loopback and does not terminate TLS, so the address it invites people to would carry every sign-in in the clear. Set `server.tls.mode` to `self-signed` or `files`, bind `server.bind` to loopback behind a proxy that terminates TLS, or set `status.mode` to `authenticated`. |
| `status.mode` | error | `status.mode` | Not a mode. They are `off`, `authenticated` and `public`. |
| `log.level` | error | `log.level` | Not a level. They are `debug`, `info`, `warn` and `error`. |
| `log.format` | error | `log.format` | Not a format. They are `text` and `json`. |
| `log.debug` | info | `log.level` | Debug logging is on, which is loud and includes request detail. |
| `limits.loopback` | warning | the `limits.*` key named | A ceiling is set on a controller only this machine can reach. It guards an instance many people reach against one caller spending what the others need; with nobody else able to reach this one it can only refuse you. Set it back to 0, or leave it if the controller is about to go behind a proxy or get an external URL. |
| `limits.negative` | error | the `limits.*` key named | A ceiling cannot be negative. Set it to 0 for no ceiling, or to the most this instance should hold. |
| `ui.capacity_map.layout` | error | `ui.capacity_map.overview_layout`, `ui.capacity_map.hosts_layout` | Not a layout the host capacity map can open in. They are `overlay`, every host on one chart, and `split`, a chart for each. |

## Configuration: infrastructure providers

Every code here is silent while `provider.enabled` is false, which is the
default. A deployment that does not rent machines is not told how to bound
something it is not doing.

| Code | Severity | Setting | What to do |
| --- | --- | --- | --- |
| `provider.interval` | error | `provider.interval` | Must be positive. It is how often machines are reconciled. |
| `provider.timeouts` | error | `provider.create_timeout` | A provider operation has no bound, or `provider.ambiguity_timeout` is not longer than `provider.create_timeout` — which would quarantine machines that are merely still being built. |
| `provider.enrol_timeout` | error | `provider.enrol_timeout` | Shorter than the silence that loses a host, so machines that did arrive would be given up on. |
| `provider.no_ceiling` | warning | `provider.max_machines` | Providers are on but the ceiling is none, so nothing will be rented however much work queues. A maximum of none is none, as it is for a pool's `max_runners`. |
| `provider.paused` | info | `provider.paused` | New machines are held by configuration. Draining, deleting, recovery and ownership checks all continue. |
| `provider.delete_grace_short` | warning | `provider.delete_grace` | A machine whose host goes briefly quiet would be destroyed mid-job. Ninety seconds of silence only makes a host unhealthy; the runners on it are not given up until five minutes, and until they are the fleet still believes they are running jobs. Set it well above that, such as `10m`. |
| `provider.idle_timeout_short` | warning | `provider.idle_timeout` | A quiet host carries no runner anything will place, so it reads as idle from the moment it falls silent. Set at or below the five minutes its runners are given, a machine is drained for a network blip rather than for being unwanted. Set it above that, such as `15m`. |
| `provider.scale_down_fast` | warning | `provider.scale_down_cooldown` | Machines are removed sooner than one idle period, so the quiet between two bursts pays the creation cost again. |

## Runtime: hosts and installations

| Code | Severity | What it means |
| --- | --- | --- |
| `host.unhealthy` | **error with runners on it, warning without** | The host has stopped heartbeating. With runners recorded on it their state is unknown, which is worse than a spare host being down. |
| `host.cordoned_with_work` | warning | A cordoned host could run jobs that are queued. Cordoning is deliberate, so this is a reminder rather than a fault. |
| `host.duplicate_agent` | warning | Two agent sessions have used this host's credentials in turn. An agent takes a new session each time it starts and never returns to an old one, so alternation means a second agent holds a copy of the token — usually a cloned VM or a copied state directory. Neither is refused: both are running real jobs, and picking one would end the other's. |
| `host.runtime_recovering` | warning | A host's agent reports that its container runtime failed — it could not be reached, or it did not answer in time — and it is holding new starts until one recovery attempt. Running jobs continue. The entry names which failure in a row this is, when the agent reported it and when the attempt is due, on the controller's clock, with the backend's own error. For a runtime that cannot be reached, start it or give the agent's user its socket (for Docker, `sudo systemctl start docker`); for one that is slow, look at its load and disk and lower the host's capacity if it carries more than the machine can. It is kept on the host row, so a controller restart does not lose it, and clears on the next heartbeat after a start succeeds. |
| `host.image_pull_failed` | warning | A runner start or prewarm on a host failed because its pool's image could not be made ready. The entry names the host, the pool, the image and the registry it comes from — `docker.io` for a bare name, otherwise the reference's host with its port — so a host whose egress blocks `ghcr.io` says so instead of showing runners that never register. Check the host can reach the registry and is logged in to it for a private image (`docker pull` on the host gives the daemon's own answer), and that the tag exists. Cleared by the next start or prewarm of the same pool that succeeds on that host. |
| `host.throttled` | warning | One or more hosts have been stepped down after sustained pressure: CPU pinned with a runner no limit binds, a load average past twice the cores, or memory at the reserve. The entry carries each host's own sentence — how many of its slots it is left, which measurement did it, what the running jobs are getting (never less than half their CPU allocation), and that it lifts one step after five minutes of calm. Wait for it, lower the host's capacity or the pools' limits so its runners fit the machine, or lift it from the host card (**Lift the throttle**) once the cause is fixed; a cleared throttle comes back on the next heartbeat if the pressure is still there. A host that keeps being throttled has too many slots for its machine. |
| `host.overprovisioned` | warning | A measured host has more slots than its machine can carry: more than it has allocatable CPUs, or more than it has 2 GB of allocatable memory for. Where every pool that can place on the host has a minimum below a core or 2 GB, that minimum is the size a slot is judged against instead, because those runners are accepted at it. A slot is two containers wherever a `docker_mode: dind` pool places on the host, whether or not it typed its own CPU and memory — a pool that typed them is given those figures a second time for the daemon its builds run in, and a pool sized by its host still splits one slot between a runner and that same daemon, so either way the machine has to be twice the size per slot and the detail names the pool that made it so and whether it typed its limits. The detail names the machine, the capacity and — with default limits on — the share each runner is being given, which is what a job there crawls on; with them off it says nothing limits the runners at all. The share is only given where the host's daemon can enforce it and the pool runs on a container backend; elsewhere nothing limits the runners either. The fix names the largest capacity that fits, never below one, or says the machine is too small to run a runner well when even one slot does not. Lower the capacity on the host card or with `PATCH /api/v1/hosts/{id}` — a host's capacity is decided once when it joins and a heartbeat never rewrites it, so `--capacity` on a fresh join token applies only at the next join, and `agent.capacity` answers only for the embedded host — or add a host. A `dind` pool sized by its host that cannot give both containers a comfortable share at all is refused the host outright rather than merely counted here — see [hosts and pools](hosts-and-pools.md#default-allocations). |
| `host.limits_unenforceable` | warning | A host's Docker or Podman daemon has said it cannot apply a CPU quota, a memory limit or a pids limit. A CPU quota it cannot apply is refused at create, so a pool that sets one fails every runner it starts there; a memory limit is dropped, so the pool's limit binds nothing; a pids limit is ignored. No default is given on that field either way. On a rootless daemon the fix is to delegate the controllers to its user (`Delegate=cpu cpuset io memory pids` in a drop-in for the user slice); on a root daemon it is cgroup v2, or a kernel built with the controller that is missing. |
| `host.limits_unverified` | info | A host's agent predates the limits probe, so the controller does not know whether its daemon can apply a limit and gives its runners no default CPU or memory, because a limit sent to a daemon that cannot apply it fails the create. Pools that set their own limits are unaffected. Upgrade the agent: the probe arrives with the next heartbeat, with no re-join, and defaults start on the next runner. Raised only while `scheduler.default_runner_limits` is on, since with it off an unverified probe changes nothing. |
| `host.shared_folder_unmounted` | warning | A host cannot give its runners a kept tool cache, for one of two reasons the detail names. The first: a containerised agent — the controller's embedded one, or an agent in a container — runs runners, but its container does not mount the [shared folder](configuration.md#the-shared-folder) from the host at its own path. The folder it would use is inside its own data volume, where the host's Docker daemon, which binds runners' caches by that path, cannot see it; handing it out would give every runner an empty cache folder owned by root that no setup action can write to. So the agent keeps no tool cache instead, and the pools that keep one, named in the detail, run their jobs without it. The second: a tool cache folder in the shared folder exists but the runner (uid 1001) cannot write to it — typically one the daemon created as root before the mount was fixed — which would fail every setup action that downloads with `EACCES`; runners start without it instead. Raised only while such a pool could run on the host. Add the mount — the detail gives the line — or run `zoomies upgrade --yes` on the host, which offers it; open or delete the unwritable folder as the detail says. The next heartbeat after the fix clears it. |
| `host.resources_unknown` | **warning with default limits on, note otherwise** | One or more hosts have reported no CPUs, no memory and no disk. They are placed by slot count alone, which is how every host was placed before agents learnt to measure themselves, so nothing is broken — but a pool's resource limits cannot be fitted against a machine nobody has measured, every figure on the host's card is missing, and with `scheduler.default_runner_limits` on its runners get no default either, because their share of an unmeasured machine cannot be computed. A pool with no limits of its own therefore runs unlimited there, which is the shape defaults exist to stop, and that is what makes it a warning. Upgrading the agent fixes it on the next heartbeat, with no re-join. |
| `installation.unhealthy` | error | The GitHub App installation is not usable — the App was uninstalled, its key was rotated, or its permissions were changed. Nothing can register until it is fixed. An App that is merely not subscribed to `workflow_job` is *not* this: the fleet works on the fallback poller, more slowly, and `webhook.never_received` is the entry for it. |
| `webhook.rejected` | warning | Deliveries arrived and were refused, almost always a signing-secret mismatch. |
| `webhook.never_received` | warning | No webhook has ever arrived, so scaling is running entirely on the poller. |
| `tailcat.unavailable` | warning | The controller's private-connection listener has no Tailcat relay it can reach, so hosts enrolled with a [private connection](private-hosts.md) cannot heartbeat or take work; direct hosts are unaffected. The entry carries the last attempt's reason. It is the platform's, because the fix is the controller's own outbound access. Nothing needs restarting: the controller retries every twenty seconds, tries the relay its identity was sealed with first and another only while that one does not answer, and the entry clears once one does. A listener that cannot start no longer stops the controller starting. |

## Runtime: pools, jobs and runners

| Code | Severity | What it means |
| --- | --- | --- |
| `pool.no_capacity` | **error with jobs waiting, warning without** | The pool cannot start the runners it wants. The entry carries the scheduler's own reason: no host matches its selector, every host is full, or every host is cordoned. |
| `pool.runners_failing` | **error with jobs waiting, warning without** | Runners are being created and dying before they register. The fix is the category of the most recent failure — an image that cannot be pulled, a registration GitHub refused, a container backend that is not answering — and the usual causes only when nothing narrowed it. This is the failure with no failed job behind it: the jobs stay queued, nothing is marked failed, and every other count reads as a fleet that is merely busy. |
| `pool.github_rate_limited` | warning | The pool has jobs waiting and its installation is inside a GitHub rate-limit backoff, so the scheduler is creating nothing for it rather than spending another call on a quota that is already gone. It lifts on its own when `poller.paused` does. |
| `scheduler.registration_throttled` | warning | The last scheduling pass had a host chosen for a runner and did not create it, because the installation was already at `scheduler.registration_concurrency` credential requests in flight. Nothing fails: the demand is kept and a later pass takes it, so it shows as runners appearing slowly. It is listed because it is the one limit that used to bind silently — the pool would report jobs waiting, and an operator reading that adds hosts, which cannot help when hosts are not what ran out. Raise the setting if the installation's GitHub quota has room; leave it if the fleet is deliberately gentle with that quota. |
| `controller.lease_lost` | **error** | Another controller has taken this database's lease, so two are running against it. Both are scheduling, and they will mint runners against each other and remove each other's workloads. Stop one; the survivor restarts with `--takeover`. |
| `pool.runner_group_unresolved` | warning | A pool asked for a runner group its target does not offer, or GitHub would not say which groups exist, so its runners registered in Default instead of the isolation boundary the pool requested. |
| `pool.runner_group_public_repositories_blocked` | warning | GitHub reports the pool's runner group as unavailable to public repositories. The runners can register, connect and appear idle, but GitHub will leave matching public-repository jobs queued. Enable **Allow public repositories** for that runner group under the organisation's **Settings > Actions > Runner groups**, or use a repository-scoped installation. |
| `pool.repository_scale_up_deferred` | warning | The per-repository creation limit held runners back. Expected under a burst; standing means the limit is too tight. |
| `jobs.unmatched` | warning | Jobs are queued that no enabled pool here will run. Usually their labels match no pool: they may belong to another runner provider, or a pool may be missing a label. The entry says so instead when the cause is the GitHub target rather than the labels — a pool advertising exactly those labels but belonging to another installation, or a repository no installation here covers — because those need the installation changed, not the workflow. |
| `jobs.runner_lost` | warning | A job's runner stopped under it, so the failure is the fleet's rather than the workflow's. When most of them share one category, the detail says so and the fix is that category's. |
| `runners.failed` | warning | Runners are in the failed state with their reasons recorded. |
| `runners.cleanup_failed` | warning | Zoomies could not finish taking a runner away: a container still on its host, or a registration still on GitHub. Different from `runners.failed`, which is a job that did not run — this is something *left behind*. Most often GitHub's own bookkeeping is a few seconds behind the webhook that told Zoomies the job was done, and this clears on the next retry with nothing to do; see [Cleanup](troubleshooting.md#what-zoomies-cleans-up-and-what-it-leaves) for the other two shapes and what each needs. |
| `runners.not_progressing` | warning | Runners have sat in `provisioning` or `registering` for over half the provision timeout, so the fleet says so while there is still time to look rather than only when it fails them. The entry splits the two shapes, because they are not fixed in the same place: a runner still waiting for a container is a backend or image problem on the host, and one whose container started without registering is the runner process failing to reach GitHub. |
| `capacity_demand.delivery_failed` | warning | An external capacity provisioner did not accept the latest event, after its retries. |
| `controller.update_available` | info | A newer release of Zoomies has been published than the one this controller was built from. Nothing is wrong and nothing updates itself: runners, pools and jobs do not depend on the controller's version. It appears only on a controller built from a release tag, and `updates.check_interval: 0` switches both the check and this entry off. |
| `controller.development_update_available` | info | The controller runs the moving development channel but its stamped commit is not the current head of `main`. A main CI image publication may still be running, or failed or cancelled before advancing `:dev`; an upgrade can only pull what was successfully published. Let main CI publish, then run `zoomies upgrade` again. |
| `controller.loop_panicked` | error | A background loop panicked and was restarted. The fleet keeps running, but this is a bug: the stack is in the log, and it is worth reporting. |
| `controller.problems_partial` | **error** | One of the queries behind this list failed, so the list is incomplete and the entry names which sections are missing from it. It exists because the alternative is worse: this page used to return a 500 for any one failing query, and an operator whose drawer will not load reads that as a fleet with nothing wrong. The controller log carries the query that failed. |
| `backup.failed` | warning | The scheduled backup is failing, and the entry carries the error. Usually the directory `backup.directory` names is missing, not writable by the controller, or out of room for a copy of the database. The controller tries again every fifteen minutes; taking one from the Backups tab shows the same error in the page. |
| `backup.remote_shadowed` | warning | A destination stored from the Backups page has the same name as one `zoomies.yaml` or the environment describes, and the file has the last word — so nothing is sent to the stored one and its settings are not the ones in use. Rename one of them, or delete the stored destination and keep describing it in the file. |
| `backup.remote_unreadable` | **error** | A stored destination's sealed secret key or passphrase does not open with this controller's encryption key, so nothing can be sent to it. This is what a database restored onto a host with a different key looks like: put the key this fleet was sealed with back, or open the destination on the Backups page and enter its secret key again. |
| `backup.remote_failed` | warning | Backups are not reaching one of the destinations under `backup.remotes`, and the entry carries the service's own refusal: a wrong secret, a bucket that is not there, a policy that does not allow writing, or a clock too far from the service's to sign with. The controller tries again every fifteen minutes and the Backups tab tests the remote on demand. The copies on this host are unaffected — they are simply all there is. |
| `backup.restore_staged` | warning | An administrator has staged a restore from the Backups tab, and it is waiting for the controller to restart. Nothing has changed yet: the database is swapped when the next controller starts, before it opens anything, and the restored fleet comes back fenced. Restart the controller from the tab to apply it, or cancel it there. |
| `backup.restore_failed` | **error** | The last staged restore did not happen — the entry says why — and the controller started on the database it already had. Put right what the reason names and stage the restore again; the entry clears when it is dismissed from the Backups tab. |
| `recovery.fenced` | error | This fleet was restored from a backup and is held: the scheduler decides as normal and applies none of it — no runner is created, drained or removed, nothing is reaped from GitHub, and the fallback poller does not sweep. The Overview shows what it *would* do, so you can tell "nothing to do" from "not allowed to". `/readyz` answers 503 while it is on; liveness is unaffected, so a container runtime does not restart it. The fix names the three things a restore does not bring with it, and lifting is `POST /api/v1/recovery/unfence`. |
| `host.version_behind` | warning | One or more hosts run a different release from the controller. A fleet part-way through an upgrade looks like this and clears itself, which is why it is a warning; one that stays this way has a host somebody has forgotten, running an agent whose odd behaviour has an explanation nobody thinks to look for. The entry names which hosts are behind, which are *ahead* — the direction the policy calls unsupported, where the fix is to upgrade the controller — and which run a build the controller cannot order against its own. Two builds of one tag are the same release and do not appear. |
| `pool.size_strands_hosts` | info | A pool asks for a fixed CPU and memory on every host, and some of its hosts could hold more runners of it than their slot count allows — so the machine above that slot count goes unused. Not wrong, and often deliberate; it is the state a fixed size drifts into as a fleet acquires unequal machines. Raise those hosts' capacity, or clear the pool's CPU and memory so each runner is given one slot's share of the host it lands on, which fills every slot on every machine whatever size it is. |
| `pool.provision_timeout_short` | warning | A pool's own provision timeout lands inside the time its runners may legitimately take to start: the agent's create budget, plus the Docker wait a pool that provides a daemon does before it registers. Runners still coming up are failed and replaced, and the replacement pulls the same image over the link that was slow to begin with. This is `scheduler.provision_timeout_short` asked of one pool, because a pool may override either half for itself. Raise the pool's provision timeout above the two together, or clear it to follow the fleet. |
| `pool.size_unlimited` | warning | A pool sets no CPU or memory limit — which normally means each of its runners is given one slot's share of the machine it lands on — while `scheduler.default_runner_limits` is off, so that share is charged against the host and never applied. A host's worth of these runners can each take every core at once, which is the shape that stops the Docker daemon answering. Turn `scheduler.default_runner_limits` back on, which is the default, or give each pool a size of its own. |
| `pool.resources_unenforced` | warning | A pool on the `process` backend sets CPU, memory or disk limits, and that backend starts a runner as a plain process with no cgroup — so the limits bind nothing. The scheduler still holds that much room on the host, so the fleet does not oversubscribe; what is missing is the enforcement, and a job that runs away can take the machine with it. Move the pool to `docker` or `podman`, where the same limits become cgroup limits, or clear them. |
| `crypto.key_mismatch` | error | This instance's encryption key does not open the GitHub App credentials in its own database. Every installation fails at once and nothing can authenticate to GitHub, which is what tells this apart from `installation.unhealthy` — that one is fixed on GitHub, this one by putting the right key file back. It is what a restore that brought the database and left the key behind looks like once the instance is running; the startup refusal catches the case where the key file is missing entirely. |
| `poller.stale` | warning | The fallback poller is enabled and has not finished a sweep for more than two and a half intervals. It is the safety net for a fleet whose webhooks stop arriving, and a net that has stopped sweeping looks exactly like a net with nothing to catch. The controller's log carries the error that ended the sweep. |
| `poller.paused` | warning | GitHub is rate-limiting one installation, so every background sweep is standing down from it until the moment named. It clears itself. One installation is held at a time -- the quota is per installation -- so the others are still polled, and the entry names which one. |
| `pool.max_above_room` | warning | The pool's maximum is more runners than its hosts have room for at the size one of its runners is given there — a pool with a minimum counts the one reduced runner a host too small for another standard one still takes, at what it would be given. It is not wrong — the maximum is a backstop rather than a target — but the runners above the room are runners the scheduler will never create, and the jobs that ask for them wait with nothing else on any page saying why. Lower the maximum, ask for less per runner, or give the pool more hosts. |
| `pool.host_overcommitted` | warning | A host this pool can land on promises more runner slots than its machine can back at the size this pool typed — a pool sized by its host cannot raise it, because a slot is exactly one of its runners. The slots above what fits read as free capacity everywhere they are counted, and every create for one of them is refused for want of CPU or memory. Adjust the host to the slots its machine can back, or give the pool's runners less. |
| `pool.elastic_cpu_unsupported` | warning | The pool lends CPU to busy runners, and some of the hosts it can land on run an agent too old to say it can move a live runner's quota. A runner placed there is held at its guaranteed share, exactly as with elastic CPU off, and nothing on the pool says which of its runners that happened to. The dry run names the hosts: upgrade the agent on each — the command is on the host's card — or keep the pool on observe, which asks nothing of the agent, until they are. |
| `pool.cache_above_disk` | warning | The pool's cache size limit is above the free disk on the smallest host it can land on. The cache is evicted down to its limit between one runner and the next, so a limit above the free space is not a limit at all: the disk fills first, and a host at or below its disk reserve takes no runner of any pool. |
| `pool.dangerous` | warning | The pool was configured to weaken the isolation between a workflow job and the host it runs on — the same sentence the pool page shows for the setting itself, so an operator sees one fact in both places rather than learning it twice. Edit the pool if this was not deliberate. |
| `pool.docker_client_missing` | error | The pool's `docker_mode` gives its jobs a Docker daemon, and the image its runners boot carries no client to reach it with — so the daemon comes up unused and every job fails at its first Docker step with `Unable to locate executable file: docker`, which names the missing binary and not the reason. A pool that asks for a daemon is moved onto `ghcr.io/eyupio/zoomies-runner-docker` wherever that image is known to exist; a digest names one exact image, and a tag from another build may name a run whose variant was never published, so those are left as you set them. Pin the variant at the same tag or digest, or clear the pool's image so it follows the fleet's default and is moved for you. |
| `pool.cache_shared` | warning | Under an organisation installation, GitHub can hand this pool's runners any repository's job whose `runs-on` matches its labels — Zoomies has no say in which repository that is. A repository-scoped cache is therefore only as private as the pool's labels: give the pool a branded label that only the intended repository's workflows use. A repository-targeted installation registers runners only that repository's jobs can reach, so this never fires there. |

## Runtime: infrastructure providers

Every code here is silent while `provider.enabled` is false, which is the
default, and every one of them is about money: a machine Zoomies cannot account
for is a machine somebody is still being billed for.

| Code | Severity | What it means |
| --- | --- | --- |
| `provider.unreachable` | **error with a machine mid-operation, warning without** | The hypervisor or cloud API could not be reached. Nothing is created or deleted while it cannot be — a timeout is never evidence about a resource — so a machine half-built behind it is stuck and being paid for, which is what raises the severity. |
| `provider.credentials_refused` | error | The credential was refused. A credential that is valid but not permitted names the privilege it is missing, because "this token is wrong" and "this token lacks a privilege on a path" are an afternoon apart. |
| `provider.quota_exhausted` | **error with jobs waiting, warning without** | The provider refused for want of capacity or against a limit of its own. The same circumstance rule as `pool.no_capacity`: a full provider with nothing queued is the system working, and the next machine released clears it. Draining and deleting carry on regardless. |
| `provider.machine_failed` | warning | A machine never reached ready, and the entry carries the provider's own words. The row is kept because the resource behind it may still exist; releasing it is an operator's decision, never Zoomies'. |
| `provider.bootstrap_failed` | error | The machine came up and its agent never did, and the entry carries the guest's own output. The usual causes are a template without the agent installed, a guest agent that is not answering, and — the one that looks like a host flapping instead — an `agent.json` left in the template, so every clone enrols as the same host. |
| `provider.ownership_unverified` | error | A machine and its resource disagree about who owns it, so the machine is quarantined. Nothing will act on it until a person does: not a drain, not a delete, not a retry. The entry says which of the four ownership facts failed. |
| `provider.orphan_found` | error | A resource wearing this fleet's naming has no machine row. It is listed on the provider's orphan tab and it is **never** deleted automatically — one left over from a lost database is yours to remove, and one still doing work belongs to something else. |
| `provider.delete_pending` | warning | A delete was issued and the provider has not confirmed the resource is gone. A 200 from a delete call is not a confirmation; an inspect that cannot find it is. Until then the resource may still be costing somebody money, so it is said out loud rather than assumed. |
| `provider.unservable` | warning | No enabled pool could ever place a runner on this provider's machines — the backend, platform or host selector rules them all out. Anything it buys would sit idle and be paid for. |
| `provider.template_unverified` | warning | Nothing has confirmed this provider's credential, template and placement since its settings changed. The connection check changes nothing and says what it found; the alternative is discovering it on the first machine. |
| `provider.provisioning_paused` | info | The kill switch is on, by configuration or on the provider's row. Nothing is stranded: draining, deleting, recovery and the ownership sweep all continue, which is the whole point of the switch stopping creates alone. |
| `provider.contract_unsupported` | error | The provider driver's declared contract range does not contain this build's, so it cannot be used. The machines it already owns stay visible, drainable and deletable: a version mismatch never strands a running machine. |

## Runtime: infrastructure providers

Raised only when a [provider](providers.md) is configured. The three that name a
machine link to it, because the machine's own page carries the failure in the
words of whatever refused it — Proxmox's task error for a clone, the guest's own
standard error for a bootstrap.

| Code | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `provider.unreachable` | warning, or **error** with a machine mid-operation | The hypervisor could not be reached. This is never evidence about a resource: nothing is created, failed or deleted on the strength of it. | Check the endpoint, the network and the certificate. Machines already running are unaffected. |
| `provider.credentials_refused` | error | The API token was refused, or is not allowed to do something. Where the provider named a privilege, so does this. | Run the provider's check, which lists every missing privilege and the path it is needed on. |
| `provider.quota_exhausted` | warning, or **error** with jobs queued | The hypervisor refused for want of capacity. New machines stand down for a while; drains and deletes continue. | Free space or capacity, or lower the provider's limit so the fleet stops asking. |
| `provider.machine_failed` | warning | A machine never reached ready, and carries the provider's own words for why. | Read the machine's page. Three failures in a row stand the provider down, so a bad template costs a few machines rather than fifty. |
| `provider.bootstrap_failed` | error | The machine came up and its agent never did. The guest's own standard error is on the machine's page. | Usually the template: a missing guest agent, a missing Zoomies binary, or an `agent.json` left in the image. See [Proxmox VE](proxmox.md#preparing-the-template). |
| `provider.ownership_unverified` | error | A machine is quarantined: its row and the resource disagree about who owns what. **Nothing will touch it again until a person does.** | Look at both sides. Then either release the row, which forgets the machine without touching the resource, or remove the resource by hand. |
| `provider.orphan_found` | error | A resource wearing this controller's marks has no row behind it. It is never deleted automatically — it may belong to another live fleet. | Review it on the provider's Orphans tab. A restored database and a row pruned early are the two ways this happens. |
| `provider.delete_pending` | warning | A delete was issued and the resource has not been confirmed gone. Something is still costing money. | Check the provider. A delete is only complete when an inspection cannot find the resource; a 200 from the delete call is not that. |
| `provider.unservable` | warning | No pool could ever place a runner on this provider's machines, so anything it rents is money for nothing. | Match the provider's machine shape, labels and platform to a pool, or disable it. |
| `provider.template_unverified` | warning | The provider's settings changed and its prerequisites have not been checked since. | Run the check. It creates nothing. |
| `provider.provisioning_paused` | info | The kill switch is on. Draining, deleting, recovery and ownership checks all continue; only creation is held. | Nothing, unless you did not mean it. |
| `provider.contract_unsupported` | error | A provider declares a contract version this build does not speak. Machines it already owns stay visible, drainable and deletable — a version mismatch must never strand a running machine. | Upgrade whichever side is behind; the message names both numbers. |

## A provider's preflight

Raised by a provider's own check, which creates nothing. They appear on the
provider's page, in the configuration wizard and in the problems drawer, in the
same shape as every other finding — because a refused credential and a template
that is not a template are *answers*, not failures.

`provider.*` codes come from any provider; `proxmox.*` from the Proxmox one.

| Code | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `provider.preflight_failed` | error | The provider refused its connection check. | Read the message; it carries the provider's own words. |
| `provider.zone_missing` | error | The provider has no zone (node, region) configured, so there is nowhere to put a machine. | Set one. |
| `proxmox.unreachable` | error | The cluster could not be reached at all. | Check the endpoint, the port (8006), the network and the certificate. |
| `proxmox.credentials_refused` | error | The API token was refused outright. | Check the token's user, realm, token id and secret. The form wants them exactly as Proxmox printed them: `user@realm!tokenid=secret`. |
| `proxmox.privilege_missing` | error | The token is valid and not allowed to do something. The finding names the privilege **and** the path it is needed on. | Grant that privilege. [Proxmox VE](proxmox.md#the-api-token) lists every one and why it is needed. |
| `proxmox.insecure_tls` | warning | Certificate verification is off, so the token crosses to whoever answered. | Paste the cluster's CA — `/etc/pve/pve-root-ca.pem` — into the provider's CA field instead. See [Security](security.md). |
| `proxmox.version_unqualified` | warning | The cluster is older than the release this integration was qualified against. It is not refused, but nothing about it has been tested. | Upgrade, or proceed knowing it is unqualified. |
| `proxmox.node_missing` | error | No node is configured, or the configured one is not in the cluster. | Choose one the credential can see; the wizard lists them. |
| `proxmox.node_offline` | warning | The node is configured and not currently online. | Machines cannot be created there until it returns. |
| `proxmox.storage_missing` | error | No storage is configured, or the configured one does not exist on that node. | Choose one the wizard lists. |
| `proxmox.storage_no_images` | error | The storage exists and does not accept disk images, so a clone has nowhere to land. | Choose a storage whose content types include `images`. |
| `proxmox.storage_inactive` | error | The storage exists and is not active. | Bring it up, or choose another. |
| `proxmox.bridge_missing` | error | No network bridge is configured, or the configured one is not on that node. A machine with no network cannot reach this controller to enrol. | Choose a bridge that can reach the controller. |
| `proxmox.template_missing` | error | No template VMID is configured, or nothing exists at it. | Prepare a template as the [runbook](proxmox.md#preparing-the-template) describes and give its VMID. |
| `proxmox.template_not_a_template` | error | A VM exists at that VMID and is not a template. Cloning a running VM is not what this does. | Convert it to a template, or point at the right VMID. |
| `proxmox.template_no_agent` | warning | The template does not have the QEMU guest agent enabled. Enrolment reaches the guest through it, so a machine made from this template will boot, cost money and never join. | Install `qemu-guest-agent` in the image and set `agent: enabled=1`. |
| `proxmox.vmid_range` | error | No VMID range is configured, or its bounds are the wrong way round. The range is both a budget and a blast radius: a VM outside it is by construction not ours. | Give a block nothing else allocates from. |
| `proxmox.vmid_range_reserved` | warning | Guests already exist inside the configured range. They are not touched, but the range is meant to be Zoomies' alone. | Move the range, or move those guests. |

| `scheduler.registration_concurrency` | error | Credential request concurrency is outside 1–16. | Use 1 by default; increase only with measured need. |
| `agent.prewarm_timeout` | error | Background preparation budget is outside 1s–15m. | Use 5m by default and restart agents. |
| `agent.prewarm_jitter` | error | Background preparation stagger is outside 0s–5m. | Use 30s by default, or 0s to disable, and restart agents. |

## What the status page says

When `status.mode` is `authenticated` or `public`, the fleet status at
`/status` shows each of the fleet's current problems to people who may have
no account and cannot act on the fleet. They are shown the code and the
sentence below, never the operator's title, detail or fix, because those
name the pool, host or repository a problem is about. Only the fleet's codes
have a sentence: a platform code never reaches the status page at all.

The sentences are held in `publicSentences` in
`internal/controller/status.go`, and `internal/docs` checks this table
against it in both directions, word for word.

| Code | What the status page says |
| --- | --- |
| `controller.problems_partial` | The controller could not check everything, so this status may be incomplete. |
| `host.cordoned_with_work` | A machine is being taken out of service once its current jobs finish. |
| `host.duplicate_agent` | Two machines are claiming to be the same one, so jobs may not be placed on it. |
| `host.image_pull_failed` | A machine cannot download the runner image, so jobs that need it may wait. |
| `host.limits_unenforceable` | A machine cannot hold runners to their size, so jobs may run slower than usual. |
| `host.limits_unverified` | A machine has not yet confirmed it can hold runners to their size. |
| `host.overprovisioned` | A machine is promised to more runners than it can hold at once. |
| `host.resources_unknown` | A machine has not reported how much room it has, so it is used cautiously. |
| `host.runtime_recovering` | A machine's container runtime is recovering, so it is taking no new jobs for now. |
| `host.shared_folder_unmounted` | A machine keeps no tool cache, so jobs there download their tools again and start slower. |
| `host.throttled` | A machine is busy enough that it is taking new jobs more slowly. |
| `host.unhealthy` | A machine has stopped checking in, so fewer jobs can run at once. |
| `host.version_behind` | A machine is running an older agent than the controller. |
| `installation.unhealthy` | The fleet has lost a permission it needs on GitHub, so jobs from some repositories cannot start. |
| `jobs.runner_lost` | A runner stopped while running a job, so that job may fail or need a re-run. |
| `jobs.unmatched` | Some jobs ask for runner labels this fleet does not offer, so they will wait until the workflow or the fleet changes. |
| `poller.paused` | The controller has stopped asking GitHub for queued jobs, so a missed notification is not caught. |
| `poller.stale` | The controller has not heard from GitHub recently, so new jobs may be noticed late. |
| `pool.cache_above_disk` | A runner cache is set larger than the disk it lives on. |
| `pool.cache_shared` | A runner cache is shared more widely than usual. |
| `pool.dangerous` | Some runners are set up with more access to their machine than the safe default. |
| `pool.docker_client_missing` | Jobs that use Docker may fail because their runner image lacks the Docker client. |
| `pool.elastic_cpu_unsupported` | Runners cannot borrow spare CPU on some machines, so busy jobs run at their normal size. |
| `pool.github_rate_limited` | GitHub is limiting how fast the fleet can register runners, so jobs may wait longer. |
| `pool.host_overcommitted` | Runners are sized to more than their machines have, so jobs may run slower. |
| `pool.max_above_room` | The fleet is allowed more runners than its machines have room for. |
| `pool.no_capacity` | No machine has room for some jobs right now, so they wait until one frees up or a machine is added. |
| `pool.provision_timeout_short` | Runners are given less time to start than they usually need, so some may be retried. |
| `pool.repository_scale_up_deferred` | Starting runners for some repositories is held back until GitHub allows it. |
| `pool.resources_unenforced` | Runner sizes are not enforced on some machines. |
| `pool.runner_group_public_repositories_blocked` | Jobs from public repositories are not allowed on some runners, so they will wait. |
| `pool.runner_group_unresolved` | Some runners cannot be registered in the group they belong to, so jobs for them wait. |
| `pool.runners_failing` | Runners are failing to start, so jobs are waiting longer than usual. |
| `pool.size_strands_hosts` | Runners are sized larger than some machines can hold, so those machines stay idle. |
| `pool.size_unlimited` | Some runners have no size limit, so one job can slow the others on its machine. |
| `provider.bootstrap_failed` | A newly rented machine failed to join the fleet. |
| `provider.contract_unsupported` | The fleet cannot rent machines from one of its providers. |
| `provider.credentials_refused` | A machine provider refused the fleet's credentials, so no new machines can be added from it. |
| `provider.delete_pending` | A machine the fleet no longer needs is still being removed. |
| `provider.machine_failed` | A machine the fleet asked for did not arrive, so jobs may wait for room. |
| `provider.orphan_found` | A machine provider has a machine the fleet cannot account for. |
| `provider.ownership_unverified` | The fleet cannot confirm it owns a machine, so it is leaving it alone. |
| `provider.preflight_failed` | A machine provider failed its checks, so no new machines can be added from it. |
| `provider.provisioning_paused` | Adding machines is paused, so jobs wait for the machines the fleet already has. |
| `provider.quota_exhausted` | A machine provider has no more room for the fleet, so jobs wait for the machines it has. |
| `provider.template_unverified` | A machine template has not been checked yet. |
| `provider.unreachable` | A machine provider cannot be reached, so no new machines can be added from it. |
| `provider.unservable` | Some jobs need machines no provider can supply. |
| `provider.zone_missing` | A machine provider is missing a location the fleet is set to use. |
| `proxmox.bridge_missing` | A machine provider is missing a network the fleet is set to use. |
| `proxmox.credentials_refused` | A machine provider refused the fleet's credentials, so no new machines can be added from it. |
| `proxmox.insecure_tls` | The fleet talks to a machine provider without checking its certificate. |
| `proxmox.node_missing` | A machine provider is missing a server the fleet is set to use. |
| `proxmox.node_offline` | A server at a machine provider is offline, so fewer machines can be added. |
| `proxmox.privilege_missing` | The fleet lacks a permission at a machine provider, so some machines cannot be added. |
| `proxmox.storage_inactive` | Storage at a machine provider is inactive, so no new machines can be added there. |
| `proxmox.storage_missing` | A machine provider is missing storage the fleet is set to use. |
| `proxmox.storage_no_images` | Storage at a machine provider cannot hold machine images. |
| `proxmox.template_missing` | A machine provider is missing the template new machines are made from. |
| `proxmox.template_no_agent` | The template new machines are made from cannot report their address. |
| `proxmox.template_not_a_template` | The template new machines are made from is set up the wrong way. |
| `proxmox.unreachable` | A machine provider cannot be reached, so no new machines can be added from it. |
| `proxmox.version_unqualified` | A machine provider runs a version this fleet has not been tested with. |
| `proxmox.vmid_range` | A machine provider's numbering range is too small for the machines the fleet may need. |
| `proxmox.vmid_range_reserved` | A machine provider's numbering range overlaps machines the fleet does not own. |
| `recovery.fenced` | The fleet is recovering from a restore and is not starting new runners yet. |
| `runners.cleanup_failed` | Some finished runners could not be cleaned up, which uses room new jobs need. |
| `runners.failed` | Some runners failed, so jobs may wait while they are replaced. |
| `runners.not_progressing` | Some runners are taking far longer to start than they should, so jobs are waiting. |
| `webhook.never_received` | The controller has never heard from GitHub directly, so new jobs are noticed late. |
| `webhook.rejected` | GitHub's notifications are being refused because their signature does not verify, so new jobs are noticed late. |

## Keeping this list honest

Every code above is checked against the source by a test
(`TestEveryProblemCodeIsDocumented`), so a code added to the validator or the
controller without a row here fails the build. The severities and the sentences
are prose and are not checked — if one reads wrong, it is wrong.
