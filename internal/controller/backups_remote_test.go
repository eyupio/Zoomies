package controller

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/backup"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// offsite points the harness's fleet at a fake object store and returns it.
func offsite(h *harness, adjust func(*config.BackupRemote)) *backup.FakeS3 {
	h.t.Helper()
	fake := backup.NewFakeS3("backups")
	h.t.Cleanup(fake.Close)
	remote := config.BackupRemote{
		Name: "offsite", Endpoint: fake.Endpoint(), Bucket: fake.Bucket(), Prefix: "fleet",
		AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret",
	}
	if adjust != nil {
		adjust(&remote)
	}
	h.c.UpdateConfig(func(c *config.Config) {
		c.Backup.Interval = time.Hour
		c.Backup.Remotes = []config.BackupRemote{remote}
	})
	return fake
}

// The copy the schedule takes has to reach the bucket, because a backup on the
// disk that just died is not a backup.
func TestAScheduledBackupIsCopiedToTheRemote(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, nil)

	h.c.scheduledBackup(h.ctx)
	entries, err := backup.List(filepath.Join(h.onDisk(), backup.DefaultDirName))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the schedule took %d backups, %v", len(entries), err)
	}
	// The loop nudges rather than uploading inside TakeBackup, so that an
	// operator pressing the button is not holding a browser open for the
	// length of a transfer. The pass is what sends it.
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("ShipBackups: %v", err)
	}

	keys := fake.Keys()
	if len(keys) != 1 || keys[0] != "fleet/"+entries[0].ID+".tar.gz" {
		t.Fatalf("the bucket holds %v, wanted the backup that was just taken", keys)
	}
	status := h.c.BackupRemotes(h.ctx)
	if len(status) != 1 {
		t.Fatalf("the page would show %d remotes", len(status))
	}
	if status[0].LastError != "" || status[0].LastUploadID != entries[0].ID || status[0].Copies != 1 {
		t.Errorf("the remote reports %+v after a successful copy", status[0])
	}

	// And a second pass sends nothing: the bucket already holds it, and a
	// remote that re-uploaded every backup every hour would cost money for
	// nothing.
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the second pass: %v", err)
	}
	if fake.Puts() != 1 {
		t.Errorf("the bucket was written to %d times for one backup", fake.Puts())
	}
}

// A remote that was unreachable while two backups were taken is two behind,
// and the pass that finds it working again has to send both. A fleet whose
// offsite copy silently skips the nights the bucket was down has a gap nobody
// knows about.
func TestARemoteThatWasDownIsCaughtUpWithEveryBackupItMissed(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, nil)
	fake.BreakWith(403, "AccessDenied", "the policy says no")

	root := filepath.Join(h.onDisk(), backup.DefaultDirName)
	for range 2 {
		h.c.scheduledBackup(h.ctx)
		if _, err := h.c.ShipBackups(h.ctx); err == nil {
			t.Fatal("the pass reported success against a bucket that refused it")
		}
		h.offset.Add(int64(2 * time.Hour))
		// The backup's name carries the clock's second, so two must not land
		// inside one.
		time.Sleep(1100 * time.Millisecond)
	}

	entries, err := backup.List(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("the schedule took %d backups while the bucket was down, %v", len(entries), err)
	}
	// The failure is on the page and in the drawer, not only in a log line.
	if status := h.c.BackupRemotes(h.ctx); status[0].LastError == "" {
		t.Error("a remote that is refusing every request reports no error")
	}
	codes := []string{}
	for _, p := range h.c.remoteProblems(h.ctx) {
		codes = append(codes, p.Code)
	}
	if len(codes) != 1 || codes[0] != "backup.remote_failed" {
		t.Errorf("the problems drawer says %v about a failing remote", codes)
	}

	fake.Mend()
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the pass after the bucket came back: %v", err)
	}
	if got := fake.Count(); got != 2 {
		t.Errorf("the bucket holds %d copies; the catch-up was meant to send both backups it missed", got)
	}
	if status := h.c.BackupRemotes(h.ctx); status[0].LastError != "" {
		t.Errorf("the remote still reports %q after a pass that worked", status[0].LastError)
	}
}

