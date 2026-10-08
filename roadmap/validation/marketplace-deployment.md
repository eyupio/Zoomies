# One-click deployment verification

**Status: not run. Nothing on this page is evidence yet.**

This is the record ZF-219's acceptance asks for, written before the runs rather
than after, so that the procedure is fixed in advance and a later result cannot
turn out to be a description of whatever happened to work. Every row below is
`not run` until somebody with a disposable VPS, a DNS name and a GitHub
organisation fills it in.

What *has* been run is the rendering, in `internal/docs`: the real
`bootstrap.sh` is executed far enough to produce the answer file a boot would
write, and that file is parsed and put through the installer's own validator;
each of the three certificate arrangements is checked for a listener and a
certificate holder that agree; the rendered cloud-config is checked for every
file the instance will look for; and the image lock is checked against the
runner catalogue. That proves the artefact is internally consistent. It proves
nothing about an instance.

**A rendered artefact is not a deployment.** The failure this record exists to
catch is the one where everything parses and the instance still comes up
without a controller.

## What this needs before it can be run

| | |
| --- | --- |
| An instance | Ubuntu 24.04 LTS, **pristine**, from a provider's own image rather than one prepared by hand |
| A DNS name | a real record, pointed at that instance, on a zone that can be changed |
| Reachable ports | 80 and 443 from the internet, for the ACME arrangement |
| A certificate | for the `files` arrangement, from the provider or anywhere else |
| A GitHub installation | able to queue real jobs against a test repository |
| The published release | the images and installer `deploy/marketplace/release.env` pins, as published, not as built locally |

## What has to be recorded

Each run records the exact release tag and image digests it deployed, the
instance size, the provider, the DNS and TLS route taken, and the wall-clock
time of each step. A run against a locally built binary is not one of these.

## Automated checks

| Check | Status |
| --- | --- |
| The rendered cloud-config is accepted by `cloud-init schema --annotate` | not run |
| Boot completes with `cloud-init status --wait` reporting done, not degraded | not run |
| No secret appears in the instance metadata, `/var/log/cloud-init-output.log`, or the controller's log | not run |
| `/healthz` answers, and `/readyz` answers, over the public URL | not run |
| The controller survives a reboot of the instance with its database intact | not run |
| An upgrade to a later release keeps the database, and a rollback is refused with the documented message | not run |

## The journey

| Step | Status |
| --- | --- |
| The setup token is readable where the first-login notes say it is | not run |
| The first administrator is created in the browser with that token, and a wrong token is refused | not run |
| A GitHub App is created and installed, and a webhook delivery arrives | not run |
| A pool is created, a workflow is pointed at it, and one job runs green | not run |
| The runner container is gone after the job, and no runner is left registered on GitHub | not run |
| Ten minutes or less from booted instance to green job, measured | not run |
| A backup taken per `docs/backup-and-restore.md` restores onto a second instance | not run |
| `zoomies uninstall --yes --volumes` leaves nothing behind, and the proxy project is removed with it | not run |

## Certificate arrangements

Each has to be run: they differ in who is exposed, which is the part that
cannot be inferred from one of them working.

| Arrangement | Status |
| --- | --- |
| `acme`, with DNS ready before boot | not run |
| `acme`, with DNS created after boot; the proxy is expected to recover on its own | not run |
| `files`, with a certificate supplied | not run |
| `off`, behind a provider's load balancer, with trusted proxies set | not run |

## The independent reading

The last row, and the one no automated check replaces: somebody who did not
write any of this follows [docs/marketplace.md](../../docs/marketplace.md) from
a booted instance to a green job, without asking the author a question and
without editing a file by hand.

| | Status |
| --- | --- |
| An independent operator completes the documented path | not run |

Only after all of the above does the friendly-provider pilot review happen.
Until then Zoomies is not submitted to an official marketplace, no broad
provider support is advertised, and no second provider is begun.
