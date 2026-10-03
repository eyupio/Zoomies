package aicontext

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture() *Snapshot {
	config := DefaultConfig("main")
	hash, _ := config.Hash()
	text := strings.Repeat("func café() { return \"🐶\" }\n", 300)
	return &Snapshot{Manifest: Manifest{SchemaVersion: SchemaVersion,
		Repository: RepositoryKey{"github.com", "ins_test", 42}, SourceBranch: "main",
		SourceCommit: strings.Repeat("a", 40), ConfigHash: hash, GeneratedAt: time.Unix(1, 0).UTC(), Manager: "zoomies", Generator: "repomix@1.18.1"},
		Files: []File{{Path: "main.go", Content: text, SHA256: Hash([]byte(text))}}}
}

func TestSnapshotsAdmitThePilotSizeButStillRefuseSourceBeyondTheBound(t *testing.T) {
	s := fixture()
	s.Files = nil
	content := strings.Repeat("a", MaxFileBytes)
	for i := 0; i < 24; i++ {
		s.Files = append(s.Files, File{Path: fmt.Sprintf("src/file-%02d.go", i), Content: content, SHA256: Hash([]byte(content))})
	}
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(bytes.NewReader(body)); err != nil {
		t.Fatal("bounded 24 MiB source refused", err)
	}
	s.Files = append(s.Files, File{Path: "src/overflow.go", Content: "a", SHA256: Hash([]byte("a"))})
	if err := s.Validate(); err == nil {
		t.Fatal("source beyond its bound admitted")
	}
}

