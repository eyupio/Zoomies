package controller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/backup"
	"github.com/eyupio/zoomies/internal/config"
)

// Getting the copies off the machine.
//
// The scheduled backup is a backup against a mistake. This is the other half:
// every copy the fleet takes is put in an S3-compatible bucket somebody else's
// disk is responsible for, because a directory of tidy copies on the volume
// that just died is the worst kind of backup -- everyone believed in it.
//
// The pass is written as "make the bucket hold what the directory holds"
// rather than "upload the backup that was just taken", and that is the whole
// design. A remote that was unreachable for two nights is two backups behind;
// a pass that only ever sent the newest would leave the gap there forever and
// nothing would say so. So every pass lists the remote, works out what is
// missing, sends it oldest first, and applies the remote's own retention
// afterwards.

const (
	// remoteSweep is how often the remotes are reconciled when nothing has
	// happened. Uploads are driven by taking a backup; this is the safety net
	// that catches up after an outage nobody watched, and it is slow because
	// listing a bucket is a request that costs money in some of them.
	remoteSweep = time.Hour
	// remoteRetry is how long a failed remote waits before it is tried again.
	// A bucket that refused the signature at noon refuses it at 12:01, and a
	// warning that re-fires every minute is a warning that gets muted.
	remoteRetry = 15 * time.Minute
	// remoteUploadTimeout bounds one pass over one remote: listing it,
	// sending what it is missing, and pruning it. It is generous because the
	// thing being sent is a whole database over a link nobody promised
	// anything about, and bounded because a transfer that has stalled must
	// not hold the pass open until the process restarts.
	remoteUploadTimeout = 6 * time.Hour
)

// The two places a destination is described, named here as the API serves
// them. They are internal/backup's, aliased so a handler reading this file
// does not have to go looking for where the merge lives.
const (
	RemoteSourceFile     = backup.OriginFile
	RemoteSourceDatabase = backup.OriginDatabase
)

// backupRemotes is every destination this fleet has: the file's and the
// stored ones, merged by internal/backup so the CLI and the controller agree
// about which wins.
func (c *Controller) backupRemotes(ctx context.Context) []backup.ResolvedRemote {
	resolved, _ := c.resolveBackupRemotes(ctx)
	return resolved
}

// resolveBackupRemotes is backupRemotes that also says whether it saw the whole
// configuration. It is false when the database would not answer, in which case
// only the file's destinations came back -- and a caller deciding what has
// left the configuration must not read that as every stored one having left.
func (c *Controller) resolveBackupRemotes(ctx context.Context) ([]backup.ResolvedRemote, bool) {
	resolved, err := backup.ResolveRemotes(ctx, c.cfg(), c.st, c.key)
	if err != nil {
		// The file's destinations are still returned, which is the half that
		// works when the database will not answer.
		c.log.Warn("could not read the stored backup remotes", "error", err)
	}
	return resolved, err == nil
}

// usableRemotes is the destinations a pass actually sends to.
func usableRemotes(resolved []backup.ResolvedRemote) []backup.ResolvedRemote {
	var out []backup.ResolvedRemote
	for _, r := range resolved {
		if r.Usable() {
			out = append(out, r)
		}
	}
	return out
}

// usableBackupRemotes is the destinations a pass actually sends to.
func (c *Controller) usableBackupRemotes(ctx context.Context) []backup.ResolvedRemote {
	return usableRemotes(c.backupRemotes(ctx))
}

// forgetGoneRemotes drops the state of a destination that is no longer in the
// configuration at all. Nothing ships to it, so its last attempt is never
// refreshed, and left in place it ages past the sweep and keeps the loop
// reconciling -- and listing every bucket that remains -- on every tick.
//
// Only a name that is gone goes. A switched-off, shadowed or unreadable
// destination is still on the page, and the page shows what its last listing
// found. And it does nothing unless the whole configuration was read: a
// database that briefly would not answer must not blank every stored remote.
func (c *Controller) forgetGoneRemotes(resolved []backup.ResolvedRemote, complete bool) {
	if !complete {
		return
	}
	named := make(map[string]bool, len(resolved))
	for _, r := range resolved {
		named[r.Remote.Name] = true
	}
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	for name, st := range c.backups.remotes {
		if !named[name] && !st.uploading {
			delete(c.backups.remotes, name)
		}
	}
}

