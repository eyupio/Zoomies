---
description: >-
  The design contract for the Zoomies web UI: design tokens, the fixed status
  colours, the app shell budget, and the accessibility rules every page keeps.
---

# Zoomies UI guidelines

The Zoomies web UI is meant to be left open on a second monitor all day. That is
the bar every decision below is measured against: it should be calm when nothing
is happening, unmistakable when something is wrong, and never make the operator
click *refresh*.

This document is the contract. If you are adding a component, take the tokens
from here rather than inventing values.

---

## 1. Design tokens

All tokens live in exactly one place: `web/src/lib/styles/tokens.css`, declared
as CSS custom properties on `:root` and overridden under `[data-theme="dark"]`.
Tailwind v4 consumes them through `@theme` so utility classes and hand-written
CSS resolve to the same values. **Never write a raw hex, px or ms value in a
component.**

### 1.1 Colour

The palette is the brand palette: a cool near-black spine with Runner Blue as
the interactive accent. See [brand.md](brand.md) for the identity itself and
the two decisions -- darkening Runner Blue for text, and spending Fast Cyan on
the busy state -- that turn it into a usable UI palette.

Every pairing below has been measured; the numbers are real, not aspirational.

#### Brand constants

Fixed by the identity, available as `--z-brand-*` and unchanged between themes.

| Token | Value |
| --- | --- |
| `--z-brand-black` | `#080808` |
| `--z-brand-white` | `#FFFFFF` |
| `--z-brand-cool-grey` | `#B9BCC2` |
| `--z-brand-mid-grey` | `#666A73` |
| `--z-brand-runner-blue` | `#2F80ED` |
| `--z-brand-fast-cyan` | `#22D3EE` |

#### Neutrals

| Token | Light | Dark | Use |
| --- | --- | --- | --- |
| `--z-bg` | `#F6F7F9` | `#0A0B0D` | Page background |
| `--z-surface` | `#FFFFFF` | `#131519` | Cards, panels, table body |
| `--z-surface-raised` | `#FFFFFF` | `#1B1E23` | Popovers, dialogs, dropdowns |
| `--z-surface-sunken` | `#EEF0F3` | `#050607` | Wells, code blocks, table headers |
| `--z-border` | `#E2E5EA` | `#262A31` | Default hairlines |
| `--z-border-strong` | `#C7CBD3` | `#363B44` | Inputs, focused containers |
| `--z-text` | `#0B0C0E` | `#F4F5F7` | Primary text |
| `--z-text-muted` | `#5B6069` | `#B9BCC2` | Secondary text, labels |
| `--z-text-subtle` | `#6E737C` | `#868B94` | Timestamps, placeholders |

Primary text is 18:1 in both themes. Muted is 5.9:1 light and 10.3:1 dark.
Subtle -- the weakest text in the product -- is 4.45:1 light and 5.75:1 dark, so
even the timestamps clear AA.

#### Brand accent

| Token | Light | Dark | Use |
| --- | --- | --- | --- |
| `--z-accent` | `#1A63D8` | `#78B4FF` | Primary buttons, links, active nav, focus |
| `--z-accent-hover` | `#154FB0` | `#9BC9FF` | Hover |
| `--z-accent-subtle` | `#E8F0FD` | `#10203A` | Selected rows, badge backgrounds |
| `--z-accent-contrast` | `#FFFFFF` | `#08131F` | Text on an accent fill |

5.13:1 and 9.16:1 against their backgrounds; the text on an accent fill is
5.5:1 light and 8.7:1 dark. Full-strength Runner Blue (`--z-brand-blue`) is
available for illustrative fills, where the bar is 3:1.

#### Status

Runner and job states get their own hue. The mapping is fixed -- do not use
these colours for anything else, because operators learn them.

| State | Token | Light | Dark |
| --- | --- | --- | --- |
| idle / healthy / success | `--z-idle` | `#177245` | `#5FD68C` |
| **busy / running** | `--z-busy` | `#0E7490` | `#3ED8F0` |
| provisioning / registering / pending | `--z-pending` | `#8A5200` | `#F0B34C` |
| draining / cordoned / paused | `--z-draining` | `#5E636D` | `#A6ABB5` |
| failed / error / destructive | `--z-danger` | `#B32218` | `#FF9089` |
| removed / neutral | `--z-neutral` | `#6E737C` | `#868B94` |

