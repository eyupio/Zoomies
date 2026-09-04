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
#   1. works out your OS, architecture, container runtime and init system
#   2. downloads the right binary and verifies its SHA-256 against the
#      published checksums file
#   3. installs it to /usr/local/bin
#   4. hands off to `zoomies init`, which does the interactive setup: service
#      user, directories, encryption key, backend, TLS, the GitHub App, your
#      admin account, and the system service
#
# POSIX sh. No bashisms -- it is checked with dash and shellcheck in CI.

set -eu

VERSION="${ZOOMIES_VERSION:-latest}"
REPO="${ZOOMIES_REPO:-eyupio/zoomies}"
PREFIX="${ZOOMIES_PREFIX:-/usr/local/bin}"
BASE_URL="${ZOOMIES_BASE_URL:-https://github.com/${REPO}/releases}"

MODE=""
CONTROLLER_URL=""
JOIN_TOKEN=""
NON_INTERACTIVE=0
ANSWERS=""
RUN_INIT=1
DO_UNINSTALL=0
ASSUME_YES=0

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

say()  { printf '%s\n' "$*"; }
step() { printf '%s->%s %s\n' "$C_ACCENT" "$C_RESET" "$*"; }
ok()   { printf '%s   ok%s %s\n' "$C_OK" "$C_RESET" "$*"; }
note() { printf '%s      %s%s\n' "$C_DIM" "$*" "$C_RESET"; }
warn() { printf '%s   !!%s %s\n' "$C_WARN" "$C_RESET" "$*" >&2; }
die()  { printf '%s error:%s %s\n' "$C_ERR" "$C_RESET" "$*" >&2; exit 1; }

banner() {
    printf '%s%sZoomies%s -- a GitHub Actions runner fleet that cleans up after itself.\n' \
        "$C_BOLD" "$C_ACCENT" "$C_RESET"
    printf '%shttps://github.com/%s%s\n\n' "$C_DIM" "$REPO" "$C_RESET"
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
  --controller <url>    For --mode agent: the controller to join.
  --join-token <token>  For --mode agent: a join token from the UI or
                        `zoomies hosts join-token create`.
  --version <v>         Release to install (default: latest).
  --prefix <dir>        Where to put the binary (default: /usr/local/bin).
  --non-interactive     Never prompt. Requires --answers, or enough flags.
  --answers <file>      YAML answer file for unattended setup.
  --no-init             Install the binary only; do not run `zoomies init`.
  --yes, -y             Assume yes for install-time confirmations.
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
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --mode)        MODE="${2:?--mode needs a value}"; shift 2 ;;
        --mode=*)      MODE="${1#*=}"; shift ;;
        --controller)  CONTROLLER_URL="${2:?--controller needs a URL}"; shift 2 ;;
        --controller=*) CONTROLLER_URL="${1#*=}"; shift ;;
        --join-token)  JOIN_TOKEN="${2:?--join-token needs a value}"; shift 2 ;;
        --join-token=*) JOIN_TOKEN="${1#*=}"; shift ;;
        --version)     VERSION="${2:?--version needs a value}"; shift 2 ;;
        --version=*)   VERSION="${1#*=}"; shift ;;
        --prefix)      PREFIX="${2:?--prefix needs a value}"; shift 2 ;;
        --prefix=*)    PREFIX="${1#*=}"; shift ;;
        --answers)     ANSWERS="${2:?--answers needs a path}"; NON_INTERACTIVE=1; shift 2 ;;
        --answers=*)   ANSWERS="${1#*=}"; NON_INTERACTIVE=1; shift ;;
        --non-interactive) NON_INTERACTIVE=1; shift ;;
        --no-init)     RUN_INIT=0; shift ;;
        --uninstall)   DO_UNINSTALL=1; shift ;;
        -y|--yes)      ASSUME_YES=1; shift ;;
        -h|--help)     usage; exit 0 ;;
        *)             die "unknown option: $1 (try --help)" ;;
    esac
done

# ---------------------------------------------------------------------------
# Detection -- never ask what we can find out
# ---------------------------------------------------------------------------

have() { command -v "$1" >/dev/null 2>&1; }

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

port_free() {
    p="$1"
    if have ss; then
        ! ss -ltn 2>/dev/null | grep -qE "[:.]${p}[[:space:]]"
    elif have netstat; then
        ! netstat -ltn 2>/dev/null | grep -qE "[:.]${p}[[:space:]]"
    else
        return 0
    fi
}

detect_existing() {
    EXISTING=""
    EXISTING_VERSION=""
    for p in "$PREFIX/zoomies" /usr/local/bin/zoomies /usr/bin/zoomies; do
        if [ -x "$p" ]; then
            EXISTING="$p"
            EXISTING_VERSION=$("$p" version --short 2>/dev/null || printf 'unknown')
            break
        fi
    done
}

# ---------------------------------------------------------------------------
# Download
# ---------------------------------------------------------------------------

