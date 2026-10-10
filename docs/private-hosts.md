---
icon: material/lock-outline
title: Private hosts with Tailcat
description: >-
  Turn your home lab into GitHub Actions runner capacity. Connect private hosts
  over built-in Tailcat tunnels: no public IPs, port forwarding or Tailscale.
---

# Your home lab. Your runner fleet.

**Put the machines you already own to work.** The mini PC under your desk,
your ARM lab and the build server behind your office firewall can all join the
same Zoomies fleet as your cloud hosts. Private connections powered by
[Tailcat](https://tailscale.com/tailcat) are built into Zoomies: one guided
setup, one command on the host, and a live **Tailcat host** badge when it joins.

No public host IP. No router port forwarding. No Tailscale account or separate
Tailcat installation. Free, open source, and managed from the same web UI as
the rest of your pack.

Still deciding which machines to use, or where the controller should run?
[Runners in your home lab](home-lab.md) covers the planning.

## Add a private host

1. Open **Hosts → Add a host** and choose **Private connection · Tailcat**.
2. Leave capacity on **Automatic**, or choose a limit. Add any labels your pools
   select, such as `location=home` or `arch=arm64`.
3. Select **Get the command**, choose the machine's operating system, then copy
   and run it on the intended machine. On Linux and macOS the installer detects
   the machine and runtime and installs the Zoomies agent service; on Windows
   the command is for PowerShell opened as administrator, and works the same
   way with a private connection. The existing supported native agent platforms
   and backends apply.
4. Leave the page open. It recognises the enrolment and shows the host and
   matching pools. On the Hosts page, **Tailcat host** identifies its private
   connection; the separate health badge tells you whether the agent is live.

There is no controller address to type in this mode. Advanced users with the
matching Zoomies binary already installed can use the shorter command under
the existing-binary instructions The install command selects the controller's published
channel; unpublished builds must be distributed to agents manually.

Before you run it, read
[what the agent owns on a host](security.md#what-the-agent-owns-on-a-host):
its service and user, its work directory, the containers carrying its labels,
its per-pool cache, and the one daemon-wide thing it does, pruning unused
Docker builder cache, with the setting that turns that off on a shared
daemon.

The runtime must permit network-interface discovery (including netlink on
Linux); sandboxes that block it cannot initialise Tailcat. No root privilege
or TUN device is needed for the tunnel itself.

Both machines still need outbound internet access. Tailcat connects them to
one another; it does not supply their internet connection. Runner jobs still
contact GitHub, image registries and dependency sources using the host's normal
network. A tunnel does not make an offline or air-gapped host internet-enabled.

## Choose the connection that fits

| Connection | When to use it | What you configure |
| --- | --- | --- |
| Direct | Your host can reach the controller over your LAN or HTTPS | The controller's reachable address |
| Tailcat | Your host cannot reach that address, or you want the agent connection independent of public ingress | Choose Private connection; Zoomies supplies the tunnel credentials |

Direct agents already connect outbound and work behind NAT. Tailcat extends
that model to controllers without an agent-facing public endpoint, and adds
WireGuard encryption, NAT traversal and relay fallback without making you
build and manage an overlay network first.

A controller with a public agent endpoint needs a private connection only for
the hosts that cannot reach that endpoint; every host that can should join
directly.

## How it works

The controller listens inside a userspace Tailcat network. Each private agent
opens outbound HTTP requests over a WireGuard-encrypted tunnel for enrolment,
heartbeats, task polling, results and live logs. The HTTP connection exists
inside the encrypted transport; it never falls back to ordinary HTTP on the
network. Environment HTTP proxies and redirects cannot reroute these requests.

The tunnel admits the agent API and single-use Proxmox setup uploads. It does
not expose the web UI, administrator API, SSH, Docker sockets, arbitrary ports
or your LAN.
Zoomies still requires a single-use join token to enrol and a host-specific
agent token for ongoing operations. The controller marks the connection from
its own observation, not an agent-supplied label.

Tailcat tries to establish a direct peer connection and uses a relay where
that is not possible. The badge identifies the transport, not a claim that
traffic is peer-to-peer at that instant. The current UI does not report the
underlying path or round-trip time.

## Restarts and credential protection

The controller stores its Tailcat identity encrypted with its existing
instance encryption key in SQLite. Existing tunnels resume with the same
address after a restart. Back up the database **and its matching encryption
key**, as described in [Backup and restore](backup-and-restore.md).

An agent keeps the private address beside its agent token in `agent.json`,
with mode `0600`. Its normal configuration contains only
`agent.controller_url: tailcat://controller`. Keep each host's state directory
private and do not mount it into runner containers. Do not clone an enrolled
agent's credentials into a second machine.

**The enrolment command contains credentials.** Keep it out of screenshots,
public issues, logs and source control. Clear it from clipboard and shell
history on shared machines. The join token expires and is single use, but the
Tailcat address is a persistent connection capability shared by private hosts
in this controller. Revoking an unused join token prevents that enrolment; it
does not rotate the controller's tunnel address. The address alone cannot
register a host or retrieve work without a valid Zoomies credential.

Drain and delete a host through the existing host controls to revoke its agent
access. To stop all private connections, set `ZOOMIES_TAILCAT_ENABLED=false`
and restart the controller. This preserves the identity for later re-enabling;
it is not a key rotation operation. Removing private connection credentials
from a compromised machine remains necessary.

## Configuration and troubleshooting

Private enrolment is enabled by default but makes no Tailcat network connection
until the first private host is requested. Once an identity exists, the
controller resumes its listener on startup; if no relay answers then, the
controller starts anyway, raises
[`tailcat.unavailable`](problem-codes.md#runtime-hosts-and-installations) and
keeps retrying, its sealed relay first, so the listener comes back without a
restart once a relay does. It requires normal Zoomies
authentication and a usable controller encryption key; it is unavailable in
auth-disabled demo mode.

| Symptom | What to check |
| --- | --- |
| `zoomies agent` fails to start naming a missing private connection address | `agent.json` in the agent's work directory has a host ID and agent token but no `tailcat_address` -- it was edited, truncated, or restored from a backup taken before this host enrolled. The address cannot be recovered locally; mint a fresh enrolment command from **Hosts → Add a host → Private connection** and run it on the host. |
| Private connection is unavailable | Enable authentication, check the controller encryption key, and ensure `server.tailcat_enabled` is true; restart after changing these settings. |
| Cannot reach a Tailcat relay | Both sides need outbound access to Tailcat's relay infrastructure; retry after correcting firewall or internet connectivity. No token is minted if setup fails. For a listener that already exists this shows as `tailcat.unavailable` in the problems list, which clears by itself once a relay answers. |
| Command expired before the host joined | Use **Mint another token** on the waiting page. Capacity and labels are retained. |
| A Tailcat host is offline | Check the Zoomies agent service and outbound internet access. The agent retries transient connection failures; existing jobs are not deliberately killed by a tunnel interruption. Prolonged loss follows the normal host-health and recovery rules. |
| Host has no matching pool | Set host labels and pool selectors to match, and check runtime/platform compatibility and capacity. The tunnel does not change scheduling rules. |
| Restored controller cannot decrypt its identity | Restore the encryption key from the same backup as the database. |
| A private provider is unreachable | Check that `zoomies gateway` is still running beside it and that its `--target` is the API the endpoint names; the gateway logs a warning each time it cannot reach the target. If the gateway was started from a new state directory, its address changed and the provider has to be given the new one. |

Tailscale describes its hosted Tailcat relays as rate-limited and not intended
for high-throughput use. They retain metadata logs. A relayed live-log stream
can therefore be slower than a direct connection. This integration currently
uses Tailcat's default relay discovery; custom relay configuration is not
exposed in Zoomies. See [Tailcat's operational details](https://tailscale.com/tailcat)
and [upstream source](https://github.com/tailscale/tailcat).

The tunnel serves the agent connection only. GitHub webhook ingress and access
to the Zoomies UI are configured separately. A fully private controller can
use the existing GitHub polling fallback when webhooks cannot reach it.

## Private providers

For Proxmox, **Providers → Add provider → Proxmox VE → Generate setup command**
automates the steps below. Run the command as root on the Proxmox host and return
to Zoomies. The wizard detects the host name, creates the API credential, trusts
the local certificate and installs a persistent gateway. An existing runner
agent keeps its registration and service; the provider uses separate state and
a separate binary. See [Proxmox setup](proxmox.md#the-provider).

The following commands are for a gateway you want to configure yourself.

The hypervisor a home lab rents machines from is behind the same router the
hosts are. A Proxmox cluster on `192.168.1.10` has no address a controller in
the cloud can dial, and the same private connection that brings a host in
brings the cluster's API in too, with the direction reversed: a host connects
out to the controller, so the controller listens inside the tunnel; a provider
is something the controller connects to, so a **gateway** listens instead.

`zoomies gateway` runs on the hypervisor node or on any machine beside it that
can reach the API. It forwards every connection that arrives through the tunnel
to that one address and nothing else (no port on the machine, no route into
the LAN, no second destination) and TLS is not terminated there: the
controller still verifies the cluster's certificate end to end, so the gateway
reads none of the API token that passes through it.

1. On a machine beside the hypervisor, run
   `zoomies gateway --target 192.168.1.10:8006`. It prints a Tailcat address
   beginning with `tc`.
2. Open **Providers → Add provider**, keep the endpoint as
   `https://192.168.1.10:8006` (the name the certificate is checked against)
   choose **Private connection · Tailcat**, and paste the address.
3. Press **Check**. The preflight now reaches the cluster through the gateway,
   and the provider card shows **Tailcat** beside the endpoint.

Leave the gateway running: as a systemd unit, a compose service or however you
keep the agent alive on that machine. It keeps its identity in `--state-dir`
(the same directory as the agent's credentials by default, mode 0600), so a
restart keeps the same address and the provider row keeps working. Moving that
file aside starts the gateway with a new address, which the provider then has
to be given.

**The address is a credential.** Whoever holds it can open connections to the
hypervisor's API, so it is handled exactly as the API token is: sealed on the
provider row with the instance key, never returned by the API, never in an
audit row. A form that cannot read it back cannot erase it by accident either,
switching the provider back to **Direct** is what clears it. Run the gateway
with `--quiet` where its output goes to a log, and read the address from the
state file instead.

The same setting that stops private hosts stops private providers:
`ZOOMIES_TAILCAT_ENABLED=false` makes the controller refuse to build a client
for a provider with a private connection, naming the setting, rather than
dialling an address it cannot reach.

## Why this is different

Hosted runner services sell managed execution capacity. Zoomies lets you turn
your own private hardware into that capacity, mix it with cloud machines,
and operate the whole fleet yourself, without a Zoomies licence or per-minute
platform fee. You still pay for your hardware, power, connectivity and any
cloud resources you choose.

**Your hardware. Your network. One pack.** This is the feature to showcase:
private home-lab runners with a guided web UI and built-in encrypted connectivity.
Compare deployment models as well as connectivity: some providers also offer
private networking or bring-your-own-compute options. Zoomies puts both the
controller and your runner hardware under your control.