// A remote's own retention is its own: a fleet may keep a week on the disk and
// a year in the bucket, which is most of why the bucket is there.
func TestTheRemoteKeepsItsOwnNumberOfCopies(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, func(r *config.BackupRemote) { r.Keep = 1 })

	for range 2 {
		h.c.scheduledBackup(h.ctx)
		if _, err := h.c.ShipBackups(h.ctx); err != nil {
			t.Fatalf("ShipBackups: %v", err)
		}
		h.offset.Add(int64(2 * time.Hour))
		time.Sleep(1100 * time.Millisecond)
	}

	if got := fake.Count(); got != 1 {
		t.Fatalf("the bucket holds %d copies with keep: 1", got)
	}
	entries, err := backup.List(filepath.Join(h.onDisk(), backup.DefaultDirName))
	if err != nil || len(entries) != 2 {
		t.Fatalf("the local directory holds %d backups, %v -- the remote's ceiling is not the fleet's", len(entries), err)
	}
}

// Nothing leaves the host unless somebody says where it goes. The default
// configuration has no remotes, and a pass over none must be a pass that does
// nothing rather than an error somebody has to read.
func TestAFleetWithNoRemoteShipsNothingAndSaysSo(t *testing.T) {
	h := newHarness(t)
	sent, err := h.c.ShipBackups(h.ctx)
	if err != nil || len(sent) != 0 {
		t.Errorf("a fleet with no remotes shipped %d copies, %v", len(sent), err)
	}
	if remotes := h.c.BackupRemotes(h.ctx); len(remotes) != 0 {
		t.Errorf("the page would show %d remotes on a fleet with none", len(remotes))
	}
	// Nothing in the drawer, either: "these copies never leave the host" is
	// the validator's info finding, which the drawer drops on purpose, and
	// the Backups page says it in its own words beside the button that fixes
	// it.
	if problems := h.c.remoteProblems(h.ctx); len(problems) != 0 {
		t.Errorf("a fleet with no remotes raised %+v", problems)
	}
}

// A copy pulled back out of a bucket must survive local retention. It is here
// because the local copies are gone or suspect, and deleting it on the next
// pass because the directory is one over its ceiling would be the cruellest
// possible moment to enforce one.
func TestACopyFetchedBackIsNeverRemovedByRetention(t *testing.T) {
	h := newHarness(t)
	offsite(h, nil)
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 1 })

	h.c.scheduledBackup(h.ctx)
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("ShipBackups: %v", err)
	}
	root := filepath.Join(h.onDisk(), backup.DefaultDirName)
	entries, err := backup.List(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("setting up: %d backups, %v", len(entries), err)
	}
	id := entries[0].ID
	if err := backup.Delete(root, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	remote, err := h.c.RemoteBackup(h.ctx, "offsite")
	if err != nil {
		t.Fatalf("RemoteBackup: %v", err)
	}
	if _, err := remote.Fetch(h.ctx, root, id, backup.FetchOptions{TakenBy: "alice", Now: h.c.Now}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// A new scheduled backup, and retention behind it, must leave the fetched
	// copy alone even though keep is 1.
	h.offset.Add(int64(2 * time.Hour))
	time.Sleep(1100 * time.Millisecond)
	h.c.scheduledBackup(h.ctx)

	entries, err = backup.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			if e.Source != backup.SourceFetched {
				t.Errorf("the fetched copy says it came from %q", e.Source)
			}
		}
	}
	if !found {
		t.Error("retention removed the copy that was brought back from offsite")
	}
}

