#!/bin/sh
# Zoomies installer -- off the lead, on the job.
#
#   curl -fsSL https://zoomies.sh/install.sh | sh
#
# It is also perfectly safe to download and read this first, which is the
# recommended way:
#
#   curl -fsSLO https://zoomies.sh/install.sh && less install.sh && sh install.sh
#
# What it does, in order:
#   1. works out your OS, architecture, container runtime, compose command and
#      init system
#   2. downloads the right binary and verifies its SHA-256 against the
#      published checksums file
#   3. installs it to /usr/local/bin
#   4. hands off to `zoomies init`, which does the interactive setup and can
#      deploy Zoomies three ways:
#
#        native   the binary under systemd (or launchd), which is the leanest
#                 option and the one that needs no container runtime at all
#        compose  a docker-compose.yml and a fully populated .env, brought up
#                 with `docker compose up -d`
#        docker   a single `docker run` container with an env file
#
#      Either way `zoomies init` does the rest: service user, directories,
#      encryption key, backend, the GitHub App, and your admin account. It
#      also asks how the controller is reached: loopback, a certificate you
#      have, a self-signed one, or a reverse proxy.
#
#      Behind Cloudflare, pick "Cloudflare in front" (or put
#      `trusted_proxies: [cloudflare]` in the answer file): TLS stays off
#      here, Cloudflare's published ranges are trusted as proxies, and audit
#      entries and rate limits record the real client instead of Cloudflare.
#
# POSIX sh. No bashisms -- it is checked with dash and shellcheck in CI.

set -eu

VERSION="${ZOOMIES_VERSION:-latest}"
REPO="${ZOOMIES_REPO:-eyupio/zoomies}"
PREFIX="${ZOOMIES_PREFIX:-/usr/local/bin}"
BASE_URL="${ZOOMIES_BASE_URL:-https://github.com/${REPO}/releases}"

MODE=""
DEPLOYMENT=""
CONTROLLER_URL=""
JOIN_TOKEN=""
NON_INTERACTIVE=0
ANSWERS=""
RUN_INIT=1
DO_UNINSTALL=0
ASSUME_YES=0
ALLOW_UNVERIFIED=0
EXISTING_OTHER=""
# Something worked out during argument parsing that belongs in the "Looking
# around" report rather than above the banner.
note_deferred=""

# ---------------------------------------------------------------------------
# Output
# ---------------------------------------------------------------------------

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ]; then
    C_RESET=$(printf '\033[0m')
    C_DIM=$(printf '\033[2m')
    C_BOLD=$(printf '\033[1m')
    C_ACCENT=$(printf '\033[38;5;99m')
    C_OK=$(printf '\033[32m')
    C_WARN=$(printf '\033[33m')
    C_ERR=$(printf '\033[31m')
else
    C_RESET='' C_DIM='' C_BOLD='' C_ACCENT='' C_OK='' C_WARN='' C_ERR=''
fi

# Every line in this script starts in the same three-column gutter, so the
# shape of the output is scannable before a word of it is read: `->` for a
# step, `ok` for a result, `!!` for something the operator should look at.
# Errors are the one message that has to be easiest to find, so they share the
# gutter rather than starting at column 1 the way they used to.
say()  { printf '%s\n' "$*"; }
step() { printf '%s->%s %s\n' "$C_ACCENT" "$C_RESET" "$*"; }
ok()   { printf '%s   ok%s %s\n' "$C_OK" "$C_RESET" "$*"; }
note() { printf '%s      %s%s\n' "$C_DIM" "$*" "$C_RESET"; }
warn() { printf '%s   !!%s %s\n' "$C_WARN" "$C_RESET" "$*" >&2; }
die()  { printf '%s   xx%s %s\n' "$C_ERR" "$C_RESET" "$1" >&2; shift; hint "$@"; exit 1; }
# hint prints the continuation lines of a warning or an error at the gutter's
# own indent. The alternative -- six literal spaces inside the message string --
# drifts silently the moment the gutter changes, and it did.
hint() { for h in "$@"; do printf '%s      %s%s\n' "$C_DIM" "$h" "$C_RESET" >&2; done; }

# A key/value line in the "Checking this host" table. One helper so no call
# site hand-counts spaces, which is what let the port lines drift out of the
# column everything else lines up in.
field() { k="$1"; shift; printf '%s      %-12s%s%s\n' "$C_DIM" "$k" "$*" "$C_RESET"; }

# Three lines, and every one of them says something. The dog is the mark; the
# tagline is the product; the third line is where to look when this goes wrong.
banner() {
    # The mark is three characters no ASCII terminal has. A dumb terminal, or a
    # locale that is not UTF-8, gets mojibake where the brand should be -- so it
    # gets a plain stand-in instead.
    mark='⟋●⟍'
    case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in
        *UTF-8*|*utf8*|*UTF8*|*utf-8*) ;;
        *) mark='[o]' ;;
    esac
    [ "${TERM:-dumb}" != "dumb" ] || mark='[o]'
    printf '\n%s%s   %s%s   %s%sZoomies%s  %soff the lead, on the job%s\n' \
        "$C_BOLD" "$C_ACCENT" "$mark" "$C_RESET" "$C_BOLD" "$C_ACCENT" "$C_RESET" "$C_DIM" "$C_RESET"
    printf '%s        %s  ephemeral GitHub Actions runners that clean up after themselves%s\n' \
        "$C_DIM" "$C_RESET" "$C_RESET"
    printf '%s           https://github.com/%s%s\n\n' "$C_DIM" "$REPO" "$C_RESET"
}