fetch() {
    # fetch <url> <dest>
    if have curl; then
        curl -fsSL --retry 3 --retry-delay 1 -o "$2" "$1"
    elif have wget; then
        wget -qO "$2" "$1"
    else
        die "neither curl nor wget is installed; one of them is needed to download Zoomies."
    fi
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
        die "could not download $url
      Check that $tag exists at $BASE_URL and that this host can reach it."

    step "Verifying the checksum"
    if sums=$(fetch_stdout "$BASE_URL/download/$tag/checksums.txt" 2>/dev/null) && [ -n "$sums" ]; then
        want=$(printf '%s\n' "$sums" | awk -v a="$asset" '$2 == a || $2 == "*"a {print $1; exit}')
        got=$(sha256_of "$tmp/zoomies")
        if [ -z "$want" ]; then
            warn "checksums.txt has no entry for $asset; cannot verify this download."
        elif [ -z "$got" ]; then
            warn "no sha256sum, shasum or openssl on this host; cannot verify this download."
        elif [ "$want" != "$got" ]; then
            die "checksum mismatch for $asset.
      expected $want
      got      $got
      Do not run this binary. Try again, and if it happens twice, report it."
        else
            ok "sha256 ${got%"${got#????????}"}... matches"
        fi
    else
        warn "could not fetch checksums.txt; skipping verification."
    fi

    chmod +x "$tmp/zoomies"

    step "Installing to $PREFIX/zoomies"
    if [ -w "$PREFIX" ]; then
        mv "$tmp/zoomies" "$PREFIX/zoomies"
    elif have sudo; then
        note "$PREFIX needs root; using sudo"
        sudo install -m 0755 "$tmp/zoomies" "$PREFIX/zoomies"
    elif have doas; then
        doas install -m 0755 "$tmp/zoomies" "$PREFIX/zoomies"
    else
        die "$PREFIX is not writable and neither sudo nor doas is available.
      Re-run as root, or use --prefix \"\$HOME/.local/bin\"."
    fi
    ok "$("$PREFIX/zoomies" version --short 2>/dev/null || printf '%s' "$tag") installed"
}

# ---------------------------------------------------------------------------
# Uninstall
# ---------------------------------------------------------------------------

do_uninstall() {
    detect_existing
    [ -n "$EXISTING" ] || die "Zoomies does not appear to be installed."
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
        sudo "$EXISTING" uninstall $args
    fi
    if [ -w "$(dirname "$EXISTING")" ]; then rm -f "$EXISTING"; else sudo rm -f "$EXISTING"; fi
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

[ "$DO_UNINSTALL" -eq 1 ] && do_uninstall

step "Looking around"
note "os          $OS/$ARCH ($DISTRO)"
note "init        $INIT_SYSTEM"
case "$RUNTIME" in
    docker|podman)
        if [ "$RUNTIME_ROOTLESS" -eq 1 ]; then
            note "runtime     $RUNTIME, rootless -- $RUNTIME_SOCKET"
        else
            note "runtime     $RUNTIME, root -- $RUNTIME_SOCKET"
        fi
        ;;
    docker-unavailable)
        note "runtime     docker is installed but its socket is not reachable"
        note "            try: sudo systemctl start docker  (or start rootless: systemctl --user start docker)"
        ;;
    podman-unavailable)
        note "runtime     podman is installed but its API socket is not running"
        note "            try: systemctl --user enable --now podman.socket"
        ;;
    *)
        note "runtime     none found -- the process backend will run jobs directly on this host"
        ;;
esac
for p in 8080 443; do
    port_free "$p" || note "port $p     already in use; setup will offer another"
done

detect_existing
if [ -n "$EXISTING" ]; then
    note "installed   $EXISTING_VERSION at $EXISTING"
    say ""
    step "Zoomies is already installed -- this will upgrade it in place."
    note "your configuration, database and runners are left alone."
fi
say ""

resolve_version
install_binary

if [ "$RUN_INIT" -eq 0 ]; then
    say ""
    ok "Binary installed. Run \`zoomies init\` when you are ready to set it up."
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
[ -n "$MODE" ] && set -- "$@" --mode "$MODE"
[ -n "$CONTROLLER_URL" ] && set -- "$@" --controller "$CONTROLLER_URL"
[ -n "$JOIN_TOKEN" ] && set -- "$@" --join-token "$JOIN_TOKEN"
[ -n "$ANSWERS" ] && set -- "$@" --answers "$ANSWERS"
[ "$NON_INTERACTIVE" -eq 1 ] && set -- "$@" --non-interactive
[ "$ASSUME_YES" -eq 1 ] && set -- "$@" --yes

say ""
step "Handing over to \`zoomies init\`"
say ""

# Piping this script into sh leaves stdin consumed, so an interactive setup has
# no terminal to read from. Reconnect one when there is a terminal to reconnect.
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ] && [ -r /dev/tty ]; then
    exec "$PREFIX/zoomies" "$@" < /dev/tty
fi
exec "$PREFIX/zoomies" "$@"
