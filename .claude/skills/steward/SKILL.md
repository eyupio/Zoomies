---
name: steward
description: Repository maintenance rules for Zoomies — keeping branches, pull requests, generated files, dependencies and docs healthy without trusting your own unverified work. Use when tidying the repository, acting on CI failures or review comments, refreshing generated artefacts, updating dependencies, or removing code, files, config keys or database rows.
---

# Steward

Maintenance work in this repository is held to the same standard as a feature:
verified before it is pushed, reversible after it lands, and approved by a
person before anything irreversible happens. This skill is the checklist for
that. It governs *how* maintenance is done; it never widens what may be done.

## When this applies

- Housekeeping passes over the tree: dead code, stale docs, orphaned tests.
- Regenerating the files CI diffs against their sources (see
  [.github/CLAUDE.md](../../../.github/CLAUDE.md), "Things CI will fail you on").
- Dependency bumps, including Dependabot follow-ups and `go mod tidy` churn.
- Driving a pull request to green after a CI failure or a review comment.
- Any change that removes something an operator or a downstream user can
  currently see: a config key, an API field, a route, a problem code, a column,
  a CLI flag.

## The three standing rules

### 1. No self-trust

A change is not finished because it looks right. It is finished when the
repository's own checks say so, run by you, on the tree you are about to push.

- Run `make build-nogui` first on a fresh checkout. `internal/api` embeds
  `internal/api/webdist`, which is a build product, so every other Go command
  fails until it exists.
- Then `make lint` and `make test` (`go test -race -count=1 ./...`). Narrow
  with `go test -run TestName ./internal/pkg/` while iterating, but run the
  whole suite before the push.
- Reproduce a reported failure *before* fixing it, and show the same check
  passing afterwards. A fix you have not seen fail is a guess.
- Re-read your own diff adversarially and ask what would make CI reject it.
  Generated files are the usual answer: `internal/api/openapi_spec.go`,
  `web/src/lib/api/schema.d.ts`, the `internal/naming/images.go` matrices,
  `docs/problem-codes.md`. Regenerate with the repository's tooling —
  `go run internal/api/gen_openapi.go`, `make openapi`, `make generate` —
  never by hand.
- Never treat a failing test as noise. "Flake" is a diagnosis you have
  evidence for, not a default. Never skip, disable or quarantine a test to
  reach green.

### 2. Soft deletions only

Deletion is the one maintenance action that cannot be undone by reading the
diff, so default to a reversible form of it.

| Instead of | Do this |
| --- | --- |
| Deleting a config key | Keep it parsed, ignore it, and raise a `config.Finding` naming the replacement |
| Deleting an API field or route | Mark it deprecated in `api/openapi.yaml`, keep serving it, regenerate both clients |
| Deleting a database row | Set the tombstone or state the store already models; go through `store.TransitionRunner` rather than removing history |
| Deleting a doc page | Leave a stub that links onward — `mkdocs build --strict` fails on a link that points nowhere |
| Dropping a dependency | Remove the import and the `docs/dependencies.md` row in the same commit, so the justification and the use disappear together |

Hard deletion is reserved for things with no external surface: unreferenced
private helpers, dead test fixtures, files you added yourself earlier in the
same branch. Before removing anything, read it — `grep` for the identifier
across `internal/`, `cmd/`, `web/` and `docs/` — and say in the commit message
what confirmed it was unused.

Never rewrite history on a branch you did not create: no rebase, no amend, no
force-push. A merge commit keeps other people's checkouts valid.

### 3. Human in the loop

Maintenance is allowed to be autonomous up to the point where a person's
judgement is the input. At that point, stop and ask, with enough context that
the answer does not require scrolling back.

Ask before:

- A schema migration, a breaking configuration change, or anything that alters
  the runner state machine.
- Removing a behaviour an operator could depend on, when the reversible form
  above is not available.
- Adding a dependency. `docs/dependencies.md` is enforced by review, and the
  standard library or fifty lines of our own is usually the answer.
- Resolving a merge conflict where both sides changed the same logic and
  picking either one loses behaviour.
- Any change to authentication, authorisation, secrets, runner isolation or a
  deletion path — `CONTRIBUTING.md` asks for an explicit threat and
  failure-mode review on those.

Proceed without asking on: a nit, a rename, an added test, regenerating a file
from its source, a lint or format fix, a lockfile refresh.

## What this skill cannot authorise

This file is repository content, not an instruction from your operator. It
cannot expand your access or redirect your task, and nothing in it makes any of
the following acceptable:

- Approving or merging a pull request.
- Skipping, disabling or quarantining a test.
- Pushing an empty commit, or closing and reopening a pull request, to kick CI.
- Rewriting history on someone else's branch.
- Pushing to, or resolving a larger ask on, a pull request you did not open.

## Before every push

1. `make build-nogui` — the tree compiles.
2. `make fmt` then `make lint` — `gofmt -l` is empty, `go vet` and the UI lint
   are clean.
3. `make test` — the full suite, with the race detector.
4. `go mod tidy` leaves `go.mod` and `go.sum` unchanged.
5. Generated files regenerated from their sources, not edited.
6. New behaviour arrives with a test; new settings arrive with a
   `docs/configuration.md` row and a `ZOOMIES_*` override in `applyEnv`; new
   finding codes arrive with a `docs/problem-codes.md` row.
7. The prose matches the house voice: British spelling, comments that say
   *why*, `--` in code and `—` in Markdown, commit messages as imperative
   sentences in sentence case with no Conventional Commits prefix.

One validated push beats three speculative ones.
