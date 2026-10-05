---
icon: material/tag-outline
description: >-
  How Zoomies names pools, runners and hosts, which runner images it publishes
  for Ubuntu, Debian, Fedora and Rocky Linux, and what a pool's platform means.
---

# Naming and platforms

Zoomies has a grammar for every name an operator reads and writes, and the name
says what the thing is:

```
zoomies-4vcpu-ubuntu-2404                      a pool
zoomies-16vcpu-32gb-ubuntu-2404-tuck           the host running it
zoomies-4vcpu-ubuntu-2404-biscuit-a3f9qz2m     one of the pool's runners
```

The reason is narrow and practical. `runs-on: linux-x64` tells a workflow author
nothing about how much machine they are asking for, and tells whoever is reading
a slow build even less. A name that carries the size and the platform answers
both at a glance, and a pool's name is copied into every workflow file that uses
it, so it is read far more often than it is written.

Runner names matter for a different reason: they are the one thing Zoomies puts
in somebody else's GitHub account. GitHub shows a runner's name in the runner
list, in a job's header, and in the "Set up job" step of every log — three places
a reader arrives at knowing nothing, and often the place they are looking
*because* something landed somewhere surprising. The name answers them there.

## The grammar

```
zoomies-<vcpu>vcpu[-<memory>gb]-<os>-<version>[-<arch>][-<suffix>]
```

| Part | Example | Notes |
| --- | --- | --- |
| `zoomies` | | Always. It is how you tell Zoomies' runners and containers from everything else on a host. |
| `<vcpu>vcpu` | `4vcpu` | A pool's per-runner share, or a host's total. A fraction rounds up. |
| `<memory>gb` | `32gb` | Only when there is a memory limit to state. A host name carries what the machine has; a pool name carries what one of its runners gets, and only when that is not the fleet's default — a figure every pool shares says nothing about any of them, which is the same reason `amd64` is left out. |
| `<os>-<version>` | `ubuntu-2404` | The distribution and its release, with the dots removed. `ubuntu-2404`, `debian-12`, `fedora-42`, `rocky-9`. |
| `<arch>` | `arm64` | Omitted for `amd64`, which is the default. Spelled out for everything else. |
| `<suffix>` | `tuck`, `biscuit-a3f9qz2m` | What tells two things of the same shape apart: a host's machine name, or a runner's kennel word and token. Pools have none: a pool *is* its shape. |

Every part after the prefix is optional and left out when it is not known. A
host that will not say what distribution it runs gets a name without one, rather
than a name that claims something nobody established.

**Any name is still legal.** Nothing rejects `gpu-builders`, and a fleet that
has always called its pool `linux-x64` keeps working exactly as it did. The
grammar is what Zoomies suggests, what `zoomies init` prints, and what an
unnamed host is given -- not a rule it enforces.

The name is capped at 64 characters, because a pool's name is what its labels
are built from and GitHub rejects a runner name longer than that.

