# CLAUDE.md

Scoped guidance for the UI, loaded in addition to the root [CLAUDE.md](../CLAUDE.md). It appends to that file and never overrides it.

## The UI

`web/` is Svelte 5 (runes), Tailwind v4, Vite, TypeScript, built straight into
`internal/api/webdist` and embedded.

- **Never write a raw colour in a component, and never write a raw value that
  already has a token.** All design tokens live in
  `web/src/lib/styles/tokens.css`. A colour written by hand only works in one
  theme, so that half is absolute; the rest is a rule about repetition, and a
  value that appears twice belongs in the token file. Media query widths, a
  one-off measure in a component's own layout, and the log viewer's xterm
  bridge are the documented exceptions.
  [docs/ui-guidelines.md](../docs/ui-guidelines.md) is the contract, and UI
  changes should keep it true.
- Status colours are a fixed mapping (idle, busy, pending, draining, danger,
  neutral). Operators learn them; do not reuse them for anything else.
- No state-management library (runes are it), no client-side router
  (`web/src/lib/router.ts` is ours), no charting library (sparklines and bars are
  inline SVG). These are deliberate — see `docs/dependencies.md`.
- Nothing is reachable from the UI that is not reachable from the REST API. If a
  page needs data, it comes from a documented route in
  [docs/api-surface.md](docs/api-surface.md).
- Playwright specs in `web/tests/` run against the real binary, including
  accessibility and mobile passes.
