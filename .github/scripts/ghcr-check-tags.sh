#!/bin/sh
# Check that every manifest a GHCR tag points at can still be pulled.
#
# usage: ghcr-check-tags.sh PACKAGE [TAG...]
#
# With no TAG, every tag in the package is checked. A multi-platform tag is an
# index whose platform and attestation manifests are untagged package
# versions; deleting one leaves the tag resolving and `docker pull` failing
# with "manifest unknown". This walks the index the way a client does, so a
# missing child is found here rather than by a user.
#
# Prints "PACKAGE:TAG ok" or "PACKAGE:TAG BROKEN <missing digests>" per tag and
# exits 1 if any tag is broken or missing. Anonymous pull only: the packages
# are public, and a check that needs a secret is a check a fork cannot run.
set -eu

owner=${GHCR_OWNER:-eyupio}
pkg=$1
shift

accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'
token=$(curl -fsS "https://ghcr.io/token?scope=repository:$owner/$pkg:pull" | jq -r .token)

get() {
  curl -fsS -H "Authorization: Bearer $token" -H "Accept: $accept" \
    "https://ghcr.io/v2/$owner/$pkg/manifests/$1"
}

if [ "$#" -eq 0 ]; then
  # Attestation tags (sha256-...) are checked through the tags they describe.
  # Word splitting is the point: one tag per argument.
  # shellcheck disable=SC2046
  set -- $(curl -fsS -H "Authorization: Bearer $token" \
    "https://ghcr.io/v2/$owner/$pkg/tags/list?n=10000" | jq -r '.tags[]' | grep -v '^sha256-' || true)
fi

status=0
for tag in "$@"; do
  if ! body=$(get "$tag"); then
    echo "$pkg:$tag MISSING"
    status=1
    continue
  fi
  missing=""
  for digest in $(printf '%s' "$body" | jq -r '.manifests[]?.digest'); do
    get "$digest" >/dev/null 2>&1 || missing="$missing $digest"
  done
  if [ -n "$missing" ]; then
    echo "$pkg:$tag BROKEN$missing"
    status=1
  else
    echo "$pkg:$tag ok"
  fi
done
exit "$status"
