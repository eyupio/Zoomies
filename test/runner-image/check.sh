#!/bin/sh
#
# Check that a built runner image contains what Zoomies relies on.
#
#   usage: check.sh <image> <os> <version> <runner|runner-docker|runner-full>
#
#   e.g.   check.sh ghcr.io/eyupio/zoomies-runner:debian-12 debian 12 runner
#
# The build already proves a few tools are on PATH. What it cannot prove is the
# contract the rest of the system assumes about the finished image: the uid the
# Docker backend sets a work mount up for, the files the entrypoint execs, the
# runner release the Dockerfile pinned, and that a variant is the operating
# system its tag and its catalogue row say it is. Any of those going missing
# otherwise shows up as a runner that will not start in somebody's pool, which
# is a worse place to find out than this job.
#
# Every expectation below names where it comes from, so a change to that source
# and a failure here arrive in the same review. <os> and <version> are the
# catalogue row's (internal/naming/images.go), passed straight from the
# generated workflow matrix or the Makefile's variant rows, so nothing here
# repeats the catalogue.
#
# No goss or similar: every assertion is one command inside the image, and a
# shell script is the smallest thing that runs one.
set -eu

usage="usage: check.sh <image> <os> <version> <runner|runner-docker|runner-full>"
image="${1:?${usage}}"
os="${2:?${usage}}"
version="${3:?${usage}}"
target="${4:?${usage}}"

case "${target}" in
  runner|runner-docker|runner-full) ;;
  *) echo "check.sh: unknown target '${target}'; expected runner, runner-docker or runner-full" >&2; exit 64 ;;
esac

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "${here}/../.." && pwd)"

# The runner release the Dockerfile pins, unless the build overrode it.
runner_version="${RUNNER_VERSION:-$(sed -n 's/^ARG RUNNER_VERSION=//p' "${root}/deploy/Dockerfile.runner")}"
[ -n "${runner_version}" ] || { echo "check.sh: cannot read RUNNER_VERSION from deploy/Dockerfile.runner" >&2; exit 1; }

failures=0
pass() { printf '  ok    %s\n' "$*"; }
fail() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
expect() { # expect <what> <want> <got>
  if [ "$2" = "$3" ]; then pass "$1: $3"; else fail "$1: want '$2', got '$3'"; fi
}

echo "checking ${image} (${os} ${version}, ${target}, actions/runner ${runner_version})"

# What runner-full must carry, read from the lock it was built from rather than
# repeated here: each tool-cache entry as <Tool>/<version>/<arch>, and the .NET
# SDKs by version. Only this image's architecture and Ubuntu release apply.
want_toolcache=""
want_dotnet=""
if [ "${target}" = runner-full ]; then
  image_arch="$(docker image inspect --format '{{.Architecture}}' "${image}")"
  case "${image_arch}" in amd64) tc_arch=x64 ;; *) tc_arch="${image_arch}" ;; esac
  while read -r t v plat a _digest _url; do
    case "${t}" in ''|'#'*) continue ;; esac
    [ "${plat}" = - ] || [ "${plat}" = "${version}" ] || continue
    [ "${a}" = - ] || [ "${a}" = "${image_arch}" ] || continue
    case "${t}" in
      python) want_toolcache="${want_toolcache} Python/${v}/${tc_arch}" ;;
      node) want_toolcache="${want_toolcache} node/${v}/${tc_arch}" ;;
      go) want_toolcache="${want_toolcache} go/${v}/${tc_arch}" ;;
      java) want_toolcache="${want_toolcache} Java_Temurin-Hotspot_jdk/${v}/${tc_arch}" ;;
      dotnet) want_dotnet="${want_dotnet} ${v}" ;;
    esac
  done < "${root}/deploy/toolcache.lock"
  [ -n "${want_toolcache}" ] || { echo "check.sh: deploy/toolcache.lock has nothing for ${version} ${image_arch}" >&2; exit 1; }
fi

# --- image metadata ----------------------------------------------------------

inspect() { docker image inspect --format "$1" "${image}"; }

