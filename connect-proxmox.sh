#!/bin/sh
# Zoomies Proxmox connector, the short form of the setup command the provider
# wizard generates.
#
#   curl -fsSL https://zoomies.sh/connect-proxmox.sh | sudo sh -s -- \
#       --version <tag> --key <key> --controller <url> --setup-id <id> --token <token>
#
# The wizard prints the whole line with the values filled in. Reading this file
# first is safe, and recommended: it downloads one release binary, checks its
# SHA-256 against the release's checksums.txt, keeps a private copy under
# /var/lib/zoomies-proxmox and runs `zoomies providers connect-proxmox` with the
# values it was given. It never touches an installed runner host: no
# /usr/local/bin, no zoomies-agent unit, no join token.
#
# This is the same work the wizard's "Show the full command" text does, in a
# file that is checked in CI instead of pasted from a browser.
#
# POSIX sh, checked with dash in CI.

set -eu
umask 077

die() { printf 'zoomies: %s\n' "$*" >&2; exit 1; }

# Everything runs inside main, called on the last line, so a transfer cut short
# is a syntax error rather than half a setup.
main() {
    version="" key="" controller="" setup_id="" token=""
    while [ $# -gt 0 ]; do
        [ $# -ge 2 ] || die "$1 needs a value; copy the complete command from the provider wizard."
        case "$1" in
            --version)    version=$2 ;;
            --key)        key=$2 ;;
            --controller) controller=$2 ;;
            --setup-id)   setup_id=$2 ;;
            --token)      token=$2 ;;
            *)            die "unknown option $1; copy the complete command from the provider wizard." ;;
        esac
        shift 2
    done
    if [ -z "$version" ] || [ -z "$key" ] || [ -z "$controller" ] || [ -z "$setup_id" ] || [ -z "$token" ]; then
        die "a value is missing; copy the complete command from the provider wizard."
    fi

    # The values name a directory and a download, so they are held to the
    # characters a tag and a key are made of before they are used for either.
    case "$version$key" in
        *[!A-Za-z0-9._-]*) die "the version or key contains a character that is not allowed; copy the complete command from the provider wizard." ;;
    esac

    [ "$(id -u)" -eq 0 ] || die "run this as root, for example: curl -fsSL https://zoomies.sh/connect-proxmox.sh | sudo sh -s -- ..."
    [ "$(uname -s)" = Linux ] || die "run this on the Proxmox host, which is Linux."

    dir="/var/lib/zoomies-proxmox/$key"
    mkdir -p "$dir"

    # A setup that already completed on this host reuses its binary, which may
    # have been upgraded since, instead of downloading another.
    receipt=$(printf '%s%s' "$setup_id" "$token" | sha256sum | cut -c1-12)
    if [ -f "$dir/completed-$receipt" ] && [ -x "$dir/zoomies" ]; then
        exec "$dir/zoomies" providers connect-proxmox --controller "$controller" --setup-id "$setup_id" --token "$token"
    fi

    case $(uname -m) in
        x86_64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) die "unsupported host architecture $(uname -m); Zoomies ships amd64 and arm64." ;;
    esac
    asset="zoomies_linux_$arch"
    base="https://github.com/eyupio/zoomies/releases/download/$version"

    tmp=$(mktemp -d "$dir/download.XXXXXX")
    trap 'rm -rf "$tmp"' EXIT
    curl --proto '=https' --proto-redir '=https' -fsSL "$base/$asset" -o "$tmp/$asset"
    curl --proto '=https' --proto-redir '=https' -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
    (
        cd "$tmp"
        awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print; found=1 } END { if (!found) exit 1 }' checksums.txt > selected ||
            die "checksums.txt has no entry for $asset."
        sha256sum -c selected
    )
    chmod 700 "$tmp/$asset"
    # Not exec: the trap has to remove the download once the connector is done.
    "$tmp/$asset" providers connect-proxmox --controller "$controller" --setup-id "$setup_id" --token "$token"
}

main "$@"
