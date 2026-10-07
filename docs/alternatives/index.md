---
icon: material/compare-horizontal
title: Alternatives to Zoomies for self-hosted GitHub Actions runners
description: >-
  The ways to run GitHub Actions on machines you control, side by side: ARC,
  GARM, the AWS Terraform module, TestFlows' Hetzner runners, Cirun and the
  hosted runner services, with when each is the better choice.
---

# Alternatives to Zoomies

There are several good ways to run GitHub Actions jobs somewhere other than
GitHub's own runners. Zoomies is one of them, and for many setups it is not the
right one. These pages say where each alternative is stronger, where Zoomies is,
and how to move between them.

Each page is written from the project's public documentation, dated, and linked
so you can check it. They are comparisons of documentation, not benchmarks, and
a feature one page does not mention is not a claim that it is missing.

## At a glance

| | Model and licence | Runs on | Pick it when |
| --- | --- | --- | --- |
| **Zoomies** | Self-hosted; [AGPL-3.0](https://github.com/eyupio/zoomies/blob/main/LICENSE) | Linux hosts you own or rent, anywhere an agent can dial out from | You have machines, not a cluster, and want one place to see and run the fleet |
| [ARC](../actions-runner-controller.md) | Kubernetes operator; Apache-2.0 | A Kubernetes or OpenShift cluster | You already operate Kubernetes |
| [GARM](garm.md) | Self-hosted; Apache-2.0 | Providers you configure | You want a provider catalogue, or GitHub Enterprise Server or Gitea |
| [AWS Terraform module](terraform-aws-github-runner.md) | Terraform module; MIT | EC2, in your AWS account | Your infrastructure is AWS and Terraform |
| [TestFlows Hetzner runners](testflows-hetzner-runners.md) | Self-hosted; Apache-2.0 | Hetzner Cloud servers | You want cheap, standby cloud runners on Hetzner |
| [Cirun](cirun.md) | Managed service | Your cloud account, or on-premises agents | You would rather have a service run the control plane |
| [Blacksmith, WarpBuild and RunsOn](../hosted-runner-services.md) | Services and a licensed AWS tool | Their cloud, or yours | You do not want machines to look after, or you need macOS |
| [GitHub-hosted runners](../costs.md) | GitHub's service | GitHub's virtual machines | The jobs are public, or the free minutes cover you |

## How to choose

1. **Do you already run Kubernetes?** If someone on the team is comfortable
   operating it, [ARC](../actions-runner-controller.md) is likely the answer.
2. **Is your infrastructure one cloud, described in code?** A cloud-native
   module such as [the AWS one](terraform-aws-github-runner.md) puts CI inside
   the account and the tooling you already use.
3. **Do you have machines already, in a rack, an office or a home lab?** That is
   the case Zoomies is built for. [Runners in your home lab](../home-lab.md)
   and [runners without Kubernetes](../runners-without-kubernetes.md) plan it.
4. **Would you rather pay to not run it?** A hosted service trades money for
   not looking after hosts. [Costs](../costs.md) has the sums.

If it is still unclear, the [FAQ](../faq.md) answers what people ask before they
self-host runners, and the [quick start](../quickstart.md) takes a fresh host to a
running job in about five minutes.
