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
#   --demo looks before it installs: the same download and checksum, but the
#   binary runs from a temporary directory as `zoomies demo` -- a controller on
#   this machine only, with a fleet already in it -- and is deleted with the demo.
#   No sudo, no service, no questions.
#
# POSIX sh. No bashisms -- it is checked with dash and shellcheck in CI.

set -eu

VERSION="${ZOOMIES_VERSION:-latest}"
VERSION_GIVEN=0
[ "${ZOOMIES_VERSION+x}" = x ] && VERSION_GIVEN=1
REPO="${ZOOMIES_REPO:-eyupio/zoomies}"
PREFIX="${ZOOMIES_PREFIX:-/usr/local/bin}"
BASE_URL="${ZOOMIES_BASE_URL:-https://github.com/${REPO}/releases}"
# A tag is visible at /releases/latest the moment it is published, but the
# release workflow attaches the binaries a few minutes later. Installing inside
# that window is the one download failure worth waiting out rather than failing.
ASSET_WAIT="${ZOOMIES_ASSET_WAIT:-300}"
RESOLVED_LATEST=0
# A redirect is somebody else's choice of protocol, and what this script
# downloads is executed on the host. Redirects are pinned to https so a 302
# can never turn the download into a plaintext one; the first request is
# pinned too unless an operator pointed ZOOMIES_BASE_URL at a plain-http
# mirror of their own, in which case pinning it would break their mirror
# rather than protect it.
CURL_PROTO="--proto-redir =https"
case "$BASE_URL" in
    https://*) CURL_PROTO="--proto =https --proto-redir =https" ;;
esac

MODE=""
DEPLOYMENT=""
CONTROLLER_URL=""
JOIN_TOKEN=""
EXTERNAL_URL=""
PORT=""
NON_INTERACTIVE=0
ANSWERS=""
RUN_INIT=1
DO_UNINSTALL=0
DO_UPGRADE=0
BINARY_ONLY=0
PREFIX_GIVEN=0
CONFIG_DIR=""
UPGRADE_IMAGE=""
ASSUME_YES=0
# YES_GIVEN is --yes typed, as opposed to implied: an unattended upgrade adds
# nothing new to the host -- a folder, a mount -- unless somebody said so.
YES_GIVEN=0
ALLOW_UNVERIFIED=0
DO_DEMO=0
# Motion -- off until init_motion says otherwise. It is a courtesy, never the
# interface: every value that could make an animated line the wrong thing to
# draw (a captured stdout, NO_COLOR, a dumb terminal, CI, --no-animation) turns
# it off, and when it is off not one byte more reaches the terminal than before.
NO_ANIM=0
PREVIEW=0
Z_ANIM=0
Z_LOAD_PID=""
[ -n "${ZOOMIES_NO_ANIMATION:-}" ] && NO_ANIM=1
[ -n "${ZOOMIES_PREVIEW:-}" ] && PREVIEW=1
EXISTING_OTHER=""
EXISTING_UNIT=""
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
    # Runner Blue. 33 is the nearest entry in the 256-colour cube to #2F80ED;
    # 99 was a violet that is in neither the brand palette nor the UI tokens,
    # so the first fifteen minutes changed accent colour twice.
    C_ACCENT=$(printf '\033[38;5;33m')
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
die()  { anim_teardown; printf '%s   xx%s %s\n' "$C_ERR" "$C_RESET" "$1" >&2; shift; hint "$@"; exit 1; }
# hint prints the continuation lines of a warning or an error at the gutter's
# own indent. The alternative -- six literal spaces inside the message string --
# drifts silently the moment the gutter changes, and it did.
hint() { for h in "$@"; do printf '%s      %s%s\n' "$C_DIM" "$h" "$C_RESET" >&2; done; }

# A key/value line in the "Checking this host" table. One helper so no call
# site hand-counts spaces, which is what let the port lines drift out of the
# column everything else lines up in.
field() { k="$1"; shift; printf '%s      %-12s%s%s\n' "$C_DIM" "$k" "$*" "$C_RESET"; }

# ---------------------------------------------------------------------------
# Motion
#
# The install is mostly waiting: a release lookup, a 40 MB download, a checksum
# fetch, and sometimes the minutes a just-published release takes to finish
# uploading. Waiting with nothing on screen reads as a hang, and the one thing
# named after a dog sprinting round the living room should not look stalled.
#
# The rule that keeps this safe is that the animation draws *around* the gutter,
# never inside it. `step`/`ok`/`warn`/`die` are untouched; the loader is one
# ephemeral line it redraws with a carriage return and clears before the real
# result is printed. So a terminal that is not a terminal -- the CI checks,
# `curl | sh | tee`, anything that captures stdout -- gets exactly the text it
# always got, and every animated byte is spent only where a person is watching.
# ---------------------------------------------------------------------------

# Whether the locale can render the multibyte paw. The banner makes the same
# downgrade for the same reason: mojibake where the mark should be.
anim_ready() {
    case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in
        *UTF-8*|*utf8*|*UTF8*|*utf-8*) return 0 ;;
    esac
    return 1
}

_cur_off() { [ "$Z_ANIM" -eq 1 ] && printf '\033[?25l'; return 0; }
_cur_on()  { [ "$Z_ANIM" -eq 1 ] && printf '\033[?25h'; return 0; }

# Decide once whether anything may be drawn, and pick the runner. Called from
# main after the arguments are known, because --preview and --no-animation are
# decided there.
init_motion() {
    Z_ANIM=0
    if anim_ready; then
        _RUNNER='🐾'; _RD=2; _DASH_MARK='🐾'
    else
        _RUNNER='[paw]'; _RD=5; _DASH_MARK='[paw]'
    fi
    _W=32
    if [ "$NO_ANIM" -eq 1 ] || [ -n "${ZOOMIES_NO_ANIMATION:-}" ]; then
        :
    elif [ "$PREVIEW" -eq 1 ]; then
        Z_ANIM=1
    elif [ -t 1 ] && [ -z "${NO_COLOR:-}" ] && [ "${TERM:-dumb}" != "dumb" ] &&
         [ -z "${CI:-}" ]; then
        Z_ANIM=1
    fi
    # A hidden cursor that outlives the script is the one animation failure an
    # operator has to fix by hand, so the trap that shows it again is armed the
    # moment motion is on and every later trap chains through it.
    [ "$Z_ANIM" -eq 1 ] || return 0
    trap 'anim_teardown' EXIT INT TERM
}

# anim_teardown stops a loader that is still running and shows the cursor, so
# an error, a signal or an early exit never leaves the terminal mid-gallop.
anim_teardown() {
    [ -n "$Z_LOAD_PID" ] && load_stop
    _cur_on
    return 0
}

