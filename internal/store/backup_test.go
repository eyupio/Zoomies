package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// onDiskStore is a real file rather than :memory:, because everything backup
// and restore is about happens to a file.
func onDiskStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zoomies.db")
	s, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// A backup is only a backup if the copy opens on its own. The documentation
// used to say to copy the database file, which on a running instance leaves
// every recent commit behind in the write-ahead log; VACUUM INTO is what makes
// the copy whole and self-contained.
func TestABackupOpensOnItsOwnAndHasTheRows(t *testing.T) {
	ctx := context.Background()
	s, _ := onDiskStore(t)
	inst := &Installation{AppID: 7, InstallationID: 9, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "backup", "copy.db")
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// No write-ahead log beside it: that is what "self-contained" means, and a
	// copy with a WAL is one that needs the other two files to be whole.
	for _, side := range []string{dest + "-wal", dest + "-shm"} {
		if _, err := os.Stat(side); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the copy has a %s beside it", filepath.Base(side))
		}
	}

	copied, err := Open(ctx, Options{Path: dest, ReadOnly: true})
	if err != nil {
		t.Fatalf("opening the copy: %v", err)
	}
	defer copied.Close()
	if err := copied.IntegrityCheck(ctx); err != nil {
		t.Errorf("the copy does not pass its own integrity check: %v", err)
	}
	got, err := copied.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatalf("the installation is not in the copy: %v", err)
	}
	if got.Target != "acme" {
		t.Errorf("target = %q, want acme", got.Target)
	}
}

// The copy holds every sealed secret and every password hash this instance
// has. VACUUM INTO creates it with the process umask, which on a stock image
// is world-readable.
func TestABackupIsReadableOnlyByItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are a POSIX question")
	}
	ctx := context.Background()
	s, _ := onDiskStore(t)
	dest := filepath.Join(t.TempDir(), "copy.db")
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the backup is mode %04o; want 0600", perm)
	}
}

