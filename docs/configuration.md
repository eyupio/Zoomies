---
title: "Configuration reference: every setting and default"
description: >-
  Where Zoomies keeps its settings — the fleet's database, a small file, and the
  ZOOMIES_* environment — with every key, its default, and the startup warnings
  that name any setting weakening your posture.
---

# Configuring Zoomies

Almost every setting lives in the fleet's own SQLite database and is changed on
the **Settings → Configuration** page. A change made there is kept: it survives
a restart, it is written to the audit trail, and it applies to this controller
and nothing else.

Three keys cannot live there, because they are what gets that database open:
`database.path`, and `security.encryption_key` or `security.encryption_key_file`.
A setting that unlocks a store cannot be stored in that store. Those go in
`zoomies.yaml` or the environment, and that is the whole of what a fresh
install's file contains.

### The four layers

```mermaid
flowchart LR
    d["built-in defaults"] --> f["zoomies.yaml"]
    f --> db["the fleet's database<br/>(the settings page)"]
    db --> e["ZOOMIES_* environment"]
    e --> v{"config.Validate"}
    v -->|"an error"| stop["startup stops, and the message<br/>names what to change"]
    v -->|"a warning"| warn["startup continues -- printed here, and shown<br/>in the UI's problems drawer while it is true"]
    v -->|"info"| note["startup continues -- a default worth knowing<br/>rather than anything wrong"]
    v -->|"nothing to say"| ok["running"]
```

Each layer wins over the one before it, and the settings page says which layer
each value came from, so "I changed it and nothing happened" is a question the
page answers rather than one you have to work out.

**The environment is deliberately last.** It is the way back in when a stored
setting has locked you out of the interface that would fix it — a bind address
nothing can reach, an external URL that breaks the login redirect — and it is
what keeps every containerised deployment working, since those ship their
configuration as environment variables. The cost is that a value typed on the
settings page can be silently overridden, so it is not left silent: a setting an
environment variable is holding is shown in its own group at the foot of the
page, naming the variable, and the API refuses to change it rather than storing
something that would be overridden at the next restart.

To hand such a setting over to the settings page, remove the variable from the
deployment's environment file and restart.

### Upgrading from a file-only install

On the first start after the upgrade, the settings your `zoomies.yaml` actually
spells are copied into the database, attributed to the file, and the file stays
as the layer underneath them. Nothing about what the controller runs changes —
you simply gain the ability to change them. It happens once, so a setting you
later clear back to its default is not poured back in at the next restart, and
an info finding (`settings.imported_from_file`) says it happened.

A key the file never mentioned is left alone. The defaults are worked out on the
host that reads them — the database path from its state directory, the agent's
capacity from its cores — so a row claiming to be "the default" would freeze one
machine's answers for every machine after it.

### From a terminal

When the settings page is the thing that is broken, `zoomies config` does the
same job against a stopped controller:

```sh
zoomies config list                       # what this fleet has stored, and where each value came from
zoomies config get server.bind            # one setting, and which layer won
zoomies config set server.bind 127.0.0.1:8080
zoomies config unset server.bind          # back to the file, or the built-in default
```

It refuses to run while a controller is up, because writing settings under a
process that has already read them leaves the two disagreeing with no way for
either to find out.

### When a setting stops the controller starting

This is the failure the layers make possible: a value saved on the settings page
that the validator refuses at the next start. The page that would fix it is
behind the controller that will not start, and editing `zoomies.yaml` does
nothing — the database sits above the file, so the file cannot win.

Zoomies says so at the moment it refuses. Every finding that names a setting
carries the layer holding it, and a refusal prints the command that takes it
back out:

```text
configuration is not valid:
  - server.bind: "0.0.0.0" is not a host:port address
      fix: use the form "127.0.0.1:8080" or ":8080".
      this value is stored in this fleet's database, so editing the configuration
      file will not change it; with the controller stopped, `zoomies config unset
      server.bind` puts the file or the default back in charge
```

The same sentence appears on the problems drawer and the Configuration tab, for
the settings that are merely unwise rather than fatal — so "I changed it and
nothing happened" is answered before it is asked.

A value the environment is pinning gets the other instruction, because there is
nothing stored to unset: `ZOOMIES_*` is the last word, and the variable has to
go from the environment the controller starts with. That is also what makes it
the way back in when the database is the problem and you would rather not stop
the controller to fix it.

### The file

The parser is strict. A misspelled key is an error naming the line, not a
setting that silently does nothing.

If no file exists and none was named explicitly, the database plus the
environment plus the defaults are used — which is what makes the container image
work with nothing but environment variables.

### Where things live

Two directories, and neither has one fixed answer: they depend on the operating
system and on whether the process is running as root.

| | Configuration | State |
| --- | --- | --- |
| Override | `ZOOMIES_CONFIG_DIR` | `ZOOMIES_STATE_DIR` |
| Linux, as root | `/etc/zoomies` | `/var/lib/zoomies` |
| Linux, as anyone else | `~/.config/zoomies` | `~/.config/zoomies` |
| macOS | `~/Library/Application Support/zoomies` | same |
| Windows | `%ProgramData%\zoomies` | same |

The configuration directory holds `zoomies.yaml` and the encryption key; the
state directory holds the database — which is where the settings themselves
live — and the agents' work areas. Every default
path below that begins `<config dir>` or `<state dir>` resolves through this
table, and `zoomies config print` says what they came out as on this host.

`ZOOMIES_STATE_DIR` is the one to reach for when the database belongs on a
different disk from everything else — it moves the database and the work
directories together, so the two do not have to be set separately.

Every finding carries a code, and the code is the stable half: it is what you
search for and alert on, while the sentence beside it is written for whoever is
reading and may improve. [Problem codes](problem-codes.md) lists all of them —
the validator's and the running controller's — with severities and what to do.

---

## Everything at once

Every setting, with its default and its environment override. These are the
values the settings page edits; the block below is the same list in the shape
`zoomies.yaml` takes, for the deployments that still configure from a file.

The defaults below are the actual defaults. A file containing only the settings
you want to change is the normal case — and a fresh install's file contains only
the two or three keys that have to be in one.

```yaml
server:
  bind: 127.0.0.1:8080          # ZOOMIES_BIND
  tailcat_enabled: true         # ZOOMIES_TAILCAT_ENABLED -- permit private agent connections
  external_url: ""              # ZOOMIES_EXTERNAL_URL  -- required for webhooks
  tls:
    mode: off                   # ZOOMIES_TLS_MODE      -- off | self-signed | files
    cert_file: ""               # ZOOMIES_TLS_CERT_FILE
    key_file: ""                # ZOOMIES_TLS_KEY_FILE
    hosts: []                   # ZOOMIES_TLS_HOSTS     -- names for a generated cert
  trusted_proxies: []           # ZOOMIES_TRUSTED_PROXIES -- CIDRs, or [cloudflare]; 0.0.0.0/0 is warned about
  allowed_origins: []           # ZOOMIES_ALLOWED_ORIGINS -- extra browser origins; "*" is warned about
  allow_indexing: false         # ZOOMIES_ALLOW_INDEXING -- let search engines in
  read_timeout: 30s             # ZOOMIES_READ_TIMEOUT
  write_timeout: 0s             # ZOOMIES_WRITE_TIMEOUT -- 0: SSE and log tails must not be cut off
  idle_timeout: 120s            # ZOOMIES_IDLE_TIMEOUT

database:
  path: <state dir>/zoomies.db  # ZOOMIES_DB_PATH -- see "Where things live" below

security:
  encryption_key: ""                        # ZOOMIES_ENCRYPTION_KEY
  encryption_key_file: <config dir>/encryption.key   # ZOOMIES_ENCRYPTION_KEY_FILE
  session_ttl: 168h                         # ZOOMIES_SESSION_TTL
  cookie_secure: null                       # ZOOMIES_COOKIE_SECURE (derived when unset)
  disable_auth: false                       # ZOOMIES_DISABLE_AUTH
  allow_private_egress: false               # ZOOMIES_ALLOW_PRIVATE_EGRESS (let the URLs it dials be private addresses)
  docker_in_docker_expected: false          # ZOOMIES_DOCKER_IN_DOCKER_EXPECTED
  rate_limit_logins: 10                     # ZOOMIES_RATE_LIMIT_LOGINS (per address per minute, and 5x that per account
                                            #   over 15m); 0 disables it and is warned about

github:
  api_base_url: https://api.github.com   # ZOOMIES_GITHUB_API_BASE_URL
  upload_base_url: ""                    # ZOOMIES_GITHUB_UPLOAD_BASE_URL
  webhook_path: /webhooks/github         # ZOOMIES_WEBHOOK_PATH
  poll_interval: 30s                     # ZOOMIES_POLL_INTERVAL
  poll_fallback: true                    # ZOOMIES_POLL_FALLBACK
  allow_workflow_cancellation: true      # ZOOMIES_ALLOW_WORKFLOW_CANCELLATION
  runner_image: ghcr.io/eyupio/zoomies-runner:latest   # ZOOMIES_RUNNER_IMAGE
  runner_version: ""                     # ZOOMIES_RUNNER_VERSION

agent:
  embedded: true                # ZOOMIES_AGENT_EMBEDDED
  name: <hostname>              # ZOOMIES_AGENT_NAME
  capacity: <cpus / 2>          # ZOOMIES_AGENT_CAPACITY
  backend: docker               # ZOOMIES_AGENT_BACKEND  -- docker | podman | process
  docker_host: ""               # ZOOMIES_DOCKER_HOST / DOCKER_HOST -- "" autodetects
  work_dir: <state dir>/work    # ZOOMIES_WORK_DIR
  labels: {}                    # ZOOMIES_AGENT_LABELS   -- "gpu=true,zone=eu"
  network: ""                   # ZOOMIES_AGENT_NETWORK
  heartbeat_interval: 30s       # ZOOMIES_HEARTBEAT_INTERVAL -- a host is unhealthy after 90s of silence and its runners lost after 5m; above 45s is warned about
  prewarm_timeout: 5m          # ZOOMIES_AGENT_PREWARM_TIMEOUT -- background pulls only; 1s–15m
  prewarm_jitter: 30s          # ZOOMIES_AGENT_PREWARM_JITTER -- random delay before background pulls; 0s–5m
  bootstrap_cpu_grace: 2m       # ZOOMIES_AGENT_BOOTSTRAP_CPU_GRACE -- 0 applies pressure throttling immediately; maximum 10m
  finished_retention: 0s        # ZOOMIES_AGENT_FINISHED_RETENTION -- 0 removes a finished workload after its report is acknowledged
  docker_build_cache_mb: 0      # ZOOMIES_AGENT_DOCKER_BUILD_CACHE_MB -- target for unused builder cache; 0 prunes nothing
  # Process backend only:
  runner_sha256: ""             # ZOOMIES_AGENT_RUNNER_SHA256 -- digest of the runner archive, when github.runner_version is pinned
  allow_unverified_runner_download: false   # ZOOMIES_AGENT_ALLOW_UNVERIFIED_RUNNER_DOWNLOAD -- warned about
  runner_download_url: ""       # ZOOMIES_AGENT_RUNNER_DOWNLOAD_URL -- an internal mirror of the actions/runner releases
  registry_auth: ""             # ZOOMIES_REGISTRY_AUTH -- credentials for a private image registry
  extra_ca_file: ""             # ZOOMIES_AGENT_EXTRA_CA_FILE -- a PEM root CA container runners trust, for a TLS-intercepting proxy
  # Standalone agents only:
  controller_url: ""            # ZOOMIES_CONTROLLER_URL
  join_token: ""                # ZOOMIES_JOIN_TOKEN
  agent_token: ""               # ZOOMIES_AGENT_TOKEN
  ca_file: ""                   # ZOOMIES_AGENT_CA_FILE
  client_cert_file: ""          # ZOOMIES_AGENT_CLIENT_CERT_FILE
  client_key_file: ""           # ZOOMIES_AGENT_CLIENT_KEY_FILE
  insecure_skip_verify: false   # ZOOMIES_AGENT_INSECURE_SKIP_VERIFY
  allow_insecure_http: false    # ZOOMIES_AGENT_ALLOW_INSECURE_HTTP -- plain http:// off-host

runners:
  docker_wait: 3m               # ZOOMIES_DOCKER_WAIT   -- how long a Docker pool's runner waits for its daemon; 0 uses the 2m compatibility fallback
  default_cpus: 2               # ZOOMIES_RUNNER_DEFAULT_CPUS       -- what one runner gets on a pool that has not said otherwise
  default_memory_mb: 4096       # ZOOMIES_RUNNER_DEFAULT_MEMORY_MB  -- the same, in megabytes
  env: {}                       # ZOOMIES_RUNNER_ENV    -- "HTTPS_PROXY=http://proxy:3128,NO_PROXY=localhost"; a pool's env wins

scheduler:
  interval: 10s                 # ZOOMIES_SCHEDULER_INTERVAL
  scale_up_delay: 0s            # ZOOMIES_SCALE_UP_DELAY
  max_runner_lifetime: 6h       # ZOOMIES_MAX_RUNNER_LIFETIME
  provision_timeout: 20m        # ZOOMIES_PROVISION_TIMEOUT -- must outlast a cold image pull and the Docker wait above
  drain_timeout: 15m            # ZOOMIES_DRAIN_TIMEOUT
  registration_concurrency: 1 # ZOOMIES_REGISTRATION_CONCURRENCY -- per installation; 1–16
  max_creates_per_tick: 10      # ZOOMIES_MAX_CREATES_PER_TICK
  default_runner_limits: true   # ZOOMIES_DEFAULT_RUNNER_LIMITS -- a pool with no cpus or memory_mb gets one slot's share of its host; off is warned about
  host_throttling: true         # ZOOMIES_HOST_THROTTLING -- step an overwhelmed host down and lift it after calm; off is warned about
  auto_rerun: false             # ZOOMIES_AUTO_RERUN -- re-run a job whose runner died under it; on is warned about
  auto_rerun_limit: 1           # ZOOMIES_AUTO_RERUN_LIMIT -- automatic re-runs per workflow run, 1–5

capacity_demand:
  destination_url: ""           # ZOOMIES_CAPACITY_DEMAND_URL (empty disables)
  signing_secret: ""            # ZOOMIES_CAPACITY_DEMAND_SIGNING_SECRET
  cooldown: 10m                  # ZOOMIES_CAPACITY_DEMAND_COOLDOWN
  timeout: 10s                   # ZOOMIES_CAPACITY_DEMAND_TIMEOUT
  pools: []                      # ZOOMIES_CAPACITY_DEMAND_POOLS (IDs or names)

log:
  level: info                   # ZOOMIES_LOG_LEVEL   -- debug | info | warn | error
  format: json                  # ZOOMIES_LOG_FORMAT  -- json | text

oidc:
  enabled: false                # ZOOMIES_OIDC_ENABLED
  issuer: ""                    # ZOOMIES_OIDC_ISSUER
  client_id: ""                 # ZOOMIES_OIDC_CLIENT_ID
  client_secret: ""             # ZOOMIES_OIDC_CLIENT_SECRET
  redirect_url: ""              # ZOOMIES_OIDC_REDIRECT_URL (derived from external_url)
  scopes: [openid, profile, email]  # ZOOMIES_OIDC_SCOPES
  username_claim: preferred_username # ZOOMIES_OIDC_USERNAME_CLAIM
  groups_claim: groups          # ZOOMIES_OIDC_GROUPS_CLAIM
  platform_groups: []           # ZOOMIES_OIDC_PLATFORM_GROUPS
  admin_groups: []              # ZOOMIES_OIDC_ADMIN_GROUPS
  operator_groups: []           # ZOOMIES_OIDC_OPERATOR_GROUPS
  allow_signup: false           # ZOOMIES_OIDC_ALLOW_SIGNUP
  link_by_username: false       # ZOOMIES_OIDC_LINK_BY_USERNAME -- let SSO take over a password account of the same name; warned about

metrics:
  enabled: true                 # ZOOMIES_METRICS_ENABLED
  path: /metrics                # ZOOMIES_METRICS_PATH
  public: false                 # ZOOMIES_METRICS_PUBLIC

status:
  mode: off                     # ZOOMIES_STATUS_MODE  off | authenticated | public; public is warned about

retention:
  jobs: 720h                    # ZOOMIES_RETENTION_JOBS      (30 days)
  runners: 168h                 # ZOOMIES_RETENTION_RUNNERS   (7 days; the row, not the container -- see agent.finished_retention)
  scaling_events: 8760h         # ZOOMIES_RETENTION_SCALING_EVENTS (365 days of scaling history; was retention.audit, which is still read)
  samples: 168h                 # ZOOMIES_RETENTION_SAMPLES   (7 days of the per-minute fleet and per-host samples the Overview and the Hosts page draw)
  runner_sessions: 8760h        # ZOOMIES_RETENTION_RUNNER_SESSIONS (365 days of the usage ledger's runner sessions, which outlive the rows above)
  webhooks: 168h                # ZOOMIES_RETENTION_WEBHOOKS
  machines: 168h                # ZOOMIES_RETENTION_MACHINES  (7 days of deleted-machine rows -- what was rented, when, and what it cost)

limits:                         # fleet-wide ceilings; 0 is unlimited, and every one is 0 by default
  hosts: 0                      # ZOOMIES_LIMITS_HOSTS             -- enrolled hosts
  pools: 0                      # ZOOMIES_LIMITS_POOLS             -- pools
  runners: 0                    # ZOOMIES_LIMITS_RUNNERS           -- live runners across every pool
  join_tokens: 0                # ZOOMIES_LIMITS_JOIN_TOKENS       -- join tokens neither used nor expired
  event_subscribers: 0          # ZOOMIES_LIMITS_EVENT_SUBSCRIBERS -- open live-update streams (one per UI tab)

backup:
  directory: ""                 # ZOOMIES_BACKUP_DIRECTORY  -- empty: a `backups` directory beside the database
  interval: 24h                 # ZOOMIES_BACKUP_INTERVAL   -- 0 switches scheduled backups off
  keep: 7                       # ZOOMIES_BACKUP_KEEP       -- the controller's own copies; 0 keeps every one
  remotes: []                   # where each copy is sent afterwards; empty is nothing leaving the host
  # remotes:
  #   - name: offsite            # ZOOMIES_BACKUP_REMOTE_NAME
  #     endpoint: https://s3.eu-west-2.amazonaws.com   # ZOOMIES_BACKUP_REMOTE_ENDPOINT
  #     region: eu-west-2        # ZOOMIES_BACKUP_REMOTE_REGION
  #     bucket: acme-zoomies     # ZOOMIES_BACKUP_REMOTE_BUCKET
  #     prefix: prod             # ZOOMIES_BACKUP_REMOTE_PREFIX
  #     access_key_id: AKIA...   # ZOOMIES_BACKUP_REMOTE_ACCESS_KEY_ID
  #     secret_access_key: ""    # ZOOMIES_BACKUP_REMOTE_SECRET_ACCESS_KEY -- keep this in the environment
  #     passphrase: ""           # ZOOMIES_BACKUP_REMOTE_PASSPHRASE -- seals the archive before it leaves
  #     path_style: null         # ZOOMIES_BACKUP_REMOTE_PATH_STYLE -- unset chooses by endpoint
  #     keep: 30                 # ZOOMIES_BACKUP_REMOTE_KEEP -- this bucket's own retention; 0 keeps every one
  #     disabled: false          # ZOOMIES_BACKUP_REMOTE_DISABLED
  #   - name: minio              # the second remote's environment is ZOOMIES_BACKUP_REMOTE_2_*
  #     endpoint: http://minio:9000
  #     bucket: backups

images:
  refresh_interval: 1h          # ZOOMIES_IMAGE_REFRESH_INTERVAL  -- 0 switches it off

updates:
  check_interval: 24h           # ZOOMIES_UPDATE_CHECK_INTERVAL   -- 0 never asks

provider:
  enabled: false                # ZOOMIES_PROVIDER_ENABLED                -- off: renting machines spends money
  paused: false                 # ZOOMIES_PROVIDER_PAUSED                 -- the kill switch; holds creation only
  interval: 30s                 # ZOOMIES_PROVIDER_INTERVAL
  sweep_interval: 10m           # ZOOMIES_PROVIDER_SWEEP_INTERVAL         -- how often each provider is asked what it is running
  max_machines: 0               # ZOOMIES_PROVIDER_MAX_MACHINES           -- fleet-wide ceiling; 0 rents nothing, and is warned about
  max_creates_in_flight: 2      # ZOOMIES_PROVIDER_MAX_CREATES_IN_FLIGHT
  scale_up_delay: 0s            # ZOOMIES_PROVIDER_SCALE_UP_DELAY         -- a machine takes minutes; it has already waited
  call_timeout: 30s             # ZOOMIES_PROVIDER_CALL_TIMEOUT
  create_timeout: 20m           # ZOOMIES_PROVIDER_CREATE_TIMEOUT
  bootstrap_timeout: 10m        # ZOOMIES_PROVIDER_BOOTSTRAP_TIMEOUT
  enrol_timeout: 15m            # ZOOMIES_PROVIDER_ENROL_TIMEOUT
  delete_timeout: 15m           # ZOOMIES_PROVIDER_DELETE_TIMEOUT
  ambiguity_timeout: 30m        # ZOOMIES_PROVIDER_AMBIGUITY_TIMEOUT      -- then a person is asked
  idle_timeout: 15m             # ZOOMIES_PROVIDER_IDLE_TIMEOUT
  scale_down_cooldown: 15m      # ZOOMIES_PROVIDER_SCALE_DOWN_COOLDOWN    -- at least one idle_timeout, or the fleet churns
  delete_grace: 10m             # ZOOMIES_PROVIDER_DELETE_GRACE           -- after a machine's host goes silent

ui:
  capacity_map:
    overview_layout: overlay    # ZOOMIES_UI_CAPACITY_MAP_OVERVIEW_LAYOUT -- overlay | split; what the Overview's map opens with
    hosts_layout: overlay       # ZOOMIES_UI_CAPACITY_MAP_HOSTS_LAYOUT    -- the same for the Hosts page, separately
  queue_warning_threshold: 1    # ZOOMIES_UI_QUEUE_WARNING_THRESHOLD      -- jobs queued before the queue tiles turn amber
```

