package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// A snooze exists so an operator stops being asked for a while. If it lived
// only in one browser, the second tab or the phone would ask again -- so it is
// the account's, and private to it.
func TestProblemDismissalsBelongToTheAccountAndSurviveReopening(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	alice := &User{Username: "alice", Role: RoleViewer}
	bob := &User{Username: "bob", Role: RoleViewer}
	for _, u := range []*User{alice, bob} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser(%s): %v", u.Username, err)
		}
	}
	until := now.Add(24 * time.Hour)
	err := s.ApplyProblemDismissals(ctx, alice.ID, []ProblemDismissal{
		{Key: "type:pool.no_capacity", Severity: "warning", DismissedAt: now, Until: &until},
		{Key: "auth.disabled|||", Severity: "error", DismissedAt: now},
	}, nil, 100)
	if err != nil {
		t.Fatalf("ApplyProblemDismissals: %v", err)
	}

	got, err := s.ProblemDismissals(ctx, alice.ID)
	if err != nil {
		t.Fatalf("ProblemDismissals(alice): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("alice holds %d dismissals, want 2: %+v", len(got), got)
	}
	var snooze *ProblemDismissal
	for i := range got {
		if got[i].Key == "type:pool.no_capacity" {
			snooze = &got[i]
		}
	}
	if snooze == nil || snooze.Until == nil || !snooze.Until.Equal(until) || snooze.Severity != "warning" {
		t.Fatalf("the snooze did not round-trip: %+v", snooze)
	}

	other, err := s.ProblemDismissals(ctx, bob.ID)
	if err != nil {
		t.Fatalf("ProblemDismissals(bob): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("bob inherited alice's dismissals: %+v", other)
	}
}

// Two tabs each put a different problem away. The second write must add to the
// first, not replace it -- that last-writer-wins overwrite is what the browser
// copy suffered from.
func TestApplyingProblemDismissalsAddsToWhatIsHeldAndReplacesTheSameKey(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	u := &User{Username: "alice", Role: RoleViewer}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	short := now.Add(15 * time.Minute)
	long := now.Add(24 * time.Hour)
	apply := func(set []ProblemDismissal, remove []string) {
		t.Helper()
		if err := s.ApplyProblemDismissals(ctx, u.ID, set, remove, 100); err != nil {
			t.Fatalf("ApplyProblemDismissals: %v", err)
		}
	}
	apply([]ProblemDismissal{{Key: "a", Severity: "info", DismissedAt: now, Until: &short}}, nil)
	apply([]ProblemDismissal{{Key: "b", Severity: "info", DismissedAt: now}}, nil)
	apply([]ProblemDismissal{{Key: "a", Severity: "warning", DismissedAt: now, Until: &long}}, nil)

	got, _ := s.ProblemDismissals(ctx, u.ID)
	if len(got) != 2 {
		t.Fatalf("held %d, want both tabs' decisions: %+v", len(got), got)
	}
	for _, d := range got {
		if d.Key == "a" && (d.Severity != "warning" || d.Until == nil || !d.Until.Equal(long)) {
			t.Fatalf("snoozing again did not replace the earlier snooze: %+v", d)
		}
	}

	apply(nil, []string{"a", "never-held"})
	got, _ = s.ProblemDismissals(ctx, u.ID)
	if len(got) != 1 || got[0].Key != "b" {
		t.Fatalf("after restoring a, held %+v, want only b", got)
	}
}

// A snooze is spent when its clock runs out, and it must not need a browser
// open at that moment to be so: the row is gone the next time anyone asks.
func TestAnExpiredSnoozeIsNotReturnedAndAPlainDismissalIsKept(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	u := &User{Username: "alice", Role: RoleViewer}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	until := now.Add(time.Hour)
	if err := s.ApplyProblemDismissals(ctx, u.ID, []ProblemDismissal{
		{Key: "snoozed", Severity: "info", DismissedAt: now, Until: &until},
		{Key: "plain", Severity: "info", DismissedAt: now},
	}, nil, 100); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Hour)
	got, err := s.ProblemDismissals(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "plain" {
		t.Fatalf("after the snooze ran out got %+v, want only the plain dismissal", got)
	}
	if n, _ := s.CountProblemDismissals(ctx, u.ID); n != 1 {
		t.Fatalf("the expired snooze row was left behind: %d rows", n)
	}
}

func TestDeletingAnAccountDeletesItsDismissals(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := &User{Username: "alice", Role: RoleViewer}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyProblemDismissals(ctx, u.ID,
		[]ProblemDismissal{{Key: "a", Severity: "info", DismissedAt: s.Now()}}, nil, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountProblemDismissals(ctx, u.ID); n != 0 {
		t.Fatalf("%d dismissals outlived their account", n)
	}
}

// The limit is on what the account is left holding. A removal of keys that were
// never there frees no room, so padding a request with them must not let it
// past a limit that counted on them -- and a refused change leaves nothing of
// itself behind.
func TestTheDismissalLimitIsCheckedOnTheRowsLeftAfterTheChange(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	u := &User{Username: "alice", Role: RoleViewer}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	keys := func(prefix string, n int) []ProblemDismissal {
		out := make([]ProblemDismissal, n)
		for i := range out {
			out[i] = ProblemDismissal{Key: fmt.Sprintf("%s%d", prefix, i), Severity: "info", DismissedAt: s.Now()}
		}
		return out
	}
	if err := s.ApplyProblemDismissals(ctx, u.ID, keys("a", 3), nil, 3); err != nil {
		t.Fatalf("filling the account to its limit: %v", err)
	}

	err := s.ApplyProblemDismissals(ctx, u.ID, keys("b", 3), []string{"never-held-1", "never-held-2", "never-held-3"}, 3)
	if !errors.Is(err, ErrTooManyDismissals) {
		t.Fatalf("padding with removals that free nothing got %v, want ErrTooManyDismissals", err)
	}
	if n, _ := s.CountProblemDismissals(ctx, u.ID); n != 3 {
		t.Fatalf("a refused change left %d rows, want the original 3", n)
	}

	// Removing real rows in the same request does make room.
	if err := s.ApplyProblemDismissals(ctx, u.ID, keys("b", 3), []string{"a0", "a1", "a2"}, 3); err != nil {
		t.Fatalf("replacing what is held within the limit: %v", err)
	}
}