func TestSnapshotsRejectUnsafeOrUnverifiableContent(t *testing.T) {
	cases := map[string]func(*Snapshot){
		"unidentified repository": func(s *Snapshot) { s.Manifest.Repository.RepositoryID = 0 },
		"credential in host":      func(s *Snapshot) { s.Manifest.Repository.GitHubHost = "user:password@github.com" },
		"unknown schema":          func(s *Snapshot) { s.Manifest.SchemaVersion++ },
		"generated source branch": func(s *Snapshot) { s.Manifest.SourceBranch = OutputBranch },
		"missing commit":          func(s *Snapshot) { s.Manifest.SourceCommit = "main" },
		"wrong hash":              func(s *Snapshot) { s.Files[0].Content += "changed" },
		"duplicate path":          func(s *Snapshot) { s.Files = append(s.Files, s.Files[0]) },
		"binary source":           func(s *Snapshot) { s.Files[0].Content = "\x00"; s.Files[0].SHA256 = Hash([]byte("\x00")) },
		"oversize file": func(s *Snapshot) {
			s.Files[0].Content = strings.Repeat("a", MaxFileBytes+1)
			s.Files[0].SHA256 = Hash([]byte(s.Files[0].Content))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := fixture()
			mutate(s)
			if s.Validate() == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	for _, path := range []string{"../private.go", "/etc/passwd", "web/../main.go", "a\\b", "a\nb", ".git/config", "web/.env.local", "key.pem", "data.sqlite", "node_modules/x.js", ".zoomies/ai-context/manifest.json"} {
		t.Run(path, func(t *testing.T) {
			s := fixture()
			s.Files[0].Path = path
			if s.Validate() == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
}

func TestDecodeEnforcesOneBoundedSnapshot(t *testing.T) {
	body, _ := json.Marshal(fixture())
	if _, err := Decode(bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{append(append([]byte{}, body...), []byte(" {}")...), []byte(`{"unknown":true}`), []byte{255}, bytes.Repeat([]byte(" "), MaxSnapshotBytes+1)} {
		if _, err := Decode(bytes.NewReader(body)); err == nil {
			t.Fatal("invalid or oversized document accepted")
		}
	}
}

func TestSnapshotIdentityMustMatchEveryRequestedDimension(t *testing.T) {
	s := fixture()
	m := s.Manifest
	if err := s.Match(m.Repository, m.SourceBranch, m.SourceCommit, m.ConfigHash); err != nil {
		t.Fatal(err)
	}
	key := m.Repository
	key.GitHubHost = "github.example.com"
	if s.Match(key, m.SourceBranch, m.SourceCommit, m.ConfigHash) == nil {
		t.Fatal("Enterprise host confused with github.com")
	}
	key = m.Repository
	key.RepositoryID++
	if s.Match(key, m.SourceBranch, m.SourceCommit, m.ConfigHash) == nil {
		t.Fatal("different repository accepted")
	}
	key = m.Repository
	key.InstallationID = "other"
	if s.Match(key, m.SourceBranch, m.SourceCommit, m.ConfigHash) == nil {
		t.Fatal("different installation accepted")
	}
	if s.Match(m.Repository, "dev", m.SourceCommit, m.ConfigHash) == nil || s.Match(m.Repository, m.SourceBranch, strings.Repeat("b", 40), m.ConfigHash) == nil || s.Match(m.Repository, m.SourceBranch, m.SourceCommit, strings.Repeat("b", 64)) == nil {
		t.Fatal("stale snapshot accepted")
	}
}

func TestCompactReadsRecoverAllUnicodeSourceWithoutRepeatedLineObjects(t *testing.T) {
	s := fixture()
	offset := 0
	var text strings.Builder
	for {
		r, err := s.Read("main.go", offset, 17)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Text) > 17 {
			t.Fatal("response budget exceeded")
		}
		text.WriteString(r.Text)
		if r.NextOffset == nil {
			break
		}
		if *r.NextOffset <= offset {
			t.Fatal("pagination did not progress")
		}
		offset = *r.NextOffset
	}
	if text.String() != s.Files[0].Content {
		t.Fatal("pagination lost or duplicated source")
	}
	for _, offset := range []int{-1, len(s.Files[0].Content) + 1, strings.Index(s.Files[0].Content, "🐶") + 1} {
		if _, err := s.Read("main.go", offset, 100); err == nil {
			t.Fatal("invalid offset accepted")
		}
	}
	if _, err := s.Read("missing", 0, 100); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := s.Read("main.go", 0, MaxResponseBytes+1); err == nil {
		t.Fatal("oversize budget accepted")
	}
}

func TestStorageSurvivesRestartAndDetectsTampering(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	d, err := OpenDiskStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.Put(t.Context(), fixture())
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = OpenDiskStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if s.Files[0].Content != fixture().Files[0].Content {
		t.Fatal("source changed across restart")
	}
	if info, _ := os.Stat(filepath.Join(dir, id+".json")); info.Mode().Perm() != 0600 {
		t.Fatal("private source is not mode 0600")
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Get(t.Context(), id); err == nil {
		t.Fatal("corrupt blob accepted")
	}
	if err := d.Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPublicationAndCancelledReadsRemainBounded(t *testing.T) {
	d, err := OpenDiskStorage(filepath.Join(t.TempDir(), "context"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := d.Put(t.Context(), fixture()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.Put(ctx, fixture()); err == nil {
		t.Fatal("cancelled publication succeeded")
	}
	if _, err := d.Get(ctx, strings.Repeat("a", 64)); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	if _, err := d.Get(t.Context(), "../secret"); err == nil {
		t.Fatal("untrusted blob path accepted")
	}
}

func TestStorageRefusesPublicDirectoriesAndSymlinkEscapes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDiskStorage(dir); err == nil {
		t.Fatal("public source directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDiskStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("secret"), 0600)
	id := strings.Repeat("a", 64)
	if err := os.Symlink(outside, filepath.Join(dir, id+".json")); err != nil {
		t.Skip(err)
	}
	if _, err := d.Get(t.Context(), id); err == nil {
		t.Fatal("storage followed an escaping symlink")
	}
}

func TestConfigurationRejectsBranchInjectionAndHashesExclusions(t *testing.T) {
	for _, branch := range []string{"", "-main", "main\nscript", "main~1", "../main", "refs//main", "main.lock", ".main", OutputBranch} {
		c := DefaultConfig(branch)
		if c.Validate() == nil {
			t.Fatalf("invalid branch %q accepted", branch)
		}
	}
	c := DefaultConfig("release/v1")
	a, err := c.Hash()
	if err != nil {
		t.Fatal(err)
	}
	c.Exclude = append(c.Exclude, "private/**")
	b, _ := c.Hash()
	if a == b {
		t.Fatal("exclusion changes did not change identity")
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.KeepSnapshots = 0 }, func(c *Config) { c.Destination = "unknown" }, func(c *Config) { c.Exclude = []string{"../private"} }, func(c *Config) { c.Exclude = []string{"a\nb"} }} {
		c := DefaultConfig("main")
		mutate(&c)
		if c.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}

func TestLiteralSearchIsBoundedAndPagesWithoutLosingMatches(t *testing.T) {
	s := fixture()
	offset := 0
	count := 0
	for {
		page, err := s.Search("CAFÉ", "", offset, 12)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Matches) > 12 {
			t.Fatal("search exceeded match limit")
		}
		count += len(page.Matches)
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if count != 300 {
		t.Fatalf("search found %d matches, want 300", count)
	}
	page, err := s.Search(".*", "", 0, 12)
	if err != nil || len(page.Matches) != 0 {
		t.Fatal("literal query treated as a regular expression")
	}
	page, err = s.Search("café", "web/", 0, 12)
	if err != nil || len(page.Matches) != 0 {
		t.Fatal("search escaped its path prefix")
	}
	if _, err := s.Search("café", "../", 0, 12); err == nil {
		t.Fatal("unsafe prefix accepted")
	}
	if _, err := s.Search("café", "", 0, 13); err == nil {
		t.Fatal("excessive match limit accepted")
	}
	line := strings.Repeat("🐶", 300) + "needle" + strings.Repeat("🐶", 300)
	s.Files = []File{{Path: "long.js", Content: line, SHA256: Hash([]byte(line))}}
	page, err = s.Search("needle", "", 0, 12)
	if err != nil || len(page.Matches) != 1 || !strings.Contains(page.Matches[0].Text, "needle") || len(page.Matches[0].Text) > 640 {
		t.Fatal("long-line match lost or unbounded")
	}
}
