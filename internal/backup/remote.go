package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
)

// A backup that leaves the machine.
//
// A copy beside the database is a backup against a mistake: a bad migration, a
// deletion somebody regrets, an upgrade that went sideways. It is not a backup
// against the disk, the machine or the datacentre, and a directory of tidy
// copies on the volume that just died is the worst kind of backup because
// everyone believed in it.
//
// So a remote is one S3-compatible bucket, and what goes into it is exactly
// the file the Backups tab downloads: the same gzipped tar of the manifest,
// the database and -- only when the backup was taken with it -- the key,
// optionally sealed with the same argon2id and AES-256-GCM. One archive
// format, whether it was downloaded by a browser, copied by hand or put there
// by the fleet, so a copy pulled out of a bucket three years from now opens
// with `zoomies restore` and nothing else.

// ErrNoRemote is a remote named by something that does not have one.
var ErrNoRemote = errors.New("backup: no such backup remote")

// Remote is one configured destination, ready to be used.
type Remote struct {
	cfg config.BackupRemote
	s3  *s3Client
}

// NewRemote builds a client for one configured remote.
func NewRemote(cfg config.BackupRemote, httpClient *http.Client) (*Remote, error) {
	client, err := newS3Client(cfg.Endpoint, cfg.Region, cfg.Bucket, cfg.AccessKeyID, cfg.SecretAccessKey, cfg.PathStyle, httpClient)
	if err != nil {
		return nil, fmt.Errorf("the backup remote %s: %w", cfg.Name, err)
	}
	return &Remote{cfg: cfg, s3: client}, nil
}

// Name is what this destination is called.
func (r *Remote) Name() string { return r.cfg.Name }

// Where is the bucket and prefix, for a log line or a row on the page.
func (r *Remote) Where() string { return r.cfg.Where() }

// Endpoint is the service it talks to.
func (r *Remote) Endpoint() string { return r.cfg.Endpoint }

// Encrypted says the archive is sealed before it leaves this host.
func (r *Remote) Encrypted() bool { return r.cfg.Encrypted() }

// Keep is how many copies this remote holds; 0 keeps every one.
func (r *Remote) Keep() int { return r.cfg.Keep }

// Key is where a backup with this id lands in the bucket.
func (r *Remote) Key(id string) string {
	name := ArchiveName(id, r.Encrypted())
	if r.cfg.Prefix == "" {
		return name
	}
	return r.cfg.Prefix + "/" + name
}

// Copy is one backup as a bucket holds it: an archive, not a directory.
type Copy struct {
	// Remote is the destination's name, so a list gathered from several says
	// which came from where.
	Remote string `json:"remote"`
	// ID is the backup's own id, read back out of the key, so a copy in a
	// bucket and the copy on disk it came from have one name.
	ID string `json:"id"`
	// Key is the object's full key, which is what an operator needs to find
	// it with any other tool.
	Key string `json:"key"`
	// Bytes is the archive's size, which is the compressed database rather
	// than the database.
	Bytes int64 `json:"bytes"`
	// StoredAt is when the object was written, from the service. It is not
	// when the backup was taken -- an old backup uploaded today is stored
	// today -- and the id carries the other.
	StoredAt time.Time `json:"stored_at"`
	// TakenAt is read from the id, which is the only thing about the backup a
	// listing can know without downloading it.
	TakenAt time.Time `json:"taken_at"`
	// Encrypted says the archive is sealed and needs the remote's passphrase.
	Encrypted bool `json:"encrypted"`
}

// Check asks the bucket one cheap question -- list one key under the prefix --
// which is the whole of what has to work for a backup to reach it: the
// endpoint resolves, TLS agrees, the signature is accepted, the bucket exists,
// and the policy allows reading it.
//
// It is what the settings page's "test this remote" runs, so that a mistyped
// secret is found when it is typed rather than at three in the morning.
func (r *Remote) Check(ctx context.Context) error {
	_, err := r.s3.list(ctx, r.cfg.Prefix, 1)
	return err
}

