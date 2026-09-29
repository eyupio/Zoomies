package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/backup"
	"github.com/eyupio/zoomies/internal/config"
)

// The controller's own backups.
//
// A fleet whose only backup is the one an operator remembers to take is a
// fleet with no backup, so the controller takes one itself on the interval
// backup.interval sets, into the directory backup.directory names, and keeps
// backup.keep of them. It is the same copy `zoomies backup` writes and the
// settings page takes on demand: one layout, one manifest, one restore.
//
// A backup beside the database is a backup against a mistake and not against
// the disk, which is why a backup here is an event rather than a file. The
// whole of the event is: take the copy, apply retention to the directory, and
// reconcile every destination backup.remotes names. Whichever of the three
// asked for it gets all of it, because "the copy left the machine" is not
// something an operator should have to press a second button for and then
// remember to.

const (
	// backupTick is how often the loop asks whether a backup is due. A minute
	// is fine-grained enough that "every 24h" means what it says and coarse
	// enough that an idle controller does not read its backup directory a
	// thousand times an hour.
	backupTick = time.Minute
	// backupRetry is how long a failed scheduled backup waits before trying
	// again. Disk that is full at noon is usually still full at 12:01, and a
	// warning that re-fires every minute is a warning that gets muted.
	backupRetry = 15 * time.Minute
)

// backupState is what the loop knows about its own last attempt, kept in
// memory: the backups themselves are the durable record, and the problems
// drawer only needs to know whether the most recent attempt worked.
type backupState struct {
	mu sync.Mutex
	// running is held while a backup is in progress, whoever asked for it,
	// so that the scheduler and the settings page cannot take two at once.
	running bool
	// lastAttemptAt and lastError describe the last scheduled attempt. A
	// backup an operator takes by hand clears the error: the thing that was
	// failing has just worked.
	lastAttemptAt time.Time
	lastError     string
	lastID        string

	// shipping is held while an offsite pass is running, whoever asked for
	// it, and remotes is what became of each destination, keyed by its name.
	// They share this mutex because they are read together: the Backups tab
	// asks one question and gets the schedule and every bucket in one answer.
	shipping bool
	remotes  map[string]*remoteState
	// ship is the nudge a finished backup gives the loop, so a copy reaches
	// the bucket in the minute it was taken rather than at the next sweep.
	// Capacity 1: it is a flag, not a queue.
	ship chan struct{}
}

