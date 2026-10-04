# Contributing to Zoomies

Thank you for helping Zoomies give self-hosted GitHub Actions runners a better
run. Contributions of code, tests, documentation, bug reports, design feedback,
and real-world deployment experience are all welcome.

By participating, you agree to follow our [Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

- Search the [issues](https://github.com/eyupio/zoomies/issues) before opening a
  new one.
- Small, focused fixes can go straight to a pull request.
- For a substantial feature, architectural change, new dependency, or breaking
  configuration change, open an issue first so the approach can be agreed
  before a lot of work is done.
- Report vulnerabilities privately as described in
  [SECURITY.md](SECURITY.md). Do not open a public issue for a security problem.

Zoomies is early-beta software that creates runners on other people's machines.
Changes to authentication, authorisation, secrets, runner isolation, container
backends, networking, upgrades, or deletion paths deserve an explicit threat
and failure-mode review.

## Development requirements

The core binary is written in Go. The web UI is built with Svelte and embedded
in that binary.

- Go 1.27.1 or later (the version in `go.mod` and CI)
- Node.js 22 or later and npm, when changing or rebuilding the UI
- Git
- Docker or Podman for backend and image work
- A GitHub App only for tests or development flows that actually talk to GitHub

Start by forking the repository, then clone your fork:

```sh
git clone https://github.com/YOUR-USERNAME/zoomies.git
cd zoomies
git remote add upstream https://github.com/eyupio/zoomies.git
```

Create a branch from the latest `main`:

```sh
git fetch upstream
git switch -c fix/short-description upstream/main
```

Use a short, descriptive branch name such as `fix/agent-reconnect`,
`feat/pool-draining`, or `docs/runner-security`.

## Build and run

Run `make help` for the maintained list of targets.

```sh
make build          # build the Go binary with the web UI embedded
make build-nogui    # fast Go-only inner loop
make dev            # local controller with debug logging and auth disabled
make ui-dev         # Vite UI against a controller on localhost:8080
```

The development mode disables authentication and is for a local workstation
only. Do not expose it to a network.

## Make a focused change

- Keep pull requests small enough to review and explain the reason for the
  change, not only the implementation.
- Follow the surrounding code and UI patterns. Go code must be formatted with
  `gofmt`; web code is formatted with Prettier.
- Add or update tests for changed behaviour.
- Update user documentation, configuration examples, API definitions, and
  screenshots when the public behaviour changes.
- Preserve safe defaults. If a change weakens isolation or exposes a new
  capability, make the trade-off explicit in both the UI and documentation.
- Never commit credentials, GitHub App keys, tokens, local databases, generated
  logs, or build output.

Generated files have a single source of truth:

- After changing `api/openapi.yaml`, run `make openapi` and commit the
  generated TypeScript client.
- After changing the runner image catalogue in `internal/naming`, run
  `make generate` and commit everything it updates. Do not hand-edit generated
  catalogue sections.

## Test your change

Run the narrowest relevant tests while developing, then the standard checks
before opening a pull request:

```sh
make test-short
make lint
make test
```

For documentation changes, install the pinned dependencies and run the same
strict build as the site workflow:

```sh
python -m pip install --require-hashes -r docs/requirements.txt
mkdocs build --strict
```

The build must produce `site/sitemap.xml`, `site/llms.txt` and `site/badge.svg`,
and the header must name the latest release — `hooks/source.py` asks the GitHub
API for it at build time, so set `GITHUB_TOKEN` if the anonymous rate limit is
in the way. These checks catch broken links and missing published assets before
a PR is merged.

For web UI changes, also run:

```sh
cd web
npm ci --no-audit --no-fund
npm run check
npm run test:unit
cd ..
make test-ui
```

Some suites intentionally require more setup:

- `make test-drill` runs a real controller and agent against fake GitHub.
- `make test-e2e` exercises Docker-backed end-to-end behaviour and skips when
  required GitHub credentials are absent.
- `make screenshots` captures documentation screenshots from the real UI.

You do not need to run an unrelated privileged or credential-dependent suite.
State exactly what you ran and what you could not run in the pull request.

CI runs what a change's files can affect, decided by the `changes` job at the
top of `.github/workflows/ci.yml`, on a pull request and on `main` alike:

| A change to | Runs |
| --- | --- |
| Markdown, `docs/`, the site, the workflows | the `internal/docs` and `internal/naming` checks, which hold the prose to the code — about a minute |
| `install.sh` | the `install.sh` checks and the upgrade test; no binaries or images |
| `web/` | the UI and Playwright jobs, and the docs checks |
| `deploy/` | the image builds and the tests that read those files |
| Go, or anything not listed | the whole set |
| `ci.yml`, the `Makefile`, `go.mod` | everything |

The Go suite is split by package across four runners; `make test
TEST_PKGS=./internal/store/` runs one shard the way CI does. Coverage is
measured on `main`, when the whole Go suite ran. `main` publishes the dev
channel's binaries and images only from a commit that changes what ships — Go,
the UI or `deploy/` — so a documentation merge leaves `--version dev` where it
was. A manual dispatch runs everything. CodeQL, govulncheck, the fuzz targets
and Scorecard run only when their own inputs change, and weekly regardless.

A pull request's checks stop when the pull request does. Pushing to it replaces
the run in flight, and closing or merging it starts a run that takes over the
pull request's concurrency group, cancels the one still going and skips every
job — so a merged pull request does not keep its fleet slots while the commit
on `main` queues behind them. Only a pull request is ever cancelled: runs on
`main` queue instead, because they publish the dev channel. A new workflow that
runs on `pull_request` needs the same three parts, and `internal/docs` fails
without them: the `closed` type, the shared group, and a
`github.event.action != 'closed'` guard leading every job's condition.

## Commit and open a pull request

Use clear, imperative commit subjects:

```sh
git add path/to/changed-file
git commit -m "Add runner reconnect backoff"
git push -u origin fix/agent-reconnect
```

Open a pull request against `eyupio/zoomies:main`. Include:

- the problem and why it matters;
- the chosen approach and important trade-offs;
- the tests and manual checks performed;
- screenshots or a short recording for visible UI changes;
- documentation, configuration, migration, or compatibility impact; and
- any follow-up work deliberately left out.

Keep the branch current with `main`, respond to review comments, and avoid
mixing unrelated cleanup into the same pull request. Maintainers may edit,
squash, or reword commits when merging.

## Coding and design principles

- Prefer straightforward code and explicit failure modes over cleverness.
- Errors should tell an operator what is true, why it matters, and what to do
  next.
- Keep the controller usable as one Go binary with SQLite; do not introduce a
  required external database or orchestration platform without prior agreement.
- The CLI and web UI are clients of the same API. Keep their behaviour and
  terminology aligned.
- Preserve accessibility, responsive layouts, keyboard operation, light and
  dark themes, and the guidance in
  [docs/ui-guidelines.md](docs/ui-guidelines.md).
- Treat logs, metrics, audit records, and documentation as part of the feature,
  not an afterthought.

## Licence

By submitting a contribution, you agree that it may be distributed under the
repository's [GNU Affero General Public License v3.0](LICENSE).

Thank you for contributing to Zoomies.