# _load_line redraws the one loader line. The runner bounces down a belt because
# none of these waits has a trustworthy total -- the release lookup has no size
# at all, and the download's length arrives one redirect away from the truth --
# so a bouncing dog on a treadmill is the honest picture of "working", and the
# live byte count and elapsed timer carry the progress that is real.
_load_line() {
    _now=$(date +%s 2>/dev/null) || _now=0
    _el=$((_now - _t0)); [ "$_el" -lt 0 ] && _el=0
    _sz=0
    if [ -n "$_load_file" ] && [ -f "$_load_file" ]; then
        _sz=$(wc -c < "$_load_file" 2>/dev/null) || _sz=0
        _sz=$(printf '%s' "$_sz" | tr -dc 0-9)
        case "$_sz" in ''|*[!0-9]*) _sz=0 ;; esac
    fi
    _size=''
    if [ -n "$_load_file" ]; then
        _mb=$((_sz / 1048576)); _md=$(((_sz % 1048576) / 104858))
        _size=$(printf '%d.%dMB' "$_mb" "$_md")
    fi
    _max=$((_W - _RD)); [ "$_max" -lt 1 ] && _max=1
    _cyc=$((_max * 2)); [ "$_cyc" -lt 1 ] && _cyc=1
    _p=$((_fr % _cyc)); [ "$_p" -gt "$_max" ] && _p=$((_cyc - _p))
    _r=$_RUNNER
    _i=0; _left=''
    while [ "$_i" -lt "$_p" ]; do _left="$_left "; _i=$((_i + 1)); done
    _pad=''
    while [ "$_i" -lt "$_max" ]; do _pad="$_pad "; _i=$((_i + 1)); done
    _belt="$C_DIM|$C_RESET$C_ACCENT$_left$_r$_pad$C_RESET$C_DIM|$C_RESET"
    printf '\r\033[K  %s  %.22s  %6s  %2ds' "$_belt" "$_load_phase" "$_size" "$_el"
}

# load_start runs the loader in the background while the caller gets on with the
# real command -- the caller must not print to the terminal between load_start
# and load_stop, which is exactly true of the fetches it wraps (their output is
# captured, not shown). load_stop is always safe to call; a no-op loader leaves
# no trace.
load_start() {
    [ "$Z_ANIM" -eq 1 ] || return 0
    _load_phase=$1
    _load_file=${2:-}
    _t0=$(date +%s 2>/dev/null) || _t0=0
    _cur_off
    ( _fr=0; while :; do _load_line; _fr=$((_fr + 1)); sleep 0.1 || return 0; done ) &
    Z_LOAD_PID=$!
}

load_stop() {
    [ -n "$Z_LOAD_PID" ] || return 0
    kill "$Z_LOAD_PID" 2>/dev/null || true
    wait "$Z_LOAD_PID" 2>/dev/null || true
    Z_LOAD_PID=""
    printf '\r\033[K'
    _cur_on
    return 0
}

# pulse animates a fixed wait in the foreground. It exists for the one loop that
# has nothing to overlap -- waiting for a release's binaries to finish
# uploading, which is a plain sleep between two checks -- and so must not fight
# a background line with it.
pulse() {
    [ "$Z_ANIM" -eq 1 ] || return 0
    pulse_secs=$1
    pulse_phase=$2
    _load_phase=$pulse_phase
    _load_file=""
    _t0=$(date +%s 2>/dev/null) || _t0=0
    _end=$((_t0 + pulse_secs))
    _fr=0
    _cur_off
    while :; do
        _load_line
        _fr=$((_fr + 1))
        _n=$(date +%s 2>/dev/null) || _n=0
        [ "$_n" -lt "$_end" ] || break
        sleep 0.1 || break
    done
    printf '\r\033[K'
    _cur_on
    return 0
}

# A tiny arrival before the permanent banner. UTF-8 terminals get the paw mark;
# the portable fallback spells it out instead of attempting ambiguous face art.
# Six frames take less than a quarter of a second: enough to feel alive, never
# enough to make a quick local install feel slower.
brand_dash() {
    [ "$Z_ANIM" -eq 1 ] || return 0
    _cur_off
    for _bp in 0 4 8 12 16 20; do
        _i=0; _pad=''
        while [ "$_i" -lt "$_bp" ]; do _pad="$_pad "; _i=$((_i + 1)); done
        printf '\r\033[K  %s~~~~%s%s%s%s%s' \
            "$C_DIM" "$C_RESET" "$_pad" "$C_ACCENT" "$_DASH_MARK" "$C_RESET"
        sleep 0.035
    done
    printf '\r\033[K'
    _cur_on
    return 0
}

# Pace the one-shot banner reveal without making the static path sleep.
brand_beat() { [ "$Z_ANIM" -eq 1 ] && sleep 0.045; return 0; }

# The dog skids to a stop and sits as the install hands off. A moving dog is a
# courtesy while work happens; a dog that has arrived is the payoff.
finale() {
    [ "$Z_ANIM" -eq 1 ] || return 0
    fin_ver=$1
    _cur_off
    _fr=0
    while [ "$_fr" -lt 11 ]; do
        _sp=$((_fr * 3))
        _i=0; _pad=""
        while [ "$_i" -lt "$_sp" ]; do _pad="$_pad "; _i=$((_i + 1)); done
        _wl=$((24 - _fr * 3)); [ "$_wl" -lt 0 ] && _wl=0
        _k=0; _w=""
        while [ "$_k" -lt "$_wl" ]; do _w="$_w~"; _k=$((_k + 1)); done
        printf '\r\033[K  %s%s%s%s%s%s%s' \
            "$C_DIM" "$_w" "$C_RESET" "$C_ACCENT" "$_pad" "$_RUNNER" "$C_RESET"
        _fr=$((_fr + 1))
        sleep 0.04
    done
    printf '\r\033[K'
    _cur_on
    printf '%s        ▄██▄     ▄██▄%s\n' "$C_ACCENT" "$C_RESET"
    printf '%s       ██████   ██████%s  %s%s%s is installed.\n' \
        "$C_ACCENT" "$C_RESET" "$C_BOLD" "$fin_ver" "$C_RESET"
    printf '%s  ▄██▄ ██████   ██████ ▄██▄%s  %snext: zoomies init%s\n' \
        "$C_ACCENT" "$C_RESET" "$C_DIM" "$C_RESET"
    printf '%s █████  ▀██▀     ▀██▀  █████%s\n' "$C_ACCENT" "$C_RESET"
    printf '%s  ▀██▀      ▄██▄       ▀██▀%s\n' "$C_ACCENT" "$C_RESET"
    printf '%s         ▄████████▄%s\n' "$C_ACCENT" "$C_RESET"
    printf '%s       ▄████████████▄%s\n' "$C_ACCENT" "$C_RESET"
    printf '%s      ████████████████%s\n' "$C_ACCENT" "$C_RESET"
    printf '%s       ▀████████████▀%s\n' "$C_ACCENT" "$C_RESET"
    printf '%s          ▀▀██▀▀%s\n' "$C_ACCENT" "$C_RESET"
    return 0
}

# preview_all plays the whole set without touching the host, so the art can be
# judged against a real terminal, font and tmux before it is trusted in an
# install. It is the reason --preview exists.
preview_all() {
    say ""
    banner
    step "Doing a thing that takes a moment"
    load_start "resolving latest"
    sleep 2
    load_stop
    ok "latest is v1.4.2"
    say ""
    step "Downloading zoomies_linux_amd64 v1.4.2"
    load_start "downloading"
    sleep 3
    load_stop
    ok "downloaded"
    say ""
    step "Verifying the checksum"
    pulse 2 "sniffing the goods"
    ok "sha256 1f4c9a72... matches"
    say ""
    step "Waiting on a release that was published seconds ago"
    pulse 3 "asset still building"
    ok "available now"
    say ""
    finale "zoomies 1.4.2 (abc1234)"
    say ""
    ok "preview done -- nothing was installed."
}

