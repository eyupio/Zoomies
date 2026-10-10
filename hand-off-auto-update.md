# Hand-off: in-product updating (ZF-232)

Updated 10 October 2026. The design is `roadmap/in-product-updates.md`, the
privilege decision `roadmap/decisions/0011-an-operator-may-let-zoomies-update-itself.md`,
and the narrative for operators `docs/upgrading.md#updating-from-the-web-ui`.

## State

Everything is built. Parts 1 to 5 are merged on `main` (#719, #734, #743, #757,
#759, #786, #789, #797, #801, #803, #816). Part 6 (the controller-restart rollout
drill, `test/upgrade/helper-check.sh` against a fake `systemctl`, the
documentation and the screenshots) is in the Part 6 pull request. The work-package
row in `roadmap/progress.md` is `done`, and `ROADMAP.md` lists ZF-232 in section 6.

## What remains

Only [the manual check](roadmap/in-product-updates-manual-check.md): run it on a
spare systemd host before recommending `auto` anywhere that matters. No drill
runs a real update through the helper's systemd units.

## Known gaps

Recorded while the feature was built; check each before relying on it.

* Why the helper can never be installed on a host is kept in memory only, so it
  is lost until the host's next heartbeat after a controller restart.
* A Docker daemon with user-namespace remapping, and a service that runs as root,
  show their helper as missing rather than unsupported.
* With the mode `off`, a host whose last attempt failed shows a disabled **Try
  again** with no reason of its own.
* A failed press on a host that belongs to a running rollout halts that rollout,
  on purpose.
* The update status reads the planner's inputs on every render.
