---
icon: material/dog-side
title: Eli, the assistant
description: >-
  Eli is the assistant built into Zoomies: a chat in the corner of every page that
  can read your fleet through a model you choose, and cannot change anything.
---

# Eli, the assistant

Eli is the assistant built into Zoomies. The name stands for **Extremely Lively
Intelligence**, which is what a dog is for a few minutes after a bath, and what a
fleet is meant to be. Eli is a chat in the corner of every page. You ask in plain
words, and Eli answers about Zoomies, about GitHub Actions and, if you allow it,
about your own fleet.

Eli talks to a model **you** choose and pay for: one on your own machine such as
Ollama, or a hosted one such as Ollama Cloud, OpenCode Go, Anthropic or OpenAI.
Nothing is sent anywhere until somebody asks Eli something.

## Setting it up

Under **Settings, Assistant**, add a provider, choose its model from the list the
provider gives, test it, and make it the default. Eli then appears in the corner of
every page for administrators. Press <kbd>E</kbd> from anywhere that is not a box
to type in, or press **Ask Eli**. <kbd>Esc</kbd> puts the panel away.

Until there is an enabled default provider there is no Eli to ask, and the
button is not shown.

## What Eli can see

By default, **nothing about your fleet**. Eli is told it cannot see it, and answers
what it knows about Zoomies and GitHub Actions, and asks you to paste anything
else. That is the right setting for a hosted model you have not decided to trust
with job and repository names.

To let Eli look, turn on **Let Eli read this fleet through this provider** in that
provider's settings. It is a switch **per provider**, off for every provider
including those you already have, because what is read is sent to that provider.
For a model on this machine or a private network it stays there; for a hosted one
it leaves your network. Changing the switch is recorded in the audit log.

With it on, Eli can look at what an agent of yours could read through the
[MCP server](ai-context.md): the fleet at a glance, problems, jobs and their
history, runners, pools, hosts and their health, label advice, a runner's log while
the runner exists, and [Kennel Club](kennel-club.md) findings. When it does, the
panel says what it looked at, under the answer.

Eli does not get the tools that change the fleet, and it does not get the ones that
read a repository's source. It reads as the person asking, through the same routes
and the same permissions, so it can see nothing that person could not.

## What is sent

When Eli looks at the fleet, what the tools return is sent to the provider with
your question: runner, host, pool, job, workflow, repository and branch names,
statuses, counts, and the end of a runner's log if Eli asks for it. A tool's answer
is cut at 16 KiB. There is no redaction yet, so the switch is the control: turn it
on for a provider only if that provider may be shown this.

Job and branch names, commit messages and log lines are written by whoever can open
a pull request against your repositories. Eli is told that such text is data and
never instructions, and every tool result is marked as the fleet's data. Eli has
no tool that changes anything, and answers are drawn as text, so the worst a hostile
name can do is mislead an answer: check what you act on.

## Limits

* One question may take at most six rounds of looking and twelve tool calls.
* A conversation is at most 40 messages, 8 KiB each and 32 KiB in all.
* The conversation lives in your browser and is gone when you reload. The
  controller keeps nothing between questions.
* Only administrators can ask. Every chat is written to the audit log as
  `assistant.chat` with the provider, the model, whether the fleet could be read
  and the tools used, and never with what was said.

## If it is not there

* **No button.** You need to be an administrator, and an enabled provider has to be
  the default. Check **Settings, Assistant**.
* **"Eli cannot see this fleet through ..."** The provider's fleet switch is off.
* **A provider error.** The text says what the provider answered; **Test** on its
  card sends one short prompt and says which part failed. A model that does not
  support tools answers without them, and says it could not look.
* **A private address is refused.** `assistant.allow_private_provider` lets a
  provider live on this machine or your network, and `assistant.local_only` refuses
  any that does not.

The demo model that ships with the demo fleet can be asked about "the fleet": it
reads the fleet and repeats what it found, so the whole loop can be seen with no
account and no key.
