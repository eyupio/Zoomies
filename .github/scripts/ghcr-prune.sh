#!/bin/sh
# Delete untagged GHCR package versions that nothing still refers to.
#
# usage: ghcr-prune.sh PACKAGE
# env:   GH_TOKEN (packages:write, actions:read), KEEP_DAYS (default 14),
#        DRY_RUN (default true), GITHUB_REPOSITORY, PUBLISHERS (workflow files
#        that push images, default "ci.yml release.yml"), PUBLISH_WAIT_TRIES
#        (default 10) and PUBLISH_WAIT_SECONDS (default 60)
#
# Why not actions/delete-package-versions: it treats every untagged version as
# garbage, but the platform and attestation manifests under a multi-platform
# tag are untagged too. Deleting them left v1.3.2 and :latest resolving to an
# index whose children were gone, and every `docker pull` failed with
# "manifest unknown". Here a version is deleted only when it is untagged,
# older than KEEP_DAYS, not referenced -- directly or through a nested index
# -- by any tagged version or any version being kept, and not a referrer (an
# attestation) whose subject is being kept.
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

# A publish in flight pushes an index before it tags it, and may re-use an
# old untagged digest as a child. The keep set is a snapshot, so rather than
# race a push, a real run waits for image-publishing workflows to finish and
# gives up -- deleting nothing -- if they do not.
if [ "$dry_run" != "true" ]; then
  tries=${PUBLISH_WAIT_TRIES:-10}
  while :; do
    busy=0
    for wf in ${PUBLISHERS:-ci.yml release.yml}; do
      for status in queued in_progress; do
        n=$(gh api "/repos/$GITHUB_REPOSITORY/actions/workflows/$wf/runs?status=$status&per_page=1" --jq '.total_count')
        busy=$((busy + n))
      done
    done
    [ "$busy" -eq 0 ] && break
    tries=$((tries - 1))
    if [ "$tries" -le 0 ]; then
      echo "::warning::$pkg: image-publishing workflows are still running; deleting nothing this time"
      exit 0
    fi
    echo "$pkg: $busy image-publishing run(s) in progress; waiting"
    sleep "${PUBLISH_WAIT_SECONDS:-60}"
  done
fi

list_versions() {
  gh api --paginate "/orgs/$owner/packages/container/$pkg/versions?per_page=100" \
    --jq '.[] | [.id, .name, (.metadata.container.tags | length), .updated_at] | @tsv'
}
list_versions > "$work/versions"

cutoff=$(date -u -d "-$keep_days days" +%Y-%m-%dT%H:%M:%SZ)

# The roots: every tagged version, and every untagged one still inside the
# grace period (a push in flight has not tagged its index yet).
roots() { awk -F'\t' -v c="$cutoff" '$3 > 0 || $4 >= c { print $2 }' "$1" | sort -u; }
roots "$work/versions" > "$work/roots"

accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'
registry=${GHCR_REGISTRY:-https://ghcr.io}
token=${GHCR_PULL_TOKEN:-$(curl -fsS "$registry/token?scope=repository:$owner/$pkg:pull" | jq -r .token)}

# fetch DIGEST: the manifest into $work/body. Returns 0 when read, 1 when the
# registry says it is gone, and exits the script on anything else -- a keep
# set built on a failed read is a guess, and guessing is how the original
# breakage happened.
fetch() {
  code=$(curl -sS -o "$work/body" -w '%{http_code}' -H "Authorization: Bearer $token" \
    -H "Accept: $accept" "$registry/v2/$owner/$pkg/manifests/$1") || code=000
  case "$code" in
    200) return 0 ;;
    404) return 1 ;;
    *) echo "::error::reading $pkg@$1 returned $code; deleting nothing"; exit 1 ;;
  esac
}

# close: add everything the digests in $work/todo refer to -- index children
# and the subject a referrer names -- to $work/keep, transitively.
close() {
  while [ -s "$work/todo" ]; do
    : > "$work/next"
    while read -r digest; do
      if ! fetch "$digest"; then
        # Already gone: a child an earlier prune took. Nothing under it is
        # left to protect, and ghcr-check-tags.sh is what reports the tag.
        echo "::warning::$pkg@$digest is referenced but missing"
        continue
      fi
      jq -r '.manifests[]?.digest, .subject?.digest // empty' "$work/body" >> "$work/next"
    done < "$work/todo"
    sort -u "$work/next" | comm -23 - "$work/keep" > "$work/todo"
    sort -u "$work/keep" "$work/todo" -o "$work/keep"
  done
}

cp "$work/roots" "$work/keep"
cp "$work/roots" "$work/todo"
close

# Referrers point the other way: an attestation names the image it describes
# as its subject, and the image does not list it. GHCR has no referrers API,
# so read every remaining candidate and keep any whose subject is kept --
# repeating, because keeping one can pull in children that are subjects too.
while :; do
  awk -F'\t' '$3 == 0 { print $2 }' "$work/versions" | sort -u | comm -23 - "$work/keep" > "$work/candidates"
  : > "$work/todo"
  while read -r digest; do
    fetch "$digest" || continue
    subject=$(jq -r '.subject?.digest // empty' "$work/body")
    if [ -n "$subject" ] && grep -qxF "$subject" "$work/keep"; then
      echo "$digest" >> "$work/todo"
    fi
  done < "$work/candidates"
  [ -s "$work/todo" ] || break
  sort -u "$work/keep" "$work/todo" -o "$work/keep"
  close
done

# still_unused DIGEST: re-read the package immediately before a delete. A
# version tagged, or pushed, since the snapshot is a new root; walk what it
# refers to into the keep set, then refuse the candidate if it is now kept,
# tagged, or an attestation of something kept. Manifests are immutable, so
# only new roots can add references -- this is the whole graph, brought up to
# date, without re-reading every manifest per candidate.
still_unused() {
  list_versions > "$work/now"
  roots "$work/now" | comm -23 - "$work/keep" > "$work/todo"
  sort -u "$work/keep" "$work/todo" -o "$work/keep"
  close
  grep -qxF "$1" "$work/keep" && return 1
  awk -F'\t' -v d="$1" '$2 == d && $3 == 0 { found = 1 } END { exit !found }' "$work/now" || return 1
  fetch "$1" || return 1
  subject=$(jq -r '.subject?.digest // empty' "$work/body")
  if [ -n "$subject" ] && grep -qxF "$subject" "$work/keep"; then
    return 1
  fi
  return 0
}

deleted=0
while IFS="$(printf '\t')" read -r id name tags updated; do
  [ "$tags" -eq 0 ] || continue
  grep -qxF "$name" "$work/keep" && continue
  if [ "$dry_run" = "true" ]; then
    echo "would delete $pkg@$name (version $id, updated $updated)"
  elif ! still_unused "$name"; then
    echo "kept $pkg@$name: referenced or tagged since the snapshot"
    continue
  else
    gh api -X DELETE "/orgs/$owner/packages/container/$pkg/versions/$id" >/dev/null
    echo "deleted $pkg@$name (version $id, updated $updated)"
  fi
  deleted=$((deleted + 1))
done < "$work/versions"
echo "$pkg: $(wc -l < "$work/keep") versions kept, $deleted selected for deletion (dry run: $dry_run)"
