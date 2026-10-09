package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

var updateAttemptsNow = time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)

// updateClock is a clock the test moves, so that a finished_at is a fact the
// test can name rather than a reading of the machine it ran on.
type updateClock struct{ at time.Time }

func (c *updateClock) now() time.Time { return c.at }

func updateAttemptStore(t *testing.T) (*Store, *updateClock) {
	t.Helper()
	clock := &updateClock{at: updateAttemptsNow}
	return newTestStoreAt(t, clock.now), clock
}

func controllerAttempt() *UpdateAttempt {
	return &UpdateAttempt{
		Scope: UpdateScopeController, FromVersion: "1.3.4", ToVersion: "v1.3.5",
		Trigger: UpdateTriggerManual, RequestedBy: "usr_1",
	}
}

func hostAttempt(hostID string) *UpdateAttempt {
	return &UpdateAttempt{
		Scope: UpdateScopeHost, HostID: hostID, FromVersion: "1.3.4", ToVersion: "v1.3.5",
		Trigger: UpdateTriggerAuto,
	}
}

func mustCreateAttempt(t *testing.T, s *Store, a *UpdateAttempt) *UpdateAttempt {
	t.Helper()
	if err := s.CreateUpdateAttempt(context.Background(), a); err != nil {
		t.Fatalf("CreateUpdateAttempt: %v", err)
	}
	return a
}

// Two actors pressing the button together, or the planner and a button, must
// not both get a request for one target: the helper runs one at a time. The
// rule is the table's, so it holds however the two callers interleave.
func TestOnlyOneUpdateAttemptIsOpenPerTarget(t *testing.T) {
	ctx := context.Background()
	s, _ := updateAttemptStore(t)

	first := mustCreateAttempt(t, s, controllerAttempt())
	if first.ID == "" || first.State != UpdateRequested || !first.RequestedAt.Equal(updateAttemptsNow) {
		t.Fatalf("a created attempt = %+v, want an id, state requested and the store's clock", first)
	}

	if err := s.CreateUpdateAttempt(ctx, controllerAttempt()); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second open controller attempt = %v, want ErrConflict", err)
	}

	// Each host is its own target, and the controller's open attempt is not
	// one of theirs.
	mustCreateAttempt(t, s, hostAttempt("host_a"))
	mustCreateAttempt(t, s, hostAttempt("host_b"))
	if err := s.CreateUpdateAttempt(ctx, hostAttempt("host_a")); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second open attempt for one host = %v, want ErrConflict", err)
	}

	// A finished attempt is history, and the target may be asked again.
	if ok, err := s.FinishUpdateAttempt(ctx, first.ID, UpdateFailed, "the helper reported an error"); err != nil || !ok {
		t.Fatalf("FinishUpdateAttempt = %v, %v, want true", ok, err)
	}
	mustCreateAttempt(t, s, controllerAttempt())

	open, err := s.OpenUpdateAttempts(ctx)
	if err != nil || len(open) != 3 {
		t.Fatalf("OpenUpdateAttempts = %d rows (%v), want the new controller attempt and two hosts", len(open), err)
	}
}

func TestAnAttemptMustNameItsTargetConsistently(t *testing.T) {
	ctx := context.Background()
	s, _ := updateAttemptStore(t)

	for name, a := range map[string]*UpdateAttempt{
		"a host attempt with no host":      {Scope: UpdateScopeHost, ToVersion: "v1.3.5", Trigger: UpdateTriggerManual},
		"a controller attempt with a host": {Scope: UpdateScopeController, HostID: "host_a", ToVersion: "v1.3.5", Trigger: UpdateTriggerManual},
		"an unknown scope":                 {Scope: "fleet", ToVersion: "v1.3.5", Trigger: UpdateTriggerManual},
		"an unknown trigger":               {Scope: UpdateScopeController, ToVersion: "v1.3.5", Trigger: "cron"},
	} {
		if err := s.CreateUpdateAttempt(ctx, a); err == nil || errors.Is(err, ErrConflict) {
			t.Errorf("%s: CreateUpdateAttempt = %v, want a refusal that is not a conflict", name, err)
		}
	}
}

