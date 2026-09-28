---
icon: material/paw
title: "Brand: the Zoomies logo, colours and rules"
description: >-
  The Zoomies identity: the cocker spaniel mark, the wordmark, the colour
  tokens, and the rules for placing them.
---

# Zoomies brand

<img src="brand/logo-master-dark.png" alt="The Zoomies logo: a black-and-white cocker spaniel curling through a circular motion path, above the Zoomies wordmark and the line SELF-HOSTED GIT RUNNERS" width="360">

The identity is a fast-moving black-and-white cocker spaniel curling through a
circular motion path, paired with the Zoomies wordmark. A dog doing zoomies in
circles is runners executing jobs quickly — that is the whole idea, and it is
why the mark is never rotated: the circular movement already communicates speed.

Personality: fast, playful, capable, technical, friendly.

**[Branding Guide v2.1](brand/BRANDING_GUIDE.md) is the authority.**
This page records what the product actually does with it, and
[ui-guidelines.md](ui-guidelines.md) is where the derived design tokens live.
The older PDF remains in the repository as a historical source, not as current
guidance. Asset provenance is recorded in
[`ASSET_MANIFEST.json`](brand/ASSET_MANIFEST.json).

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
| Paw Black | `#000000` | The official favicon and smallest-size artwork |

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

## Logo system

Use the artwork in this order. Moving down the list is a response to less
available space; it is not a choice between interchangeable logos.

| File | Use |
| --- | --- |
| `brand/logo-master-dark.png` | Primary full logo on Zoomies Black or another dark ground |
| `brand/logo-light-background.png` | Primary full logo on white or a very light ground |
| `brand/logo-white-transparent.png` | Transparent white primary logo on a dark or coloured ground |
| `brand/mark-dark.png`, `brand/mark-white-transparent.png` | Original circular dog mark where the name is already visible |
| `brand/paw-swish-black.png`, `brand/paw-swish-white.png` | Favicons and equivalent tiny UI marks from 16–64px only |
| `brand/wordmark-dark.png`, `brand/wordmark-white-transparent.png` | The wordmark alone |
| `brand/github-avatar.png` | Original-dog artwork with safe space for GitHub's circular crop |
| `brand/github-social-preview.png` | 1280×640 GitHub social preview |
| `brand/social-card.png` | 1200×630 Open Graph sharing image |

The product's own copies live in `web/public/` and are the sizes the app
actually serves: `favicon.ico` (16/32/48), `favicon-16.png`, `favicon-32.png`,
`apple-touch-icon.png`, `icon-192.png`, `icon-512.png`, the dedicated
`maskable-icon-512.png`, and the web UI assets under `brand/`.

`brand/mark-white.png` and its `@2x` copy are the circular dog at the 128px
minimum and at twice it, which is why that mark can be served at its minimum and
no larger: those are the sizes in the directory, and enlarging a 256px raster to
fill a bigger slot is the sort of thing this page tells other people not to do.

`brand/app-logo.png` is the original-dog GitHub avatar with circular-crop safe
space. An App manifest cannot carry a logo, so the connect flow hands the
operator this file and a link to the page that takes it; without it the App
wears GitHub's grey default and signs every "Set up job" line anonymously.

That is one step in a wizard somebody sees once, so it is also on every
installation's card on the Installations page, with the same download and the
same link, until it is dismissed. Whether an App has an avatar is not something
GitHub will tell us -- its API serves a generated identicon for an App without
one, indistinguishable from an upload -- so the reminder is offered rather than
detected, and the operator is the one who says it is done.

To set it by hand: open the App's settings (**Installations -> the connection ->
Open the App's settings**, or `https://github.com/organizations/<org>/settings/apps/<app>`
for an organisation App and `https://github.com/settings/apps/<app>` for a
personal one) and upload `brand/github-avatar.png` under **Display
information**. GitHub crops it to a circle, which the file already allows for.

## The badge