Busy is the Fast Cyan family: the brand asks for the accent to be used
sparingly, and *this runner is executing a job right now* is the single most
valuable "look here" signal on the page.

Each has a `-subtle` companion for badge and chart-fill backgrounds, and
`--z-danger` has a `-contrast` for text on a destructive fill. Every status is
**also** carried by a shape: a filled dot for busy, a hollow dot for idle, a
dashed ring for provisioning, a slash for draining, a triangle for failed.
Colour alone never encodes state -- that is both an accessibility requirement
and a practical one for an operator glancing at a sparkline from across the
room.

#### Charts

`--z-chart-1` … `--z-chart-6` are a categorical ramp derived from the status
hues, ordered for maximum adjacent separation. Sparklines use `--z-busy` for
running work and `--z-pending` for queue depth, because those are the two
quantities an operator is actually watching.

#### The mark

`--z-mark-chip` is the Zoomies Black ground the monochrome mark sits on. The
mark is white line art drawn for a dark surface, so the UI puts it on a small
black chip rather than inverting it -- one asset, correct in both themes,
legible at 24px. See [brand.md](brand.md), which lists every place the signed-in
product carries the mark, the name or the descriptor.

### 1.2 Type

Two faces, both self-hosted as woff2 so an air-gapped install has no network
dependency and no third-party font request:

* **Inter Variable** — UI. `--z-font-sans`. Geist and `system-ui` are the
  brand's sanctioned alternatives and sit in the fallback stack.
* **JetBrains Mono** — logs, IDs, labels, durations, anything monospaced.
  `--z-font-mono`.

The wordmark in the logo artwork is custom-rendered and is not Inter. Do not
try to set it in type.

Both declare a full system fallback stack so the first paint is never blank.

| Token | Size / line-height | Weight | Use |
| --- | --- | --- | --- |
| `--z-text-2xs` | 11px / 16px | 500 | Table micro-labels, badge text |
| `--z-text-xs` | 12px / 18px | 450 | Metadata, timestamps |
| `--z-text-sm` | 13px / 20px | 450 | **Default table and body text** |
| `--z-text-base` | 14px / 22px | 450 | Form controls, prose |
| `--z-text-lg` | 16px / 24px | 550 | Card titles |
| `--z-text-xl` | 20px / 28px | 600 | Page titles |
| `--z-text-2xl` | 28px / 34px | 640 | Overview metrics |
| `--z-text-3xl` | 36px / 42px | 660 | Hero numbers |

The base size is 13px, not 16px. This is a dense operational tool; 13px Inter at
450 weight on the warm background stays comfortably legible while fitting a
useful number of runners on screen. Prose in docs and empty states steps up to
14px.

Numerals in tables use `font-variant-numeric: tabular-nums` so columns align.
IDs use `--z-font-mono` at `--z-text-xs`.

**Form controls are the one exception, and only on a phone.** Below 768px,
`Input`, `Textarea` and `Select` step up to `--z-text-lg` (16px). Mobile Safari
zooms the whole viewport whenever a focused control is under 16px, and the
viewport meta deliberately sets no `maximum-scale`, so without this every field
tap on the first-run screens jumped a 360px page to roughly 410px of effective
width and ran the card off both edges — once per field. Control height comes
from the space scale, so nothing reflows; only the glyphs grow.

**A validation error takes the hint's place; it never appears beside it.**
`Field` renders one message row, because an error rendered *in addition* to the
hint adds a line and moves everything below it — including the submit button,
out from under a pointer already on its way down, so the click lands on whatever
takes its place and the operator experiences a button that does nothing. A field
on a form worth clicking therefore carries a hint, so the row is occupied before
an error needs it. Anything a field must say *alongside* an error goes in
`notice`, which is rendered independently, is `aria-live="polite"`, and is what
the caps-lock warning uses.

### 1.3 Spacing

A 4px base with a restricted scale. Only these values exist:

`--z-space-1` 4 · `-2` 8 · `-3` 12 · `-4` 16 · `-5` 20 · `-6` 24 · `-8` 32 ·
`-10` 40 · `-12` 48 · `-16` 64

