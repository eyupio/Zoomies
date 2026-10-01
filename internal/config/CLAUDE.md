# CLAUDE.md

Scoped guidance for configuration, loaded in addition to the root [CLAUDE.md](../../CLAUDE.md). It appends to that file and never overrides it.

## Configuration

**Settings live in the database.** The Settings page writes the
`instance_settings` table and nowhere else, and the controller layers it over
the defaults and `zoomies.yaml` at start (`config.Rebuild`); a `ZOOMIES_*`
variable is the operator's last-resort override on top, not where a setting is
kept. So code that needs a setting's value reads the effective configuration --
the running `*config.Config`, or `config.Effective` for a deployment that is
not this process (the installer upgrading one reads its database read-only) --
and never a `ZOOMIES_*` variable or a `.env` file's copy of one. A value read
from the environment is wrong the moment an operator has used the Settings
page. `TestNoCodeReadsASettingFromTheEnvironment` in `internal/docs` fails on
any such read outside `internal/config`; do not add to its allowlist to get a
change through. The one real exception is a remote agent, which has no
database: its own instance settings come from its file and environment.

The same goes for what the installer writes. A container controller's `.env`
and Compose file carry only what opens the database and what Compose reads
itself; `zoomies init` stores every answer in the database first
(`SettingsEnv`, then `zoomies config import-env` in a one-off container), and
`zoomies upgrade` moves an older deployment's out of its environment
(`internal/installer/envsettings.go`). Never add a stored setting's variable
to the `.env` template or the controller's `environment:` block --
`TestAControllersComposeFileHandsItNoSetting` and
`TestAControllersEnvFileHoldsOnlyWhatOpensItsDatabase` fail if you do.

Every setting is a row in the registry in `internal/config/settings.go`, which
gives it its `zoomies.yaml` key, its `ZOOMIES_*` override and its place on the
Settings page. Adding a setting means adding the row, plus a row in
`docs/configuration.md`.

`config.Validate` returns `Finding`s in three severities, and the distinctions
matter: **errors** stop startup with a message saying what to change;
**warnings** never stop anything but each one names a setting that weakens the
default posture; **info** findings are neither wrong nor risky, and exist for
the defaults that surprise people (`agent.none`, `tls.self_signed`). A few
codes choose their severity from the circumstances -- `auth.disabled` is a
warning on loopback and an error on a public bind.

The same list is printed at startup and rendered in the UI's problems panel,
alongside the problems the running controller raises. If you add a setting that
can make the deployment less safe, add the warning too -- silent dangerous
toggles are the thing this design exists to prevent. Every code needs a row in
`docs/problem-codes.md`, which `internal/docs` tests in both directions;
`docs/security.md` explains what the dangerous ones cost.

The safe configuration is the default: loopback bind, auth on, ephemeral
runners, no Docker socket in jobs, no root.
