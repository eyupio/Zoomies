---
icon: material/file-document-outline
title: Terms
description: >-
  The terms Zoomies and its website are offered under: the AGPL-3.0 licence for
  the software, no warranty, no service to depend on, and where to report a
  problem.
---

# Terms

Zoomies is free software, not a service. There is no account to create with this
project, nothing to subscribe to and no hosted product whose availability we
promise, so these terms are short.

## The software

Zoomies is licensed under the
[GNU Affero General Public License, version 3](https://github.com/eyupio/zoomies/blob/main/LICENSE)
(AGPL-3.0). That licence is the whole agreement for the software, and it says
what you may do: run it, read it, change it, and share it, on the conditions it
sets out.

In plain words, and without replacing the licence: running Zoomies for your own
organisation, changed or not, asks nothing of you. If you change it and let
other people use your changed version over a network, you must offer them the
source of what you run. The [FAQ](faq.md#can-i-run-zoomies-as-a-service-for-other-people)
has the longer answer.

## No warranty

The licence provides the software **as is**, without warranty of any kind, and
limits liability for what happens when you run it. That is not boilerplate
here. Zoomies starts containers that run your repositories' code on your
machines, and the project is plain about what has and has not been tested:

- [What is qualified](index.md#what-is-qualified) says which parts have run on
  real hosts and which have only been built.
- [Security](security.md) says what a self-hosted runner exposes and what each
  setting that weakens the defaults costs.
- [Is it safe to run self-hosted runners on public repositories?](faq.md#is-it-safe-to-run-self-hosted-runners-on-public-repositories)
  is answered there, and the answer is a careful one.

You are responsible for the machines you run it on, the repositories you point
it at, and the GitHub App you give it. Read those pages before you put anything
precious on it.

## No support contract

Zoomies is developed in the open by [EyUp.io](https://eyup.io) and its contributors.
Help comes through
[GitHub issues](https://github.com/eyupio/zoomies/issues), and there is no
service level, response time or commitment to fix a particular problem by a
particular date. A vulnerability is the exception to the way you ask, not to
the lack of a contract: report it through the
[security policy](https://github.com/eyupio/zoomies/blob/main/SECURITY.md).

## The website

The documentation at zoomies.sh is published under the terms of the repository
it is built from. Links to other sites, including GitHub, the runner services
and the projects [compared](alternatives/index.md) here, are provided for
convenience; those sites are theirs, and their terms apply to them.

The comparisons are written from each project's public documentation, dated, and
linked so you can check them. They are not benchmarks. If one is out of date or
wrong about a project, please [open an issue](https://github.com/eyupio/zoomies/issues)
and it will be corrected.

## Names and logos

"Zoomies", the paw-and-swish mark and the artwork under
[`docs/brand`](brand.md) identify this project. Describing the software, linking
to it and showing the README badge on a repository that really does run its CI
on Zoomies are all welcome. Presenting something else as Zoomies, or as endorsed
by this project, is not.

## Privacy

What the site and the software do and do not collect is on the
[privacy page](privacy.md).

## Changes

These terms change through the repository like everything else, and the page's
history is its change log. If you are relying on a particular version, the
repository holds every one.