# A block-built interpretation of the Zoomies paw: four toe pads and one broad,
# tapered central pad. The logo's fine speed ring is deliberately omitted at
# terminal resolution so the paw stays crisp instead of becoming a flat blob.
banner() {
    printf '\n'
    brand_dash
    if anim_ready && [ "${TERM:-dumb}" != "dumb" ]; then
        printf '%s        ▄██▄     ▄██▄%s\n' "$C_ACCENT" "$C_RESET"; brand_beat
        printf '%s       ██████   ██████%s          %sZOOMIES%s\n' \
            "$C_ACCENT" "$C_RESET" "$C_BOLD$C_ACCENT" "$C_RESET"; brand_beat
        printf '%s  ▄██▄ ██████   ██████ ▄██▄%s   %sSELF-HOSTED GITHUB ACTIONS RUNNERS%s\n' \
            "$C_ACCENT" "$C_RESET" "$C_BOLD" "$C_RESET"; brand_beat
        printf '%s █████  ▀██▀     ▀██▀  █████%s   Fast when needed. Gone when done.\n' \
            "$C_ACCENT" "$C_RESET"; brand_beat
        printf '%s  ▀██▀      ▄██▄       ▀██▀%s   %soff the lead, on the job.%s\n' \
            "$C_ACCENT" "$C_RESET" "$C_DIM" "$C_RESET"; brand_beat
        printf '%s         ▄████████▄%s\n' "$C_ACCENT" "$C_RESET"; brand_beat
        printf '%s       ▄████████████▄%s\n' "$C_ACCENT" "$C_RESET"; brand_beat
        printf '%s      ████████████████%s\n' "$C_ACCENT" "$C_RESET"; brand_beat
        printf '%s       ▀████████████▀%s\n' "$C_ACCENT" "$C_RESET"; brand_beat
        printf '%s          ▀▀██▀▀%s\n' "$C_ACCENT" "$C_RESET"; brand_beat
    else
        printf "%s          .--.    .--.%s\n" "$C_ACCENT" "$C_RESET"
        printf "%s         (    )  (    )%s        %sZOOMIES%s\n" "$C_ACCENT" "$C_RESET" "$C_BOLD$C_ACCENT" "$C_RESET"
        printf "%s    .-.  (    )  (    )  .-.%s   %sSELF-HOSTED GITHUB ACTIONS RUNNERS%s\n" "$C_ACCENT" "$C_RESET" "$C_BOLD" "$C_RESET"
        printf "%s   (   )  '--'    '--'  (   )%s  Fast when needed. Gone when done.\n" "$C_ACCENT" "$C_RESET"
        printf "%s    '-'      .--.        '-'%s   %soff the lead, on the job.%s\n" "$C_ACCENT" "$C_RESET" "$C_DIM" "$C_RESET"
        printf "%s           .'    '.%s\n" "$C_ACCENT" "$C_RESET"
        printf "%s         .'        '.%s\n" "$C_ACCENT" "$C_RESET"
        printf "%s        (            )%s\n" "$C_ACCENT" "$C_RESET"
        printf "%s         '.        .'%s\n" "$C_ACCENT" "$C_RESET"
        printf "%s           '------'%s\n" "$C_ACCENT" "$C_RESET"
    fi
    printf '%s       https://github.com/%s%s\n\n' "$C_DIM" "$REPO" "$C_RESET"
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
  --version <v>         Release or channel to install: v1.2.3, dev, or latest
                        (default: latest).
  --external-url <url>  Public hostname or URL browsers and GitHub use for
                        the controller. A bare hostname implies https://.
  --port <number>        Host port for the controller (default: 8080). It is
                        checked before deployment files are written.
  --prefix <dir>        Where to put the binary (default: /usr/local/bin).
  --non-interactive     Never prompt. Requires --answers, or enough flags.
  --answers <file>      YAML answer file for unattended setup. Implies
                        --non-interactive and --yes.
  --upgrade             Upgrade the existing deployment, keeping its configuration
                        and credentials. Refresh stock runner images, pull the
                        matching service image and restart the service/container.
                        Anything this release expects that the deployment
                        lacks -- the shared folder, its mount, a deleted
                        docker-compose.yml -- is listed and added once you
                        agree; --yes adds it without asking, and
                        --non-interactive leaves it and says how to add it.
  --config-dir <dir>    Existing configuration directory (for a custom install).
  --image <ref>         With --upgrade: replacement for a custom container image.
  --no-init             Install the binary only; do not run `zoomies init`.
  --yes, -y             Do not ask before installing. Implied by
                        --non-interactive and --answers.
  --allow-unverified    Install even when the download's SHA-256 cannot be
                        checked against the release's checksums.txt. Only for
                        a private mirror that does not publish one.
  --uninstall           Run `zoomies uninstall`, then remove the binary.
  --demo                Look around without installing anything. Downloads the
                        binary to a temporary folder, checks it, and runs
                        `zoomies demo` from there: a controller on this machine
                        only, with a fleet already in it. No sudo, no service,
                        no questions, and both are deleted when you press
                        Ctrl-C. Takes --version, --port and --allow-unverified.
  --no-animation        Do not animate the waits, even on a terminal. Same as
                        ZOOMIES_NO_ANIMATION.
  --preview             Play the installer's animations and exit. Installs
                        nothing and changes nothing on the host.
  --help, -h            This.

Environment:
  ZOOMIES_VERSION, ZOOMIES_PREFIX, ZOOMIES_REPO, ZOOMIES_BASE_URL
  ZOOMIES_ASSET_WAIT    Seconds to wait for a just-published release's
                        binaries to finish uploading (default 300, 0 to
                        fail immediately).
  NO_COLOR              Disable colour.
  ZOOMIES_NO_ANIMATION  Set to any value to keep the waits as plain text.
  ZOOMIES_PREVIEW       Set to any value for the effect of --preview.

Examples:
  # A single VM, interactive
  curl -fsSL https://zoomies.sh/install.sh | sh

  # See it with a fleet in it first, installing nothing
  curl -fsSL https://zoomies.sh/install.sh | sh -s -- --demo

  # Add a runner host in one line
  curl -fsSL https://zoomies.sh/install.sh | sh -s -- \
      --mode agent --controller https://zoomies.example.com --join-token zoojoin_...

  # Upgrade a runner host without redeeming another join token
  curl -fsSL https://zoomies.sh/install.sh | sh -s -- --upgrade --mode agent

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

# ---------------------------------------------------------------------------
# Everything below runs inside main, which is called on the last line
#
# `curl | sh` feeds this script to a shell as it arrives, and a transfer that
# is cut short leaves the shell holding a prefix of it -- one that has parsed
# and would happily run. Half this script is not a smaller install, it is an
# install that stops in the middle of writing to /usr/local/bin. Inside a
# function nothing runs until the closing brace has been read, so a truncated
# download is a syntax error rather than a partial installation.
# ---------------------------------------------------------------------------
main() {

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
        --version)     needs_value --version $# "a release tag such as v1.2.3, dev, or latest"; VERSION="$2"; VERSION_GIVEN=1; shift 2 ;;
        --version=*)   VERSION="${1#*=}"; VERSION_GIVEN=1; shift ;;
        --external-url) needs_value --external-url $# "the controller's public hostname or URL"; EXTERNAL_URL="$2"; shift 2 ;;
        --external-url=*) EXTERNAL_URL="${1#*=}"; shift ;;
        --port)        needs_value --port $# "a TCP port from 1 to 65535"; PORT="$2"; shift 2 ;;
        --port=*)      PORT="${1#*=}"; shift ;;
        --prefix)      needs_value --prefix $# "a directory to install the binary into"; PREFIX="$2"; PREFIX_GIVEN=1; shift 2 ;;
        --prefix=*)    PREFIX="${1#*=}"; PREFIX_GIVEN=1; shift ;;
        # --answers and --non-interactive both imply --yes, and have to: an
        # unattended run has nobody to answer a confirmation, and the one that
        # guards a same-version reinstall would otherwise exit 0 without ever
        # reaching `zoomies init` -- reporting a host as configured that was
        # never touched.
        --answers)     needs_value --answers $# "a path to a YAML answer file"; ANSWERS="$2"; NON_INTERACTIVE=1; ASSUME_YES=1; shift 2 ;;
        --answers=*)   ANSWERS="${1#*=}"; NON_INTERACTIVE=1; ASSUME_YES=1; shift ;;
        --non-interactive) NON_INTERACTIVE=1; ASSUME_YES=1; shift ;;
        --upgrade)     DO_UPGRADE=1; RUN_INIT=0; shift ;;
        --config-dir)  needs_value --config-dir $# "the existing configuration directory"; CONFIG_DIR="$2"; shift 2 ;;
        --config-dir=*) CONFIG_DIR="${1#*=}"; shift ;;
        --image)       needs_value --image $# "the replacement container image"; UPGRADE_IMAGE="$2"; shift 2 ;;
        --image=*)     UPGRADE_IMAGE="${1#*=}"; shift ;;
        --no-init)     RUN_INIT=0; BINARY_ONLY=1; shift ;;
        --uninstall)   DO_UNINSTALL=1; shift ;;
        --demo)        DO_DEMO=1; shift ;;
        -y|--yes)      ASSUME_YES=1; YES_GIVEN=1; shift ;;
        --allow-unverified) ALLOW_UNVERIFIED=1; shift ;;
        --no-animation) NO_ANIM=1; shift ;;
        --preview)     PREVIEW=1; shift ;;
        -h|--help)     usage; exit 0 ;;
        *)             die "unknown option: $1 (try --help)" ;;
    esac
