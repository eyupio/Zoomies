---
icon: material/source-repository-multiple
title: One self-hosted runner fleet for many repositories
description: >-
  Sharing self-hosted GitHub Actions runners across repositories: where GitHub
  lets a runner be registered, how one Zoomies fleet serves a whole
  organisation, what personal accounts need, and how to keep repositories apart.
---

# One fleet for many repositories

A self-hosted runner serves whatever GitHub registered it to. Registered to an
organisation, it can take jobs from every repository there; registered to one
repository, it takes that repository's jobs and no others. So the question of
sharing runners across repositories is mostly a question of where they are
registered — and Zoomies registers them wherever the GitHub App you connect is
installed.

## Where GitHub lets a runner be registered

| Level | Who it serves | Zoomies |
| --- | --- | --- |
| A repository | That repository only | Yes: a repository target, written `owner/name` |
| An organisation | Any repository in the organisation that its runner group allows | Yes: an organisation target |
| An enterprise | Several organisations in a GitHub Enterprise Cloud enterprise | No |

[GitHub's documentation](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/add-runners)
covers all three. A personal account has no level of its own: its runners are
registered repository by repository.

## One organisation, every repository

Install the GitHub App on the organisation — the installer and the Connect
GitHub dialog both offer it — and one pool can serve every repository there. A
workflow reaches it by the pool's label:

```yaml
runs-on: zoomies-linux-x64
```

Give each pool one branded label and add pools as the work needs different
shapes — a bigger machine, a GPU host, arm64 — rather than one pool per
repository. Every pool also answers to `zoomies`, so `runs-on: zoomies` means
"anywhere in this fleet", which suits a repository nobody has assigned a pool to
yet. [The labels to give a pool](configuration.md#the-labels-to-give-a-pool) has
the rest.

On an organisation, GitHub puts runners in a
[runner group](https://docs.github.com/en/actions/concepts/runners/runner-groups),
and a group decides which repositories may use them. When it connects, Zoomies
creates an organisation-wide `zoomies` group, enables it for public
repositories, and registers new pools' runners there; a pool can name another
group instead, and a group an administrator chose is never rewritten.
[A pool belongs to one installation](hosts-and-pools.md#a-pool-belongs-to-one-installation)
explains how that works, and what happens when a group cannot be found.

GitHub's own default keeps public repositories out of runner groups, and for a
good reason: a self-hosted runner on a public repository runs code from anyone
who can open a pull request. Enabling the group for them makes the runners
reachable, not safe. Read [Security](security.md) before a public repository
uses this fleet.

## A personal account

GitHub offers no account-wide runners for a personal account, so there is no
single registration that covers all its repositories. Connect each repository
you want to run jobs for as its own target, `owner/name`, with a pool of its
own. It is one installation and one pool per repository, all in the same fleet
and on the same hosts.

## Keeping repositories apart

Within an organisation, **GitHub decides which runner gets a queued job**: it
offers the job to any runner in scope whose labels match. A runner Zoomies
started for one repository's job may be handed another repository's job from
the same organisation instead, and nothing on Zoomies' side can prevent it.
Ephemeral runners make that harmless for most fleets — each job still gets a
fresh runner, destroyed afterwards — but it matters where repositories must not
share machines or caches.

| To keep apart | Use | How strong |
| --- | --- | --- |
| Which repositories may use a set of runners | A runner group limited to those repositories, named on the pool | Enforced by GitHub |
| One repository from all others | A repository-target installation with its own pool | Enforced by GitHub |
| Workloads by label | Separate pools with separate labels | A convention kept in workflow files |
| One busy repository from starving the rest | A pool's `repository_scale_up_limit` | A throttle on new runners, not isolation |

A pool's cache follows the same rule: a repository cache under an organisation
installation is only as private as the pool's labels. See
[The pool cache](configuration.md#the-pool-cache) before sharing one.

## Moving many repositories at once

The [migration wizard](migration.md) reads the workflows in every repository
the App can see, maps GitHub's hosted runner labels to your pools, and opens one
pull request per repository after showing you each diff — never more than
twenty-five at a time, and never onto a default branch directly. Jobs already
on self-hosted runners are left alone; for those,
[give a pool the label they already use](migration.md#coming-from-your-own-static-runners).

## Where to go next

- [Quick start](quickstart.md): connect GitHub and create the first pool.
- [Hosts and pools](hosts-and-pools.md): how a pool decides where its runners go.
- [Self-hosted runners in Docker](docker.md): what a job can reach, depending on
  how it gets Docker.
