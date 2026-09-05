#!/usr/bin/env bash
#
# Zoomies runner entrypoint.
#
# This is the other half of the contract in internal/backend: whatever the
# backend sets here is what the runner uses. Keep the two in step.
#
#   ZOOMIES_JITCONFIG      base64 JIT configuration (preferred; ephemeral)
#   ZOOMIES_RUNNER_URL     org or repo URL          (registration-token path)
#   ZOOMIES_RUNNER_TOKEN   registration token       (registration-token path)
#   ZOOMIES_RUNNER_NAME    runner name
#   ZOOMIES_RUNNER_LABELS  comma-separated labels
#   ZOOMIES_RUNNER_GROUP   runner group name
#   ZOOMIES_EPHEMERAL      "true" to pass --ephemeral
#
set -euo pipefail

cd /home/runner

log() { printf '%s zoomies-runner: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

# The runner treats SIGINT as "finish the current job, then exit", which is
# exactly what a Zoomies drain means. Forward it rather than letting the shell
# die and orphan the runner.
child=0
# shellcheck disable=SC2317  # invoked by trap, which shellcheck cannot see
forward() {
  if [ "$child" -ne 0 ]; then
    log "forwarding $1 to the runner (it will finish its current job first)"
    kill -"$1" "$child" 2>/dev/null || true
  fi
}
trap 'forward INT'  INT
trap 'forward TERM' TERM

if [ -n "${ZOOMIES_JITCONFIG:-}" ]; then
  log "starting with a just-in-time configuration (ephemeral, single use)"
  ./run.sh --jitconfig "${ZOOMIES_JITCONFIG}" &
  child=$!
elif [ -n "${ZOOMIES_RUNNER_TOKEN:-}" ]; then
  : "${ZOOMIES_RUNNER_URL:?ZOOMIES_RUNNER_URL is required alongside ZOOMIES_RUNNER_TOKEN}"
  log "registering ${ZOOMIES_RUNNER_NAME:-unnamed} against ${ZOOMIES_RUNNER_URL}"

  args=(
    --unattended
    --replace
    --url "${ZOOMIES_RUNNER_URL}"
    --token "${ZOOMIES_RUNNER_TOKEN}"
    --name "${ZOOMIES_RUNNER_NAME:-$(hostname)}"
    --work /home/runner/_work
  )
  [ -n "${ZOOMIES_RUNNER_LABELS:-}" ] && args+=(--labels "${ZOOMIES_RUNNER_LABELS}")
  [ -n "${ZOOMIES_RUNNER_GROUP:-}" ]  && args+=(--runnergroup "${ZOOMIES_RUNNER_GROUP}")
  [ "${ZOOMIES_EPHEMERAL:-false}" = "true" ] && args+=(--ephemeral)

  ./config.sh "${args[@]}"

  # Deregister on the way out so a crashed container does not leave a ghost
  # runner in the GitHub UI. Ephemeral runners deregister themselves, so this
  # only matters on the persistent path.
  # shellcheck disable=SC2317  # invoked by trap, which shellcheck cannot see
  cleanup() {
    log "removing this runner's registration"
    ./config.sh remove --token "${ZOOMIES_RUNNER_TOKEN}" >/dev/null 2>&1 || true
  }
  trap cleanup EXIT

  ./run.sh &
  child=$!
else
  log "no credentials supplied."
  log "set ZOOMIES_JITCONFIG, or ZOOMIES_RUNNER_URL together with ZOOMIES_RUNNER_TOKEN."
  exit 64
fi

# wait returns early when a trap fires, so loop until the child is really gone.
#
# Two things the obvious `while ! wait "$child"; do :; done` gets wrong: bash
# answers a second wait on a pid it has already reaped with 127, so a runner
# that exits non-zero would spin here for ever; and the loop's own status is
# what `$?` reports afterwards, so the exit code was always 0. The controller
# reads that code to tell "finished" from "failed", which is the whole point of
# passing it on.
status=0
while :; do
  if wait "$child"; then
    status=0
    break
  else
    status=$?
  fi
  # Above 128 is either a trap interrupting wait or the child dying of a
  # signal; only keep waiting if the child is in fact still there.
  if [ "$status" -le 128 ] || ! kill -0 "$child" 2>/dev/null; then
    break
  fi
done
log "runner exited with status ${status}"
exit "$status"
