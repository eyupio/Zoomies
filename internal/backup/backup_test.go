package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// host is a database with something sealed in it and a key file that opens
// it, which is the only configuration where the manifest has anything
// interesting to say.
func host(t *testing.T) (*config.Config, *store.Store, *cryptox.Key) {
	t.Helper()
	dir := t.TempDir()
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(dir, "state", "zoomies.db")
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cfg.Security.EncryptionKeyFile = filepath.Join(dir, "encryption.key")
	if err := cryptox.WriteKeyFile(cfg.Security.EncryptionKeyFile, key); err != nil {
		t.Fatalf("WriteKeyFile: %v", err)
	}
	st, err := store.Open(ctx, store.Options{Path: cfg.Database.Path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sealed, err := key.Seal([]byte("-----BEGIN RSA PRIVATE KEY-----\nnot really\n"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := st.CreateInstallation(ctx, &store.Installation{AppID: 7, InstallationID: 9, Target: "acme", TargetType: store.TargetOrg, PrivateKeyEnc: sealed}); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	return cfg, st, key
}

func take(t *testing.T, cfg *config.Config, st *store.Store, opts TakeOptions) *Entry {
	t.Helper()
	opts.Config = cfg
	e, err := Take(context.Background(), st, opts)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	return e
}

// The manifest is the half that is not the data, and everything a restore
// wants to know has to be in it: the build, the ledger, the key's fingerprint,
// what the key opens, and the blanked configuration.
func TestABackupCarriesWhatARestoreWillNeedToKnow(t *testing.T) {
	cfg, st, key := host(t)
	e := take(t, cfg, st, TakeOptions{Source: SourceManual, TakenBy: "alice"})

	if e.Manifest == nil {
		t.Fatalf("no manifest was read back: %+v", e)
	}
	m := e.Manifest
	if m.ManifestVersion != ManifestVersion || m.TakenAt.IsZero() || m.Source != SourceManual || m.TakenBy != "alice" {
		t.Errorf("the manifest does not say what it is, when, or who took it: %+v", m)
	}
	if m.Zoomies.Version == "" || len(m.Database.Migrations) == 0 || m.Database.Integrity != "ok" || m.Database.SHA256 == "" {
		t.Errorf("the copy was not checked, measured or attributed: %+v", m.Database)
	}
	if m.Key.Fingerprint != key.Fingerprint() || m.Key.Included {
		t.Errorf("key = %+v, want the fingerprint %s and not the key", m.Key, key.Fingerprint())
	}
	if len(m.Secrets) != 1 || m.Secrets[0].Target != "acme" {
		t.Errorf("the manifest does not say what the key opens: %+v", m.Secrets)
	}
	if !strings.Contains(string(m.Config), `"encryption_key_file"`) || strings.Contains(string(m.Config), key.Encode()) {
		t.Errorf("the configuration is missing or carries the key: %s", m.Config)
	}
	// And the directory is what List renders.
	if !ValidID(e.ID) || e.Bytes == 0 || e.TotalBytes <= e.Bytes {
		t.Errorf("entry = %+v", e)
	}
	if dir := Dir(cfg); filepath.Dir(e.Dir) != dir || dir != filepath.Join(filepath.Dir(cfg.Database.Path), DefaultDirName) {
		t.Errorf("the backup went to %s, not the default beside the database", e.Dir)
	}
}

// Pressing the button twice inside a second must produce two backups, not one
// overwriting the other and not a failure.
func TestTwoBackupsInOneSecondAreTwoBackups(t *testing.T) {
	cfg, st, _ := host(t)
	frozen := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return frozen }
	a := take(t, cfg, st, TakeOptions{Now: now})
	b := take(t, cfg, st, TakeOptions{Now: now})
	if a.ID == b.ID || !ValidID(b.ID) {
		t.Fatalf("ids %q and %q", a.ID, b.ID)
	}
	all, err := List(Dir(cfg))
	if err != nil || len(all) != 2 {
		t.Fatalf("List = %d entries, %v", len(all), err)
	}
	// Newest first, and the suffixed one sorts after the plain one.
	if all[0].ID != b.ID {
		t.Errorf("List order: %s, %s", all[0].ID, all[1].ID)
	}
}

// A directory that merely looks like a backup is not one, and an id that is
// not a name this package writes is refused before it reaches the filesystem:
// the id becomes a path component.
func TestOnlyRealBackupsAreListedAndOnlyRealIDsAreLookedUp(t *testing.T) {
	cfg, st, _ := host(t)
	take(t, cfg, st, TakeOptions{})
	root := Dir(cfg)
	if err := os.MkdirAll(filepath.Join(root, DirPrefix+"00000000-000000"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "somebody-elses"), 0o700); err != nil {
		t.Fatal(err)
	}
	all, err := List(root)
	if err != nil || len(all) != 1 {
		t.Fatalf("List = %d entries, %v", len(all), err)
	}
	for _, bad := range []string{"../zoomies.db", "zoomies-20260101-000000/../..", "", "zoomies-", "state"} {
		if _, err := Get(root, bad); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Get(%q) = %v, want ErrInvalidID", bad, err)
		}
	}
	if _, err := Get(root, DirPrefix+"00000000-000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a directory with no database in it was found: %v", err)
	}
	if _, err := Get(root, DirPrefix+"19990101-000000"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get of nothing = %v", err)
	}
}

// Retention only ever removes what the fleet took of itself: never a stranger's
// directory, and never a backup an operator brought here.
func TestPruneLeavesUploadsAndStrangersAlone(t *testing.T) {
	cfg, st, _ := host(t)
	root := Dir(cfg)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 4 {
		at := stamp.Add(time.Duration(i) * time.Minute)
		take(t, cfg, st, TakeOptions{Source: SourceScheduled, Now: func() time.Time { return at }})
	}
	// An upload, older than all of them.
	old := stamp.Add(-time.Hour)
	uploaded := take(t, cfg, st, TakeOptions{Source: SourceUploaded, Now: func() time.Time { return old }})
	strangers := filepath.Join(root, DirPrefix+"00000000-000000")
	if err := os.MkdirAll(strangers, 0o700); err != nil {
		t.Fatal(err)
	}

	removed, err := Prune(root, 2)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("removed %v, want the two oldest scheduled copies", removed)
	}
	if _, err := os.Stat(strangers); err != nil {
		t.Error("retention deleted a directory this package did not write")
	}
	if _, err := Get(root, uploaded.ID); err != nil {
		t.Errorf("retention deleted an uploaded backup: %v", err)
	}
	if removed, err := Prune(root, 0); err != nil || len(removed) != 0 {
		t.Errorf("keep 0 removed %v, %v; it means keep everything", removed, err)
	}
}

