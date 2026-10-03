package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/aicontext"
)

func retainedContext(t *testing.T, r *AIContextRepository, commit string) *aicontext.Snapshot {
	t.Helper()
	hash, err := r.Config.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return &aicontext.Snapshot{Manifest: aicontext.Manifest{SchemaVersion: 1, Repository: r.Key, SourceBranch: r.Config.SourceBranch, SourceCommit: commit, ConfigHash: hash, GeneratedAt: time.Now().UTC(), Manager: "zoomies", Generator: "repomix@1.18.1"}, Files: []aicontext.File{{Path: "main.go", Content: "package main\n", SHA256: aicontext.Hash([]byte("package main\n"))}}}
}
func snapshotDigest(t *testing.T, s *aicontext.Snapshot) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return aicontext.Hash(b)
}

func TestContextPublicationIsAtomicAndFailedChecksRetainSourceWithoutAccess(t *testing.T) {
	s := newTestStore(t)
	r, u, _ := contextFixture(t, s)
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	snap := retainedContext(t, r, strings.Repeat("a", 40))
	digest := snapshotDigest(t, snap)
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision+1, snap); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale config: %v", err)
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), r.ID, digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed transaction retained a blob")
	}
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, snap); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.AIContextUserAccess(t.Context(), r.ID, u.ID); err != nil || !allowed {
		t.Fatalf("ready access: %v %v", allowed, err)
	}
	if err := s.FailAIContextCheck(t.Context(), r.ID, "stale", strings.Repeat("b", 40), "Generation failed."); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.AIContextUserAccess(t.Context(), r.ID, u.ID); err != nil || allowed {
		t.Fatalf("stale access: %v %v", allowed, err)
	}
	f, err := s.GetAIContextFreshness(t.Context(), r.ID)
	if err != nil || f.Digest != digest || f.PublishedCommit != snap.Manifest.SourceCommit || f.State != "stale" {
		t.Fatalf("last success lost: %+v %v", f, err)
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), r.ID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), "another", digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-repository read")
	}
	if err := s.ConfirmAIContextSnapshot(t.Context(), r.ID, r.Revision, digest, strings.Repeat("b", 40), snap.Manifest.ConfigHash); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong commit confirmed: %v", err)
	}
	if err := s.ConfirmAIContextSnapshot(t.Context(), r.ID, r.Revision, digest, snap.Manifest.SourceCommit, snap.Manifest.ConfigHash); err != nil {
		t.Fatal(err)
	}
}

func TestContextRetentionKeepsThePublishedSnapshotEvenWhenTimestampsTie(t *testing.T) {
	s := newTestStoreAt(t, func() time.Time { return time.Unix(1700000000, 0) })
	r, _, _ := contextFixture(t, s)
	r.Config.KeepSnapshots = 1
	if err := s.SaveAIContextConfig(t.Context(), r.ID, r.Revision, r.Config); err != nil {
		t.Fatal(err)
	}
	r, _ = s.GetAIContextRepository(t.Context(), r.ID)
	var current *aicontext.Snapshot
	for _, letter := range []string{"a", "b", "c", "d"} {
		current = retainedContext(t, r, strings.Repeat(letter, 40))
		if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, current); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.read.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM ai_context_snapshots WHERE repository_id=?`, r.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retention: %d %v", count, err)
	}
	f, err := s.GetAIContextFreshness(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), r.ID, f.Digest); err != nil {
		t.Fatal("published blob was deleted", err)
	}
	if f.PublishedCommit != current.Manifest.SourceCommit {
		t.Fatal("published metadata is stale")
	}
}

func TestContextSourceAndFreshnessSurviveAnIndependentDatabaseBackup(t *testing.T) {
	s, _ := onDiskStore(t)
	r, _, _ := contextFixture(t, s)
	snap := retainedContext(t, r, strings.Repeat("a", 40))
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, snap); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.Backup(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	f, err := copy.GetAIContextFreshness(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := copy.GetAIContextSnapshot(t.Context(), r.ID, f.Digest)
	if err != nil || got.Files[0].Content != snap.Files[0].Content {
		t.Fatalf("backup lost source: %v", err)
	}
	if err := copy.DeleteAIContextRepository(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := copy.GetAIContextSnapshot(t.Context(), r.ID, f.Digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete retained source")
	}
}

func TestContextQuotaFailureRollsBackRetentionAndPublicationTogether(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	r.Config.KeepSnapshots = 1
	if err := s.SaveAIContextConfig(t.Context(), r.ID, r.Revision, r.Config); err != nil {
		t.Fatal(err)
	}
	r, _ = s.GetAIContextRepository(t.Context(), r.ID)
	previous := retainedContext(t, r, strings.Repeat("a", 40))
	digest := snapshotDigest(t, previous)
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, previous); err != nil {
		t.Fatal(err)
	}
	next := retainedContext(t, r, strings.Repeat("b", 40))
	if err := s.publishAIContextSnapshot(t.Context(), r.ID, r.Revision, next, 1); err == nil {
		t.Fatal("over-quota publication accepted")
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), r.ID, digest); err != nil {
		t.Fatal("quota failure deleted the previous snapshot", err)
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), r.ID, snapshotDigest(t, next)); !errors.Is(err, ErrNotFound) {
		t.Fatal("quota failure retained the new snapshot")
	}
	f, err := s.GetAIContextFreshness(t.Context(), r.ID)
	if err != nil || f.Digest != digest {
		t.Fatalf("quota failure changed freshness: %+v %v", f, err)
	}
}

func TestContextAdmissionRejectsAnotherRepositoryAndExcludedSource(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	snapshot := retainedContext(t, r, strings.Repeat("a", 40))
	snapshot.Manifest.Repository.RepositoryID++
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, snapshot); err == nil {
		t.Fatal("foreign repository admitted")
	}
	snapshot.Manifest.Repository = r.Key
	snapshot.Files[0].Path = "dist/main.go"
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, snapshot); err == nil {
		t.Fatal("excluded source admitted")
	}
	snapshot.Files[0].Path = "main.go"
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, snapshot); err != nil {
		t.Fatal(err)
	}
	digest := snapshotDigest(t, snapshot)
	if _, err := s.exec(t.Context(), `UPDATE ai_context_snapshots SET body=? WHERE repository_id=? AND digest=?`, []byte(`{}`), r.ID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), r.ID, digest); err == nil {
		t.Fatal("corrupt retained source admitted")
	}
}