# internal/backend/docker.go runs the container without overriding its user and
# relies on the image to be uid 1001; the Dockerfile's USER line is what does it.
expect "image user" "runner" "$(inspect '{{.Config.User}}')"
expect "image entrypoint" "[/usr/local/bin/entrypoint.sh]" "$(inspect '{{json .Config.Entrypoint}}' | tr -d '"')"
expect "label io.zoomies.os" "${os}" "$(inspect '{{index .Config.Labels "io.zoomies.os"}}')"
expect "label io.zoomies.os-version" "${version}" "$(inspect '{{index .Config.Labels "io.zoomies.os-version"}}')"
expect "label io.zoomies.runner-version" "${runner_version}" "$(inspect '{{index .Config.Labels "io.zoomies.runner-version"}}')"

# --- inside the image --------------------------------------------------------
#
# One container for every assertion, so the check costs one start rather than
# forty. It prints "ok"/"FAIL" lines itself and exits with the failure count.

inside=$(cat <<'EOF'
failures=0
pass() { printf '  ok    %s\n' "$*"; }
fail() { printf '  FAIL  %s\n' "$*"; failures=$((failures + 1)); }
has() { if command -v "$1" >/dev/null 2>&1; then pass "$1 on PATH"; else fail "$1 is not on PATH"; fi; }
runs() { # runs <description> <command...>
  what="$1"; shift
  if "$@" >/dev/null 2>&1; then pass "${what}"; else fail "${what}: '$*' failed"; fi
}

runs "installed software inventory" jq -e '.schema == 1 and (.packages | length > 0) and (.tool_cache_entries | type == "array")' /usr/local/share/zoomies/installed-software.json

# The runner user: uid and gid 1001 (Dockerfile.runner), which the Docker
# backend's work mount and socket handling assume (internal/backend/docker.go),
# with the passwordless sudo the Dockerfile promises workflows.
[ "$(id -un)" = runner ] && pass "running as runner" || fail "running as $(id -un), not runner"
[ "$(id -u)" = 1001 ] && pass "uid 1001" || fail "uid is $(id -u), not 1001"
[ "$(id -g)" = 1001 ] && pass "gid 1001" || fail "gid is $(id -g), not 1001"
[ ! -r /etc/shadow ] && pass "shadow is not readable by runner" || fail "runner can read /etc/shadow without sudo"
if sudo -n true 2>/tmp/zoomies-sudo-error; then
  pass "passwordless sudo"
else
  detail="$(cat /tmp/zoomies-sudo-error)"
  fail "passwordless sudo: ${detail:-sudo -n true failed without an error message}"
fi
[ "${RUNNER_ALLOW_RUNASROOT:-}" = 0 ] && pass "RUNNER_ALLOW_RUNASROOT=0" || fail "RUNNER_ALLOW_RUNASROOT is '${RUNNER_ALLOW_RUNASROOT:-}'"
# The proxy the agent hands a runner (proxyEnvKeys in internal/backend/proxy.go)
# has to survive sudo, or a job's `sudo apt-get install` loses it. The two
# spellings are checked because the Dockerfile's env_keep lists each by name.
if kept="$(HTTPS_PROXY=http://proxy.invalid:3128 no_proxy=.invalid sudo -n env 2>&1)"; then
  :
else
  fail "sudo environment probe failed: $kept"
  kept=""
fi
for v in HTTPS_PROXY=http://proxy.invalid:3128 no_proxy=.invalid; do
  printf '%s\n' "${kept}" | grep -qx "$v" && pass "sudo keeps ${v%%=*}" || fail "sudo drops ${v%%=*}"
done

# The tool cache the Dockerfile names, which the setup-* actions unpack into,
# and the mount point of a pool's cache (RunnerCacheMount in
# internal/backend/docker.go), whose owner a fresh named volume inherits.
[ "${AGENT_TOOLSDIRECTORY:-}" = /opt/hostedtoolcache ] && pass "AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache" || fail "AGENT_TOOLSDIRECTORY is '${AGENT_TOOLSDIRECTORY:-}'"
for d in /opt/hostedtoolcache /opt/zoomies-cache; do
  [ -d "$d" ] && [ -w "$d" ] && pass "$d is writable" || fail "$d is missing or not writable by the runner"
done

# What the entrypoint execs (deploy/runner-entrypoint.sh cds to /home/runner and
# runs ./config.sh and ./run.sh) and the work directory it and the backend name
# (RunnerWorkMount in internal/backend/docker.go).
[ -x /usr/local/bin/entrypoint.sh ] && pass "entrypoint is executable" || fail "/usr/local/bin/entrypoint.sh is missing or not executable"
for f in config.sh run.sh bin/Runner.Listener bin/Runner.Worker bin/installdependencies.sh; do
  [ -x "/home/runner/$f" ] && pass "/home/runner/$f" || fail "/home/runner/$f is missing or not executable"
done
[ -d /home/runner/_work ] && [ -w /home/runner/_work ] && pass "/home/runner/_work is writable" || fail "/home/runner/_work is missing or not writable by the runner"
[ -w /home/runner ] && pass "/home/runner is writable (config.sh writes .runner there)" || fail "/home/runner is not writable by the runner"

# The runner release the Dockerfile pinned, asked of the binary itself rather
# than of a label that could have been stamped without it. The image sets
# ACTIONS_RUNNER_PRINT_LOG_TO_STDOUT so a runner's log reaches the container's,
# and with it set the listener prints its whole log around the version -- the
# last line is "Runner execution has finished", not the version. Unset for
# this one question, and take the line shaped like a version rather than the
# last, so a log line the listener adds later cannot stand in for it.
got="$(cd /home/runner && env -u ACTIONS_RUNNER_PRINT_LOG_TO_STDOUT ./bin/Runner.Listener --version 2>/dev/null | tr -d '\r' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | tail -n 1)"
[ "${got}" = "${WANT_RUNNER}" ] && pass "actions/runner ${got}" || fail "actions/runner reports '${got}', want '${WANT_RUNNER}'"

# The platform: /etc/os-release is what the distribution says, the environment
# is what the entrypoint's first log line prints. Both must match the row.
. /etc/os-release
[ "${ID}" = "${WANT_OS}" ] && pass "os-release ID=${ID}" || fail "os-release ID is '${ID}', want '${WANT_OS}'"
# Rocky reports 9.6 for a row that says 9; Ubuntu and Debian report the row exactly.
case "${VERSION_ID}" in
  "${WANT_VERSION}"|"${WANT_VERSION}".*) pass "os-release VERSION_ID=${VERSION_ID}" ;;
  *) fail "os-release VERSION_ID is '${VERSION_ID}', want '${WANT_VERSION}'" ;;
