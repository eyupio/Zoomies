package store

import (
	"context"
	"database/sql"
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

func TestAnExplicitUIRepairCanRetryAFailureWithoutRepeatingAnActiveRepair(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first := &EliRepair{Repo: "acme/repo", UserID: "owner", DedupKey: "ui:repo:1:head:owner"}
	if added, err := s.EnqueueEliRepair(ctx, first, 5); err != nil || !added {
		t.Fatal(err)
	}
	duplicate := &EliRepair{Repo: first.Repo, UserID: first.UserID, DedupKey: first.DedupKey}
	if added, err := s.EnqueueEliRepair(ctx, duplicate, 5); err != nil || added || duplicate.ID != first.ID {
		t.Fatalf("duplicate %+v %v", duplicate, err)
	}
	first.State = "failed"
	if err := s.SaveEliRepair(ctx, first); err != nil {
		t.Fatal(err)
	}
	retry := &EliRepair{Repo: first.Repo, UserID: first.UserID, DedupKey: first.DedupKey}
	if added, err := s.EnqueueEliRepair(ctx, retry, 5); err != nil || !added || retry.ID == first.ID {
		t.Fatalf("retry %+v %v", retry, err)
	}
	duplicate = &EliRepair{Repo: first.Repo, UserID: first.UserID, DedupKey: first.DedupKey}
	if added, err := s.EnqueueEliRepair(ctx, duplicate, 5); err != nil || added || duplicate.ID != retry.ID {
		t.Fatalf("active retry duplicated %+v %v", duplicate, err)
	}
}
func TestPersonalRepairHistoryIsFilteredBeforeThePageLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	own := &EliRepair{Repo: "own/repo", UserID: "owner", DedupKey: "own"}
	s.EnqueueEliRepair(ctx, own, 1)
	for i := 0; i < 110; i++ {
		s.EnqueueEliRepair(ctx, &EliRepair{Repo: fmt.Sprint("repo/", i), UserID: fmt.Sprint("user", i), DedupKey: fmt.Sprint("other", i)}, 1)
	}
	rows, err := s.ListEliRepairsForOwner(ctx, "owner")
	if err != nil || len(rows) != 1 || rows[0].ID != own.ID {
		t.Fatalf("lost personal history: %v %v", rows, err)
	}
}
func TestEliMigrationPreservesExistingProviderOwnershipAndFleetConsent(t *testing.T) {
	ctx := context.Background()
	path := atSchemaBefore(t, "0093_eli_repairs.sql")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO assistant_providers(id,name,kind,model,enabled,is_default,fleet_access,owner_id,key_enc,created_at,updated_at) VALUES('shared','Model','openai','model',1,0,1,'',x'0102',1,2),('subscription','Plan','claude_code','sonnet',1,1,0,'owner',NULL,3,4)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	shared, err := s.GetAssistantProvider(ctx, "shared")
	if err != nil || shared.OwnerID != "" || !shared.FleetAccess || len(shared.KeyEnc) != 2 {
		t.Fatalf("lost shared provider: %+v %v", shared, err)
	}
	own, err := s.GetAssistantProvider(ctx, "subscription")
	if err != nil || own.OwnerID != "owner" || !own.IsDefault || own.Model != "sonnet" {
		t.Fatalf("lost subscription: %+v %v", own, err)
	}
}