Every pool name carries the brand whether or not it is in the grammar: a name
saved as `gpu` is stored as `zoomies-gpu`. That is
[`internal/store/brand.go`](https://github.com/eyupio/zoomies/blob/main/internal/store/brand.go),
and it is about the label a workflow in somebody else's repository has to write,
not about the grammar here.

## Runner names

A runner is named after the pool it belongs to, plus a discriminator that tells
it from its siblings — `zoomies-4vcpu-ubuntu-2404-biscuit-a3f9qz2m`:

| Part | Example | What it is for |
| --- | --- | --- |
| brand | `zoomies` | The only sign that a registration on GitHub is this fleet's. |
| shape | `4vcpu-ubuntu-2404` | The pool's, in the grammar above. |
| kennel word | `biscuit` | A handle two people can say to each other. |
| token | `a3f9qz2m` | Eight random characters, which is what makes the name unique. |

The **kennel word** is one of thirty-two cocker spaniel names — the same list
the pool editor offers, because a fleet whose pools are named from one list and
whose runners are named from another reads as two products. It is there to be
said out loud: "the biscuit one" is a thing two people on a call can both find.

The **token** is eight random characters, and it is what actually guarantees
uniqueness. GitHub requires a runner's name to be unique within the target, and
thirty-two words collide about as often as two people in a room of eight share
a birthday — fine for something you say, useless for something a registration
depends on.

A pool that has no shape to report — no resources, no platform — lends its own
name instead, so a pool created in the UI gets
`zoomies-biscuit-docker-linux-truffle-a3f9qz2m` rather than a runner called
nothing in particular.

When a name will not fit in 64 characters, the **shape** is what gives way, one
whole segment at a time. Neither the brand nor the token may be spent: without
the brand, the reaper cannot tell this registration from one somebody made by
hand; without the token, two runners truncated to the same name are one
registration fighting itself. Segments go whole rather than mid-word, because
`...ubuntu-24` claims a release that does not exist, where a name one segment
shorter only says less.

Because a runner's name is in the grammar, anything holding one can read it
back into the pool's shape — which is what makes it worth the characters rather
than just longer.

## Platforms

A pool's **platform** is the machine its runners need: a distribution, a
release, and an architecture. It does two things.

**It picks the runner image.** A pool that names `ubuntu` `24.04` boots
`ghcr.io/eyupio/zoomies-runner:ubuntu-2404` without anyone keeping the pool and
the image in step by hand. Naming an image explicitly still wins -- an operator
who has built their own is not second-guessed.

**It restricts placement.** The scheduler will not put an Ubuntu 24.04 runner on
a Debian host, or an arm64 runner on an amd64 one. Before this existed, a pool
said which backend it wanted and nothing about the machine underneath, so the
only thing that noticed a mismatch was the job that failed on it.

Every field is optional, and an empty field is a promise nobody made:

* A pool that names no platform goes anywhere, which is what every pool created
  before platforms existed does.
* A host that has not reported its distribution -- an agent older than this
  feature -- is not ruled out by one. Adding a platform to a pool narrows where
  its runners can go; it never widens it, and it never strands a host.

When no host matches, the Overview says so in those words, and names the machine
to add:

```
cannot scale zoomies-4vcpu-ubuntu-2404-arm64 0 -> 2: no host can take a new
docker runner (1 not Ubuntu 24.04, arm64); add a Ubuntu 24.04, arm64 host, or
change the pool's platform to one you have
```

## The runner image

`ghcr.io/eyupio/zoomies-runner` is published with one tag per operating system,
and the tag is the same `<os>-<version>` that appears in a pool name.

<!-- zoomies:catalogue-begin -->
| Tag | Base | Architectures | `zoomies-runner-full` |
| --- | --- | --- | --- |
| `ubuntu-2404` | `ubuntu:24.04@sha256:224a1869083a311ef3f13648a154ba79832fbef6364d31493642ca03082da254` | amd64, arm64 | yes |
| `ubuntu-2604` | `ubuntu:26.04@sha256:da6fc2be547864451aa253836dd926da33623312df4a9a243e35dc877c378a78` | amd64, arm64 | yes |
| `ubuntu-2204` | `ubuntu:22.04@sha256:829f6df217bcbae2b371026e81711d1a787c61b2967ad09d015063663ebafbf7` | amd64, arm64 | yes |
| `debian-13` | `debian:13-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a` | amd64, arm64 | — |
| `debian-12` | `debian:12-slim@sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171` | amd64, arm64 | — |
| `fedora-42` | `fedora:42@sha256:99e203b80b1c3d8f7e161ec10a68fd02b081ef83a3963553e513c82846b97814` | amd64, arm64 | — |
| `rocky-9` | `rockylinux/rockylinux:9@sha256:8101994123cf3d0a8fee517bee7f39e555c7d92bd2d9eb3303cc988a0eeed00f` | amd64, arm64 | — |
<!-- zoomies:catalogue-end -->

`:latest` points at `ubuntu-2404`, which is what a pool that names no platform
gets. Each release also publishes `<tag>-<version>` for pinning one operating
system without pinning the controller.

Every variant is built for both architectures, and each architecture on a
machine of its own kind: the amd64 images on an x86-64 builder, the arm64 images
on an arm64 one, and the two merged into a single multi-architecture tag, so a
host pulls the same reference whatever it runs on. The arm64 images used to be
cross-built under QEMU, whose aarch64 emulation segfaults inside `ldconfig` on
Ubuntu 22.04's glibc; that is why 22.04 was once amd64 only, and why nothing is
emulated now.

Every variant is published twice: as `zoomies-runner` and as
`zoomies-runner-docker`, the same image plus a Docker CLI, which a pool is
switched to when its `docker_mode` gives jobs a daemon. The Ubuntu variants are
published a third time, as `zoomies-runner-full`: the Docker image plus the
language toolchains the `setup-*` actions install, which a pool opts into by
naming it — the table above says which. All three are targets of one
`deploy/Dockerfile.runner`.

That file takes the base image and the package family (`apt` or `dnf`) as build
arguments; everything a distribution names differently lives in one
`deploy/runner-*.sh` script per concern -- the baseline tools, the build
toolchain, the GitHub CLI, the Docker client. actions/runner's own .NET
dependencies are left to the tarball's `installdependencies.sh`, which already
knows every distribution's package names for them; naming them by hand is what
made this image Ubuntu 24.04 and nothing else.

The `apt` variants share one script more: every package installation goes
through `deploy/runner-apt.sh`, which fetches the index again and retries when a
download fails. The Ubuntu and Debian archives are each several machines behind
one name, and a package the index has just started listing can 404 on whichever
of them the build happens to reach — a mirror mid-sync, not a missing package,
and not a reason to lose a whole image build. The `dnf` variants need no
equivalent: dnf reads a metalink, knows about several mirrors at once and fails
over between them itself.

Adding an operating system, or swapping one for its next release, is a row in
[`internal/naming/images.go`](https://github.com/eyupio/zoomies/blob/main/internal/naming/images.go)
and then `make generate`, which rewrites the table above, the Makefile's build
variants and both workflows' matrices from it. Go tests fail, naming that
command, if any of them is edited by hand instead.

There is no Alpine variant. actions/runner ships glibc binaries and .NET
dependencies that musl does not satisfy, so an Alpine image would build and then
fail at the first job.

### Baking in a toolchain

The image already carries what a build usually reaches for — a compiler, the
headers native extensions link against, `python3`, `node`, `git-lfs`, `shellcheck` and
the GitHub CLI — because a runner missing `cc` is a workflow that fails in the
middle of somebody's afternoon with an error about a missing compiler rather
than about this image.

When a whole fleet needs something beyond that, `EXTRA_PACKAGES` bakes it in
without forking the Dockerfile:

```sh
docker build -f deploy/Dockerfile.runner \
  --build-arg BASE=ubuntu:24.04 --build-arg OS_FAMILY=apt \
  --build-arg OS_ID=ubuntu --build-arg OS_VERSION=24.04 \
  --build-arg EXTRA_PACKAGES="ruby-dev libpq-dev" \
  -t ghcr.io/acme/our-runner:ubuntu-2404 .
```

The packages are named for the variant's own package manager, so an `apt`
variant wants `libpq-dev` and a `dnf` one `libpq-devel`. They are installed in
the image's last layer, so changing the list costs one package install rather
than rebuilding the toolchain above it — and an unknown name fails the build,
which is a much better place to find out than a workflow.

Then point a pool at it with `--image`, which overrides whatever its platform
would have selected.

### Building locally

```sh
make image-runner                          # the default variant
make image-runner RUNNER_VARIANT=debian-12 # one other
make images-runner                         # all of them
```

## What a name is built from

**Pools.** `zoomies init` suggests a first pool named for the host it just set
up, dividing the machine among the runners it will hold:

```sh
zoomies pools create --name zoomies-4vcpu-ubuntu-2404 \
  --labels linux,x64,zoomies,zoomies-4vcpu-ubuntu-2404 --backend docker --max 4 \
  --installation ins_... --cpus 4 --os ubuntu --os-version 24.04 --arch amd64
```

The pool advertises its canonical name as a label, the `zoomies` brand label
every pool answers to, and the kernel and architecture labels actions/runner
advertises anyway. Declaring the last two is what makes the scheduler refuse an
x64 job on an arm64 pool.

The **Pools** page's editor fills in the same name, following the answers as
they are given: choose Debian 12 and it says `zoomies-debian-12`, ask for four
CPUs and it says `zoomies-4vcpu-debian-12`. It used to lead with a kennel word
instead, which left an operator who met both the pool editor and `zoomies init` with
two conventions for one thing.

A new pool opens at the fleet's default size (`runners.default_cpus`,
`runners.default_memory_mb`), and that size is left out of the name: every pool
here gets it, so saying it would tell a workflow author nothing. Ask for more
than the usual and the name says so.

The spaniel is still there, for the two cases the shape cannot cover on its own:
a pool created before anything about it is known — the first one, before a host
has connected — is `zoomies-biscuit`, and a second pool of a shape the fleet
already has becomes `zoomies-4vcpu-ubuntu-2404-truffle` rather than colliding.
The dice ask for one outright, for an operator who wants a handle whether or not
the name needs one.

Any name is still legal, and the platform rather than the name is what picks the
image and restricts placement.

**Hosts.** An agent that joins without `--name` is named for what it is:
`zoomies-16vcpu-32gb-ubuntu-2404-build01`. The size is what the agent may
actually use, which is the cgroup's share when the agent runs in a container --
so the controller in a two-core container does not claim the host's sixty-four.

**Runners.** The pool's shape, a kennel word and a token, as
[above](#runner-names). The `zoomies-` prefix is also how the uninstaller and
the agent's orphan sweep know which registrations and containers are Zoomies' to
reap, so it must not appear in front of anything else.
