# Trying the update helper on a real host

Nothing in the test suite runs a real update through the helper's systemd
units: the tests use a fake engine and a recording service manager, and the
end-to-end drill against a fake `systemctl` is Task 6.1 of
[the plan](in-product-updates-plan.md). This is the short list to run by hand on
spare machines before `updates.mode` is set to `auto` anywhere that matters. It
takes about an hour and a half. Use hosts you can lose.

Run every step as a person who can `sudo`, and write down what you saw, not
only whether it passed.

## Before you start

* A spare Linux host with systemd, running a Zoomies controller **one release
  behind** the newest tag. Install that release with
  `curl -fsSL https://zoomies.sh/install.sh | sh -s -- --version vX.Y.Z`, which
  runs `zoomies init` when it has installed the binary. Do the native install
  first; repeat sections 2 to 4 for a Compose deployment and for Podman if you
  run those.
* A second host for section 5, joined to that controller as an agent on the
  same release, and, for section 6, whichever of these you can find: a Linux
  host that systemd does not run (Alpine with OpenRC, say), a Mac, and a
  container deployment under rootless Podman or rootless Docker.
* `updates.mode` set to `manual`. On a running controller that is the
  Configuration page (Settings → Configuration, the setting `updates.mode`; the
  Updates page links to it for the platform role), or
  `PATCH /api/v1/settings` with `{"updates": {"mode": "manual"}}` as the
  platform role. `zoomies config set updates.mode manual` works only while the
  controller is stopped: with it running, it refuses, because the running
  process has already read the settings it would change.
* You are signed in with the platform role, and have a second account with the
  admin role and a third with the viewer role for the role checks.
* The helper is not installed yet. `zoomies updates helper status` says
  `The update helper is not installed on this host: there is no
  update-helper.json in /var/lib/zoomies-update or /etc/zoomies.`

## 1. The offer, and a refusal that is said at install

