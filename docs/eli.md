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
Ollama, or a hosted one such as Ollama Cloud, OpenCode Zen, OpenCode Go, Anthropic or OpenAI, or your own
Claude, ChatGPT or GitHub Copilot plan.
Nothing is sent anywhere until somebody asks Eli something.

## Setting it up

Under **Settings, Assistant**, add a provider, choose its model from the list the
provider gives, test it, and make it the default. Eli then appears in the corner of
every page for administrators. Press <kbd>E</kbd> from anywhere that is not a box
to type in, or press **Ask Eli**. <kbd>Esc</kbd> puts the panel away.

Until there is an enabled default provider there is no Eli to ask, and the
button is not shown.

### Using your own subscription

A provider can also be your own plan, used through the vendor's own program on the
controller's machine: **Claude** through Claude Code, **ChatGPT** through Codex, or
**GitHub Copilot** through its command line tool. Zoomies runs the program and asks
it the question. You sign in to it once, as the user the controller runs as
(`claude auth login`, `codex login` or `copilot login`); Zoomies never sees the login,
holds no key and has no address to call.

| | Claude Code | Codex | GitHub Copilot |
| --- | --- | --- | --- |
| Sends the question to | Anthropic | OpenAI | GitHub |
| Tools of its own | Every one off | Read-only sandbox | None pre-approved |
| Model | `sonnet`, `opus`, `haiku`, chosen from a list | Left empty, Codex chooses, or named | Left empty, Copilot chooses, or named |
| **Test** spends | Nothing | Nothing | One request of the plan |
| Refuses an API key | Yes | Yes | Not detectable |

Things to know before you add one:

* **It is yours alone.** A plan is one person's, and the vendors' terms do not allow
  it to serve other people, so only the person who added the provider can use it,
  test it, change it or make it the default. Another administrator sees it on the
  list, marked with your name, and can remove it, but cannot use it. For them Eli
  answers through a provider they are allowed to use, if there is one.
* **Read the vendor's terms yourself.** Whether your plan allows this use is your
  decision, and Zoomies cannot check it for you.
* **Eli cannot read the fleet through it.** Eli's own fleet tools are never offered
  to these programs, and the fleet switch is not shown for them. They are run from
  an empty temporary directory with an environment that carries no `ZOOMIES_*`
  setting and no key or token. What you type to Eli does leave this machine, for the
  vendor.
* **Codex and Copilot have tools of their own, and cannot be run without them.**
  Claude Code is run with every one of its tools off, in its safe mode. Codex has no
  such mode: it is run in its read-only sandbox, so it can read files the controller's
  user can read, and it is asked in the prompt not to. Copilot is run with nobody to
  ask and none of its approval options passed, so a tool that needs permission does
  not run. For both, whatever their tools did is ignored: it is not shown and Eli
  does nothing on the strength of it, but ignoring what a tool did is not the same as
  the tool not having done it. Only the person who added the provider can ask it
  anything, and nothing about your fleet is put in the question, so what could lead
  one to read a file is the person's own words.
* **Copilot's question is a command line argument.** Anyone who can list processes on
  the controller's machine can read it while the answer is being made.
* **They follow the programs' releases.** The options Zoomies uses are the
  programs' own. If a release stops accepting one, **Test** says to update the
  program.
* **A plan, not an API key.** For Claude Code and Codex, **Test** refuses a program
  signed in with an API key and says so; use the Anthropic or OpenAI provider for
  that. Copilot cannot be told apart, but its token variables are never passed on.
* **Not while Local models only is on.** The question goes to the vendor.
* **Host installs only.** They need the program installed on the machine the
  controller runs on. The controller image is distroless and has none, so a compose
  or image deployment cannot use them.
* **Two at a time.** At most two of these programs run at once, so a busy
  conversation cannot start a crowd of them.

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

## Personal conversations and PR repairs

Eli uses your configured model provider. In Settings, Assistant, add a personal
provider, test it and make it your default. Its key is encrypted at rest and
never returned to the browser. Each account has its own providers and default.
Administrators manage installation providers separately. Existing providers
remain installation-owned after an upgrade; they are not assigned to a user.

## A chat that fits your work

Open Ask Eli from any fleet page. Choose Compact, Default, Expanded or Full
screen with one click, move the panel to either side, or drag its resize handle.
The panel remembers its size preset and side in this browser. Minimise it while
working and reopen the same conversation. Conversation text stays in memory and
is cleared on sign-out; it is not saved to browser storage.

