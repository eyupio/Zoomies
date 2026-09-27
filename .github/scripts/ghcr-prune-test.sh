#!/bin/sh
# Exercise ghcr-prune.sh against a fake registry and a fake `gh`, so the rule
# that broke v1.3.2 -- never delete what a kept tag still needs -- is checked
# without credentials or a live package. Run it from anywhere; needs python3,
# jq and curl. Exits non-zero on the first wrong answer.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'kill "$server" 2>/dev/null || true; rm -rf "$work"' EXIT

old=2020-01-01T00:00:00Z
new=$(date -u +%Y-%m-%dT%H:%M:%SZ)
d() { printf 'sha256:%064d' "$1"; }

# The package: a tagged index over two platforms; an attestation whose
# subject is one of those platforms, untagged and old -- the case the first
# version of this script got wrong; an old unreferenced image with its own
# attestation (both should go); and a young orphan (a push in flight).
mkdir -p "$work/m"
index=$(d 1) amd=$(d 2) arm=$(d 3) att=$(d 4) stale=$(d 5) staleatt=$(d 6) young=$(d 7)
printf '{"manifests":[{"digest":"%s"},{"digest":"%s"}]}' "$amd" "$arm" > "$work/m/$index"
echo '{}' > "$work/m/$amd"
echo '{}' > "$work/m/$arm"
printf '{"subject":{"digest":"%s"}}' "$amd" > "$work/m/$att"
echo '{}' > "$work/m/$stale"
printf '{"subject":{"digest":"%s"}}' "$stale" > "$work/m/$staleatt"
echo '{}' > "$work/m/$young"
printf '1\t%s\t1\t%s\n2\t%s\t0\t%s\n3\t%s\t0\t%s\n4\t%s\t0\t%s\n5\t%s\t0\t%s\n6\t%s\t0\t%s\n7\t%s\t0\t%s\n' \
  "$index" "$old" "$amd" "$old" "$arm" "$old" "$att" "$old" "$stale" "$old" "$staleatt" "$old" "$young" "$new" \
  > "$work/versions"

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
  *) cat "$work/versions" ;;
esac
SH
chmod +x "$work/bin/gh"

run() {
  : > "$work/deleted"
  PATH="$work/bin:$PATH" GHCR_REGISTRY="http://127.0.0.1:$(cat "$work/port")" GHCR_PULL_TOKEN=x \
    DRY_RUN=false KEEP_DAYS=14 "$here/ghcr-prune.sh" zoomies > "$work/out" 2>&1
}

run
got=$(sed 's|.*/versions/||' "$work/deleted" | sort | tr '\n' ' ')
if [ "$got" != "5 6 " ]; then
  cat "$work/out"
  echo "FAIL: deleted versions [$got], want [5 6 ] -- the platforms, their attestation and the young orphan must stay"
  exit 1
fi

# A registry that cannot answer must leave everything in place.
touch "$work/fail"
if run; then
  echo "FAIL: the prune succeeded while the registry was failing"
  exit 1
fi
if [ -s "$work/deleted" ]; then
  echo "FAIL: deleted $(cat "$work/deleted") while the registry was failing"
  exit 1
fi
echo "ghcr-prune: ok"
