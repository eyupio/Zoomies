#!/bin/sh
# Run the update helper against a real release, a real installed binary and a
# systemctl that only writes down what it is asked.
#
# Usage: test/upgrade/helper-check.sh
#
# The helper's Go tests stand a fake in for the engine, so nothing there shows
# the whole chain that runs when somebody presses Update: the helper reads the
# request, runs the installed binary's own `zoomies upgrade` for the tag, the
# engine downloads the release, checks it against checksums.txt, keeps the old
# binary as zoomies.previous, puts the new one in place and restarts the unit,
# and the helper writes result.json. This runs all of that, in a folder of its
# own, and checks what is left:
#
#   * the installed binary is the release asked for, and zoomies.previous holds
#     the bytes it replaced, which is the rollback the docs promise
#   * systemctl was asked to restart the agent's unit, so the new binary is the
#     one running and not only the one on disk
#   * result.json says ok, for the tag asked for, which is all the controller
#     ever reads
#   * and, with checksums.txt naming other bytes, nothing is replaced, nothing
#     is restarted, and result.json says why
#
# It needs no root, no systemd and no network. `zoomies updates helper run`
# refuses any uid but 0, keeps root's state at a fixed path under /var/lib and
# wants every folder above the binary to be root's, so the one step that is the
# helper runs from a Go test that stands where the command would, with the
# seams the helper's own tests use (see
# internal/installer/updatehelper_check_test.go). Everything else, the engine
# included, is the binary built from this tree.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
go=${GO:-go}
goos=$("$go" env GOOS)
goarch=$("$go" env GOARCH)
asset="zoomies_${goos}_${goarch}"
older=1.0.0
newer=1.0.1
tag="v$newer"
# The account the update folder is recorded as belonging to. It is the uid the
# Go side's seam says owns the folder and the request, and is not any real
# account: the check never needs one.
service_uid=65532

# The helper refuses a folder above the binary, the pointer or its own state
# that its group could write. A umask of 002, common for a desktop account,
# would make every folder here one it refuses, and the check would fail on
# that rather than on anything about an update.
umask 022

work=$(mktemp -d)
case_dir=$work
cleanup() { rm -rf "$work"; }
# Cleaning up from the INT and TERM traps would delete the folder and then let
# the script carry on in it; exiting runs the EXIT trap, which cleans up once.
trap cleanup EXIT
trap 'exit 130' INT TERM

fail() {
    echo "FAIL: $*" >&2
    for f in "$case_dir/helper.log" "$case_dir/systemctl.log" "$case_dir/var/update/result.json"; do
        [ -f "$f" ] || continue
        echo "--- $f ---" >&2
        tail -40 "$f" >&2
    done
    exit 1
}

# sha256sum is GNU coreutils, so this check, and `make test-upgrade`, runs on Linux.
sha_of() { sha256sum "$1" | cut -d' ' -f1; }

# field <file> <json-key>: a top-level value as written, quotes and all. A
# result is one flat document, and jq is a dependency a check meant to run on a
# bare machine does better without.
field() {
    tr ',' '\n' < "$1" | sed -n "s/^[{]*\"$2\":\\(.*\\)$/\\1/p" | sed 's/}$//' | head -1
}

# Two releases, stamped as published ones are: the helper acts only for a host
# that runs a release, and only on a tag newer than it.
echo "-> building $older and $newer from this tree"
for v in "$older" "$newer"; do
    (cd "$root" && CGO_ENABLED=0 "$go" build -trimpath \
        -ldflags "-s -w -X github.com/eyupio/zoomies/internal/version.Version=$v" \
        -o "$work/zoomies-$v" ./cmd/zoomies)
done
old_sha=$(sha_of "$work/zoomies-$older")
new_sha=$(sha_of "$work/zoomies-$newer")
# What a tampered or truncated download hashes to: neither release.
printf 'not the release\n' > "$work/other"
other_sha=$(sha_of "$work/other")

