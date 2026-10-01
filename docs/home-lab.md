---
icon: material/home-outline
title: GitHub Actions runners in your home lab
description: >-
  Planning GitHub Actions runners on machines you own: which home-lab hardware
  suits which jobs, where to put the controller, keeping jobs on the right
  machines, what happens when a box is switched off, and your home network.
---

# Runners in your home lab

A mini PC, an old desktop, a Raspberry Pi or a Proxmox server you already run
can take real CI work: builds that are too slow on GitHub's two-core runners,
arm64 builds done natively instead of under emulation, or simply minutes you no
longer pay for. This page is about planning that fleet.
[Private hosts with Tailcat](private-hosts.md) is the step-by-step for joining a
machine once you know where it fits.

## What a home machine is good for

| Machine | Good for | Worth knowing |
| --- | --- | --- |
| A mini PC or an old desktop, x86-64 Linux | Most Linux jobs, on every [runner image](naming.md#the-runner-image) | Its cores and memory are shared between its runners, each with a guaranteed slice — see [how big a runner is](hosts-and-pools.md#how-big-a-runner-is-and-how-many-there-are) |
| An arm64 board or server running 64-bit Linux | Native arm64 builds and tests | Every runner image is published for arm64; a pool can select arm64 hosts without any labelling |
| A Proxmox VE server | Runner hosts made and removed as pools need them | Zoomies can [rent virtual machines from Proxmox](proxmox.md) through its provider contract |
| A spare Windows PC | Windows jobs, as processes on the machine | The Windows agent is [not yet qualified](index.md#what-is-qualified) on real hardware |

macOS is not on the list: there is no macOS runner image.

## Where the controller goes

The controller is what GitHub talks to, so where it runs decides how quickly
jobs start.

- **On a small cloud VM, with the home machines joined to it.** GitHub delivers
  its `workflow_job` webhooks to the VM's public address and runners start at
  once. Home machines connect out to it through [Tailcat](private-hosts.md), so
  nothing at home is opened to the internet — no public address, no port
  forwarding.
- **At home, on one of the machines.** Everything stays in the house. Without an
  address GitHub can reach, webhooks cannot arrive and Zoomies falls back to
  polling, which starts runners in tens of seconds rather than at once — see
  [`server.external_url`](configuration.md#serverexternal_url). A tunnel that
  gives the controller a public HTTPS address, such as the Cloudflare Tunnel the
  [compose file](compose.md#the-proxy-in-front) is set up for, restores
  webhooks without opening a port.

## Keeping jobs on the right machines

A pool chooses its hosts with a host selector, and a host carries labels you
give it when it joins, such as `location=home`. Architecture and operating
system need no labels at all: a pool that selects `arch=arm64` only ever lands
on arm64 machines. Give each pool a branded label for workflows to ask for —
`zoomies-home-arm64`, say — and a reviewer can see where a job will run.
[Hosts and pools](hosts-and-pools.md) has the details.

## When a machine is switched off

Home machines sleep, reboot and get unplugged. Zoomies expects it:

- A host that stops sending heartbeats takes no new runners after 90 seconds.
  After five minutes it is presumed gone, and any job it was running is marked
  as lost.
- Before switching a machine off on purpose, **cordon** it: it finishes what it
  is running and accepts nothing new. Uncordon it when it is back.
- If every host a pool can use is away, its jobs wait in GitHub's queue, and the
  scheduler says why — `unhealthy`, `cordoned` — in Scaling events, the
  problems drawer and the CLI. A pool whose selector also admits a cloud host
  keeps jobs moving while the home machines are off.

[When nothing can be placed](hosts-and-pools.md#when-nothing-can-be-placed)
lists every reason and its fix.

## Your home network

A runner runs your repositories' code on your machine, and by default a job's
container can open connections to anything that machine can reach — which, at
home, can include your NAS, your router's admin page and every other device on
the network.

- **Run trusted code only.** Keep home runners for private repositories you
  control. On a public repository anyone who can open a pull request is running
  code in your house; GitHub's advice is not to do it, and [Security](security.md)
  explains why Zoomies does not change that.
- **Put runner hosts on their own network** where you can — a separate VLAN or a
  guest network, with a firewall rule that keeps them away from the rest of the
  house.
- **Know what the tunnel does and does not carry.** Tailcat carries only the
  agent's connection to the controller; it does not expose the web UI, SSH,
  Docker sockets or your LAN. It also gives the host no internet of its own:
  jobs still reach GitHub and package registries over your normal connection.

## What it costs

Nothing for Zoomies, and nothing from GitHub for self-hosted runner minutes
today; you pay for the electricity and your time. A machine you already own
starts ahead of anything you would rent — [what self-hosted runners
cost](costs.md) works through the sums against GitHub's hosted runner prices.

## Getting started

1. Run the controller: [quick start](quickstart.md) on a small VM or a home
   machine.
2. Connect GitHub, and create a pool with a host selector for the machines it
   should use.
3. Join each home machine through **Hosts → Add a host → Private connection ·
   Tailcat**, adding a `location=home` label as you go.
4. Point a workflow's `runs-on` at the pool's label and push.

## Where to go next

- [Private hosts with Tailcat](private-hosts.md): joining a machine, step by
  step.
- [Self-hosted runners in Docker](docker.md): what a job can reach, depending on
  how it gets Docker.
- [Elastic CPU zoomies](elastic-cpu.md): letting a busy runner borrow the cores
  nobody else is using — useful on a machine with more cores than jobs.