Page gutters are `--z-space-6`, card padding `--z-space-5`, table cell padding
`--z-space-3` vertical / `--z-space-4` horizontal, form field gap
`--z-space-4`.

### 1.4 Radii

`--z-radius-sm` 4 (badges, inputs) · `--z-radius-md` 8 (buttons, cards) ·
`--z-radius-lg` 12 (dialogs, panels) · `--z-radius-full` 9999 (dots, pills).

### 1.5 Elevation

Shadows are warm-tinted and very restrained; in dark mode elevation is carried
mostly by surface colour, not shadow.

| Token | Light | Dark |
| --- | --- | --- |
| `--z-shadow-sm` | `0 1px 2px rgb(41 34 24 / .06)` | `0 1px 2px rgb(0 0 0 / .5)` |
| `--z-shadow-md` | `0 4px 12px -2px rgb(41 34 24 / .10)` | `0 4px 12px -2px rgb(0 0 0 / .6)` |
| `--z-shadow-lg` | `0 16px 40px -8px rgb(41 34 24 / .16)` | `0 16px 40px -8px rgb(0 0 0 / .7)` |

### 1.6 Motion

| Token | Value | Use |
| --- | --- | --- |
| `--z-motion-fast` | 120ms | Hover, focus, colour change |
| `--z-motion-base` | 200ms | Panels, dropdowns, row insert |
| `--z-motion-slow` | 320ms | Dialogs, drawer, route transition |
| `--z-ease` | `cubic-bezier(.2,.8,.2,1)` | Everything |

State changes animate the *colour and the dot*, never layout — a table that
reflows while you are reading it is worse than one that does not animate at all.
Everything inside `@media (prefers-reduced-motion: reduce)` collapses to `1ms`.

### 1.7 Theme

Three states: `light`, `dark`, `system`. `system` is the default and sets no
attribute, so `prefers-color-scheme` decides. An explicit choice writes
`data-theme` on `<html>` and persists to `localStorage` under `zoomies.theme`.
The choice is applied by a tiny inline script before first paint so there is no
flash.

---

## 2. Information architecture

Persistent left navigation, collapsible to icons only (persisted). Order is
fixed, because muscle memory is the point:

1. **Overview** — fleet health at a glance
2. **Pools** — what runners to make
3. **Runners** — what runners exist right now
4. **Jobs** — what has run
5. **Hosts** — where runners can go
6. **Installations** — GitHub App connections
7. **Migrate** — move repositories off GitHub's runners onto this fleet
8. **Audit** — who did what
9. **Settings** — users, tokens, appearance, danger zone

The navigation is headed by the mark, the wordmark and the descriptor, and every
page ends in a hairline footer carrying the mark, the name, the running version,
a link to the docs and the credit *Developed by EyUp.io* — so a signed-in
screenshot says which product and which build it came from, and who makes it,
without anyone having to open Settings. The credit opens in a new tab: it is the
one link in the shell that leaves the product. On a phone the navigation moves to
the bottom edge and loses its masthead, so the mark appears in the top bar
instead.

A **command palette** on `Cmd/Ctrl+K` jumps to any page, any pool, any runner by
ID or name, and runs quick actions (drain a runner, cordon a host, create a
pool). It is the fastest path to everything and is discoverable from a hint in
the top bar.

### Overview

The one page that has to earn the second monitor.

* **Four metric tiles**: queued jobs, running jobs, live runners, median queue
  wait. Each carries a sparkline of the last hour.
* **Per-pool utilisation bars** — busy / live, with the pool's min and max marked
  so an operator can see a pool pinned at its ceiling.
* **Recent scaling activity** — a reverse-chronological list of decisions in the
  scheduler's own words: *"scaled `linux-x64` 2 → 4: 3 jobs queued > 30s"*.
* **Problems summary** — one line saying how many things need a person, worst
  severity first, with a *Review* button that opens the problems drawer.
  **When everything is fine it is a single quiet line, not an empty box and not
  a green celebration.** It is a line rather than a list on purpose: the panels
  above are what the page is for, and a configuration warning somebody chose
  deliberately must not push the pools and the running jobs below the fold
  every day.

### The problems drawer

