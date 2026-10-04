# CLAUDE.md

Scoped guidance for the store, loaded in addition to the root [CLAUDE.md](../../CLAUDE.md). It appends to that file and never overrides it.

## Invariants

* **One writer.** `store.Store` funnels writes through a single connection
  behind a mutex, with a separate pooled reader in WAL mode. Do not add a second
  writer; `database is locked` is designed out of this codebase.
* **The runner state machine is enforced in the store**, not the caller.
  `provisioning → registering → idle ⇄ busy → draining → removed`, plus
  `failed`. Go through `store.TransitionRunner`; an agent must not be able to
  report a nonsensical state and corrupt fleet accounting.
* **Sentinel errors** from the store: `ErrNotFound`, `ErrConflict`,
  `ErrInvalidTransition`, `ErrInvalidArtifact` for an assistant-written note
  that breaks a note's rules, and `ErrJoinTokenUsed` / `ErrJoinTokenExpired` for
  the two ways a join token that exists is still refused. Match with
  `errors.Is`. Refusals the auth service makes for a reason the caller can
  act on are `auth.ErrInvalidInput`; the API answers those with a 422 and
  everything else with a 500 and a request ID.
* **IDs are prefixed** (`pool_`, `run_`, `job_`, `usr_`…) via `store.NewID`, so a
  pasted ID is self-describing in a log line or bug report. Add new prefixes to
  `internal/store/ids.go`.
