package store

import (
	"errors"
	"strings"
	"testing"
)

func TestContextRemovalIsAtomicAndCannotResurrectConsentOrOldSource(t *testing.T) {
	s := newTestStore(t)
	r, u, g := contextFixture(t, s)
	old := &AIContextSetup{RepositoryID: r.ID, Revision: r.Revision, PlanHash: strings.Repeat("a", 64), PlanJSON: "{}"}
	if err := s.ClaimAIContextSetup(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAIContextSetup(t.Context(), r.ID, old.LeaseToken, 1, "https://github.com/acme/widgets/pull/1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	snapshot := retainedContext(t, r, strings.Repeat("b", 40))
	digest := snapshotDigest(t, snapshot)
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, nil); err != nil {
		t.Fatal(err)
	}
	// A note is derived from the source, so removal takes it too.
	note := &AIContextArtifact{RepositoryID: r.ID, Slug: "review", Kind: "report", Title: "Review", Body: "findings", AuthorName: u.Username, ViaKind: "user"}
	if err := s.PublishAIContextArtifact(t.Context(), note, u.ID); err != nil {
		t.Fatal(err)
	}
	config := r.Config
	config.Disabled = true
	config.SetupGeneration++
	next := &AIContextSetup{RepositoryID: r.ID, Revision: r.Revision + 1, PlanHash: strings.Repeat("c", 64), PlanJSON: "{}"}
	if err := s.ClaimAIContextMaintenance(t.Context(), next, strings.Repeat("d", 64), config); !errors.Is(err, ErrConflict) {
		t.Fatal("stale plan replaced setup", err)
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), r.ID, digest); err != nil {
		t.Fatal("failed claim deleted source", err)
	}
	if err := s.ClaimAIContextMaintenance(t.Context(), next, old.PlanHash, config); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAIContextSnapshot(t.Context(), r.ID, digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed snapshot retained", err)
	}
	members, err := s.AIContextMembers(t.Context(), r.ID)
	if err != nil || len(members) != 0 {
		t.Fatal("removed members retained", err)
	}
	if _, err := s.GetAIContextArtifact(t.Context(), r.ID, "review", 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed context kept its notes", err)
	}
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, r.Revision, snapshot); !errors.Is(err, ErrConflict) {
		t.Fatal("old in-flight publication succeeded", err)
	}
	removed, _ := s.GetAIContextRepository(t.Context(), r.ID)
	if err := s.PublishAIContextSnapshot(t.Context(), r.ID, removed.Revision, retainedContext(t, removed, strings.Repeat("b", 40))); err == nil {
		t.Fatal("disabled context accepted source")
	}
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIContextAvailable(t.Context(), r.ID, true); err != nil {
		t.Fatal(err)
	}
	if allowed, err := s.AIContextConnectionAccess(t.Context(), r.ID, g.ID, u.ID); err != nil || allowed {
		t.Fatal("old connection consent resurrected", err)
	}
	competing := *next
	if err := s.ClaimAIContextMaintenance(t.Context(), &competing, next.PlanHash, config); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate maintenance changed state", err)
	}
}