done

# Decide whether there is going to be any motion before a single line of it is
# printed, then --preview -- which is the one invocation that exists purely to be
# looked at -- plays the whole set and leaves before anything is detected.
init_motion
if [ "$PREVIEW" -eq 1 ]; then
    preview_all
    exit 0
fi

# ---------------------------------------------------------------------------
# The answers we already have, checked before anything is downloaded
#
# A value this script does not understand used to travel all the way through a
# release lookup, a 25 MB download and a system write before `zoomies init`
# rejected it. Every check below is one this script can make in a millisecond,
# so it makes it first.
# ---------------------------------------------------------------------------

if [ "$DO_UPGRADE" -eq 1 ]; then
    if [ "$DO_UNINSTALL" -eq 1 ] || [ "$BINARY_ONLY" -eq 1 ] || [ -n "$ANSWERS" ] ||
       [ -n "$JOIN_TOKEN" ] || [ -n "$CONTROLLER_URL" ] || [ -n "$DEPLOYMENT" ] ||
       [ -n "$EXTERNAL_URL" ] || [ -n "$PORT" ]; then
        die "--upgrade uses the existing deployment; do not combine it with setup, join, --no-init or --uninstall options."
    fi
elif [ -n "$UPGRADE_IMAGE" ]; then
    die "--image belongs to --upgrade."
fi

# --demo installs nothing, so a flag about installing, joining, upgrading or
# removing is a misunderstanding of it. Saying so costs a line; ignoring the
# flag is how somebody comes to believe they have set up a controller.
if [ "$DO_DEMO" -eq 1 ]; then
    if [ "$DO_UPGRADE" -eq 1 ] || [ "$DO_UNINSTALL" -eq 1 ] || [ "$BINARY_ONLY" -eq 1 ] ||
       [ -n "$MODE" ] || [ -n "$DEPLOYMENT" ] || [ -n "$CONTROLLER_URL" ] || [ -n "$JOIN_TOKEN" ] ||
       [ -n "$EXTERNAL_URL" ] || [ -n "$ANSWERS" ] || [ -n "$CONFIG_DIR" ] ||
       [ -n "$UPGRADE_IMAGE" ] || [ "$PREFIX_GIVEN" -eq 1 ]; then
        die "--demo installs nothing; do not combine it with setup, join, upgrade, uninstall or --prefix options." \
            "It takes --version, --port and --allow-unverified, and nothing else."
    fi
fi

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
    *[!A-Za-z0-9._-]*) die "--version $VERSION does not look like a release tag or channel. Try latest, dev, or v1.2.3." ;;
esac

case "$PORT" in
    "") ;;
    *[!0-9]*) die "--port must be a whole number from 1 to 65535." ;;
    *)
        if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
            die "--port must be from 1 to 65535."
        fi ;;
esac

if [ -n "$ANSWERS" ] && [ ! -r "$ANSWERS" ]; then
    die "the answer file $ANSWERS cannot be read." \
        "\`zoomies init --print-answers\` writes an annotated one you can start from."
fi

# An agent has nothing to prompt for and nothing to guess: it needs a
# controller to join and a token to join it with. Interactively `zoomies init`
# asks; with no terminal there is nobody to ask, so say it now rather than
# after the download.
if [ "$DO_UPGRADE" -eq 0 ] && [ "$MODE" = agent ] && [ "$NON_INTERACTIVE" -eq 1 ] && [ -z "$ANSWERS" ]; then
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

# Ask for the two installation-wide values before downloading anything. The
# build choice has to live here because it decides which binary is fetched;
# the external address is carried into `zoomies init` so it is asked once.
prompt_install_inputs() {
    if [ "$NON_INTERACTIVE" -eq 0 ] && [ "$ASSUME_YES" -eq 0 ] &&
       [ "$VERSION_GIVEN" -eq 0 ] && have_tty; then
        printf '%s   ?? %sWhich build should be installed?%s\n' "$C_ACCENT" "$C_RESET" "$C_RESET"
        note "1) Latest stable (default)"
        note "2) Development -- newest build from main"
        printf '%s      Choice [1]: %s' "$C_DIM" "$C_RESET"
        read -r reply < /dev/tty || reply=""
        case "$reply" in
            ""|1|latest) VERSION=latest ;;
            2|dev|development) VERSION=dev ;;
            *) die "choose 1 for latest stable or 2 for development, or pass --version latest|dev." ;;
        esac
        say ""
    fi

    if [ "$RUN_INIT" -eq 1 ] && [ "$DO_UPGRADE" -eq 0 ] &&
       [ "$NON_INTERACTIVE" -eq 0 ] && [ -n "$MODE" ] && [ "$MODE" != agent ] &&
       [ -z "$ANSWERS" ] && [ -z "$EXTERNAL_URL" ] && have_tty; then
        printf '%s   ?? %sExternal hostname or URL%s\n' "$C_ACCENT" "$C_RESET" "$C_RESET"
        note "The address browsers and GitHub use, for example zoomies.example.com."
        note "A bare hostname uses https://. Include http:// and a port for local-only setup."
        printf '%s      Address: %s' "$C_DIM" "$C_RESET"
        read -r EXTERNAL_URL < /dev/tty || EXTERNAL_URL=""
        [ -n "$EXTERNAL_URL" ] || die "the external hostname cannot be empty; pass --external-url <hostname-or-url>."
        say ""
    fi

    if [ "$RUN_INIT" -eq 1 ] && [ "$DO_UPGRADE" -eq 0 ] &&
       [ "$NON_INTERACTIVE" -eq 0 ] && [ -n "$MODE" ] && [ "$MODE" != agent ] &&
       [ -z "$ANSWERS" ] && [ -z "$PORT" ] && have_tty; then
        printf '%s   ?? %sController port%s\n' "$C_ACCENT" "$C_RESET" "$C_RESET"
        note "8080 is the default. Zoomies checks that the port is available before installing."
        printf '%s      Port [8080]: %s' "$C_DIM" "$C_RESET"
        read -r PORT < /dev/tty || PORT=""
        [ -n "$PORT" ] || PORT=8080
        case "$PORT" in
            *[!0-9]*) die "the port must be a whole number from 1 to 65535." ;;
            *)
                if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
                    die "the port must be from 1 to 65535."
                fi ;;
        esac
        say ""
    fi
}

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

    # Both units, because a runner host runs zoomies-agent and no controller.
    # The names come from internal/installer/service.go -- UnitController,
    # UnitAgent, and the launchd labels -- rather than being guessed at.
    case "$INIT_SYSTEM" in
        systemd)
            if systemctl is-active --quiet zoomies 2>/dev/null; then
                EXISTING_RUNNING=1
                EXISTING_UNIT=zoomies
            elif systemctl is-active --quiet zoomies-agent 2>/dev/null; then
                EXISTING_RUNNING=1
                EXISTING_UNIT=zoomies-agent
            fi
            ;;
        launchd)
            for label in sh.zoomies.controller sh.zoomies.agent; do
                # A non-root install's job is in the user domain, not system/.
                if launchctl print "system/$label" >/dev/null 2>&1 ||
                   launchctl print "gui/$(id -u)/$label" >/dev/null 2>&1; then
                    EXISTING_RUNNING=1
                    EXISTING_UNIT="$label"
                    break
                fi
            done
            ;;
    esac
}