Ask Eli actions on host, runner and problem views send the displayed context
and a relevant question. Eli sees this snapshot, and can use read-only fleet tools when enabled for its provider. After an
answer, follow-on prompts use the conversation's topic and skip questions
already asked. Extend `web/src/lib/assistant/prompts.ts` with a narrative topic;
new page integrations pass an `EliContext` to `AskEli.svelte`.

## Hybrid provider ownership

| Request | Provider |
| --- | --- |
| Chat and contextual Ask Eli | Signed-in user's personal provider |
| PR repair requested in Zoomies or a GitHub comment | Requester's personal default provider |
| Unattended repair of a failed PR job | Installation provider selected by the repository policy |

There is no fallback from a missing personal provider to an installation key
when authentication is enabled. The authentication-disabled development demo
uses its built-in installation provider because it has no personal accounts.
Provider address restrictions and the Local models only setting apply to both
scopes. Removing a user removes their personal providers. Deleting an
installation provider removes repository policies that reference it.

## Set up PR repairs

1. Add an installation provider under Settings, Assistant, Installation providers.
2. Open Repository repair policies. Choose a repository, its GitHub installation
   and provider, then enable repairs. Automatic repairs are a separate opt-in.
3. Set the attempt budget, from 1 to 50 per rolling 24 hours. Explicit and
   automatic attempts share this repository budget. Each personal account also
   has a limit of 10 explicit attempts per rolling 24 hours.
4. Under Link a GitHub account, verify that the person owns the account, choose
   their Zoomies user and a repository where they have write permission, and
   link it. Zoomies resolves and records the numeric GitHub ID. A login rename
   never assigns another person's key to a requester.
5. Each requester confirms **Allow this GitHub account to request repairs using
   my provider** on their own Assistant settings page, and chooses an enabled
   personal default provider. Administrators cannot confirm on their behalf.
   Changing a link clears this confirmation.

The GitHub App needs **Contents: write**, **Pull requests: write**,
**Issues: write**, **Actions: read**, **Checks: read** and **Commit statuses: read**
for the relevant repositories. Subscribe to **Issue comment** alongside
**Workflow job**. Existing installations may need to approve new App
permissions. Editing `.github/workflows` also requires the appropriate GitHub
App workflow permission, and a separate opt-in on the Zoomies policy.

On a pull request, a linked maintainer can comment:

```text
@eli fix the failing test
@zoomies fix this PR issue
```

Commands must begin a line outside a quote or code block. Only new comments by
human users trigger a repair. Edited comments, ordinary issues, bots and
unlinked users do not. Both triggers use the existing signed webhook endpoint.
The App's actual GitHub account may have a different name; command recognition
uses the comment text and does not require GitHub mention notification routing.

## What a repair does

The durable queue deduplicates webhook retries and charges attempts at
admission. The worker reads the PR's pinned commit, changed text files, repository
inventory and bounded failed-job evidence. It asks the selected model for a
strict JSON plan, with at most six model turns and eight edits. Source is capped
at 128 KiB, individual files at 32 KiB, and replacement content at 64 KiB.
Recognisable provider/GitHub tokens and private-key headers are redacted before
model use. These filters cannot identify every possible secret, so use a
provider permitted to process your repository and job logs.

Eli does not execute repository code or run local tests. It cannot edit symlinks,
submodules, credential paths or binary files. Workflow files are protected unless
explicitly enabled. Fork PRs, closed PRs and default-branch PRs are refused.
An infrastructure failure may produce a diagnosis rather than a code change.

Before publication, Eli rechecks repository policy, provider availability and
requester access. The commit has the original PR head as its parent, preserves
file modes and uses a non-force branch update. A concurrent push stops publication.
Eli never merges the PR or bypasses branch protection.

The bot posts progress and watches check runs and commit statuses on its commit.
It reports checks passed, checks failed, a newer PR head, or unverified after an
hour without complete results. Reported checks are not a guarantee that every
branch-protection requirement is satisfied. Automatic repair will not start
another attempt on an Eli commit. A maintainer can request another attempt.

Queued work survives restart. A repair interrupted during publication has an
uncertain outcome, so it is marked interrupted and never replayed automatically.
Inspect the PR before asking again. Settings, Assistant shows repair history;
ordinary users see their own requests and administrators see the whole history.

Model-generated fixes still need review. The limits constrain cost and the
editing surface; they do not prove a patch correct. Provider errors, quota
exhaustion or unsupported model output stop a repair without trying another
user's credentials.