[![CI has the Zoomies](badge.svg)](https://zoomies.sh)

A repository whose CI runs on Zoomies can say so with the badge, served from
`https://zoomies.sh/badge.svg`. It is the paw/swish on Zoomies Black beside
"CI", and "has the zoomies" on Runner Blue — the three colours the guide
gives a badge, and the one thing the identity is about: a dog doing zoomies
is CI running fast.

```markdown
[![CI has the Zoomies](https://zoomies.sh/badge.svg)](https://zoomies.sh)
```

The [migration wizard](migration.md#the-badge) adds that line to each
repository it moves, unless the operator unticks it. The file is
`docs/badge.svg` in the repository, published by the site from there so the
one a contributor edits is the one every README shows; the product's own copy
under `web/public/brand/` is what the review step previews, because a
self-hosted controller does not fetch from zoomies.sh to draw its own screen.

It is a self-contained SVG on purpose. A README badge is fetched through
GitHub's image proxy, which follows no stylesheet, no web font and no second
request, so the mark is carried inline as a tiny PNG made from
`paw-swish-white.png` and the text is pinned to a width so it lays out the
same whichever font a viewer has. Do not recolour it, and do not draw a second
one: a badge in different colours on different READMEs is not a brand.

## Using the system in the product

The full primary logo appears on the sign-in, first-run, boot and connection
failure screens — the screens that open the product — and it is given room on
them: 260px on the sign-in page, about 300px on the first-run screen, 250px on
the two transient ones, and never below the 220px minimum. The holding shape is
Zoomies Black, and it is capped at the width of whatever contains it, so a phone
shrinks the lockup rather than overflowing. The artwork carries its own clear
space, so the shape is deliberately larger than the dog inside it — that padding
is part of the supplied file and is not cropped away.

![The sign-in page: a Zoomies Black panel on the left holding the lockup, a one-line description, three facts and links to zoomies.sh, GitHub and EyUp.io; the form on the right, under the address of the instance being signed in to](screenshots/sign-in-dark.webp#only-dark){ .zoomies-shot }
![The sign-in page: a Zoomies Black panel on the left holding the lockup, a one-line description, three facts and links to zoomies.sh, GitHub and EyUp.io; the form on the right, under the address of the instance being signed in to](screenshots/sign-in-light.webp#only-light){ .zoomies-shot }

The sign-in page is the one screen where the brand gets a panel rather than a
shape. It is the page met by people who did not install the controller —
somebody sent its address by the colleague who did — so the left of it is
Zoomies Black in both themes and says what they are looking at: the lockup, a
one-line description, three facts true of every installation, and links to
[zoomies.sh](https://zoomies.sh), the source on GitHub and
[EyUp.io](https://eyup.io). The panel *is* the holding shape, so the lockup sits
straight on it, pulled out by exactly its artwork's padding so the dog lines up
with the text beneath it; the clear space moves with the artwork and nothing
enters it. The faint rings in the panel's far corner are the circular motion
path, drawn out past the edge so only an arc of it shows, with one stroke of
Runner Blue — decoration on the ground, well away from the artwork, which
carries no texture of its own. The text on the panel uses the fixed
`--z-panel-*` tokens rather than the theme's, because the panel does not change
with the theme and the light theme's greys fail on black.

On a phone the panel comes apart: the lockup becomes a band above the form at
its 220px minimum, and the links move below the form in the theme's own
colours. The first-run screen stays a card. Only the person who has just
installed the controller ever sees it, and they already know what it is.

The original circular dog carries Settings → About at 128px, its minimum size —
the one identity slot in the signed-in product with room for the primary mark,
and the page that is about the product rather than about the fleet. The
paw/swish carries everything up to its 64px maximum: the navbar, where the
detailed dog does not read clearly, and genuinely tiny placements such as the
mobile top bar, page footer and command palette.
Both use the supplied white reverse artwork on a Zoomies Black
chip, so the artwork is unchanged and remains legible in either theme.

Once somebody is signed in, the identity is carried in these quieter places:

| Where | What |
| --- | --- |
| The navigation masthead | 32px paw/swish beside the name and descriptor; icon alone when collapsed |
| The top bar, on a phone | Paw/swish, because the masthead is not on screen there |
| The foot of every page | Paw/swish, name, running version and descriptor |
| The command palette | Paw/swish and name beside the key hints |
| Settings → About | 128px circular dog beside the name, descriptor and a one-line description of the product |

The descriptor is set in Inter -- small, uppercase, letter-spaced -- rather than
cropped out of the wordmark artwork, whose own descriptor line is drawn for
240px and is a smudge at sidebar width.

### Names from the kennel

The mark is a cocker spaniel, so the names the product invents come from one.
The pool wizard opens with a name already filled in: the brand, a spaniel from
`web/src/lib/pools/names.ts`, and what the pool will actually run on --
`zoomies-truffle-docker-linux`. The kennel half is what makes one pool tellable
from another in a list; the infrastructure half is what an operator wants to
know before sending a job to it.

The `zoomies-` prefix is not decoration and is never dropped: in GitHub's own
runner settings it is the only thing distinguishing our runners from anyone
else's. So the name is offered and the prefix is not. The dice roll another
name and a name typed over is left alone, but a name given without the brand
gains it -- in the wizard's field as soon as it loses focus, and again in the
store, so that a pool created from the CLI, the API or an answer file is
branded exactly as one created from the wizard is.

## Animated homepage logo

The homepage animates the original white knockout artwork with four short
performances: paw-wave greeting, curious head tilt, squirrel-chasing run and a full
double zoomies spin. Blinking, panting, floppy ears and a wagging tail bring the
dog to life. The nose stays attached to the muzzle, the front paw covers the
rear leg, and both lower swish strokes remain separate from the dog.
The source pixels, wordmark and circular swish are retained. SVG clips articulate
parts of the dog; this is not a new vector master or a replacement logo download.

A shuffled sequence visits each performance once before reshuffling, avoids
immediate repeats and varies the quiet interval between performances. Every
performance begins and ends in the same neutral pose, so the logo switches
without flicker or shifting the page. The wordmark and swish remain still.

Visitors can pause the animation. Reduced-motion preferences and a failed or
unavailable script leave the original static logo. Motion and its timer pause
when the logo is off screen or the tab is hidden; Material instant navigation
cleans up the old controller before attaching the new one.

`docs/brand/animated-logo.html`, `docs/stylesheets/animated-logo.css` and
`docs/javascripts/animated-logo.js` own this homepage treatment. The approved
motion exception applies to the articulated dog, not rotation, recolouring or
distortion of the full brand mark elsewhere.

## Clear space and minimum sizes

* Full logo clear space: the height of the **O** in the wordmark on every side.
* Standalone mark clear space: at least 12.5% of its longest dimension. The
  supplied square icon files already include this padding; do not crop it away.
* Primary full logo: 220px wide minimum.
* Circular dog mark: 128px square minimum.
* Paw/swish: 16px square minimum and 64px maximum.

## Do and do not

**Do**

* Use the dark master on dark surfaces.
* Use the white knockout over dark photography or a coloured UI.
* Use the original circular dog for avatars, touch icons, PWA icons and social artwork.
* Use the paw/swish only for favicons and equivalent tiny UI marks.
* Keep every mark's supplied swish and safe padding intact.

**Do not**

* Stretch or skew the artwork.
* Recolour any part of the dog.
* Add a shadow, gradient or glow.
* Put the dark master on a busy background.
* Rotate the mark.
* Promote the paw/swish into a primary or social logo.

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
| Config directory | `/etc/zoomies` as root, `~/.config/zoomies` otherwise; see [Where things live](configuration.md#where-things-live) |
| Container images | `ghcr.io/eyupio/zoomies`, `ghcr.io/eyupio/zoomies-agent`, `ghcr.io/eyupio/zoomies-runner`, `ghcr.io/eyupio/zoomies-runner-docker` |
| Runner names | `zoomies-4vcpu-ubuntu-2404-biscuit-a3f9qz2m` — the brand, the pool's shape, a kennel word and a token |
| Pool names | `zoomies-truffle-docker-linux`; a name given without the prefix gains one |
| Pool labels | `zoomies-linux-x64`, `zoomies-gpu`; every pool also answers to `zoomies` |
| Migration branch | `zoomies/migrate-runners-<timestamp>` |

### On somebody else's screen

Two of those names are read far more often outside Zoomies than inside it: the
runner name, which GitHub prints in its runner list and in every job's "Set up
job" step, and the pool label, which has to be written into `runs-on` in every
workflow in the organisation. Both start with the product name, and neither
carries anything a reader would have to decode. The runner name carries the
pool's *shape* rather than the name somebody invented for it, because a reader
on GitHub is there precisely because they do not yet know what they are looking
at — and when it will not fit, the shape is what gives way rather than the
brand. See [Naming and platforms](naming.md#runner-names).

`internal/store/brand.go` is the one place these are spelled out;
`web/src/lib/brand.ts` is the browser's copy of it.

## Sharing and discovery

Most people meet Zoomies on somebody else's screen: a controller their
colleague installed, or a link pasted into a chat window. Two places carry the
identity outwards, and both are part of the brand rather than an afterthought.

**A controller's own page.** `web/index.html` carries Open Graph and Twitter
card tags, `brand/social-card.png` as the card image, and a link to
[zoomies.sh](https://zoomies.sh) on the sign-in page's brand panel — beside the
source on GitHub — and in the footer of every signed-in page, each beside the
credit *Developed by [EyUp.io](https://eyup.io)*.
Both addresses come from `web/src/lib/links.ts`, and the site's footer prints
the same credit from `extra.developer` in `mkdocs.yml`, so the product and the
site say the same thing about where they came from. `og:url` and `og:image` have to be absolute for a preview to
render at all -- the service fetching the page is not a browser and has no base
to resolve a relative path against -- so the controller substitutes
`server.external_url` into them when it starts. An instance that has not been
told its own address keeps relative paths rather than guessing at one.

**The site.** `overrides/main.html` extends the Material theme with the same
tags, derived per page, plus a `SoftwareApplication` description in JSON-LD on
the home page. Material's own social plugin is not used: it renders a card
image per page with Cairo and Pillow, which is two native libraries in the docs
build for a picture the brand pack already supplies.

## For print, signage or merchandise

The SVG files in the original pack are raster wrappers, not true vector
redraws. For large-format work, commission a proper vector redraw from the
approved masters rather than enlarging them.
