// Package backup is one copy of the database, and everything that has to be
// true about it before it is worth keeping.
//
// It is shared by three callers that used to have one: `zoomies backup` on the
// command line, the controller's own scheduled copies, and the API the settings
// page takes a backup through. One implementation is the point. A copy taken
// automatically is restorable by exactly the command that restores one taken by
// hand, and a manifest written by the controller says the same things in the
// same fields as one written on a host where the controller will not start.
//
// A backup here is a directory: the database, copied with VACUUM INTO so that
// it is one whole file with no write-ahead log beside it, and a manifest saying
// which build wrote it, how far the schema had got, which encryption key opens
// it, what that key is needed for, and what the instance was configured to be.
// The encryption key itself is never in there unless it was asked for, because
// a backup that carries its own key is a credential rather than a copy.
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// ManifestVersion is the manifest's own shape number. It moves when a field is
// removed or renamed, not when one is added.
const ManifestVersion = 1

// The names inside one backup directory. They are fixed rather than derived
// from the instance, so a restore takes a directory and nothing else, and so an
// operator looking at an unlabelled folder can tell what it is.
const (
	// DBName and DirPrefix are the store's, because it writes this layout
	// itself before a migration: one naming for one thing, so a copy taken
	// automatically is restorable by the command that restores one taken by
	// hand.
	DBName    = store.BackupDBName
	DirPrefix = store.BackupDirPrefix

	ManifestName = "manifest.json"
	KeyName      = "encryption.key"

	// DefaultDirName is the directory beside the database where backups go
	// when nothing says otherwise. Beside the database rather than in a
	// temporary directory, because the one property a backup needs is to
	// survive the process that took it, and on a container deployment the
	// state directory is the mounted volume.
	DefaultDirName = "backups"
)

// Where a backup came from. It is recorded in the manifest so that a list of
// backups can say which were the operator's own and which the controller
// took, and so that retention can be told to leave the former alone.
const (
	SourceCLI          = "cli"
	SourceManual       = "manual"
	SourceScheduled    = "scheduled"
	SourceUploaded     = "uploaded"
	SourcePreMigration = "pre-migration"
	// SourceFetched is a copy pulled back out of a remote. It is its own
	// source rather than "uploaded" because the two are answers to different
	// questions on the day they matter: an uploaded backup is one a person
	// carried here, and a fetched one is the offsite copy of a fleet that has
	// lost the local ones.
	SourceFetched = "fetched"
)

// ErrNotFound is a backup id that names nothing in the directory.
var ErrNotFound = errors.New("backup: no such backup")

// ErrInvalidID is a backup id that is not the shape this package writes. It is
// refused before anything touches the filesystem, because the id becomes a
// path component and this is the one place a path traversal could start.
var ErrInvalidID = errors.New("backup: not a backup id")

// idPattern is the directory name Take writes: the prefix, the instant, and a
// counter when two were taken inside one second.
var idPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(DirPrefix) + `\d{8}-\d{6}(-\d+)?$`)

// ValidID reports whether id is a name this package could have written.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Manifest is what turns a copy of a database into a backup somebody can act
// on months later.
//
// Everything in it answers a question asked during a restore and nowhere else:
// which build wrote this, how far the schema had got, whether the key file in
// my hand is the right one, what the key is needed for at all, and what this
// instance was configured to be. None of it can be recovered from the database
// file itself once the instance that produced it is gone.
type Manifest struct {
	Transfer        *TransferInfo   `json:"transfer,omitempty"`
	ManifestVersion int             `json:"manifest_version"`
	TakenAt         time.Time       `json:"taken_at"`
	Source          string          `json:"source,omitempty"`
	TakenBy         string          `json:"taken_by,omitempty"`
	Zoomies         Build           `json:"zoomies"`
	Database        Database        `json:"database"`
	Key             KeyInfo         `json:"key"`
	Secrets         []Secret        `json:"secrets"`
	ConfigPath      string          `json:"config_path,omitempty"`
	Config          json.RawMessage `json:"config,omitempty"`
}