usage() {
    cat <<'EOF'
Zoomies installer

Usage:
  install.sh [options]

Options:
  --mode <single|controller|agent>
                        What to install. "single" is a controller with an
                        embedded agent -- one process, one VM, the common case.
                        Asked interactively if omitted.
  --deployment <native|compose|docker>
                        How to run it. "native" is the binary under systemd,
                        "compose" writes a docker-compose.yml and a populated
                        .env, "docker" runs a single container. Asked
                        interactively if omitted, and only the ones this host
                        can actually run are offered.
  --controller <url>    For --mode agent: the controller to join.
  --join-token <token>  For --mode agent: a join token from the UI or
                        `zoomies hosts join-token create`.
  --version <v>         Release to install (default: latest).
  --prefix <dir>        Where to put the binary (default: /usr/local/bin).
  --non-interactive     Never prompt. Requires --answers, or enough flags.
  --answers <file>      YAML answer file for unattended setup. Implies
                        --non-interactive.
  --no-init             Install the binary only; do not run `zoomies init`.
  --yes, -y             Do not ask before installing. Implied by
                        --non-interactive.
  --allow-unverified    Install even when the download's SHA-256 cannot be
                        checked against the release's checksums.txt. Only for
                        a private mirror that does not publish one.
  --uninstall           Run `zoomies uninstall`, then remove the binary.
  --help, -h            This.

Environment:
  ZOOMIES_VERSION, ZOOMIES_PREFIX, ZOOMIES_REPO, ZOOMIES_BASE_URL
  NO_COLOR              Disable colour.

Examples:
  # A single VM, interactive
  curl -fsSL https://zoomies.sh/install.sh | sh

  # Add a runner host in one line
  curl -fsSL https://zoomies.sh/install.sh | sh -s -- \
      --mode agent --controller https://zoomies.example.com --join-token zoojoin_...

  # Unattended
  sh install.sh --non-interactive --answers /etc/zoomies/answers.yaml

  # Behind Cloudflare, unattended: the same answer file, with the listener
  # told that Cloudflare terminates TLS and may speak for the client:
  #   bind: 0.0.0.0:8080
  #   tls:
  #     mode: "off"
  #   trusted_proxies: [cloudflare]
  # Interactively, `zoomies init` offers "Cloudflare in front" at the
  # reachability question and writes all three for you.
EOF
}

# A flag whose value is missing is a typo, and it deserves the same error
# format as everything else here. `${2:?...}` reports it as "install.sh: 154:
# 2: --version needs a value", which names the shell's positional parameter
# rather than the flag the operator typed, and exits 2 instead of 1.
needs_value() {
    # needs_value <flag> <args-remaining> <what it takes>
    [ "$2" -ge 2 ] || die "$1 needs a value: $3."
}

while [ $# -gt 0 ]; do
    case "$1" in
        --mode)        needs_value --mode $# "single, controller or agent"; MODE="$2"; shift 2 ;;
        --mode=*)      MODE="${1#*=}"; shift ;;
        --deployment)  needs_value --deployment $# "native, compose or docker"; DEPLOYMENT="$2"; shift 2 ;;
        --deployment=*) DEPLOYMENT="${1#*=}"; shift ;;
        --controller)  needs_value --controller $# "the controller's URL"; CONTROLLER_URL="$2"; shift 2 ;;
        --controller=*) CONTROLLER_URL="${1#*=}"; shift ;;
        --join-token)  needs_value --join-token $# "a token that starts zoojoin_"; JOIN_TOKEN="$2"; shift 2 ;;
        --join-token=*) JOIN_TOKEN="${1#*=}"; shift ;;
        --version)     needs_value --version $# "a release tag such as v1.2.3, or latest"; VERSION="$2"; shift 2 ;;
        --version=*)   VERSION="${1#*=}"; shift ;;
        --prefix)      needs_value --prefix $# "a directory to install the binary into"; PREFIX="$2"; shift 2 ;;
        --prefix=*)    PREFIX="${1#*=}"; shift ;;
        --answers)     needs_value --answers $# "a path to a YAML answer file"; ANSWERS="$2"; NON_INTERACTIVE=1; shift 2 ;;
        --answers=*)   ANSWERS="${1#*=}"; NON_INTERACTIVE=1; shift ;;
        # --yes is implied, and has to be: an unattended run has nobody to
        # answer a confirmation, and the one that guards a same-version
        # reinstall would otherwise exit 0 without ever reaching `zoomies init`
        # -- reporting a host as configured that was never touched.
        --non-interactive) NON_INTERACTIVE=1; ASSUME_YES=1; shift ;;
        --no-init)     RUN_INIT=0; shift ;;
        --uninstall)   DO_UNINSTALL=1; shift ;;
        -y|--yes)      ASSUME_YES=1; shift ;;
        --allow-unverified) ALLOW_UNVERIFIED=1; shift ;;
        -h|--help)     usage; exit 0 ;;
        *)             die "unknown option: $1 (try --help)" ;;
    esac