---

## Every key

Where each setting can live, and when a change to it takes effect. "At once"
means the running controller picks it up on its next pass; "next restart" means
the change is stored and applied when the controller starts again, which the
settings page reports rather than refusing the edit.

### `agent`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `agent.agent_token` | `ZOOMIES_AGENT_TOKEN` | on the agent's own host | Agent token — The credential a standalone agent carries afterwards. It is configured on that agent's own host. |
| `agent.allow_insecure_http` | `ZOOMIES_AGENT_ALLOW_INSECURE_HTTP` | on the agent's own host | Allow plain HTTP to the controller — Let a standalone agent use a plain http:// controller URL off loopback, which puts its token and every runner credential on the wire in the clear. It is configured on that agent's own host. |
| `agent.allow_unverified_runner_download` | `ZOOMIES_AGENT_ALLOW_UNVERIFIED_RUNNER_DOWNLOAD` | next restart | Allow unverified runner downloads — Let the process backend install a runner archive whose digest it cannot check. The alternative to checking is executing whatever the network handed over. |
| `agent.backend` | `ZOOMIES_AGENT_BACKEND` | next restart | Runner backend — What a runner runs in: a Docker container, a Podman container, or a bare process on this host. |
| `agent.ca_file` | `ZOOMIES_AGENT_CA_FILE` | on the agent's own host | Controller certificate — The certificate a standalone agent pins for its controller. It is configured on that agent's own host. |
| `agent.capacity` | `ZOOMIES_AGENT_CAPACITY` | next restart | Runners per host — How many runners this host will hold at once. It defaults to one per two cores, which leaves the machine room to breathe. |
| `agent.client_cert_file` | `ZOOMIES_AGENT_CLIENT_CERT_FILE` | on the agent's own host | Client certificate — A standalone agent's client certificate, for mutual TLS. It is configured on that agent's own host. |
| `agent.client_key_file` | `ZOOMIES_AGENT_CLIENT_KEY_FILE` | on the agent's own host | Client private key — The key for that client certificate. It is configured on that agent's own host. |
| `agent.controller_url` | `ZOOMIES_CONTROLLER_URL` | on the agent's own host | Controller URL — The controller a standalone agent connects to. It is configured on that agent's own host. |
| `agent.docker_build_cache_mb` | `ZOOMIES_AGENT_DOCKER_BUILD_CACHE_MB` | next restart | Docker build cache target — The target size for unused Docker builder cache. 0 leaves a shared or externally managed daemon alone. |
| `agent.docker_host` | `ZOOMIES_DOCKER_HOST` | next restart | Docker socket — The Docker or Podman socket. Empty finds one, preferring a rootless socket over the root one. |
| `agent.extra_ca_file` | `ZOOMIES_AGENT_EXTRA_CA_FILE` | next restart | Extra CA for runners — A PEM bundle on this host that container runners and their Docker sidecars trust as well as the image's own, for a network whose proxy re-signs TLS. |
| `agent.embedded` | `ZOOMIES_AGENT_EMBEDDED` | next restart | Run an agent in this controller — Run an agent inside this controller, so a single machine needs one process. Off makes a controller that schedules runners onto other hosts and starts none itself. |
| `agent.prewarm_timeout` | `ZOOMIES_AGENT_PREWARM_TIMEOUT` | next restart | Background image preparation budget; default 5m, range 1s–15m. Foreground create budgets are independent. |
| `agent.prewarm_jitter` | `ZOOMIES_AGENT_PREWARM_JITTER` | next restart | Random delay before background preparation; default 30s, range 0s–5m. Foreground work can pass during the delay. |
| `agent.bootstrap_cpu_grace` | `ZOOMIES_AGENT_BOOTSTRAP_CPU_GRACE` | next restart | Startup CPU grace — Keep new runners at their normal CPU quota for 2m before applying host-pressure throttling. Range 0s–10m; 0s disables the grace. Memory and CPU limits remain enforced. |
| `agent.finished_retention` | `ZOOMIES_AGENT_FINISHED_RETENTION` | next restart | Keep finished containers for — How long a finished runner's container stays on the host before the agent deletes it. It is the window for reading a finished runner's log, and it is host disk: 0 deletes on the next pass. |
| `agent.heartbeat_interval` | `ZOOMIES_HEARTBEAT_INTERVAL` | next restart | Heartbeat interval — How often an agent reports in. A host that goes quiet for 90 seconds is counted lost, so this has to be comfortably under that. |
| `agent.insecure_skip_verify` | `ZOOMIES_AGENT_INSECURE_SKIP_VERIFY` | on the agent's own host | Skip certificate verification — Let a standalone agent skip verifying its controller's certificate. It is configured on that agent's own host. |
| `agent.join_token` | `ZOOMIES_JOIN_TOKEN` | on the agent's own host | Join token — The single-use token a standalone agent redeems to enrol. It is configured on that agent's own host. |
| `agent.labels` | `ZOOMIES_AGENT_LABELS` | next restart | Host labels — Key=value labels describing this host, which a pool can require of the hosts it runs on. |
| `agent.name` | `ZOOMIES_AGENT_NAME` | next restart | Host name — What this host is called in the fleet. Empty names it after the machine it is on. |
| `agent.network` | `ZOOMIES_AGENT_NETWORK` | next restart | Container network — An existing container network to attach runners to. Empty uses the daemon's default bridge. |
| `agent.registry_auth` | `ZOOMIES_REGISTRY_AUTH` | next restart | Registry credentials — A base64 X-Registry-Auth value the container backends send when they pull. Without it a pool on a private registry cannot use pinned-only pulls at all. |
| `agent.runner_download_url` | `ZOOMIES_AGENT_RUNNER_DOWNLOAD_URL` | next restart | Runner download mirror — Where the process backend fetches runner archives from, for hosts that mirror releases internally. The path below it is the same. |
| `agent.runner_sha256` | `ZOOMIES_AGENT_RUNNER_SHA256` | next restart | Runner archive digest — The expected digest of the actions/runner archive the process backend downloads. Zoomies ships the digest for the release it pins; supply one when you pin another. |
| `agent.work_dir` | `ZOOMIES_WORK_DIR` | next restart | Working directory — Where runner working directories and the agent's own credentials live. |

### `backup`

These three are edited on **Settings → Backups**, beside the copies they
produce, rather than in the Configuration list. They are ordinary settings
otherwise: the same keys, the same environment overrides, the same export.

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `backup.directory` | `ZOOMIES_BACKUP_DIRECTORY` | at once | Backup directory — Where backups are kept. Empty is a `backups` directory beside the database, which on a container deployment is the mounted volume. A relative path is relative to the database's directory. |
| `backup.interval` | `ZOOMIES_BACKUP_INTERVAL` | at once | Scheduled backup interval — How often the controller takes a copy of its own database. 0 switches scheduled backups off; one can still be taken on demand from the Backups tab or with `zoomies backup`. |
| `backup.keep` | `ZOOMIES_BACKUP_KEEP` | at once | Backups to keep — How many of the controller's own backups are kept; the oldest beyond it go after each new one. 0 keeps every one. Backups somebody uploaded or fetched back from a remote are never counted and never deleted by this. |

#### `backup.remotes`

Where each copy goes after it is taken: a list of S3-compatible buckets, empty
by default, so nothing leaves the host until somebody says where it goes. Any
implementation of the S3 API does — AWS, MinIO, Ceph, Backblaze B2, Cloudflare
R2, Garage — and what lands there is exactly the archive the Backups tab
downloads, so a copy pulled out of a bucket in three years opens with `zoomies
restore` and nothing else. [Backup and restore](backup-and-restore.md) is the
whole story; this is the reference.

Unlike everything else on this page, these are read from `zoomies.yaml` and the
environment only. They are not settings in the database, and the settings page
does not edit them: a fleet whose database is gone has no stored settings to
read, and finding the offsite copy is exactly what that fleet needs to do. Keep
the file mode 0600, and prefer the environment for the secret key.

