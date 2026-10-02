package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContextSetupFreezesConfigurationAndLeasesTheSameProposal(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	proposal := &AIContextSetup{RepositoryID: r.ID, Revision: r.Revision, PlanHash: strings.Repeat("a", 64), PlanJSON: `{"reviewed":true}`}
	if err := s.ClaimAIContextSetup(t.Context(), proposal); err != nil {
		t.Fatal(err)
	}
	competing := *proposal
	if err := s.ClaimAIContextSetup(t.Context(), &competing); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate lease: %v", err)
	}
	if err := s.SaveAIContextConfig(t.Context(), r.ID, r.Revision, r.Config); !errors.Is(err, ErrConflict) {
		t.Fatalf("configuration changed while publishing: %v", err)
	}
	if err := s.ReleaseAIContextSetup(t.Context(), r.ID, "wrong lease"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAIContextSetup(t.Context(), &competing); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong release unlocked proposal")
	}
	if err := s.ReleaseAIContextSetup(t.Context(), r.ID, proposal.LeaseToken); err != nil {
		t.Fatal(err)
	}
	competing.PlanHash = strings.Repeat("b", 64)
	if err := s.ClaimAIContextSetup(t.Context(), &competing); !errors.Is(err, ErrConflict) {
		t.Fatal("retry replaced reviewed proposal")
	}
	competing.PlanHash = proposal.PlanHash
	if err := s.ClaimAIContextSetup(t.Context(), &competing); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAIContextSetup(t.Context(), r.ID, proposal.LeaseToken, 1, "https://github.com/acme/widgets/pull/1"); !errors.Is(err, ErrConflict) {
		t.Fatal("old lease completed new call")
	}
	if err := s.CompleteAIContextSetup(t.Context(), r.ID, competing.LeaseToken, 1, "https://github.com/acme/widgets/pull/1"); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetAIContextSetup(t.Context(), r.ID)
	if err != nil || saved.State != "awaiting_merge" || saved.PRNumber != 1 {
		t.Fatalf("%+v %v", saved, err)
	}
}

func TestContextSetupCanRecoverAnExpiredClaim(t *testing.T) {
	now := time.Now()
	s := newTestStoreAt(t, func() time.Time { return now })
	r, _, _ := contextFixture(t, s)
	p := &AIContextSetup{RepositoryID: r.ID, Revision: r.Revision, PlanHash: strings.Repeat("a", 64), PlanJSON: "{}"}
	if err := s.ClaimAIContextSetup(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	if err := s.ClaimAIContextSetup(t.Context(), p); err != nil {
		t.Fatal(err)
	}
}

func TestContextSetupStateAppearsOnTheBoundedAdministrativePage(t *testing.T) {
	s := newTestStore(t)
	r, _, _ := contextFixture(t, s)
	p := &AIContextSetup{RepositoryID: r.ID, Revision: r.Revision, PlanHash: strings.Repeat("a", 64), PlanJSON: "{}"}
	if err := s.ClaimAIContextSetup(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteAIContextSetup(t.Context(), r.ID, p.LeaseToken, 1, "https://github.com/acme/widgets/pull/1"); err != nil {
		t.Fatal(err)
	}
	page, total, err := s.ListAIContextRepositories(t.Context(), 1, 0)
	if err != nil || total != 1 || len(page) != 1 || page[0].SetupState != "awaiting_merge" || page[0].SetupPRURL == "" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestContextSetupProposalSurvivesRestartAndDeletionCascades(t *testing.T) {
	now := time.Now()
	options := Options{Path: filepath.Join(t.TempDir(), "context.db"), Now: func() time.Time { return now }}
	s, err := Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _ := contextFixture(t, s)
	p := &AIContextSetup{RepositoryID: r.ID, Revision: r.Revision, PlanHash: strings.Repeat("a", 64), PlanJSON: `{"reviewed":"before restart"}`}
	if err := s.ClaimAIContextSetup(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	oldLease := p.LeaseToken
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	s, err = Open(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	saved, err := s.GetAIContextSetup(t.Context(), r.ID)
	if err != nil || saved.PlanJSON != p.PlanJSON || saved.PlanHash != p.PlanHash {
		t.Fatalf("lost reviewed proposal: %+v %v", saved, err)
	}
	if err := s.ClaimAIContextSetup(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if p.LeaseToken == oldLease {
		t.Fatal("restart reused an expired lease")
	}
	if err := s.DeleteAIContextRepository(t.Context(), r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAIContextSetup(t.Context(), r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphaned setup: %v", err)
	}
}
