package backup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// fakeStore is an object store that lasts as long as the test.
func fakeStore(t *testing.T) *FakeS3 {
	t.Helper()
	f := NewFakeS3("backups")
	t.Cleanup(f.Close)
	return f
}

// remoteFor is a configured remote pointed at the fake, with whatever the test
// wants changed about it.
func remoteFor(t *testing.T, f *FakeS3, adjust func(*config.BackupRemote)) *Remote {
	t.Helper()
	cfg := config.BackupRemote{
		Name: "offsite", Endpoint: f.Endpoint(), Bucket: f.Bucket(), Prefix: "fleet",
		AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret",
	}
	if adjust != nil {
		adjust(&cfg)
	}
	remote, err := NewRemote(cfg, nil)
	if err != nil {
		t.Fatalf("NewRemote: %v", err)
	}
	return remote
}

// takeOne is one backup on disk, which is what every remote operation is about.
func takeOne(t *testing.T, at time.Time) (*config.Config, *Entry) {
	t.Helper()
	cfg, st, _ := host(t)
	entry, err := Take(context.Background(), st, TakeOptions{
		Config: cfg, Source: SourceScheduled, TakenBy: "the schedule",
		Now: func() time.Time { return at },
	})
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	return cfg, entry
}

// The whole point, end to end: a backup taken here is in the bucket, comes
// back out of it, and is an ordinary backup when it does -- because a copy
// nobody has ever restored is a copy nobody knows about.
func TestUploadingABackupAndFetchingItBackGivesTheSameBackup(t *testing.T) {
	f := fakeStore(t)
	remote := remoteFor(t, f, nil)
	_, entry := takeOne(t, time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC))

	copied, err := remote.Upload(context.Background(), entry)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if copied.Key != "fleet/"+entry.ID+".tar.gz" {
		t.Errorf("stored at %q, which is not the prefix and name a restore would look for", copied.Key)
	}
	if copied.Bytes <= 0 {
		t.Errorf("the copy reports %d bytes", copied.Bytes)
	}

	held, err := remote.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(held) != 1 || held[0].ID != entry.ID {
		t.Fatalf("the bucket lists %+v, wanted the one backup %s", held, entry.ID)
	}
	if held[0].TakenAt.IsZero() {
		t.Error("a listing should be able to say when a backup was taken from its id alone")
	}

	// Somewhere else entirely, as a second fleet's directory would be.
	root := filepath.Join(t.TempDir(), "restored")
	fetched, err := remote.Fetch(context.Background(), root, entry.ID, FetchOptions{TakenBy: "alice"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if fetched.ID != entry.ID {
		t.Errorf("fetched %s, wanted %s", fetched.ID, entry.ID)
	}
	if fetched.Source != SourceFetched {
		t.Errorf("the fetched copy says it came from %q; it came out of a bucket", fetched.Source)
	}
	if fetched.Manifest == nil || fetched.Manifest.Database.SHA256 != entry.Manifest.Database.SHA256 {
		t.Fatal("the fetched database is not the one that was uploaded")
	}

	v, err := Verify(context.Background(), root, fetched.ID)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !v.OK {
		t.Errorf("the copy that came back does not verify: %v", v.Problems)
	}
}

// A remote with a passphrase must put an encrypted file in the bucket, not a
// database with a different name: the file is on somebody else's disk, and
// whoever holds the bucket is not the person who is allowed to read the fleet.
func TestAnEncryptedRemoteUploadsSomethingThatDoesNotOpenWithoutThePassphrase(t *testing.T) {
	f := fakeStore(t)
	remote := remoteFor(t, f, func(r *config.BackupRemote) { r.Passphrase = "a long passphrase" })
	_, entry := takeOne(t, time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC))

	copied, err := remote.Upload(context.Background(), entry)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.HasSuffix(copied.Key, ".tar.gz"+EncryptedExt) {
		t.Errorf("an encrypted copy is stored at %q, which does not say it is encrypted", copied.Key)
	}

	// The same remote, minus the passphrase, is what somebody holding the
	// bucket has: the bytes and nothing else.
	blind := remoteFor(t, f, func(r *config.BackupRemote) {
		r.Passphrase = ""
	})
	// Its key has no .enc, so it does not even find the object -- and asked
	// for the encrypted one, it cannot open it.
	if _, err := blind.Fetch(context.Background(), t.TempDir(), entry.ID, FetchOptions{}); err == nil {
		t.Error("an unencrypted remote fetched an encrypted copy")
	}
	if _, err := remote.Fetch(context.Background(), t.TempDir(), entry.ID, FetchOptions{Passphrase: "the wrong one"}); err == nil {
		t.Error("the wrong passphrase opened the archive")
	}

	root := t.TempDir()
	fetched, err := remote.Fetch(context.Background(), root, entry.ID, FetchOptions{})
	if err != nil {
		t.Fatalf("Fetch with the remote's own passphrase: %v", err)
	}
	if fetched.Manifest == nil || fetched.Manifest.Database.SHA256 != entry.Manifest.Database.SHA256 {
		t.Error("what came back is not what went in")
	}
}

