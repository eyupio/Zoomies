# CLAUDE.md

Scoped guidance for CI and generated files, loaded in addition to the root [CLAUDE.md](../CLAUDE.md). It appends to that file and never overrides it.

## Things CI will fail you on

Beyond tests and lint, several files must stay in sync with their sources. Each
has a CI job that diffs them:

* `internal/api/openapi_spec.go` is generated from `api/openapi.yaml`. After
  editing the spec, run `go run internal/api/gen_openapi.go` from the repo root.
* `web/src/lib/api/schema.d.ts` is generated from the same spec. Run
  `make openapi` and commit the result.
* `internal/catalog/catalog.json` and the check list on `docs/kennel-club.md`
  are generated from `docs/problem-codes.md` and the Kennel registry by
  `make generate`; `TestTheCatalogIsCurrent` fails on a stale copy.
* The runner image catalogue in `internal/naming/images.go` is the source for
  both workflows' build matrices, the Makefile's `variant.*` rows and the table
  in `docs/naming.md`. Adding or swapping an operating system is a row there and
  then `make generate`; editing any of the four by hand fails a test in
  `internal/naming`.
* `install.sh` at the repo root is copied verbatim to the site root; the script
  people `curl` is the script a contributor edits. Do not create a second copy.
* The app shell must stay under **200 KB gzipped** (`web/vite.config.ts`
  enforces it, the number is documented in `docs/ui-guidelines.md`). Route chunks
  are excluded, so move weight to a lazily loaded route rather than raising the
  budget.
* `go mod tidy` must leave `go.mod`/`go.sum` unchanged, and `gofmt -l` must be
  empty.
* `govulncheck ./...` must find nothing reachable. It runs in its own workflow,
  on every change and weekly against `main`, so a module found vulnerable after
  it merged still gets reported.
* Every `uses:` in `.github/workflows` is pinned to a commit with its release
  in a comment, and a workflow with more than one job grants no `write`
  permission at the top. Both are tested in `internal/docs`; Dependabot moves a
  pin and its comment together, and a new action gets the same treatment.
* A workflow that runs on `pull_request` lists `closed` among its types, groups
  its concurrency by pull-request number with `cancel-in-progress` true for
  pull requests only, and leads every job's condition with
  `github.event.action != 'closed'`, inside parentheses around any `||` the
  condition already had. Closing or merging a pull request starts a run whose
  only purpose is to take over the group and cancel the one in flight, so
  nothing may start for it. `internal/docs` tests all of it;
  `cancel-merged-checks.yml` is the one exemption, because it is the closed
  handler and a guard on its job would switch it off.
* `mkdocs build --strict`: a docs link that points nowhere fails the build. The
  site workflow also checks that `sitemap.xml` and `llms.txt` came out of it,
  both generated (by `overrides/sitemap.xml` and `hooks/seo.py`) rather than
  written, so a build that quietly stopped producing one would otherwise ship,
  and that the header names a release: `hooks/source.py` resolves the latest
  release, stars and forks from the GitHub API at build time, and an offline
  build is allowed to go without them where the published site is not.