// A stored destination whose secrets this controller's key does not open is
// the shape a database restored onto the wrong host takes. It must not be
// tried every hour and refused by the service -- which reads as the bucket's
// fault -- and it must not be silent either.
func TestAStoredDestinationThisKeyCannotOpenIsReportedRatherThanTried(t *testing.T) {
	h := newHarness(t)
	fake := backup.NewFakeS3("backups")
	t.Cleanup(fake.Close)

	row := &store.BackupRemote{
		Name: "offsite", Endpoint: fake.Endpoint(), Bucket: fake.Bucket(),
		AccessKeyID: "AKIAEXAMPLE", Enabled: true,
	}
	if err := h.st.CreateBackupRemote(h.ctx, row); err != nil {
		t.Fatalf("CreateBackupRemote: %v", err)
	}
	// Sealed with somebody else's key, which is exactly what a restored
	// database carries when the key file was left behind.
	other, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sealed, err := other.SealString("secret")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	if err := h.st.SetBackupRemoteSecrets(h.ctx, row.ID, sealed, nil); err != nil {
		t.Fatalf("SetBackupRemoteSecrets: %v", err)
	}

	statuses := h.c.BackupRemotes(h.ctx)
	if len(statuses) != 1 || statuses[0].Problem == "" {
		t.Fatalf("the page shows %+v; it should carry the reason", statuses)
	}
	codes := []string{}
	for _, p := range h.c.remoteProblems(h.ctx) {
		codes = append(codes, p.Code)
	}
	if !slices.Contains(codes, "backup.remote_unreadable") {
		t.Errorf("the problems drawer says %v about a destination whose secrets will not open", codes)
	}

	h.c.scheduledBackup(h.ctx)
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("ShipBackups: %v", err)
	}
	if fake.Puts() != 0 {
		t.Error("a destination whose secret will not open was used anyway")
	}
}

// A stored destination the file names too is ignored, and says so. The file
// wins because it is the copy a host that has lost its database can read.
func TestTheFileWinsOverAStoredDestinationOfTheSameName(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, nil)

	row := &store.BackupRemote{
		Name: "offsite", Endpoint: "https://elsewhere.example.com", Bucket: "other",
		AccessKeyID: "AKIAEXAMPLE", Enabled: true,
	}
	if err := h.st.CreateBackupRemote(h.ctx, row); err != nil {
		t.Fatalf("CreateBackupRemote: %v", err)
	}

	statuses := h.c.BackupRemotes(h.ctx)
	if len(statuses) != 2 {
		t.Fatalf("the page shows %d destinations; the shadowed one is listed rather than hidden", len(statuses))
	}
	if statuses[0].Source != RemoteSourceFile || !statuses[1].Shadowed {
		t.Fatalf("the page shows %+v", statuses)
	}

	// And the copies go to the file's bucket, not the row's.
	h.c.scheduledBackup(h.ctx)
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("ShipBackups: %v", err)
	}
	if fake.Count() != 1 {
		t.Errorf("the file's bucket holds %d copies", fake.Count())
	}
}

// A destination added on the page gets the same two warnings as one in the
// file. Where a fleet chose to describe its offsite copies must not decide
// whether it is told they are readable by whoever owns the bucket.
func TestAStoredDestinationIsWarnedAboutLikeAFileOne(t *testing.T) {
	h := newHarness(t)
	row := &store.BackupRemote{
		Name: "offsite", Endpoint: "http://s3.example.com", Bucket: "acme",
		AccessKeyID: "AKIAEXAMPLE", Enabled: true,
	}
	if err := h.st.CreateBackupRemote(h.ctx, row); err != nil {
		t.Fatalf("CreateBackupRemote: %v", err)
	}

	codes := map[string]bool{}
	for _, p := range h.c.remoteProblems(h.ctx) {
		codes[p.Code] = true
	}
	if !codes["backup.remote_plaintext"] {
		t.Error("a stored destination with no passphrase was not warned about")
	}
	if !codes["backup.remote_insecure"] {
		t.Error("a stored destination reached over plain HTTP was not warned about")
	}

	// Loopback is a developer's MinIO: the credentials never cross a network,
	// so that one is not a warning.
	row.Endpoint = "http://127.0.0.1:9000"
	if err := h.st.UpdateBackupRemote(h.ctx, row); err != nil {
		t.Fatalf("UpdateBackupRemote: %v", err)
	}
	for _, p := range h.c.remoteProblems(h.ctx) {
		if p.Code == "backup.remote_insecure" {
			t.Error("a destination on loopback was warned about as if it crossed a network")
		}
	}
}