esac
[ "${ZOOMIES_RUNNER_OS:-}" = "${WANT_OS}" ] && pass "ZOOMIES_RUNNER_OS" || fail "ZOOMIES_RUNNER_OS is '${ZOOMIES_RUNNER_OS:-}'"
[ "${ZOOMIES_RUNNER_OS_VERSION:-}" = "${WANT_VERSION}" ] && pass "ZOOMIES_RUNNER_OS_VERSION" || fail "ZOOMIES_RUNNER_OS_VERSION is '${ZOOMIES_RUNNER_OS_VERSION:-}'"
[ "${ZOOMIES_RUNNER_VERSION:-}" = "${WANT_RUNNER}" ] && pass "ZOOMIES_RUNNER_VERSION" || fail "ZOOMIES_RUNNER_VERSION is '${ZOOMIES_RUNNER_VERSION:-}'"

# The baseline from deploy/runner-packages.sh, plus what the entrypoint itself
# calls (bash, timeout, date, uname, hostname).
for t in bash git curl jq tar unzip zip gzip xz sudo ssh rsync timeout date uname hostname; do has "$t"; done
# The toolchain from deploy/runner-toolchain.sh, deploy/runner-gh.sh and deploy/runner-shellcheck.sh.
for t in cc make cmake python3 node npm git-lfs gh shellcheck; do has "$t"; done
# libxml2's headers and runtime must be a compatible pair. Package metadata
# skew once broke Rocky release builds; prove recovery leaves a usable library.
runs "libxml2 development package" pkg-config --exists libxml-2.0
xml_test_dir=$(mktemp -d)
cat > "$xml_test_dir/check.c" <<'XML'
#include <libxml/parser.h>
int main(void) {
    xmlInitParser();
    xmlCleanupParser();
    return 0;
}
XML
# shellcheck disable=SC2046 # pkg-config emits compiler/linker argument lists
if cc $(pkg-config --cflags libxml-2.0) "$xml_test_dir/check.c" \
    -o "$xml_test_dir/check" $(pkg-config --libs libxml-2.0) && "$xml_test_dir/check"; then
  pass "libxml2 compiles, links and runs"
else
  fail "libxml2 headers/runtime cannot build a program"
