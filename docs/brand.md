# Zoomies brand

<img src="brand/logo-master-dark.png" alt="The Zoomies logo: a black-and-white cocker spaniel curling through a circular motion path, above the Zoomies wordmark and the line SELF-HOSTED GIT RUNNERS" width="360">

The identity is a fast-moving black-and-white cocker spaniel curling through a
circular motion path, paired with the Zoomies wordmark. A dog doing zoomies in
circles is runners executing jobs quickly — that is the whole idea, and it is
why the mark is never rotated: the circular movement already communicates speed.

Personality: fast, playful, capable, technical, friendly.

**[Zoomies_Brand_Guide.pdf](brand/Zoomies_Brand_Guide.pdf) is the authority.**
This page records what the product actually does with it, and
[ui-guidelines.md](ui-guidelines.md) is where the derived design tokens live.

## Colour

The core palette is monochrome. The accent exists for the product UI only — the
logo stays monochrome, always.

| Name | Value | Where it is used |
| --- | --- | --- |
| Zoomies Black | `#080808` | The ground the mark sits on, in both themes |
| White | `#FFFFFF` | Surfaces in the light theme, and the knockout mark |
| Cool Grey | `#B9BCC2` | Secondary text in the dark theme |
| Mid Grey | `#666A73` | Secondary text in the light theme |
| Runner Blue | `#2F80ED` | The interactive accent — see the note below |
| Fast Cyan | `#22D3EE` | The *busy* runner state, and nothing else |

They are available in `brand/brand-tokens.json` and as CSS custom properties
(`--z-brand-black`, `--z-brand-runner-blue`, …) at the top of
`web/src/lib/styles/tokens.css`.

### Two decisions worth explaining

**Runner Blue is darkened for interactive use.** `#2F80ED` is 3.87:1 on white.
That is fine for a large shape but fails WCAG AA for 13px text, and it fails for
white text on a button. So `--z-accent` is a darkened Runner Blue (`#1A63D8`,
5.5:1) and the pure brand blue is kept for illustrative fills where 3:1 is the
bar. The dark theme lifts it the other way, to `#78B4FF` at 9.2:1.

**Fast Cyan is spent on one thing.** The brand guide says to use the accent
sparingly. In an operations dashboard the single most valuable "look here"
signal is *this runner is executing a job right now*, so that is what Fast Cyan
marks, and nothing else does.

## Logo files

Everything in `docs/brand/` is derived from the approved master. The
full-resolution originals live in the brand pack.

| File | Use |
| --- | --- |
| `brand/logo-master-dark.png` | The approved master, on Zoomies Black |
| `brand/logo-white-transparent.png` | White knockout, for any dark or coloured ground |
| `brand/mark-white-transparent.png` | The dog and circle alone — avatars, favicons, compact navigation |
| `brand/wordmark-dark.png`, `brand/wordmark-white-transparent.png` | The wordmark alone |
| `brand/social-card.png` | 1200×630, for the repository's social preview |

The product's own copies live in `web/public/` and are the sizes the app
actually serves: `favicon.ico` (16/32/48), `favicon-16.png`, `favicon-32.png`,
`apple-touch-icon.png`, `icon-192.png`, `icon-512.png`, and
`brand/mark-white.png`, `brand/wordmark-white.png`, `brand/logo-white.png`, and
`brand/app-logo.png` — a 512px square of the mark on Zoomies Black, which is
what an operator uploads as their GitHub App's avatar. An App manifest cannot
carry a logo, so the connect flow hands them this file and a link to the page
that takes it; without it the App wears GitHub's grey default and signs every
"Set up job" line in the organisation's logs anonymously.

## Using the mark in the product

The mark is monochrome line art with white outlines, drawn to sit on a dark
ground. Inverting it for a light theme washes it out, and at 24px in a sidebar
the detail disappears either way.

So the UI does the thing the brand guide sanctions — the white knockout over a
dark surface — and draws it on a small Zoomies Black chip
(`--z-mark-chip`) with a `--z-radius-md` corner. One asset, correct in both
themes, legible at 24px, and it keeps the circular shape intact.

The full lock-up appears on the sign-in, first-run and boot screens, and on the
one that says the controller cannot be reached -- the four screens that are
nothing but the lock-up, where there is room for it at its 240px minimum.

Once somebody is signed in, the identity is carried in five quieter places:

| Where | What |
| --- | --- |
| The navigation masthead | Mark and wordmark, over the descriptor; the mark alone when collapsed |
| The top bar, on a phone | The mark alone, because the masthead is not on screen there |
| The foot of every page | The mark, the name, the running version and the descriptor, in a hairline |
| The command palette | The mark and the name, in the footer beside the key hints |
| Settings → About | The mark at 44px beside the name and the descriptor |

The descriptor is set in Inter -- small, uppercase, letter-spaced -- rather than
cropped out of the wordmark artwork, whose own descriptor line is drawn for
240px and is a smudge at sidebar width.

## Clear space and minimum sizes

* Clear space: roughly the height of the lowercase **o** in *Zoomies*. Nothing
  crowds the dog circle.
* Full logo: 240px wide minimum.
* Mark: 32px minimum, 64px or more where there is room.
* Favicon: the supplied ICO, or the 32×32 PNG.

## Do and do not

**Do**

* Use the dark master on dark surfaces.
* Use the white knockout over dark photography or a coloured UI.
* Use the mark alone for avatars, favicons, app icons and compact navigation.
* Keep the circular motion shape intact.

**Do not**

* Stretch or skew the artwork.
* Recolour any part of the dog.
* Add a shadow, gradient or glow.
* Put the dark master on a busy background.
* Rotate the mark.

## Typography

**Inter** for the interface, with Geist and `system-ui` as the sanctioned
alternatives, and **JetBrains Mono** for logs, identifiers and durations. Both
are self-hosted as woff2 so an air-gapped install makes no third-party font
request. The wordmark in the artwork is custom-rendered and is not Inter — do
not try to set it in type.

## Naming

| | |
| --- | --- |
| Product | Zoomies |
| Descriptor | Self-hosted Git runners |
| CLI | `zoomies` |
| Service | `zoomies` (controller), `zoomies-agent` (agent) |
| Config directory | `/etc/zoomies`, or `.zoomies/` for a per-user install |
| Container images | `ghcr.io/eyupio/zoomies`, `ghcr.io/eyupio/zoomies-runner` |
| Runner names | `zoomies-k3f9qz2m` — the brand and eight random characters |
| Pool labels | `zoomies-linux-x64`, `zoomies-gpu`; every pool also answers to `zoomies` |
| Migration branch | `zoomies/migrate-runners-<timestamp>` |

### On somebody else's screen

Two of those names are read far more often outside Zoomies than inside it: the
runner name, which GitHub prints in its runner list and in every job's "Set up
job" step, and the pool label, which has to be written into `runs-on` in every
workflow in the organisation. Both are short, both start with the product name,
and neither carries anything a reader would have to decode. The runner name used
to carry the pool as well — `zoomies-linux-x64-a3f9q` — which made the brand the
part GitHub truncated.

`internal/store/brand.go` is the one place these are spelled out;
`web/src/lib/brand.ts` is the browser's copy of it.

## For print, signage or merchandise

The SVG files in the original pack are raster wrappers, not true vector
redraws. Commission a proper redraw from the approved master rather than
enlarging them.