1. Run `sudo zoomies upgrade` at a terminal. After it has reviewed the
   deployment's additions it asks `Make this host ready to be updated from the
   web UI?`, as a question of its own, with the sentence that says what the
   helper is for and `Add the update helper? [y/N]`. Answer `n`: it prints
   `The update helper was not added.` and `Add later: sudo zoomies updates
   helper install`, and nothing appears under
   `/etc/systemd/system/zoomies-update.*` or `/var/lib/zoomies-update`.
2. Run `sudo zoomies upgrade --yes`. It must still leave the helper out and
   print the same `Add later:` line.
3. On a host whose binary lives under a folder its group can write (old
   Debian's `/usr/local`, `root:staff 2775`), run
   `sudo zoomies updates helper install`. It must refuse, saying the folder is
   writable by its group above the zoomies binary and which `chmod g-w` to run.
   Nothing is enabled.

## 2. Install the helper

1. `sudo zoomies updates helper install`. It says it wrote
   `/var/lib/zoomies-update/update-helper.json` (root's) and
   `/etc/zoomies/update-helper.json` (the copy the service reads), then
   `Installed and started zoomies-update.path, which runs zoomies-update.service
   as root when the service asks for an update`. The update folder (`update`
   under the state directory, `/var/lib/zoomies/update` for a root install)
   belongs to the account Zoomies runs as, and holds `helper.json`.
2. `systemctl status zoomies-update.path` is active (waiting).
   `systemctl cat zoomies-update.service` shows `Type=oneshot`,
   `TimeoutStartSec=2h`, `KillMode=mixed`, `StartLimitBurst=5` and no
   `EnvironmentFile`.
3. `zoomies updates helper status` shows the update folder, where it is
   recorded, the account and the binary, `Helper  installed <time>, by zoomies
   <version>`, and `Last result  none yet`.
4. Run the install again. It succeeds, and `helper.json` is left as it was: its
   `installed_at` does not move.
5. Settings → Updates, under **Update this controller**, now offers
   `Update to <tag>` to the platform role, and the `controller.update_helper_missing`
   warning is gone from Problems.

## 3. Press Update

1. Press **Update to <tag>** on Settings → Updates and read the
   confirmation. It is titled `Update the controller`, asks
   `Update this controller from <running> to <tag>?`, and says that the
   controller restarts, that jobs that are running keep running, and that a
   release which changes the database has a copy of it kept first (none is
   taken for a release that changes nothing there). Confirm.
2. In another terminal: `journalctl -u zoomies-update -f`. You should see
   `request upd_... asks for <tag>, from <your name> at <time>`, then
   `running <binary> upgrade --version <tag> --non-interactive --config-dir
   /etc/zoomies`, the engine's lines prefixed `engine:`, and
   `answered upd_...: updated from <old> to <new>`. The downloaded release
   checks the deployment with its own `zoomies upgrade --check` before
   anything is replaced; its output is shown only if it refuses.
3. The page shows `Updating to <tag>`, then, while the controller is down,
   `Waiting for the controller to answer`, and comes back on its own. The
   running version is the new one. Nothing says `Updated` before the
   controller is back.
4. `zoomies updates status` and `GET /api/v1/updates` show the attempt as
   `succeeded`, `/var/lib/zoomies/update` holds no `request.json`, and
   `zoomies updates helper status` shows the last result as `updated from
   <old> to <new>`.
5. Signed in as the admin and as the viewer, Settings → Updates shows no Update
   button and says `Updating the controller needs the platform role.`

## 4. Things that must go wrong cleanly

1. **A refused request.** As the account Zoomies runs as, write a request with
   a bad tag into the update folder, for example
   `sudo -u zoomies sh -c 'printf "%s" "{\"v\":1,\"id\":\"upd_manualcheck1\",\"tag\":\"latest\",\"requested_by\":\"me\",\"requested_at\":\"2026-10-09T09:00:00Z\"}" > /var/lib/zoomies/update/request.json'`.
   The path unit runs the helper at once. The journal says why it refused,
   `request.json` is gone, and `zoomies updates helper status` shows the
   refusal as the last result. The controller asked for none of this: it
   opens no attempt and raises no problem.
2. **An update that fails.** Make the download fail: block outbound HTTPS to
   `github.com` and `objects.githubusercontent.com` with a host firewall rule,
   press Update, and lift the rule once the attempt has ended. On a container
   deployment, renaming `deployment.json` out of the way makes the upgrade
   refuse the deployment instead; put it back afterwards. Settings → Updates
   shows the attempt as `The update to <tag> did not succeed`, with the
   helper's sentence for the platform role and
   `The update did not succeed. Whoever holds the platform role can read why.`
   for the admin and the viewer. `controller.update_failed` appears under
   Problems, and the Update button is offered again.
3. **The start limit.** Not on a host you need: write the bad request from
   step 1 six times within ten minutes. systemd refuses the sixth start of
   `zoomies-update.service` (`StartLimitBurst=5` in `StartLimitIntervalSec=10min`).
   Record what `systemctl status zoomies-update.service zoomies-update.path`
   say, then whether a later request is picked up after
   `sudo zoomies updates helper install`, which runs `systemctl reset-failed`
   on both units.
4. **Removing under work.** While an update is running,
   `sudo zoomies updates helper remove` must refuse, saying the helper is
   running an update now and that its trigger is off; `sudo zoomies uninstall`
   must refuse the whole uninstall. Once the update has finished,
   `sudo zoomies updates helper install` turns the trigger back on.
5. **Switching updating off mid-flight.** Press Update, then set `updates.mode`
   to `off` straight away. The update that is running finishes and is recorded,
   Settings → Updates says how it ended, and a new press is refused with
   `update.mode_off`.

## 5. A remote host

1. On the second host, `sudo zoomies agent join <controller-url> --token
   <join-token> --update-helper` (or `sudo zoomies updates helper install`
   after joining). `GET /api/v1/hosts/{id}` lists `self-update` among the
   host's `features` only once the helper is installed, and its `update` block
   has `can_update` true while the host is behind the controller.
2. Before the helper is installed, the host's card on **Hosts** says
   `Update by command`, with the sentence `This host's agent does not offer to
   update itself, which it does only once the update helper is installed on the
   host. Run sudo zoomies updates helper install there, or update it with the
   command below.` and a disabled Update button. The command to copy stays
   beneath it, and `host.update_unavailable` is listed under Problems.
3. With the helper installed, the card says `Can be updated` and its
   **Update** button is offered to the admin role (the operator sees only the
   command, the viewer neither). Press it. The confirmation is titled
   `Update the agent on <name>`, asks `Update the agent on <name> from <running>
   to <tag>?`, and says that the agent restarts, that running jobs keep running
   because their runners stay in place and the new agent takes them over, that
   the restart does not wait for them to finish, and that the page says the
   update is done only when the host reports the release. Confirm with
   `Update to <tag>`.
4. The card says `Updating` until the host reports the release, then `Updated`
   and `<name> runs <version> now.` `journalctl -u zoomies-update` on the host
   shows the request and the upgrade, and `GET /api/v1/hosts/{id}` shows the
   attempt as `succeeded`.
5. Start a long job on the host and press Update while it runs. The agent
   restarts without waiting for the job; the job's runner keeps its process,
   the new agent takes it over, and the job finishes on its own.
6. Make it fail (block the download on the host as in section 4). The card says
   `Failed` with a `Try again` button, the helper's sentence for the platform
   role and a fixed sentence for every other role, and `host.update_failed` is
   listed under Problems.
7. Take the host offline straight after pressing Update and bring it back
   within 90 minutes: the task waits for the agent's next poll, the host is
   updated once, and the attempt succeeds. Keep it offline for more than 90
   minutes instead: the attempt ends as `Timed out`, `host.update_failed` says
   so, and the host is not updated when it comes back.

## 6. Where the helper can never be installed

Each of these must show the reason and an upgrade command, and suggest no
install anywhere: not on the page, not in a problem, not in a refusal.

1. **A controller-only container.** A Compose deployment with the embedded
   agent off (`agent.embedded: false`) does not mount the shared folder.
   Settings → Updates, under **Update this controller**, says `The update helper
   cannot be installed here: this controller runs in a container that runs no
   runners, so the container does not mount the shared folder the update helper
   would read a request from. Update this controller on its host with the
   command below.`, with **Copy the upgrade command** for
   `sudo zoomies upgrade`, no install command and no Update button. Problems
   has no `controller.update_helper_missing`. `zoomies updates status` shows
   the helper as `unsupported` with an `Upgrade` row, and
   `POST /api/v1/updates/controller` is refused with `update.helper_missing`
   and the same reason.
2. **A controller under a rootless runtime.** A container deployment with its
   embedded agent on, under rootless Podman or rootless Docker: the same, with a
   reason that says it runs in a container under a rootless runtime.
3. **A controller on a host systemd does not run, or on macOS.** The same,
   with a reason that names systemd, or Linux.
4. **A host systemd does not run.** An agent joined with `--no-service` on a
   host without systemd and started by hand with `zoomies agent`, one release
   behind. Its card says `Update by command` with `The update helper cannot be
   installed on this host: its agent runs on a host that systemd does not run,
   and the update helper is a pair of systemd units. Update it on the host with
   the command below.`, draws no Update button, and keeps the command beneath.
   `GET /api/v1/hosts/{id}` shows `update.state` as `unsupported`, and
   `host.update_unavailable` is listed as a note whose fix is the command on
   the card. `POST /api/v1/hosts/{id}/update` is refused with
   `update.host_cannot_update` and the card's sentence.
5. **A Mac, and an agent too old to say why.** An agent on macOS shows the
   same, with a reason that names Linux. An agent from before this release
   on a Linux host without systemd says nothing about its helper, and is shown
   as before: missing, with the install command.
6. **An agent in a container under a rootless runtime.** Shown as unsupported,
   with a reason that names the rootless runtime.
7. **A controller restart.** Restart the controller while the host in step 4
   is behind. For up to one heartbeat its card offers the install command, as it
   would for an older agent; after the next heartbeat it says why again.

## What to send back

For each section: the version before and after, `journalctl -u zoomies-update`
for the run, `systemctl status zoomies-update.path`, the card or page as it
read, and anything that surprised you. The places most likely to differ from
the tests are the unit's sandbox (`ProtectHome=read-only` against `~/.docker`
and Podman's rootless storage), `KillMode=mixed` while `zoomies upgrade`
restarts the service, whether the path unit keeps watching after the service
reaches its start limit, and, for section 6, whether a real host's container
and systemd look the way the controller and the agent read them.