done

# ---------------------------------------------------------------------------
# The answers we already have, checked before anything is downloaded
#
# A value this script does not understand used to travel all the way through a
# release lookup, a 25 MB download and a system write before `zoomies init`
# rejected it. Every check below is one this script can make in a millisecond,
# so it makes it first.
# ---------------------------------------------------------------------------

case "$MODE" in
    ""|single|controller|agent) ;;
    *) die "\"$MODE\" is not an install mode; use single, controller or agent." \
            "single      a controller with an embedded agent -- one VM, the common case" \
            "controller  a controller that runner hosts join" \
            "agent       a runner host that joins an existing controller" ;;
esac

case "$DEPLOYMENT" in
    ""|native|compose|docker) ;;
    *) die "\"$DEPLOYMENT\" is not a deployment; use native, compose or docker." \
            "native   the binary under systemd or launchd" \
            "compose  a docker-compose.yml and a populated .env" \
            "docker   a single container with an env file" ;;
esac

case "$VERSION" in
    *[!A-Za-z0-9._-]*) die "--version $VERSION does not look like a release tag. Try latest, or v1.2.3." ;;
esac

if [ -n "$ANSWERS" ] && [ ! -r "$ANSWERS" ]; then
    die "the answer file $ANSWERS cannot be read." \
        "\`zoomies init --print-answers\` writes an annotated one you can start from."
fi

# An agent has nothing to prompt for and nothing to guess: it needs a
# controller to join and a token to join it with. Interactively `zoomies init`
# asks; with no terminal there is nobody to ask, so say it now rather than
# after the download.
if [ "$MODE" = agent ] && [ "$NON_INTERACTIVE" -eq 1 ] && [ -z "$ANSWERS" ]; then
    [ -n "$CONTROLLER_URL" ] ||
        die "--mode agent --non-interactive also needs --controller <url>." \
            "It is the address the Hosts -> Add a host page shows."
    [ -n "$JOIN_TOKEN" ] ||
        die "--mode agent --non-interactive also needs --join-token <token>." \
            "Mint one on the Hosts page, or with \`zoomies hosts join-token create --ttl 15m\`."
fi

# Flags that only mean something for an agent, on a host that is not one.
# Silently ignoring them is how an operator ends up believing a host joined a
# controller it has never heard of.
if [ "$MODE" != agent ] && { [ -n "$CONTROLLER_URL" ] || [ -n "$JOIN_TOKEN" ]; }; then
    if [ -z "$MODE" ]; then
        MODE=agent
        note_deferred="--controller/--join-token were given, so this host is being set up as an agent."
    else
        die "--controller and --join-token belong to --mode agent, and this is --mode $MODE." \
            "Drop them, or pass --mode agent to join this host to a controller."
    fi
fi

if [ "$DO_UNINSTALL" -eq 1 ] && [ "$RUN_INIT" -eq 0 ]; then
    warn "--no-init has no meaning alongside --uninstall; ignoring it."
fi

# ---------------------------------------------------------------------------
# Detection -- never ask what we can find out
# ---------------------------------------------------------------------------

have() { command -v "$1" >/dev/null 2>&1; }

# Whether there is a controlling terminal to ask questions on.
#
# `[ -r /dev/tty ]` tests the device node's permission bits, which are readable
# in a container, a CI job and a cloud-init run that have no terminal at all --
# so the old check passed and the open then failed with ENXIO *after* the
# binary was installed, leaving `cannot open /dev/tty` as the last word. Only
# opening it actually answers the question.
have_tty() { (exec 3</dev/tty) 2>/dev/null; }

detect_platform() {
    OS=$(uname -s | tr '[:upper:]' '[:lower:]')
    case "$OS" in
        linux|darwin) ;;
        *) die "$OS is not supported. Zoomies runs on Linux, and on macOS for a development controller." ;;
    esac

    ARCH=$(uname -m)
    case "$ARCH" in
        x86_64|amd64)  ARCH=amd64 ;;
        aarch64|arm64) ARCH=arm64 ;;
        *) die "$ARCH is not supported. Zoomies ships amd64 and arm64 builds." ;;
    esac

    DISTRO="unknown"
    if [ -r /etc/os-release ]; then
        # shellcheck disable=SC1091
        DISTRO=$(. /etc/os-release && printf '%s' "${ID:-unknown}")
    elif [ "$OS" = darwin ]; then
        DISTRO="macos"
    fi
}

detect_init() {
    INIT_SYSTEM="none"
    if [ "$OS" = darwin ]; then
        have launchctl && INIT_SYSTEM="launchd"
    elif [ -d /run/systemd/system ]; then
        INIT_SYSTEM="systemd"
    elif [ -f /sbin/openrc-run ]; then
        INIT_SYSTEM="openrc"
    fi
}

