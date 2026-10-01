---
title: Recovering a GHCR image that fails with manifest unknown
description: >-
  Diagnose missing child manifests in multi-platform Zoomies images on GHCR,
  check affected tags and republish them without repeating unsafe cleanup.
---

# Recovering a published image that no longer pulls

A multi-platform tag on GHCR is an index. The `linux/amd64` and `linux/arm64`
manifests it lists, and the attestation manifests beside them, are separate
package versions with no tag of their own. If one of them is deleted, the tag
still resolves — `docker manifest inspect` answers — but `docker pull` fails
with `manifest unknown`.

That is what the daily `cleanup-packages.yml` run did while it used
`actions/delete-package-versions` with "untagged only, keep the newest ten":
it treated the children of every older multi-platform tag as garbage.
`cleanup-packages.yml` now runs `.github/scripts/ghcr-prune.sh`, which deletes
an untagged version only when no tagged or recent version refers to it, and
has no schedule until a dry run has been reviewed.

## Finding broken tags

`image-health.yml` checks `latest`, `dev` and the newest release of every
package each day and after each release. To check by hand, with no
credentials:

```sh
# Named tags
.github/scripts/ghcr-check-tags.sh zoomies latest dev v1.3.2
# Every tag in a package
.github/scripts/ghcr-check-tags.sh zoomies-runner
```

Each line is `package:tag ok` or `package:tag BROKEN` followed by the missing
digests; the script exits non-zero if anything is broken.

## Republishing

The missing manifests cannot be restored: the only fix is to push the tag
again.

* **`dev` and `main`** are pushed by every merge to `main`. Merge anything, or
  re-run the latest `ci.yml` run on `main`.
* **A release tag and `latest`.** `release.yml` can be dispatched with a
  `tag`, but it deliberately refuses a tag whose release is already published
  as a full release, because the same dispatch would replace the binaries
  people have downloaded. So a published release is not rebuilt in place.
  Cut the next patch release instead: tag `main` as `vX.Y.Z+1` and publish the
  release. That pushes every image family under the new tag and moves
  `latest` to it.
* **A prerelease or draft** can be rebuilt in place:

  ```sh
  gh workflow run release.yml --ref main -f tag=v1.4.0-rc1
  ```

* **Older release tags** stay broken until someone republishes them the same
  way. Point users at the newest release: an upgrade is the supported path.

After any of these, run `image-health.yml` from the Actions tab, or the
script above, and check that the tag reports `ok`.
