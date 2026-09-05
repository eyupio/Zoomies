# Zoomies Branding Guide

Version 2.1 - September 2026

This repository keeps its curated documentation copies directly in
`docs/brand/` and its production web copies in `web/public/`. The original
media-pack directory names are flattened here so consumers have one obvious
file for each placement.

## Brand idea

Zoomies is fast, playful, and technical without feeling corporate. The circular cocker-spaniel motion mark turns the familiar idea of a dog doing zoomies into a visual metaphor for jobs and runners moving quickly.

The original detailed dog logo and wordmark remain the main brand. A simpler two-step icon system supports progressively smaller uses without weakening the primary identity.

## The logo system

Use the marks in this order of preference:

1. **Primary full logo - circular dog mark + ZOOMIES wordmark.** This is the main brand. Use the supplied original v1 artwork unchanged whenever space allows.
2. **Circular cocker-spaniel mark.** Use the original dog mark when the brand name is already visible nearby or a standalone symbol is needed at generous sizes.
3. **Front-view cocker-spaniel head + swish.** Use this simplified secondary mark in badges and app UI at medium-small sizes.
4. **All-black paw print + swish.** This is the official favicon/navbar/smallest-size mark. Use it at 16-64 px, including compact navigation where the dog is not clear.

Do not replace the primary full logo with the paw in prominent brand placements. The paw is a functional small-size shorthand, not the main identity.

## Quick file chooser

| Need | Recommended file |
|---|---|
| Main logo on a dark background | `logo-master-dark.png` |
| Main logo on a light background | `logo-light-background.png` |
| Transparent white main logo | `logo-white-transparent.png` |
| Original circular dog mark | `mark-dark.png` or `mark-white-transparent.png` |
| Compact navbar | `paw-swish-black.png` or `paw-swish-white.png` |
| Badge or medium-small app mark | `head-swish-black.png` or `head-swish-white.png` |
| Browser favicon | `favicon.ico` |
| GitHub avatar using original dog | `github-avatar.png` |
| GitHub social preview | `github-social-preview.png` |
| Open Graph sharing image | `social-card.png` |

## Colour

### Core palette

| Colour | Hex | RGB | Use |
|---|---:|---:|---|
| Zoomies Black | `#080808` | `8, 8, 8` | Primary dark background and brand neutral |
| White | `#FFFFFF` | `255, 255, 255` | Reversed artwork and light backgrounds |
| Cool Grey | `#B9BCC2` | `185, 188, 194` | Secondary UI and documentation |
| Mid Grey | `#666A73` | `102, 106, 115` | Supporting text and interface elements |
| Runner Blue | `#2F80ED` | `47, 128, 237` | Product accent and selected states |
| Fast Cyan | `#22D3EE` | `34, 211, 238` | Secondary accent and speed highlights |
| Paw Black | `#000000` | `0, 0, 0` | Official paw/swish artwork |

The primary logo remains monochrome. Runner Blue and Fast Cyan are supporting interface/background colours; do not recolour individual dog, wordmark, paw, or swish elements.

## Backgrounds and contrast

- Use the light-background primary file on white or very light surfaces.
- Use the dark/master or transparent-white artwork on Zoomies Black and other dark surfaces.
- Use the black paw/swish on white or very light backgrounds. Use the supplied white paw/swish only when a reversed mark is required on dark backgrounds.
- Keep at least 4.5:1 contrast between functional artwork and its background.
- On photos, place the logo over a calm area with strong contrast or use a solid holding shape.
- Never add a drop shadow, outline, glow, gradient, or texture to the official artwork.

## Clear space

For the full logo, keep clear space on every side equal to the height of the letter **O** in the wordmark.

For standalone marks, keep clear space equal to at least 12.5% of the artwork's longest dimension. The supplied square icon files already include safe padding; do not crop it away.

## Minimum sizes

| Artwork | Minimum digital size |
|---|---:|
| Primary full logo | 220 px wide |
| Circular dog mark | 128 px square |
| Front-view head + swish | 48 px square |
| Paw + swish | 16 px square |

Below 48 px, use the paw/swish rather than either dog mark. At 16 px, use the supplied `favicon-16x16.png` without further resampling.

## Wordmark and typography

The supplied wordmark files contain the approved italic ZOOMIES artwork and the descriptor `SELF-HOSTED GIT RUNNERS`. Treat them as artwork: do not retype, stretch, condense, or alter their spacing.

Use Inter for product UI and documentation, with Geist Sans or `system-ui` as fallbacks:

```css
font-family: Inter, "Geist Sans", system-ui, sans-serif;
```

## Correct use

- Prefer the primary full logo in repository READMEs, documentation headers, websites, launch pages, and sponsorship material.
- Use the circular dog mark when motion and personality are important and the name is already present.
- Use the head/swish in badges and app UI at 48 px and above.
- Use the paw/swish for favicons, compact navbar identity, and equivalent tiny UI glyphs up to 64 px.
- Use the original circular dog mark for Apple touch icons, Android/PWA icons, GitHub avatars, and social artwork.
- Scale proportionally and preserve the supplied aspect ratio and clear space.

## Incorrect use

Do not:

- make the paw/swish the main brand logo;
- swap, detach, rotate, or redraw a swish;
- stretch or squash any artwork;
- recolour individual parts of a logo;
- add effects, strokes, shadows, bevels, or gradients;
- place dark artwork on a dark background or light artwork on a light background;
- crop into the dog, ears, paw pads, wordmark, swish, or required clear space;
- combine the marks with other logos into a new lockup without review.

## Web implementation

The production copies are in `web/public/`; the app-level design tokens are in
`web/src/lib/styles/tokens.css`.

Suggested HTML:

```html
<link rel="icon" href="/favicon.ico" sizes="any">
<link rel="icon" type="image/png" sizes="32x32" href="/favicon-32.png">
<link rel="icon" type="image/png" sizes="16x16" href="/favicon-16.png">
<link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon.png">
<link rel="manifest" href="/site.webmanifest">
```

## GitHub and social use

- GitHub avatars are circularly cropped in many views. The supplied original-dog avatar keeps the main artwork inside the safe area.
- Do not use the paw as a GitHub or social avatar; it is reserved for favicon-scale applications.
- GitHub social previews use a 2:1 canvas; the supplied 1280x640 artwork preserves the original full-logo presentation.
- The Open Graph image is 1200x630 and preserves the approved v1 social artwork.
- Do not add taglines directly inside the logo. Put campaign copy elsewhere in the layout.

## Production notes

- The primary artwork is preserved unchanged from `Zoomies_Brand_Pack_v1.zip`.
- The paw/swish master is preserved from the approved attached PNG and is used to generate 16, 32, 48, and 64 px favicon/small-mark derivatives, plus the white reverse used by compact navigation.
- The approved front-view icon binary was not present in the supplied v1 archive. The included `brand/secondary/` artwork is a restored, visually consistent reconstruction based on the approved direction; its provenance is also recorded in `ASSET_MANIFEST.json`.
- The supplied artwork is high-resolution raster PNG. The v1 SVG files are convenience wrappers around raster artwork, not true vector redraws.
- Transparent PNGs use alpha and are suitable for web and presentation use. White variants may appear blank in file browsers with a white canvas.
- For large-format print, signage, embroidery, or precision cutting, commission a clean vector redraw from these masters before production.

## Asset provenance

This pack follows the approved hierarchy from the Zoomies logo concept conversation: the original circular cocker-spaniel logo remains the primary identity; the simplified front-view cocker-spaniel head is the medium-small secondary icon; and the latest all-black paw-print-with-swish is the official favicon/small-size mark.