// A copy nobody has opened is a copy nobody knows about. Verify has to catch
// the file being altered after it was taken, which is the failure an operator
// finds otherwise on the day the backup is needed.
func TestVerifyNoticesABackupThatHasChangedSinceItWasTaken(t *testing.T) {
	cfg, st, _ := host(t)
	e := take(t, cfg, st, TakeOptions{})
	ctx := context.Background()

	v, err := Verify(ctx, Dir(cfg), e.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !v.OK || !v.DigestKnown || !v.DigestMatches || v.Integrity != "ok" || !v.SchemaReadable || v.Migrations == 0 {
		t.Errorf("a fresh backup does not verify: %+v", v)
	}

	f, err := os.OpenFile(filepath.Join(e.Dir, DBName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("trailing rubbish")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	v, err = Verify(ctx, Dir(cfg), e.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.OK || v.DigestMatches || len(v.Problems) == 0 {
		t.Errorf("an altered backup verified: %+v", v)
	}
}

// The archive is the form a backup leaves the machine in, and it has to come
// back as the same backup: same manifest, same database, verified on arrival,
// and named for when it was taken rather than when it was uploaded.
func TestAnArchiveRoundTripsThroughUploadUnderItsOwnName(t *testing.T) {
	cfg, st, _ := host(t)
	e := take(t, cfg, st, TakeOptions{Source: SourceManual, TakenBy: "alice"})
	var buf bytes.Buffer
	if err := WriteArchive(&buf, e); err != nil {
		t.Fatalf("WriteArchive: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "\x1f\x8b") {
		t.Error("the archive is not gzipped")
	}

	other := t.TempDir()
	got, err := Unpack(context.Background(), other, bytes.NewReader(buf.Bytes()), UnpackOptions{Source: SourceUploaded, TakenBy: "bob"})
	if err != nil {
		t.Fatalf("Unpack: %v", err)
	}
	if got.ID != e.ID {
		t.Errorf("uploaded as %s, want the original's name %s", got.ID, e.ID)
	}
	if got.Source != SourceUploaded || got.Manifest == nil || got.Manifest.TakenBy != "bob" {
		t.Errorf("the upload does not say it is one: %+v", got)
	}
	if got.Manifest.Database.SHA256 != e.Manifest.Database.SHA256 {
		t.Error("the database that arrived is not the one that left")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 1 {
		t.Errorf("a staging directory was left behind: %v", entries)
	}
	// And a second upload of the same file is a second backup.
	again, err := Unpack(context.Background(), other, bytes.NewReader(buf.Bytes()), UnpackOptions{Source: SourceUploaded})
	if err != nil || again.ID == got.ID {
		t.Errorf("second upload: %v, %v", again, err)
	}
}

// An archive is written by this package and read by this package. One that
// tries to put a file somewhere else, or carries something that is not a
// backup, is refused before anything lands.
func TestUnpackRefusesWhatItDidNotWrite(t *testing.T) {
	cases := map[string]func(*testing.T) []byte{
		"not gzip": func(t *testing.T) []byte { return []byte("hello") },
		"a member outside the backup": func(t *testing.T) []byte {
			return tarOf(t, map[string][]byte{"../../etc/passwd": []byte("root")})
		},
		"a member with the wrong name": func(t *testing.T) []byte {
			return tarOf(t, map[string][]byte{"zoomies-1/notes.txt": []byte("hi")})
		},
		// The right name at the end of the wrong path is still the wrong
		// path: the name is resolved to the package's constant, and the
		// directory the archive puts it in never reaches the filesystem.
		"a real member behind a climbing path": func(t *testing.T) []byte {
			return tarOf(t, map[string][]byte{"../../" + DBName: []byte("root")})
		},
		"a real member behind an absolute path": func(t *testing.T) []byte {
			return tarOf(t, map[string][]byte{"/tmp/" + DBName: []byte("root")})
		},
		"no database": func(t *testing.T) []byte {
			return tarOf(t, map[string][]byte{"zoomies-1/" + ManifestName: []byte("{}")})
		},
		"a database that is not one": func(t *testing.T) []byte {
			return tarOf(t, map[string][]byte{"zoomies-1/" + DBName: []byte("not sqlite")})
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			_, err := Unpack(context.Background(), root, bytes.NewReader(build(t)), UnpackOptions{})
			if err == nil {
				t.Fatal("the archive was accepted")
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Errorf("something was left in the root: %v", entries)
			}
		})
	}
}

// Encryption exists because a downloaded backup ends up on a laptop. It has
// to round-trip, refuse the wrong passphrase, and refuse a file cut short --
// the last one silently, in the sense that a truncated backup must not open
// as a shorter backup.
func TestEncryptionRoundTripsAndRefusesTheWrongPassphraseAndATruncatedFile(t *testing.T) {
	plain := bytes.Repeat([]byte("the quick brown fox "), 200_000) // a few chunks
	var enc bytes.Buffer
	w, err := NewEncryptor(&enc, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	// Written in odd-sized pieces so chunk boundaries fall mid-write.
	for i := 0; i < len(plain); i += 77_777 {
		end := min(i+77_777, len(plain))
		if _, err := w.Write(plain[i:end]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc.Bytes(), []byte("quick brown")) {
		t.Fatal("the plaintext is visible in the encrypted file")
	}
	isEnc, _ := IsEncrypted(bytes.NewReader(enc.Bytes()))
	if !isEnc {
		t.Error("IsEncrypted does not recognise its own header")
	}
	if isEnc, _ := IsEncrypted(bytes.NewReader([]byte("\x1f\x8b plain"))); isEnc {
		t.Error("IsEncrypted claims a plain file is encrypted")
	}

	r, err := NewDecryptor(bytes.NewReader(enc.Bytes()), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %v, %d bytes of %d", err, len(got), len(plain))
	}

	r, err = NewDecryptor(bytes.NewReader(enc.Bytes()), "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); !errors.Is(err, ErrWrongPassphrase) {
		t.Errorf("wrong passphrase read = %v", err)
	}

	cut := enc.Bytes()[:enc.Len()-100]
	r, err = NewDecryptor(bytes.NewReader(cut), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(r); err == nil {
		t.Error("a file cut short opened as a shorter file")
	}

	if _, err := NewDecryptor(bytes.NewReader([]byte("plain text")), "x"); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("decrypting a plain file = %v", err)
	}
}

// A staged restore is checked when it is asked for, because that is when the
// operator is looking; applied by the next start, before the store is opened;
// and recorded either way, so the page can say what happened.
func TestAStagedRestoreIsCheckedNowAndAppliedAtTheNextStart(t *testing.T) {
	cfg, st, _ := host(t)
	ctx := context.Background()
	e := take(t, cfg, st, TakeOptions{Source: SourceManual})

	// Something that happens after the backup, which the restore must undo.
	if err := st.CreateInstallation(ctx, &store.Installation{AppID: 8, InstallationID: 10, Target: "later", TargetType: store.TargetOrg}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Stage(ctx, cfg, e, Staged{RequestedBy: "alice", RevokeAPITokens: true})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	loaded, err := LoadStaged(cfg.Database.Path)
	if err != nil || loaded == nil || loaded.BackupID != e.ID || loaded.RequestedBy != "alice" || !loaded.RevokeAPITokens {
		t.Fatalf("LoadStaged = %+v, %v; staged %+v", loaded, err, s)
	}

	out, err := ApplyStaged(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("ApplyStaged: %v", err)
	}
	if out == nil || !out.OK || out.Report == nil || out.Report.MovedAside == "" {
		t.Fatalf("outcome = %+v", out)
	}
	if again, _ := LoadStaged(cfg.Database.Path); again != nil {
		t.Error("the staged restore is still waiting after being applied")
	}
	last, err := LastOutcome(cfg.Database.Path)
	if err != nil || last == nil || !last.OK || last.BackupID != e.ID {
		t.Errorf("LastOutcome = %+v, %v", last, err)
	}

	restored, err := store.Open(ctx, store.Options{Path: cfg.Database.Path})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	insts, _ := restored.ListInstallations(ctx)
	if len(insts) != 1 || insts[0].Target != "acme" {
		t.Errorf("the restored database is not the backup: %+v", insts)
	}
	if f, _ := restored.RecoveryFenced(ctx); !f.Fenced {
		t.Error("the restored fleet is not fenced")
	}
	if nothing, err := ApplyStaged(ctx, cfg, nil); nothing != nil || err != nil {
		t.Errorf("a second start applied something: %+v, %v", nothing, err)
	}
}

// A restore that fails after the copy has already replaced the database. The
// error used to say only what went wrong, and the staged path's log line then
// claimed the controller was starting on the database it had -- when it was
// starting on the restored one, half-invalidated and, if the failure came
// before the fence was written, not fenced either. The fence is the write that
// stops a restored copy deleting machines that were rented after the backup,
// so it goes first, and the error says which database is in place.
func TestARestoreThatFailsAfterTheCopySaysSoAndKeepsTheFenceUp(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// break is SQL run against the backup's own copy, standing in for the
		// disk filling or the file being busy at one particular write.
		break_    string
		wantFence bool
	}{
		{"before anything was invalidated", `DROP TABLE sessions`, true},
		{"at the last write", `CREATE TRIGGER no_audit BEFORE INSERT ON audit_events
			BEGIN SELECT RAISE(ABORT, 'disk full'); END`, true},
		{"at the fence itself", `CREATE TRIGGER no_fence BEFORE INSERT ON settings
			BEGIN SELECT RAISE(ABORT, 'disk full'); END`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, st, _ := host(t)
			e := take(t, cfg, st, TakeOptions{})
			db, err := sql.Open("sqlite", "file:"+filepath.Join(e.Dir, DBName))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.break_); err != nil {
				t.Fatalf("breaking the backup: %v", err)
			}
			db.Close()

			_, err = Restore(ctx, cfg, e.Dir, RestoreOptions{Replace: true})
			if err == nil {
				t.Fatal("the restore reported success although a step after the copy failed")
			}
			for _, want := range []string{"is now in place", ".before-restore-"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error does not say %q, so an operator cannot tell which database is live:\n%v", want, err)
				}
			}
			fenced := strings.Contains(err.Error(), "fence is up")
			if fenced != tc.wantFence {
				t.Errorf("the error says fenced=%v, want %v:\n%v", fenced, tc.wantFence, err)
			}

			restored, err := store.Open(ctx, store.Options{Path: cfg.Database.Path, ReadOnly: true})
			if err != nil {
				t.Fatalf("opening what is in place: %v", err)
			}
			defer restored.Close()
			if f, _ := restored.RecoveryFenced(ctx); f.Fenced != tc.wantFence {
				t.Errorf("the restored database is fenced=%v, want %v", f.Fenced, tc.wantFence)
			}
		})
	}
}

// The key check is the one that catches a fleet that would start, look healthy
// and fail on its first GitHub call. It has to refuse at staging time.
func TestStagingRefusesABackupSealedWithAnotherKey(t *testing.T) {
	cfg, st, _ := host(t)
	e := take(t, cfg, st, TakeOptions{})
	other, _ := cryptox.GenerateKey()
	if err := cryptox.WriteKeyFile(cfg.Security.EncryptionKeyFile, other); err != nil {
		t.Fatal(err)
	}
	_, err := Stage(context.Background(), cfg, e, Staged{})
	if err == nil || !strings.Contains(err.Error(), "not the one that sealed this backup") {
		t.Fatalf("Stage = %v", err)
	}
	if s, _ := LoadStaged(cfg.Database.Path); s != nil {
		t.Error("a refused restore was staged anyway")
	}
	if err := CancelStaged(cfg.Database.Path); err != nil {
		t.Errorf("cancelling nothing = %v", err)
	}
}
