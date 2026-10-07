---
icon: material/shield-account-outline
title: Privacy
description: >-
  What the Zoomies website and the Zoomies software do and do not collect.
  There is no telemetry, no analytics and no account; the one request the
  software makes that is not about your fleet is a daily release check you can
  turn off.
---

# Privacy

Zoomies is software you run yourself, so most of the privacy question is
answered by where it runs: on your machines, with its data in a SQLite file you
own. This page says what is left over — what the website at zoomies.sh does,
and what the software sends anywhere you did not point it.

## The website

zoomies.sh is a static site published through GitHub Pages from the
[documentation in the repository](https://github.com/eyupio/zoomies/tree/main/docs).

- **No analytics and no advertising.** The site carries no analytics script, no
  tag manager and no tracking pixel, and it does not set cookies of its own.
- **Self-hosted fonts and scripts.** The fonts and the one script the site
  ships come from zoomies.sh itself, so reading a page does not tell a font
  provider or a CDN that you did.
- **Search runs in your browser.** The search index is downloaded once and
  queried locally; what you type is not sent anywhere.
- **Your colour-scheme choice stays on your device.** The light and dark toggle
  remembers your choice in your browser's local storage. It never leaves it.
- **GitHub sees ordinary web-server data.** As the host, GitHub receives the
  address and browser details any web server receives, under
  [GitHub's privacy statement](https://docs.github.com/en/site-policy/privacy-policies/github-general-privacy-statement).
  This project does not receive those logs.
- **What the build looks up.** When the site is built, it asks GitHub for the
  latest release, star count and fork count of the repository, and tells
  participating search engines (through IndexNow) which pages changed. Neither
  involves a visitor.

The installer is served from the same place: `curl -fsSL https://zoomies.sh/install.sh | sh`
fetches a script from zoomies.sh, which then downloads a release from GitHub.
Read it first if you would rather, it is the file `install.sh` in the
repository.

## The software

A Zoomies controller, and the agents that join it, have **no telemetry**. Nothing
about your fleet, your repositories, your jobs or your hostnames is sent to the
project or to anyone else because you ran it.

What it does talk to, because you configured it to:

- **GitHub.** It uses your GitHub App to learn about queued jobs and to register
  runners, which is the whole job. Webhook payloads and API responses are
  GitHub's own data about your own repositories.
- **Whatever you add.** An identity provider for sign-in, a Proxmox server, an
  S3-compatible bucket for backups, a tunnel — each is a service you chose, and
  each is configured in your own controller.

The one request the software makes that is **not** about your fleet is the
update check: once a day (`updates.check_interval`, default `24h`), the
controller asks github.com which release of Zoomies is current. The request
carries the version you run in its `User-Agent` (`zoomies/<version>`, which on a
development build includes its `main-sha-*` identity), and GitHub sees your
address as it would for any request. Nothing else about your installation is
sent, and nothing is downloaded. Every other call to GitHub's API carries the
same `User-Agent`. Set the interval to `0` and the update check never asks. See
[`updates.check_interval`](configuration.md#updatescheck_interval-knowing-the-controller-is-behind).

### What your controller keeps

Its database holds what it needs to run a fleet: pools, hosts, runners, jobs,
the audit log and the accounts that sign in. The credentials it holds are encrypted
or stored hashed; [Security](security.md#3-credentials-and-how-they-are-stored) lists
what is stored where and how. All of it stays on the machine you installed on,
and your backups are yours to place.

The controller serves its web UI with a session cookie that signs you in. That
cookie is `HttpOnly` and set by your controller, not by this project.

### The README badge

The [migration wizard](migration.md) offers to add a one-line badge to a
repository's README, and shows it in the diff before it opens the pull request.
The badge is an image served from zoomies.sh, so a browser or proxy displaying
that README asks zoomies.sh for it. It is a checkbox in the wizard, and
declining it adds nothing.

## AI Context

[AI Context](ai-context.md) is optional and off until you set it up. It
publishes a prepared copy of a repository's source to a branch of that same
repository, and lets an assistant you connect read it through your controller.
It reads and writes only what you authorise, inside your own GitHub account and
your own controller.

## Contact

For a privacy question about the website or the project, open an issue on
[GitHub](https://github.com/eyupio/zoomies/issues). A security problem goes
through the [security policy](https://github.com/eyupio/zoomies/blob/main/SECURITY.md)
instead.

If this page ever stops being true, that is a bug. Tell us.
