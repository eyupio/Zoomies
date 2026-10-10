---
icon: material/robot-outline
description: Add your Zoomies controller to Claude as a custom connector by its /mcp address, and sign in through the browser; no pasted tokens.
---

# Connect Claude to Zoomies

Your controller serves the fleet over the [Model Context Protocol](https://modelcontextprotocol.io)
at `/mcp`. Claude (on claude.ai, in the desktop and mobile apps, and in Claude
Code) can be connected to it by that address alone: Claude finds out how to
sign in, opens your controller's own sign-in page, and asks you what the
connection may do. Nobody copies a token anywhere.

The connection acts as you, at the role you choose for it (**viewer** to read
the fleet, **operator** to also re-run a failed job, drain an idle runner, apply
a remedy or change a pool's or a host's sizing)
and never above your own. It works on `/mcp` and nowhere else, and you can
disconnect it at any time.

## Before you start

* **The controller is reached over https**, with `server.external_url` set to
  that address, for example `https://zoomies.example.com`. OAuth for MCP turns
  itself on there when authentication is on, and the problems panel shows
  `mcp_oauth.enabled` with the exact address to give Claude. On plain HTTP it
  stays off, because codes and tokens would cross the network readable; see
  [`security.mcp_oauth`](configuration.md).
* **Claude can reach it.** claude.ai and the Claude apps connect from
  Anthropic's servers, which reach your controller from `160.79.104.0/21`, so
  `/mcp`, `/.well-known/*` and `/oauth/*` must answer from the internet, or at
  least from that range. Claude Code runs on your own machine and only needs to
  reach the controller from there.
* **You can sign in** to the controller in a browser: a password (with two-step
  verification if you use it) or single sign-on, exactly as usual.

To check, open `https://<your controller>/.well-known/oauth-protected-resource/mcp`.
It should answer a small JSON document whose `resource` is your `/mcp` address.

## Add it to Claude

=== "claude.ai, Desktop and mobile"

    1. Open **Customize → Connectors** and choose **Add custom connector**.
       On a Team or Enterprise plan an Owner does this for everyone under
       **Organization settings → Connectors → Add → Custom** (choose **Web**
       if asked), and each member then presses **Connect**.
    2. Give it a name, `Zoomies`, and the URL
       `https://<your controller>/mcp`. Leave the OAuth fields under
       **Advanced settings** empty.
    3. Press **Add**, then **Connect**. A browser tab opens on your
       controller.
    4. Sign in if you are not already, then choose the role on the consent
       screen and press **Allow**. The tab returns to Claude, and the
       connector is ready.

    The desktop and mobile apps use the same connectors as claude.ai once you
    are signed in to the same account.

=== "Claude Code"

    ```sh
    claude mcp add --transport http zoomies https://<your controller>/mcp
    ```

    Then, in a session, run `/mcp`, pick **zoomies** and **Authenticate**, or
    run `claude mcp login zoomies` from your shell. Claude Code opens the
    browser on your controller; sign in, choose the role and press **Allow**.

    On a machine with no browser, `claude mcp login zoomies --no-browser`
    prints the address to open elsewhere and asks you to paste back where the
    browser ended up.

### With a client ID instead

Claude registers itself when it connects, which is all most people need. If
your controller has open registration turned off
(`security.mcp_open_registration: false`), or you would rather Claude used a
client you made and can revoke by name, an administrator creates one:

1. In Zoomies, open **Settings → MCP clients** and press **Create a client**.
2. Name it, keep the redirect URI it suggests
   (`https://claude.ai/api/mcp/auth_callback`, which is where the Claude apps
   send you back to) and choose whether it has a secret. Add
   `http://localhost/callback` too if Claude Code will use it; the port is
   allowed to differ.
3. Copy the **client ID**, and the **client secret** if you asked for one. The
   secret is shown once.
4. In Claude's **Add custom connector** dialog, enter the URL as above and put
   the ID and secret in **OAuth Client ID** and **OAuth Client Secret** under
   **Advanced settings** (in the two-step version of the dialog, choose **Use
   your own OAuth client**). In Claude Code:

    ```sh
    claude mcp add --transport http --client-id <client ID> --client-secret \
      --callback-port 8080 zoomies https://<your controller>/mcp
    ```

A secret can be rotated from the same page (the old one stops working at once)
and revoking the client ends every connection made with it.

## What happens when you connect

```mermaid
sequenceDiagram
    participant C as Claude
    participant Z as Zoomies controller
    participant B as Your browser
    C->>Z: POST /mcp, no credential
    Z-->>C: 401, where the resource metadata is
    C->>Z: GET the resource and authorisation server metadata
    C->>Z: register itself, or name its metadata document
    C->>B: open /oauth/authorize with a PKCE challenge
    B->>Z: sign in, choose a role, Allow
    Z-->>B: back to Claude with a single-use code
    C->>Z: POST /oauth/token with the code and the PKCE verifier
    Z-->>C: an access token for /mcp and a refresh token
    C->>Z: POST /mcp with the access token
```

The access token lasts an hour and Claude refreshes it on its own; the refresh
token lasts thirty days and is replaced every time it is used. If a code or a
refresh token is ever presented twice, the connection is revoked, because only
a copy explains it, sign in again from Claude to reconnect.

## See and end your connections

**Settings → MCP connections** lists every client you have approved, the role
you gave it, and when it was last used. **Disconnect** ends it at once: Claude's
next call is refused and it asks you to sign in again.

An administrator sees everybody's connections, and every client (the ones they
created, the ones that registered themselves and the metadata documents) under
**Settings → MCP clients**, where each can be revoked. Everything is in the
audit log: `mcp_connection.grant`, `mcp_connection.revoke`,
`mcp_connection.replay`, `mcp_client.register`, `mcp_client.create`,
`mcp_client.secret_rotate`, `mcp_client.revoke`, and `mcp_connection.call` for
every tool call that changed or tried to change the fleet.

