#!/bin/sh
# Delete untagged GHCR package versions that nothing still refers to.
#
# usage: ghcr-prune.sh PACKAGE
# env:   GH_TOKEN (packages:write), KEEP_DAYS (default 14), DRY_RUN (default true)
#
# Why not actions/delete-package-versions: it treats every untagged version as
# garbage, but the platform and attestation manifests under a multi-platform
# tag are untagged too. Deleting them left v1.3.2 and :latest resolving to an
# index whose children were gone, and every `docker pull` failed with
# "manifest unknown". Here a version is deleted only when it is untagged,
# older than KEEP_DAYS, and not referenced -- directly or through a nested
# index -- by any tagged version or any version being kept.
#
# It fails closed: if any manifest that decides the keep set cannot be read
# for a reason other than already being gone, nothing is deleted.
set -eu

owner=${GHCR_OWNER:-eyupio}
pkg=$1
keep_days=${KEEP_DAYS:-14}
dry_run=${DRY_RUN:-true}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

gh api --paginate "/orgs/$owner/packages/container/$pkg/versions?per_page=100" \
  --jq '.[] | [.id, .name, (.metadata.container.tags | length), .updated_at] | @tsv' \
  > "$work/versions"

cutoff=$(date -u -d "-$keep_days days" +%Y-%m-%dT%H:%M:%SZ)

# The roots: every tagged version, and every untagged one still inside the
# grace period (a push in flight has not tagged its index yet).
awk -F'\t' -v c="$cutoff" '$3 > 0 || $4 >= c { print $2 }' "$work/versions" | sort -u > "$work/roots"

accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'
token=$(curl -fsS "https://ghcr.io/token?scope=repository:$owner/$pkg:pull" | jq -r .token)

# Close the root set over manifest references: index children, and the
# subject an attestation or referrer manifest names.
cp "$work/roots" "$work/keep"
cp "$work/roots" "$work/todo"
while [ -s "$work/todo" ]; do
  : > "$work/next"
  while read -r digest; do
    code=$(curl -sS -o "$work/body" -w '%{http_code}' -H "Authorization: Bearer $token" \
      -H "Accept: $accept" "https://ghcr.io/v2/$owner/$pkg/manifests/$digest") || code=000
    case "$code" in
      200) ;;
      # Already gone: a child an earlier prune took. There is nothing under
      # it left to protect, and ghcr-check-tags.sh is what reports the tag.
      404) echo "::warning::$pkg@$digest is referenced but missing"; continue ;;
      # Anything else means the keep set is not known; deleting on a guess
      # is how the original breakage happened.
      *) echo "::error::reading $pkg@$digest returned $code; deleting nothing"; exit 1 ;;
    esac
    body=$(cat "$work/body")
    printf '%s' "$body" | jq -r '.manifests[]?.digest, .subject?.digest // empty' >> "$work/next"
  done < "$work/todo"
  sort -u "$work/next" | comm -23 - "$work/keep" > "$work/todo"
  sort -u "$work/keep" "$work/todo" -o "$work/keep"
done

deleted=0
while IFS="$(printf '\t')" read -r id name tags updated; do
  [ "$tags" -eq 0 ] || continue
  grep -qxF "$name" "$work/keep" && continue
  if [ "$dry_run" = "true" ]; then
    echo "would delete $pkg@$name (version $id, updated $updated)"
  else
    gh api -X DELETE "/orgs/$owner/packages/container/$pkg/versions/$id" >/dev/null
    echo "deleted $pkg@$name (version $id, updated $updated)"
  fi
  deleted=$((deleted + 1))
done < "$work/versions"
echo "$pkg: $(wc -l < "$work/keep") versions kept, $deleted selected for deletion (dry run: $dry_run)"