# Sets RUNTIME, RUNTIME_SOCKET and RUNTIME_ROOTLESS. Rootless sockets are
# preferred: a container escape from a runner then lands on an unprivileged
# process rather than on root.
detect_runtime() {
    RUNTIME="none"
    RUNTIME_SOCKET=""
    RUNTIME_ROOTLESS=0
    # A socket that is there and refuses this user is a different fault from a
    # daemon that is not running, and has a different fix. Keeping the first one
    # found means the summary can say which.
    RUNTIME_DENIED=""

    uid=$(id -u)
    xdg="${XDG_RUNTIME_DIR:-/run/user/$uid}"

    for candidate in \
        "${DOCKER_HOST:-}" \
        "$xdg/docker.sock" \
        "${HOME:-}/.docker/run/docker.sock" \
        /var/run/docker.sock
    do
        candidate="${candidate#unix://}"
        [ -n "$candidate" ] || continue
        [ -S "$candidate" ] || continue
        if docker_ok "$candidate"; then
            RUNTIME="docker"
            RUNTIME_SOCKET="$candidate"
            case "$candidate" in
                "$xdg"/*|"${HOME:-/nonexistent}"/*) RUNTIME_ROOTLESS=1 ;;
                *) RUNTIME_ROOTLESS=0 ;;
            esac
            return
        fi
        [ -n "$RUNTIME_DENIED" ] || RUNTIME_DENIED="$candidate"
    done

    for candidate in \
        "${CONTAINER_HOST:-}" \
        "$xdg/podman/podman.sock" \
        /run/podman/podman.sock
    do
        candidate="${candidate#unix://}"
        [ -n "$candidate" ] || continue
        [ -S "$candidate" ] || continue
        RUNTIME="podman"
        RUNTIME_SOCKET="$candidate"
        case "$candidate" in
            "$xdg"/*) RUNTIME_ROOTLESS=1 ;;
            *) RUNTIME_ROOTLESS=0 ;;
        esac
        return
    done

    # A binary with no socket usually means the service or the user's rootless
    # instance is not running, which is a fixable thing worth naming.
    if have docker; then
        RUNTIME="docker-unavailable"
    elif have podman; then
        RUNTIME="podman-unavailable"
    fi
}

docker_ok() {
    [ -S "$1" ] || return 1
    if have docker; then
        DOCKER_HOST="unix://$1" docker version >/dev/null 2>&1 && return 0
        return 1
    fi
    # No CLI: a readable and writable socket is the best signal available.
    [ -r "$1" ] && [ -w "$1" ]
}

# port_free succeeds when nothing is listening on the given port. Written as
# explicit ifs rather than a negated pipeline: `! cmd | grep -q` reads the same
# but skips errexit, which is exactly the kind of thing that makes an installer
# carry on after a check it thinks it made.
# Sets COMPOSE_CMD to whichever compose is usable, or leaves it empty.
# Compose v2 is a docker subcommand; v1 was a separate binary. Both are still
# in the field, so both are accepted and the one found is passed through rather
# than re-derived later.
# Compose is worth reporting whenever it is installed, not only when the
# socket happens to be reachable: a stopped daemon is a fixable thing, and
# `zoomies init` re-detects compose regardless -- so restricting this to a live
# docker socket only made the two halves of one install disagree on screen.
detect_compose() {
    COMPOSE_CMD=""
    case "$RUNTIME" in
        docker|docker-unavailable|podman|podman-unavailable) ;;
        *) return 0 ;;
    esac
    if have docker && docker compose version >/dev/null 2>&1; then
        COMPOSE_CMD="docker compose"
    elif have docker-compose && docker-compose --version >/dev/null 2>&1; then
        COMPOSE_CMD="docker-compose"
    fi
}

# Sets PORT_CHECKED to 0 when neither tool is installed, so the report can say
# that the check did not happen rather than implying the port is free.
PORT_CHECKED=1
port_free() {
    p="$1"
    if have ss; then
        if ss -ltn 2>/dev/null | grep -qE "[:.]${p}[[:space:]]"; then
            return 1
        fi
    elif have netstat; then
        if netstat -ltn 2>/dev/null | grep -qE "[:.]${p}[[:space:]]"; then
            return 1
        fi
    else
        PORT_CHECKED=0
    fi
    # Neither tool is present, so we cannot tell. Say the port is free rather
    # than blocking an install on a check we were unable to make -- but
    # PORT_CHECKED records that nobody looked, because silence that reads as
    # "all clear" is worse than admitting the gap.
    return 0
}

# Run a command as root, by whichever route this host has. One helper, because
# the install path used to check for sudo and doas carefully while the
# uninstall path called `sudo` bare -- so `--uninstall` on a doas host ended in
# `sudo: not found` rather than in this script's own message.
run_privileged() {
    if [ "$(id -u)" = 0 ]; then
        "$@"
    elif have sudo; then
        sudo "$@"
    elif have doas; then
        doas "$@"
    else
        die "this needs root, and neither sudo nor doas is installed." \
            "Re-run it as root."
    fi
}

# Work out now whether the binary can be written at all, and with what.
#
# This used to be discovered after the download, which meant an operator with
# no sudo on a locked-down host watched 25 MB arrive before being told the
# install was never going to happen. Sets ELEVATE to the command that will do
# the writing, or empty for a plain move.
preflight_prefix() {
    ELEVATE=""
    dir="$PREFIX"
    # A prefix that does not exist yet is the parent's problem to permit.
    while [ ! -d "$dir" ] && [ "$dir" != "/" ] && [ "$dir" != "." ]; do
        dir=$(dirname "$dir")
    done
    [ -d "$dir" ] || die "$PREFIX does not exist and neither does anything above it."
    [ -w "$dir" ] && return 0
    if have sudo; then
        ELEVATE="sudo"
    elif have doas; then
        ELEVATE="doas"
    else
        die "$PREFIX is not writable by $(id -un), and neither sudo nor doas is installed." \
            "Re-run this as root, or install somewhere you own:" \
            "  sh install.sh --prefix \"\$HOME/.local/bin\""
    fi
}

# Sets EXISTING, EXISTING_VERSION and EXISTING_RUNNING.
#
# The last one matters more than it looks: replacing the file under a running
# service leaves the old build running on the old inode, so an install that
# reports success has changed nothing the operator can see until the service is
# restarted. Saying so is the difference between an upgrade and a mystery.
detect_existing() {
    EXISTING=""
    EXISTING_VERSION=""
    EXISTING_RUNNING=0
    # `command -v` is consulted too, because the script's own advice for an
    # unprivileged host is --prefix "$HOME/.local/bin" -- and an operator who
    # took it was then told Zoomies was not installed.
    on_path=$(command -v zoomies 2>/dev/null || printf '')
    for p in "$PREFIX/zoomies" "$on_path" /usr/local/bin/zoomies /usr/bin/zoomies; do
        [ -n "$p" ] || continue
        if [ -x "$p" ]; then
            EXISTING="$p"
            EXISTING_VERSION=$("$p" version --short 2>/dev/null || printf 'unknown')
            break
        fi
    done
    [ -n "$EXISTING" ] || return 0

    # Two copies on one host is a real state, and the one the shell runs is not
    # necessarily the one being replaced.
    if [ -n "$on_path" ] && [ "$on_path" != "$PREFIX/zoomies" ] && [ -x "$PREFIX/zoomies" ]; then
        EXISTING_OTHER="$on_path"
    fi

    case "$INIT_SYSTEM" in
        systemd) systemctl is-active --quiet zoomies 2>/dev/null && EXISTING_RUNNING=1 ;;
        launchd) launchctl print system/sh.zoomies >/dev/null 2>&1 && EXISTING_RUNNING=1 ;;
    esac
}

# Restarting is spelled differently on each init system, and an operator who
# has just been told their upgrade has not taken effect should not have to look
# the command up.
restart_hint() {
    case "$INIT_SYSTEM" in
        systemd) printf 'sudo systemctl restart zoomies' ;;
        launchd) printf 'sudo launchctl kickstart -k system/sh.zoomies' ;;
        openrc)  printf 'sudo rc-service zoomies restart' ;;
        *)       printf 'restart the zoomies process' ;;
    esac
}

# ---------------------------------------------------------------------------
# Download
# ---------------------------------------------------------------------------

# fetch <url> <dest>. Sets FETCH_ERROR to whatever the transfer tool said, so
# the caller can put it under its own message rather than letting curl print
# `curl: (22) The requested URL returned error: 404` above it -- unbranded,
# outside the gutter, and the first thing the operator's eye lands on.
FETCH_ERROR=""
fetch() {
    FETCH_ERROR=""
    if have curl; then
        FETCH_ERROR=$(curl -fsSL --retry 3 --retry-delay 1 -o "$2" "$1" 2>&1) && return 0
    elif have wget; then
        FETCH_ERROR=$(wget -qO "$2" "$1" 2>&1) && return 0
    else
        die "neither curl nor wget is installed." \
            "Install one of them, then run this again."
    fi
    return 1
}

fetch_stdout() {
    if have curl; then
        curl -fsSL --retry 3 --retry-delay 1 "$1"
    else
        wget -qO- "$1"
    fi
}

sha256_of() {
    if have sha256sum; then
        sha256sum "$1" | cut -d' ' -f1
    elif have shasum; then
        shasum -a 256 "$1" | cut -d' ' -f1
    elif have openssl; then
        openssl dgst -sha256 "$1" | awk '{print $NF}'
    else
        printf ''
    fi
}

resolve_version() {
    [ "$VERSION" = latest ] || return 0
    step "Finding the latest release"
    # The redirect target of /releases/latest names the tag without needing the
    # API, which keeps this working for unauthenticated users behind a rate limit.
    if have curl; then
        url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE_URL/latest" 2>/dev/null || printf '')
    else
        url=$(wget -qS --max-redirect=5 -O /dev/null "$BASE_URL/latest" 2>&1 |
              awk '/^  Location: /{print $2}' | tail -1)
    fi
    case "$url" in
        */tag/*) VERSION="${url##*/tag/}" ;;
        *) die "could not work out the latest release. Pass --version v1.2.3, or check that $BASE_URL is reachable." ;;
    esac
    ok "latest is $VERSION"
}