// The copy leaves the machine because a backup was taken, not because
// somebody pressed a button afterwards.
//
// The loop takes the backup and the backup nudges the loop, so the nudge lands
// in a channel the pass it came from has already read. It used to sit there
// until a later tick happened to pick it up, and a fleet whose remotes were
// reconciled five minutes ago would sit on the night's backup for as long as
// that took. This runs one pass with the remote freshly reconciled -- so
// nothing but the nudge can start an upload -- and expects the bucket to hold
// what the pass took.
func TestABackupTakenByThePassIsShippedByTheSamePass(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, nil)

	// Reconcile once, so remotesDue is false for the next hour and the only
	// thing that can ship in the pass below is the backup it takes.
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the first pass: %v", err)
	}
	if h.c.remotesDue(h.ctx, h.c.Now()) {
		t.Fatal("setting up: the remote is still due, so this proves nothing")
	}

	h.c.backupPass(h.ctx, false)

	entries, err := backup.List(filepath.Join(h.onDisk(), backup.DefaultDirName))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the pass took %d backups, %v", len(entries), err)
	}
	keys := fake.Keys()
	if len(keys) != 1 || keys[0] != "fleet/"+entries[0].ID+".tar.gz" {
		t.Fatalf("the bucket holds %v after the pass that took %s", keys, entries[0].ID)
	}
}

// twoOffsite is two destinations on one fake bucket, the shape whose second
// half an operator later deletes or switches off.
func twoOffsite(h *harness, adjustSpare func(*config.BackupRemote)) {
	h.t.Helper()
	fake := offsite(h, nil)
	spare := config.BackupRemote{
		Name: "spare", Endpoint: fake.Endpoint(), Bucket: fake.Bucket(), Prefix: "spare",
		AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret",
	}
	if adjustSpare != nil {
		adjustSpare(&spare)
	}
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Remotes = append(c.Backup.Remotes, spare) })
}

// remoteStateNames is the destinations the controller is holding state for.
func remoteStateNames(h *harness) []string {
	h.c.backups.mu.Lock()
	defer h.c.backups.mu.Unlock()
	var names []string
	for name := range h.c.backups.remotes {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// A remote that has left the configuration must take its state with it. Its
// last attempt is never refreshed again -- nothing ships to it -- so once it is
// an hour old the loop reconciles on every tick, and each of those passes
// lists every bucket that is left, which is a request that costs money in
// some of them and is what the hourly sweep exists to avoid.
func TestARemovedRemoteDoesNotKeepTheLoopListingEveryBucket(t *testing.T) {
	h := newHarness(t)
	twoOffsite(h, nil)
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the first pass: %v", err)
	}
	if names := remoteStateNames(h); !slices.Equal(names, []string{"offsite", "spare"}) {
		t.Fatalf("setting up: state is held for %v", names)
	}

	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Remotes = c.Backup.Remotes[:1] })
	h.advance(50 * time.Minute)
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the pass after the removal: %v", err)
	}
	if names := remoteStateNames(h); !slices.Equal(names, []string{"offsite"}) {
		t.Errorf("state is still held for %v after spare left the configuration", names)
	}

	// The survivor was reconciled twenty minutes ago; only what was left of
	// the removed one is more than an hour old.
	h.advance(20 * time.Minute)
	if h.c.remotesDue(h.ctx, h.c.Now()) {
		t.Error("the loop is due on every tick because of a remote that no longer exists")
	}
}