// List is every backup this remote holds, newest first.
//
// It holds an object only if it sits directly in this remote's own directory,
// which is exactly where Key writes it. A bucket's prefix match is a string
// match, so "prod" also finds "prod-eu/..." and "zoomies" finds
// "zoomies/prod/..."; an empty prefix finds the whole bucket. Retention runs on
// this list, so accepting those would have one fleet delete another's offsite
// backups.
//
// Anything else in the directory that is not named like an archive this
// package writes is ignored rather than reported: an operator's bucket is
// allowed to hold other things, and a listing that complained about them would
// be a listing nobody reads.
func (r *Remote) List(ctx context.Context) ([]Copy, error) {
	dir := r.dir()
	listPrefix := r.cfg.Prefix
	if dir != "." {
		listPrefix = dir + "/"
	}
	objects, err := r.s3.list(ctx, listPrefix, 0)
	if err != nil {
		return nil, err
	}
	out := []Copy{}
	for _, o := range objects {
		if path.Dir(o.Key) != dir {
			continue
		}
		id, encrypted, ok := idFromKey(o.Key)
		if !ok {
			continue
		}
		out = append(out, Copy{
			Remote: r.cfg.Name, ID: id, Key: o.Key, Bytes: o.Size,
			StoredAt: o.LastModified, TakenAt: takenFromName(id), Encrypted: encrypted,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// dir is the directory Key puts this remote's archives in, in the form
// path.Dir reports for an object key: "." for the bucket's root.
func (r *Remote) dir() string {
	if dir := strings.Trim(r.cfg.Prefix, "/"); dir != "" {
		return dir
	}
	return "."
}

// idFromKey reads a backup's id back out of an object key, and says whether
// the archive is encrypted. It looks at the name alone; List is what checks
// that the key's directory is this remote's own.
func idFromKey(key string) (id string, encrypted bool, ok bool) {
	name := path.Base(key)
	if strings.HasSuffix(name, EncryptedExt) {
		encrypted = true
		name = strings.TrimSuffix(name, EncryptedExt)
	}
	name, found := strings.CutSuffix(name, ".tar.gz")
	if !found || !ValidID(name) {
		return "", false, false
	}
	return name, encrypted, true
}

// Upload copies one backup into the bucket and returns what landed there.
//
// The archive is written to a file beside the backup first rather than
// streamed straight out. A signed PUT names the length and the digest of what
// it is sending, which is what lets the service refuse a transfer that arrived
// altered instead of storing it and saying nothing -- and neither is knowable
// until the last byte of a gzip stream has been produced. The spool costs one
// compressed copy of the database, briefly, on the volume the backup is
// already on.
func (r *Remote) Upload(ctx context.Context, entry *Entry) (*Copy, error) {
	spool, err := os.CreateTemp(filepath.Dir(entry.Dir), ".offsite-*")
	if err != nil {
		return nil, fmt.Errorf("backup: spooling %s for %s: %w", entry.ID, r.cfg.Name, err)
	}
	defer func() {
		spool.Close()
		_ = os.Remove(spool.Name())
	}()

	digest := sha256.New()
	var out io.Writer = io.MultiWriter(spool, digest)
	if r.Encrypted() {
		enc, err := NewEncryptor(out, r.cfg.Passphrase)
		if err != nil {
			return nil, err
		}
		if err := WriteArchive(enc, entry); err != nil {
			return nil, err
		}
		// Close writes the final chunk. Without it the file is a truncated
		// archive that will not open, which is the one failure a backup must
		// not be able to have.
		if err := enc.Close(); err != nil {
			return nil, err
		}
	} else if err := WriteArchive(out, entry); err != nil {
		return nil, err
	}

	size, err := spool.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	key := r.Key(entry.ID)
	if err := r.s3.put(ctx, key, spool, size, hex.EncodeToString(digest.Sum(nil))); err != nil {
		return nil, err
	}
	return &Copy{
		Remote: r.cfg.Name, ID: entry.ID, Key: key, Bytes: size,
		StoredAt: time.Now().UTC(), TakenAt: entry.TakenAt, Encrypted: r.Encrypted(),
	}, nil
}

// FetchOptions says who is pulling a copy back and how the archive opens.
type FetchOptions struct {
	// Passphrase overrides the remote's own, for an archive sealed with a
	// passphrase this configuration no longer carries -- which is what a
	// rotation leaves behind in a bucket.
	Passphrase string
	// TakenBy is recorded in the manifest of the copy that lands on disk.
	TakenBy string
	// MaxBytes bounds the unpacked database. Zero is unbounded.
	MaxBytes int64
	// Now is the clock; nil uses the wall clock.
	Now func() time.Time
}

// Fetch pulls one copy out of the bucket and unpacks it into root, where it is
// an ordinary backup: listed, verifiable, and restorable by the same staging
// the Backups tab does everything else through.
//
// It is deliberately two steps rather than "restore from the bucket". A
// restore swaps the database this fleet runs on, and the file it swaps in
// should be one somebody has seen land, verified, and chosen -- not one that
// arrived and was applied in the same request.
func (r *Remote) Fetch(ctx context.Context, root, id string, opts FetchOptions) (*Entry, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	body, _, err := r.s3.get(ctx, r.Key(id))
	if err != nil {
		return nil, err
	}
	defer body.Close()

	passphrase := opts.Passphrase
	if strings.TrimSpace(passphrase) == "" {
		passphrase = r.cfg.Passphrase
	}
	var src io.Reader = body
	encrypted, peek := IsEncrypted(body)
	src = peek
	switch {
	case encrypted && strings.TrimSpace(passphrase) == "":
		return nil, errors.New("backup: this copy is encrypted and the remote has no passphrase configured; give the one it was sealed with")
	case encrypted:
		src, err = NewDecryptor(peek, passphrase)
		if err != nil {
			return nil, err
		}
	}

	entry, err := Unpack(ctx, root, src, UnpackOptions{
		Source: SourceFetched, TakenBy: opts.TakenBy, MaxBytes: opts.MaxBytes, Now: opts.Now,
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

// Delete removes one copy from the bucket.
func (r *Remote) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return r.s3.remove(ctx, r.Key(id))
}

// Prune deletes all but the newest keep copies and returns the ids it removed.
// Zero keeps every one, exactly as backup.keep does on disk.
//
// It only ever removes an object whose key is one this package writes, under
// this remote's own prefix: a bucket shared with somebody else's backups is
// the normal case, and retention that guessed would eventually delete the
// wrong thing.
func (r *Remote) Prune(ctx context.Context, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	copies, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	if len(copies) <= keep {
		return nil, nil
	}
	var removed []string
	for _, c := range copies[keep:] {
		if err := r.s3.remove(ctx, c.Key); err != nil {
			return removed, err
		}
		removed = append(removed, c.ID)
	}
	return removed, nil
}

// Missing is the backups in entries that this remote does not already hold,
// newest first and no more than the remote keeps.
//
// It is what makes a remote catch up by itself. A bucket that was unreachable
// for two nights is two backups behind, and the next pass sends both rather
// than only the one it happens to have been asked about -- while a remote
// keeping three copies is never sent the fourth-oldest backup only to delete
// it again a second later.
func Missing(entries []Entry, held []Copy, keep int) []Entry {
	have := map[string]bool{}
	for _, c := range held {
		have[c.ID] = true
	}
	var ours []Entry
	for _, e := range entries {
		// A backup with no database, or one this fleet did not take, is not
		// something to put in somebody's bucket by itself.
		if e.Problem != "" || e.Source == SourcePreMigration {
			continue
		}
		ours = append(ours, e)
	}
	if keep > 0 && len(ours) > keep {
		ours = ours[:keep]
	}
	out := []Entry{}
	for _, e := range ours {
		if !have[e.ID] {
			out = append(out, e)
		}
	}
	// Oldest first, so an interrupted catch-up leaves the bucket holding a
	// contiguous run rather than a gap.
	slices.Reverse(out)
	return out
}