install_binary() {
    tag="$VERSION"
    case "$tag" in v*) ;; *) tag="v$tag" ;; esac
    asset="zoomies_${OS}_${ARCH}"
    url="$BASE_URL/download/$tag/$asset"

    tmp=$(mktemp -d "${TMPDIR:-/tmp}/zoomies-install.XXXXXX")
    # shellcheck disable=SC2064
    trap "rm -rf '$tmp'" EXIT INT TERM

    step "Downloading $asset $tag"
    fetch "$url" "$tmp/zoomies" ||
        die "could not download $asset $tag." \
            "$url" \
            "${FETCH_ERROR:-the transfer failed with no message.}" \
            "Check that $tag exists at $BASE_URL," \
            "and that this host can reach it."

    # A download that cannot be verified is refused, not warned about. All
    # three ways verification can fail to happen used to print one dim line and
    # carry on -- and on the `curl | sh` path that line scrolls away above the
    # sudo password prompt, so nobody ever saw it. --allow-unverified is the
    # deliberate opt-in for a private mirror that publishes no checksums.
    step "Verifying the checksum"
    unverified=""
    if sums=$(fetch_stdout "$BASE_URL/download/$tag/checksums.txt" 2>/dev/null) && [ -n "$sums" ]; then
        want=$(printf '%s\n' "$sums" | awk -v a="$asset" '$2 == a || $2 == "*"a {print $1; exit}')
        got=$(sha256_of "$tmp/zoomies")
        if [ -z "$want" ]; then
            unverified="checksums.txt has no entry for $asset."
        elif [ -z "$got" ]; then
            unverified="no sha256sum, shasum or openssl on this host to hash the download with."
        elif [ "$want" != "$got" ]; then
            die "checksum mismatch for $asset." \
                "expected $want" \
                "got      $got" \
                "Do not run this binary. Try again, and if it happens twice, report it."
        else
            ok "sha256 ${got%"${got#????????}"}... matches"
        fi
    else
        unverified="$BASE_URL/download/$tag/checksums.txt could not be fetched."
    fi

    if [ -n "$unverified" ]; then
        if [ "$ALLOW_UNVERIFIED" -eq 1 ]; then
            warn "installing an unverified binary: $unverified"
        else
            die "this download cannot be verified: $unverified" \
                "Zoomies will not install a binary it has not checked." \
                "If this is a mirror that publishes no checksums, pass --allow-unverified."
        fi
    fi

    chmod +x "$tmp/zoomies"

    # preflight_prefix already settled whether this can be written and with
    # what, before the download, so there is nothing left here to discover.
    step "Installing to $PREFIX/zoomies"
    if [ -n "$ELEVATE" ]; then
        run_privileged mkdir -p "$PREFIX" || die "could not create $PREFIX."
        run_privileged install -m 0755 "$tmp/zoomies" "$PREFIX/zoomies" ||
            die "could not write $PREFIX/zoomies."
    else
        mkdir -p "$PREFIX" || die "could not create $PREFIX."
        install -m 0755 "$tmp/zoomies" "$PREFIX/zoomies" ||
            die "could not write $PREFIX/zoomies."
    fi
    NEW_VERSION=$("$PREFIX/zoomies" version --short 2>/dev/null || printf '%s' "$tag")
    if [ -n "$EXISTING_VERSION" ] && [ "$EXISTING_VERSION" != "$NEW_VERSION" ]; then
        ok "$EXISTING_VERSION replaced by $NEW_VERSION"
    else
        ok "$NEW_VERSION installed"
    fi
    if [ "$EXISTING_RUNNING" -eq 1 ]; then
        # The running process still holds the old inode, so nothing an operator
        # can see has changed yet.
        note "the running service is still on the old build; pick this one up with:"
        note "  $(restart_hint)"
    fi

    # A prefix off the operator's PATH is a working install they cannot run,
    # and the symptom -- "zoomies: command not found" straight after a
    # successful install -- looks like the install failed.
    case ":${PATH}:" in
        *":$PREFIX:"*) ;;
        *) note "$PREFIX is not on your PATH; add it, or run $PREFIX/zoomies by its full path" ;;
    esac

    # The script ends in an exec, which never runs the EXIT trap, so the
    # download directory -- and, on the sudo path, the whole binary -- would be
    # left under /tmp after every install. Clean up here, while there is still
    # a shell to do it.
    rm -rf "$tmp"
    trap - EXIT INT TERM
}