// A late or repeated report must not rewrite how an attempt ended: a
// result.json read twice, or a timeout racing the helper's answer, would
// otherwise turn a success into a failure after the planner had acted on it.
func TestFinishingAnAttemptIsMonotonic(t *testing.T) {
	ctx := context.Background()
	s, clock := updateAttemptStore(t)
	a := mustCreateAttempt(t, s, controllerAttempt())

	clock.at = updateAttemptsNow.Add(time.Minute)
	if ok, err := s.FinishUpdateAttempt(ctx, a.ID, UpdateSucceeded, ""); err != nil || !ok {
		t.Fatalf("first FinishUpdateAttempt = %v, %v, want true", ok, err)
	}
	clock.at = updateAttemptsNow.Add(time.Hour)
	if ok, err := s.FinishUpdateAttempt(ctx, a.ID, UpdateTimedOut, "gave up waiting"); err != nil || ok {
		t.Fatalf("second FinishUpdateAttempt = %v, %v, want false", ok, err)
	}

	got, err := s.ListUpdateAttempts(ctx, UpdateScopeController, "", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListUpdateAttempts = %d rows (%v), want 1", len(got), err)
	}
	if got[0].State != UpdateSucceeded || got[0].Error != "" {
		t.Errorf("state = %q, error = %q, want the first finish to stand", got[0].State, got[0].Error)
	}
	if got[0].FinishedAt == nil || !got[0].FinishedAt.Equal(updateAttemptsNow.Add(time.Minute)) {
		t.Errorf("finished at %v, want the moment of the first finish", got[0].FinishedAt)
	}

	if ok, err := s.FinishUpdateAttempt(ctx, "upd_nothere", UpdateFailed, "x"); err != nil || ok {
		t.Errorf("finishing an unknown attempt = %v, %v, want false and no error", ok, err)
	}
	if _, err := s.FinishUpdateAttempt(ctx, a.ID, UpdateRequested, ""); err == nil {
		t.Error("finishing an attempt as requested was accepted; only an end state closes one")
	}
}

