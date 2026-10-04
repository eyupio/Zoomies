package store

import (
	"errors"
	"strings"
	"testing"
)

func publishFixture(t *testing.T) (*Store, *AIContextRepository, *User, *OAuthGrant) {
	t.Helper()
	s := newTestStore(t)
	r, u, g := contextFixture(t, s)
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, []string{u.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAIContextAvailable(t.Context(), r.ID, true); err != nil {
		t.Fatal(err)
	}
	return s, r, u, g
}

// A note is versioned rather than overwritten, so an edit never loses what an
// earlier run concluded; the listing shows the latest of each.
func TestANoteIsVersionedAndListedByItsLatest(t *testing.T) {
	s, r, u, _ := publishFixture(t)
	for i, title := range []string{"First look", "Second look"} {
		a := &AIContextArtifact{RepositoryID: r.ID, Slug: "review", Kind: "report", Title: title, Body: "# " + title, AuthorName: u.Username, ViaKind: "user"}
		if err := s.PublishAIContextArtifact(t.Context(), a, u.ID); err != nil {
			t.Fatal(err)
		}
		if a.Version != i+1 {
			t.Fatalf("version = %d, want %d", a.Version, i+1)
		}
	}
	items, total, err := s.ListAIContextArtifacts(t.Context(), r.ID, 10, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].Title != "Second look" || items[0].Versions != 2 || items[0].Body != "" {
		t.Fatalf("listing = %+v, %d, %v", items, total, err)
	}
	first, err := s.GetAIContextArtifact(t.Context(), r.ID, "review", 1)
	if err != nil || first.Title != "First look" || first.Body != "# First look" {
		t.Fatalf("version 1 = %+v, %v", first, err)
	}
	latest, err := s.GetAIContextArtifact(t.Context(), r.ID, "review", 0)
	if err != nil || latest.Version != 2 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
}

func TestANoteBreakingTheRulesIsRefusedWithTheRuleItBroke(t *testing.T) {
	s, r, u, _ := publishFixture(t)
	for name, a := range map[string]AIContextArtifact{
		"a slug with capitals": {Slug: "Review", Kind: "report", Title: "t", Body: "b"},
		"an unknown kind":      {Slug: "review", Kind: "essay", Title: "t", Body: "b"},
		"a two-line title":     {Slug: "review", Kind: "report", Title: "t\nu", Body: "b"},
		"an empty body":        {Slug: "review", Kind: "report", Title: "t"},
		"an oversized body":    {Slug: "review", Kind: "report", Title: "t", Body: strings.Repeat("x", MaxArtifactBodyBytes+1)},
	} {
		a.RepositoryID, a.AuthorName, a.ViaKind = r.ID, u.Username, "user"
		if err := s.PublishAIContextArtifact(t.Context(), &a, u.ID); !errors.Is(err, ErrInvalidArtifact) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.SetAIContextAvailable(t.Context(), r.ID, false); err != nil {
		t.Fatal(err)
	}
	a := &AIContextArtifact{RepositoryID: r.ID, Slug: "review", Kind: "report", Title: "t", Body: "b", AuthorName: u.Username, ViaKind: "user"}
	if err := s.PublishAIContextArtifact(t.Context(), a, u.ID); !errors.Is(err, ErrConflict) {
		t.Errorf("published to a repository whose context is not verified: %v", err)
	}
}

func TestOnlyTheNewestVersionsOfANoteAreKept(t *testing.T) {
	s, r, u, _ := publishFixture(t)
	for i := 0; i < MaxArtifactVersions+3; i++ {
		a := &AIContextArtifact{RepositoryID: r.ID, Slug: "plan", Kind: "plan", Title: "Plan", Body: "step", AuthorName: u.Username, ViaKind: "user"}
		if err := s.PublishAIContextArtifact(t.Context(), a, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	items, _, _ := s.ListAIContextArtifacts(t.Context(), r.ID, 10, 0)
	if items[0].Versions != MaxArtifactVersions || items[0].Version != MaxArtifactVersions+3 {
		t.Fatalf("kept %d versions, latest %d", items[0].Versions, items[0].Version)
	}
	if _, err := s.GetAIContextArtifact(t.Context(), r.ID, "plan", 3); !errors.Is(err, ErrNotFound) {
		t.Fatal("a pruned version is still readable", err)
	}
}

// Reading consent is not writing consent. A connection publishes only where its
// owner said it may, and a client that knows nothing of publishing cannot
// clear or widen that by saving read consent.
func TestAConnectionPublishesOnlyWhereItsOwnerSaidSo(t *testing.T) {
	s, r, u, g := publishFixture(t)
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AIContextConnectionPublishAccess(t.Context(), r.ID, g.ID, u.ID); ok {
		t.Fatal("read consent alone let a connection publish")
	}
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, []string{r.ID}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AIContextConnectionPublishAccess(t.Context(), r.ID, g.ID, u.ID); !ok {
		t.Fatal("explicit publish consent was not recorded")
	}
	// Saving read consent again without saying anything about publishing
	// keeps it.
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, nil); err != nil {
		t.Fatal(err)
	}
	choices, err := s.AIContextConnectionChoices(t.Context(), g.ID, u.ID, 10, 0)
	if err != nil || len(choices.PublishRepositoryIDs) != 1 {
		t.Fatalf("publish consent lost on a read-only save: %+v, %v", choices, err)
	}
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, []string{}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AIContextConnectionPublishAccess(t.Context(), r.ID, g.ID, u.ID); ok {
		t.Fatal("an explicit empty publish list did not clear it")
	}
	// Removing the member removes every consent, publishing included.
	if err := s.ReplaceAIContextConnectionAccess(t.Context(), g.ID, u.ID, []string{r.ID}, []string{r.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAIContextMembers(t.Context(), r.ID, nil); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AIContextConnectionPublishAccess(t.Context(), r.ID, g.ID, u.ID); ok {
		t.Fatal("a removed member's connection can still publish")
	}
}
