---
icon: material/aws
title: "Zoomies vs terraform-aws-github-runner"
description: >-
  How Zoomies compares with the AWS Terraform module for self-hosted GitHub
  Actions runners: an infrastructure-as-code EC2 autoscaler against a host-based
  fleet controller, and when each fits.
---

# Zoomies and the AWS Terraform module

[terraform-aws-github-runner](https://github.com/github-aws-runners/terraform-aws-github-runner)
is a Terraform module that builds event-driven, auto-scaling GitHub Actions
runners on EC2 in your own AWS account. It is the maintained successor to the
Philips Labs repository, which is archived. If your infrastructure is AWS and
Terraform, it is a strong choice, and this page is about when it is and when
Zoomies is.

Written from the module's public documentation, reviewed in September 2026.

## Side by side

| | AWS Terraform module | Zoomies |
| --- | --- | --- |
| Model and licence | Terraform module you apply and operate; MIT | One Go binary you run; [AGPL-3.0](https://github.com/eyupio/zoomies/blob/main/LICENSE) |
| Runs on | EC2, including spot, in your AWS account | Linux hosts anywhere an agent can dial out from |
| Scaling | Lambda functions react to webhooks and scale to zero | A controller reconciles pools against queued jobs, and scales to zero by default |
| Images | Your own AMIs and subnets | [Runner images](../naming.md#the-runner-image) for Ubuntu, Debian, Fedora and Rocky Linux |
| Architectures | Linux x64 and arm64, and Windows | Linux x64 and arm64; Windows [not yet qualified](../index.md#what-is-qualified) |
| State | Your AWS account and Terraform state | One SQLite file |
| Seeing the fleet | Your AWS tooling | A live [web UI](../ui.md), [metrics](../metrics.md) and an audit log |

## When the AWS module is the better choice

- **Everything you run is on AWS.** CI sits in the same account, network, IAM and
  bill as the rest, and the module is infrastructure as code like the rest.
- **You want a fresh machine for a job**, and spot pricing for it. Zoomies gives
  each job a fresh container on a shared host instead.
- **You need Windows runners today.** The module documents them.

## When Zoomies is the better choice

- **You are not all on one cloud**, or not in one at all. Hosts can be a rack, an
  office, a home lab, a Proxmox cluster and a rented server in one fleet.
- **You do not want a Terraform pipeline to run CI.** One command installs the
  controller; a host joins with one more. See the [quick start](../quickstart.md).
- **You are moving repositories off hosted runners.** The
  [migration wizard](../migration.md) rewrites `runs-on` across them.

## Moving between them

The two use different labels, so they run side by side. Move one repository at a
time, then destroy the module's stack when nothing targets it.

## Where to go next

- [Hosts and pools](../hosts-and-pools.md): how Zoomies decides where a runner
  goes.
- [What it costs](../costs.md): the sums for self-hosting against hosted runners.
- [All alternatives](index.md)