// Retention on the remote keeps the newest and nothing else, and it only ever
// removes keys this package writes -- a bucket is usually shared.
func TestRemoteRetentionKeepsTheNewestAndLeavesStrangersAlone(t *testing.T) {
	f := fakeStore(t)
	remote := remoteFor(t, f, nil)
	f.Put("fleet/somebody-elses-backup.tar.gz", []byte("not ours"))

	cfg, st, _ := host(t)
	var ids []string
	for i := range 4 {
		at := time.Date(2026, 3, 1, 2, i, 0, 0, time.UTC)
		entry, err := Take(context.Background(), st, TakeOptions{Config: cfg, Source: SourceScheduled, Now: func() time.Time { return at }})
		if err != nil {
			t.Fatalf("Take: %v", err)
		}
		if _, err := remote.Upload(context.Background(), entry); err != nil {
			t.Fatalf("Upload: %v", err)
		}
		ids = append(ids, entry.ID)
	}

	removed, err := remote.Prune(context.Background(), 2)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed %v, wanted the two oldest", removed)
	}
	held, err := remote.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(held) != 2 || held[0].ID != ids[3] || held[1].ID != ids[2] {
		t.Errorf("the bucket holds %+v, wanted the newest two", held)
	}
	for _, key := range f.Keys() {
		if key == "fleet/somebody-elses-backup.tar.gz" {
			return
		}
	}
	t.Error("retention deleted an object this package did not write")
}

// A remote that was unreachable for two nights is two backups behind, and the
// next pass has to send both. Missing is what decides that, and it is a pure
// function so the rule is testable without a bucket.
func TestMissingSendsTheBacklogButNeverMoreThanTheRemoteKeeps(t *testing.T) {
	entries := []Entry{
		{ID: "zoomies-20260304-020000", Source: SourceScheduled},
		{ID: "zoomies-20260303-020000", Source: SourceScheduled},
		{ID: "zoomies-20260302-020000", Source: SourceScheduled},
		{ID: "zoomies-20260301-020000", Source: SourceScheduled},
	}
	held := []Copy{{ID: "zoomies-20260301-020000"}}

	missing := Missing(entries, held, 0)
	if len(missing) != 3 {
		t.Fatalf("with no ceiling, %d of the backlog would be sent; wanted 3", len(missing))
	}
	if missing[0].ID != "zoomies-20260302-020000" {
		t.Errorf("the backlog starts at %s; it is sent oldest first, so an interrupted catch-up leaves the bucket holding a contiguous run rather than a gap", missing[0].ID)
	}

	// A remote keeping two is never sent the third-oldest only to delete it.
	missing = Missing(entries, held, 2)
	if len(missing) != 2 || missing[0].ID != "zoomies-20260303-020000" {
		t.Errorf("with keep=2 the backlog is %+v; wanted only the newest two", missing)
	}

	// The store's pre-migration copies have no manifest and are the fleet's
	// own scaffolding, not its backups.
	pre := append([]Entry{{ID: "zoomies-20260305-020000", Source: SourcePreMigration}}, entries...)
	for _, e := range Missing(pre, nil, 0) {
		if e.Source == SourcePreMigration {
			t.Error("a pre-migration copy was queued for a bucket")
		}
	}

	// A backup whose database has gone is not something to upload.
	broken := []Entry{{ID: "zoomies-20260306-020000", Source: SourceScheduled, Problem: "the database is missing"}}
	if got := Missing(broken, nil, 0); len(got) != 0 {
		t.Errorf("a broken backup was queued for a bucket: %+v", got)
	}
}

