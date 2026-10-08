---
icon: material/backup-restore
description: >-
  The two files a Zoomies controller is made of, how to copy them safely by
  schedule, from settings or by hand, and how to restore the fleet here or
  elsewhere.
---

# Backup and restore

Zoomies keeps everything in one SQLite file and one encryption key. Back up
both, together, or you have backed up nothing useful: the database holds the
GitHub App's private key and every webhook secret **encrypted**, and the key
that decrypts them is a separate file.

## What to copy

| What | Where, by default | Why |
| --- | --- | --- |
| The database | `/var/lib/zoomies/zoomies.db` | Pools, hosts, runners, jobs, users, tokens, the audit log, and the encrypted secrets. |
| The encryption key | `/etc/zoomies/encryption.key` | Without it the GitHub App's private key and the webhook secrets in that database cannot be read. |
| `zoomies.yaml` | `/etc/zoomies/zoomies.yaml` | Not strictly needed (the defaults are safe and the rest is quick to retype) but it is the record of what you chose. |

Those are the paths for an install running as root. A non-root install puts
both directories under `~/.config/zoomies`, and macOS uses
`~/Library/Application Support/zoomies`. `ZOOMIES_STATE_DIR` and
`ZOOMIES_CONFIG_DIR` override them; `zoomies config print` tells you where they
actually are on this host.

A container deployment keeps all of it in the `zoomies-data` volume.

## Taking a backup

There are three ways, and they write the same thing: a timestamped directory,
`zoomies-20260908-181718/`, holding the database and a manifest. A copy taken
by any of them is restorable by any of the others.

* **The controller takes one itself**, on a schedule. `backup.interval` is
  nightly by default and `backup.keep` keeps the newest seven, into
  `backup.directory`; a `backups` directory beside the database unless you
  say otherwise, which on a container deployment is the mounted volume. Set
  the interval to `0` to switch the schedule off. A scheduled copy that fails
  is `backup.failed` in the problems drawer, with the error, and is retried
  every fifteen minutes.
* **Settings → Backups** takes one on demand, lists every copy in the
  directory with what its manifest says, verifies, downloads, uploads and
  deletes them, and stages a restore. It needs the administrator role, because
  a backup is the whole database. The rest of this page says what each button
  does.
* **`zoomies backup`** on the command line:

```sh
zoomies backup
```

Run it on the machine that holds the data, as a user who can read the
database; it opens the file directly rather than talking to a controller, so it
works while the controller is running and it works when the controller will not
start.

The copy is taken with SQLite's `VACUUM INTO`, which is what makes it whole.
The database runs in WAL mode, so there are two more files beside it,
`zoomies.db-wal` and `zoomies.db-shm`, and **copying `zoomies.db` alone while
the controller is running gives you a file that is missing the most recent
writes.** What `zoomies backup` produces is one checkpointed file with no WAL
beside it, already checked with `PRAGMA integrity_check`, at mode 0600.

Useful flags:

| Flag | What it does |
| --- | --- |
| `--dir` | Where to put it. Defaults to a `backups` directory beside the database. |
| `--keep N` | After writing, delete all but the newest N. It only ever removes directories this command made (the name has to match and a manifest has to be inside) so a shared backup directory is safe. |
| `--include-key` | Copy the encryption key in as well. Read the next section before you do. |

### The manifest

The manifest is the half that is not the data, and everything in it answers a
question asked during a restore and nowhere else:

* **Which build wrote this**, and the full migration ledger. A restore has to
  run that release or a later one, because the store refuses a database whose
  ledger names migrations the binary does not have.
* **The encryption key's fingerprint** and where it came from, so you can tell
  whether the key file in your hand is the one that opens this database,
  rather than finding out when the first GitHub call fails.
* **What the key is needed for**: each GitHub App installation whose private
  key or webhook secret is sealed in there. Never the secrets themselves.
* **The configuration**, through the same blanking `zoomies config print` uses.

### The key, and why it is not in there by default

A backup with the key in it decrypts itself. That is convenient and it is a
different object from the one you get by default: it is a credential, and it
has to be stored like the GitHub App's private key, because that is what it
contains.

A backup without the key is safe to keep almost anywhere and is useless on its
own for anything sealed. So keep the key too (once, wherever you keep secrets)
and let the fingerprint in each manifest tell you they belong together.
`zoomies backup` says which of the two you have taken, every time.

A key passed in `ZOOMIES_ENCRYPTION_KEY` rather than a file has nothing to
copy; `--include-key` says so rather than pretending.

