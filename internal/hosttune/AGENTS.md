# AGENTS.md -- host health and tuning

Scoped guidance for `internal/hosttune`, which the root [AGENTS.md](../../AGENTS.md)
points here. It adds to the root and never overrides it.

`internal/hosttune` owns isolated OS checks and consent-based tuning. Doctor is
read-only unless `--interactive` delegates an explicitly approved fix to tune.
`zoomies upgrade`, `update` and `deployment update` share the complete upgrade
flow and must never call tuning or open a tuning menu, including with `--yes`.
Doctor is concise by default; `--verbose` gives the full report.
Native agents collect health; container installations use the installed native
binary in `zoomies-host-health.service`, publishing through the shared mount.
Keep GET host payloads, `host.updated`, OpenAPI and UI types consistent. Tests
inject all host filesystem/command access; never modify the test machine.
Tuning state lives separately in `/var/lib/zoomies-host-tune/state.json` and
must survive uninstall. Never reboot, change CPU mitigations, or restart Docker
while work can run; the one way to restart it on a busy host is `--force`, a
maintenance restart that takes the host out of service and waits for its jobs
first, and ends one only with `--kill-running`. See `docs/host-health.md` for
consent and reversal rules.
