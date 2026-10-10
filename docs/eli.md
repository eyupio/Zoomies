# Eli conversations and PR repairs

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
and a relevant question. Eli sees this snapshot, not live fleet access. After an
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