// The destination of a backup is nearly always a name derived from the date,
// and the one time it is not, the file already there is somebody's older
// backup. Overwriting it on a typo is the failure worth refusing.
func TestABackupNeverOverwritesOne(t *testing.T) {
	ctx := context.Background()
	s, _ := onDiskStore(t)
	dest := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(dest, []byte("somebody's older backup"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	err := s.Backup(ctx, dest)
	if err == nil {
		t.Fatal("the backup overwrote an existing file")
	}
	kept, rerr := os.ReadFile(dest)
	if rerr != nil || string(kept) != "somebody's older backup" {
		t.Errorf("the existing file was changed: %q, %v", kept, rerr)
	}
}

// Opening read-only must not migrate, because migrating is a write and the
// caller asked to look rather than to touch. A backup being verified has to
// come back exactly as it was written.
func TestAReadOnlyOpenRefusesWritesAndLeavesTheFileAlone(t *testing.T) {
	ctx := context.Background()
	s, path := onDiskStore(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	ro, err := Open(ctx, Options{Path: path, ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only Open: %v", err)
	}
	defer ro.Close()

	if err := ro.CreateInstallation(ctx, &Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("a write on a read-only store returned %v; want ErrReadOnly", err)
	}
	// The refusal names the decision rather than the file, because "attempt to
	// write a readonly database" sends the reader to check permissions.
	if err := ro.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if after.ModTime() != before.ModTime() || after.Size() != before.Size() {
		t.Error("a read-only open changed the file")
	}
}

// Rolling a release back is a thing operators do under pressure, and an older
// binary against a newer schema reads columns whose meaning it does not know
// and writes rows the newer one will not accept -- silently. This is the
// moment to say the database went forward with the release.
func TestOpeningADatabaseFromANewerBuildIsRefused(t *testing.T) {
	ctx := context.Background()
	s, path := onDiskStore(t)
	if _, err := s.exec(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		"9999_from_the_future.sql", ms(s.Now())); err != nil {
		t.Fatalf("planting a future migration: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, ro := range []bool{false, true} {
		_, err := Open(ctx, Options{Path: path, ReadOnly: ro})
		if !errors.Is(err, ErrSchemaNewer) {
			t.Errorf("read_only=%v: Open returned %v; want ErrSchemaNewer", ro, err)
		}
		// Naming the migration is the difference between "upgrade something"
		// and knowing which release to go back to.
		if err != nil && !strings.Contains(err.Error(), "9999_from_the_future.sql") {
			t.Errorf("read_only=%v: the refusal does not name the migration: %v", ro, err)
		}
	}
}

// The question this answers is asked once, at startup, before deciding whether
// a new encryption key may be generated.
func TestHasSealedSecretsSeesTheOneThingAKeyOpens(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	has, err := s.HasSealedSecrets(ctx)
	if err != nil {
		t.Fatalf("HasSealedSecrets: %v", err)
	}
	if has {
		t.Error("a fresh database reports sealed secrets, so a first run would refuse to generate a key")
	}

	inst := &Installation{
		AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg,
		PrivateKeyEnc: []byte("sealed"),
	}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	has, err = s.HasSealedSecrets(ctx)
	if err != nil {
		t.Fatalf("HasSealedSecrets: %v", err)
	}
	if !has {
		t.Error("an installation with a sealed private key does not count as a sealed secret")
	}
}

// Every column the instance key seals has to count, or a database whose only
// sealed values are one of these gets a silently generated replacement key:
// every authenticator secret then stops opening, and an offsite passphrase is
// lost along with the archives that were uploaded under it.
func TestHasSealedSecretsSeesTwoStepBackupRemoteAndTailcatSecrets(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, s *Store)
		want bool
	}{
		{"a confirmed authenticator secret", func(t *testing.T, s *Store) {
			u := seedTwoStepUser(t, s, "ada")
			if err := s.BeginTwoStep(ctx, u.ID, []byte("sealed")); err != nil {
				t.Fatal(err)
			}
			if err := s.ConfirmTwoStep(ctx, u.ID, 1, []string{"h"}); err != nil {
				t.Fatal(err)
			}
		}, true},
		// Nothing is signed in with it yet, and starting again replaces it, so it
		// is not worth refusing a first run for.
		{"an enrolment nobody confirmed", func(t *testing.T, s *Store) {
			u := seedTwoStepUser(t, s, "ada")
			if err := s.BeginTwoStep(ctx, u.ID, []byte("sealed")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a backup remote's secret key", func(t *testing.T, s *Store) {
			r := seedBackupRemote(t, s)
			if err := s.SetBackupRemoteSecrets(ctx, r.ID, []byte("sealed"), nil); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a backup remote's passphrase", func(t *testing.T, s *Store) {
			r := seedBackupRemote(t, s)
			if err := s.SetBackupRemoteSecrets(ctx, r.ID, nil, []byte("sealed")); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"a backup remote with no secrets", func(t *testing.T, s *Store) {
			seedBackupRemote(t, s)
		}, false},
		{"a provider's private connection address", func(t *testing.T, s *Store) {
			p := seedProvider(t, s)
			if err := s.SetProviderTailcatAddress(ctx, p.ID, []byte("sealed")); err != nil {
				t.Fatal(err)
			}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			tc.seed(t, s)
			has, err := s.HasSealedSecrets(ctx)
			if err != nil {
				t.Fatalf("HasSealedSecrets: %v", err)
			}
			if has != tc.want {
				t.Errorf("HasSealedSecrets = %v with %s, want %v", has, tc.name, tc.want)
			}
		})
	}
}

func seedBackupRemote(t *testing.T, s *Store) *BackupRemote {
	t.Helper()
	r := &BackupRemote{
		Name: "offsite", Endpoint: "https://s3.example", Bucket: "backups", Enabled: true,
	}
	if err := s.CreateBackupRemote(context.Background(), r); err != nil {
		t.Fatalf("CreateBackupRemote: %v", err)
	}
	return r
}

func TestAPrivateConnectionIdentityPreventsGeneratingAReplacementKey(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetSetting(context.Background(), "tailcat.identity.v1", "sealed-identity", true); err != nil {
		t.Fatal(err)
	}
	has, err := s.HasSealedSecrets(context.Background())
	if err != nil || !has {
		t.Fatalf("private connection identity was not protected: %v", err)
	}
}

// A backup destination is data, even when its filename resembles SQL.
func TestBackupBindsTheDestinationAsData(t *testing.T) {
	ctx := context.Background()
	s, _ := onDiskStore(t)
	inst := &Installation{AppID: 7, InstallationID: 9, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "operator's-copy'; DROP TABLE installations; --.db")
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(ctx, Options{Path: dest, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	for _, db := range []*Store{s, copy} {
		rows, err := db.ListInstallations(ctx)
		if err != nil || len(rows) != 1 || rows[0].Target != "acme" {
			t.Fatalf("backup changed data: %v %v", rows, err)
		}
	}
}
