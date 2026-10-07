# Zoomies positioning copy

Draft copy for directory submissions. Nothing here has been submitted. Each variant opens differently on purpose: AI engines down-weight duplicate text, and each audience wants a different first sentence. Do not paste one variant everywhere.

Rules the copy follows:

- Claims are limited to what the docs back. No "faster", "more secure" or "production ready". The competitive review says not to publish those without evidence.
- Linux is what is built and tested. The Windows agent is not yet qualified, so Windows is not in the copy. Say so if a form asks about platforms.
- Say "free and open source (AGPL-3.0)", not "free" alone. The licence matters to a reader deciding whether to run it.
- Disclose authorship wherever you post as a person (Reddit, Lobsters, Hacker News, Indie Hackers).

Links to use: home `https://zoomies.sh`, source `https://github.com/eyupio/zoomies`, quick start `https://zoomies.sh/quickstart/`, alternatives `https://zoomies.sh/alternatives/`, install `curl -fsSL https://zoomies.sh/install.sh | sh`.

Tags to draw from: github actions, self-hosted runners, ci/cd, devops, ephemeral runners, autoscaling, go, sqlite, docker, podman, proxmox, open source, self-hosted.

---

## Dev tools (DevHunt, SourceForge, Slashdot, Stackshare, awesome-lists, Dev.to, Hashnode)

**Tagline:** Self-hosted GitHub Actions runners without Kubernetes.

**Short (60 characters):** Fleet controller for self-hosted GitHub Actions runners

**Long (about 150 words):**

> Zoomies is a self-hosted GitHub Actions runner controller written in Go. It is one static binary with SQLite for state and a Svelte web UI embedded in it: no Kubernetes and no database server.
>
> It watches for queued jobs, starts a fresh runner container for each one, registers it with a single-use just-in-time configuration, and destroys it when the job finishes. Pools scale to zero by default. Runners can use Docker, Podman or no container runtime, and hosts can be anywhere an agent can dial out from, so a machine behind NAT needs no inbound rule.
>
> The UI shows every pool, runner and job live and says in plain words why the scheduler did what it did. A migration wizard rewrites `runs-on` across your repositories and opens one pull request for each, after showing the diff.
>
> Free and open source under the AGPL-3.0. Install with one command.

---

## Alternative framing (AlternativeTo, SaaSHub, Slant)

**Tagline:** Self-hosted alternative to ARC and GARM for ordinary hosts.

**Short:** Free self-hosted GitHub Actions runner fleet controller

**Long (about 150 words):**

> Zoomies is a free, open-source alternative to actions-runner-controller (ARC), GARM and hosted runner services for teams that have machines rather than a Kubernetes cluster.
>
> Where ARC needs a cluster, Zoomies needs a Linux host with a container runtime. Where a hosted service bills by the minute and runs your jobs on its own machines, Zoomies runs them on yours, with no software fee. Where GARM is built around provider plugins, Zoomies is built around hosts you already own, with a guided migration off GitHub's runners.
>
> Key features:
> - Ephemeral runner per job, scale to zero
> - Live web UI with plain-language scheduler reasons and an audit log
> - Migration wizard: one pull request per repository
> - Private and home-lab hosts join by dialling out
> - Proxmox VE provider, Prometheus metrics, MCP server for AI assistants
>
> Compared honestly, with where each alternative is better: zoomies.sh/alternatives/. AGPL-3.0.

**Alternatives to list on AlternativeTo / SaaSHub:** actions-runner-controller, GARM, Cirun, terraform-aws-github-runner, Blacksmith, WarpBuild, RunsOn, GitHub-hosted runners.

---

## Startup and launch directories (Fazier, Uneed, Indie Hackers, Product Hunt if used)

**Tagline:** Give your GitHub Actions runners the Zoomies.

**Short:** Self-hosted GitHub Actions runners, ready in minutes

**Long (about 150 words):**