// A switched-off remote is still on the page, and the page shows what a
// listing last found there, so its state stays -- but a listing has no
// attempt time, and that must not be read as a remote nobody has tried.
func TestAListedDisabledRemoteKeepsItsCountsWithoutMakingTheLoopDue(t *testing.T) {
	h := newHarness(t)
	twoOffsite(h, func(r *config.BackupRemote) { r.Disabled = true })
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the first pass: %v", err)
	}

	h.c.NoteRemoteListing("spare", 3, 4096)
	if h.c.remotesDue(h.ctx, h.c.Now()) {
		t.Error("listing a switched-off remote made the loop reconcile on every tick")
	}

	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the second pass: %v", err)
	}
	for _, status := range h.c.BackupRemotes(h.ctx) {
		if status.Name == "spare" && status.Copies != 3 {
			t.Errorf("the page shows %d copies for the switched-off remote, wanted the 3 its listing found", status.Copies)
		}
	}
}

// The stored half of the configuration comes from the database. A read that
// fails leaves only the file's remotes to compare against, and every stored
// remote's state would look abandoned; wiping it would blank the page and the
// problems drawer for an outage that has nothing to do with them.
func TestAFailedStoreReadDoesNotWipeTheStateOfStoredRemotes(t *testing.T) {
	h := newHarness(t)
	offsite(h, nil)
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the first pass: %v", err)
	}
	h.c.NoteRemoteListing("stored", 2, 1024)

	if err := h.st.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("the pass with the store down: %v", err)
	}
	if names := remoteStateNames(h); !slices.Equal(names, []string{"offsite", "stored"}) {
		t.Errorf("state is held for %v after a pass that could not read the stored remotes", names)
	}
}

// A nudge that arrives while somebody is pressing "copy offsite now" is a
// backup that has just been taken. Dropping it would leave that copy waiting
// for the hourly sweep, so the pass that cannot run puts it back.
func TestANudgeRefusedByAPassAlreadyRunningIsKept(t *testing.T) {
	h := newHarness(t)
	offsite(h, nil)

	if !h.c.beginShipping() {
		t.Fatal("setting up: shipping was already held")
	}
	h.c.nudgeRemotes()
	h.c.shipScheduled(h.ctx)
	h.c.endShipping()

	if !h.c.tookNudge() {
		t.Error("the nudge was dropped, so the backup it stood for waits for the hourly sweep")
	}
}

// Retention runs when a backup is taken, which is the wrong moment for an
// operator who has just lowered the number kept: with backup.interval off,
// nothing happens ever. This is that button, and it has to reach the buckets
// too -- both numbers are set on the same page.
func TestRetentionCanBeAppliedWithoutTakingABackup(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, func(r *config.BackupRemote) { r.Keep = 1 })
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 0 })

	for range 3 {
		h.c.scheduledBackup(h.ctx)
		h.offset.Add(int64(2 * time.Hour))
		// The name carries the clock's second, so two backups must not land
		// in the same one.
		time.Sleep(1100 * time.Millisecond)
	}
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("ShipBackups: %v", err)
	}
	root := filepath.Join(h.onDisk(), backup.DefaultDirName)
	before, err := backup.List(root)
	if err != nil || len(before) != 3 {
		t.Fatalf("setting up: %d backups, %v", len(before), err)
	}
	// keep: 1 on the remote means the pass above already pruned it; what is
	// under test is the local half and that the bucket is not disturbed.
	if got := fake.Count(); got != 1 {
		t.Fatalf("setting up: the bucket holds %d copies", got)
	}

	// The ceiling is lowered, and nothing has happened yet: that is the whole
	// complaint this route answers.
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 1 })
	if entries, _ := backup.List(root); len(entries) != 3 {
		t.Fatalf("lowering backup.keep removed %d backups by itself", 3-len(entries))
	}

	report, err := h.c.PruneBackups(h.ctx)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if report.Keep != 1 || len(report.Removed) != 2 {
		t.Errorf("the report says keep %d, removed %v", report.Keep, report.Removed)
	}
	after, err := backup.List(root)
	if err != nil || len(after) != 1 {
		t.Fatalf("after pruning: %d backups, %v", len(after), err)
	}
	if after[0].ID != before[0].ID {
		t.Errorf("retention kept %s; the newest was %s", after[0].ID, before[0].ID)
	}
	if len(report.Remotes) != 1 || report.Remotes[0].Name != "offsite" || report.Remotes[0].Error != "" {
		t.Errorf("the report's remotes read %+v", report.Remotes)
	}
	if got := fake.Count(); got != 1 {
		t.Errorf("the bucket holds %d copies after a prune that should have left it alone", got)
	}
}