fi
rm -rf "$xml_test_dir"
# ca-certificates: a bundle at either family's path, and one curl can use.
if [ -s /etc/ssl/certs/ca-certificates.crt ] || [ -s /etc/pki/tls/certs/ca-bundle.crt ]; then
  pass "CA bundle present"
else
  fail "no CA bundle at /etc/ssl/certs/ca-certificates.crt or /etc/pki/tls/certs/ca-bundle.crt"
fi
# The UTF-8 locale LANG names; without it a job's non-ASCII output is mangled.
if locale -a 2>/dev/null | grep -qi '^en_US\.utf-\?8$'; then pass "en_US.UTF-8 locale"; else fail "en_US.UTF-8 locale is not installed"; fi

# The Docker client is the whole difference between the first two targets, and
# runner-full is built on runner-docker.
if [ "${WANT_TARGET}" = runner-docker ] || [ "${WANT_TARGET}" = runner-full ]; then
  runs "docker --version" docker --version
  runs "docker buildx version" docker buildx version
  runs "docker compose version" docker compose version
else
  # The plain image leaves the client out on purpose (Dockerfile.runner), and
  # the controller's pool.docker_client_missing is written for exactly this
  # image: one that grew a client would make that problem wrong.
  if command -v docker >/dev/null 2>&1; then fail "docker is on PATH in the plain runner image"; else pass "no docker client, as intended"; fi
fi

# runner-full: every tool-cache entry the lock names, finished -- a setup-*
# action ignores a directory without its .complete marker and downloads the
# version again -- and the toolchains that live outside the tool cache.
if [ "${WANT_TARGET}" = runner-full ]; then
  for e in ${WANT_TOOLCACHE}; do
    if [ -d "${AGENT_TOOLSDIRECTORY}/${e}" ] && [ -f "${AGENT_TOOLSDIRECTORY}/${e}.complete" ]; then
      pass "tool cache has ${e}"
    else
      fail "tool cache is missing ${e}, or its .complete marker"
    fi
  done
  sdks="$(dotnet --list-sdks 2>/dev/null)"
  for v in ${WANT_DOTNET}; do
    printf '%s\n' "${sdks}" | grep -q "^${v} " && pass ".NET SDK ${v}" || fail ".NET SDK ${v} is not installed"
  done
  for t in java mvn gradle dotnet rustup rustc cargo; do has "$t"; done
  [ -w /usr/share/dotnet ] && pass "/usr/share/dotnet is writable (setup-dotnet installs there)" || fail "/usr/share/dotnet is not writable by the runner"
  [ -w "${RUSTUP_HOME:-/nonexistent}" ] && pass "RUSTUP_HOME is writable" || fail "RUSTUP_HOME '${RUSTUP_HOME:-}' is not writable by the runner"
fi

exit "${failures}"
EOF
)

set +e
docker run --rm --entrypoint /bin/bash \
  -e WANT_OS="${os}" -e WANT_VERSION="${version}" \
  -e WANT_RUNNER="${runner_version}" -e WANT_TARGET="${target}" \
  -e WANT_TOOLCACHE="${want_toolcache}" -e WANT_DOTNET="${want_dotnet}" \
  "${image}" -c "${inside}"
status=$?
set -e
# 125 and above is docker or the shell failing to start at all, not a count.
if [ "${status}" -ge 125 ]; then
  fail "could not run /bin/bash in the image (exit ${status})"
else
  failures=$((failures + status))
fi

# --- the entrypoint, end to end ----------------------------------------------
#
# With no credentials the entrypoint must refuse with 64 and say why. That is
# the one run that proves the real ENTRYPOINT starts under the image's own user
# and gets as far as its credential check -- a missing bash, a wrong shebang or
# a lost execute bit all fail here rather than at a job.
set +e
out="$(docker run --rm "${image}" 2>&1)"
status=$?
set -e
if [ "${status}" = 64 ] && printf '%s' "${out}" | grep -q 'no credentials supplied'; then
  pass "entrypoint refuses to start without credentials (exit 64)"
else
  fail "entrypoint without credentials: want exit 64 and 'no credentials supplied', got exit ${status}:"
  printf '%s\n' "${out}" | sed 's/^/        /'
fi

if [ "${failures}" -ne 0 ]; then
  echo "${image}: ${failures} check(s) failed" >&2
  exit 1
fi
echo "${image}: every check passed"
