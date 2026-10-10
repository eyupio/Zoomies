#!/bin/sh
# Exercise the real retry helper with a fake package manager: no root/network.
set -eu
root="$(CDPATH='' cd -- "$(dirname "$0")/../.." && pwd)"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT HUP INT TERM
export DNF_TEST_DIR="$scratch"
mkdir "$scratch/bin"
cat > "$scratch/bin/dnf" <<'EOF'
#!/bin/sh
set -eu
count=0
[ ! -f "$DNF_TEST_DIR/count" ] || count=$(cat "$DNF_TEST_DIR/count")
count=$((count + 1))
echo "$count" > "$DNF_TEST_DIR/count"
printf '%s\n' "$@" > "$DNF_TEST_DIR/args-$count"
case "$DNF_TEST_CASE" in
  success) exit 0 ;;
  refresh) [ "$1" = --refresh ] ;;
  skew) [ "$1" = --refresh ] && [ "$2" = --nobest ] ;;
  toolchain)
    if grep -qx upgrade "$DNF_TEST_DIR/args-$count"; then
      grep -q -- --refresh "$DNF_TEST_DIR/args-$count" && exit 0
      exit 1
    fi
    exit 0
    ;;
  missing) exit 1 ;;
  *) exit 99 ;;
esac
EOF
cat > "$scratch/bin/sleep" <<'EOF'
#!/bin/sh
echo "$1" >> "$DNF_TEST_DIR/delays"
EOF
REAL_GREP=$(command -v grep)
export REAL_GREP
cat > "$scratch/bin/grep" <<'EOF'
#!/bin/sh
if [ "$1" = -q ] && [ "$2" = '^ID=fedora' ]; then exit 1; fi
exec "$REAL_GREP" "$@"
EOF
cat > "$scratch/bin/git" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$scratch/bin/dnf" "$scratch/bin/sleep" "$scratch/bin/grep" "$scratch/bin/git"
export PATH="$scratch/bin:$PATH"

for scenario in success refresh skew missing; do
  export DNF_TEST_CASE="$scenario"
  rm -f "$scratch/count" "$scratch/delays" "$scratch"/args-*
  result=0
  sh "$root/deploy/runner-dnf.sh" --allowerasing --setopt=install_weak_deps=False \
    libxml2-devel 'dnf-command(config-manager)' > "$scratch/output" 2>&1 || result=$?
  case "$scenario" in
    success) attempts=1; expected_status=0 ;;
    refresh) attempts=2; expected_status=0 ;;
    skew) attempts=3; expected_status=0 ;;
    missing) attempts=3; expected_status=1 ;;
  esac
  [ "$result" -eq "$expected_status" ]
  [ "$(cat "$scratch/count")" -eq "$attempts" ]
  i=1
  while [ "$i" -le "$attempts" ]; do
    {
      [ "$i" -lt 2 ] || echo --refresh
      [ "$i" -lt 3 ] || echo --nobest
      printf '%s\n' install -y --allowerasing --setopt=install_weak_deps=False \
        libxml2-devel 'dnf-command(config-manager)'
    } > "$scratch/expected"
    # Exact argv checks prove required packages/options survive every retry,
    # with no --skip-broken, signature bypass, or argument splitting.
    diff -u "$scratch/expected" "$scratch/args-$i"
    i=$((i + 1))
  done
  case "$attempts" in
    1) [ ! -f "$scratch/delays" ] ;;
    2) [ "$(cat "$scratch/delays")" = 10 ] ;;
    3) printf '10\n20\n' > "$scratch/expected"; diff -u "$scratch/expected" "$scratch/delays" ;;
  esac
  echo "ok: $scenario"
done

rm -f "$scratch/count"
result=0
sh "$root/deploy/runner-dnf.sh" > "$scratch/output" 2>&1 || result=$?
[ "$result" -eq 64 ]
[ ! -f "$scratch/count" ]
echo 'ok: empty package list rejected before invoking dnf'

rm -f "$scratch/count" "$scratch/delays" "$scratch"/args-*
export DNF_TEST_CASE=toolchain
sh "$root/deploy/runner-toolchain.sh" dnf > "$scratch/output" 2>&1
upgrade_attempts=0
for args in "$scratch"/args-*; do
  if grep -qx upgrade "$args"; then
    upgrade_attempts=$((upgrade_attempts + 1))
    if [ "$upgrade_attempts" -eq 1 ]; then
      [ "$(sed -n '1p' "$args")" = upgrade ]
    else
      [ "$(sed -n '1p' "$args")" = --refresh ]
      [ "$(sed -n '2p' "$args")" = upgrade ]
    fi
  fi
done
[ "$upgrade_attempts" -eq 2 ]
found_compilers=false
for args in "$scratch"/args-*; do
  if grep -qx gcc "$args"; then
    grep -qx gcc-c++ "$args"
    grep -qx make "$args"
    found_compilers=true
  fi
done
[ "$found_compilers" = true ]
echo 'ok: RPM base upgrade retries before compiler package installation'