> Zoomies is the easiest way to run GitHub Actions on machines you already own. One command installs it, a live web UI shows every runner and job, and each job gets a fresh runner that is destroyed when it finishes.
>
> It is built for teams who pay for CI minutes, or babysit a handful of long-lived runners, and do not want to run Kubernetes to fix that. Add a spare server, a home-lab box or a Proxmox cluster and it becomes runner capacity; a migration wizard moves your repositories over one pull request at a time.
>
> It is a single Go binary with SQLite, so there is nothing else to operate. Free and open source under the AGPL-3.0, with no hosted service to depend on.
>
> Just looking? `curl -fsSL https://zoomies.sh/install.sh | sh -s -- --demo` runs a demo fleet on your machine and removes itself when you press Ctrl-C.

**First comment (Product Hunt / Indie Hackers):**

> Hi, I'm one of the people building Zoomies. We wanted ephemeral self-hosted runners without standing up Kubernetes, and a UI that explains what the scheduler is doing instead of leaving you to read logs. Honest limits: it is Linux-first, the Windows agent is not yet qualified, and there is no support contract. I'd most like feedback on the migration wizard and on the quick start; where did it go wrong for you?

---

## Agent / MCP registries (only once a public package or remote URL exists)

**Tagline:** Ask Claude about your self-hosted CI runner fleet.

**Short:** MCP server for a Zoomies GitHub Actions runner fleet

**Long (about 100 words):**

> This MCP server lets an assistant read a Zoomies runner fleet: fleet status, problems, hosts, pools, runners, jobs and the end of a runner's log, plus the controller's own explanation of why a job is where it is. Operators can additionally let it re-run a failed job or drain an idle runner.
>
> It acts as the person who connected it, at the role they choose (viewer or operator), and sign-in happens through the controller's own page: no pasted tokens. Log output, branch names and commit text come back marked as untrusted data.
>
> Requires a Zoomies controller you run yourself. AGPL-3.0.

---

## Show HN

**Title:** Show HN: Zoomies – self-hosted GitHub Actions runners without Kubernetes

**First comment (technical angle, written as the author):**

> I work on Zoomies, a controller that gives each GitHub Actions job a fresh runner container and tears it down afterwards. One static Go binary, SQLite, a Svelte UI embedded with go:embed.
>
> A few design decisions I'd like feedback on:
>
> - Agents only connect outbound (long-poll for tasks, POST results), so a host behind NAT needs no inbound rule. That forced log streaming to be inverted: the controller queues a stream task and the agent opens a chunked POST that is relayed to the browser.
> - Webhook deliveries are at-least-once and can arrive out of order, so the jobs upsert refuses to move a job backwards through its lifecycle.
> - The scheduler is a pure function from a snapshot to a plan, with no clock, database or network, which makes scaling behaviour testable and gives every decision a human-readable reason.
>
> Limits: Linux-first, the Windows agent is not yet qualified on real hardware, and it is not tested against GitHub Enterprise Server. AGPL-3.0. Compared with ARC, GARM and the hosted services, with where each is better: https://zoomies.sh/alternatives/

---

## Community replies (Reddit, Lobsters, forums)

Not a post to paste. Reply only where someone is genuinely asking, say you work on the project, and answer the question first. The 90/10 rule applies.

> Disclosure: I work on Zoomies, so weigh that. For a few VMs and no cluster it's one binary plus SQLite, each job gets a fresh container, and it scales to zero. If you already run Kubernetes, ARC is probably the better fit, and I wrote down where each one wins here: https://zoomies.sh/alternatives/

---

## Listicle outreach email

> Subject: Self-hosted GitHub Actions runners roundup
>
> Hi [name], I read your post on [title]. It's useful. In case it's relevant for the next update: Zoomies is a free, open-source (AGPL-3.0) runner controller for people with machines rather than a Kubernetes cluster. It's one Go binary, ephemeral runners, a live UI and a migration wizard. There's an honest comparison with ARC, GARM and the hosted services at https://zoomies.sh/alternatives/ that says where each is better. Happy to answer questions or correct anything I've got wrong about the others. No worries if it isn't a fit.
>
> [your name], I work on Zoomies

---

## Per-directory checklist

1. Re-run `dig +short <domain>` and open the root URL; skip it if it does not resolve.
2. Copy the variant for that directory's type. Change the opening sentence if two directories sit side by side.
3. Get approval before creating an account, entering personal details or pressing Submit.
4. After it goes live, check the link: `curl -sIL <listing> | grep -i rel=`.
5. Log the row in `tracker.csv`.