Reachable from the count in the top bar on every page, from the Overview's
*Review*, and from the command palette. It holds the list itself — unhealthy
hosts, failed registrations, webhook delivery failures, unmatched queued jobs,
and every dangerous configuration setting the validator flagged — worst first,
each entry saying what is true, why it matters and what to change, with a link
to the pool, host, runner or installation it is about.

Every entry can be **dismissed**, which is a per-operator preference in
`localStorage` and never fleet state: `GET /api/v1/problems`, `zoomies status`
and any alerting rule still see everything. Two rules stop a dismissal from
hiding a real fault:

* it is forgotten the moment the controller stops reporting that problem, so
  the same fault happening again is news again; and
* it only covers the severity it was made at, so a warning that becomes an
  error comes back.

Dismissed entries stay listed at the bottom of the drawer, dated, and can be
restored one at a time or all at once.

---

## 3. Component inventory

Every component lives in `web/src/lib/components/` and takes its values from
tokens. Svelte 5 runes (`$state`, `$derived`, `$props`, `$effect`) throughout —
no stores except for genuinely global state.

### Primitives

| Component | Notes |
| --- | --- |
| `Button` | variants: `primary`, `secondary`, `ghost`, `danger`; sizes `sm`, `md`; `loading` swaps the label for a spinner without changing width |
| `IconButton` | square, always has an `aria-label` |
| `Input`, `Textarea`, `Select`, `Switch`, `Checkbox`, `RadioGroup` | all support `error` and `hint`; the error is announced with `aria-describedby` |
| `Field` | label + control + hint + error; the only way form controls are laid out |
| `Badge` | status pill: colour **and** shape from the state map |
| `StatusDot` | the shape half of the state encoding, reusable inline |
| `Tooltip` | on hover *and* focus; never the only place information lives |
| `Dialog` | focus trap, restores focus on close, `Esc` closes, backdrop click closes only non-destructive dialogs |
| `Drawer` | right-hand detail panel; same focus rules |
| `DropdownMenu` | roving tabindex, type-ahead |
| `Tabs` | `aria-controls`/`aria-selected`, arrow-key navigation |
| `Toast` | bottom-right, `aria-live="polite"` (`assertive` for errors), auto-dismiss except on error |
| `Skeleton` | **the only loading affordance for content.** Spinners are for in-flight *actions* only |
| `EmptyState` | icon + one sentence of what this is + the action that fills it; a `visual` snippet replaces the icon disc where artwork is wanted, as the not-found page does with the mark |
| `CopyButton` | wraps any ID; announces "copied" to screen readers |
| `RelativeTime` | "4m ago", absolute ISO timestamp in the tooltip, updates itself |
| `Duration` | humanised, tabular numerals |
| `Sparkline` | inline SVG, no chart library, `role="img"` with a text summary |
| `UtilisationBar` | busy/live with min and max ticks |
| `ConfirmDialog` | destructive confirmation that **names the thing** ("Delete pool `linux-x64`? 3 runners will be drained.") and requires typing the name for anything irreversible |

### Composites

| Component | Notes |
| --- | --- |
| `DataGrid` | TanStack Table core + our own markup. Server-side pagination, sorting and filtering; column show/hide persisted per grid; sticky header; row selection with a bulk action bar; full keyboard navigation (`↑ ↓` rows, `Enter` opens, `Space` selects, `Shift+↑/↓` range) |
| `FilterBar` | chips for active filters, each individually removable, plus a clear-all |
| `PageHeader` | title, subtitle, breadcrumb, primary action |
| `MetricTile` | number, label, delta, sparkline |
| `LogViewer` | xterm.js with search, follow/pause, wrap toggle, download, and a line counter |
| `Timeline` | runner state history with durations between transitions |
| `Wizard` | the pool creation flow: target → labels → backend → scaling → review |

### The log viewer

100k lines without jank is a hard requirement. xterm.js with the canvas/WebGL
addon handles the rendering; the constraints on our side are:

* a bounded scrollback (`scrollback: 100000`) — beyond that the oldest lines go;
* writes are **batched on `requestAnimationFrame`**, never once per SSE frame;
* `fit` is debounced and only runs on real size changes;
* search uses the search addon, not a DOM scan;
* "follow" is a mode, not a scroll position — leaving the bottom turns it off,
  and a floating "jump to latest" button turns it back on.

---

## 4. Interaction patterns

### Real time