# ---------------------------------------------------------------------------
# Uninstall
# ---------------------------------------------------------------------------

do_uninstall() {
    detect_existing
    [ -n "$EXISTING" ] || die "Zoomies does not appear to be installed." \
        "If it went somewhere else, name it: sh install.sh --uninstall --prefix <dir>."
    step "Removing Zoomies"
    note "this runs \`zoomies uninstall\`, which stops the service, removes the"
    note "unit, the service user and the data directory, and can deregister"
    note "your runners from GitHub first."
    args=""
    [ "$ASSUME_YES" -eq 1 ] && args="--yes"
    if [ -w "$EXISTING" ] || [ "$(id -u)" = 0 ]; then
        # shellcheck disable=SC2086
        "$EXISTING" uninstall $args
    else
        # shellcheck disable=SC2086
        run_privileged "$EXISTING" uninstall $args
    fi
    if [ -w "$(dirname "$EXISTING")" ]; then rm -f "$EXISTING"; else run_privileged rm -f "$EXISTING"; fi
    ok "removed $EXISTING"
    exit 0
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

banner
detect_platform
detect_init
detect_runtime
detect_compose

[ "$DO_UNINSTALL" -eq 1 ] && do_uninstall

# ---------------------------------------------------------------------------
# What this host is, and what that means for the install about to happen
#
# `zoomies init` prints its own, fuller environment report a few seconds later,
# so this one deliberately covers only what bears on the download and the write
# -- and says what each finding costs, rather than reporting a fact and leaving
# the operator to work out whether it matters.
# ---------------------------------------------------------------------------

step "Checking this host"
field os "$OS/$ARCH ($DISTRO)"
case "$INIT_SYSTEM" in
    none) field init "none -- nothing here will restart Zoomies after a reboot" ;;
    *)    field init "$INIT_SYSTEM" ;;