Destinations can also be **added on the Backups page**, where they are stored
as rows with their secrets sealed under the instance encryption key. A fleet
normally uses one or the other. A name described in both places is the file's,
and the stored row is shown as ignored rather than left to disagree in
silence — see [Backup and restore](backup-and-restore.md#copies-that-leave-the-machine).

| Key | Environment | What it is |
| --- | --- | --- |
| `name` | `ZOOMIES_BACKUP_REMOTE_NAME` | What this destination is called on the Backups tab, in the log and in the API. Lower-case letters, digits and dashes. Empty is `offsite`, then `offsite-2`. |
| `endpoint` | `ZOOMIES_BACKUP_REMOTE_ENDPOINT` | The service's URL. The scheme decides whether the connection is encrypted; there is no separate switch to disagree with it. |
| `region` | `ZOOMIES_BACKUP_REMOTE_REGION` | What requests are signed for. Empty is `us-east-1`, which implementations with no regions of their own accept. |
| `bucket` | `ZOOMIES_BACKUP_REMOTE_BUCKET` | The bucket. Zoomies never creates it: a destination that appeared by itself is one nobody has set the retention, versioning or access policy of. |
| `prefix` | `ZOOMIES_BACKUP_REMOTE_PREFIX` | The key prefix inside it, so one bucket can hold several fleets. |
| `access_key_id` | `ZOOMIES_BACKUP_REMOTE_ACCESS_KEY_ID` | The credential's id. |
| `secret_access_key` | `ZOOMIES_BACKUP_REMOTE_SECRET_ACCESS_KEY` | Its secret. It belongs in the environment, or in a file only the controller reads. |
| `passphrase` | `ZOOMIES_BACKUP_REMOTE_PASSPHRASE` | Seals the archive with argon2id and AES-256-GCM before it is uploaded. Empty uploads the plain archive and the validator says what that costs. Nothing here can recover a lost passphrase. |
| `path_style` | `ZOOMIES_BACKUP_REMOTE_PATH_STYLE` | Puts the bucket in the path rather than the hostname. Unset chooses: virtual-hosted for AWS's own endpoints, path style for everything else, which is what MinIO, Ceph and a bare address need. |
| `keep` | `ZOOMIES_BACKUP_REMOTE_KEEP` | How many copies this remote holds; the oldest beyond it go after each upload. 0 keeps every one, and is the default. |
| `disabled` | `ZOOMIES_BACKUP_REMOTE_DISABLED` | Stops uploads without removing what the bucket holds or making the configuration something you have to reconstruct. |

The unnumbered environment variables above are the first remote; the second is
`ZOOMIES_BACKUP_REMOTE_2_*` and so on to nine. A variable whose `…_NAME`
matches a remote the file already describes fills that one in wherever it sits
in the list, which is how a compose file hands over only the secret key of a
bucket `zoomies.yaml` describes.

The permissions a remote needs on its bucket and prefix are `s3:PutObject`,
`s3:GetObject`, `s3:DeleteObject` and `s3:ListBucket` — the last two only for
retention and for the Backups tab's listing, so a write-only credential works
if you set `keep: 0` and never expect the page to say what is there.

### `capacity_demand`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `capacity_demand.cooldown` | `ZOOMIES_CAPACITY_DEMAND_COOLDOWN` | at once | Cooldown — How long to wait before asking for capacity for the same pool again. |
| `capacity_demand.destination_url` | `ZOOMIES_CAPACITY_DEMAND_URL` | at once | Destination URL — Where signed requests for host capacity are posted. Empty disables the integration. |
| `capacity_demand.pools` | `ZOOMIES_CAPACITY_DEMAND_POOLS` | at once | Pools to publish for — Which pools to publish demand for. Empty publishes for all of them. |
| `capacity_demand.signing_secret` | `ZOOMIES_CAPACITY_DEMAND_SIGNING_SECRET` | at once | Signing secret — The secret those requests are signed with. Anyone holding it can forge one, so it is stored sealed. |
| `capacity_demand.timeout` | `ZOOMIES_CAPACITY_DEMAND_TIMEOUT` | at once | Request timeout — How long one of those requests may take. |

### `database`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `database.path` | `ZOOMIES_DB_PATH` | file or environment only | Database file — The SQLite file holding this fleet, including every setting below. It is named in the configuration file or the environment because nothing can read it from inside itself. |

### `github`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `github.allow_workflow_cancellation` | `ZOOMIES_ALLOW_WORKFLOW_CANCELLATION` | at once | Allow cancelling workflow runs — Let operators ask GitHub to cancel the workflow run that owns a job. Turning it off is what a read-only Actions grant wants. |
| `github.api_base_url` | `ZOOMIES_GITHUB_API_BASE_URL` | at once | GitHub API base URL — https://api.github.com for github.com, your Enterprise Server's /api/v3, or a GHE.com tenant's address. It is the default for a new installation; each existing one keeps the base it was added with. |
| `github.poll_fallback` | `ZOOMIES_POLL_FALLBACK` | next restart | Poll for queued jobs — List queued jobs on a timer as well as waiting for webhooks. On by default: a controller that silently stops scaling because a webhook was misconfigured is worse than a few extra API calls. |
| `github.toolchain_scan_interval` | `ZOOMIES_TOOLCHAIN_SCAN_INTERVAL` | at once | Toolchain scan interval — How often every workflow is read for the toolchain versions each pool's jobs install. 0, the default, scans only when asked: a scan reads every workflow file in every repository, from the GitHub quota the scheduler shares. |
| `github.poll_interval` | `ZOOMIES_POLL_INTERVAL` | at once | Poll interval — How often the fallback poller looks for queued jobs. |
| `github.runner_image` | `ZOOMIES_RUNNER_IMAGE` | at once | Default runner image — The container image a new pool runs when it names neither an image nor an operating system. |
| `github.runner_version` | `ZOOMIES_RUNNER_VERSION` | at once | Pinned runner release — Pin the actions/runner release. Empty tracks whatever the image carries. |
| `github.upload_base_url` | `ZOOMIES_GITHUB_UPLOAD_BASE_URL` | at once | GitHub upload base URL — The upload endpoint, when your Enterprise Server puts it somewhere other than beside the API. |
| `github.webhook_path` | `ZOOMIES_WEBHOOK_PATH` | next restart | Webhook path — The path GitHub posts deliveries to. Changing it means changing the App's webhook URL too. |

### `images`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `images.refresh_interval` | `ZOOMIES_IMAGE_REFRESH_INTERVAL` | at once | Image refresh interval — How often every pool's image is prewarmed again, so a moving tag reaches the hosts. 0 switches it off, which is what an air-gapped fleet wants. |

### `limits`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `limits.event_subscribers` | `ZOOMIES_LIMITS_EVENT_SUBSCRIBERS` | at once | Most live-update streams — The most live-update streams open at once. Every open tab of the UI holds one, so leave room for every operator's browser. 0 is unlimited. |
| `limits.hosts` | `ZOOMIES_LIMITS_HOSTS` | at once | Most hosts — The most hosts that may be enrolled at once; a join beyond it is refused. 0 is unlimited. A host joining again under its own name is not counted twice. |
| `limits.join_tokens` | `ZOOMIES_LIMITS_JOIN_TOKENS` | at once | Most outstanding join tokens — The most join tokens that may be outstanding at once, counting those neither used nor expired; minting one beyond it is refused. 0 is unlimited. |
| `limits.pools` | `ZOOMIES_LIMITS_POOLS` | at once | Most pools — The most pools this instance holds; creating one beyond it is refused. 0 is unlimited. |
| `limits.runners` | `ZOOMIES_LIMITS_RUNNERS` | at once | Most runners — The most live runners across every pool. At the ceiling the scheduler creates no more, and each pool it held back says so in its scaling reason. 0 is unlimited. |

Every limit is platform-scoped: it is how much of the controller's machine a
fleet may spend, so on an instance several teams use, the account that runs the
process sets it and the fleet's administrators do not. A request that would
cross one is refused with a `409` whose code is `limit_reached` and whose
message and `field` name the setting. The runner ceiling has no request to
refuse — runners are the scheduler's to create — so the scheduler stops at it
and each pool it held back says `limits.runners` in its scaling reason.

A ceiling on a controller only its own machine can reach protects nothing, and
the validator says so with `limits.loopback`.

### `log`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `log.format` | `ZOOMIES_LOG_FORMAT` | next restart | Log format — json for a log collector, text for a person reading a terminal. |
| `log.level` | `ZOOMIES_LOG_LEVEL` | at once | Log level — How much detail the controller logs. |

### `metrics`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `metrics.enabled` | `ZOOMIES_METRICS_ENABLED` | next restart | Prometheus endpoint — Serve the Prometheus endpoint. |
| `metrics.path` | `ZOOMIES_METRICS_PATH` | next restart | Metrics path — Where it is served. |
| `metrics.public` | `ZOOMIES_METRICS_PUBLIC` | next restart | Serve metrics without authentication — Serve it without authentication. Off by default, because job and repository names are visible in the label set. |

### `status`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `status.mode` | `ZOOMIES_STATUS_MODE` | at once | Fleet status page — Who may read the fleet's status at `/status`, `/status.svg` and `/api/v1/status`: nobody (`off`, the default), anyone signed in (`authenticated`), or anyone who can reach this controller (`public`). It carries no names: a state, banded counts, rounded waits and problem codes. `public` raises the `status.public` warning, and is refused with `status.public_no_tls` on a public bind without TLS. |

### `oidc`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `oidc.admin_groups` | `ZOOMIES_OIDC_ADMIN_GROUPS` | next restart | Administrator groups — Provider groups whose members get the administrator role. |
| `oidc.allow_signup` | `ZOOMIES_OIDC_ALLOW_SIGNUP` | next restart | Create accounts on first sign-in — Create an account on a first successful single sign-on, rather than refusing anyone not already here. |
| `oidc.client_id` | `ZOOMIES_OIDC_CLIENT_ID` | next restart | Client ID — The client this controller identifies itself as. |
| `oidc.client_secret` | `ZOOMIES_OIDC_CLIENT_SECRET` | next restart | Client secret — The client secret that goes with it. |
| `oidc.enabled` | `ZOOMIES_OIDC_ENABLED` | next restart | Single sign-on — Offer single sign-on as well as local accounts. |
| `oidc.groups_claim` | `ZOOMIES_OIDC_GROUPS_CLAIM` | next restart | Groups claim — The token claim listing the groups a user is in. |
| `oidc.issuer` | `ZOOMIES_OIDC_ISSUER` | next restart | Issuer URL — The identity provider's issuer URL, from which its endpoints are discovered. |
| `oidc.link_by_username` | `ZOOMIES_OIDC_LINK_BY_USERNAME` | next restart | Link sign-on to local accounts — Let a first single sign-on take over an existing local account with the same username. Turn it on for the one migration where that is the intention, then turn it off again. |
| `oidc.platform_groups` | `ZOOMIES_OIDC_PLATFORM_GROUPS` | next restart | Platform groups — Provider groups whose members get the platform role, above administrator. |
| `oidc.operator_groups` | `ZOOMIES_OIDC_OPERATOR_GROUPS` | next restart | Operator groups — Provider groups whose members get the operator role. A user in no mapped group is a viewer. |
| `oidc.redirect_url` | `ZOOMIES_OIDC_REDIRECT_URL` | next restart | Redirect URL — Where the provider sends the browser back to. Empty derives it from the external URL. |
| `oidc.scopes` | `ZOOMIES_OIDC_SCOPES` | next restart | Scopes — The scopes asked for at sign-in. |
| `oidc.username_claim` | `ZOOMIES_OIDC_USERNAME_CLAIM` | next restart | Username claim — The token claim that becomes a Zoomies username. |

### `provider`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `provider.ambiguity_timeout` | `ZOOMIES_PROVIDER_AMBIGUITY_TIMEOUT` | next restart | Unknown-outcome timeout — How long an operation whose outcome is unknown is reconciled by looking before a person is asked instead. It must outlast the creation timeout: a create that is merely slow is not an unknown outcome. |
| `provider.bootstrap_timeout` | `ZOOMIES_PROVIDER_BOOTSTRAP_TIMEOUT` | next restart | Agent install timeout — How long installing the agent inside a machine that is already up may take. |
| `provider.call_timeout` | `ZOOMIES_PROVIDER_CALL_TIMEOUT` | next restart | Provider request timeout — How long one API request to a provider may take. |
| `provider.create_timeout` | `ZOOMIES_PROVIDER_CREATE_TIMEOUT` | next restart | Machine creation timeout — How long the whole asynchronous creation of a machine may take, rather than the request that starts it. |
| `provider.delete_grace` | `ZOOMIES_PROVIDER_DELETE_GRACE` | next restart | Grace before a silent machine is lost — How long a machine whose host has gone silent is left alone before it is treated as lost. It has to outlast the controller's own judgement that a host is gone, or a network blip would destroy a machine in the middle of a job. |
| `provider.delete_timeout` | `ZOOMIES_PROVIDER_DELETE_TIMEOUT` | next restart | Deletion timeout — How long an asynchronous deletion may take. |
| `provider.enabled` | `ZOOMIES_PROVIDER_ENABLED` | next restart | Rent machines — Whether the machine loop runs at all. Off by default: renting a machine spends money, and nothing here should start doing that because a release added the ability to. |
| `provider.enrol_timeout` | `ZOOMIES_PROVIDER_ENROL_TIMEOUT` | next restart | Enrolment timeout — How long a bootstrapped machine has to appear as a host. It has to outlast a heartbeat timeout, or a machine that joined and went briefly quiet would be given up on. |
| `provider.idle_timeout` | `ZOOMIES_PROVIDER_IDLE_TIMEOUT` | at once | Idle before draining — How long a machine's host must have had no runner on it before the machine is drained. |
| `provider.interval` | `ZOOMIES_PROVIDER_INTERVAL` | next restart | Machine loop interval — How often the machine loop runs. It is slower than the scheduler's on purpose: a clone takes minutes, and the pass that watches one gains nothing from a ten-second tick. |
| `provider.max_creates_in_flight` | `ZOOMIES_PROVIDER_MAX_CREATES_IN_FLIGHT` | at once | Machines built at once — How many machines may be being built at once across the fleet, so a burst of queued jobs cannot ask a hypervisor for fifty clones in one pass. |
| `provider.max_machines` | `ZOOMIES_PROVIDER_MAX_MACHINES` | at once | Machines the fleet may rent — The ceiling across every provider. Zero rents nothing, exactly as a pool's max_runners of zero runs nothing: a maximum of none is none. |
| `provider.paused` | `ZOOMIES_PROVIDER_PAUSED` | at once | Pause new machines — Stop creating machines while leaving draining, deleting, recovering and verifying ownership running. A switch that stopped those too would strand running machines nobody is watching. |
| `provider.scale_down_cooldown` | `ZOOMIES_PROVIDER_SCALE_DOWN_COOLDOWN` | next restart | Cooldown before deleting — How long that idleness must hold continuously before anything is deleted, so a quiet minute between two bursts does not destroy the machines the second burst is about to want. |
| `provider.scale_up_delay` | `ZOOMIES_PROVIDER_SCALE_UP_DELAY` | next restart | Delay before renting — How long a pool's demand must stand before a machine is bought for it. Zero, unlike the scheduler's: a machine that takes four minutes to arrive has already spent the delay by being slow. |
| `provider.sweep_interval` | `ZOOMIES_PROVIDER_SWEEP_INTERVAL` | next restart | Ownership sweep interval — How often each provider is asked for everything it believes it is running, which is how an orphaned machine and one that vanished underneath us are both found. |

### `retention`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `retention.jobs` | `ZOOMIES_RETENTION_JOBS` | at once | Keep job history for — How long job history is kept. |
| `retention.machines` | `ZOOMIES_RETENTION_MACHINES` | at once | Keep deleted machines for — How long a deleted machine's row is kept, so what the fleet rented and gave back is still answerable after the machine itself is gone. |
| `retention.runner_sessions` | `ZOOMIES_RETENTION_RUNNER_SESSIONS` | at once | Keep runner sessions for — How long the usage ledger keeps each finished runner's session — who it ran for, where, and for how long — after the runner's own row has been pruned. |
| `retention.runners` | `ZOOMIES_RETENTION_RUNNERS` | at once | Keep finished runners for — How long finished runners are kept. |
| `retention.samples` | `ZOOMIES_RETENTION_SAMPLES` | at once | Keep Overview samples for — How long the Overview's samples are kept. |
| `retention.scaling_events` | `ZOOMIES_RETENTION_SCALING_EVENTS` | at once | Keep scaling history for — How long scaling decisions are kept. Audit rows are not covered by this, or by anything: they are never deleted. |
| `retention.webhooks` | `ZOOMIES_RETENTION_WEBHOOKS` | at once | Keep webhook deliveries for — How long webhook deliveries are kept. |

### `runners`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `runners.default_cpus` | `ZOOMIES_RUNNER_DEFAULT_CPUS` | at once | Standard CPUs per runner — Where a pool's CPU slider opens when somebody chooses to set a fixed size, in cores; fractions are allowed. It is not what a pool with no size becomes: such a pool is given one slot's share of whichever host each runner lands on. 0 means nothing has been said and the built-in 2 cores answers. |
| `runners.default_memory_mb` | `ZOOMIES_RUNNER_DEFAULT_MEMORY_MB` | at once | Standard memory per runner — How much memory one runner gets on a pool that has not said otherwise, in megabytes. It is the figure a new pool opens on, and the one a host's recommended capacity is worked out from. 0 means nothing has been said and the built-in 4096 answers. |
| `runners.minimum_cpus` | `ZOOMIES_RUNNER_MINIMUM_CPUS` | at once | Minimum CPUs per runner — The fleet's minimum CPU per runner, in cores: the least a runner may be given when no host has room for its standard size — a fixed pool's figures, or an automatic pool's whole slot share — so a host a little short still runs the job. Every pool that sets no minimum of its own follows it, live; a pool's own minimum wins. 0 is none. |
| `runners.minimum_memory_mb` | `ZOOMIES_RUNNER_MINIMUM_MEMORY_MB` | at once | Minimum memory per runner — The fleet's minimum memory per runner, in megabytes: the least a runner may be given when no host has room for its standard size, fixed or automatic. Every pool that sets no minimum of its own follows it, live; a pool's own minimum wins. 0 is none; anything set is held to 512. |
| `runners.docker_wait` | `ZOOMIES_DOCKER_WAIT` | at once | Docker daemon wait — How long DinD provisioning waits for a healthy daemon, and a Docker runner waits before registering. Default 3m. Whole seconds, up to an hour; 0 leaves the runner image's own default. A pool's env can set ZOOMIES_DOCKER_WAIT to override it for that pool. |
| `runners.env` | `ZOOMIES_RUNNER_ENV` | at once | Runner environment — Key=value variables every runner starts with, such as a proxy or a package mirror. A pool's own env wins where the two name the same variable. Every job can read these, so a credential does not belong here: give it to the pool, or to the workflow as a GitHub secret. |

### `scheduler`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `scheduler.auto_rerun` | `ZOOMIES_AUTO_RERUN` | at once | Re-run jobs the fleet broke — Ask GitHub to run a job again when this fleet is what broke it, never a test that failed. Off by default: it spends GitHub minutes without asking. |
| `scheduler.auto_rerun_limit` | `ZOOMIES_AUTO_RERUN_LIMIT` | at once | Automatic re-runs per workflow run — How many times one run may be re-run automatically (1–5), counted from GitHub's own run attempt. |
| `scheduler.default_runner_limits` | `ZOOMIES_DEFAULT_RUNNER_LIMITS` | at once | Default runner limits — Give a runner whose pool sets no CPU or memory limit one slot's share of its host as a real limit. Off, a host's worth of them can each take every core. |
| `scheduler.drain_timeout` | `ZOOMIES_DRAIN_TIMEOUT` | at once | Drain timeout — Fail a runner that has been draining this long with no job left on it. A runner still finishing a job is never touched by it. |
| `scheduler.host_throttling` | `ZOOMIES_HOST_THROTTLING` | at once | Throttle hosts under pressure — Let the controller throttle a host its measurements say is overwhelmed, and step it back up after a stretch of calm. |
| `scheduler.placement_mode` | `ZOOMIES_PLACEMENT_MODE` | at once | Host placement — `headroom` (default), `shadow` to compare startup-history placement, or `readiness` to enable it. See [rollout and evidence](development/stability-performance.md#placement-rollout). |
| `scheduler.interval` | `ZOOMIES_SCHEDULER_INTERVAL` | at once | Scheduler interval — How often the scheduler runs a pass even with nothing to react to. |
| `scheduler.registration_concurrency` | `ZOOMIES_REGISTRATION_CONCURRENCY` | at once | Bound concurrent credential requests per installation; default 1, range 1–16. Excess demand waits for a later scheduling pass. Existing calls finish when lowered. |
| `scheduler.max_creates_per_tick` | `ZOOMIES_MAX_CREATES_PER_TICK` | at once | Runners created per pass — How many runners may be created in one pass, so a thundering herd of queued jobs cannot exhaust a host in one go. |
| `scheduler.max_runner_lifetime` | `ZOOMIES_MAX_RUNNER_LIFETIME` | at once | Maximum runner lifetime — Drain a runner that has lived this long, next time it is not busy. It bounds how long a runner's credentials live; it never ends a job. |
| `scheduler.provision_timeout` | `ZOOMIES_PROVISION_TIMEOUT` | at once | Provision timeout — Fail a runner that never finishes registering, so a bad image does not hold a host slot for ever. |
| `scheduler.scale_up_delay` | `ZOOMIES_SCALE_UP_DELAY` | at once | Scale-up delay — How long a job must have been queued before it counts as demand. It damps churn when jobs arrive in bursts; 0 reacts at once. |

### `security`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `security.cookie_secure` | `ZOOMIES_COOKIE_SECURE` | next restart | Secure session cookies — Force the Secure attribute on session cookies. Unset derives it from the external URL and the TLS mode, which is right unless a proxy in front makes it wrong. |
| `security.allow_private_egress` | `ZOOMIES_ALLOW_PRIVATE_EGRESS` | live | Allow private outbound addresses — Let the URLs this controller dials — the OIDC issuer, the GitHub API, the capacity-demand destination, the runner download mirror, a backup remote and a provider — name this machine, a link-local address or a private network. Off, writing one through the API is refused and one in the file or environment is warned about, as `egress.private_target`. Turn it on when one of them really lives on a network you own. |
| `security.disable_auth` | `ZOOMIES_DISABLE_AUTH` | next restart | Disable authentication — Remove all authentication. It exists for local development, and it is refused wherever this controller looks reachable. |
| `security.docker_in_docker_expected` | `ZOOMIES_DOCKER_IN_DOCKER_EXPECTED` | live | Docker-in-Docker is expected here — Stop a pool that gives its jobs their own Docker daemon being listed as a dangerous setting. The daemon still runs in a privileged container; this is a fleet saying it knows, so that the settings still worth a second look are not buried under one it has already decided. The host socket and persistent runners keep warning. |
| `security.encryption_key` | `ZOOMIES_ENCRYPTION_KEY` | file or environment only | Encryption key — The 32-byte key, base64 or hex, that seals GitHub App private keys, webhook secrets and the stored credentials below. Prefer the key file or the environment variable: a key written into zoomies.yaml is a key in your configuration management system. |
| `security.encryption_key_file` | `ZOOMIES_ENCRYPTION_KEY_FILE` | file or environment only | Encryption key file — Where that key is read from, and written to on a first run. Back it up beside the database: without it the sealed rows cannot be read. |
| `security.rate_limit_logins` | `ZOOMIES_RATE_LIMIT_LOGINS` | next restart | Login attempts per minute — Password attempts allowed per source address per minute, and five times that per account. |
| `security.session_ttl` | `ZOOMIES_SESSION_TTL` | next restart | Session lifetime — How long a browser login lasts before it has to be made again. |

### `server`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `server.allow_indexing` | `ZOOMIES_ALLOW_INDEXING` | next restart | Allow search engine indexing — Invite search engines into the UI. Off by default: a controller is somebody's infrastructure rather than somebody's website. |
| `server.allowed_origins` | `ZOOMIES_ALLOWED_ORIGINS` | next restart | Allowed browser origins — Extra browser origins allowed to make state-changing requests. Empty means same-origin only, which is what the built-in UI needs. |
| `server.bind` | `ZOOMIES_BIND` | next restart | Listen address — The address the controller listens on. 127.0.0.1:8080 is this machine only; 0.0.0.0:8080 is every interface. |
| `server.external_url` | `ZOOMIES_EXTERNAL_URL` | next restart | External URL — How GitHub and browsers reach this controller. It forms the webhook URL, so webhooks need it. |
| `server.idle_timeout` | `ZOOMIES_IDLE_TIMEOUT` | next restart | Idle timeout — How long an idle keep-alive connection is held open. |
| `server.read_timeout` | `ZOOMIES_READ_TIMEOUT` | next restart | Read timeout — How long a client may take to send its request. |
| `server.tailcat_enabled` | `ZOOMIES_TAILCAT_ENABLED` | next restart | Private agent network — Permit private agent connections, started on first enrolment. |
| `server.tls.cert_file` | `ZOOMIES_TLS_CERT_FILE` | next restart | Certificate file — The certificate the listener serves, when the mode is files. |
| `server.tls.hosts` | `ZOOMIES_TLS_HOSTS` | next restart | Certificate host names — The names baked into a generated self-signed certificate. |
| `server.tls.key_file` | `ZOOMIES_TLS_KEY_FILE` | next restart | Private key file — The private key for that certificate. The file stays on disk; only its path is stored here. |
| `server.tls.mode` | `ZOOMIES_TLS_MODE` | next restart | TLS mode — How the listener terminates TLS: off behind a reverse proxy, self-signed for a generated certificate, files for one of your own. |
| `server.trusted_proxies` | `ZOOMIES_TRUSTED_PROXIES` | next restart | Trusted proxies — CIDRs whose X-Forwarded-For header is believed, or the word cloudflare for Cloudflare's published ranges. Empty takes client addresses from the socket, which is the safe answer. |
| `server.write_timeout` | `ZOOMIES_WRITE_TIMEOUT` | next restart | Write timeout — How long a response may take. It is 0, and should stay 0: the event stream and a followed log are responses that never end. |

### `ui`

What the web UI opens with. The capacity map layouts are each a starting
point: the page itself lets an operator pick the other answer, and remembers
the pick in that browser, so what is set here is what somebody who has never
chosen sees. The two pages that carry it are set separately, because they are
opened for different reasons — the Overview to glance at a fleet, the Hosts
page to look into a machine — and a fleet may want each to open on its own
answer. The queue warning threshold is not a per-page starting point: there is
nothing to move away from on the page, so what is set here is what every
operator's browser sees.

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `ui.capacity_map.hosts_layout` | `ZOOMIES_UI_CAPACITY_MAP_HOSTS_LAYOUT` | at once | Hosts capacity map layout — The same choice for the map on the Hosts page, which can open differently from the Overview's: split suits the page a machine is looked into on, overlay the page a fleet is glanced at. |
| `ui.capacity_map.overview_layout` | `ZOOMIES_UI_CAPACITY_MAP_OVERVIEW_LAYOUT` | at once | Overview capacity map layout — How the host capacity map on the Overview opens: overlay draws every host on one chart, split draws a chart for each. An operator who picks the other one on the page keeps their pick in that browser. |
| `ui.queue_warning_threshold` | `ZOOMIES_UI_QUEUE_WARNING_THRESHOLD` | at once | Queue depth warning threshold — How many jobs must be queued before the queue tiles on the Overview, Jobs and Pools pages turn to their warning colour. 1, the default, warns as soon as anything is waiting; a fleet whose queue is normally busy can raise it so the colour still means something. |

### `updates`

| Key | Environment | Takes effect | What it is |
| --- | --- | --- | --- |
| `updates.check_interval` | `ZOOMIES_UPDATE_CHECK_INTERVAL` | at once | Update check interval — How often github.com is asked which release of Zoomies is current. 0 never asks, and is the one request that is not about your fleet. Nothing is ever downloaded by it. |

## The settings that matter most

### `server.external_url`

The address GitHub and your browser use. Webhooks are delivered to
`<external_url><github.webhook_path>`, and the OIDC redirect URL is derived from
it.

Without it, Zoomies cannot tell GitHub where to deliver webhooks, so scaling
falls back entirely to polling and reacts in tens of seconds rather than
instantly. It warns about this at startup.

### `server.tailcat_enabled`

Default `true`; environment override `ZOOMIES_TAILCAT_ENABLED`.
Permits built-in private agent connections through Tailcat. The controller
starts its userspace listener on first private enrolment, then resumes it on
subsequent starts. Requires authentication and the instance encryption key.
Set false and restart to stop private connections without deleting their saved
identity. See [Private hosts](private-hosts.md).

### `server.bind`

Defaults to loopback. If you change it to `0.0.0.0` and leave TLS off, you get a
warning — which is correct behaviour if a reverse proxy terminates TLS, and a
problem otherwise. When you do run behind a proxy, also set
`server.trusted_proxies` so audit entries and login rate limiting see the real
client address instead of your proxy's.

It governs `X-Forwarded-Proto` as well as `X-Forwarded-For`. Without it the
controller reads every request as plain `http`, whatever the header says,
because nothing distinguishes your proxy from anyone else who can reach the
listener — so it will not send `Strict-Transport-Security`, and an `https://`
`Origin` is same-origin only by way of `server.external_url`. Set both and
neither applies.

### The setup token

While the instance has no accounts, the controller prints a **setup token** at
startup — on the banner and on a log line beginning `setup token` — and the
first-run page asks for it. It is what stops a controller you have just deployed
from being claimed by whoever loads the page first; an empty database is not, by
itself, proof that you are its owner.

```sh
docker compose logs zoomies | grep 'setup token'
```

It is minted per process and held only in memory, so restarting the controller
prints a new one, and the line stops appearing for good once an account exists.
`zoomies init` and the installer create the first account on the console
and never need it. The token is for a person at a browser; an unattended
deployment should not scrape it out of the log, and uses the variables below.

### The first account, from the environment

A compose file or a Terraform module has nobody watching, so there is nobody to
read the setup token. These three variables create the first account instead,
once, at startup, on a database with no accounts. They are read from the
environment only: they are not keys in `zoomies.yaml` and not settings in the
database, because they are an instruction to an empty database rather than
something the controller keeps doing.

| Environment | What it is |
| --- | --- |
| `ZOOMIES_BOOTSTRAP_ADMIN` | The username of the first account. It holds the `platform` role, as the one the first-run page makes does. |
| `ZOOMIES_BOOTSTRAP_PASSWORD_FILE` | A file holding that account's password, at least 12 characters. A trailing newline is ignored. |
| `ZOOMIES_BOOTSTRAP_TOKEN_FILE` | A file holding an API token you generated — at least 32 characters, no whitespace, for example `openssl rand -hex 32`. The controller registers it as a `platform` API token named `bootstrap` for the account, and prints nothing: the secret is the one you already hold. The account has no password, so it is automation's identity and cannot sign in to the UI; create people their own accounts with it. |

Set the username and exactly one of the two files; anything else is
`bootstrap.incomplete` and the controller does not start. Each file must be
mode 0600 or tighter — a file group or other can read is refused, as the
encryption key file is. Docker's `secrets:` mounts files 0444 unless the long
syntax sets `mode: 0400`.

The account is recorded in the audit log as `auth.bootstrap` with the system as
the actor and the method it came by; the first-run page, the installer and an
answer file's `admin` keys write the same row with their own method. Once any
account exists the variables are ignored, and `bootstrap.ignored` names them
until they are removed. `/readyz` reports `bootstrap_required`, so a provisioner
waits on one endpoint for the instance to be usable.

### `server.allow_indexing`

Off, and it should usually stay off. A controller serves `/robots.txt` and
`/sitemap.xml` like any other web address, and by default the first of them
declines crawling altogether: this is your infrastructure, and appearing in a
search result is a way of being found that nobody asked for. The page itself
carries `noindex, nofollow` to say the same thing to a crawler that arrived from
a link without reading `robots.txt`.

Turn it on and `robots.txt` invites crawlers to the UI's own pages — never
`/api/`, `/metrics` or the webhook path — advertises the sitemap, and the page
switches to `index, follow`. Zoomies warns at startup when it is on, because the
sign-in page and this controller's address then become public knowledge.

`/sitemap.xml` is served either way. It lists the interface's top-level pages
and nothing about your fleet: no pool, runner or job has an entry, because those
addresses are gone by tomorrow.

### `security.encryption_key`

32 bytes, base64 or hex. Generate one with `openssl rand -base64 32`.

Supply it through `ZOOMIES_ENCRYPTION_KEY` or a `0600` key file. Putting it in
`zoomies.yaml` produces a warning, because anything that can read your config —
backups, configuration management, a support bundle — can then decrypt every
stored secret.

Zoomies refuses to read a key file that is group- or world-readable.

**Back it up, separately from the database.** Losing it means re-entering the
GitHub App private key and webhook secret.

### `github.api_base_url` — GitHub Enterprise Server

```yaml
github:
  api_base_url: https://ghes.example.com/api/v3
```

A bare hostname is accepted and `/api/v3` appended. Everything else — App auth,
JIT configs, webhooks, runner groups — works the same.

GitHub Enterprise Cloud with data residency (GHE.com) is laid out like
github.com rather than like Enterprise Server: the API is the root of its own
`api.` host, with no `/api/v3`. Give the tenant's address in any of its forms —
`octocorp.ghe.com`, `https://octocorp.ghe.com` or
`https://api.octocorp.ghe.com` — and Zoomies uses
`https://api.octocorp.ghe.com/` for the API and `https://octocorp.ghe.com` for
runner registration and the links it shows.

```yaml
github:
  api_base_url: octocorp.ghe.com
```

### `github.toolchain_scan_interval` — what each pool's jobs install

```yaml
github:
  toolchain_scan_interval: 24h   # 0, the default, scans only when asked
```

A toolchain scan reads every workflow every installation can see and records,
per pool, which `setup-python`, `setup-node`, `setup-go`, `setup-java` and
`setup-dotnet` versions its jobs ask for, how many jobs ask, and in which
repositories. Each job is counted against the pool the scheduler would give it
on its labels, so the answer is about the pool that really runs it. It is what
tells you what to bake into a pool's image or keep in its
[cache](#the-pool-cache), rather than finding out one download at a time.

Read the result with `GET /api/v1/toolchains`, and start a scan by hand with
`POST /api/v1/toolchains/scan`. A version the workflow does not state is listed
with the reason: `go-version-file: go.mod` reads a file this does not fetch, and
`${{ inputs.python }}` is only known when the run starts. A matrix is expanded
the way GitHub expands it, so `python-version: ${{ matrix.python }}` over
`["3.12", "3.13"]` is two versions. Jobs on GitHub-hosted labels, or labels no
pool carries, are listed as unmatched rather than guessed onto a pool.

It needs the App's **Contents: read** permission, which the migration wizard
already asks for. GitHub answers an App without it exactly as it answers a
repository with no workflows, so the scan asks first, and says so, rather than
reporting an organisation that needs nothing.

It is off by default because of what it costs. A scan lists every repository
and reads every workflow file in each: on an organisation with hundreds of
repositories that is thousands of API calls, from the same hourly quota the
scheduler uses to find queued jobs. It reads four repositories at a time, and
skips an installation GitHub is already rate-limiting, but it does not ration
itself beyond that: a daily scan, or one by hand after workflows change, is
plenty, because workflows do not change by the hour.
The result is kept in memory, not in the database: it is a reading the next
scan replaces, and a restart loses nothing a scan cannot rebuild.

A finished scan also [fills the tool cache](#filling-the-tool-cache) of every
pool that keeps one, with the versions it found that pool's jobs asking for.

### `github.allow_workflow_cancellation` — cancel runs from Zoomies

```yaml
github:
  allow_workflow_cancellation: true
```

On by default. Operators can cancel the GitHub workflow run
containing a queued or running job from its drawer. GitHub only provides a
run-level API, so this stops every job in that run. The confirmation also
offers force cancellation for a run that remains stuck after an ordinary
request. As soon as GitHub accepts the request, Zoomies pauses every locally
queued job in the run and removes runners executing its jobs; it does not wait
for the current step to finish. GitHub's completion events still supply the
authoritative job conclusions.

This feature requires the GitHub App's **Actions: read and write** permission.
New Apps created while the setting is enabled request it automatically. For an
existing App, change the permission in GitHub and accept the permission update
on every installation before using the action. Set the option to `false` if you
prefer to keep Actions read-only and hide cancellation from Zoomies.

### `github.runner_image` — the fallback runner image

```yaml
github:
  runner_image: ghcr.io/eyupio/zoomies-runner:latest
```

This is the last of three answers, not the first. A pool that names an image
gets that image; a pool that names a **platform** gets the variant its platform
selects; only a pool that names neither falls back to this setting. Whichever it
lands on, a pool whose `docker_mode` gives jobs a daemon then gets that image's
Docker variant. See [Naming and platforms](naming.md) for the catalogue and how
a pool picks from it.

Five images are published to GHCR:

| Image | What it is |
| --- | --- |
| `ghcr.io/eyupio/zoomies` | the controller |
| `ghcr.io/eyupio/zoomies-agent` | an agent, for a host that runs one in a container |
| `ghcr.io/eyupio/zoomies-runner` | the runner a pool starts |
| `ghcr.io/eyupio/zoomies-runner-docker` | the same, plus a Docker CLI — a pool is switched to it when its `docker_mode` gives jobs a daemon, see [Jobs that build container images](#jobs-that-build-container-images) |
| `ghcr.io/eyupio/zoomies-runner-full` | `zoomies-runner-docker` plus the language toolchains the `setup-*` actions would otherwise download, for the Ubuntu variants — a pool opts in by naming it, see [The full image](#the-full-image) |

They share their tag names and release/development channels.

**The controller and the agent** follow releases:

| Tag | Points at | Published by |
| --- | --- | --- |
| `latest` | The newest full release | `release.yml`, when a release is published |
| `vX.Y.Z` | One published release | `release.yml`, when a release is published |
| `dev` | The tip of `main` | `ci.yml`, on every push to `main` |
| `main` | The tip of `main` | `ci.yml`, on every push to `main` |
| `sha-<commit>` | One exact commit | `ci.yml`, on every push to `main` |

An operator pulling `latest` gets a release, which is what it reads as. It used
to mean the tip of `main`, and both workflows wrote it, so whichever ran last
won — and a controller pulled from it could be an unreleased build stamped
`main-sha-abc1234`. That is worse than a stale tag: an agent is installed from a
release asset and no release carries a `main-` version, so such a controller
cannot match any agent it enrols, and shows every host as a different build for
as long as it runs. Run `dev` when you want `main`; it says so.

A **prerelease** — a tag with a hyphen in it, `v0.1-alpha`, `v1.0-rc1` — is
published under its own tag and does not move `latest`. Name it to run it.

**The runner images** use the same explicit channels:

| Tag | Points at | Published by |
| --- | --- | --- |
| `latest` | The newest full release | `release.yml`, when a release is published |
| `dev` | The tip of `main` | `ci.yml`, on every push to `main` |
| `main` | The tip of `main` | `ci.yml`, on every push to `main` |
| `sha-<commit>` | One exact commit | `ci.yml`, on every push to `main` |
| `vX.Y.Z` | One tagged release | `release.yml`, on a `v*` tag |

With no image override, development controller images automatically select
`:dev` runners; platform pools select e.g. `:debian-12-dev`. Release controller
images select their own release tag, e.g. `:v1.2.3` or `:debian-12-v1.2.3`,
including when pulled via `:latest`. Docker-enabled pools keep the same tag
when selecting `zoomies-runner-docker`.

The `dev`, `main` and SHA controller aliases share one image and therefore
share the `dev` runner default: a container cannot discover which alias was
used to pull it. For an immutable development runner, explicitly select
`sha-<commit>` (or `<platform>-sha-<commit>`) in the pool image field.
Native builds retain the `latest` default. Local container builds default to
`dev`; set the Docker build argument `RUNNER_IMAGE_TAG` for a different tag.

Explicit pool images, including custom repositories, tags and digests, remain
overrides. `github.runner_image` / `ZOOMIES_RUNNER_IMAGE` still overrides the
fallback for pools without a platform. Leave these unset to follow the build.
Existing saved `:latest` values are treated as explicit overrides; clear the
pool image and remove any fallback setting to restore automatic selection.

Both runner images are also published with one tag per operating system —
`ubuntu-2404`, `ubuntu-2604`, `ubuntu-2204`, `debian-12`, `debian-13`,
`fedora-42`, `rocky-9`, each built for amd64 and arm64 — plus `<os>-<version>-dev`, `<os>-<version>-main` and
`<os>-<version>-<tag>` for
pinning one operating system without pinning the controller. `latest` is the
`ubuntu-2404` variant. Set this key to a specific variant to change what an
unspecified pool boots fleet-wide.

Every push to `main` rebuilds the runner variants so their `dev` labels and
build stamp identify the same commit as the controller, agent image and binary.
Every variant and both targets come out of the same Dockerfile, so they are
never out of step with each other.

### What is in the runner image

Both runner images carry the actions/runner release and its .NET dependencies,
and the toolchain a build typically reaches for without saying so:

| | |
| --- | --- |
| Compilers and build tools | `build-essential` (gcc, g++, make), `cmake`, `pkg-config`, `autoconf`, `automake`, `libtool`, `patch`, `gettext` |
| Headers native extensions link against | OpenSSL, zlib, libffi, libyaml, libxml2, libxslt, libcurl, SQLite, readline, bzip2, lzma, ncurses, uuid |
| Interpreters | `python3` with `pip` and `venv`, `nodejs` with `npm` |
| Source and transfer | `git`, `git-lfs`, `curl`, `wget`, `rsync`, `openssh-client`, `gnupg` |
| Archives | `tar`, `gzip`, `xz-utils`, `bzip2`, `zstd`, `zip`, `unzip` |
| GitHub | the `gh` CLI |
| Diagnostics | `jq`, `file`, `netcat-openbsd`, `dnsutils`, `iputils-ping`, `net-tools`, `lsb-release` |

`python3` and `nodejs` are a floor, not a choice about versions: `setup-python`
and `setup-node` still install what a workflow asks for and take precedence on
`PATH`. They are here so that a step which uses either **without** a setup
action first does not fail.

Deliberately absent: a JDK, and the other language runtimes with a good
`setup-*` action. Those install the version the workflow asked for, where this
image could only guess — at a cost of hundreds of megabytes of probably-wrong
version.

A large image is no longer cold-start time. A pool's image is prewarmed on
every host that can run it and refreshed on a timer, so the pull happens in the
background and a job finds the image already there. What a bigger image costs
is disk on the host.

If you want something leaner, point the pool at an image of your own; Zoomies
only requires that it can run the entrypoint contract described in
`deploy/runner-entrypoint.sh`.

#### The tool cache

`setup-python`, `setup-node`, `setup-go` and the other setup actions unpack
what they download into the runner's tool cache. Both images set
`AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache` — the path GitHub's own runners
use — and give that directory to the runner account. Left to its default, the
cache would sit under `_work/_tool` and go with the runner at the end of every
job.

That makes the tool cache something you can fill ahead of time. An image built
`FROM` one of ours that installs the versions your workflows ask for into
`/opt/hostedtoolcache` turns each of those setup steps into a lookup rather
than a download; the pool's image prewarm puts that image on the host before
any job needs it. To keep one tool cache between a pool's runners instead, see
[keeping a tool cache](#keeping-a-tool-cache).

#### The full image

`ghcr.io/eyupio/zoomies-runner-full` is that image built for you: the Docker
variant with the toolchains most builds reach for already in place, so the
first step of a job is its own work rather than a download.

| Toolchain | Versions | Where a job finds it |
| --- | --- | --- |
| Python | 3.10 to 3.14 | the tool cache, for `setup-python` |
| Node.js | 22 and 24 | the tool cache, for `setup-node` |
| Go | the two supported releases | the tool cache, for `setup-go` |
| Java (Eclipse Temurin) | 17, 21 and 25; 17 is `JAVA_HOME` | the tool cache, for `setup-java` |
| .NET SDK | 8 and 10 | `/usr/share/dotnet`, where `setup-dotnet` installs |
| Maven and Gradle | the current release of each | `mvn` and `gradle` on `PATH` |
| Rust | stable, with `rustfmt` and `clippy` | `rustup`, `cargo` and `rustc` on `PATH` |

Each is laid out exactly as its setup action leaves what it downloads, so a
step asking for a version the image carries is answered from disk. Asking for
one it does not carry still works; that step downloads as it would anywhere
else. A version range such as `3.12` or `22` is matched against what the tool
cache holds, and the newest patch there wins. `setup-dotnet` still asks the
network which patch is current before it finds the SDK already installed, so
.NET saves the download, not the round trip.

It is published for the Ubuntu variants — 24.04, 26.04 and 22.04 — on both
architectures, under the same tags as `zoomies-runner`. The Python builds
`setup-python` installs are made for Ubuntu and nothing else, and an image
without them would be missing the toolchain most jobs ask for first. Point a
pool at it by name:

```yaml
image: ghcr.io/eyupio/zoomies-runner-full:ubuntu-2404
```

The versions are the newest release of each line when the image was built,
pinned by digest in `deploy/toolcache.lock`, and a weekly workflow moves them
forward. Each archive is checked against that digest before it is unpacked.
Most publishers state the digest; `actions/python-versions` does not, so a
Python archive's digest is the one recorded when it was first pinned, and an
archive that changes after that fails the build.

It is several gigabytes larger than `zoomies-runner`. That is disk on each host
that runs the pool, not queue time: the image is prewarmed on those hosts before
a job needs it, like any other pool image.

### `updates.check_interval` — knowing the controller is behind

```yaml
updates:
  check_interval: 24h
```

Once a day, the controller asks github.com which release of Zoomies is current.
If that is not the release this controller was built from, the Overview's
problems panel says so, at **info** severity, naming both versions and linking
the release notes.

It only ever tells you. **Nothing downloads and nothing restarts** — the
controller does not update itself, and it never will: a controller that
restarted itself would drop in-flight webhook deliveries and every agent's
long poll, and would only work for some of the ways `zoomies init` can install
it. The upgrade stays a decision you make.

The notice appears **only on a controller built from a release tag**. One built
from `main` — which is what the `:dev` and `:main` images are — is normally
*ahead* of the newest release, so telling it that a release is available would
be telling it to downgrade. It says nothing rather than something wrong.

This is the one request Zoomies makes to github.com that is not about your
fleet, and it goes there whatever `github.api_base_url` is set to, because the
releases of this software live on github.com whichever GitHub your runners talk
to. An air-gapped deployment, or a GitHub Enterprise Server one with no route
out, wants `check_interval: 0` — which switches off the check and the notice
together.

### Deployment models

`zoomies init` can run Zoomies three ways, and offers only the ones your host can
actually do:

| | What it writes | When to pick it |
| --- | --- | --- |
| `native` | a hardened systemd unit (or a launchd plist) | leanest, starts fastest, and needs no container runtime for the controller itself |
| `compose` | `docker-compose.yml` and a populated `.env` | easiest to upgrade and to move to another host |
| `docker` | an env file and one `docker run` | fewest files, but you manage the run command |

Pass `--deployment native|compose|docker` to skip the question. Compose is the
default when a compose command is present, native otherwise.

There is a fourth way in, for hosts you do not install on at all: a platform
that builds with Nixpacks reads `nixpacks.toml` from the repository root and
deploys the controller from the source. It has no Docker socket, so it runs no
runners itself and agents join it from elsewhere -- [Deploying on a
PaaS](paas.md) is the whole of it.

A controller's settings go into its database, not the generated `.env`:
before the container first starts, the installer stores the external URL, bind
address, TLS mode, trusted proxies, backend, capacity, work directory and log
settings through `zoomies config import-env` in a one-off container of the same
image, so the Settings page can change every one of them. The `.env` holds only
what opens the database — a freshly generated encryption key, the database and
state paths — and what Compose or `docker run` reads itself: the image tag, the
published port and the host's real docker group id. An agent, which has no
database, keeps its settings in its `.env`. Every variable carries a comment.
The file is `0600`, written atomically, and a re-run **reuses the existing
encryption key** rather than minting a new one — which would render every
stored secret undecryptable. A deployment an older release wrote them into
`.env` for has them moved by [`zoomies upgrade`](upgrading.md#settings-that-were-in-env).

`zoomies uninstall` reads back which deployment was used and tears down the
right thing: `<compose> down` for a compose install (offering `-v`, and saying
plainly that this destroys the database), `stop` and `rm` for a container.

### Behind Cloudflare (or any reverse proxy)

`docker-compose.yml` is set up for this: Cloudflare terminates TLS and proxies
to the origin over plain HTTP on port 80.

```yaml
server:
  bind: 0.0.0.0:8080          # the container port; compose publishes it on 80
  external_url: https://zoomies.sh
  tls:
    mode: off                 # Cloudflare holds the certificate
  trusted_proxies: [cloudflare]
```

Three things follow from that, and getting any of them wrong is quiet rather
than loud:

* **The external URL is `https://`, not `http://`.** It is what the session
  cookie's `Secure` flag is derived from, what the webhook URL is built from,
  and what the UI links to. The container serving HTTP does not change any of
  that. It does mean the UI has to be opened through the https address: a
  browser on a plain-http page -- `http://<ip>` to check the container is up --
  throws a Secure cookie away, so Zoomies refuses to sign you in from one and
  says why, rather than signing you in and out in the same second. To test over
  plain http, set `cookie_secure: false` for the duration.
* **`trusted_proxies` must trust Cloudflare.** Write the word `cloudflare` and
  Zoomies expands it to Cloudflare's published ranges, embedded in the binary.
  Without them the origin sees Cloudflare's address on every connection, so
  the audit log records Cloudflare for every action and the login rate limiter
  throttles the whole internet as one client. With them, Zoomies takes the
  address from `CF-Connecting-IP` — which Cloudflare sets and a client cannot
  override — falling back to the right-most non-proxy entry of
  `X-Forwarded-For`. The ranges move when the binary does, and `zoomies init`
  offers a "Cloudflare in front" choice that writes the token for you.
* **Only Cloudflare should be able to reach the origin.** Publishing port 80
  puts an unauthenticated webhook endpoint on the public internet with
  Cloudflare merely in front of it, not in the way. Firewall the origin to
  Cloudflare's ranges, or use a Cloudflare Tunnel and publish no port at all.

Zoomies will warn at startup that it is listening without TLS. In this
deployment that warning is expected, and it is the reason the warning says
"if a proxy already terminates TLS, this is expected" rather than treating it
as an error. The controller cannot see the proxy from behind it, so it has no
way to tell this deployment from an origin genuinely exposed in the clear.

The web UI does not repeat it: `bind.public_no_tls` is printed at startup and
by `zoomies config check`, but it is left off the UI's problems drawer
and the Settings page, because a count that is permanently amber on a
correctly configured fleet is a count nobody reads.

### `agent.capacity`

The maximum number of concurrent runners on this host. It is a hard ceiling the
scheduler respects; a pool's `max_runners` cannot exceed the capacity actually
available across matching hosts.

Default is half the CPU count, on the reasoning that a job usually wants more
than one core and the host still has to breathe.

With `scheduler.default_runner_limits` on, the capacity also sets each runner's
share: a runner whose pool sets no `cpus` or `memory_mb` is given the host's
allocatable CPU and memory divided by this number, as a real cgroup limit. A
capacity that is more slots than the machine has cores, or than it has 2 GB of
memory for, is warned about as `host.overprovisioned`, with the largest
capacity that fits.

### `agent.bootstrap_cpu_grace`

New runners keep their normal CPU allocation for **2m** before host-pressure
throttling can reduce it. This protects registration on a loaded host without
removing the CPU or memory limits. A running container does not prove its
GitHub listener is ready, so the grace is bounded by age, including after an
agent restart. It also covers a job accepted during that window.

Set `agent.bootstrap_cpu_grace` (or `ZOOMIES_AGENT_BOOTSTRAP_CPU_GRACE`) to
`0s` for immediate throttling, or up to `10m` for slow hosts. Restart the agent
after changing it. Configure standalone agents on their own hosts; the
controller's instance setting configures its embedded agent. Existing stored
values and environment overrides take precedence over defaults.

This is separate from `runners.docker_wait`: that wait now holds the host's
startup slot until the DinD daemon passes a `docker info` health probe, before
creating the runner. The fleet default is **3m**, with a pool's
`env.ZOOMIES_DOCKER_WAIT` taking precedence (whole seconds, 1–3600).
`0s` at fleet level selects the 2m fallback; it does not skip readiness.
The outer 10m agent create budget and `scheduler.provision_timeout` still
bound provisioning, including image pulls and queue time respectively.

The grace is not extra capacity. If normal limits themselves are too small,
reduce host capacity or assign the pool more CPU/memory. Changing these
settings affects new work; it does not resize existing containers.

### `agent.finished_retention`

A finished runner is removed from its host on the reconcile pass after the
controller acknowledges the runner's terminal report. The default is `0s`:
GitHub's job result and Zoomies' runner history remain available, but the
stopped container's writable layer, its Docker-in-Docker sidecar and any
Zoomies-created scratch directory do not accumulate from job to job.

Set a non-zero duration only when you deliberately want a window in which to
read the stopped container's local logs, for example:

```yaml
agent:
  finished_retention: 10m
```

This setting does not delete a workload before its final state is safe. If the
controller cannot acknowledge the terminal report, the agent keeps the
workload and retries. A completed GitHub job also triggers removal of its ephemeral
runner, including failed and cancelled jobs; this explicit removal does not wait
for the log-retention window. Pending host removals are recovered after a
controller restart and retried until the host confirms success.

New Docker runner and Docker-in-Docker containers rotate their local logs at
10 MiB, retaining three files per container. This bounds log growth while a
runner is active or waiting for removal. Existing containers keep their original
logging settings until replaced.

### `agent.docker_build_cache_mb`

Target size in MiB for unused build cache in the host Docker daemon. The default
is `0` (disabled). When explicitly enabled, the agent requests cache cleanup at startup and every five
minutes in a separate loop, so a slow Docker daemon does not delay runner
removal or heartbeats. Failed cleanup is logged and retried.

```yaml
agent:
  docker_build_cache_mb: 5120
```

Set `0` to disable this cleanup, for example when another service manages a
shared daemon's cache. The environment override is
`ZOOMIES_AGENT_DOCKER_BUILD_CACHE_MB`; changing it requires restarting the agent.
Both standalone and embedded agents use this setting.

This targets Docker's own builder cache, including cache left by cancelled
host-socket builds. On a shared daemon it includes unused cache from other
builds. Docker protects cache currently in use, so the target is not a hard
disk quota. Separate Buildx builders using the `docker-container` driver, job
images, named volumes and arbitrary files created through the host socket are
outside this cleanup. Podman and process backends do not use this setting.
Docker-in-Docker build data is discarded with each runner's sidecar instead.

The scheduler also stops placing work when the host's reported free disk falls
below its reserve (at least 2 GiB). That reading covers `agent.work_dir`'s
filesystem; a separate Docker data filesystem and disk written by jobs already
running still need enough capacity.

### `agent.registry_auth`

A base64 `X-Registry-Auth` value the container backends send when they pull an
image: the JSON `{"username":"...","password":"..."}` that `docker login`
writes, base64url-encoded. Empty means anonymous pulls, which is right for
public images.

Set it when a pool's image lives in a private registry. Without it such a pool
cannot use `pull_policy: pinned-only` at all — the pull it needs is the one the
registry refuses, and the runner never starts.

It is a credential, so prefer `ZOOMIES_REGISTRY_AUTH` in the service's
environment file to a value written into `zoomies.yaml`, which is world-
readable on plenty of hosts.

### `agent.docker_host`

Empty autodetects, **preferring a rootless socket**, in this order:

1. `$DOCKER_HOST`
2. `$XDG_RUNTIME_DIR/docker.sock`
3. `/run/user/<uid>/docker.sock`
4. `~/.docker/run/docker.sock` (Docker Desktop on macOS)
5. `/var/run/docker.sock`

The group a service account must join to reach a root socket is read from the
socket itself, not from the name `docker`. In a containerised deployment that
gid is `DOCKER_GID` in the environment file: `group_add` reads it, the compose
files also pass it into the container's environment, and `zoomies init` checks
it against the socket before the container is started -- a container is given
a group when it is created, and no `usermod` on the host can add one
afterwards. A socket that exists but cannot be opened is diagnosed rather than
guessed at: the agent reports the account it runs as, the group that owns the
socket, and whether that account is already a member. If it is, the fix is a
restart of the agent and not another `usermod` -- supplementary groups are
fixed when a process starts. In a container it reports the group the container
holds against the one it needs, and names the `DOCKER_GID` line to change and
the `docker compose up -d` that recreates the container.

### `agent.labels` and pool host selectors

Agent labels describe a host; a pool's `host_selector` requires them. Every
key/value in the selector must match.

```yaml
# on the GPU box
agent:
  labels: { gpu: "true", zone: "eu-west" }
```

```yaml
# in the pool
host_selector: { gpu: "true" }
```

A join token can carry labels too, and those win. Labels decide which pools'
work a host is offered — and therefore which pools' runner registrations it is
handed — so what the operator minting the token pinned is not something the
machine being enrolled can talk its way out of. Keys the token says nothing
about are still the agent's to declare.

### Re-joining a host

Joining with the name of a host that already exists replaces it: the row keeps
its ID, and the previous registration's runner records go with it. That needs
the agent token the previous registration was issued, which the agent sends from
its credentials file automatically. A rebuilt machine with its state directory
intact therefore re-joins with no ceremony.

If the credentials really are gone, delete the host first:

```sh
zoomies hosts delete host_xxxxxxxx
zoomies agent join https://zoomies.example.com --token <join-token>
```

A join token on its own is deliberately not enough — otherwise anyone trusted to
enrol one machine could seize any other machine by naming itself after it.

An empty selector matches any host, so once a specialised machine joins, give
the general pools a selector of their own — otherwise they are eligible for the
GPU box too. [Hosts and pools](hosts-and-pools.md) works that shape through.

### `agent.runner_sha256` and the process backend's download

The process backend downloads the `actions/runner` release itself, and it
refuses to install an archive it cannot verify. Zoomies ships the SHA-256 of
every archive for the release it pins by default, so the common case needs no
setting at all. Pin a different release with `github.runner_version` and give
its digest here -- every actions/runner release lists them in its notes -- and
the download is verified against that instead.

`agent.allow_unverified_runner_download` accepts an archive with no known
digest. It is warned about at startup, because it means executing whatever the
network handed over. `agent.runner_download_url` points the download at an
internal mirror that keeps GitHub's release layout underneath it.

### `scheduler.scale_up_delay`

How long a job must have been queued before it counts as demand. `0s` reacts
immediately, which is what you want most of the time. Raise it if your workflows
arrive in bursts that resolve themselves and you would rather not churn runners.

### `scheduler.max_runner_lifetime`

Drains a runner that has lived this long, the next time it is **not** busy. It
bounds how long a persistent runner's state and credentials live, so a pool that
keeps a minimum re-registers its runners this often. It never interrupts a
running job: a job that hangs keeps its runner busy, and ending that is what the
workflow's `timeout-minutes` is for. A runner that never finished registering is
`provision_timeout`'s to fail, not this setting's.

### `scheduler.provision_timeout`

Fails a runner that has been starting up this long without registering. It
covers `provisioning` and `registering` together — there is no second
"registration timeout" — and the `runners.not_progressing` problem is raised at
half of it, so the fleet says something while there is still time to look.

It is the last of three bounds on a runner's start and the only one that gives
up, so it has to be the most patient of them. An agent allows itself fifteen
minutes to materialise a runner, because a first pull of a large image on a slow
link is minutes rather than seconds, and a runner on a pool that provides Docker
then waits up to `runners.docker_wait` for that daemon before it registers at
all. Set inside those two, the scheduler condemns runners the rest of the system
is patiently still making — and the pool replaces each one, which puts a second
pull of the same image on the link that was slow to begin with. A cold fleet is
where that bites, and a cold fleet is the one least able to absorb it. The
validator warns when the number is inside them.

The default of `20m` is not the cost of a broken runner. A create that actually
fails — a missing image, a registry that refuses, a backend that will not
start — is reported by the agent and fails the row the moment the report lands.
This is the backstop for the create that is never reported at all, which is
worth being patient about because nothing else will notice it.

### `scheduler.drain_timeout`

Fails a runner that has been draining this long with **no job left on it**.

A drain asks a runner to finish what it is doing and stop, and the agent carries
that out. The queue those instructions live in is in memory on purpose, so a
controller restart drops one that had already been issued. The row is left in
draining, which is a state nothing else counts against: it holds its slot on the
host, its pool sits one runner short, and both stay that way for as long as the
controller runs. This bounds it — the runner is failed, so an operator sees it
and its slot is taken back.

It does not touch a runner still finishing a job, however long that takes.
Waiting is what a drain is, there is no maximum job duration in Zoomies by
design, and a build ended from here would look to its owner like a failure with
no cause. Ending a job that hangs is the workflow's `timeout-minutes`.

`0s` leaves a drain unbounded, which is the behaviour before this setting
existed.

### `scheduler.default_runner_limits`

Whether a runner whose pool leaves `cpus` or `memory_mb` unset is created with
one slot's share of its host's allocatable machine as a cgroup limit on that
field — the same share the scheduler already charges it. On by default.

The charge was always there; what was missing was the limit. A pool with no
limits was charged a slot's worth of the host, so the books balanced, and its
runners were created with no cgroup limit at all, so eight of them could each
take every core while the books said everything fitted. That is how a host's
Docker daemon stops answering, and it is what this setting prevents. The share
is given only where it would bind — a container backend whose daemon has said
it can apply the limit, on a host that has reported its size — and a pool's
own limits win on every field it set. Where a runner's limits came from is on
its page as `allocation_source`, and an out-of-memory kill on a defaulted
limit says so, with the two ways out. [Default
allocations](hosts-and-pools.md#default-allocations) has the rules in full.

`false` restores unlimited containers for pools that set no limits, and raises
`scheduler.default_runner_limits_off` at startup and in the problems drawer
for as long as it stands. Set `cpus` and `memory_mb` on every pool if you turn
it off; a fleet whose pools all set their own limits is unaffected either way.

### `scheduler.host_throttling`

Whether the controller steps a host down when its measurements say it is
overwhelmed: fewer slots, and a lower CPU quota on the runners already on it,
climbed back up one rung at a time after a stretch of calm. On by default.

The pressure holds refuse every new start while a host's CPU or memory is
acutely short, and release the moment a sample says otherwise; a host that is
overwhelmed on and off for an hour spends that hour being let back in at full
capacity and given a fresh runner each time. The throttle is what outlasts a
sample. Each rung takes a quarter of the host's slots and, down to half, a
quarter of every runner's CPU quota; it climbs every two minutes while the
pressure keeps coming back, to a top rung of three, and comes down after five
minutes of calm. A running job is never slowed below half its allocation, so a
throttle slows work and never fails it. [Current usage and automatic
holds](hosts-and-pools.md#current-usage-and-automatic-holds) has the ladder,
what ends a throttle, and which hosts can be throttled at all.

`false` raises `scheduler.host_throttling_off`, and a throttle standing when
the setting is switched off is lifted rather than left on a rung nothing will
ever step down. Leave it on unless something outside Zoomies manages the
hosts' load.

### `scheduler.auto_rerun` and `scheduler.auto_rerun_limit`

Whether a job this fleet broke is sent back to GitHub without anybody asking.
**Off by default**, and the default is the recommendation for most
deployments: the button on the job does the same thing when a person
has looked at the failure and decided —
[whose failure was it?](troubleshooting.md#ci-is-flaky-is-it-or-is-it-us)
has it.

A runner that dies under a job — the host rebooted, the container was killed
for its memory limit, the daemon stopped answering — fails that job in a way
that is, on GitHub, indistinguishable from a test failure. The job did not
fail on its merits, and the ordinary remedy is to run it again. With
`auto_rerun` on, Zoomies asks as soon as GitHub reports the job over.

It is narrow on purpose:

* **Only the fleet's own failures.** The same split the Jobs page shows: a job
  Zoomies has confessed to breaking. A test that failed is never re-run.
* **Only while GitHub's attempt number is at or below `auto_rerun_limit`.**
  That is what bounds a fault the fleet causes every time — a host out of
  disk, an image that will not pull — rather than re-running the same job
  until somebody notices the bill. The count is GitHub's own run attempt, so
  a controller restart does not reset it, and a re-run an operator asked for
  by hand counts against it too.
* **Only once per completion.** A duplicate delivery does not change the
  job's state, so it never reaches the re-run.

Each one writes a line on the job's timeline, marked *via recovery* rather
than as an operator's doing, and counts
`zoomies_job_reruns_total{trigger="fleet_fault"}`.

Turning it on raises `scheduler.auto_rerun_on`, which is a warning rather than
an error: it is not a weaker security posture, it is a standing permission to
spend the installation's GitHub minutes. A job that got as far as running may
also have had side effects outside GitHub that its author expected to happen
once — a deployment, a published package — and Zoomies cannot know which.

### `runners.default_cpus` and `runners.default_memory_mb` — how big a runner is

```yaml
runners:
  default_cpus: 2
  default_memory_mb: 4096
```

These two are where a pool's size sliders open when somebody chooses to set a
fixed size. They are **not** what a pool with no size becomes: such a pool is
given one slot's share of whichever host each runner lands on, which is the
sizing most fleets want and what a new pool does.

The distinction matters because the two behave differently as a fleet grows. A
share follows the machine — 3.8 cores on a 16-core box with four slots, 7.6 on
a 32-core one — so one pool is sized correctly on every host it reaches. A
figure typed here is the same everywhere, so it fits the host it was chosen for
and strands the machine on the ones that joined later.

What neither of them is, is *no limit*. A runner with no cgroup limit can take
every core and all of the memory on the machine it lands on, which is the shape
that stops the Docker daemon answering, times out every other create on that
host and reads on the Hosts page as a machine that is merely busy. The share is
applied as a real cgroup limit by `scheduler.default_runner_limits`, which is
on by default; with it off, a pool that names no size is charged the share and
given nothing, and the controller says so (`pool.size_unlimited`).

Two cores and four gigabytes suits most fleets, and a fleet of small boxes or
of compilers is entitled to say otherwise once here rather than on every pool
it creates. Changing them does not touch the pools that already exist — their
figures are their own — and the recommended capacity on the Hosts page follows
whichever is larger: what the enabled pools ask for, or this.

### `runners.minimum_cpus` and `runners.minimum_memory_mb` — the least a runner will take

```yaml
runners:
  default_cpus: 8
  default_memory_mb: 32768
  minimum_cpus: 4
  minimum_memory_mb: 24576
```

A fixed size is a standard, and machines are rarely exactly the size a
standard was chosen for. A pool that asks for 32 GB has nowhere to run on a
30 GB host, and its jobs wait for a machine that is never coming. A **minimum**
is the size the pool will still accept: when no host has room for the standard,
the runner goes on the host that can spare the most, and is given as much of the
standard as that host can spare — never less than the minimum. On the 30 GB host
above it gets 30 GB, not 24.

An **automatic** pool has a standard too: one slot's share of whichever host a
runner lands on. A host with a free slot but less than a whole share left —
because other pools' runners hold more than a slot's worth of it — used to leave
an automatic pool's job queued however much of the machine was idle. With a
minimum, the runner takes what that host can spare instead, never less than the
minimum; without one, an automatic pool still waits for a whole share, and the
pool's page says so and names the minimum as the fix. A minimum above a host's
share does nothing on that host, and a docker-in-docker slot is never cut below
what a runner and its daemon need between them.

The standard still wins wherever it fits. A minimum never shrinks a runner the
fleet could have given the full size to; it only turns "no host has room" into a
job that runs. Such a runner is recorded as *reduced* — the Runners page says
what it was given against what its pool asks for, and its create's reason says
why — and its host is charged what it was given, not the standard.

A host a pool runs on only at its minimum counts as one of the pool's hosts
everywhere a host is judged. The pool's room counts the runners the scheduler
really places there — as many at the standard as fit, then one more given what
is left if that covers the minimum — at the size they are given, so
`pool.max_above_room` and `pool.host_overcommitted` do not warn about room the
pool is using. `host.overprovisioned` judges a slot against the smallest
minimum every pool that reaches the host accepts, rather than a core and 2 GB,
and a pool that cannot be placed is told which resource is short of its
minimum, not of its standard.

These two settings are the fleet's minimum, and every pool that has not set one
of its own follows them — live. A pool's own minimum, set on its size step or
as `resources.min_cpus` and `resources.min_memory_mb`, wins on the field it
sets; where it is 0, the pool's minimum is the fleet's, so changing these
settings moves every such pool on the next scheduling pass, with nothing
rewritten. The two fields are independent: a pool can set its own memory
minimum and still take the fleet's CPU one.

A fleet minimum at or above a fixed pool's own standard means nothing for that
pool and is ignored there — a minimum is only a minimum below the size it is a
floor under. On an automatic pool it applies as it stands, capped at the slot's
share on each host like any minimum.

A pool's `0` therefore means *the fleet's*, not *none*. There is no per-pool
"no minimum" while the fleet sets one: a pool that must never run below some
size gives its own figure — its standard, if it should never be reduced at all.
Both settings default to 0, so a fleet that has never set them behaves exactly
as it did before they existed. The pool's page shows the minimum in force and
marks an inherited one *(fleet default)*; the API returns it as
`effective_minimum`, beside `resources`, which keeps the pool's own figures so
that saving or exporting a pool never freezes the fleet's value into it. A
minimum is held to the same floor any runner is: a quarter of a core and 512
MB.
[How big a runner is](hosts-and-pools.md#how-big-a-runner-is-and-how-many-there-are)
has what the wizard does with them, and what it checks the answer against.

### `agent.extra_ca_file` — behind a proxy that re-signs TLS

```yaml
agent:
  extra_ca_file: /etc/zoomies/acme-root-ca.pem
```

A network whose proxy intercepts TLS presents its own certificate for every
site, signed by the organisation's root CA. Until a runner trusts that CA,
nothing in a job reaches GitHub, a registry or a package mirror. This names a
PEM file on the agent's host holding that CA — it is a host setting, not a
pool one, because the proxy belongs to the network the host sits on.

When it is set, the Docker and Podman backends mount the file read-only into
every runner, and the runner image's entrypoint adds it to the system trust
store before the runner starts listening: `update-ca-certificates` on Ubuntu
and Debian images, `update-ca-trust` on the DNF family, using the passwordless
`sudo` the image already gives the runner user. That covers git, curl, OpenSSL
and the package managers. The entrypoint also sets `NODE_EXTRA_CA_CERTS` — the
runner and every JavaScript action are Node, which carries its own roots — and
points `REQUESTS_CA_BUNDLE` and `SSL_CERT_FILE` at the updated system bundle
unless a pool already set them. A docker-in-docker sidecar, which is what
pulls a dind job's images, gets the same mount and trusts it through
`SSL_CERT_DIR`, keeping its own roots too.

A custom image with no `sudo`, or without either trust store tool, still gets
`NODE_EXTRA_CA_CERTS` and logs a warning that the rest of the job will not
trust the CA; add it to that image's trust store when you build it.

The validator refuses a path that is relative, unreadable or holds no PEM
certificate (`agent.extra_ca_invalid`), and a good one is reported once as
information (`agent.extra_ca`), because every job on the host now trusts
whatever that CA signs. The process backend ignores the setting: a bare
runner is a process on the host and already uses the host's own trust store,
so add the CA there.

The setting is about runners. The controller and agent processes themselves
are Go, and reach GitHub through the proxy by trusting its CA the standard Go
way — `SSL_CERT_FILE=/etc/zoomies/acme-root-ca.pem` replaces the system roots,
`SSL_CERT_DIR` adds a directory to them — or simply through the host's trust
store, which Go reads by default. The runner image's settings are not needed
for that and do not provide it.

### `runners.docker_wait` and `runners.env` — what every runner starts with

```yaml
runners:
  docker_wait: 3m
  env:
    HTTPS_PROXY: http://proxy.internal:3128
    NO_PROXY: localhost,.internal
```

Both are fleet-wide and both are live: the controller writes them into each
runner's environment when it builds the create task, so a change on the
Settings page is in the next runner without anything restarting. A pool's own
`env` is layered over them, so the fleet says what is usual and a pool says
what is different.

`runners.docker_wait` is how long a runner on a pool with a `docker_mode`
waits for that daemon before it refuses to take a job. It reaches the runner
image as `ZOOMIES_DOCKER_WAIT`, in whole seconds. The fleet default is three minutes; the image and backend fallback for an
unset override remain two minutes: `dockerd` in a fresh
docker-in-docker sidecar sets up its storage driver and firewall rules before
it listens, and on a host that is also extracting images for the runners
queued behind it that takes longer than the thirty seconds an earlier default
allowed. A runner that gives up exits with code 69, and the Runners page says
so and points here. The image refuses anything outside one second to an hour,
so the validator refuses it first (`runners.docker_wait`); `0s` leaves the
image's default.

Setting `ZOOMIES_DOCKER_WAIT` on the controller itself overrides
`runners.docker_wait` the way any `ZOOMIES_*` variable overrides its setting,
and a bare number there — `120`, not `120s` — is read as whole seconds rather
than refused: that spelling named the runner image's own wait for years
before this setting existed, on a pool's `env` and often on the controller's
too, and an upgrade should not stop over a value that already meant something.

`runners.env` is for the variables every job on every pool needs — a proxy, a
package mirror, a `GOFLAGS`. Every job can read them, so a credential does not
belong there: a pool's `env` narrows the audience to that pool's jobs, and a
GitHub secret narrows it to the workflow. The variables the controller writes
for each runner individually — its name, labels, group and credentials — are
refused (`runners.env_reserved`), because one value for the whole fleet is
wrong for every runner in it.

#### How a runner picks up a proxy

A runner inherits `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` — in both
spellings, since curl reads only the lowercase ones — from the agent that
starts it. That holds on every backend: the bare-process runner, the Docker and
Podman runner container, and a pool's docker-in-docker sidecar, whose daemon
pulls the images a job's builds name and needs the proxy as much as the runner
does. Nothing else from the agent's environment reaches a runner.

A host that can reach GitHub only through a proxy has its agent configured with
one already — it could not reach the controller or pull an image otherwise — so
setting the proxy on the agent's service is usually all a proxied host needs.
Where that is not what the runners should use, the more specific setting wins,
variable by variable:

1. a pool's `env`,
2. `runners.env`,
3. the agent's own environment.

Setting a variable to an empty string in either is how a pool, or the whole
fleet, sends its runners direct although the agent uses a proxy. The sidecar
takes the same proxy variables as its runner and none of the pool's other
`env`, which are the job's rather than the daemon's.

`NO_PROXY` needs nothing added for Zoomies itself: a runner never talks to the
controller, and the docker-in-docker daemon is reached on `127.0.0.1`, which
proxy-aware clients already go to direct. Add your own internal registries and
package mirrors to it as you would for any other machine behind the proxy.

### `images.refresh_interval` — keeping a moving tag current

```yaml
images:
  refresh_interval: 1h
```

Every pool's image is prepared on a host by *prewarming* it, and prewarming is
otherwise triggered by exactly three things: creating a pool, editing one, and
`POST /pools/{id}/prewarm`. None of those happen on their own, so a pool that
names a tag which moves — and the default
`ghcr.io/eyupio/zoomies-runner:dev` moves on every merge to `main` — would
reach a host once, at the first job it ever ran, and keep that image for as long
as the host lived.

This setting is what re-runs the prewarm. Every interval, each pool's image is
prewarmed again on every healthy host that can run the pool, which pulls only
when the tag has actually moved.

It is deliberately not the same thing as a pool's **pull policy**:

| | Decides | Costs |
| --- | --- | --- |
| `pull_policy` (per pool) | what happens **when a runner is created** | a registry round trip in front of the job, which is the queue wait ephemeral runners exist to avoid |
| `images.refresh_interval` | how often the fleet **catches up in the background** | bandwidth, on nobody's critical path |

So the pairing that gets both a fast start and a current image is the default
one: `if-not-present` on the pool, and an hourly refresh here.

Runners that already exist are never touched, and neither is the image any of
them was created from — a container keeps the image it started with until it is
replaced. What changes is the image the *next* runner is created from.

Set it to `0` to switch it off. That is the right answer for an air-gapped
fleet, or one that pins every pool to a digest, and Zoomies says so once at
startup rather than leaving you to wonder.

---

### `provider.max_machines` — the ceiling on what the fleet may rent

```yaml
provider:
  enabled: true
  max_machines: 6
```

`provider.enabled` decides whether Zoomies may rent hosts at all. It is off,
and it stays off until somebody deliberately turns it on, because every machine
behind it is a bill.

`max_machines` is the ceiling across every provider put together, and it is the
one number to set in the same edit as `enabled`. Each provider row carries its
own limit as well, but those bound one hypervisor each; this is what bounds a
mistake — a provider configured twice, a demand signal that never settles, a
pool whose jobs nothing can run so the shortfall never closes.

**Zero rents nothing**, exactly as a pool's `max_runners` of zero runs nothing,
and the validator says so rather than letting a fleet look enabled and do
nothing. That is deliberate: the alternative reading, where an unset number
means "as many as it takes", puts the one setting that decides the size of an
invoice behind a value somebody can forget.

`max_creates_in_flight` is the other half: how many machines may be being built
at once. A burst of two hundred queued jobs should not become two hundred
simultaneous clone requests, whatever the ceiling allows in total.

### `provider.paused` — the kill switch

```yaml
provider:
  paused: true
```

Holds the creation of new machines while leaving everything else running:
machines already up keep working, idle ones still drain, drained ones are still
deleted, an operation whose outcome was unknown is still resolved, and
ownership is still verified. That asymmetry is the point. A switch that stopped
deletion too would leave VMs running with nothing tending them, which is the
opposite of what somebody reaching for a kill switch wants.

The same switch exists per provider, as a row, and that is the one the Hosts
page presses — so one misbehaving hypervisor can be held without stopping the
rest. This setting is the fleet-wide version, in the file, for the case where a
restart should come back held.

### `provider.delete_grace` — how long a quiet machine is left alone

```yaml
provider:
  delete_grace: 10m
```

A machine's host is counted unhealthy after 90 seconds without a heartbeat and
lost after five minutes. This is how much longer still a machine is left alone
before the fleet treats it as gone, and it must comfortably outlast both — a
machine destroyed for a network blip takes the job it was running with it, and
that job's owner sees a failure with no cause. The validator warns when it is
set at or below the 90 seconds, which is the setting most likely to be tuned
down by somebody impatient with a slow scale-down.

`idle_timeout` and `scale_down_cooldown` are the other end of the same
question: how long a machine's host must have been empty before it is drained,
and how long that emptiness must hold continuously before anything is deleted.
Set the cooldown below one idle period and a fleet pays the creation cost again
in every gap between two bursts.

## Pool settings

Pools live in the database, not in the config file — they are created in the UI,
the CLI or the API. These are their fields:

| Field | Meaning |
| --- | --- |
| `name` | Unique. Appears in scaling reasons and in the UI. |
| `installation_id` | Which GitHub App installation this pool registers against. |
| `labels` | What `runs-on` must ask for. Normalised to lowercase, and always includes `zoomies`, which Zoomies adds to every pool. |
| `runner_group` | GitHub runner group. New organisation pools default to the managed `zoomies` group; set an explicit name or an empty value to choose another group or GitHub Default. Repository targets have no runner groups. |
| `backend` | `docker`, `podman` or `process`. |
| `image` | Runner image for the container backends. Changing it replaces the pool's idle runners: one made from the old image is drained and a new one takes its place, and a busy one finishes its job first. |
| `pull_policy` | `if-not-present` (the default), `always`, or `pinned-only`, which refuses to run anything but the digest the pool names. |
| `platform` | The machine these runners need: `os` (a distribution, not a kernel), `os_version` and `arch`. It picks the runner image, and it keeps the pool off hosts running something else. Every field is optional; an empty one constrains nothing. See [Naming and platforms](naming.md). |
| `runner_version` | Pin an `actions/runner` release instead of tracking the latest. |
| `min_runners` | Kept warm even with nothing queued. `0` is usually right. |
| `max_runners` | Hard ceiling. **Always set this** — it is your backstop against a runaway workflow. |
| `repository_scale_up_limit` | Best-effort limit on new capacity attributed to one repository; `0` disables it. This is a creation throttle, **not** a strict concurrency or isolation boundary: GitHub can assign any matching queued job to an existing compatible idle runner. Strict isolation requires repository-specific pools and corresponding repository-specific `runs-on` labels in workflows. |
| `priority` | Higher-priority pools are given creation capacity first when the fleet cannot satisfy every pool at once. Pools at the same priority share it fairly. Under `scheduler.max_creates_per_tick`, a lower-priority pool whose oldest queued job has waited a full `scheduler.interval` is first given one create, so a busy top tier cannot starve it; the higher pool's scaling reason then says it was deferred for fairness across priorities. |
| `idle_timeout` | How long an idle runner waits before being drained. |
| `ephemeral` | One job per runner. Leave it on. |
| `no_default_labels` | Off. On, runners register with only the pool's `labels` — no `self-hosted`, operating system or architecture — as `config.sh --no-default-labels` does, and a job asking for one of those no longer matches the pool unless it lists it. Only a non-ephemeral pool may set it: GitHub adds those labels to every just-in-time runner itself. See [Leaving out the default labels](hosts-and-pools.md#leaving-out-the-default-labels). |
| `docker_mode` | `none`, `dind`, or `host-socket`. Anything but `none` switches a pool on the stock runner image to its Docker variant, under the same tag — see [below](#jobs-that-build-container-images) and [security.md](security.md). |
| `resources` | `cpus`, `memory_mb`, `disk_gb`, `pids_limit` per runner. Leaving `cpus` and `memory_mb` out is how a pool says “the host decides”: each runner is then given one slot's share of whichever machine it lands on, charged against that host and applied as a real cgroup limit. Set them for the same size on every host. `disk_gb` is advisory, enforced only where the backend can, and independent of that choice. |
| `cpu_burst` | The elastic CPU policy: `mode` is `off`, `observe` (decide and publish metrics, move nothing) or `automatic` (lend the host's spare CPU to a busy runner above its guarantee), and `max_cpus` is the most one runner may be lent up to, with `0` meaning the host's allocatable CPU. Needs automatic sizing on `docker` or `podman`; a new pool that qualifies starts on `observe`, an existing one stays `off`. See [Elastic CPU zoomies](elastic-cpu.md). |
| `runner_settings` | The fleet timings this pool overrides: `provision_timeout`, `drain_timeout`, `max_runner_lifetime`, `scale_up_delay` and `docker_wait`. Every field is optional and a pool follows the fleet on the ones it leaves alone — including after the fleet's own figure changes. Zero is an answer in each rather than an absence, so an absent field, an explicit `null` and `"0s"` are three different things. See [Runner settings a pool can override](hosts-and-pools.md#runner-settings-a-pool-can-override). |
| `cache` | A disposable accelerator directory mounted at `/opt/zoomies-cache`, scoped `pool` or `repository`, with an enforced `size_limit`. It is not workflow storage and may be evicted — see [below](#the-pool-cache). |
| `cost_per_runner_hour` | An optional rate you supply, used only to estimate what the fleet costs. Zoomies never embeds prices of its own. |
| `host_selector` | Restricts the pool to matching hosts. |
| `env` | Injected into every runner. A proxy variable here wins over the one the runner would otherwise inherit from its agent — see [How a runner picks up a proxy](#how-a-runner-picks-up-a-proxy). |
| `run_as_root` | Off. Turning it on is warned about. |
| `enabled` | A disabled pool drains to zero and creates nothing. |

### The labels to give a pool

Give it one branded label of its own — `zoomies-linux-x64`, `zoomies-gpu` — and
let a workflow write that alone:

```yaml
runs-on: zoomies-linux-x64
```

One label is enough to reach a pool. Branding it means a reviewer of the pull
request that introduces it can tell the job has left GitHub's runners, which
`runs-on: [self-hosted, linux, x64]` does not say. Zoomies also adds `zoomies` to
every pool, so `runs-on: zoomies` means "anywhere in this fleet" — useful for a
repository nobody has assigned a pool to yet.

Runners are named for the brand too, and for their pool's shape:
`zoomies-4vcpu-ubuntu-2404-biscuit-a3f9qz2m`, which is what GitHub shows in its
runner list and in every job's log header. See
[Naming and platforms](naming.md#runner-names).

### The pool cache

`cache` mounts a directory at `/opt/zoomies-cache` inside every runner the pool
creates, and keeps it between runners. It exists to stop an ephemeral fleet
paying for the same download twice — a package or layer cache, a toolchain, a
warm module directory.

It is **not** workflow storage. Nothing guarantees a hit, an operator may empty
it at any time, and a job that cannot run without it is a job that will fail one
morning. Use `actions/cache` for anything a workflow depends on.

```yaml
cache:
  enabled: true
  scope: pool         # or: repository
  size_limit: 0       # bytes; 0 is unlimited
  source: ""          # a named-volume prefix, or an absolute host path
  repository: ""      # owner/name, for a repository cache under an org installation
  tools: false        # keep the setup-* actions' tool cache too; see below
```

`scope` decides who shares it. `pool` gives every runner in the pool the same
cache, which is the faster of the two and assumes the repositories in the pool
may see each other's build artefacts. `repository` gives each repository its own,
which is what to use when the pool serves repositories that should not.

A repository cache needs to know which repository it is for. An installation
scoped to a single repository says so by itself and `repository` stays empty.
An installation scoped to a whole organisation — one App over one shared fleet,
which is the usual deployment — does not, so name it there as `owner/name`
under that organisation. Without this a shared fleet would need a separate
installation per repository to give each one a cache.

Naming the repository does not stop GitHub giving this pool's runners another
repository's job. A runner registered to an organisation takes any queued job
whose `runs-on` matches its labels, and that job reads and writes the cache.
So a repository cache under an organisation installation is only as private as
the pool's labels: give such a pool a branded label that only that repository's
workflows use, and keep it that way. Zoomies warns about the combination on the
pool's page and in the problems panel, because the cache's privacy rests on
something it cannot see — see [security.md](security.md#6-the-dangerous-toggles).

`source` is left empty for a daemon-managed volume, which is the easy answer. An
absolute path puts the cache on a disk you chose; anything else is treated as a
volume-name prefix. Zoomies appends the scope's own identity to whichever you
give, so two pools never collide, and refuses a source containing `..`.

`size_limit` is enforced, not advisory. As a runner starts, whole cache entries
are removed, least recently modified first, until the cache is back under the
limit. Deleting from a cache is only safe while nothing is reading it, so a
runner that starts to find another runner still using the same cache leaves it
alone and the next start tries again — on a pool with `max_runners` above one,
that is most starts, and the eviction happens at the one that finds it idle.
This bounds how far the cache drifts over the limit from one job to the next.
It is not a filesystem quota: a single job can still fill the disk before the
next runner starts, and if that matters, give the cache its own filesystem.

Only a directory can be measured, so a non-zero `size_limit` requires `source`
to be an absolute host path. On a named volume the bytes are the daemon's, on a
filesystem the agent may not even share, and a limit there would be a number in
a form that controlled nothing — so it is refused rather than accepted.

#### Keeping a tool cache

`cache.tools` keeps the pool's tool cache between runners as well: what
`setup-python`, `setup-node`, `setup-go` and `setup-java` download, so the first
job to ask for a version downloads it and the rest find it there. It is a
setting of the pool, stored with it — the **Keep a tool cache as well** box
under the cache on the pool's size step — and not an environment variable to
remember.

```yaml
cache:
  enabled: true
  scope: pool          # or: repository
  tools: true
```

It is kept in each host's [shared folder](#the-shared-folder), under
`cache/tools`, in a folder named for the pool — and the repository, for a
repository-scoped cache — so it is shared with exactly the runners the pool
cache is. It is mounted read-only at `/opt/zoomies-tools-shared`, and each
runner is pointed at a folder of its own, `/opt/zoomies-tools`, holding a link
to every version in the kept cache whose install finished. That is its own
folder rather than the image's `/opt/hostedtoolcache`, so an image that ships
toolchains, such as [`zoomies-runner-full`](#the-full-image), keeps them. A pool
whose `env` sets `AGENT_TOOLSDIRECTORY` itself keeps its own.

Only the [fill](#filling-the-tool-cache) writes to the kept cache. A job that
asks for a version it does not have yet downloads it into the runner's own
folder, which is removed with the runner, so two jobs never unpack into the same
folder at once and neither sees the other's half-written copy. The next scan
puts that version in the kept cache for the jobs after it.

This shares more than a build cache does. What is in a tool cache is run, not
just read. A job cannot change the kept cache, but whatever the fill put there
is what every job on the pool runs, so only turn it on for a pool whose
repositories already trust one another, and prefer `scope: repository` when
they do not.

It needs the cache on and a container backend: a process runner uses the host's
own tool cache already. A host whose shared folder cannot be created starts the
pool's runners without the kept tool cache and says so in its log, rather than
not starting them.

#### Filling the tool cache

A kept tool cache starts empty, and each host's copy fills one job at a time.
The [toolchain scan](#githubtoolchain_scan_interval-what-each-pools-jobs-install)
knows what the pool's jobs will ask for, so when a scan finishes, each host that
can run the pool is asked to put those versions in its copy before the jobs
arrive: the twelve asked for by the most jobs, if there are more.

The fill runs in a short-lived container of the pool's own image, as its runner
user, with only the tool cache mounted, one pool at a time per host, and never
in a runner's slot. It resolves each version the way the setup action would —
the newest stable release that matches — from the source that action reads,
checks the download against the checksum its publisher lists (Node.js, Go and
Temurin do; the Python manifest lists none, and `setup-python` checks none
either), and lays it out where the action looks first:

| Toolchain | Resolved against | Filled |
| --- | --- | --- |
| Python | the `actions/python-versions` manifest | plain versions (`3.12`, `3.11.x`), on Ubuntu images only, because those are the only builds it publishes |
| Node.js | nodejs.org's release index | plain versions, `lts/*` and `lts/<name>` |
| Go | go.dev's release list | plain versions and `stable` |
| Java | Adoptium's API | the `temurin` distribution only |
| .NET | — | never: `setup-dotnet` installs outside the tool cache |

A version already there is left alone. A range such as `>=3.10`, a version read
from a file, and a distribution the fill does not serve are listed as skipped
with the reason, not guessed at: a toolchain installed under a name the action
does not look for is a download the job still makes. `GET /api/v1/toolchains`
reports each host's latest fill under `fills`, with what became of every
version. It downloads through the pool's proxy, or the agent's, and trusts the
host's [extra CA](#agentextra_ca_file-behind-a-proxy-that-re-signs-tls) as the
runners do.

A host whose agent predates the fill is not asked, and one added since the last
scan is filled by the next. With `github.toolchain_scan_interval` at 0, the
default, that is the next scan you start.

#### The shared folder

Every host keeps the data runners have to reach in one folder:
`/var/lib/zoomies/shared` on a Linux host where Zoomies runs as root, and
`shared` under the state directory everywhere else. It has one folder per
purpose:

| Folder | What is in it |
| --- | --- |
| `cache/tools` | each pool's [kept tool cache](#keeping-a-tool-cache) |
| `cache/pools` | pool caches whose `source` is pointed here, as `/var/lib/zoomies/shared/cache/pools` |

The agent creates any of these that are missing each time it starts, which is
how a later release adds a folder: it needs the shared folder to be writable by
the account Zoomies runs as — `zoomies` for a native install, uid 65532 in the
container images. The installer creates it that way.

A container deployment mounts the host's `/var/lib/zoomies/shared` at the same
path inside the controller's container. The host's Docker daemon binds a
runner's cache folders from it, and a folder the controller named by any other
path would be one the daemon cannot see. An installation made before the shared
folder existed has neither the folder nor the mount; `zoomies upgrade` and
`install.sh --upgrade` [offer to add both](upgrading.md#what-a-release-adds-to-the-host).
For a Compose file you maintain by hand, create it before starting:

```sh
sudo mkdir -p /var/lib/zoomies/shared && sudo chown 65532:65532 /var/lib/zoomies/shared
```

A cache folder Zoomies creates for a runner is writable by every account — the
runner is uid 1001 in the stock images, and something else in your own — while
the folders above it keep everyone else out. One that is already there is left
as you made it.

### Jobs that build container images

A job that runs `docker`, `docker buildx` or `docker compose` — which includes
`docker/setup-qemu-action`, `docker/setup-buildx-action` and
`docker/build-push-action`, and any job with a `container:` or a `services:`
block — needs a Docker daemon, and its pool decides whether it gets one. Set the
pool's `docker_mode` to `dind` or `host-socket`; the default, `none`, gives the
job no daemon at all, and its first Docker step fails. Both alternatives weaken
the pool's isolation and both are warned about at startup;
[security.md](security.md#6-the-dangerous-toggles) says what each costs, and
`dind` is the one to prefer.

That is the one setting. A daemon is worth nothing to a job whose image has no
client to reach it with, and the stock runner image deliberately carries none —
most pools never build an image, and a client on every runner is cold-start
time spent for nothing — so a pool that asks for a daemon while on
`ghcr.io/eyupio/zoomies-runner` is switched to
`ghcr.io/eyupio/zoomies-runner-docker` under the same tag as it is saved. That
covers every tag this build publishes: the channels (`latest`, `dev`, `main`, or
no tag at all), the release it was cut from, and the operating-system aliases a
pool gets by naming a platform (`ubuntu-2404`, `debian-12-dev`). One step of one
workflow publishes both images, so a tag that exists for one exists for the
other. The response, the audit row and the pool's page all show
the image that runs; the wizard says so on the step that decides it and shows
it on the review step; and a pool saved before this rule existed is moved by a
migration the first time a controller that has it starts. Idle runners made
from the old image are replaced, so a warm pool does not keep handing Docker
jobs to runners that cannot run them. The switch is not reversed when the
daemon goes away again: the variant runs everything the stock image does.

Three kinds of image are left exactly as you set them. A tag this build does
not hand out — a commit pin (`sha-<commit>`), or a release older than the
controller running it — promises nothing about the variant: a run whose second
build did not finish published the stock image and not its Docker one, and the
first prerelease predates the variant altogether. Moving a pool onto a tag the
registry may not have would stop every job on it, not only the Docker ones. A
digest reference (`…@sha256:…`) names one exact image and cannot be moved to
another. An image of your own — a mirror of the stock image under another
registry included — is yours to equip, and only has to put `docker` on the
runner's `PATH`.

A pool left on either of the first two is not left to find out: it raises
[`pool.docker_client_missing`](problem-codes.md), on its own page and in the
problems drawer, naming the reference to pin instead —
`ghcr.io/eyupio/zoomies-runner-docker:sha-<commit>`, or a digest of the variant.
A runner that starts with a daemon it has no client for also says so in its own
log, at the top, before any job runs; the job itself fails at its first Docker
step with

```text
Error: Unable to locate executable file: docker.
```

which names the missing binary and not the reason.

`ghcr.io/eyupio/zoomies-runner-docker` is the stock runner image plus
`docker-ce-cli`, `docker-buildx-plugin` and `docker-compose-plugin` — the client
only. It never runs a daemon of its own; that is what `docker_mode` is for.

Whichever image a pool runs is pulled under its `pull_policy`, and the default,
`if-not-present`, fetches a tag only when the host does not already have it. On
its own that would leave a pool on `latest` running the build it first pulled
for as long as its hosts lived, which is what
[`images.refresh_interval`](#imagesrefresh_interval-keeping-a-moving-tag-current)
exists to prevent: the image is prewarmed again on a timer, in the background,
so the tag moves without a registry round trip in front of any job. Setting
`pull_policy: always` puts that round trip back on every runner created — with
the layers still cached, so the cost is a manifest check and not a download —
and is only worth it when an hour is too long to wait.

On a `host-socket` pool Zoomies also adds the group that owns the host's
`docker.sock` to the runner container, because the runner is not root inside it
and a socket it cannot open is the same failure with a different message.

### How a job finds a pool

1. The job's `runs-on` labels are normalised.
2. Labels every runner advertises anyway — `self-hosted`, `linux`, `windows`,
   `macos`, `x64`, `arm`, `arm64` — do not constrain the choice, except that a
   pool declaring a *contradicting* os/arch label is excluded.
3. A pool matches when it provides every remaining label the job asked for.
4. Among matching pools, the most specific wins (fewest surplus labels), with a
   deterministic tie-break by name, so the same job always lands in the same
   pool.

A queued job that matches no enabled pool appears in the Overview's problems
panel. That is almost always a typo in `runs-on` or a label missing from a pool.

---

## Validation

Every start prints what it found:

```text
$ zoomies controller
level=INFO msg="configuration warning" code=bind.public_no_tls setting=server.bind
  title="listening on 0.0.0.0:8080 without TLS"
  detail="session cookies, API tokens and the GitHub App private key you paste during setup all cross the network in cleartext."
  fix="put a TLS-terminating reverse proxy in front, or set server.tls.mode to self-signed or files."
```

Check a file without starting anything:

```sh
zoomies config check --config /etc/zoomies/zoomies.yaml
zoomies config print          # the effective config, secrets blanked
```

The same findings are served at `GET /api/v1/problems` and rendered in the UI's
problems drawer, so a warning cannot be missed just because nobody was reading
the logs the day it appeared. An operator can dismiss one they have read; that
is a per-browser preference and changes nothing the API or `zoomies status`
reports.

### `scheduler.registration_concurrency`

The default of **1** serialises credential minting within each installation.
Other installations and cleanup keep progressing. Rate-limit responses establish
an installation hold until GitHub's reset or retry deadline. Excess demand stays
with the scheduler without creating a provisioning row or waiter goroutine.
Raise this to at most **16** only when measured registration throughput needs it;
an outage may then leave that many requests already in flight. Lowering it takes
effect as existing requests finish. Admission resumes on a scheduling pass after
the hold expires. Registration quota exhaustion does not increase pool failure backoff.

### `agent.prewarm_timeout`

Background image preparation has a **5m** budget, configurable from **1s to 15m**.
This bounds how long background work can occupy the host startup slot. Foreground
creates keep their independent budget and have priority over pending background
work. An already-running pull is not pre-empted. Restart standalone agents with
their own setting; changing controller configuration only configures its embedded agent.

### `agent.prewarm_jitter`

Before background image preparation, each agent waits a random duration from zero
to **30s**, without holding a lifecycle or startup slot. Set **0s** to disable or
up to **5m** to spread a large fleet further. This applies to manual prewarming too.
It does not change GitHub/provider retry deadlines or foreground image pull policies.
