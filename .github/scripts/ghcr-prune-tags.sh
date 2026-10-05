#!/bin/sh
# Delete old per-commit tagged GHCR package versions.
#
# usage: ghcr-prune-tags.sh PACKAGE
# env:   GH_TOKEN (packages:write, actions:read), TAG_KEEP_DAYS (default 30),
#        TAG_KEEP_COMMITS (default 20), DRY_RUN (default true),
#        GITHUB_REPOSITORY, PUBLISHERS, PUBLISH_WAIT_TRIES, PUBLISH_WAIT_SECONDS
#
# ghcr-prune.sh only removes untagged versions, and every commit publishes a
# sha-<short> tag and one <variant>-sha-<short> tag per variant, so those
# tagged versions (and the platform and attestation manifests under them)
# piled up for ever. A version is deleted here only when
#   - every tag on it is a per-commit tag ([<variant>-]sha-<hex>); a release
#     tag, latest, dev, main or any other tag keeps it;
#   - it was last updated more than TAG_KEEP_DAYS ago;
#   - its commit is not among the TAG_KEEP_COMMITS newest commits; and
#   - no kept tagged version refers to it as a child or attestation subject.
# Deleting a version unreferences its children, so the ghcr-prune.sh run that
# follows removes them. It fails closed: a manifest that cannot be read for a
# reason other than being gone means nothing is deleted.
set -eu

owner=${GHCR_OWNER:-eyupio}
pkg=$1
keep_days=${TAG_KEEP_DAYS:-30}
keep_commits=${TAG_KEEP_COMMITS:-20}
dry_run=${DRY_RUN:-true}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Same race as ghcr-prune.sh: do not delete while an image is being published.
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

gh api --paginate "/orgs/$owner/packages/container/$pkg/versions?per_page=100" \
  --jq '.[] | {id, name, updated: .updated_at, tags: (.metadata.container.tags // [])}' | jq -s . > "$work/versions.json"

cutoff=$(date -u -d "-$keep_days days" +%Y-%m-%dT%H:%M:%SZ)

# Candidates, oldest rule first: sha-only tags, old, and outside the newest
# commits. A commit's age is the newest update of any version tagged with it.
jq -r --arg cutoff "$cutoff" --argjson n "$keep_commits" '
  def sha: test("^([a-z0-9.]+(-[a-z0-9.]+)*-)?sha-[0-9a-f]{7,40}$");
  def commit: capture("sha-(?<c>[0-9a-f]{7,40})$").c;
  (map(select(.tags | any(sha))) as $shaed
   | ($shaed | [.[] | .updated as $u | .tags[] | select(sha) | {c: commit, u: $u}]
      | group_by(.c) | map({c: .[0].c, u: (map(.u) | max)}) | sort_by(.u) | reverse
      | .[:$n] | map(.c)) as $newest
   | .[]
   | select((.tags | length) > 0 and (.tags | all(sha)) and .updated < $cutoff
            and ([.tags[] | commit] | any(. as $c | $newest | index($c)) | not))
   | [.id, .name, (.tags | join(","))] | @tsv)' "$work/versions.json" > "$work/candidates"

# Everything else tagged is kept; collect what those versions refer to.
cut -f1 "$work/candidates" | jq -R 'tonumber' | jq -s . > "$work/candidate-ids.json"
jq -r --slurpfile c "$work/candidate-ids.json" \
  '.[] | select((.tags | length) > 0 and ([.id] | inside($c[0]) | not)) | .name' "$work/versions.json" > "$work/kept"

registry=${GHCR_REGISTRY:-https://ghcr.io}
token=${GHCR_PULL_TOKEN:-$(curl -fsS "$registry/token?scope=repository:$owner/$pkg:pull" | jq -r .token)}
accept='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json'
: > "$work/referenced"
while read -r digest; do
  code=$(curl -sS -o "$work/body" -w '%{http_code}' -H "Authorization: Bearer $token" \
    -H "Accept: $accept" "$registry/v2/$owner/$pkg/manifests/$digest") || code=000
  case "$code" in
    200) jq -r '.manifests[]?.digest, .subject?.digest // empty' "$work/body" >> "$work/referenced" ;;
    404) echo "::warning::$pkg@$digest is tagged but missing" ;;
    *) echo "::error::reading $pkg@$digest returned $code; deleting nothing"; exit 1 ;;
  esac
done < "$work/kept"
sort -u "$work/referenced" -o "$work/referenced"

deleted=0 skipped=0
while IFS="$(printf '\t')" read -r id name tags; do
  if grep -qxF "$name" "$work/referenced"; then
    echo "kept $pkg@$name ($tags): a kept version refers to it"
    skipped=$((skipped + 1))
    continue
  fi
  if [ "$dry_run" = "true" ]; then
    echo "would delete $pkg@$name (version $id, tags $tags)"
  else
    gh api -X DELETE "/orgs/$owner/packages/container/$pkg/versions/$id" >/dev/null
    echo "deleted $pkg@$name (version $id, tags $tags)"
  fi
  deleted=$((deleted + 1))
done < "$work/candidates"
echo "$pkg: $deleted old per-commit version(s) selected for deletion, $skipped kept as referenced (dry run: $dry_run)"