// The other half of the same button: a destination holding more than it is
// meant to is pruned without a backup being taken either.
func TestRetentionOnDemandReachesTheBuckets(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, nil)
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 0 })

	for range 3 {
		h.c.scheduledBackup(h.ctx)
		h.offset.Add(int64(2 * time.Hour))
		time.Sleep(1100 * time.Millisecond)
	}
	if _, err := h.c.ShipBackups(h.ctx); err != nil {
		t.Fatalf("ShipBackups: %v", err)
	}
	if got := fake.Count(); got != 3 {
		t.Fatalf("setting up: the bucket holds %d copies", got)
	}

	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Remotes[0].Keep = 1 })
	report, err := h.c.PruneBackups(h.ctx)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if len(report.Remotes) != 1 || len(report.Remotes[0].Removed) != 2 {
		t.Fatalf("the report's remotes read %+v", report.Remotes)
	}
	if got := fake.Count(); got != 1 {
		t.Errorf("the bucket holds %d copies after keep was lowered to 1", got)
	}
	// And the page's own figure is the one the operator just changed, rather
	// than the count from before the prune.
	status := h.c.BackupRemotes(h.ctx)
	if len(status) != 1 || status[0].Copies != 1 {
		t.Errorf("the page would say the bucket holds %+v", status)
	}
	// The local directory keeps every copy: the two ceilings are separate,
	// and a fleet with three here and one offsite is a correct fleet.
	if entries, _ := backup.List(filepath.Join(h.onDisk(), backup.DefaultDirName)); len(entries) != 3 {
		t.Errorf("pruning the bucket removed local backups too: %d left", len(entries))
	}
}

// Retention deletes copies, and two other things in this controller also move
// them: a backup being written, and an offsite pass sending one. Pruning
// beside either could count a directory that is not finished or delete the
// archive being uploaded, so it refuses and says which one to wait for.
func TestRetentionOnDemandWaitsForABackupAndForAnOffsitePass(t *testing.T) {
	h := newHarness(t)
	offsite(h, nil)

	if !h.c.beginBackup() {
		t.Fatal("setting up: a backup was already being taken")
	}
	_, err := h.c.PruneBackups(h.ctx)
	h.c.endBackup()
	if !errors.Is(err, ErrBackupRunning) {
		t.Errorf("pruning beside a backup returned %v", err)
	}

	if !h.c.beginShipping() {
		t.Fatal("setting up: a pass was already running")
	}
	report, err := h.c.PruneBackups(h.ctx)
	h.c.endShipping()
	if !errors.Is(err, ErrShippingRunning) {
		t.Errorf("pruning beside an offsite pass returned %v", err)
	}
	// The local half still ran: it is done before the buckets are touched,
	// and it is the half that has nothing to do with them.
	if report.Keep != h.c.cfg().Backup.Keep {
		t.Errorf("the report says keep %d", report.Keep)
	}
}