# Restarting is spelled differently on each init system, and an operator who
# has just been told their upgrade has not taken effect should not have to look
# the command up.
restart_hint() {
    unit="${EXISTING_UNIT:-zoomies}"
    case "$INIT_SYSTEM" in
        systemd) printf 'sudo systemctl restart %s' "$unit" ;;
        launchd)
            if [ "$(id -u)" = 0 ]; then
                printf 'sudo launchctl kickstart -k system/%s' "$unit"
            else
                printf 'launchctl kickstart -k gui/%s/%s' "$(id -u)" "$unit"
            fi
            ;;
        # No OpenRC service is written -- `zoomies init` deploys with compose on
        # these hosts and says why -- so what restarts is the container.
        openrc)  printf '%s restart' "${COMPOSE_CMD:-docker compose}" ;;
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
        # shellcheck disable=SC2086 # CURL_PROTO is two flags, deliberately split
        FETCH_ERROR=$(curl -fsSL $CURL_PROTO --retry 3 --retry-delay 1 -o "$2" "$1" 2>&1) && return 0
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
        # shellcheck disable=SC2086 # as above
        curl -fsSL $CURL_PROTO --retry 3 --retry-delay 1 "$1"
    else
        wget -qO- "$1"
    fi
}

# A tag becomes the target of the /releases/latest redirect the moment it is
# published, but the workflow that cross-compiles the binaries and attaches
# them to the release is still running for a few minutes after that. An install
# started inside that window asks for an asset that is genuinely on its way, so
# wait for it rather than sending the operator away with a 404. Only when the
# tag was resolved for them: an explicit --version missing its asset is a typo
# or a withdrawn release, and no amount of waiting fixes either.
wait_for_asset() {
    # wait_for_asset <url> <dest>
    [ "$RESOLVED_LATEST" -eq 1 ] || return 1
    case "$ASSET_WAIT" in ''|*[!0-9]*) return 1 ;; esac
    [ "$ASSET_WAIT" -gt 0 ] || return 1

    warn "$asset is not attached to $tag yet."
    hint "$tag was published very recently and its binaries are still" \
         "uploading. Waiting up to ${ASSET_WAIT}s for them."
    waited=0
    while [ "$waited" -lt "$ASSET_WAIT" ]; do
        # A bare sleep 15 is the emptiest possible terminal. Pacing it with the
        # runner says the same thing -- we are waiting, not stuck -- and the
        # elapsed timer in the loader counts up on its own.
        pulse 15 "$asset is still building"
        waited=$((waited + 15))
        if fetch "$1" "$2"; then
            ok "downloaded after ${waited}s"
            return 0
        fi
        [ "$Z_ANIM" -eq 1 ] || note "still waiting (${waited}s of ${ASSET_WAIT}s)"
    done
    return 1
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

# newest_release_tag names the most recent versioned release from the API. The
# rolling dev prerelease intentionally does not qualify: asking for latest must
# never turn into asking for main.
newest_release_tag() {
    # Only the real github.com has this API shape. A ZOOMIES_BASE_URL pointing
    # somewhere else -- a mirror, a test server -- gets the error instead of a
    # guess at what its API might be.
    [ "$BASE_URL" = "https://github.com/${REPO}/releases" ] || return 0
    api="https://api.github.com/repos/${REPO}/releases?per_page=20"
    if have curl; then
        # shellcheck disable=SC2086 # as above
        body=$(curl -fsSL $CURL_PROTO "$api" 2>/dev/null || printf '')
    else
        body=$(wget -qO- "$api" 2>/dev/null || printf '')
    fi
    printf '%s\n' "$body" | grep -o '"tag_name"[^,]*' |
        sed 's/.*: *"//; s/".*//' | grep '^v' | head -1
}

resolve_version() {
    [ "$VERSION" = latest ] || return 0
    step "Finding the latest release"
    # The lookup is one or two round trips and usually under a second, but on a
    # cold connection or a rate-limited API it is exactly the kind of pause that
    # looks like nothing is happening -- so the dog runs while it resolves.
    load_start "resolving latest"
    # The redirect target of /releases/latest names the tag without needing the
    # API, which keeps this working for unauthenticated users behind a rate limit.
    if have curl; then
        # shellcheck disable=SC2086 # as above
        url=$(curl -fsSLI $CURL_PROTO -o /dev/null -w '%{url_effective}' "$BASE_URL/latest" 2>/dev/null || printf '')
    else
        url=$(wget -qS --max-redirect=5 -O /dev/null "$BASE_URL/latest" 2>&1 |
              awk '/^  Location: /{print $2}' | tail -1)
    fi
    case "$url" in
        */tag/*) VERSION="${url##*/tag/}" ;;
        # No tag in the redirect. GitHub's /releases/latest only knows about
        # full releases, so while every Zoomies release is a pre-release it
        # redirects to the release index instead and the tag has to come from
        # somewhere else. This is the fallback rather than the first choice
        # because it spends an unauthenticated API call, which is rate limited
        # per address and shared with everyone else behind the same NAT.
        *) VERSION=$(newest_release_tag) ;;
    esac
    load_stop
    case "$VERSION" in
        ""|latest)
            die "could not work out the latest release. Pass --version v1.2.3, or check that $BASE_URL is reachable." ;;
    esac
    RESOLVED_LATEST=1
    ok "latest is $VERSION"
}