# lay_out <case> <checksum>: one host with the helper installed and a request
# waiting, and a release host serving $newer with <checksum> for it. Each case
# has a host of its own, because the helper allows one attempt in ten minutes
# and a second case on the first one's state would be refused for that.
lay_out() {
    case_dir="$work/$1"
    mkdir -p "$case_dir/opt/zoomies" "$case_dir/etc" "$case_dir/var/update" \
        "$case_dir/var/lib/zoomies-update" "$case_dir/state" "$case_dir/fakebin" \
        "$case_dir/releases/download/$tag"
    chmod 750 "$case_dir/var/update"
    chmod 700 "$case_dir/var/lib/zoomies-update"
    binary="$case_dir/opt/zoomies/zoomies"
    cp "$work/zoomies-$older" "$binary"
    chmod 755 "$binary"

    cp "$work/zoomies-$newer" "$case_dir/releases/download/$tag/$asset"
    printf '%s  %s\n' "$2" "$asset" > "$case_dir/releases/download/$tag/checksums.txt"

    # A native agent, as zoomies init leaves one: the record the engine reads
    # to know what it is upgrading, and an agent that needs no Docker.
    printf '{"deployment":"native","mode":"agent"}\n' > "$case_dir/etc/deployment.json"
    printf 'agent:\n  backend: process\n' > "$case_dir/etc/zoomies.yaml"

    # Root's copy of the pointer, which is where the helper takes the folder,
    # the binary and the account from, and never from the request.
    printf '{"v":1,"dir":"%s","binary":"%s","account":"zoomies","uid":%s,"config_dir":"%s"}\n' \
        "$case_dir/var/update" "$binary" "$service_uid" "$case_dir/etc" \
        > "$case_dir/var/lib/zoomies-update/update-helper.json"
    chmod 600 "$case_dir/var/lib/zoomies-update/update-helper.json"

    printf '{"v":1,"id":"upd_helpercheck%s","tag":"%s","requested_by":"user:helper-check","requested_at":"2026-10-10T09:00:00Z"}\n' \
        "$1" "$tag" > "$case_dir/var/update/request.json"

    # systemctl as the engine sees it on a host where the agent's unit runs
    # the installed binary. Every call is written down, one line of argv each.
    cat > "$case_dir/fakebin/systemctl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$case_dir/systemctl.log"
case "\$*" in
"show --property=LoadState --value zoomies-agent") echo loaded ;;
"show --property=ExecStart --value zoomies-agent") echo "{ path=$binary ; argv[]=$binary agent ; ignore_errors=no }" ;;
"show "*) echo inactive ;;
esac
exit 0
EOF
    chmod 755 "$case_dir/fakebin/systemctl"
    : > "$case_dir/systemctl.log"
}

# answer: run the helper once, as root's path unit would on the request.
answer() {
    if ! (cd "$root" &&
        PATH="$case_dir/fakebin:$PATH" \
        ZOOMIES_STATE_DIR="$case_dir/state" \
        HELPER_CHECK_STATE_DIR="$case_dir/var/lib/zoomies-update" \
        HELPER_CHECK_TOP="$case_dir" \
        HELPER_CHECK_RELEASES="$case_dir/releases" \
        "$go" test -count=1 -v -tags helpercheck \
            -run '^TestTheHelperAnswersTheRequestTheCheckLaidOut$' ./internal/installer/ \
            > "$case_dir/helper.log" 2>&1); then
        fail "the helper did not answer the request"
    fi
    [ -f "$case_dir/var/update/result.json" ] ||
        fail "the helper finished without writing result.json"
    [ ! -e "$case_dir/var/update/request.json" ] ||
        fail "the helper left request.json in the folder, so its path unit would fire on it again"
}

echo "-> a request for $tag, with the release's checksum"
lay_out update "$new_sha"
answer
result="$case_dir/var/update/result.json"
[ "$(sha_of "$binary")" = "$new_sha" ] ||
    fail "the installed binary is not $tag after the helper answered ok"
[ -f "$binary.previous" ] ||
    fail "there is no zoomies.previous beside the binary, so there is nothing to roll back to"
[ "$(sha_of "$binary.previous")" = "$old_sha" ] ||
    fail "zoomies.previous is not the $older binary it replaced"
grep -qx 'restart zoomies-agent' "$case_dir/systemctl.log" ||
    fail "systemctl was never asked to restart zoomies-agent, so the old binary would still be the one running"
[ "$(field "$result" ok)" = true ] || fail "result.json does not say ok"
[ "$(field "$result" tag)" = "\"$tag\"" ] || fail "result.json answers for $(field "$result" tag), not $tag"
[ "$(field "$result" to)" = "\"$newer\"" ] || fail "result.json says the host runs $(field "$result" to) now, not $newer"
echo "   ok $older replaced by $newer, $older kept as zoomies.previous, zoomies-agent restarted, result ok for $tag"

echo "-> a request for $tag, with checksums.txt naming other bytes"
lay_out mismatch "$other_sha"
answer
result="$case_dir/var/update/result.json"
[ "$(sha_of "$binary")" = "$old_sha" ] ||
    fail "the installed binary changed although the download did not match checksums.txt"
[ ! -e "$binary.previous" ] ||
    fail "a zoomies.previous was made although nothing should have been replaced"
! grep -q '^restart ' "$case_dir/systemctl.log" ||
    fail "systemctl was asked to restart a unit although nothing was replaced"
[ "$(field "$result" ok)" = false ] || fail "result.json says ok for a download that did not match checksums.txt"
grep -q 'checksum mismatch' "$result" ||
    fail "result.json does not say the checksum did not match: $(field "$result" error)"
echo "   ok nothing replaced or restarted, result says the checksum did not match"

echo "PASS: the update helper ran zoomies upgrade for $tag, which replaced the binary, kept the old one and restarted the unit, and refused a download checksums.txt did not name"