// A bucket that refuses is recorded against its own name, and the copies on
// this host are pruned anyway. A fleet whose retention stopped at the first
// wrong credential would keep every copy it has and never say why.
func TestRetentionOnDemandRecordsABucketThatRefuses(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, func(r *config.BackupRemote) { r.Keep = 1 })
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 0 })

	for range 2 {
		h.c.scheduledBackup(h.ctx)
		h.offset.Add(int64(2 * time.Hour))
		time.Sleep(1100 * time.Millisecond)
	}
	fake.BreakWith(403, "AccessDenied", "the policy says no")

	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 1 })
	report, err := h.c.PruneBackups(h.ctx)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if len(report.Removed) != 1 {
		t.Errorf("the local half removed %v while the bucket was refusing", report.Removed)
	}
	if len(report.Remotes) != 1 || report.Remotes[0].Error == "" {
		t.Fatalf("the report says %+v about a bucket that refused", report.Remotes)
	}
	// internal/backup words the refusal rather than passing the service's
	// code through, so a policy that is too narrow reads as the permissions
	// it is missing rather than as "AccessDenied".
	if !strings.Contains(report.Remotes[0].Error, "s3:ListBucket") {
		t.Errorf("the refusal reads %q, which does not say what the credential needs", report.Remotes[0].Error)
	}
	// And the page carries it, so the drawer has something to raise.
	if status := h.c.BackupRemotes(h.ctx); len(status) != 1 || status[0].LastError == "" {
		t.Errorf("the page would say %+v about the destination that refused", status)
	}
}

// The default fleet has no destinations at all, and pruning it is the local
// half alone rather than an error somebody has to read.
func TestRetentionOnDemandOnAFleetWithNoDestinations(t *testing.T) {
	h := newHarness(t)
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Interval = time.Hour })

	for range 2 {
		h.c.scheduledBackup(h.ctx)
		h.offset.Add(int64(2 * time.Hour))
		time.Sleep(1100 * time.Millisecond)
	}
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 1 })

	report, err := h.c.PruneBackups(h.ctx)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if len(report.Removed) != 1 || report.Error != "" {
		t.Errorf("the report reads %+v", report)
	}
	if len(report.Remotes) != 0 {
		t.Errorf("a fleet with no destinations reported %+v", report.Remotes)
	}
}

// A backup directory that cannot be read is the report's own error rather
// than a pass that claims to have removed nothing.
func TestRetentionOnDemandSaysWhyTheDirectoryRefused(t *testing.T) {
	h := newHarness(t)
	root := filepath.Join(h.onDisk(), backup.DefaultDirName)
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Interval = time.Hour })
	h.c.scheduledBackup(h.ctx)
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 1 })

	// A file where the directory should be: List cannot read it, and the
	// operator is told that rather than "nothing to remove".
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("clearing the directory: %v", err)
	}
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("writing the file in its place: %v", err)
	}

	report, err := h.c.PruneBackups(h.ctx)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if report.Error == "" {
		t.Error("a directory that could not be read reported no error")
	}
	if len(report.Removed) != 0 {
		t.Errorf("it also claims to have removed %v", report.Removed)
	}
}

// A destination this controller cannot even build a client for -- an endpoint
// that is not a URL, which the startup validator would have caught but a
// stored row edited by hand would not -- is recorded against its own name.
// The others are still pruned: one malformed row must not stop retention.
func TestRetentionOnDemandRecordsADestinationItCannotBuild(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, nil)
	h.c.UpdateConfig(func(c *config.Config) {
		c.Backup.Keep = 0
		c.Backup.Remotes = append(c.Backup.Remotes, config.BackupRemote{
			Name: "broken", Endpoint: "://not-a-url", Bucket: fake.Bucket(),
			AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret",
		})
	})

	for range 2 {
		h.c.scheduledBackup(h.ctx)
		h.offset.Add(int64(2 * time.Hour))
		time.Sleep(1100 * time.Millisecond)
	}
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Keep = 1 })

	report, err := h.c.PruneBackups(h.ctx)
	if err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}
	if len(report.Removed) != 1 {
		t.Errorf("the local half removed %v", report.Removed)
	}
	var broken *RemotePruning
	for i, r := range report.Remotes {
		if r.Name == "broken" {
			broken = &report.Remotes[i]
		}
	}
	if broken == nil || broken.Error == "" {
		t.Fatalf("the report says %+v about a destination that cannot be built", report.Remotes)
	}
	if len(report.Remotes) != 2 {
		t.Errorf("the working destination was dropped from the pass: %+v", report.Remotes)
	}
}