// Build is the build that took the copy. A restore has to run this release or
// a later one: the store refuses a database whose ledger names migrations the
// binary does not have.
type Build struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
	Go        string `json:"go"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// Database describes the copy itself.
type Database struct {
	SourcePath string   `json:"source_path"`
	Bytes      int64    `json:"bytes"`
	SHA256     string   `json:"sha256"`
	Integrity  string   `json:"integrity"`
	Migrations []string `json:"migrations"`
}

// KeyInfo says which key opens this database, and whether it is in the
// backup.
//
// The fingerprint is always here and the key itself is not, unless it was
// asked for. That asymmetry is the whole design: a backup that always carried
// the key would put the database and the thing that decrypts it in one file
// nobody thinks of as a credential, and a backup that said nothing about the
// key would leave an operator holding two files with no way to tell whether
// they belong together.
type KeyInfo struct {
	Fingerprint string `json:"fingerprint"`
	SourcePath  string `json:"source_path,omitempty"`
	Included    bool   `json:"included"`
	// From says where this instance's key came from, because a key passed in
	// the environment has no file to copy and an operator restoring needs to
	// know that is what they are looking for.
	From string `json:"from"`
}

// Secret is one thing in this database that only the key opens. It is a list
// of what will be unreadable without it, never of the secrets themselves.
type Secret struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	Target        string `json:"target,omitempty"`
	AppID         int64  `json:"app_id,omitempty"`
	PrivateKey    bool   `json:"private_key"`
	WebhookSecret bool   `json:"webhook_secret"`
}

// LastMigration is the newest migration the copy had taken, or "" when the
// manifest recorded none.
func (m *Manifest) LastMigration() string {
	if m == nil || len(m.Database.Migrations) == 0 {
		return ""
	}
	return m.Database.Migrations[len(m.Database.Migrations)-1]
}

// DefaultDir is where backups go for a database at dbPath when nothing says
// otherwise.
func DefaultDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), DefaultDirName)
}

// Dir resolves the backup directory the configuration names, or the default
// beside the database. A relative path in the configuration is relative to
// the database's own directory rather than to wherever the process happened
// to be started from, because "backups" in a unit file and "backups" in a
// terminal would otherwise be two different places.
func Dir(cfg *config.Config) string {
	dir := strings.TrimSpace(cfg.Backup.Directory)
	if dir == "" {
		return DefaultDir(cfg.Database.Path)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(cfg.Database.Path), dir)
	}
	return dir
}

// PreMigrationDir is where the store keeps the copies it takes before
// migrating, for a database at dbPath.
func PreMigrationDir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), store.PreMigrationDir)
}

// TakeOptions says where a copy goes and what to record about it.
type TakeOptions struct {
	// Config is the configuration the copy is of. It names the database, the
	// key, and everything the manifest records the instance as being.
	Config *config.Config
	// Dir is the directory the new backup's own directory is created in.
	// Empty is Dir(Config).
	Dir string
	// IncludeKey copies the encryption key in as well. Convenient and
	// dangerous: the copy then decrypts itself.
	IncludeKey bool
	// Source and TakenBy are recorded in the manifest.
	Source  string
	TakenBy string
	// Now is the clock; nil uses the wall clock.
	Now func() time.Time
}

// Take writes a consistent copy of st into a new timestamped directory and
// returns it, with its manifest read back.
//
// The copy is checked before it is reported, because the moment to find out
// that a backup is unreadable is while the original is still there.
func Take(ctx context.Context, st *store.Store, opts TakeOptions) (*Entry, error) {
	if opts.Config == nil {
		return nil, errors.New("backup: a configuration is needed to say what is being backed up")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	root := opts.Dir
	if root == "" {
		root = Dir(opts.Config)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("backup: creating %s: %w", root, err)
	}

	taken := now().UTC()
	dest, err := newDir(root, taken)
	if err != nil {
		return nil, err
	}
	// Anything that fails from here leaves a directory with no manifest in
	// it, which List would show as broken. Removing it is right: a half
	// backup is worse than none, because it looks like one.
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dest)
		}
	}()

	dbPath := filepath.Join(dest, DBName)
	if err := st.Backup(ctx, dbPath); err != nil {
		return nil, err
	}
	m, err := buildManifest(ctx, opts.Config, dbPath, dest, opts, taken)
	if err != nil {
		return nil, err
	}
	if err := writeManifest(dest, m); err != nil {
		return nil, err
	}
	ok = true
	return Get(root, filepath.Base(dest))
}

// newDir makes the backup's directory, named for the instant it was taken and
// suffixed when that instant is already taken: two backups inside one second
// is what pressing the button twice looks like, and the second must not fail
// or overwrite the first.
func newDir(root string, taken time.Time) (string, error) {
	base := filepath.Join(root, DirPrefix+taken.Format("20060102-150405"))
	for n := 0; n < 1000; n++ {
		dest := base
		if n > 0 {
			dest = fmt.Sprintf("%s-%d", base, n+1)
		}
		err := os.Mkdir(dest, 0o700)
		if err == nil {
			return dest, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("backup: creating %s: %w", dest, err)
		}
	}
	return "", fmt.Errorf("backup: %s and a thousand suffixes of it already exist", base)
}

// buildManifest gathers everything the restore will want to know.
func buildManifest(ctx context.Context, cfg *config.Config, dbPath, dest string, opts TakeOptions, taken time.Time) (*Manifest, error) {
	m := &Manifest{
		ManifestVersion: ManifestVersion,
		TakenAt:         taken,
		Source:          opts.Source,
		TakenBy:         opts.TakenBy,
		Zoomies: Build{
			Version: version.Version, Commit: version.Commit, BuildDate: version.Date,
			Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		},
		Database:   Database{SourcePath: cfg.Database.Path},
		ConfigPath: cfg.Path(),
		Secrets:    []Secret{},
	}

	// The copy is checked here rather than trusted, because the moment to find
	// out that a backup is unreadable is while the original is still there.
	copied, err := store.Open(ctx, store.Options{Path: dbPath, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("backup: opening the copy at %s: %w", dbPath, err)
	}
	defer func() { _ = copied.Close() }()
	if err := copied.IntegrityCheck(ctx); err != nil {
		return nil, err
	}
	m.Database.Integrity = "ok"

	applied, err := copied.AppliedMigrations(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range applied {
		m.Database.Migrations = append(m.Database.Migrations, a.Name)
	}

	info, err := os.Stat(dbPath)
	if err != nil {
		return nil, err
	}
	m.Database.Bytes = info.Size()
	// The digest is for the operator moving this file somewhere else, so a
	// copy that arrived truncated is caught before it is restored rather than
	// after.
	if m.Database.SHA256, err = FileSHA256(dbPath); err != nil {
		return nil, err
	}

	insts, err := copied.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	for _, i := range insts {
		if len(i.PrivateKeyEnc) == 0 && len(i.WebhookSecretEnc) == 0 {
			continue
		}
		m.Secrets = append(m.Secrets, Secret{
			Kind: "installation", ID: i.ID, Target: i.Target, AppID: i.AppID,
			PrivateKey: len(i.PrivateKeyEnc) > 0, WebhookSecret: len(i.WebhookSecretEnc) > 0,
		})
	}

	// The offsite destinations an administrator stored are sealed with the
	// same key, so they belong on the same list: restoring this database onto
	// a host holding a different key gives a fleet that cannot reach its own
	// backups, and the manifest is where that is said in advance.
	remotes, err := copied.ListBackupRemotes(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range remotes {
		if len(r.SecretKeyEnc) == 0 && len(r.PassphraseEnc) == 0 {
			continue
		}
		m.Secrets = append(m.Secrets, Secret{Kind: "backup_remote", ID: r.ID, Target: r.Name})
	}

	if err := describeKey(cfg, dest, opts.IncludeKey, m); err != nil {
		return nil, err
	}

	// The configuration, with every secret absent, so a restore has the
	// record of what this instance was without the backup becoming a place
	// secrets live.
	blob, err := json.Marshal(config.Redacted(cfg))
	if err != nil {
		return nil, err
	}
	m.Config = blob
	return m, nil
}

// describeKey records which key opens this database and, if asked, copies it
// in.
//
// A backup with no secrets in it needs no key, and saying so is better than an
// empty fingerprint that reads like a failure.
func describeKey(cfg *config.Config, dest string, includeKey bool, m *Manifest) error {
	if len(m.Secrets) == 0 && strings.TrimSpace(cfg.Security.EncryptionKey) == "" &&
		strings.TrimSpace(cfg.Security.EncryptionKeyFile) == "" {
		m.Key.From = "none: nothing in this database is sealed"
		return nil
	}

	var (
		key  *cryptox.Key
		err  error
		path string
	)
	switch {
	case strings.TrimSpace(cfg.Security.EncryptionKey) != "":
		key, err = cryptox.ParseKey(cfg.Security.EncryptionKey)
		m.Key.From = "the environment or zoomies.yaml"
	default:
		path = strings.TrimSpace(cfg.Security.EncryptionKeyFile)
		key, err = cryptox.LoadKeyFile(path)
		m.Key.From = "security.encryption_key_file"
		m.Key.SourcePath = path
	}
	if err != nil {
		// A backup of a database whose key cannot be read is still worth
		// taking, and is exactly the moment to say the key is missing.
		return fmt.Errorf("reading the encryption key so the backup can record which one this is: %w "+
			"(the database has been copied; put the key where security.encryption_key_file points and take the backup again)", err)
	}
	m.Key.Fingerprint = key.Fingerprint()

	if !includeKey {
		return nil
	}
	if path == "" {
		return errors.New("include the key: this instance's key comes from the environment rather than a file, " +
			"so there is nothing to copy; store it wherever you keep the rest of your secrets")
	}
	if err := cryptox.WriteKeyFile(filepath.Join(dest, KeyName), key); err != nil {
		return fmt.Errorf("copying the encryption key into the backup: %w", err)
	}
	m.Key.Included = true
	return nil
}

func writeManifest(dir string, m *Manifest) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("backup: writing the manifest: %w", err)
	}
	return nil
}

// ReadManifest reads the manifest beside a backup's database.
func ReadManifest(dir string) (*Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("backup: %s is not a manifest: %w", filepath.Join(dir, ManifestName), err)
	}
	return &m, nil
}

// FileSHA256 is the hex digest of a file's contents.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// The directory
// ---------------------------------------------------------------------------

// Entry is one backup as the directory holds it: what List renders and what
// every other operation is addressed to.
type Entry struct {
	// ID is the directory's name, and the only thing a caller needs to name
	// it by.
	ID string `json:"id"`
	// Dir is the directory itself.
	Dir string `json:"-"`
	// TakenAt is from the manifest when there is one, and from the name
	// otherwise, which is what a pre-migration copy has.
	TakenAt time.Time `json:"taken_at"`
	// Source is where it came from; SourcePreMigration for the store's own
	// copies, which have no manifest to say so.
	Source string `json:"source"`
	// Bytes is the database's size, and TotalBytes the whole directory's.
	Bytes      int64 `json:"bytes"`
	TotalBytes int64 `json:"total_bytes"`
	// KeyIncluded says the backup carries the encryption key, and is a
	// credential.
	KeyIncluded bool `json:"key_included"`
	// Manifest is nil when there is none, or none readable; Problem then
	// says which.
	Manifest *Manifest `json:"manifest,omitempty"`
	Problem  string    `json:"problem,omitempty"`
}

// List returns every backup under root, newest first. A directory that has
// the right name but no database in it is not listed: it is not a backup.
//
// A missing root is an empty list rather than an error, because a fleet that
// has never taken a backup has no directory yet and that is not a fault.
func List(root string) ([]Entry, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: reading %s: %w", root, err)
	}
	out := []Entry{}
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		entry, err := describe(root, e.Name())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *entry)
	}
	// The names carry the timestamp, so sorting them sorts by age without
	// trusting a modification time that a copy between machines rewrites.
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(b.ID, a.ID) })
	return out, nil
}

// Get returns one backup by id, or ErrNotFound.
func Get(root, id string) (*Entry, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return describe(root, id)
}

func describe(root, id string) (*Entry, error) {
	dir := filepath.Join(root, id)
	entry := &Entry{ID: id, Dir: dir, TakenAt: takenFromName(id)}
	dbInfo, dbErr := os.Stat(filepath.Join(dir, DBName))
	switch {
	case dbErr == nil:
		entry.Bytes = dbInfo.Size()
	case errors.Is(dbErr, os.ErrNotExist):
		// A directory with a manifest and no database is a backup that lost
		// its half -- listed, so retention can remove it and an operator can
		// see it, and refused by everything that would open it. One with
		// neither is not a backup at all.
		if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		entry.Problem = "the database is missing from this backup; only its manifest is left"
	default:
		return nil, fmt.Errorf("backup: reading %s: %w", dir, dbErr)
	}

	m, err := ReadManifest(dir)
	switch {
	case err == nil:
		entry.Manifest = m
		if !m.TakenAt.IsZero() {
			entry.TakenAt = m.TakenAt
		}
		entry.Source = m.Source
		if entry.Source == "" {
			entry.Source = SourceCLI
		}
		entry.KeyIncluded = m.Key.Included
	case errors.Is(err, os.ErrNotExist):
		// The store's pre-migration copies have no manifest and never did;
		// the name and the database are all there is to say about them.
		entry.Source = SourcePreMigration
	default:
		if entry.Problem == "" {
			entry.Problem = err.Error()
		}
		entry.Source = SourceCLI
	}
	if _, err := os.Stat(filepath.Join(dir, KeyName)); err == nil {
		entry.KeyIncluded = true
	}

	total := int64(0)
	if files, err := os.ReadDir(dir); err == nil {
		for _, f := range files {
			if info, err := f.Info(); err == nil && !f.IsDir() {
				total += info.Size()
			}
		}
	}
	entry.TotalBytes = total
	return entry, nil
}

// takenFromName reads the instant out of a backup's name.
func takenFromName(id string) time.Time {
	stamp := strings.TrimPrefix(id, DirPrefix)
	if i := strings.Index(stamp[9:], "-"); i >= 0 {
		stamp = stamp[:9+i]
	}
	t, err := time.Parse("20060102-150405", stamp)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Delete removes one backup. It removes only a directory this package names
// and that holds a database, so an id pointing at anything else is refused
// before anything is deleted.
func Delete(root, id string) error {
	entry, err := Get(root, id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(entry.Dir); err != nil {
		return fmt.Errorf("backup: removing %s: %w", entry.Dir, err)
	}
	return nil
}

// Prune deletes all but the newest keep backups under root and returns the
// ids it removed. Zero keeps every one.
//
// It only ever removes a directory this package made: the name has to match
// and a manifest has to be inside it. Retention that guessed would eventually
// delete the wrong thing, and the directory an operator points at is often
// shared with somebody else's copies. Uploaded and fetched backups are kept
// too: retention is for the copies the fleet takes of itself, and a file
// somebody carried here -- or pulled back out of a bucket -- is here for a
// reason that retention does not know about.
func Prune(root string, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	all, err := List(root)
	if err != nil {
		return nil, err
	}
	var ours []Entry
	for _, e := range all {
		if e.Manifest == nil || e.Source == SourceUploaded || e.Source == SourceFetched {
			continue
		}
		ours = append(ours, e)
	}
	if len(ours) <= keep {
		return nil, nil
	}
	var removed []string
	for _, e := range ours[keep:] {
		if err := os.RemoveAll(e.Dir); err != nil {
			return removed, fmt.Errorf("backup: removing the old backup %s: %w", e.ID, err)
		}
		removed = append(removed, e.ID)
	}
	return removed, nil
}

// ---------------------------------------------------------------------------
// Verification
// ---------------------------------------------------------------------------

// Verification is what re-reading a backup found.
type Verification struct {
	CheckedAt time.Time `json:"checked_at"`
	// OK is every check below passing.
	OK bool `json:"ok"`
	// Integrity is SQLite's own answer: "ok", or what it found.
	Integrity string `json:"integrity"`
	// DigestMatches says the file is byte for byte what the manifest
	// recorded. False on a backup with no manifest to compare against, and
	// DigestKnown says whether the comparison was possible at all.
	DigestKnown   bool `json:"digest_known"`
	DigestMatches bool `json:"digest_matches"`
	// SchemaReadable says this build can open it: a backup from a newer
	// release is sound and still not restorable here.
	SchemaReadable bool `json:"schema_readable"`
	// Migrations is how many the copy has taken, and Latest the newest.
	Migrations int    `json:"migrations"`
	Latest     string `json:"latest,omitempty"`
	// Problems is every check that failed, in words.
	Problems []string `json:"problems"`
}

// Verify opens a backup's database and asks it the questions a restore would.
//
// It is what makes a backup a backup rather than a file: a copy nobody has
// opened is a copy nobody knows about, and an operator who verifies the copies
// in the corner finds the bad one before the day it is needed.
func Verify(ctx context.Context, root, id string) (*Verification, error) {
	entry, err := Get(root, id)
	if err != nil {
		return nil, err
	}
	return verifyDir(ctx, entry)
}

func verifyDir(ctx context.Context, entry *Entry) (*Verification, error) {
	v := &Verification{CheckedAt: time.Now().UTC(), Problems: []string{}}
	dbPath := filepath.Join(entry.Dir, DBName)

	if entry.Manifest != nil && entry.Manifest.Database.SHA256 != "" {
		v.DigestKnown = true
		sum, err := FileSHA256(dbPath)
		if err != nil {
			return nil, err
		}
		v.DigestMatches = sum == entry.Manifest.Database.SHA256
		if !v.DigestMatches {
			v.Problems = append(v.Problems, "the database is not the file the manifest recorded: its digest has changed, so it was altered or truncated after it was taken")
		}
	}

	st, err := store.Open(ctx, store.Options{Path: dbPath, ReadOnly: true})
	if err != nil {
		if errors.Is(err, store.ErrSchemaNewer) {
			v.SchemaReadable = false
			v.Integrity = "not checked"
			v.Problems = append(v.Problems, "this backup was written by a newer release than "+version.Short()+": "+err.Error())
		} else {
			v.Integrity = "unreadable"
			v.Problems = append(v.Problems, "the database does not open: "+err.Error())
		}
		v.OK = false
		return v, nil
	}
	defer func() { _ = st.Close() }()
	v.SchemaReadable = true

	if err := st.IntegrityCheck(ctx); err != nil {
		v.Integrity = err.Error()
		v.Problems = append(v.Problems, err.Error())
	} else {
		v.Integrity = "ok"
	}
	if applied, err := st.AppliedMigrations(ctx); err == nil {
		v.Migrations = len(applied)
		if n := len(applied); n > 0 {
			v.Latest = applied[n-1].Name
		}
	}
	v.OK = len(v.Problems) == 0
	return v, nil
}