There is no refresh button. A single SSE connection to `/api/v1/events` feeds a
client-side cache; each page subscribes to the event kinds it cares about. On
reconnect the client sends `Last-Event-ID` and the server replays what it
buffered, then the page does one reconciling fetch. The connection state is
visible in the top bar: a quiet dot when live, an explicit "reconnecting…" when
not — never a silent stall.

The one page that asks rather than listens is *Add a host*: while it waits for
the new machine it fetches its own join token every few seconds, because
credentials are deliberately not on the stream. Even there the stream is the
fast path — a host's first frame is the cue to ask at once — and the page says
in words that it is waiting and how.

### Optimistic updates

User actions apply locally first and roll back on failure with a toast that says
what failed and why. Draining a runner flips its badge immediately; if the API
returns 409 the badge flips back and the toast explains. Background outcomes
(the runner actually reaching `removed`) arrive over SSE and need no toast.

### Forms

Validation rules come from the same source as the API's, generated into
`web/src/lib/api/schema.ts` from the OpenAPI document. Errors appear inline on
blur and again on submit; the first invalid field receives focus. Defaults are
filled in from the host's detected capabilities, so pool creation is mostly
pressing *Next*.

### Destructive actions

Named, counted, and consequential. "Delete" says what will be destroyed and how
many things it affects. Anything that removes runners from GitHub says so
explicitly. Irreversible actions require typing the resource name.

### Empty states

Never a blank table. Each says what the thing is and what to do next, with the
action inline:

* Pools: *"No pools yet. A pool decides what labels your runners answer to and
  how many of them exist."* + **Create a pool**
* Runners: *"No runners right now. That is normal when nothing is queued —
  runners are created on demand."*
* Jobs: *"No jobs recorded yet. Zoomies records a job the first time GitHub
  tells it about one."* + a link to check webhook delivery.

### Keyboard

Everything reachable, in a sensible order, with a visible focus ring
(`2px` `--z-accent`, `2px` offset — never removed). `Cmd/Ctrl+K` palette,
`g` then `o/p/r/j/h/i/a/s` to jump between sections, `/` focuses the current
page's search, `?` opens the shortcut sheet, `Esc` closes the topmost layer.

### Responsive

Three breakpoints only: `< 768px` (phone — read-only monitoring: metric tiles
stack, grids become cards, mutating actions are hidden rather than broken),
`768–1180px` (tablet — nav collapses to icons), `> 1180px` (full).

---

## 5. Performance budget

| Budget | Limit |
| --- | --- |
| App shell (JS + CSS, gzipped) | **< 200 KB** |
| Route chunk | < 80 KB gzipped |
| First contentful paint on a warm cache | < 400 ms |
| Interaction to next paint | < 200 ms |

How it is kept:

* route-level code splitting; xterm.js and TanStack Table load only on the
  routes that use them;
* no chart library — sparklines and bars are hand-written SVG;
* Lucide icons imported individually so tree-shaking works;
* fonts declared by hand as four Latin faces with `font-display: swap`,
  rather than @fontsource's own stylesheets, which pull in Cyrillic, Greek and
  Vietnamese in both woff and woff2;
* the SSE cache is a plain `Map`, not a reactive deep-proxy over thousands of
  rows.

`npm run build` prints the gzipped shell size and **fails** if it exceeds the
budget, so this stays true.

---

## 6. Accessibility checklist

Every PR that touches the UI should be able to answer yes to all of these:

- [ ] Operable with the keyboard alone, including grids and dialogs
- [ ] Focus visible everywhere, and moved into and restored out of dialogs
- [ ] Custom widgets carry the right `role`, and state via `aria-*`
- [ ] Colour is never the sole carrier of meaning
- [ ] Text meets WCAG AA in **both** themes (4.5:1 body, 3:1 large and UI)
- [ ] Live regions announce async outcomes once, not on every keystroke
- [ ] Reduced-motion honoured
- [ ] Zoom to 200% without loss of function

---

## 7. Adding something new

1. Look for an existing component first. The inventory above is deliberately
   small.
2. If a token you need does not exist, add it to `tokens.css` in **both**
   themes, check the contrast, and record it here.
3. If it is a status, add it to the state map (colour **and** shape).
4. Write the empty, loading and error states before the happy path. They are
   most of what an operator actually sees.