// The loop's quiet pass: nothing is due, nothing has been asked for, and
// there is nowhere for a copy to go. It has to be a pass that does nothing
// rather than one that takes a backup or reaches for a bucket.
func TestAQuietPassTakesNothingAndSendsNothing(t *testing.T) {
	h := newHarness(t)
	root := filepath.Join(h.onDisk(), backup.DefaultDirName)
	// The schedule off, which is what backup.interval 0 means, and no
	// destinations -- the default fleet.
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Interval = 0 })

	h.c.backupPass(h.ctx, false)

	if entries, err := backup.List(root); err == nil && len(entries) != 0 {
		t.Errorf("a pass with the schedule off took %d backups", len(entries))
	}
	if remotes := h.c.BackupRemotes(h.ctx); len(remotes) != 0 {
		t.Errorf("a fleet with no destinations reported %+v", remotes)
	}
}

// And the noisy one: a pass whose bucket refuses leaves the failure on the
// destination rather than raising it out of the loop, which has nobody to
// tell. The page and the drawer are where it is read.
func TestAScheduledPassRecordsABucketThatRefuses(t *testing.T) {
	h := newHarness(t)
	fake := offsite(h, nil)
	fake.BreakWith(500, "InternalError", "the service is having a moment")

	h.c.scheduledBackup(h.ctx)
	h.c.shipScheduled(h.ctx)

	status := h.c.BackupRemotes(h.ctx)
	if len(status) != 1 || status[0].LastError == "" {
		t.Errorf("the page would say %+v about a destination that refused every request", status)
	}
}

// A destination added on the Backups page has to be shipped to by the
// schedule, exactly like one in zoomies.yaml.
//
// The loop's own pass used to ask the configuration file whether there was
// anywhere to send a copy, and a destination an operator typed into the page
// is a database row the file knows nothing about. So the schedule took its
// backups every night, the page showed a healthy destination with a button
// that worked when pressed, and nothing ever left the machine on its own --
// the worst shape this can fail in, because everything visible said it was
// working.
func TestTheScheduleShipsToADestinationAddedOnThePage(t *testing.T) {
	h := newHarness(t)
	fake := backup.NewFakeS3("backups")
	t.Cleanup(fake.Close)

	row := &store.BackupRemote{
		Name: "offsite", Endpoint: fake.Endpoint(), Bucket: fake.Bucket(), Prefix: "fleet",
		AccessKeyID: "AKIAEXAMPLE", Enabled: true,
	}
	if err := h.st.CreateBackupRemote(h.ctx, row); err != nil {
		t.Fatalf("CreateBackupRemote: %v", err)
	}
	sealed, err := h.key.SealString("secret")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	if err := h.st.SetBackupRemoteSecrets(h.ctx, row.ID, sealed, nil); err != nil {
		t.Fatalf("SetBackupRemoteSecrets: %v", err)
	}
	h.c.UpdateConfig(func(c *config.Config) { c.Backup.Interval = time.Hour })

	// One whole turn of the loop: the schedule takes the copy, and the same
	// pass sends it. Nothing here presses a button.
	h.c.backupPass(h.ctx, false)

	entries, err := backup.List(filepath.Join(h.onDisk(), backup.DefaultDirName))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the pass took %d backups, %v", len(entries), err)
	}
	keys := fake.Keys()
	if len(keys) != 1 || keys[0] != "fleet/"+entries[0].ID+".tar.gz" {
		t.Fatalf("the bucket holds %v after the pass that took %s", keys, entries[0].ID)
	}
	if status := h.c.BackupRemotes(h.ctx); len(status) != 1 || status[0].LastUploadID != entries[0].ID {
		t.Errorf("the page reports %+v after the schedule copied a backup offsite", status)
	}
}
