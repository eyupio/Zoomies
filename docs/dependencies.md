---
description: >-
  Every dependency Zoomies carries, the one-line reason it earns its place,
  and the two that were deliberately left out.
---

# Dependencies

Every dependency needs a reason. If one below stops earning its line, it should
go. The bar is: *would writing this ourselves be worse than carrying it?*

Two things are deliberately **not** here, and both were close calls — see
[Deliberate omissions](#deliberate-omissions).

## Go

| Module | Why |
| --- | --- |
| `modernc.org/sqlite` | Pure-Go SQLite. No cgo means the binary is static, cross-compiles to four platforms from one machine, and runs on distroless. |
| `github.com/go-chi/chi/v5` | Router. `net/http`'s 1.22 mux nearly suffices, but chi's middleware chaining and sub-router mounting keep the API's role and audit middleware readable. Small, stable, no reflection. |
| `github.com/google/go-github/v88` | The GitHub REST client. Hand-rolling the Actions endpoints, their pagination and their error shapes would be a permanent maintenance tax for no gain. |
| `github.com/bradleyfalzon/ghinstallation/v2` | GitHub App installation tokens: JWT signing, token caching, refresh. It is 400 lines we would otherwise get subtly wrong. |
| `golang.org/x/crypto` | `argon2` for password hashing. The algorithm is not something to implement. |
| `golang.org/x/oauth2` | OIDC's authorisation-code flow. |
| `github.com/coreos/go-oidc/v3` | OIDC discovery and ID-token verification, including JWKS rotation. Optional feature, well-maintained library. |
| `github.com/prometheus/client_golang` | The metrics endpoint. The exposition format has enough edge cases that hand-writing it is a false economy. |
| `gopkg.in/yaml.v3` | `zoomies.yaml`. Strict decoding turns a misspelled key into an error naming the line. |
| `github.com/charmbracelet/bubbletea` | The installer's TUI runtime. |
| `github.com/charmbracelet/huh` | The installer's prompts — select, input, confirm, with validation. It is what makes `zoomies init` feel like a product rather than a script. |
| `github.com/charmbracelet/lipgloss` | Styling for the installer and the CLI's table output. |
| `golang.org/x/term` | Terminal detection, so the CLI and installer degrade to plain output when piped. |

Standard library for everything else: `net/http`, `log/slog`, `database/sql`,
`crypto/*`, `embed`, `os/exec`, `context`, `testing`.

## npm

Runtime (shipped to the browser):

| Package | Why |
| --- | --- |
| `svelte` | The framework. Runes, no virtual DOM, and a compiled output small enough to hit the 200 KB shell budget. |
| `@tanstack/svelte-table` / `@tanstack/table-core` | Headless data-grid logic — sorting, filtering, column visibility, row selection — with our own markup. Reimplementing this correctly, including keyboard navigation over a virtualised body, is weeks. |
| `@xterm/xterm` + `addon-fit`, `addon-search`, `addon-webgl` | The log viewer. 100k lines without jank is a hard requirement, and xterm's WebGL renderer is the reason it is achievable. |
| `@lucide/svelte` | Icons. Imported individually so tree-shaking keeps only what is used. |
| `@fontsource-variable/inter`, `@fontsource/jetbrains-mono` | Self-hosted fonts. An air-gapped install must not make a third-party font request, and neither should anyone else's browser. |

Build and test only:

| Package | Why |
| --- | --- |
| `vite`, `@sveltejs/vite-plugin-svelte` | Build and dev server. |
| `tailwindcss`, `@tailwindcss/vite` | Utility CSS. v4's CSS-first `@theme` reads our design tokens directly, so there is exactly one source of truth for a colour. |
| `typescript`, `svelte-check` | Types, and type checking inside `.svelte` files. |
| `openapi-typescript` | Generates the API client's types from `api/openapi.yaml`. The UI cannot drift from the API without the build failing. |
| `@playwright/test` | UI tests against the real binary. |
| `eslint`, `typescript-eslint`, `eslint-plugin-svelte`, `@eslint/js`, `globals` | Linting. |
| `prettier`, `prettier-plugin-svelte` | Formatting. |

## The docs site

`docs/requirements.txt`, pinned. These never reach a user's machine — they
build [zoomies.sh](https://zoomies.sh) and nothing else.

| Package | Why |
| --- | --- |
| `mkdocs` | Turns `docs/` into the site, so the website is the repository's own documentation rather than a second copy of it. Pinned below 2.0 deliberately; the reason is in `docs/requirements.txt`. |
| `mkdocs-material` | The theme. It carries the search, the light/dark palettes, the tabbed blocks the quick start uses, and the Mermaid integration below. |
| `pymdown-extensions` | The fenced-block, tabbed and admonition syntax the pages are written in, including the custom `mermaid` fence. |
| Mermaid | Diagrams. Material fetches it from a CDN in the reader's browser, only on a page that has one, and colours it from the palette above — so a diagram lives in the Markdown beside what it explains, renders on GitHub as well, and there is no exported image to go stale. |
| `pillow` | Encodes the screenshots in `docs/screenshots/` as lossless WebP, at under half the size of the PNGs Playwright takes. Only `make screenshots` needs it, so it is not in `docs/requirements.txt` and the site builds without it; the browser can write lossless WebP itself but compresses it six times worse. |

## Deliberate omissions

**`github.com/docker/docker`.** The official client drags in a very large
dependency tree — containerd, gRPC, OpenTelemetry, swarm types — for what
amounts to a dozen JSON endpoints over a unix socket. `internal/backend/dockerapi.go`
is a few hundred lines of `net/http` against the documented Engine API, and it
buys Podman support almost free, because `podman.sock` speaks the same protocol.
The one genuinely fiddly part, demultiplexing the log stream's 8-byte frame
headers, is unit-tested.

**A charting library.** The Overview needs sparklines and utilisation bars.
Those are inline SVG paths of a few dozen lines each. The smallest credible
charting library is larger than the entire rest of the app shell.

**A client-side router.** Fourteen routes, two of them with a parameter segment,
do not need a routing library. `web/src/lib/router.ts` is small enough to read in
one sitting and does exactly what the History API already offers.

**A state-management library.** Svelte 5 runes are the state management library.

## Reviewing a new dependency

1. Could the standard library, or fifty lines of our own, do it?
2. Is it maintained, and does it have a release in the last year?
3. What does it pull in transitively? (`go mod graph`, `npm ls --all`.)
4. If it ships to the browser, what does it cost against the shell budget?
5. Add a line to the right table above, in the same voice: *why*, not *what*.

A pull request that adds a dependency without a line here should be asked for
one.
