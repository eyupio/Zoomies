package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRepairQueueDeduplicatesBeforeChargingAndKeepsInterruptedWritesStopped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := &EliRepair{Repo: "acme/repo", DedupKey: "comment:1"}
	added, e := s.EnqueueEliRepair(ctx, r, 1)
	if e != nil || !added {
		t.Fatalf("enqueue %v %v", added, e)
	}
	duplicate := &EliRepair{Repo: r.Repo, DedupKey: r.DedupKey}
	added, e = s.EnqueueEliRepair(ctx, duplicate, 1)
	if e != nil || added || duplicate.ID != r.ID {
		t.Fatalf("duplicate %+v %v %v", duplicate, added, e)
	}
	_, e = s.EnqueueEliRepair(ctx, &EliRepair{Repo: r.Repo, DedupKey: "other"}, 1)
	if !errors.Is(e, ErrConflict) {
		t.Fatalf("budget %v", e)
	}
	r.State = "working"
	r.CommitSHA = "fix"
	if e = s.SaveEliRepair(ctx, r); e != nil {
		t.Fatal(e)
	}
	if e = s.InterruptEliRepairs(ctx); e != nil {
		t.Fatal(e)
	}
	_, e = s.NextEliRepair(ctx)
	if !errors.Is(e, ErrNotFound) {
		t.Fatalf("repeated interrupted repair: %v", e)
	}
	for i := 0; i < 105; i++ {
		_, e = s.EnqueueEliRepair(ctx, &EliRepair{Repo: fmt.Sprintf("acme/%d", i), DedupKey: fmt.Sprint(i)}, 1)
		if e != nil {
			t.Fatal(e)
		}
	}
	own, e := s.IsEliRepairCommit(ctx, r.Repo, "fix")
	if e != nil || !own {
		t.Fatal("old repair commit lost loop protection")
	}
}
func TestPersonalProviderDefaultsAreIndependentAndNamesAreScoped(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, owner := range []string{"", "usr_one", "usr_two"} {
		p := &AssistantProvider{OwnerID: owner, Name: "Model", Kind: "openai", Enabled: true}
		if e := s.CreateAssistantProvider(ctx, p); e != nil {
			t.Fatal(e)
		}
		if e := s.SetDefaultAssistantProvider(ctx, p.ID); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := s.ListAssistantProviders(ctx)
	if e != nil || len(rows) != 3 {
		t.Fatalf("%v %v", rows, e)
	}
	for _, r := range rows {
		if !r.IsDefault {
			t.Errorf("lost default for %q", r.OwnerID)
		}
	}
}
func TestCheckingRepairsRemainVisibleBeyondTheHistoryLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := &EliRepair{Repo: "acme/check", DedupKey: "check"}
	s.EnqueueEliRepair(ctx, r, 1)
	r.State = "checking"
	s.SaveEliRepair(ctx, r)
	for i := 0; i < 105; i++ {
		s.EnqueueEliRepair(ctx, &EliRepair{Repo: fmt.Sprint(i), DedupKey: fmt.Sprint(i)}, 1)
	}
	rows, e := s.CheckingEliRepairs(ctx)
	if e != nil || len(rows) != 1 || rows[0].ID != r.ID {
		t.Fatalf("checking rows %v %v", rows, e)
	}
}

func TestGitHubRepairConsentIsPinnedAndClearedWhenAnAdministratorChangesTheLink(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user := &User{Username: "owner", Role: RoleViewer}
	if e := s.CreateUser(ctx, user); e != nil {
		t.Fatal(e)
	}
	link := EliIdentity{UserID: user.ID, GitHubUserID: 42, GitHubLogin: "octo"}
	s.SetEliIdentity(ctx, link)
	rows, _ := s.EliIdentities(ctx)
	if rows[0].Confirmed {
		t.Fatal("admin enabled personal provider use")
	}
	if e := s.ConfirmEliIdentity(ctx, user.ID, 43, true); !errors.Is(e, ErrNotFound) {
		t.Fatalf("stale identity: %v", e)
	}
	if e := s.ConfirmEliIdentity(ctx, user.ID, 42, true); e != nil {
		t.Fatal(e)
	}
	rows, _ = s.EliIdentities(ctx)
	if !rows[0].Confirmed {
		t.Fatal("owner confirmation not kept")
	}
	s.SetEliIdentity(ctx, link)
	rows, _ = s.EliIdentities(ctx)
	if rows[0].Confirmed {
		t.Fatal("admin retained an earlier confirmation")
	}
	p := &AssistantProvider{OwnerID: user.ID, Name: "Personal", Kind: "openai"}
	s.CreateAssistantProvider(ctx, p)
	s.DeleteUser(ctx, user.ID)
	if _, e := s.GetAssistantProvider(ctx, p.ID); !errors.Is(e, ErrNotFound) {
		t.Fatalf("deleted account retained provider: %v", e)
	}
}
