# 0007: The repository page hosts AI Context and adds no switch to it

**Status**: accepted. Taken on 7 October 2026 on the owner's instruction, in
answer to what enabling AI Context from a repository page should do.

## Context

The owner expected Kennel Club to let a repository's AI Context be enabled and
seen from the repository itself. AI Context, as built, works per repository but
not in the way a switch does:

* There is no enabled flag. A repository is opted in by creating a draft for it
  in the setup wizard and getting the setup pull request merged; its state
  (draft, preparing, awaiting merge, ready, stale, removed) is worked out from
  the draft, the setup and the verification of what was published.
* Removing it is a reviewed pull request, and it **deletes** the repository's
  snapshots, notes and access grants. Nothing pauses it.
* It does not depend on `kennel.enabled`, and nothing about it is fleet-wide or
  per installation. Who may change it is an administrator or the owner of that
  installation; reading its source is a separate membership that neither
  administrators nor owners get by being what they are.
* Its card is inline in a 656-line page and has no component of its own. There
  is no event stream for it, so its state arrives by refresh.

The Kennel repository already carries the GitHub repository ID and the
installation, which is the pair AI Context finds its record by.

## Decision

The repository page gets an **AI Context** tab that hosts the AI Context card,
extracted into one component that the AI Context page renders too. It calls the
same endpoints and applies the same gates. For a repository with no AI Context
the tab offers to set it up, which opens the existing wizard for that
repository; removing it stays the reviewed removal pull request. **Add no pause
and no new switch.**

## Consequences

The extraction is a refactor of a page that has its own specification,
`ai-context.spec.ts`, and that specification passing with its assertions
unchanged is the proof nothing was lost.

A person who is neither an administrator nor the owner of the installation is
answered 404 for a repository that has AI Context and for one that has not, so
the tab cannot tell them apart. For such a person it says what it cannot know
instead of claiming the repository is not set up, and it offers no action they
could not take.

The tab shows what the record carries: state, freshness, the last verified
commit, the branch, where the output goes and the setup pull request. File counts
and sizes come from the snapshot and are for people who have been given source
access, so they are not shown on a page everyone with a viewer role can open.
The diagnosis of a failed run is attached only when one repository is read, so
the tab reads it again after a recheck.

A non-destructive pause is a reasonable thing to want, and it is a new AI Context
feature with a migration, controller changes and documentation. It is not this
decision. If it is wanted it gets its own record.
