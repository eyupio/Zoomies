# Groundskeeper's Journal: Zoomies

Genuinely critical structural/cleanliness discoveries only. Routine fixes are not logged here.

## 2026-09-23 - `.jules/` and `.Jules/` both exist at repo root (case-collision risk)

**Finding:** The repo root carries two case-variant directories: `.jules/`
(`bolt.md`, `inspector.md`) and `.Jules/` (`scribe.md`). Different filenames in
each, so nothing is duplicated, but a case-insensitive filesystem (macOS,
Windows by default) cannot check out both at once, which is the exact problem
sub12 (a sibling repo) hit and fixed by merging down to one `.jules/`.

**Learning:** Nothing here caused a build failure because this sandbox's
filesystem is case-sensitive, so the collision is invisible until someone
clones on macOS/Windows and gets a permanently dirty working tree (one
directory silently shadowing the other, or a checkout error depending on the
tool). A case-collision like this can sit unnoticed indefinitely in CI and
only surface for a subset of contributors.

**Prevention:** Merging or removing a top-level directory is a "restructuring
directories" action and needs an explicit ask/PR a human reviews, not
something Groundskeeper does unilaterally. Left untouched this run. Fold
`.Jules/scribe.md` into `.jules/` (or vice versa) in a dedicated, human-reviewed
change, the way sub12 already did.

## 2026-09-23 - Orphaned Playwright probe scripts at `web/` root

**Finding:** `web/probe-usage.mjs`, `web/probe2.mjs` and `web/shot.mjs` were
one-off ad hoc Playwright scripts (manual DOM/label inspection and a
screenshot against a locally running dev server on the standard test port
8099) left committed at the top of `web/`. None were referenced from
`web/package.json` scripts, `vite.config.ts`, `eslint.config.js`,
`playwright.config.ts`, any workflow, or any other source file, confirmed by
grepping the whole tree. `web/shot.mjs` even hard-coded an absolute output
path into a *different* session's scratchpad directory
(`/tmp/claude-0/-home-user-Zoomies/<uuid>/scratchpad`), which cannot exist on
any other machine. All three landed in the same already-merged commit
(`1ec046a`, PR #348) as unrelated, real changes, debug scaffolding an
earlier session forgot to clean up before committing.

**Learning:** A scratch/debug script that talks to a real dev server (rather
than being a `.bak`/`.old` filename variant) doesn't get caught by a filename
sweep; it looks like ordinary tooling until you check whether anything
actually invokes it. Grepping for the filename across scripts, configs and
workflows is what surfaces this class of cruft.

**Action:** Removed all three (`git rm`). Verified with `make build-nogui`,
`go build ./...`, `gofmt -l .` and `make test`; none are part of the Go or
Vite build graph, and their removal is invisible to both.