// What a remote refuses has to arrive as a sentence an operator can act on:
// the fix for a wrong secret and the fix for a missing bucket are different
// jobs, and "403" is neither.
func TestARefusedRequestSaysWhatToDoAboutIt(t *testing.T) {
	f := fakeStore(t)
	remote := remoteFor(t, f, nil)

	for _, tc := range []struct {
		name   string
		status int
		code   string
		want   string
	}{
		{"a bucket that is not there", 404, "NoSuchBucket", "never creates one"},
		{"a secret that is wrong", 403, "SignatureDoesNotMatch", "secret access key is wrong"},
		{"a policy that says no", 403, "AccessDenied", "s3:PutObject"},
		{"a clock that has drifted", 403, "RequestTimeTooSkewed", "clock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.BreakWith(tc.status, tc.code, "refused")
			defer f.Mend()
			err := remote.Check(context.Background())
			if err == nil {
				t.Fatal("the check passed against a service that refused it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error reads %q, which does not tell the operator about %q", err, tc.want)
			}
		})
	}
}

// A remote holds one fleet's copies under a prefix; a bucket shared with
// another fleet must not make one appear in the other's list.
func TestAListingIsScopedToTheRemotesOwnPrefix(t *testing.T) {
	f := fakeStore(t)
	ours := remoteFor(t, f, func(r *config.BackupRemote) { r.Prefix = "prod" })
	theirs := remoteFor(t, f, func(r *config.BackupRemote) { r.Name = "other"; r.Prefix = "staging" })

	cfg, st, _ := host(t)
	entry, err := Take(context.Background(), st, TakeOptions{Config: cfg, Source: SourceScheduled})
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if _, err := theirs.Upload(context.Background(), entry); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	held, err := ours.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(held) != 0 {
		t.Errorf("the prod remote lists %+v, which is staging's", held)
	}
}

// A raw string prefix is not a directory: "prod" also matches "prod-eu/...",
// "zoomies" matches "zoomies/prod/...", and an empty prefix matches the whole
// bucket. Retention that listed by string prefix would delete another fleet's
// offsite backups without a word, which is the one failure it must not have.
func TestAListingNeverReachesIntoASiblingOrNestedPrefix(t *testing.T) {
	for _, tc := range []struct{ ours, theirs string }{
		{"prod", "prod-eu"},
		{"zoomies", "zoomies/prod"},
		{"", "staging"},
	} {
		t.Run(tc.ours+" against "+tc.theirs, func(t *testing.T) {
			f := fakeStore(t)
			ours := remoteFor(t, f, func(r *config.BackupRemote) { r.Prefix = tc.ours })
			theirs := remoteFor(t, f, func(r *config.BackupRemote) { r.Name = "other"; r.Prefix = tc.theirs })
			ctx := context.Background()

			base := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
			// Theirs are older than ours, so a listing that merged the two would
			// hand them to retention first.
			for i := 0; i < 2; i++ {
				_, entry := takeOne(t, base.Add(time.Duration(i)*time.Hour))
				if _, err := theirs.Upload(ctx, entry); err != nil {
					t.Fatalf("Upload: %v", err)
				}
			}
			_, entry := takeOne(t, base.Add(48*time.Hour))
			if _, err := ours.Upload(ctx, entry); err != nil {
				t.Fatalf("Upload: %v", err)
			}

			held, err := ours.List(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(held) != 1 || held[0].ID != entry.ID {
				t.Fatalf("the %q remote lists %+v, which is not just its own copy", tc.ours, held)
			}

			removed, err := ours.Prune(ctx, 1)
			if err != nil {
				t.Fatalf("Prune: %v", err)
			}
			if len(removed) != 0 {
				t.Errorf("pruning the %q remote removed %v, which are %q's", tc.ours, removed, tc.theirs)
			}
			kept, err := theirs.List(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(kept) != 2 {
				t.Errorf("the %q remote holds %d copies after the other's retention pass, wanted 2", tc.theirs, len(kept))
			}
		})
	}
}

// The endpoint decides the request style, because the alternative is a setting
// that silently disagrees with the endpoint: a bucket in the hostname needs
// DNS nobody arranges for a MinIO on a private network.
func TestPathStyleIsChosenByTheEndpointUnlessItIsSaid(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		endpoint  string
		pathStyle *bool
		want      string
	}{
		{"https://s3.eu-west-2.amazonaws.com", nil, "https://backups.s3.eu-west-2.amazonaws.com/k"},
		{"http://minio:9000", nil, "http://minio:9000/backups/k"},
		{"https://s3.eu-west-2.amazonaws.com", &yes, "https://s3.eu-west-2.amazonaws.com/backups/k"},
		{"http://minio:9000", &no, "http://backups.minio:9000/k"},
	} {
		client, err := newS3Client(tc.endpoint, "", "backups", "id", "secret", tc.pathStyle, nil)
		if err != nil {
			t.Fatalf("newS3Client(%s): %v", tc.endpoint, err)
		}
		if got := client.url("k").String(); got != tc.want {
			t.Errorf("%s addresses a key as %s, wanted %s", tc.endpoint, got, tc.want)
		}
	}
}

// The signature is the one part of this with an external definition, so it is
// checked against the example AWS documents it with rather than against
// itself.
func TestTheCanonicalRequestIsEncodedTheWayTheSignatureDefinesIt(t *testing.T) {
	if got := uriEncode("a b/c~d.e", false); got != "a%20b/c~d.e" {
		t.Errorf("a path encodes as %q; a space is %%20, a tilde and a dot are themselves, and a slash stays", got)
	}
	if got := uriEncode("a b/c", true); got != "a%20b%2Fc" {
		t.Errorf("a query value encodes as %q; outside a path a slash is encoded too", got)
	}
	if emptyPayloadSHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Error("the empty payload digest is not the digest of no bytes, so every unsigned-body request would be refused")
	}
}