# Downloads the release binary for this host into a fresh temporary directory
# and checks it against the release's checksums. It leaves the directory in
# $tmp and the verified, executable binary at $tmp/zoomies, with a trap that
# removes the directory however the script ends; whoever called it decides
# whether the binary is installed or only run.
download_binary() {
    tag="$VERSION"
    # dev is a real, moving prerelease asset. Numeric versions keep the
    # project's v-prefixed tag spelling, while already-prefixed versions pass
    # through unchanged.
    case "$tag" in dev|v*) ;; *) tag="v$tag" ;; esac
    asset="zoomies_${OS}_${ARCH}"
    url="$BASE_URL/download/$tag/$asset"

    tmp=$(mktemp -d "${TMPDIR:-/tmp}/zoomies-install.XXXXXX")
    # The temp-dir cleanup and the motion teardown share the trap, because an
    # interrupt here has to both remove the half-written download and stop the
    # loader that was watching it.
    # shellcheck disable=SC2064
    trap "rm -rf '$tmp'; anim_teardown" EXIT INT TERM

    step "Downloading $asset $tag"
    # The loader watches the file curl is writing, so the byte count it shows is
    # the transfer, not a guess. It runs in the background only while the fetch
    # itself is silent (its errors are captured, never echoed), so the two never
    # contend for the terminal. load_stop is called on every way out of the fetch
    # so a failure hands a clean line back to wait_for_asset or die.
    load_start "downloading" "$tmp/zoomies"
    if fetch "$url" "$tmp/zoomies"; then
        _dl_ok=1
    else
        _dl_ok=0
    fi
    load_stop
    if [ "$_dl_ok" -eq 0 ]; then
        wait_for_asset "$url" "$tmp/zoomies" ||
            die "could not download $asset $tag." \
                "$url" \
                "${FETCH_ERROR:-the transfer failed with no message.}" \
                "Check that $tag exists at $BASE_URL," \
                "and that this host can reach it." \
                "If $tag was published minutes ago its binaries may still be" \
                "building; try again shortly, or pass --version <earlier tag>."
    fi

    # A download that cannot be verified is refused, not warned about. All
    # three ways verification can fail to happen used to print one dim line and
    # carry on -- and on the `curl | sh` path that line scrolls away above the
    # sudo password prompt, so nobody ever saw it. --allow-unverified is the
    # deliberate opt-in for a private mirror that publishes no checksums.
    step "Verifying the checksum"
    # "Sniffing the goods" is the whole point of this step and the dog is the
    # one who checks. The fetch is into a file rather than a command substitution
    # so the background loader can run beside a silent transfer, exactly as it
    # did for the download; the success/emptiness test below is unchanged.
    load_start "sniffing the goods"
    sums=''
    if fetch_stdout "$BASE_URL/download/$tag/checksums.txt" 2>/dev/null > "$tmp/sums"; then
        sums=$(cat "$tmp/sums" 2>/dev/null)
    fi
    load_stop
    unverified=""
    if [ -n "$sums" ]; then
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
}

install_binary() {
    download_binary
    if [ "$DO_UPGRADE" -eq 1 ]; then
        step "Checking the existing deployment before replacing its binary"
        upgrade_with "$tmp/zoomies" --check ||
            die "the upgrade preflight failed; the installed binary and deployment were left in place." \
                "Check the error above. The selected release must support zoomies upgrade."
    fi

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
    # A download that is not what it claims to be -- a mirror serving an HTML
    # error page with a 200, the wrong architecture, a truncated transfer --
    # used to be papered over here by `|| printf "$tag"`, and the operator's
    # next line was the shell's "Exec format error" from the handoff.
    if ! NEW_VERSION=$("$PREFIX/zoomies" version --short 2>/dev/null); then
        if ! "$PREFIX/zoomies" --help >/dev/null 2>&1; then
            die "$PREFIX/zoomies was written, but it will not run on this host." \
                "The download may be for the wrong architecture ($OS/$ARCH was detected)," \
                "or it may be incomplete. Try again, and if it happens twice, report it."
        fi
        NEW_VERSION="$tag"
    fi
    if [ -n "$EXISTING_VERSION" ] && [ "$EXISTING_VERSION" != "$NEW_VERSION" ]; then
        ok "$EXISTING_VERSION replaced by $NEW_VERSION"
    else
        ok "$NEW_VERSION installed"
    fi
    if [ "$EXISTING_RUNNING" -eq 1 ] && [ "$DO_UPGRADE" -eq 0 ]; then
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
    # tmp is gone; keep only the teardown trap for the rest of the run so a later
    # interrupt still shows the cursor.
    trap 'anim_teardown' EXIT INT TERM
}

# The new binary understands its own upgrade procedure. Run its read-only
# check before replacing anything; older releases cannot silently fall through
# into setup and spend a new join token.
upgrade_with() {
    upgrade_binary="$1"
    shift
    set -- upgrade --installed-binary "$PREFIX/zoomies" "$@"
    [ -n "$MODE" ] && set -- "$@" --mode "$MODE"
    [ -n "$CONFIG_DIR" ] && set -- "$@" --config-dir "$CONFIG_DIR"
    [ -n "$UPGRADE_IMAGE" ] && set -- "$@" --image "$UPGRADE_IMAGE"
    [ -n "$RUNTIME_SOCKET" ] && set -- "$@" --docker-host "unix://$RUNTIME_SOCKET"
    case "$RUNTIME" in docker|podman) set -- "$@" --runtime "$RUNTIME" ;; esac
    [ "$YES_GIVEN" -eq 1 ] && set -- "$@" --yes
    [ "$NON_INTERACTIVE" -eq 1 ] && set -- "$@" --non-interactive
    # The upgrade asks before it adds anything the deployment lacks, and this
    # script usually arrives through a pipe, so stdin is the rest of the
    # script. The terminal the setup interview uses carries the question; the
    # read-only --check never asks, so it keeps whatever stdin it has.
    upgrade_tty=0
    case " $* " in *" --check "*) ;; *)
        if [ "$NON_INTERACTIVE" -eq 0 ] && [ "$YES_GIVEN" -eq 0 ] && [ ! -t 0 ] && have_tty; then
            upgrade_tty=1
        fi ;;
    esac
    if [ "$upgrade_tty" -eq 1 ]; then
        upgrade_run "$upgrade_binary" "$@" </dev/tty
    else
        upgrade_run "$upgrade_binary" "$@"
    fi
}

upgrade_run() {
    upgrade_run_binary="$1"
    shift
    if [ -n "$ELEVATE" ] && [ "$OS" = linux ]; then
        run_privileged "$upgrade_run_binary" "$@"
    else
        "$upgrade_run_binary" "$@"
    fi
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

    # `zoomies uninstall` asks before it removes anything, and this script is
    # most often reached through a pipe -- which leaves stdin consumed, so the
    # question is answered by EOF, the uninstall reports "nothing was removed",
    # and set -e stops us before the binary is deleted. The same /dev/tty that
    # carries the setup interview carries this one.
    if [ "$ASSUME_YES" -eq 0 ] && [ ! -t 0 ]; then
        if have_tty; then
            exec 0</dev/tty
        else
            die "there is no terminal here to confirm a removal on." \
                "Re-run with --yes to remove Zoomies unattended."
        fi
    fi

    if [ -w "$EXISTING" ] || [ "$(id -u)" = 0 ]; then
        # shellcheck disable=SC2086
        "$EXISTING" uninstall $args
    else
        # shellcheck disable=SC2086
        run_privileged "$EXISTING" uninstall $args
    fi
    # Only once the uninstall actually succeeded -- set -e achieves that today
    # by accident, and an accident is a poor guard on a delete.
    if [ -w "$(dirname "$EXISTING")" ]; then rm -f "$EXISTING"; else run_privileged rm -f "$EXISTING"; fi
    ok "removed $EXISTING"
    exit 0
}

# ~40 MB of binary lands in $TMPDIR and is then copied into $PREFIX. A full
# /tmp otherwise surfaces as a truncated download and therefore a checksum
# mismatch, carrying "Do not run this binary ... report it" for what is a disk
# problem.
check_space() {
    have df || return 0
    free=$(df -Pk "$1" 2>/dev/null | awk 'NR==2 {print $4}')
    [ -n "$free" ] || return 0
    [ "$free" -ge 102400 ] ||
        die "$1 has only $((free / 1024)) MB free, and Zoomies needs about 100 MB." \
            "Free some space there, or point TMPDIR somewhere with room."
}

