#!/bin/sh
# Exercise ghcr-prune-tags.sh against a fake registry and a fake `gh`: old
# per-commit tags go, but a release tag, a moving tag, the newest commits and
# anything a kept version refers to stay. Needs python3, jq and curl.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'kill "$server" 2>/dev/null || true; rm -rf "$work"' EXIT

d() { printf 'sha256:%064d' "$1"; }
age() { date -u -d "-$1 days" +%Y-%m-%dT%H:%M:%SZ; }

mkdir -p "$work/m"
: > "$work/lines"
# row ID TAGS-JSON AGE-DAYS
row() {
  printf '{"id":%s,"name":"%s","updated":"%s","tags":%s}\n' "$1" "$(d "$1")" "$(age "$3")" "$2" >> "$work/lines"
  echo '{}' > "$work/m/$(d "$1")"
}
# A release that is also a commit's tag, a moving tag, and a young commit.
row 1 '["v1.0.0","sha-aaaaaaa"]' 90
row 2 '["dev"]' 90
row 3 '["ubuntu-2404-sha-bbbbbbb","sha-bbbbbbb"]' 5
# Twenty-five old commits, oldest last: ids 100+2i and 101+2i carry sha-<i> and
# ubuntu-2404-sha-<i>. The young commit above takes one of the twenty slots,
# so commits 1 to 19 stay and 20 to 25 go.
i=1
while [ "$i" -le 25 ]; do
  c=$(printf '%07x' "$i")
  row $((100 + 2 * i)) "[\"sha-$c\"]" $((40 + i))
  row $((101 + 2 * i)) "[\"ubuntu-2404-sha-$c\"]" $((40 + i))
  i=$((i + 1))
done
# The release index refers to commit 25's first version, so that one stays.
printf '{"manifests":[{"digest":"%s"}]}' "$(d 150)" > "$work/m/$(d 1)"

cat > "$work/server.py" <<'PY'
import http.server, os, sys
root = sys.argv[1]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if os.path.exists(os.path.join(root, "fail")):
            self.send_response(500); self.end_headers(); return
        p = os.path.join(root, "m", self.path.rsplit("/", 1)[-1])
        if not os.path.exists(p):
            self.send_response(404); self.end_headers(); return
        self.send_response(200); self.end_headers(); self.wfile.write(open(p, "rb").read())
    def log_message(self, *a): pass
s = http.server.HTTPServer(("127.0.0.1", 0), H)
open(os.path.join(root, "port"), "w").write(str(s.server_port))
s.serve_forever()
PY
python3 "$work/server.py" "$work" &
server=$!
while [ ! -s "$work/port" ]; do sleep 0.1; done

mkdir -p "$work/bin"
cat > "$work/bin/gh" <<SH
#!/bin/sh
case "\$*" in
  *DELETE*) echo "\$*" >> "$work/deleted" ;;
  */actions/workflows/*) echo 0 ;;
  *) cat "$work/lines" ;;
esac
SH
chmod +x "$work/bin/gh"

run() {
  : > "$work/deleted"
  GITHUB_REPOSITORY=eyupio/zoomies PATH="$work/bin:$PATH" GHCR_REGISTRY="http://127.0.0.1:$(cat "$work/port")" GHCR_PULL_TOKEN=x \
    DRY_RUN=${1:-false} TAG_KEEP_DAYS=30 TAG_KEEP_COMMITS=20 "$here/ghcr-prune-tags.sh" zoomies > "$work/out" 2>&1
}

run
got=$(sed 's|.*/versions/||' "$work/deleted" | sort -n | tr '\n' ' ')
want="140 141 142 143 144 145 146 147 148 149 151 "
if [ "$got" != "$want" ]; then
  cat "$work/out"
  echo "FAIL: deleted [$got], want [$want]"
  exit 1
fi

run true
if [ -s "$work/deleted" ]; then
  echo "FAIL: a dry run deleted $(cat "$work/deleted")"
  exit 1
fi

touch "$work/fail"
if run; then
  echo "FAIL: the prune succeeded while the registry was failing"
  exit 1
fi
if [ -s "$work/deleted" ]; then
  echo "FAIL: deleted $(cat "$work/deleted") while the registry was failing"
  exit 1
fi
echo "ghcr-prune-tags: ok"