## Getting a backup off the machine, and back on

A backup beside the database is a backup against a mistake, not against the
disk. Either the fleet takes the copy somewhere else itself, [backup
remotes](#copies-that-leave-the-machine), below, or you do, with `rsync`, a
snapshot of the volume, or a job of your own. Keep the key with your secrets,
once, either way.

**Download** on the Backups tab is the same thing for a browser: the backup's
directory as one `zoomies-<timestamp>.tar.gz`, holding the manifest, the
database and, only when the backup was taken with it, the key. **Download
encrypted** seals the same archive with a passphrase first, for a file that is
going to sit on a laptop or in a shared drive: the passphrase goes through
argon2id and the archive through AES-256-GCM in chunks, so a file cut short or
altered does not open as a shorter backup. Nothing on the controller remembers
the passphrase; the file opens with it and with nothing else.

**Upload** brings either kind back, on this controller or another one. The
archive is unpacked into a staging directory, verified exactly as a restore
would verify it, and then listed under the name its manifest gives it, marked
as uploaded. An upload is never counted or removed by retention: an operator who
brought a file here brought it for a reason.

**Verify** re-reads a backup that is already here (the database's digest
against the manifest, `PRAGMA integrity_check`, and whether this build can open
it) because a copy nobody has opened is a copy nobody knows about, and the
day to find the bad one is not the day it is needed.

**Prune now** applies retention without waiting for a backup. Retention
normally runs as part of taking one, which is the right moment for it and the
wrong one for somebody who has just lowered `backup.keep` from thirty to seven:
until the next backup the fleet is still holding thirty, and with
`backup.interval` off it holds thirty forever. The button runs both halves
(`backup.keep` here and each destination's own `keep` in its bucket) and says
what it deleted, per destination, because the two numbers are set on the same
page and are meant to disagree. Nothing that was uploaded or brought back from
a bucket is counted or removed.

## Copies that leave the machine

A backup destination is an S3-compatible bucket the fleet puts every copy in
after it takes one. Any implementation of the S3 API does (AWS, MinIO, Ceph,
Backblaze B2, Cloudflare R2, Garage) and several can be configured at once,
because "offsite" and "somebody else's provider" are different words and some
fleets want both.

There are two places to describe one, and a fleet normally uses whichever suits
it:

* **Settings → Backups → Copies off this machine** is the ordinary way.
  **Add a destination** asks for the endpoint, the bucket and the credential,
  **Test** proves them against the real service before anything is saved, and
  the destination is stored in this fleet's database with its secret key and
  passphrase sealed under the instance encryption key; the same way a
  provider's credential is. Nothing has to be edited on disk and nothing has to
  be restarted.
* **`backup.remotes` in `zoomies.yaml`** describes one in the file instead.
  It is the copy a host that has lost its database can still read, which is the
  day an offsite backup is for, so a name described in both places is the
  file's: the stored row is shown as ignored rather than quietly disagreeing.
  A destination described this way is read-only on the page, and says so.

```yaml
backup:
  interval: 24h
  keep: 7
  remotes:
    - name: offsite
      endpoint: https://s3.eu-west-2.amazonaws.com
      region: eu-west-2
      bucket: acme-zoomies
      prefix: prod
      access_key_id: AKIA...
      # Better in ZOOMIES_BACKUP_REMOTE_SECRET_ACCESS_KEY than here.
      secret_access_key: ""
      passphrase: "a long random phrase kept with the encryption key"
      keep: 30
    - name: minio
      # Plain HTTP warns (backup.remote_insecure), and rightly: accept it only
      # on a network you own end to end.
      endpoint: http://minio.internal:9000
      bucket: backups
      access_key_id: zoomies
      secret_access_key: ""      # ZOOMIES_BACKUP_REMOTE_2_SECRET_ACCESS_KEY
      passphrase: ""             # ZOOMIES_BACKUP_REMOTE_2_PASSPHRASE
```

What lands in the bucket is exactly what the **Download** button produces:
`zoomies-20260908-181718.tar.gz`, or `.tar.gz.enc` when the remote has a
passphrase. There is no separate format, so a copy pulled out of a bucket in
three years is opened by `zoomies restore` and by nothing else in particular.

Three things are worth knowing before you rely on it:

* **Set a passphrase.** A backup is the whole fleet (every repository and job
  it has seen, every account, and the sealed GitHub App credentials) and in a
  bucket it is a file anyone who can read the bucket can open. With a
  passphrase the archive is sealed with argon2id and AES-256-GCM before it
  leaves this host, so the bucket holds something its owner cannot read.
  Nothing on the controller can recover a lost passphrase: keep it where you
  keep the encryption key. Without one, the startup validator says so every
  time (`backup.remote_plaintext`).
* **Know which place holds your credentials, and what each costs.** A stored
  destination's secret key and passphrase are sealed with the instance
  encryption key, so they are only as recoverable as that key: a database
  restored onto a host holding a different one raises
  `backup.remote_unreadable` and sends nothing until the key is put back or the
  secret is entered again. A destination in the file is readable by anything
  that can read the file, which is why the secret belongs in
  `ZOOMIES_BACKUP_REMOTE_SECRET_ACCESS_KEY` and the file belongs at mode 0600,
  and why it is the one that still works on a host with no database at all.
  Neither ever reaches a manifest, a settings export or a diagnostics bundle;
  what the manifest does carry is the *list* of destinations whose secrets the
  key opens, so a restore knows in advance what it will lose without it.
* **Zoomies never creates the bucket.** A backup destination that appeared by
  itself is one nobody has set the retention, versioning or access policy of.
  Make it, give the credential `s3:PutObject`, `s3:GetObject`,
  `s3:DeleteObject` and `s3:ListBucket` on it, and consider object versioning
  and a lifecycle rule; they are the protection against somebody deleting the
  copies, which retention here cannot be.

The pass itself is written as *make the bucket hold what the directory holds*
rather than *upload the backup that was just taken*. A remote that was
unreachable for two nights is two backups behind, so the next pass sends both,
oldest first; and a remote with `keep: 30` is never sent the thirty-first
oldest backup only to delete it a second later.

The pass is part of taking a backup rather than a separate thing somebody
remembers: the schedule's copy, one taken from the page and one taken by
`zoomies backup` all leave for every destination as they are written, and the
upload happens after the request has answered so that pressing the button does
not mean holding a browser open for the length of a transfer. **Copy offsite
now** runs the same pass on demand, which is what an operator who has just
fixed a credential wants. Every destination is reconciled again on the hour
regardless, so one that was unreachable catches up by itself,
`backup.remote_failed` in the problems drawer carries the service's own
refusal, which is usually a wrong secret, a bucket that is not there, or a
clock too far out to sign with.

**Backups → Copies off this machine** lists every destination whichever place
describes it: what it holds and when it last took a copy, **Test** for a
listing that proves the credential, **Show copies** for a live listing of the
bucket, and **Bring back** for one of them. A stored destination also has
**Edit** (which is where a rotated key goes) and **Remove**, which forgets the
destination and leaves what its bucket holds alone. `zoomies backup` sends the
copy too (`--remote` for one destination, `--no-offsite` for none), and says
what it did.

The schedule itself (where backups go, how often, and how many are kept) is
on the same page, above the list. Those three settings are also in the
configuration export and can still be set in `zoomies.yaml` or the
environment; a value the environment is holding reads as pinned here exactly as
it does on the Configuration page.

### Coming back from one

Fetching is separate from restoring on purpose. A restore swaps the database
the fleet runs on, and the copy it swaps in should be one somebody has seen
land and verified first, so **Bring back** unpacks the archive into the backup
directory as an ordinary backup (verified on the way in, marked *From
offsite*, and never counted or removed by retention) and the restore is the
same staged restore as for any other backup.

On a host that has lost everything but `zoomies.yaml` and the key, the command
line does both halves:

```console
$ zoomies restore --from-remote offsite
acme-zoomies/prod holds 7 backups:

  zoomies-20260908-181718  41.2 MB  taken 2026-09-08T18:17:18Z, encrypted
  zoomies-20260907-181702  41.1 MB  taken 2026-09-07T18:17:02Z, encrypted
  ...

Restore one with:
  zoomies restore --from-remote offsite zoomies-20260908-181718 --replace

$ zoomies restore --from-remote offsite latest --replace
```

`latest` is the newest copy the remote holds. The restore that follows is the
ordinary one, with every check and every refusal it makes, and the fleet comes
back fenced, exactly as it would from a local copy.

## Restoring

There are two ways. The command line restores a stopped controller's database
in place; the Backups tab stages a restore for the running controller to apply
when it restarts. Both make the same checks and do the same things to the
restored database, so the sections below apply to both.

### From the settings page

A controller cannot restore under itself: the database is open, every loop
holds it, and a file swapped under a process that has it open is a process
reading a file nobody else can see. So **Restore** on the Backups tab stages
the restore rather than performing it:

1. Every check in *What it refuses* below is made now (the copy is sound, this
   build can read it, and this host's key is the one that sealed it) because
   now is when you are looking. A refusal is a sentence in the dialog, not a
   line in a log after the restart.
2. The restore is written down beside the database, and the tab shows a banner
   saying which backup is waiting, who asked, and what will be invalidated.
   Nothing has changed yet: the fleet runs on the database it has, the
   problems drawer says `backup.restore_staged`, and **Cancel** forgets it.
3. **Restart and restore** stops the controller. It exits with code `3`, which a
   systemd unit set to restart on failure and a container with a restart
   policy both act on; the next controller to start finds the staged restore,
   applies it before it opens the database, and comes up fenced. The tab
   watches the health probe through both halves (gone, then back) and says
   which half it is in, so "still restarting" and "never coming back" are told
   apart. If nothing starts the process, start it by hand: the restore is
   applied whoever starts it.

What became of it is recorded either way. A restore that did not happen is
`backup.restore_failed` in the drawer with the reason. A restore that was
refused before anything moved leaves the controller on the database it already
had; one that failed after the copy has replaced it, so the reason says which
database is in place and whether it is fenced, and where the old one was kept.
A restore that did happen is a banner on the tab saying what was moved aside
and what was invalidated, until you dismiss it.

The two options the dialog offers (revoking every API token, and making every
agent join again) are the command's two flags, and the same cost applies.

### From the command line

```sh
zoomies restore /var/backups/zoomies/zoomies-20260908-181718
```

It restores the database and nothing else. The encryption key, `zoomies.yaml`
and the service unit are yours to put back, because each is a decision about
this host rather than a copy of the data.

Stop the controller first. Restoring underneath a running one leaves it holding
a database that is no longer there, and `zoomies restore` now refuses rather
than letting you find that out afterwards.

### What it refuses, and why it refuses rather than warns

Every one of these is something you would otherwise discover after the
controller was running on the restored data; the fleet live, the original
possibly gone, and the symptom saying nothing about the restore that caused it.
So they all happen before anything is moved.

* **A controller that is still running.** Restore takes the same lock a
  controller takes before it opens the database, and holds it until it is
  finished. Renaming a file does not reach a process that already has it open:
  a live controller would go on reading the database moved aside, every
  connection it opened afterwards would read the restored one, and the writes
  it made in between would land in the file nobody looks at again. Nothing
  fails at the time, which is what makes it worth refusing.
* **A copy that is not sound.** The backup's database is opened and integrity
  checked first.
* **A backup from a newer release.** The store refuses a ledger naming
  migrations this binary does not have, and the refusal names them.
* **The wrong encryption key.** The manifest's fingerprint is compared against
  the key this host is configured with. A mismatch would give you a fleet that
  starts, reports itself healthy, and cannot authenticate to GitHub.
* **An existing database.** `--replace` is required, and it moves the database
  that was there to `zoomies.db.before-restore-<timestamp>` rather than
  deleting it, with its `-wal` and `-shm`, which belong to that database and
  would otherwise be replayed into the restored one.

### What it invalidates

A backup freezes credentials in the state where they still work, and restoring
brings them back. Two are dealt with by default:

* **Every session.** A browser cookie from the day of the backup would
  otherwise still be signed in.
* **Every unredeemed join token.** Each one enrols a new host. The redeemed
  ones are kept: they cannot be used again, and they are the record of how each
  host got here.
* **Every rented machine's proof of ownership.** If you rent hosts from an
  infrastructure [provider](providers.md), a restored database is a *copy*, and
  the machines it names may since have been destroyed, rebuilt, or handed to a
  different controller. Zoomies will not delete a machine it cannot currently
  prove it owns, so the restore takes that proof away and each one has to be
  re-established against the hypervisor before anything can act on it. Nothing
  else about the machines changes; the rows, the resources and the hosts are
  all still there.

Two more are flags, because each has a cost only you can weigh:

| Flag | When |
| --- | --- |
| `--revoke-api-tokens` | The backup may have been read by someone else. Whatever automation holds a token needs a new one. |
| `--reset-agent-tokens` | Same, or you are rebuilding the fleet's hosts anyway. Each agent exits with the command to join again. |

### A backup from an older release

`zoomies restore` accepts it (the refusal is only for a backup from a *newer*
release) and the database is migrated to this build as part of putting it
back. A copy of it exactly as it was is kept first, under `pre-migration/`
beside the database, so restoring an old backup does not consume it.

### Upgrades copy the database first

Whenever a controller starts and finds migrations pending on an existing
database, it copies it to `pre-migration/zoomies-<timestamp>/` before applying
anything, and keeps the last two. Migrations are one-way and the release that
wrote a database will refuse to open it once a newer one has moved it on, so
this is the rollback for an upgrade nobody planned to roll back, and it is the
same layout `zoomies restore` takes, so putting one back is one command.

It is a copy of the whole database each time, so the two are worth checking on
the disk budget of a very large fleet. A first start takes none: there is
nothing there to lose.

### And what the controller checks when it starts

`zoomies restore` is not the only path onto a restored database (a database
put in place by hand skips all of the above) so the controller makes two of
the same checks itself, at startup, rather than hours later:

* **The database is newer than the binary.** It refuses to start and names the
  migrations it does not have. See [there is no
  downgrade](upgrading.md#there-is-no-downgrade).
* **The key did not come with the database.** If the database holds GitHub App
  credentials and there is no key file, it refuses to start and names the file
  to put back, rather than generating a fresh key, which is what it does on a
  genuine first run, and which here would leave every sealed credential
  unreadable for good.

A key that is present but *wrong* cannot be refused at startup (a key is
proven only by opening something) so that shows up as `crypto.key_mismatch` in
the problems drawer, naming the installations it cannot decrypt. `zoomies
restore` catches it earlier, from the manifest's fingerprint.

### The fence, and the three things that do not travel with the file

The restored database is marked for recovery, with the reason recorded and an
audit row saying where it came from. A controller reading that mark **decides
as normal and applies none of it**: no runner is created, drained or removed,
nothing is reaped from GitHub, and the fallback poller does not sweep. The
Overview and the problems drawer show exactly what it would do the moment the
fence is lifted, which is the difference between "nothing to do" and "not
allowed to".

`/readyz` answers 503 while the fence is on, so a load balancer takes the
instance out of rotation. Liveness is unaffected, so a container runtime does
not restart it; that would achieve nothing and lose your session.

Check these three before you lift it, because they are what a restore does not
bring with it:

* **The external URL.** If the new machine answers on a different address, the
  GitHub App's webhook URL points at the old one and no delivery will arrive.
  The Installations page says when a webhook was last seen.
* **The agents.** Each one holds a credential for a controller. A restore onto
  the same address and certificate needs nothing; a new address means rejoining
  the hosts with fresh join tokens.
* **The runners.** Any runner that was live belongs to a host that no longer
  reports it. The controller reclaims runners from a host that has stopped
  heartbeating, so this resolves itself, but the first few minutes will show
  failures for work that had already gone.
* **The rented machines, if you have any.** While the fence is on, no machine
  is created *or deleted*, deletion is fenced too, which it is not for runners,
  and the asymmetry is deliberate. A restored copy's rows may describe virtual
  machines a different, still-running controller owns, and deleting one of
  those on the strength of a restored row is the worst thing this system could
  do. Lifting the fence does not resume deleting either: each machine's
  ownership has to be proved against the provider first, and one whose row and
  resource disagree is quarantined for you to look at rather than destroyed.

Then lift the fence:

```sh
curl -X POST -H "Authorization: Bearer $ZOOMIES_TOKEN" \
  https://zoomies.example.com/api/v1/recovery/unfence
```

It needs an administrator, and it is audited under its own action; the audit
log is where somebody later asks who decided the fleet was ready.

### Restoring onto a second machine

Stop the original first. Two controllers reconciling one fleet's runners from
the same rows is how a restore becomes an outage; they will each decide the
other's runners are theirs to remove. There is no interlock across machines
that could enforce this for you.

## What is not worth backing up

Runner containers, work directories and the runner binary cache are all
disposable by design; an ephemeral runner is destroyed after one job, and
anything a host holds can be rebuilt by pulling an image. There is nothing in
`work_dir` that a restore needs.

## How often

The database is the only thing that changes, and what it holds is
configuration and history rather than anything a workflow depends on minute to
minute. The default schedule (nightly, keeping seven) is enough for most
fleets; the controller takes an extra copy before an upgrade by itself, because
that is the only rollback there is. Where those copies go is the other half of
the question, and the honest answer is that a directory on the same disk is not
an answer: add a [backup destination](#copies-that-leave-the-machine), or ship
the directory yourself. Until one of the two is true the Backups page and the
startup output say so (`backup.no_remote`), which is the whole point of the
entry.

## Moving a configuration

A backup is the whole fleet. The configuration alone (the settings an
administrator has set, and nothing else) travels separately, from
**Settings → Configuration**:

* **Export** writes every setting somebody has set: stored in the database, set
  in the file, or pinned by the environment. Defaults are left out, because
  they are computed on the host that reads them, and no secret's value is ever
  in it; the export names the secrets that were configured, so the import can
  say which to set by hand. As YAML it is the shape `zoomies.yaml` takes, so
  the file can be started from; as JSON it carries when it was taken and where
  from.
* **Import** reads an export, or a `zoomies.yaml`, back. Every key is planned
  through the same checks a change on the page goes through and shown first:
  which would change, which are already so, and which this controller refuses
  and why. Applying is one change or none (a refused key has to be fixed in
  the document or left out) and the keys that wait for a restart are marked
  on the page afterwards, exactly as a change made by hand would be.

Both are audited, as `settings.export` and `settings.import`.

### Moving pools

The pools are the fleet's other half, and they travel the same way, from
**Pools → Export** and **Pools → Import** or with `zoomies pools export` and
`zoomies pools import`. It is how the pools you run on one instance reach
another you run (staging to production, an old machine to a new one) and how
they are kept in version control beside the workflows that use them.

* **Export** writes every pool's own settings: its platform, labels, limits and
  runner settings. Nothing the instance made up about a pool is in it (no id,
  no counts, no timestamps) and the installation is named by the organisation
  or repository it covers rather than by its id, so the file means the same
  thing on an instance with the same GitHub App installed. A pool's
  environment is the one place a secret can hide, so the export names the
  variables and never carries their values; set those by hand afterwards.
* **Import** reads that file back and matches pools by name. Each is planned
  through the same checks the pool editor and `PATCH` go through and shown
  first: which pools would be created, which would change and in which
  settings (with the value now and the value incoming) which are already so,
  and which this instance refuses and why. An installation it does not have,
  a figure the pools API refuses, and an edit that would leave a pool with no
  host that could run it are all refusals; a new pool that no host can run yet
  is created with a warning, as the editor allows. Applying is one change or
  none: a refused pool has to be fixed in the document or skipped.

As with the settings, what a document leaves out is left alone. A pool the
file does not name is never deleted, and a setting a pool entry leaves out
keeps the value it has, so a hand-written file that says only
`max_runners: 2` changes that and nothing else. Applying the same file twice
changes nothing the second time.

Both are audited, as `pools.export` and `pools.import`, and every pool the
import creates or changes has its own `pool.create` or `pool.update` row.

## Moving one installation, or removing its history

A backup is the whole fleet and is sealed under the instance key, which also
seals every other installation's credentials. To take one GitHub App to a new
machine without handing that key over, export the installation alone:

```sh
zoomies export --installation ins_k3f9qz2m --passphrase-file ./move.pass
zoomies import zoomies-installation-ins_k3f9qz2m.json --passphrase-file ./move.pass   # on the new instance
zoomies installations verify ins_k3f9qz2m
```

The archive holds the installation, its pools, runners, jobs and their events,
webhook deliveries, scaling events, runner sessions, usage days and capacity
samples, and every audit row that names any of them. With a passphrase, the
App's private key and webhook secret are sealed under it with argon2id and
AES-256-GCM (the scheme an encrypted backup uses) and the import opens them
and seals them again under the new instance's key; without one, the history
travels and the credentials do not. The instance-sealed columns never leave in
any case. The import is all or nothing, keeps every row's ID, and leaves the
runner rows out: they name hosts only the old instance has, and their history
arrives as sessions. Stop the pools on the old instance first, for the reason
[restoring onto a second machine](#restoring-onto-a-second-machine) gives.

Deleting an installation leaves its history behind on purpose. To remove that
too (a team that has left, whose jobs and audit trail should go with it)
purge it: `DELETE /api/v1/installations/{id}?purge=true&confirm=<target>`,
with the target typed to confirm, removes exactly the set an export writes and
nothing that belongs to another installation. Export first if it may ever be
wanted. The one row left afterwards is the `installation.purge` audit entry
recording that it happened. Export and import are audited as
`installation.export` and `installation.import`.
