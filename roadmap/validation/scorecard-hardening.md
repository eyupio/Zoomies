# OpenSSF Scorecard hardening: 12 September 2026

Baseline: **4.6/10**, commit `c74735aa3a7525bd184f27f5568a9d3b6a9a4c23`,
[scan at 14:02 UTC](https://api.scorecard.dev/projects/github.com/eyupio/zoomies).
This is a supply-chain checklist, not an application penetration test.

## Repository changes

- CodeQL scans Go and JavaScript/TypeScript on main pushes, pull requests and
  weekly. Go is explicitly built so analysis includes the embedded application.
- Native Go fuzz targets exercise key decoding, encryption round trips,
  ciphertext tampering and malformed ciphertext. CI runs each for 30 seconds;
  weekly runs use five minutes. Failed corpora are retained as artifacts.
- Workflows default to read-only or no permissions. Runner update and package
  cleanup write permissions are scoped to their jobs. Drill code no longer has
  a write token or pushes unreviewed changes to main: evidence is retained in
  job summaries and 90-day artifacts. Historical drill records remain intact.
- Binary releases now attach the existing GitHub attestation bundle as
  `zoomies-provenance.sigstore.json`. This contains the actual signed statement;
  it is not a renamed checksum. Existing releases are not retrospectively
  attested. New releases gradually replace them in Scorecard's release window.
- Docker base images use verified multi-platform manifest digests, including
  runner matrix overrides in both CI and release workflows. When updating a
  runner base digest, edit `internal/naming/images.go`, run `make generate`
  and update the Dockerfile default together. Dependabot continues to update Docker dependencies under `deploy/`.
- Documentation dependencies have a complete hash-checked lock, with weekly
  Dependabot updates. Regenerate with Python 3.12 and `pip-compile
  --generate-hashes --strip-extras --no-emit-index-url --output-file
  docs/requirements.txt docs/requirements.in`; install using `--require-hashes`.

## Advisory fixes

| Advisory | Change |
| --- | --- |
| GO-2026-6237 | DHCP upgraded to `v0.0.0-20260719225207-c76316d4aa82`. |
| GO-2026-6179, GO-2026-6180 | `golang.org/x/mod` upgraded to `v0.40.0`; required `x/tools` update to `v0.49.0`. Go already uses fixed toolchain `1.27.1`. |
| GHSA-2883-xcg3-v3hh | npm override requires `js-yaml >=4.3.2` within major 4, including the exact transitive pin from tooling. |
| GO-2026-5932 | No fixed version exists: this advisory concerns deprecated `x/crypto/openpgp`. Keep the module for supported cryptographic packages; the application dependency graph contains no OpenPGP packages and govulncheck reports zero affected symbols or imported packages. Module-level scanners may still flag it. |

## Repository administrator follow-up

The GitHub connection used for this change cannot write repository administration
settings. The baseline reports main unprotected and the ruleset list is empty.
In **Settings → Rules → Rulesets**, create an active branch ruleset for main:

1. Require pull requests and resolution of review conversations.
2. Require passing checks: start with existing `Go`, `UI`, `Drill`, `Upgrade`,
   `install.sh` and `govulncheck` checks, using the exact names shown by a green
   PR. Add both CodeQL jobs and both fuzz jobs after their first successful run.
3. Block force pushes and branch deletion; require the branch to be up to date.
4. Require an independent approval when a second reviewer is available. GitHub
   does not let an author approve their own PR. Do not configure an impossible
   review requirement for a sole maintainer without an intentional bypass plan.

Review requirements and actual approved merge history both matter to Scorecard.
Moving workflow files cannot manufacture independent review history.

Register and complete the [OpenSSF Best Practices questionnaire](https://www.bestpractices.dev/)
when ready, using accurate answers before adding its badge. The maintenance
check is zero solely because the repository is under 90 days old. Contributor
diversity also grows through genuine participation; neither is a code fix.

The public score updates after changes reach main and Scorecard publishes a new
scan. SAST and review history improve over subsequent commits, and signed
release scoring improves as new releases are published. No target score is
promised before remeasurement.

## Local validation

- `actionlint` passed all workflow files (custom runner-label warnings excluded).
- Go application build and `govulncheck ./...` passed; zero reachable or imported
  package vulnerabilities, one module-only warning.
- Both fuzz targets passed more than 1.16 million combined executions; the
  existing cryptox unit suite passed.
- UI type checking reported zero errors/warnings; production build and all
  three UI unit tests passed.
- Documentation installed with required hashes and built with `mkdocs --strict`.
- Container publication and release attestation attachment require real GitHub
  workflow runs; those publishing paths were not executed locally.
