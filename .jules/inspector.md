# Inspector journal

## 2026-09-18 - `go test -race ./...` can spuriously time out `internal/controller` in this sandbox
**Finding:** A full `go test -race -count=1 ./...` run failed with `internal/controller` hitting the
10-minute per-package timeout, dumping hundreds of goroutines mid-migration. It reproduced once more
at a longer explicit `-timeout`, which first looked like a real deadlock worth chasing.
**Learning:** `internal/controller`'s tests each open a fresh SQLite store and run the full migration
set (`newHarness`), and modernc.org/sqlite (pure-Go) under the race detector is dramatically slower at
that than the interpreter overhead alone suggests. The same package passed in ~51s without `-race`, and
a plain `go test -count=1 ./...` (no race) passed cleanly on two separate full runs in this sandbox,
including immediately after a change that could not plausibly affect `internal/controller`. This reads
as sandbox CPU/resource contention under race instrumentation, not a deadlock or a regression.
**Prevention:** Before treating a `-race` timeout in `internal/controller` (or any package that opens
many fresh migrated stores) as a real bug, rerun the same package without `-race` and rerun the full
suite without `-race`. Only chase it as a deadlock if it reproduces there too. Don't burn a session's
budget re-running `-race` at ever-longer timeouts on a single package before doing that cheaper check.

## 2026-09-27 - Second consecutive run with no unambiguous defect; where the budget went
**Finding:** A full pass -- `go test -count=1 ./...` (all packages green), frontend `npm run test:unit`
(200/200), `npm run lint` (prettier + eslint clean), `npm run check` (svelte-check, 0 errors/warnings) --
plus close manual reading of the areas most likely to hide a real bug (DataGrid's abort/retry/live-refresh
machinery, the scheduler's busy/stillRunning drain guards, the usage-cost rollup's rounding, the Proxmox
provider's VMID allocation and node placement, the Usage/DateRange preset-vs-custom range math, form
double-submit guards across every `async function submit/save`, and ConfirmDialog coverage on every
destructive button) turned up nothing that wasn't already correct or a documented deliberate choice. This
is the second consecutive Inspector run on this repo to end this way (see PR #320, which found only a
stale doc comment across an equally thorough sweep of scheduler/controller/store/migrate/gateway/UI).
**Learning:** This repo's own review discipline (the exhaustive "why" comments, the matching test for
almost every invariant named in a comment) means a defect that survives to `main` is genuinely rare, not
that there is nothing left to check. Skimming files top-to-bottom for "smells" mostly re-confirms already-
correct code and burns budget without raising the odds of a real find.
**Prevention:** On this repo, spend the audit budget on *recently merged* work first (`git log --stat`
since the last Inspector PR) rather than a fresh top-to-bottom sweep -- a defect is far more likely to be
in code that hasn't yet had a review pass than in the long-settled core. If a full sweep still finds
nothing unambiguous, stop and skip the PR rather than forcing a low-value fix (a copy tweak, a comment
change) to have something to show; the instructions explicitly allow ending with no PR, and a previous run
already did exactly that for the same reason.