// The merge rule has to hold for every caller -- the controller's pass, the
// API and `zoomies restore --from-remote` on a host with no controller -- so
// it is tested where it lives rather than three times over.
func TestTheFileWinsAndAStoredSecretThatWillNotOpenIsReported(t *testing.T) {
	f := fakeStore(t)
	cfg := config.Default()
	cfg.Backup.Remotes = []config.BackupRemote{{
		Name: "offsite", Endpoint: f.Endpoint(), Bucket: f.Bucket(),
		AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "from-the-file",
	}}
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	sealed, err := key.SealString("from-the-database")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}
	stranger, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	strange, err := stranger.SealString("somebody else's")
	if err != nil {
		t.Fatalf("SealString: %v", err)
	}

	rows := fakeRemotes{
		{ID: "bkr_1", Name: "offsite", Endpoint: "https://elsewhere.example.com", Bucket: "other",
			AccessKeyID: "a", SecretKeyEnc: sealed, Enabled: true},
		{ID: "bkr_2", Name: "second", Endpoint: "https://s3.example.com", Bucket: "second",
			AccessKeyID: "b", SecretKeyEnc: sealed, Enabled: true},
		{ID: "bkr_3", Name: "strange", Endpoint: "https://s3.example.com", Bucket: "third",
			AccessKeyID: "c", SecretKeyEnc: strange, Enabled: true},
	}

	resolved, err := ResolveRemotes(context.Background(), cfg, rows, key)
	if err != nil {
		t.Fatalf("ResolveRemotes: %v", err)
	}
	if len(resolved) != 4 {
		t.Fatalf("resolved %d destinations, wanted the file's and all three rows", len(resolved))
	}
	if resolved[0].Origin != OriginFile || resolved[0].Remote.SecretAccessKey != "from-the-file" {
		t.Errorf("the file's destination reads %+v", resolved[0])
	}
	if !resolved[1].Shadowed || resolved[1].Usable() {
		t.Errorf("the stored destination of the same name is %+v; the file has the last word", resolved[1])
	}
	if resolved[2].Remote.SecretAccessKey != "from-the-database" || !resolved[2].Usable() {
		t.Errorf("a stored destination with its own name reads %+v", resolved[2])
	}
	if resolved[3].Problem == "" || resolved[3].Usable() {
		t.Errorf("a stored destination sealed with another key reads %+v; it must not be tried", resolved[3])
	}

	// And the lookup every route uses agrees with the list.
	found, err := FindResolvedRemote(context.Background(), cfg, rows, key, "offsite")
	if err != nil || found.Origin != OriginFile {
		t.Errorf("looking up offsite gave %+v, %v", found, err)
	}
	if _, err := FindResolvedRemote(context.Background(), cfg, rows, key, "strange"); err == nil {
		t.Error("a destination whose secret will not open was handed out")
	}
	if _, err := FindResolvedRemote(context.Background(), cfg, rows, key, "nowhere"); !errors.Is(err, ErrNoRemote) {
		t.Errorf("an unknown name gave %v", err)
	}

	// A fleet with no database at all still has the file's destinations,
	// which is the case `zoomies restore --from-remote` exists for.
	resolved, err = ResolveRemotes(context.Background(), cfg, nil, key)
	if err != nil || len(resolved) != 1 || resolved[0].Origin != OriginFile {
		t.Errorf("with no database the fleet resolved %+v, %v", resolved, err)
	}
}

// fakeRemotes is a stored-destination list without a database behind it.
type fakeRemotes []store.BackupRemote

func (f fakeRemotes) ListBackupRemotes(context.Context) ([]*store.BackupRemote, error) {
	out := make([]*store.BackupRemote, len(f))
	for i := range f {
		row := f[i]
		out[i] = &row
	}
	return out, nil
}