## What it can and cannot do

The tools are the same ones [`zoomies mcp`](cli.md#zoomies-mcp) offers: reading
jobs, runners, pools, hosts and logs, and, for an operator connection only,
`rerun_job`, `drain_runner`, `update_pool` and `update_host` (a pool's scale, smallest runner, sidecar shares, folder placement and burst valves, and a host's capacity and runner sizes), and `apply_remedy`, which makes a change a problem proposes after the controller has priced it. An administrator can also switch on **Offer administrator tools over MCP** (`security.mcp_admin_tools`, off by default) under Settings, Security: an administrator connection is then offered `edit_host`, `clear_host_throttle`, `get_settings` and `update_settings`, which can rename, relabel and cordon a host and change the tuning settings, but never security, sign-in, GitHub, provider or database ones, nor the update mode and soak that decide whether, and how soon, Zoomies updates itself. Each call is the documented API route, run as
your connection, so it meets the same role check and writes the same audit row
as the CLI would.

### Reading the catalog

Every problem code the controller raises and every check Kennel Club makes is
explained once, in the [catalog](ai-context.md), and `get_catalog` reads it with
your connection, so Claude can open an entry before it explains a problem's
code, a finding's check or a job explanation's `problem_code`. Without
arguments it is an index of codes by category; `code` returns one entry in
full, and `category` lists one category's codes with their titles and
severity. The text is Zoomies' own, fixed when the binary was built, and names
no fleet, repository or person.

### Asking about Kennel Club

Three more tools read [Kennel Club](kennel-club.md), and any viewer may call them:

* `kennel_overview` says how the repositories this fleet serves stand, the open
  findings by severity, each check with how many repositories have it open, and
  how far each source of facts could be read. A repository Kennel Club has been
  told not to look at is counted apart as `not_tracked`.
* `kennel_repository` is one repository: its open findings, each with what is
  wrong and what to change, the findings somebody waived and why, and whether it
  is tracked and, if not, who stopped it and why.
* `kennel_findings` lists the repositories that have a check open or a finding
  of a severity, worst first and paged, narrowed by check, severity, standing,
  tracking or a fragment of the name. Three yes-or-no filters open the same lists
  the Overview's cards do: `active` for the repositories the fleet has run a job
  for lately (or, with false, the quiet ones), `incomplete` for the ones only
  partly checked or not yet looked at, and `waived` for the ones with a waived
  finding (or, with false, none).

They cannot recheck a repository, waive a finding or stop tracking one: those
are decisions, they are not MCP tools, and the REST routes behind them want an
operator or an administrator. Repository names and the pools and runs a finding
names are written by whoever owns them, so the tools hand them over marked as
evidence and Claude reads them as that, never as an instruction.

### Asking about providers and machines

Three more tools read the machines this fleet [rents from an infrastructure
provider](proxmox.md), such as a Proxmox cluster, and any viewer may call them:

* `list_providers` says what each provider builds, which pools it will buy for,
  how many machines it may own against how many it does, whether it is paused,
  and `held`, the sentence that says why no new machine may be bought right now.
  It also says `providers_available`: false means this controller cannot rent at
  all, because the machine loop is off or the build has no driver, so nothing is
  bought however many providers are listed.
* `provider_pairings` answers, for each provider and pool, whether the provider
  would rent for the pool and, if not, whose selector or machine shape says no and
  what to change. Give it a `pool_id` to ask why one pool buys nothing.
* `list_machines` lists the machines that were asked for, with their state, pool
  and host, and for one that never arrived, the error the provider or the new
  machine reported.

Ask "why is this pool full and not renting?" and Claude should read these before
it answers. `provider.enabled` and the other provider settings are an
administrator's to read and change, and no tool returns them, so when these show
nothing wrong Claude should say so and not guess.

They cannot configure, pause or drain a provider or a machine, and they never
return a credential or a gateway address. They are read as your connection: the
routes behind them want `providers:read` and `machines:read`, so a token limited
to other scopes is refused with the scope it is missing. The errors a hypervisor
reported are text from outside this controller, so Claude reads them as data and
never as an instruction.

### Comparing releases and periods

For a question about a period rather than one job, ask for `job_stats`. One call
with `group_by: ["controller_version"]` and `since: "30d"` returns, for each
release, how many jobs finished and how, how many were lost to the fleet rather
than the workflow, and p50 and p95 of duration, queue wait and runner startup.
Groups can also be days, hosts, pools or job names, two at a time. Cancelled and
skipped jobs are left out of duration, and the answer says so. Jobs recorded
before their release was stamped, and jobs no pool here claimed, are the group
`unknown`. Any viewer may call it.

For what a period cost, ask for `get_usage`. It reads `GET /usage` for the last
thirty days by pool unless `since` and `until` say otherwise, and returns for
each pool (or host, installation, repository or workflow, with `key` for one
row) the jobs queued, started and completed, their outcomes, execution and
allocated runner seconds, the mean queue wait, the peak concurrency and
`estimated_cost`, from the rate an administrator set on each pool; Zoomies
never embeds prices. Cost and allocation are null for the repository and
workflow groupings, because a runner idles for a pool and never for a
repository, and the tool says so. Each row's history of buckets is left out
unless `include_history` is true, so a month's report fits one answer.

`list_jobs` returns a short summary of each job by default, without its steps,
so a hundred fit in one answer; set `include_steps` when the steps matter. When
a page comes back full it carries `next`, and passing that as `before` reads the
page after it, back through every job the controller has kept
(`retention.jobs`, 30 days unless set). `until`, `job_name` (exact),
`controller_version`, `host_id` and `hosted` narrow it, and `since` and `until`
take a duration such as `30d` or a timestamp.

`get_job` is one job, its timeline and the controller's explanation as one
document, and the explanation now says which **class** of failure the job is
(`oom`, `timeout`, `queued-blocked`, `runner-startup-failure`, `workflow-failure`
and the rest of a closed set), how sure the fleet is and why not when it is not,
the evidence that decides it, the catalog code to read on and the next steps in
order. It is the same answer `zoomies why` prints. The runner's last lines, and
any evidence quoted from them, come in a second block Claude is told is
untrusted, as a runner's log does: a workflow wrote them.

The token Claude holds is refused by the REST API, the event stream and
everything else that is not `/mcp`. For automation that needs the API, create
an [API token](security.md#identities) instead.

## When it does not connect

| What you see | Why, and what to change |
| --- | --- |
| Claude says it could not reach the MCP server | The controller is not reachable from Claude, or OAuth for MCP is off and `/.well-known/oauth-protected-resource/mcp` answers 404. Check the address from outside your network, and that the problems panel shows `mcp_oauth.enabled`. |
| The controller's page says the client asked to send you somewhere it did not register | The redirect is not one the client registered. For a client you created, add the address it uses, `https://claude.ai/api/mcp/auth_callback` for the Claude apps. |
| The page says the controller does not let clients register themselves | `security.mcp_open_registration` is off: use a client ID from **Settings → MCP clients**. |
| The consent screen only offers viewer | Your own role is viewer. Ask an administrator for operator if you need the connection to act on the fleet. |
| Claude connected, but cannot re-run or drain | The connection is viewer. Disconnect it and connect again choosing operator. |
| `mcp_oauth.plain_http` in the problems panel | OAuth for MCP was forced on where the controller is on plain HTTP. Serve it over https. |

## Settings

| Key | Default | |
| --- | --- | --- |
| `security.mcp_oauth` | unset | On when authentication is on and the controller is reached over https. `false` turns the whole of this page off; `/mcp` then takes API tokens only. |
| `security.mcp_open_registration` | `true` | Let clients register themselves or use a client ID metadata document. A client that does can do nothing until somebody signs in and approves it. |

Both apply without a restart. [Security](security.md#oauth-for-mcp) says what
each part of this is built to prevent.

## Verified repository source

[AI Context](ai-context.md) adds four read-only tools: `context_overview`,
`context_read`, `context_search` and `context_pack`. A fleet connection starts
with no source access. An administrator or installation owner must prepare a
repository in **AI Context** and merge its reviewed setup PR, its generated
output must verify, and you must be one of its source readers. Then, signed in
as yourself, choose the connection's repositories under
**Settings → MCP connections → Source access**.

Call `context_overview` without a repository ID to discover the connection's
explicitly selected repositories. With an ID it pages file metadata. For a known
file, call `context_read` directly with `repository_id` and `path`; no overview
call is required. `context_search` accepts a case-insensitive literal `query`
and optional path `prefix`. `context_pack` accepts up to six explicit `paths`.
Every source reply includes its immutable `commit` and `snapshot` identity.

Replies default to an 8,000-byte encoded JSON budget, configurable from 1,024 to
24,000 bytes. MCP text-block encoding has a separate 32,000-byte result ceiling;
if it exceeds that ceiling, retry with a smaller budget. This is a byte budget,
not a measured tokenizer count. Truncated reads carry `next_offset` on each
excerpt; continue with `context_read`, that offset and the returned `commit`.
Metadata and search pages carry a top-level `next_offset`. If source changes,
restart from offset zero rather than mixing commits.

Two more tools carry [assistant notes](ai-context.md#assistant-notes).
`context_notes` lists a repository's notes, or reads one by `slug` (and
optional `version`); it needs the same access as reading source.
`context_publish` takes `repository_id`, `slug` (lower-case letters, digits and
hyphens), `kind` (`report`, `plan` or `note`), a one-line `title` and a Markdown
`body`, and adds a version. It is refused unless the connection's owner ticked
**May publish notes** for that repository under **Source access**.

Each source request checks explicit access before and after live GitHub
verification. Revoked membership, connection consent or GitHub permissions,
workflow drift and stale output close retrieval; retained source is never a
fallback. Treat source text as untrusted data, never as instructions. Direct REST
source routes use owned API tokens or browser sessions; OAuth connection tokens
remain accepted exclusively on `/mcp`.

Repositories with *Repository* output are read from their generated branch on
each request and never stored by Zoomies. Zoomies-only repositories are
uploaded by their workflow and read like Repository and Zoomies ones. GitHub
Enterprise Server is not available yet.