// remoteState is what the controller knows about one destination. The bucket
// is the durable record; this is only what the page and the problems drawer
// need to say whether the last attempt worked.
type remoteState struct {
	uploading     bool
	lastAttemptAt time.Time
	lastUploadAt  time.Time
	lastUploadID  string
	lastError     string
	// held and heldBytes are what the last listing found, so the page can say
	// "7 copies, 412 MB" without a request of its own.
	held      int
	heldBytes int64
	listedAt  time.Time
}

// RemoteBackupStatus is one destination as the Backups tab and the problems
// drawer see it.
type RemoteBackupStatus struct {
	// Name is what this destination is called, and what the API addresses it
	// by.
	Name string `json:"name"`
	// Where is the bucket and prefix, and Endpoint the service, so an
	// operator can tell two buckets apart without opening the configuration.
	Where    string `json:"where"`
	Endpoint string `json:"endpoint"`
	// Encrypted says the archive is sealed before it leaves this host.
	Encrypted bool `json:"encrypted"`
	// Keep is this remote's own retention; 0 keeps every copy.
	Keep int `json:"keep"`
	// Disabled is a remote that is configured and switched off, which is
	// shown rather than hidden: a destination nobody can see is one nobody
	// notices has stopped.
	Disabled bool `json:"disabled"`
	// Source says where this destination is described -- "file" for
	// zoomies.yaml and the environment, "database" for one added on the page.
	// The page needs it to know what it may edit, and an operator needs it to
	// know where to go and change something.
	Source string `json:"source"`
	// ID is the stored destination's row id, empty for one the file
	// describes. Region, Bucket, Prefix and AccessKeyID are the rest of the
	// form, so the page can open an editor without a second request.
	ID          string `json:"id,omitempty"`
	Region      string `json:"region,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	AccessKeyID string `json:"access_key_id,omitempty"`
	// PathStyle is the tri-state: null asks for the style to be chosen from
	// the endpoint.
	PathStyle *bool `json:"path_style"`
	// Shadowed is a stored destination the file overrides by name. Nothing is
	// sent to it, and the page says why rather than leaving a row that
	// quietly does nothing.
	Shadowed bool `json:"shadowed"`
	// Problem is why this destination cannot be used at all -- almost always
	// that this controller's key does not open its stored secrets.
	Problem string `json:"problem,omitempty"`
	// HasSecretKey says a secret key is held for this destination. The secret
	// itself is never served: whether there is one is the only part of it a
	// page can act on.
	HasSecretKey bool `json:"has_secret_key"`
	// Uploading says something is being sent right now.
	Uploading bool `json:"uploading"`
	// Copies and Bytes are what the last listing found, and ListedAt when.
	Copies   int        `json:"copies"`
	Bytes    int64      `json:"bytes"`
	ListedAt *time.Time `json:"listed_at"`
	// LastUploadAt and LastUploadID are the last copy that reached it.
	LastUploadAt *time.Time `json:"last_upload_at"`
	LastUploadID string     `json:"last_upload_id,omitempty"`
	// LastError is why the last attempt did not work, in the words the
	// service used.
	LastError string `json:"last_error,omitempty"`
}

// remoteStates is the per-destination state, keyed by name, behind the backup
// state's own mutex: a name that leaves the configuration takes its state with
// it at the next pass.
func (c *Controller) remoteState(name string) *remoteState {
	if c.backups.remotes == nil {
		c.backups.remotes = map[string]*remoteState{}
	}
	st, ok := c.backups.remotes[name]
	if !ok {
		st = &remoteState{}
		c.backups.remotes[name] = st
	}
	return st
}

// BackupRemotes reports every destination this fleet has and what became of
// it: the file's first, then the stored ones.
func (c *Controller) BackupRemotes(ctx context.Context) []RemoteBackupStatus {
	resolved := c.backupRemotes(ctx)
	out := []RemoteBackupStatus{}
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	for _, entry := range resolved {
		r := entry.Remote
		status := RemoteBackupStatus{
			Name: r.Name, Where: r.Where(), Endpoint: r.Endpoint,
			Encrypted: r.Encrypted(), Keep: r.Keep, Disabled: !r.Enabled(),
			Source: entry.Origin, ID: entry.ID, Shadowed: entry.Shadowed, Problem: entry.Problem,
			Region: r.Region, Bucket: r.Bucket, Prefix: r.Prefix,
			AccessKeyID: r.AccessKeyID, PathStyle: r.PathStyle,
			HasSecretKey: strings.TrimSpace(r.SecretAccessKey) != "",
		}
		if st, ok := c.backups.remotes[r.Name]; ok && !entry.Shadowed {
			status.Uploading = st.uploading
			status.Copies = st.held
			status.Bytes = st.heldBytes
			status.LastUploadID = st.lastUploadID
			status.LastError = st.lastError
			if !st.listedAt.IsZero() {
				at := st.listedAt
				status.ListedAt = &at
			}
			if !st.lastUploadAt.IsZero() {
				at := st.lastUploadAt
				status.LastUploadAt = &at
			}
		}
		out = append(out, status)
	}
	return out
}

// RemoteBackup resolves one destination by name, for the handlers that list,
// fetch from or delete a copy in it.
func (c *Controller) RemoteBackup(ctx context.Context, name string) (*backup.Remote, error) {
	resolved, err := backup.FindResolvedRemote(ctx, c.cfg(), c.st, c.key, name)
	if err != nil {
		return nil, err
	}
	return backup.NewRemote(resolved.Remote, c.backupHTTP)
}

// CheckBackupRemote tests a destination that has not been saved yet, which is
// what makes the page's "test" button worth pressing before the credential is
// stored rather than after.
func (c *Controller) CheckBackupRemote(ctx context.Context, cfg config.BackupRemote) error {
	remote, err := backup.NewRemote(cfg, c.backupHTTP)
	if err != nil {
		return err
	}
	return remote.Check(ctx)
}

// NoteRemoteListing records what a listing found, so that a page which has
// just read a remote leaves the count behind for the page that has not.
func (c *Controller) NoteRemoteListing(name string, copies int, bytes int64) {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	st := c.remoteState(name)
	st.held = copies
	st.heldBytes = bytes
	st.listedAt = c.Now()
}

// nudgeRemotes asks the backup loop to reconcile the remotes now. Capacity 1,
// like every other nudge in here: it is a flag saying there is something to
// send, not a queue of things to send.
func (c *Controller) nudgeRemotes() {
	select {
	case c.backups.ship <- struct{}{}:
	default:
	}
}

// NudgeBackupRemotes asks the backup loop to reconcile the destinations now.
// It is what a destination added or corrected on the page presses: waiting an
// hour to find out whether it works is the opposite of what an operator who
// has just typed a credential wants.
func (c *Controller) NudgeBackupRemotes() { c.nudgeRemotes() }

// ShipBackups makes every enabled remote hold what the backup directory holds,
// and returns what it sent.
//
// It is safe to call from anywhere: one pass at a time across the whole
// controller, so the loop's catch-up and an operator pressing "copy offsite
// now" cannot upload the same archive twice.
func (c *Controller) ShipBackups(ctx context.Context) ([]backup.Copy, error) {
	cfg := c.cfg()
	resolved, complete := c.resolveBackupRemotes(ctx)
	// Before the early return, so that deleting the last destination clears
	// its state too.
	c.forgetGoneRemotes(resolved, complete)
	configured := usableRemotes(resolved)
	if len(configured) == 0 {
		return nil, nil
	}
	if !c.beginShipping() {
		return nil, ErrShippingRunning
	}
	defer c.endShipping()

	entries, err := backup.List(backup.Dir(cfg))
	if err != nil {
		return nil, err
	}

	var (
		sent  []backup.Copy
		errs  []error
		clock = c.Now
	)
	for _, entry := range configured {
		cfgRemote := entry.Remote
		// Each destination is built on its own so that one with a typo in its
		// endpoint is recorded against its own name, where the page and the
		// problems drawer show it, and the copies still leave for the others.
		remote, err := backup.NewRemote(cfgRemote, c.backupHTTP)
		if err != nil {
			c.noteRemoteFailure(cfgRemote.Name, err, clock)
			errs = append(errs, err)
			continue
		}
		copies, err := c.shipOne(ctx, remote, entries, clock)
		sent = append(sent, copies...)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", remote.Name(), err))
		}
	}
	return sent, errors.Join(errs...)
}

// noteRemoteFailure records a destination that could not even be built.
func (c *Controller) noteRemoteFailure(name string, err error, clock func() time.Time) {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	st := c.remoteState(name)
	st.uploading = false
	st.lastAttemptAt = clock()
	st.lastError = err.Error()
}

// shipOne reconciles one destination and records what happened to it.
func (c *Controller) shipOne(ctx context.Context, remote *backup.Remote, entries []backup.Entry, clock func() time.Time) ([]backup.Copy, error) {
	name := remote.Name()
	c.backups.mu.Lock()
	st := c.remoteState(name)
	st.uploading = true
	st.lastAttemptAt = clock()
	c.backups.mu.Unlock()

	finish := func(err error, sent []backup.Copy, held []backup.Copy) {
		c.backups.mu.Lock()
		defer c.backups.mu.Unlock()
		st := c.remoteState(name)
		st.uploading = false
		if err != nil {
			st.lastError = err.Error()
			return
		}
		st.lastError = ""
		if n := len(sent); n > 0 {
			st.lastUploadAt = clock()
			st.lastUploadID = sent[n-1].ID
		}
		st.held = len(held)
		st.heldBytes = 0
		for _, item := range held {
			st.heldBytes += item.Bytes
		}
		st.listedAt = clock()
	}

	ctx, cancel := context.WithTimeout(ctx, remoteUploadTimeout)
	defer cancel()

	held, err := remote.List(ctx)
	if err != nil {
		finish(err, nil, nil)
		return nil, err
	}
	var sent []backup.Copy
	for _, entry := range backup.Missing(entries, held, remote.Keep()) {
		copied, err := remote.Upload(ctx, &entry)
		if err != nil {
			finish(err, sent, held)
			c.log.Warn("a backup could not be copied offsite",
				"remote", name, "backup", entry.ID, "where", remote.Where(), "error", err)
			return sent, err
		}
		sent = append(sent, *copied)
		held = append([]backup.Copy{*copied}, held...)
		c.log.Info("copied a backup offsite",
			"remote", name, "backup", entry.ID, "where", remote.Where(),
			"bytes", copied.Bytes, "encrypted", copied.Encrypted)
	}
	if removed, err := remote.Prune(ctx, remote.Keep()); err != nil {
		finish(err, sent, held)
		return sent, err
	} else if len(removed) > 0 {
		c.log.Info("removed old copies from a backup remote",
			"remote", name, "removed", len(removed), "keep", remote.Keep())
		kept := held[:0]
		for _, item := range held {
			if !slices.Contains(removed, item.ID) {
				kept = append(kept, item)
			}
		}
		held = kept
	}
	finish(nil, sent, held)
	return sent, nil
}

// ErrShippingRunning is a second offsite pass asked for while one is running.
var ErrShippingRunning = errors.New("controller: the backups are already being copied offsite; wait for that to finish")

func (c *Controller) beginShipping() bool {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	if c.backups.shipping {
		return false
	}
	c.backups.shipping = true
	return true
}

func (c *Controller) endShipping() {
	c.backups.mu.Lock()
	c.backups.shipping = false
	c.backups.mu.Unlock()
}

// remotesDue says whether the loop should reconcile the remotes on this tick:
// when something asked it to, when a remote is behind after a failure and the
// retry is up, and once a sweep however quiet things have been.
//
// Only a destination a pass would send to can be due. The others are never
// attempted, so their attempt time cannot advance, and counting them would
// keep the loop listing every bucket that does work on every tick.
func (c *Controller) remotesDue(ctx context.Context, now time.Time) bool {
	resolved, complete := c.resolveBackupRemotes(ctx)
	c.forgetGoneRemotes(resolved, complete)
	usable := map[string]bool{}
	for _, r := range usableRemotes(resolved) {
		usable[r.Remote.Name] = true
	}
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	if len(c.backups.remotes) == 0 {
		return true
	}
	for name, st := range c.backups.remotes {
		if st.uploading || !usable[name] {
			continue
		}
		wait := remoteSweep
		if st.lastError != "" {
			wait = remoteRetry
		}
		if st.lastAttemptAt.IsZero() || now.Sub(st.lastAttemptAt) >= wait {
			return true
		}
	}
	return false
}

// remoteProblems is what the drawer says about the copies that leave the
// machine.
//
// A remote that is failing is a warning rather than an error for the same
// reason a failing local backup is: the fleet is still running jobs. It is
// never silent, though, because the whole value of an offsite copy is that
// somebody would notice its absence before the day it is needed.
func (c *Controller) remoteProblems(ctx context.Context) []Problem {
	var out []Problem
	statuses := c.BackupRemotes(ctx)
	// "backups never leave this host" is not raised here. It is an info
	// finding, and Problems deliberately drops those from the drawer so that
	// a list which is never clear does not stop being read -- so the
	// validator keeps it, where the startup output and the Configuration page
	// show it, and the Backups page says the same thing in its own words
	// beside the button that fixes it.
	for _, status := range statuses {
		// A stored destination the file shadows, or one whose secrets will
		// not open, is a problem about this fleet's configuration rather than
		// about a bucket, and each says so in its own words.
		if status.Shadowed {
			out = append(out, Problem{
				Code:     "backup.remote_shadowed",
				Severity: config.SeverityWarning,
				Setting:  "backup.remotes",
				Title:    "the stored backup remote " + status.Name + " is being ignored",
				Detail: "zoomies.yaml or the environment describes a destination with the same name, and the file has the " +
					"last word. Nothing is sent to the stored one, and its settings are not the ones in use.",
				Fix: "rename one of them, or delete the stored destination and keep describing it in the file.",
			})
			continue
		}
		if status.Problem != "" {
			out = append(out, Problem{
				Code:     "backup.remote_unreadable",
				Severity: config.SeverityError,
				Setting:  "backup.remotes",
				Title:    "the backup remote " + status.Name + " cannot be used",
				Detail:   status.Problem,
				Fix: "this is what a database restored onto a host with a different encryption key looks like. Put the key " +
					"this fleet was sealed with back, or open the destination on the Backups tab and enter its secret key again.",
			})
			continue
		}
		// The same two things the startup validator says about a destination
		// in the file, said here about one in the database -- which the
		// validator cannot see. A fleet should not learn that its offsite
		// copies are readable by whoever owns the bucket from where it chose
		// to describe the destination.
		if status.Source == RemoteSourceDatabase && !status.Disabled {
			if !status.Encrypted {
				out = append(out, Problem{
					Code:     "backup.remote_plaintext",
					Severity: config.SeverityWarning,
					Setting:  "backup.remotes",
					Title:    "the backup remote " + status.Name + " is sent the backup unencrypted",
					Detail: "a backup is the whole fleet: every repository and job it has seen, every account, and the sealed " +
						"GitHub App credentials. In " + status.Where + " it is a file anyone who can read the bucket can open.",
					Fix: "set a passphrase on the destination from the Backups tab, the archive is then sealed with argon2id and " +
						"AES-256-GCM before it leaves this host, and keep it wherever you keep the encryption key. Nothing here can recover it.",
				})
			}
			if u, err := url.Parse(status.Endpoint); err == nil && u.Scheme == "http" && !config.LoopbackHost(u.Hostname()) {
				out = append(out, Problem{
					Code:     "backup.remote_insecure",
					Severity: config.SeverityWarning,
					Setting:  "backup.remotes",
					Title:    "the backup remote " + status.Name + " is reached over plain HTTP",
					Detail: "the access key, the signature and the backup itself cross the network in the clear, so anyone on the " +
						"path can both read the fleet and write to the bucket afterwards.",
					Fix: "use https:// unless the endpoint is on this host or a network you own end to end.",
				})
			}
		}
		if status.LastError == "" {
			continue
		}
		at := c.Now()
		c.backups.mu.Lock()
		if st, ok := c.backups.remotes[status.Name]; ok && !st.lastAttemptAt.IsZero() {
			at = st.lastAttemptAt
		}
		c.backups.mu.Unlock()
		detail := fmt.Sprintf("the last attempt, at %s, failed: %s. The controller tries again every %s.",
			at.Format(time.RFC3339), status.LastError, remoteRetry)
		if status.LastUploadID != "" && status.LastUploadAt != nil {
			detail += fmt.Sprintf(" The newest copy it holds is %s, from %s.", status.LastUploadID, status.LastUploadAt.Format(time.RFC3339))
		} else {
			detail += " Nothing has ever reached it."
		}
		out = append(out, Problem{
			Code:     "backup.remote_failed",
			Severity: config.SeverityWarning,
			Setting:  "backup.remotes",
			Title:    "backups are not reaching " + status.Name,
			Detail:   detail,
			Fix:      "read the error: it names the bucket, the credential or the clock. The Backups tab tests the remote on demand, and the copies on this host are unaffected, they are simply all there is.",
			Since:    &at,
		})
	}
	return out
}