// BackupStatus is the state of scheduled backups, for the settings page and
// the problems drawer.
type BackupStatus struct {
	// Directory is where backups are kept, resolved.
	Directory string `json:"directory"`
	// Interval is backup.interval; zero means scheduled backups are off.
	Interval time.Duration `json:"-"`
	// Keep is backup.keep.
	Keep int `json:"keep"`
	// Running says a backup is being taken right now.
	Running bool `json:"running"`
	// LastScheduledAt is when the loop last tried, LastScheduledID what it
	// wrote, and LastError why it did not.
	LastScheduledAt time.Time `json:"-"`
	LastScheduledID string    `json:"last_scheduled_id,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	// NextDueAt is when the next scheduled backup is expected, from the
	// newest backup in the directory and the interval. Zero when off.
	NextDueAt time.Time `json:"-"`
}

// BackupDir is where this controller keeps its backups.
func (c *Controller) BackupDir() string { return backup.Dir(c.cfg()) }

// TakeBackup takes a backup now, on behalf of whoever asked, and applies
// retention afterwards.
//
// One at a time. The store serialises the copy itself, but two callers racing
// for the same second would race for the same directory name too, and a
// backup and a prune interleaved could delete the copy being taken.
func (c *Controller) TakeBackup(ctx context.Context, source, by string) (*backup.Entry, error) {
	if !c.beginBackup() {
		return nil, ErrBackupRunning
	}
	defer c.endBackup()

	cfg := c.cfg()
	entry, err := backup.Take(ctx, c.st, backup.TakeOptions{
		Config: cfg, Source: source, TakenBy: by, Now: c.clock,
	})
	if err != nil {
		return nil, err
	}
	// Retention runs after a backup the controller took, whatever asked for
	// it: a backup taken from the page is the same kind of copy as one the
	// schedule took, and the ceiling is the ceiling. Uploads are never
	// counted or removed, which Prune itself guarantees.
	if removed, err := backup.Prune(backup.Dir(cfg), cfg.Backup.Keep); err != nil {
		c.log.Warn("could not remove old backups", "dir", backup.Dir(cfg), "error", err)
	} else if len(removed) > 0 {
		c.log.Info("removed old backups", "removed", len(removed), "keep", cfg.Backup.Keep)
	}
	c.log.Info("took a backup", "id", entry.ID, "source", source, "bytes", entry.Bytes, "dir", entry.Dir)
	// The copy exists; getting it off the machine is the backup loop's next
	// pass rather than this caller's wait. An operator pressing the button
	// should not hold a browser open for the length of an upload, and the
	// schedule should not skip a night because a bucket was slow.
	c.nudgeRemotes()
	return entry, nil
}

// BackupPruning is what applying retention removed, here and in each bucket.
//
// It is reported per destination rather than as a total because the numbers
// disagree on purpose: this host keeps backup.keep copies and each remote
// keeps its own, so "three here, thirty offsite" is a correct fleet and a
// single count would read like a fault.
type BackupPruning struct {
	// Keep is backup.keep as the pass read it. Zero keeps every copy, and
	// nothing is removed from this host at all.
	Keep int `json:"keep"`
	// Removed is what was deleted from the backup directory. Copies somebody
	// uploaded or pulled back out of a bucket are never in it: Prune itself
	// guarantees that, because a copy that arrived here is not one this fleet
	// took and retention has no claim on it.
	Removed []string `json:"removed"`
	// Error is why the directory could not be pruned, in the words the
	// filesystem used.
	Error string `json:"error,omitempty"`
	// Remotes is the same pass over each destination.
	Remotes []RemotePruning `json:"remotes"`
}

// RemotePruning is what retention removed from one destination.
type RemotePruning struct {
	Name string `json:"name"`
	// Keep is this destination's own retention; zero keeps every copy.
	Keep    int      `json:"keep"`
	Removed []string `json:"removed"`
	// Error is the service's own refusal, so a bucket that would not answer
	// reads differently from one that had nothing to remove.
	Error string `json:"error,omitempty"`
}

// PruneBackups applies retention now, to this host's directory and to every
// destination, and reports what went.
//
// Retention otherwise runs as part of a backup, which is the right moment for
// it and the wrong one for an operator who has just lowered backup.keep from
// thirty to seven: nothing at all happens until the next backup, and on a
// fleet with the schedule off that is never. So this exists, it is deliberate,
// and it says what it deleted rather than reporting a tidy success.
//
// Whatever cannot be pruned is recorded against the thing that refused and the
// pass carries on: one bucket with a wrong credential must not leave the
// others full.
func (c *Controller) PruneBackups(ctx context.Context) (BackupPruning, error) {
	cfg := c.cfg()
	out := BackupPruning{Keep: cfg.Backup.Keep, Removed: []string{}, Remotes: []RemotePruning{}}

	// The directory is pruned under the lock a backup takes, for the reason
	// TakeBackup holds it: a prune interleaved with a copy being written
	// could count a directory that is not finished yet.
	if !c.beginBackup() {
		return out, ErrBackupRunning
	}
	removed, err := backup.Prune(backup.Dir(cfg), cfg.Backup.Keep)
	c.endBackup()
	out.Removed = append(out.Removed, removed...)
	if err != nil {
		out.Error = err.Error()
		c.log.Warn("could not remove old backups", "dir", backup.Dir(cfg), "error", err)
	} else if len(removed) > 0 {
		c.log.Info("removed old backups", "removed", len(removed), "keep", cfg.Backup.Keep)
	}

	// And then the buckets, under the offsite pass's own lock: a prune that
	// ran beside an upload could delete the copy being sent.
	configured := c.usableBackupRemotes(ctx)
	if len(configured) == 0 {
		return out, nil
	}
	if !c.beginShipping() {
		return out, ErrShippingRunning
	}
	defer c.endShipping()
	for _, entry := range configured {
		item := RemotePruning{Name: entry.Remote.Name, Keep: entry.Remote.Keep, Removed: []string{}}
		remote, err := backup.NewRemote(entry.Remote, c.backupHTTP)
		if err != nil {
			item.Error = err.Error()
			out.Remotes = append(out.Remotes, item)
			continue
		}
		gone, err := remote.Prune(ctx, remote.Keep())
		item.Removed = append(item.Removed, gone...)
		if err != nil {
			item.Error = err.Error()
			c.noteRemoteFailure(entry.Remote.Name, err, c.Now)
		} else if len(gone) > 0 {
			c.log.Info("removed old copies from a backup remote",
				"remote", entry.Remote.Name, "removed", len(gone), "keep", remote.Keep())
			// What the page says this bucket holds was counted before the
			// prune. Read it once more so the figure the operator is shown
			// after pressing the button is the one they just changed.
			if held, err := remote.List(ctx); err == nil {
				var bytes int64
				for _, one := range held {
					bytes += one.Bytes
				}
				c.NoteRemoteListing(entry.Remote.Name, len(held), bytes)
			}
		}
		out.Remotes = append(out.Remotes, item)
	}
	return out, nil
}

// ErrBackupRunning is a second backup asked for while one is being taken.
var ErrBackupRunning = errors.New("controller: a backup is already being taken; wait for it to finish")

func (c *Controller) beginBackup() bool {
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	if c.backups.running {
		return false
	}
	c.backups.running = true
	return true
}

func (c *Controller) endBackup() {
	c.backups.mu.Lock()
	c.backups.running = false
	c.backups.mu.Unlock()
}

// BackupStatus reports the schedule and its last outcome.
func (c *Controller) BackupStatus() BackupStatus {
	cfg := c.cfg()
	c.backups.mu.Lock()
	out := BackupStatus{
		Directory:       backup.Dir(cfg),
		Interval:        cfg.Backup.Interval,
		Keep:            cfg.Backup.Keep,
		Running:         c.backups.running,
		LastScheduledAt: c.backups.lastAttemptAt,
		LastScheduledID: c.backups.lastID,
		LastError:       c.backups.lastError,
	}
	c.backups.mu.Unlock()
	if out.Interval > 0 {
		out.NextDueAt = c.nextBackupDue(cfg)
		if out.NextDueAt.IsZero() {
			out.NextDueAt = c.Now()
		}
	}
	return out
}

// nextBackupDue is when the schedule next wants a backup: the newest copy the
// fleet took of itself, plus the interval. A backup somebody uploaded or
// pulled back out of a bucket does not count -- it is a copy that arrived
// here, not evidence this fleet has been backing itself up. The zero time
// means there is no such copy, so one is due now.
func (c *Controller) nextBackupDue(cfg *config.Config) time.Time {
	entries, err := backup.List(backup.Dir(cfg))
	if err != nil {
		return time.Time{}
	}
	for _, e := range entries {
		if e.Source == backup.SourceUploaded || e.Source == backup.SourceFetched || e.Problem != "" {
			continue
		}
		return e.TakenAt.Add(cfg.Backup.Interval)
	}
	return time.Time{}
}

// backupLoop takes the scheduled backups.
//
// It is its own loop rather than a housekeeping step: VACUUM INTO on a large
// database takes as long as it takes, and the housekeeping pass has a host
// health check in it that should not wait for a copy to finish.
func (c *Controller) backupLoop(ctx context.Context) {
	ticker := time.NewTicker(backupTick)
	defer ticker.Stop()
	for {
		nudged := false
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.backups.ship:
			nudged = true
		}
		c.backupPass(ctx, nudged)
	}
}

// backupPass is one turn of the loop, pulled out so a test can run one without
// waiting for a tick.
func (c *Controller) backupPass(ctx context.Context, nudged bool) {
	if !nudged {
		c.scheduledBackup(ctx)
		// A backup taken in this pass left its own nudge behind. Take it here
		// rather than at some later tick: a scheduled backup is a backup event
		// like any other, and the copies leave with it.
		nudged = c.tookNudge()
	}
	// And then the copies that leave the machine. A nudge means a backup has
	// just been taken and there is something to send; a tick means checking
	// whether a remote that was unreachable has come back.
	if nudged || c.remotesDue(ctx, c.Now()) {
		c.shipScheduled(ctx)
	}
}

// tookNudge reports whether a nudge was waiting, and takes it.
func (c *Controller) tookNudge() bool {
	select {
	case <-c.backups.ship:
		return true
	default:
		return false
	}
}

// shipScheduled is the loop's offsite pass. Its failures are already recorded
// against each remote, where the page and the problems drawer read them, so
// nothing is logged twice here.
//
// Whether there is anywhere to send a copy is ShipBackups' question rather
// than this one's, and that is the point: the pass used to ask the
// configuration file, which cannot see a destination an operator added on the
// Backups page. A fleet whose only destination was a stored one took its
// backups every night and never sent one, while the page showed a healthy
// bucket and a button that worked when pressed -- the worst way for this to
// fail, because everything an operator could see said it was working.
func (c *Controller) shipScheduled(ctx context.Context) {
	_, err := c.ShipBackups(ctx)
	if errors.Is(err, ErrShippingRunning) {
		// Somebody pressed "copy offsite now" while this pass was being
		// asked for. Put the nudge back rather than dropping it: the reason
		// it exists is a backup that has just been taken, and a pass already
		// half-way through its own listing may not carry it.
		c.nudgeRemotes()
		return
	}
	if err != nil {
		c.log.Debug("the offsite copies did not all go", "error", err)
	}
}

// scheduledBackup is one pass of the loop, pulled out so a test can run one
// without waiting for a tick.
func (c *Controller) scheduledBackup(ctx context.Context) {
	cfg := c.cfg()
	if cfg.Backup.Interval <= 0 {
		return
	}
	now := c.Now()
	c.backups.mu.Lock()
	backingOff := c.backups.lastError != "" && now.Sub(c.backups.lastAttemptAt) < backupRetry
	c.backups.mu.Unlock()
	if backingOff {
		return
	}
	if due := c.nextBackupDue(cfg); !due.IsZero() && due.After(now) {
		return
	}

	entry, err := c.TakeBackup(ctx, backup.SourceScheduled, "the schedule")
	if errors.Is(err, ErrBackupRunning) {
		// Somebody is taking one by hand. That will satisfy the schedule.
		return
	}
	c.backups.mu.Lock()
	defer c.backups.mu.Unlock()
	c.backups.lastAttemptAt = now
	if err != nil {
		c.backups.lastError = err.Error()
		c.log.Warn("the scheduled backup failed", "error", err, "retry_in", backupRetry)
		return
	}
	c.backups.lastError = ""
	c.backups.lastID = entry.ID
}

// NoteManualBackup clears a scheduled failure: what was failing has just
// worked, and a warning about it would be stale.
func (c *Controller) NoteManualBackup() {
	c.backups.mu.Lock()
	c.backups.lastError = ""
	c.backups.mu.Unlock()
}

// backupProblems is what the drawer says about backups: a schedule that is
// failing, a restore that is waiting for a restart, and a restore that did not
// happen.
func (c *Controller) backupProblems(ctx context.Context) []Problem {
	var out []Problem
	status := c.BackupStatus()
	if status.LastError != "" {
		at := status.LastScheduledAt
		out = append(out, Problem{
			Code:     "backup.failed",
			Severity: config.SeverityWarning,
			Setting:  "backup.directory",
			Title:    "the scheduled backup is failing",
			Detail:   fmt.Sprintf("the last attempt, at %s, failed: %s. The controller tries again every %s.", at.Format(time.RFC3339), status.LastError, backupRetry),
			Fix:      "check that " + status.Directory + " exists, is writable by the controller, and has room for a copy of the database; or take one from the Backups tab and read the error there.",
			Since:    &at,
		})
	}
	dbPath := c.DatabasePath()
	if staged, err := backup.LoadStaged(dbPath); err == nil && staged != nil {
		at := staged.RequestedAt
		by := staged.RequestedBy
		if by == "" {
			by = "an administrator"
		}
		out = append(out, Problem{
			Code:     "backup.restore_staged",
			Severity: config.SeverityWarning,
			Title:    "a restore is waiting for the controller to restart",
			Detail: fmt.Sprintf("%s asked for %s to be restored. Nothing has changed yet: the database is swapped when the controller next starts, and until then the fleet runs on the database it has.", by, staged.BackupID) +
				" The restored fleet will come back fenced.",
			Fix:   "restart the controller from the Backups tab to apply it, or cancel it there.",
			Since: &at,
		})
	}
	out = append(out, c.remoteProblems(ctx)...)
	if outcome, err := backup.LastOutcome(dbPath); err == nil && outcome != nil && !outcome.OK {
		at := outcome.AttemptedAt
		out = append(out, Problem{
			Code:     "backup.restore_failed",
			Severity: config.SeverityError,
			Title:    "the last restore did not finish",
			Detail:   fmt.Sprintf("restoring %s at %s failed: %s", outcome.BackupID, at.Format(time.RFC3339), outcome.Error),
			Fix:      "read the reason, put right what it names, and stage the restore again from the Backups tab. Dismiss this from the same tab once it is understood.",
			Since:    &at,
		})
	}
	return out
}

// DatabasePath is the database the staged-restore files sit beside. It is the
// configured path rather than the store's own, because the store a test opens
// is in memory and has nowhere for a file to sit; in production the two are
// the same file.
func (c *Controller) DatabasePath() string { return c.cfg().Database.Path }

// ---------------------------------------------------------------------------
// Restarting
// ---------------------------------------------------------------------------

// RequestRestart asks the process to stop so that its service manager starts
// it again. It is how a staged restore is applied from the settings page: the
// restore has to happen with the database closed, and the only process that
// can close this controller's database is this controller, by exiting.
//
// It is a request, not a promise: whether the process comes back is up to
// whatever started it, which is why the page that presses it says what to do
// if nothing does.
func (c *Controller) RequestRestart(reason string) {
	c.restartOnce.Do(func() {
		c.restartReason = reason
		c.log.Warn("a restart was requested; the controller will stop and expects its service manager to start it again", "reason", reason)
		close(c.restart)
	})
}

// RestartRequested is closed once RequestRestart has been called.
func (c *Controller) RestartRequested() <-chan struct{} { return c.restart }

// Restarting reports whether a restart has been asked for, and why.
func (c *Controller) Restarting() (bool, string) {
	select {
	case <-c.restart:
		return true, c.restartReason
	default:
		return false, ""
	}
}