# A missing downloader surfaces as "could not work out the latest release",
# which sends the operator to look at their network and at a --version flag
# that fails the same way one step later. It is a missing package, and saying
# so is one line.
need_downloader() {
    if have curl || have wget; then return 0; fi
    die "neither curl nor wget is installed, and one of them is needed to download Zoomies." \
        "Debian or Ubuntu:  sudo apt install curl" \
        "Alpine:            sudo apk add curl" \
        "Fedora:            sudo dnf install curl"
}

# ---------------------------------------------------------------------------
# --demo
#
# Somebody deciding whether Zoomies is worth a machine and a GitHub organisation
# used to have two choices: answer the installer's questions, or read about it.
# This is the third. It downloads and checks the binary exactly as an install
# would, and then runs `zoomies demo` from the temporary directory it landed in:
# a controller on this machine only, with a fleet already in it, that deletes its
# own data when it stops. Nothing is written outside $TMPDIR and nothing asks for
# sudo, because nothing here is installed.
# ---------------------------------------------------------------------------
run_demo() {
    need_downloader
    check_space "${TMPDIR:-/tmp}"
    resolve_version
    download_binary
    # An install copies the binary somewhere that runs programs; this runs it
    # where it landed, and a temporary directory is the most common place for a
    # hardened host to refuse that. The failure has a one-line fix, so it gets
    # said rather than left as "Permission denied" from the shell.
    if ! "$tmp/zoomies" version --short >/dev/null 2>&1; then
        die "the downloaded binary will not run from $tmp." \
            "If ${TMPDIR:-/tmp} is mounted noexec, point TMPDIR somewhere that is not:" \
            "  TMPDIR=\$HOME sh install.sh --demo" \
            "Otherwise the download may be for the wrong architecture ($OS/$ARCH was detected) or incomplete."
    fi
    say ""
    step "Starting the demo"
    # The binary is not exec'd, so that this shell is still here afterwards to
    # delete it: an exec never runs the EXIT trap, and a temporary directory
    # holding a 50 MB binary is not what somebody who asked for "nothing
    # installed" expects to find in /tmp the next morning.
    set -- demo
    [ -n "$PORT" ] && set -- "$@" --port "$PORT"
    demo_status=0
    "$tmp/zoomies" "$@" || demo_status=$?
    say ""
    ok "Nothing was installed on this machine."
    note "to set Zoomies up for real: curl -fsSL https://zoomies.sh/install.sh | sh"
    exit "$demo_status"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

banner
detect_platform

# --demo leaves here, before anything on this host is probed, offered or
# written: the runtime checks below can offer to install Docker, and somebody
# looking around has asked for none of it.
if [ "$DO_DEMO" -eq 1 ]; then
    run_demo
fi

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

# report_runtime prints the runtime/compose fields of the host report. It is
# called twice on a host with nothing installed: once for the initial finding,
# and again after offer_container_runtime, so the report an operator scrolls
# back to matches what actually ended up on the host rather than the moment
# before they fixed it.
report_runtime() {
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
}

step "Checking this host"
field os "$OS/$ARCH ($DISTRO)"
case "$INIT_SYSTEM" in
    none) field init "none -- nothing here will restart Zoomies after a reboot" ;;
    *)    field init "$INIT_SYSTEM" ;;
esac
report_runtime
for p in 8080 443; do
    port_free "$p" || field ports "$p is already in use; setup will offer another"
done
if [ "$PORT_CHECKED" -eq 0 ]; then
    field ports "not checked -- no ss or netstat here"
fi

# ---------------------------------------------------------------------------
# Nothing installed, and somebody to ask
#
# `zoomies init` can fall back to the process backend when no runtime answers,
# but that means every job runs directly on this host with none of the
# isolation the rest of this project exists to provide. A host with neither
# Docker nor Podman installed used to slide into that fallback without ever
# being asked, so the operator found out from the backend picker's warning
# rather than from a question they could have said yes to. This offer is the
# fix, gated the same way the build-type and external-URL prompts are: an
# interactive run with a terminal to ask on, and nothing already decided by
# --upgrade, --no-init or the answer file.
# ---------------------------------------------------------------------------
offer_container_runtime() {
    [ "$RUNTIME" = none ] || return 0
    [ "$OS" = linux ] || return 0
    [ "$DO_UPGRADE" -eq 0 ] && [ "$RUN_INIT" -eq 1 ] || return 0
    [ "$NON_INTERACTIVE" -eq 0 ] && [ -z "$ANSWERS" ] && have_tty || return 0

    say ""
    printf '%s   ?? %sNo container runtime found on this host.%s\n' "$C_ACCENT" "$C_RESET" "$C_RESET"
    note "Docker or Podman gives each job its own throwaway container. Without one,"
    note "jobs run directly on this host and can touch anything the agent user can --"
    note "\`zoomies init\` will offer that as the process backend if you skip this."
    printf '%s      Install a container runtime now? [y/N] %s' "$C_DIM" "$C_RESET"
    read -r reply < /dev/tty || reply=""
    case "$reply" in
        y|Y|yes|Yes) ;;
        *) say ""; return 0 ;;
    esac

    printf '%s   ?? %sWhich one?%s\n' "$C_ACCENT" "$C_RESET" "$C_RESET"
    note "1) Docker (default)"
    note "2) Podman"
    printf '%s      Choice [1]: %s' "$C_DIM" "$C_RESET"
    read -r reply < /dev/tty || reply=""
    case "$reply" in
        ""|1|docker|Docker) install_runtime_pkg docker ;;
        2|podman|Podman)    install_runtime_pkg podman ;;
        *) die "choose 1 for Docker or 2 for Podman." ;;
    esac

    detect_runtime
    detect_compose
    say ""
    note "now:"
    report_runtime
    say ""
}

# install_runtime_pkg <docker|podman> installs it as root, by whichever route
# fits this host. Docker ships one convenience script that covers every distro
# this installer supports, so it is used as-is rather than reinvented as a
# worse per-distro dance; Podman has no such script, so this dispatches to the
# package manager /etc/os-release named for detect_platform.
install_runtime_pkg() {
    pkg="$1"
    step "Installing $pkg"
    if [ "$pkg" = docker ]; then
        if have curl; then
            run_privileged sh -c "curl -fsSL $CURL_PROTO https://get.docker.com | sh" ||
                die "the Docker install script failed; see the output above." \
                    "Install it yourself: https://docs.docker.com/engine/install/"
        else
            run_privileged sh -c "wget -qO- https://get.docker.com | sh" ||
                die "the Docker install script failed; see the output above." \
                    "Install it yourself: https://docs.docker.com/engine/install/"
        fi
    else
        pm=""
        case "$DISTRO" in
            ubuntu|debian|raspbian)              pm="apt-get" ;;
            fedora|rhel|centos|rocky|almalinux)   pm="dnf" ;;
            opensuse*|sles)                       pm="zypper" ;;
            arch|manjaro)                         pm="pacman" ;;
            alpine)                               pm="apk" ;;
        esac
        [ -n "$pm" ] || die "$DISTRO has no package manager this installer knows how to drive." \
            "Install Podman yourself: https://podman.io/docs/installation"
        case "$pm" in
            apt-get) run_privileged apt-get update && run_privileged apt-get install -y podman ;;
            dnf)     run_privileged dnf install -y podman ;;
            zypper)  run_privileged zypper --non-interactive install podman ;;
            pacman)  run_privileged pacman -Sy --noconfirm podman ;;
            apk)     run_privileged apk add podman ;;
        esac ||
            die "installing podman failed; see the output above."
    fi
    ok "$pkg installed"
}

offer_container_runtime

need_downloader

check_space "${TMPDIR:-/tmp}"