// A host that goes away mid-rollout leaves an attempt nobody will ever report
// on. It closes in the same transaction as the delete, so there is no moment
// at which the host is gone and its attempt still reads as in flight.
func TestDeletingAHostClosesItsOpenAttemptsAsCancelled(t *testing.T) {
	ctx := context.Background()
	s, clock := updateAttemptStore(t)
	host := &Host{Name: "vm-1", Capacity: 2, Backends: StringSlice{"docker"}}
	other := &Host{Name: "vm-2", Capacity: 2, Backends: StringSlice{"docker"}}
	for _, h := range []*Host{host, other} {
		if err := s.CreateHost(ctx, h); err != nil {
			t.Fatalf("CreateHost: %v", err)
		}
	}
	gone := mustCreateAttempt(t, s, hostAttempt(host.ID))
	kept := mustCreateAttempt(t, s, hostAttempt(other.ID))
	controller := mustCreateAttempt(t, s, controllerAttempt())

	clock.at = updateAttemptsNow.Add(5 * time.Minute)
	if _, err := s.DeleteHostForgettingMachine(ctx, host.ID, ""); err != nil {
		t.Fatalf("DeleteHostForgettingMachine: %v", err)
	}

	all, err := s.ListUpdateAttempts(ctx, "", "", 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListUpdateAttempts = %d rows (%v), want 3", len(all), err)
	}
	byID := map[string]UpdateAttempt{}
	for _, a := range all {
		byID[a.ID] = a
	}
	if a := byID[gone.ID]; a.State != UpdateCancelled || a.Error == "" || a.FinishedAt == nil || !a.FinishedAt.Equal(clock.at) {
		t.Errorf("the deleted host's attempt = %+v, want cancelled with a reason and the delete's time", a)
	}
	if a := byID[kept.ID]; a.State != UpdateRequested {
		t.Errorf("another host's attempt = %q, want it left open", a.State)
	}
	if a := byID[controller.ID]; a.State != UpdateRequested {
		t.Errorf("the controller's attempt = %q, want it left open", a.State)
	}

	// The plain DeleteHost is the same delete.
	if _, err := s.DeleteHost(ctx, other.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if a, err := s.ListUpdateAttempts(ctx, UpdateScopeHost, other.ID, 10); err != nil || len(a) != 1 || a[0].State != UpdateCancelled {
		t.Errorf("after DeleteHost the second host's attempts = %+v (%v), want one cancelled", a, err)
	}
}

// A host that is removed and joins again under the same id keeps what was
// done to it. A foreign key would either have refused the delete or taken the
// history with it.
func TestAReJoinKeepsAHostsAttemptHistory(t *testing.T) {
	ctx := context.Background()
	s, _ := updateAttemptStore(t)
	host := &Host{Name: "vm-1", Capacity: 2, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	a := mustCreateAttempt(t, s, hostAttempt(host.ID))
	if ok, err := s.FinishUpdateAttempt(ctx, a.ID, UpdateSucceeded, ""); err != nil || !ok {
		t.Fatalf("FinishUpdateAttempt = %v, %v", ok, err)
	}

	if _, err := s.DeleteHost(ctx, host.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	again := &Host{ID: host.ID, Name: "vm-1", Capacity: 2, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(ctx, again); err != nil {
		t.Fatalf("CreateHost under the same id: %v", err)
	}

	got, err := s.ListUpdateAttempts(ctx, UpdateScopeHost, host.ID, 10)
	if err != nil || len(got) != 1 || got[0].ID != a.ID || got[0].State != UpdateSucceeded {
		t.Fatalf("the re-joined host's history = %+v (%v), want its finished attempt untouched", got, err)
	}
}

// An open attempt is the only record that something is in flight. Age never
// makes it history; the controller's timeout does, by finishing it.
func TestPruneSparesOpenAttempts(t *testing.T) {
	ctx := context.Background()
	s, clock := updateAttemptStore(t)

	open := mustCreateAttempt(t, s, controllerAttempt())
	done := mustCreateAttempt(t, s, hostAttempt("host_a"))
	recent := mustCreateAttempt(t, s, hostAttempt("host_b"))

	clock.at = updateAttemptsNow.Add(time.Minute)
	if ok, err := s.FinishUpdateAttempt(ctx, done.ID, UpdateFailed, "no space left"); err != nil || !ok {
		t.Fatalf("FinishUpdateAttempt = %v, %v", ok, err)
	}
	clock.at = updateAttemptsNow.Add(100 * 24 * time.Hour)
	if ok, err := s.FinishUpdateAttempt(ctx, recent.ID, UpdateSucceeded, ""); err != nil || !ok {
		t.Fatalf("FinishUpdateAttempt = %v, %v", ok, err)
	}

	n, err := s.PruneUpdateAttempts(ctx, updateAttemptsNow.Add(50*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneUpdateAttempts = %d, %v, want the one old finished attempt", n, err)
	}
	left, err := s.ListUpdateAttempts(ctx, "", "", 10)
	if err != nil || len(left) != 2 {
		t.Fatalf("after the prune %d attempts remain (%v), want the open one and the recent one", len(left), err)
	}
	for _, a := range left {
		if a.ID == done.ID {
			t.Errorf("the old finished attempt survived the prune")
		}
	}
	if still, err := s.OpenUpdateAttempts(ctx); err != nil || len(still) != 1 || still[0].ID != open.ID {
		t.Errorf("OpenUpdateAttempts = %+v (%v), want the old open attempt kept", still, err)
	}
}

func TestUpdateAttemptsListNewestFirstForOneTargetOrAll(t *testing.T) {
	ctx := context.Background()
	s, clock := updateAttemptStore(t)

	var ids []string
	for i := 0; i < 3; i++ {
		clock.at = updateAttemptsNow.Add(time.Duration(i) * time.Hour)
		a := mustCreateAttempt(t, s, hostAttempt("host_a"))
		ids = append(ids, a.ID)
		if ok, err := s.FinishUpdateAttempt(ctx, a.ID, UpdateFailed, "x"); err != nil || !ok {
			t.Fatalf("FinishUpdateAttempt = %v, %v", ok, err)
		}
	}
	mustCreateAttempt(t, s, hostAttempt("host_b"))
	mustCreateAttempt(t, s, controllerAttempt())

	got, err := s.ListUpdateAttempts(ctx, UpdateScopeHost, "host_a", 2)
	if err != nil || len(got) != 2 || got[0].ID != ids[2] || got[1].ID != ids[1] {
		t.Fatalf("host_a's two newest = %+v (%v), want %s then %s", got, err, ids[2], ids[1])
	}
	if got, err = s.ListUpdateAttempts(ctx, UpdateScopeHost, "", 10); err != nil || len(got) != 4 {
		t.Errorf("every host attempt = %d rows (%v), want 4", len(got), err)
	}
	if got, err = s.ListUpdateAttempts(ctx, "", "", 10); err != nil || len(got) != 5 {
		t.Errorf("every attempt = %d rows (%v), want 5", len(got), err)
	}
}