esac
case "$RUNTIME" in
    docker|podman)
        if [ "$RUNTIME_ROOTLESS" -eq 1 ]; then
            field runtime "$RUNTIME, rootless -- $RUNTIME_SOCKET"
        else
            field runtime "$RUNTIME, root -- $RUNTIME_SOCKET"
        fi
        ;;
    docker-unavailable)
        if [ -n "$RUNTIME_DENIED" ]; then
            # The socket is there and this user may not open it. Naming the
            # group that owns it turns the fix into one command, and setup will
            # put the service account in that group for you.
            # GNU stat and BSD stat spell this differently, and the fix has to
            # name a real group on both.
            denied_group=$(stat -c '%G' "$RUNTIME_DENIED" 2>/dev/null \
                || stat -f '%Sg' "$RUNTIME_DENIED" 2>/dev/null \
                || echo docker)
            field runtime "docker is running, but $RUNTIME_DENIED is not yours to open"
            field "" "it belongs to group $denied_group. Setup adds the service account"
            field "" "to it; for your own shell, sudo usermod -aG $denied_group $(id -un),"
            field "" "then log in again."
        else
            field runtime "docker is installed, but its socket is not answering"
            field "" "start it: sudo systemctl start docker"
            field "" "or rootless: systemctl --user start docker"
        fi
        field "" "native works regardless; compose and docker need this fixed."
        ;;
    podman-unavailable)
        field runtime "podman is installed, but its API socket is not running"
        field "" "start it: systemctl --user enable --now podman.socket"
        field "" "native works regardless; compose and docker need this fixed."
        ;;
    *)
        field runtime "none -- jobs would run directly on this host, unisolated"
        ;;
esac
if [ -n "$COMPOSE_CMD" ]; then
    field compose "$COMPOSE_CMD"
fi
for p in 8080 443; do
    port_free "$p" || field ports "$p is already in use; setup will offer another"
done
if [ "$PORT_CHECKED" -eq 0 ]; then
    field ports "not checked -- no ss or netstat here"
fi

preflight_prefix
detect_existing
if [ -n "$EXISTING" ]; then
    if [ "$EXISTING_RUNNING" -eq 1 ]; then
        field installed "$EXISTING_VERSION at $EXISTING, and running"
    else
        field installed "$EXISTING_VERSION at $EXISTING"
    fi
fi
if [ -n "$EXISTING_OTHER" ]; then
    field "" "your shell runs $EXISTING_OTHER, which this install does not replace"
fi
[ -z "$note_deferred" ] || field "" "$note_deferred"
say ""

# ---------------------------------------------------------------------------
# There has to be somebody to ask
#
# `zoomies init` is a conversation, and a host with no controlling terminal
# cannot have one. This used to be found out after the binary was installed, by
# which point the last line on screen was `cannot open /dev/tty` and the host
# was half set up -- exactly the automated contexts where nobody is watching.
# ---------------------------------------------------------------------------
if [ "$RUN_INIT" -eq 1 ] && [ "$NON_INTERACTIVE" -eq 0 ] && ! have_tty; then
    die "there is no terminal here to run the interactive setup on." \
        "Choose one of:" \
        "  --no-init                         install the binary now, and run \`zoomies init\` yourself later" \
        "  --non-interactive --answers FILE  unattended setup" \
        "     \`zoomies init --print-answers\` writes an annotated file to start from."
fi

resolve_version

# ---------------------------------------------------------------------------
# Say what is about to happen, then ask
#
# Between the banner and the first change to this host there used to be nothing
# -- `curl | sh` went straight from detection to writing an executable into
# /usr/local/bin with sudo. One honest checkpoint, skipped by --yes and by
# --non-interactive, is what makes that a decision rather than a surprise.
# ---------------------------------------------------------------------------

# `zoomies version --short` prints "1.4.0 (abc1234)"; the tag is "v1.4.0". Both
# are reduced to bare digits before they are compared, so the two spellings of
# one version are not mistaken for two versions.
INSTALLED_TAG="${EXISTING_VERSION%% *}"
INSTALLED_TAG="${INSTALLED_TAG#v}"
WANTED_TAG="${VERSION#v}"