detect_existing
if [ "$DO_UPGRADE" -eq 1 ] && [ "$PREFIX_GIVEN" -eq 0 ] && [ -n "$EXISTING" ]; then
    PREFIX=$(dirname "$EXISTING")
fi
preflight_prefix
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

prompt_install_inputs
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

# Same version already here: there is nothing to *download*, which is not the
# same as nothing to do, and conflating the two is what sent operators round in
# circles.
#
# The controller's own join command carries --mode agent, --controller and
# --join-token. Run on a host that already had this version -- which is every
# host, since the command pins no version and "latest" is whatever release is
# newest -- this line exited 0 and dropped all three on the floor. The host was
# never joined, the operator was told to run `zoomies init` instead, and the
# advice below sent them to --yes, which re-downloads the identical binary and
# joins nothing. That is the loop: the one thing they asked for was the one
# thing that never happened.
#
# So the early exit is now only for the bare `curl | sh` with no setup asked
# for. When the invocation carries intent -- a mode, a controller, a token --
# the download is skipped and the run goes on to the handoff at the bottom,
# which is where the joining happens.
SKIP_DOWNLOAD=0
if [ "$DO_UPGRADE" -eq 0 ] && [ -n "$INSTALLED_TAG" ] && [ "$INSTALLED_TAG" = "$WANTED_TAG" ]; then
    if [ -n "$MODE" ] || [ -n "$CONTROLLER_URL" ] || [ -n "$JOIN_TOKEN" ] || [ -n "$ANSWERS" ]; then
        SKIP_DOWNLOAD=1
    elif [ "$ASSUME_YES" -eq 0 ]; then
        ok "$EXISTING_VERSION is already installed at $EXISTING."
        note "to set this host up, run: zoomies init"
        note "to install a different build, add --version <tag>"
        note "to download this same version again anyway, add --yes"
        exit 0
    fi
fi

step "About to"
if [ "$SKIP_DOWNLOAD" -eq 1 ]; then
    field keep "$EXISTING_VERSION, already at $EXISTING -- it is the version asked for"
elif [ -n "$EXISTING" ]; then
    field install "$VERSION over $EXISTING_VERSION, at $PREFIX/zoomies"
else
    field install "zoomies $VERSION ($OS/$ARCH) to $PREFIX/zoomies"
fi
if [ -n "$ELEVATE" ]; then
    if [ "$RUN_INIT" -eq 1 ] && [ "$OS" = linux ] && [ "$MODE" != agent ]; then
        field privilege "$ELEVATE, to write to $PREFIX and to install the service"
    else
        field privilege "$ELEVATE, to write to $PREFIX"
    fi
    field "" "it may ask for your password"
fi
if [ "$DO_UPGRADE" -eq 1 ]; then
    field "then" "upgrade the existing service and images, keeping its configuration and credentials"
    field "jobs" "existing runner containers stay running; reporting resumes after the restart"
elif [ "$RUN_INIT" -eq 0 ]; then
    field "then" "nothing -- --no-init was given, so setup is yours to run"
elif [ -n "$MODE" ]; then
    field "then" "run \`zoomies init\` to set this host up as $MODE"
else
    field "then" "run \`zoomies init\`, which asks what this host should be"
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
case "$INSTALLED_TAG:$WANTED_TAG" in
    [0-9]*:[0-9]*)
        if [ "$INSTALLED_TAG" != "$WANTED_TAG" ] &&
           printf '1\n' | sort -V >/dev/null 2>&1 &&
           [ "$(printf '%s\n%s\n' "$INSTALLED_TAG" "$WANTED_TAG" | sort -V | tail -1)" = "$INSTALLED_TAG" ]; then
            warn "$VERSION is older than the installed $EXISTING_VERSION, so this is a downgrade."
            hint "Back up the database first -- /var/lib/zoomies/zoomies.db on a native install, the zoomies-data volume on a container one." \
                 "An older build may not read a schema a newer one wrote."
        fi ;;
esac

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

if [ "$SKIP_DOWNLOAD" -eq 1 ]; then
    ok "$EXISTING_VERSION is already installed at $EXISTING, so it was kept."
else
    install_binary
fi

if [ "$DO_UPGRADE" -eq 1 ]; then
    say ""
    upgrade_with "$PREFIX/zoomies" || die "the binary is installed, but the deployment upgrade did not finish; fix the error above and run --upgrade again."
    exit 0
fi

if [ "$RUN_INIT" -eq 0 ]; then
    say ""
    if [ "$SKIP_DOWNLOAD" -eq 1 ]; then
        ok "Binary left as it was. Nothing else on this host was touched."
    elif [ -n "$EXISTING" ]; then
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
[ -n "$EXTERNAL_URL" ] && set -- "$@" --external-url "$EXTERNAL_URL"
[ -n "$PORT" ] && set -- "$@" --port "$PORT"
[ -n "$ANSWERS" ] && set -- "$@" --answers "$ANSWERS"
[ -n "$CONFIG_DIR" ] && set -- "$@" --config-dir "$CONFIG_DIR"
[ "$NON_INTERACTIVE" -eq 1 ] && set -- "$@" --non-interactive
[ "$ASSUME_YES" -eq 1 ] && set -- "$@" --yes

# ---------------------------------------------------------------------------
# The interview has to be able to finish
#
# On the documented one-liner as an ordinary user, the binary is installed with
# sudo and setup then runs unprivileged. `zoomies init` quietly retargets
# everything at the operator's home -- ~/.config/zoomies rather than
# /etc/zoomies, the service running as them rather than a dedicated account --
# still offers "the binary under systemd", and fails at the second-to-last step
# with "writing /etc/systemd/system/zoomies.service: permission denied". By
# then the GitHub App and the administrator have been created against a
# configuration directory that a re-run under sudo will not look in.
#
# So the elevation the binary needed is carried through to setup as well, which
# is what the operator expects from a script that has already asked for their
# password.
#
# An agent join is not exempt. It writes the same kind of unit, and it redeems
# its single-use join token before it gets that far, so a join that fails at
# the unit has spent the token and left its credentials under the operator's
# home -- the retry under sudo needs a new token and finds a different state
# directory.
# ---------------------------------------------------------------------------
ELEVATE_INIT=""
if [ -n "$ELEVATE" ] && [ "$OS" = linux ]; then
    ELEVATE_INIT="$ELEVATE"
fi

say ""
# The dog has arrived. This is the only place the finale runs: --no-init and
# --uninstall exit above with their own closing line, and only the interactive
# handoff earns the little skid to a stop before setup takes the lead.
finale "${NEW_VERSION:-$VERSION}"
say ""
if [ -n "$ELEVATE_INIT" ]; then
    step "Handing over to \`$ELEVATE_INIT zoomies init\`"
    note "setup installs a service and writes to /etc/zoomies, so it runs as root --"
    note "the same $ELEVATE_INIT that installed the binary a moment ago."
else
    step "Handing over to \`zoomies init\`"
fi
say ""

# Piping this script into sh leaves stdin consumed, so an interactive setup has
# no terminal to read from. Reconnect one when there is a terminal to reconnect;
# have_tty opens /dev/tty rather than testing its permission bits, because the
# bits are readable in plenty of places the open is not.
if [ "$NON_INTERACTIVE" -eq 0 ] && [ ! -t 0 ] && have_tty; then
    # shellcheck disable=SC2086
    exec $ELEVATE_INIT "$PREFIX/zoomies" "$@" < /dev/tty
fi
# shellcheck disable=SC2086
exec $ELEVATE_INIT "$PREFIX/zoomies" "$@"
}

main "$@"
