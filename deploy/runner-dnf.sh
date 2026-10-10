#!/bin/sh
# Install required RPM packages, recovering from repository metadata skew.
# Rocky 9's BaseOS and AppStream can temporarily disagree: libxml2-devel was
# advertised before its exact-version libxml2 dependency was available.
# Refresh metadata before retrying; only the final attempt permits an older
# compatible candidate. Never skip broken/missing packages or signature checks.
# Usage: runner-dnf.sh [upgrade] [options...] [packages...]
set -eu

command=install
if [ "${1:-}" = upgrade ]; then
  command=upgrade
  shift
fi

if [ "$command" = install ] && [ "$#" -eq 0 ]; then
  echo "runner-dnf.sh: no packages named" >&2
  exit 64
fi

attempt=1
while :; do
  case "$attempt" in
    1) if dnf "$command" -y "$@"; then exit 0; fi ;;
    2) if dnf --refresh "$command" -y "$@"; then exit 0; fi ;;
    3)
      echo "runner-dnf.sh: trying compatible available versions; all requested packages remain required" >&2
      if dnf --refresh --nobest "$command" -y "$@"; then exit 0; fi
      echo "runner-dnf.sh: dnf install failed 3 times; giving up" >&2
      exit 1
      ;;
  esac
  delay=$((attempt * 10))
  echo "runner-dnf.sh: install failed; refreshing metadata in ${delay}s (attempt $((attempt + 1)) of 3)" >&2
  sleep "$delay"
  attempt=$((attempt + 1))
done