# Same version, nothing asked for: there is nothing to do, and saying so beats
# re-downloading 27 MB to arrive back where we started.
if [ -n "$INSTALLED_TAG" ] && [ "$INSTALLED_TAG" = "$WANTED_TAG" ] && [ "$ASSUME_YES" -eq 0 ]; then
    ok "$EXISTING_VERSION is already installed at $EXISTING."
    note "to set this host up, run: zoomies init"
    note "to reinstall the same version anyway, add --yes"
    exit 0
fi

step "About to"
if [ -n "$EXISTING" ]; then
    field install "$VERSION over $EXISTING_VERSION, at $PREFIX/zoomies"
else
    field install "zoomies $VERSION ($OS/$ARCH) to $PREFIX/zoomies"
fi
[ -z "$ELEVATE" ] || field privilege "$ELEVATE, to write to $PREFIX -- it may ask for your password"
if [ "$RUN_INIT" -eq 0 ]; then
    field then "nothing -- --no-init was given, so setup is yours to run"
elif [ -n "$MODE" ]; then
    field then "run \`zoomies init\` to set this host up as $MODE"
else
    field then "run \`zoomies init\`, which asks what this host should be"
fi
if [ -n "$EXISTING" ]; then
    field keeps "your configuration, encryption key, database and runners"
else
    field keeps "nothing under /etc/zoomies or /var/lib/zoomies is touched by this script"
fi
say ""

# A downgrade is a decision, not a step, because a database written by a newer
# build may not be readable by an older one. `sort -V` is GNU and BusyBox only
# -- BSD sort, which is what macOS ships, does not have it -- so the check is
# skipped rather than wrong where it cannot be made.
if [ -n "$INSTALLED_TAG" ] && [ "$INSTALLED_TAG" != "$WANTED_TAG" ] &&
   printf '1\n' | sort -V >/dev/null 2>&1 &&
   [ "$(printf '%s\n%s\n' "$INSTALLED_TAG" "$WANTED_TAG" | sort -V | tail -1)" = "$INSTALLED_TAG" ]; then
    warn "$VERSION is older than the installed $EXISTING_VERSION, so this is a downgrade."
    hint "Back up $PREFIX/../zoomies.db first: an older build may not read a schema a newer one wrote."
fi

if [ "$NON_INTERACTIVE" -eq 0 ] && [ "$ASSUME_YES" -eq 0 ] && have_tty; then
    printf '%s   ?? %sContinue? [Y/n] ' "$C_ACCENT" "$C_RESET"
    # Piping this script into sh consumes stdin, so the answer is read from the
    # terminal directly -- the same one the handoff at the bottom reconnects.
    read -r reply < /dev/tty || reply=""
    case "$reply" in
        ""|y|Y|yes|Yes) ;;
        *) say ""; ok "Nothing was installed."; exit 0 ;;
    esac
    say ""
fi

install_binary

if [ "$RUN_INIT" -eq 0 ]; then
    say ""
    if [ -n "$EXISTING" ]; then
        ok "Binary upgraded. Nothing else on this host was touched."
    else
        ok "Binary installed. Run \`zoomies init\` when you are ready to set it up."
    fi
    exit 0
fi

# Hand off. `zoomies init` owns the interactive setup; everything detected above
# is passed through so it never asks a question this script already answered.
set -- init \
    --detected-os "$OS" \
    --detected-arch "$ARCH" \
    --detected-distro "$DISTRO" \
    --detected-init "$INIT_SYSTEM" \
    --detected-runtime "$RUNTIME" \
    --installed-binary "$PREFIX/zoomies"
[ -n "$RUNTIME_SOCKET" ] && set -- "$@" --detected-socket "$RUNTIME_SOCKET"
[ "$RUNTIME_ROOTLESS" -eq 1 ] && set -- "$@" --detected-rootless
[ -n "$COMPOSE_CMD" ] && set -- "$@" --detected-compose "$COMPOSE_CMD"
[ -n "$MODE" ] && set -- "$@" --mode "$MODE"
[ -n "$DEPLOYMENT" ] && set -- "$@" --deployment "$DEPLOYMENT"
[ -n "$CONTROLLER_URL" ] && set -- "$@" --controller "$CONTROLLER_URL"
[ -n "$JOIN_TOKEN" ] && set -- "$@" --join-token "$JOIN_TOKEN"
[ -n "$ANSWERS" ] && set -- "$@" --answers "$ANSWERS"
[ "$NON_INTERACTIVE" -eq 1 ] && set -- "$@" --non-interactive
[ "$ASSUME_YES" -eq 1 ] && set -- "$@" --yes

say ""
step "Handing over to \`zoomies init\`"
say ""

# Piping this script into sh leaves stdin consumed, so an interactive setup has
# no terminal to read from. Reconnect one when there is a terminal to reconnect;
# have_tty opens /dev/tty rather than testing its permission bits, because the
# bits are readable in plenty of places the open is not.
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ] && have_tty; then
    exec "$PREFIX/zoomies" "$@" < /dev/tty
fi
exec "$PREFIX/zoomies" "$@"
