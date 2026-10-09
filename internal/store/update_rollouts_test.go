package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"
)

func rolloutStore(t *testing.T) (*Store, *updateClock) {
	t.Helper()
	return updateAttemptStore(t)
}

func newRollout() *UpdateRollout {
	return &UpdateRollout{Target: "v1.3.5", Trigger: UpdateTriggerManual, StartedBy: "usr_1"}
}

func mustCreateRollout(t *testing.T, s *Store, r *UpdateRollout) *UpdateRollout {
	t.Helper()
	if err := s.CreateUpdateRollout(context.Background(), r); err != nil {
		t.Fatalf("CreateUpdateRollout: %v", err)
	}
	return r
}

// rolloutByID reads a rollout whatever its state; the store's own readers
// answer for the open one only.
func rolloutByID(s *Store, id string) (UpdateRollout, error) {
	return scanUpdateRollout(s.read.QueryRowContext(context.Background(),
		`SELECT `+updateRolloutCols+` FROM update_rollouts WHERE id = ?`, id))
}

func mustRollout(t *testing.T, s *Store, id string) UpdateRollout {
	t.Helper()
	got, err := rolloutByID(s, id)
	if err != nil {
		t.Fatalf("reading rollout %s: %v", id, err)
	}
	return got
}

// Two rollouts walking the fleet at once would each decide the next host from
// a different picture of it, so a second never starts while one is running or
// halted. The rule is the table's, so it holds however two callers interleave.
func TestOnlyOneRolloutIsOpenAtATime(t *testing.T) {
	ctx := context.Background()
	s, _ := rolloutStore(t)

	first := mustCreateRollout(t, s, newRollout())
	if first.ID == "" || first.State != RolloutRunning || !first.StartedAt.Equal(updateAttemptsNow) || first.FinishedAt != nil {
		t.Fatalf("a created rollout = %+v, want an id, state running, the store's clock and no end", first)
	}

	if err := s.CreateUpdateRollout(ctx, newRollout()); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second rollout while one is running = %v, want ErrConflict", err)
	}

	// A halted rollout is still open: it is waiting for an operator, and a new
	// one must not slip past it.
	if ok, err := s.HaltUpdateRollout(ctx, first.ID, "vm-2 did not come back"); err != nil || !ok {
		t.Fatalf("HaltUpdateRollout = %v, %v, want true", ok, err)
	}
	if err := s.CreateUpdateRollout(ctx, newRollout()); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second rollout while one is halted = %v, want ErrConflict", err)
	}

	// Finished rollouts are history, and the fleet may be rolled out again.
	if ok, err := s.FinishUpdateRollout(ctx, first.ID, RolloutCancelled); err != nil || !ok {
		t.Fatalf("FinishUpdateRollout = %v, %v, want true", ok, err)
	}
	second := mustCreateRollout(t, s, newRollout())
	if second.ID == first.ID {
		t.Errorf("a second rollout reused the id %s", second.ID)
	}
}

func TestARolloutMustNameItsTargetAndHowItStarted(t *testing.T) {
	ctx := context.Background()
	s, _ := rolloutStore(t)
	for name, r := range map[string]*UpdateRollout{
		"no target":      {Trigger: UpdateTriggerManual},
		"a blank target": {Target: "  ", Trigger: UpdateTriggerAuto},
		"an odd trigger": {Target: "v1.3.5", Trigger: "cron"},
	} {
		if err := s.CreateUpdateRollout(ctx, r); err == nil || errors.Is(err, ErrConflict) {
			t.Errorf("%s: CreateUpdateRollout = %v, want a refusal that is not a conflict", name, err)
		}
	}
	if _, err := s.OpenUpdateRollout(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("after refused creates OpenUpdateRollout = %v, want ErrNotFound", err)
	}
}

func TestTheOpenRolloutIsTheRunningOrHaltedOne(t *testing.T) {
	ctx := context.Background()
	s, _ := rolloutStore(t)

	if _, err := s.OpenUpdateRollout(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenUpdateRollout with none = %v, want ErrNotFound", err)
	}
	r := mustCreateRollout(t, s, newRollout())
	got, err := s.OpenUpdateRollout(ctx)
	if err != nil || got.ID != r.ID || got.State != RolloutRunning {
		t.Fatalf("OpenUpdateRollout = %+v (%v), want the running rollout", got, err)
	}
	if ok, err := s.HaltUpdateRollout(ctx, r.ID, "vm-2 did not come back"); err != nil || !ok {
		t.Fatalf("HaltUpdateRollout = %v, %v", ok, err)
	}
	got, err = s.OpenUpdateRollout(ctx)
	if err != nil || got.ID != r.ID || got.State != RolloutHalted || got.HaltedReason != "vm-2 did not come back" {
		t.Fatalf("OpenUpdateRollout = %+v (%v), want the halted rollout and its reason", got, err)
	}
	if ok, err := s.FinishUpdateRollout(ctx, r.ID, RolloutDone); err != nil || !ok {
		t.Fatalf("FinishUpdateRollout = %v, %v", ok, err)
	}
	if _, err := s.OpenUpdateRollout(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("OpenUpdateRollout after the end = %v, want ErrNotFound", err)
	}
}

// Halting and resuming race the planner's own pass, and an operator's click
// can arrive late. Each is a move out of one named state, so the loser learns
// it lost instead of overwriting the winner.
func TestHaltingAndResumingAreGuardedByState(t *testing.T) {
	ctx := context.Background()
	s, clock := rolloutStore(t)
	r := mustCreateRollout(t, s, newRollout())

	if ok, err := s.ResumeUpdateRollout(ctx, r.ID); err != nil || ok {
		t.Fatalf("resuming a running rollout = %v, %v, want false and no error", ok, err)
	}
	if ok, err := s.HaltUpdateRollout(ctx, r.ID, "vm-2 did not come back"); err != nil || !ok {
		t.Fatalf("first halt = %v, %v, want true", ok, err)
	}
	if ok, err := s.HaltUpdateRollout(ctx, r.ID, "a different reason"); err != nil || ok {
		t.Fatalf("halting a halted rollout = %v, %v, want false and no error", ok, err)
	}
	if got := mustRollout(t, s, r.ID); got.HaltedReason != "vm-2 did not come back" {
		t.Errorf("reason = %q, want the first halt's to stand", got.HaltedReason)
	}
	if ok, err := s.ResumeUpdateRollout(ctx, r.ID); err != nil || !ok {
		t.Fatalf("resuming a halted rollout = %v, %v, want true", ok, err)
	}
	if got := mustRollout(t, s, r.ID); got.State != RolloutRunning || got.HaltedReason != "" {
		t.Errorf("after resume = %+v, want running and no reason left over", got)
	}

	// A finished rollout can be neither halted nor resumed.
	clock.at = updateAttemptsNow.Add(time.Hour)
	if ok, err := s.FinishUpdateRollout(ctx, r.ID, RolloutDone); err != nil || !ok {
		t.Fatalf("FinishUpdateRollout = %v, %v", ok, err)
	}
	if ok, err := s.HaltUpdateRollout(ctx, r.ID, "late"); err != nil || ok {
		t.Errorf("halting a done rollout = %v, %v, want false", ok, err)
	}
	if ok, err := s.ResumeUpdateRollout(ctx, r.ID); err != nil || ok {
		t.Errorf("resuming a done rollout = %v, %v, want false", ok, err)
	}
	if ok, err := s.HaltUpdateRollout(ctx, "rol_nothere", "x"); err != nil || ok {
		t.Errorf("halting an unknown rollout = %v, %v, want false and no error", ok, err)
	}
	if got := mustRollout(t, s, r.ID); got.State != RolloutDone {
		t.Errorf("state = %q, want done to stand", got.State)
	}
}

// A rollout that has ended stays ended: a late cancel from a second tab, or a
// finish from a pass that read the rollout before it ended, must not rewrite
// how it went or when.
func TestFinishingARolloutIsFinal(t *testing.T) {
	ctx := context.Background()
	s, clock := rolloutStore(t)
	r := mustCreateRollout(t, s, newRollout())

	clock.at = updateAttemptsNow.Add(time.Minute)
	if ok, err := s.FinishUpdateRollout(ctx, r.ID, RolloutDone); err != nil || !ok {
		t.Fatalf("first FinishUpdateRollout = %v, %v, want true", ok, err)
	}
	clock.at = updateAttemptsNow.Add(time.Hour)
	if ok, err := s.FinishUpdateRollout(ctx, r.ID, RolloutCancelled); err != nil || ok {
		t.Fatalf("second FinishUpdateRollout = %v, %v, want false", ok, err)
	}
	got := mustRollout(t, s, r.ID)
	if got.State != RolloutDone {
		t.Errorf("state = %q, want the first finish to stand", got.State)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(updateAttemptsNow.Add(time.Minute)) {
		t.Errorf("finished at %v, want the moment of the first finish", got.FinishedAt)
	}

	if ok, err := s.FinishUpdateRollout(ctx, "rol_nothere", RolloutDone); err != nil || ok {
		t.Errorf("finishing an unknown rollout = %v, %v, want false and no error", ok, err)
	}
	for _, state := range []string{RolloutRunning, RolloutHalted, "paused"} {
		if _, err := s.FinishUpdateRollout(ctx, r.ID, state); err == nil {
			t.Errorf("finishing a rollout as %q was accepted; only done or cancelled ends one", state)
		}
	}

	// A halted rollout can be cancelled: that is how an operator gives up on it.
	next := mustCreateRollout(t, s, newRollout())
	if ok, err := s.HaltUpdateRollout(ctx, next.ID, "vm-2 did not come back"); err != nil || !ok {
		t.Fatalf("HaltUpdateRollout = %v, %v", ok, err)
	}
	if ok, err := s.FinishUpdateRollout(ctx, next.ID, RolloutCancelled); err != nil || !ok {
		t.Fatalf("cancelling a halted rollout = %v, %v, want true", ok, err)
	}
	if got := mustRollout(t, s, next.ID); got.State != RolloutCancelled || got.HaltedReason != "vm-2 did not come back" {
		t.Errorf("a cancelled rollout = %+v, want cancelled with the reason it had stopped for kept as history", got)
	}
}

// An open rollout is the only record that the fleet is mid-update. Age never
// makes it history; an operator or the planner does, by finishing it.
func TestPruneSparesAnOpenRollout(t *testing.T) {
	ctx := context.Background()
	s, clock := rolloutStore(t)

	old := mustCreateRollout(t, s, newRollout())
	clock.at = updateAttemptsNow.Add(time.Minute)
	if ok, err := s.FinishUpdateRollout(ctx, old.ID, RolloutDone); err != nil || !ok {
		t.Fatalf("FinishUpdateRollout = %v, %v", ok, err)
	}
	halted := mustCreateRollout(t, s, newRollout())
	if ok, err := s.HaltUpdateRollout(ctx, halted.ID, "vm-2 did not come back"); err != nil || !ok {
		t.Fatalf("HaltUpdateRollout = %v, %v", ok, err)
	}

	clock.at = updateAttemptsNow.Add(200 * 24 * time.Hour)
	n, err := s.PruneUpdateRollouts(ctx, updateAttemptsNow.Add(100*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneUpdateRollouts = %d, %v, want the one finished rollout", n, err)
	}
	if _, err := rolloutByID(s, old.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the old finished rollout survived the prune: %v", err)
	}
	if got, err := s.OpenUpdateRollout(ctx); err != nil || got.ID != halted.ID {
		t.Errorf("OpenUpdateRollout = %+v (%v), want the halted rollout kept however old", got, err)
	}

	// Running ones are spared too.
	if ok, err := s.ResumeUpdateRollout(ctx, halted.ID); err != nil || !ok {
		t.Fatalf("ResumeUpdateRollout = %v, %v", ok, err)
	}
	if n, err := s.PruneUpdateRollouts(ctx, updateAttemptsNow.Add(300*24*time.Hour)); err != nil || n != 0 {
		t.Errorf("PruneUpdateRollouts = %d, %v, want a running rollout kept", n, err)
	}
}

// Attempts are what a rollout is made of, and the page that explains a
// rollout lists them, so each remembers which one asked for it. A lone button
// press has none.
func TestAnAttemptRemembersItsRollout(t *testing.T) {
	ctx := context.Background()
	s, _ := rolloutStore(t)
	r := mustCreateRollout(t, s, newRollout())

	part := hostAttempt("host_a")
	part.RolloutID = r.ID
	mustCreateAttempt(t, s, part)
	alone := mustCreateAttempt(t, s, hostAttempt("host_b"))

	got, err := s.ListUpdateAttempts(ctx, UpdateScopeHost, "host_a", 10)
	if err != nil || len(got) != 1 || got[0].RolloutID != r.ID {
		t.Fatalf("the rollout's attempt = %+v (%v), want it to carry %s", got, err, r.ID)
	}
	got, err = s.ListUpdateAttempts(ctx, UpdateScopeHost, "host_b", 10)
	if err != nil || len(got) != 1 || got[0].ID != alone.ID || got[0].RolloutID != "" {
		t.Fatalf("a button press's attempt = %+v (%v), want no rollout", got, err)
	}
	open, err := s.OpenUpdateAttempts(ctx)
	if err != nil || len(open) != 2 {
		t.Fatalf("OpenUpdateAttempts = %d rows (%v), want 2", len(open), err)
	}
}

// A host that goes away mid-rollout has its attempt cancelled, but the rollout
// is the controller's to judge: it sees the cancelled attempt and halts. The
// delete itself must not decide that.
func TestDeletingAHostLeavesTheRolloutAlone(t *testing.T) {
	ctx := context.Background()
	s, _ := rolloutStore(t)
	host := &Host{Name: "vm-1", Capacity: 2, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	r := mustCreateRollout(t, s, newRollout())
	a := hostAttempt(host.ID)
	a.RolloutID = r.ID
	mustCreateAttempt(t, s, a)

	if _, err := s.DeleteHostForgettingMachine(ctx, host.ID, ""); err != nil {
		t.Fatalf("DeleteHostForgettingMachine: %v", err)
	}
	if got := mustRollout(t, s, r.ID); got.State != RolloutRunning || got.FinishedAt != nil {
		t.Errorf("after the delete the rollout = %+v, want it still running", got)
	}
	got, err := s.ListUpdateAttempts(ctx, UpdateScopeHost, host.ID, 10)
	if err != nil || len(got) != 1 || got[0].RolloutID != r.ID || got[0].State != UpdateCancelled {
		t.Errorf("the host's attempt = %+v (%v), want cancelled and still naming its rollout", got, err)
	}
}

// A rollout started for named hosts must remember them, or the planner would
// walk it over every host that is behind.
func TestARolloutRemembersTheHostsItWasStartedFor(t *testing.T) {
	s, _ := rolloutStore(t)
	named := newRollout()
	named.HostIDs = StringSlice{"host_b", "host_a"}
	mustCreateRollout(t, s, named)
	if got := mustRollout(t, s, named.ID); !slices.Equal(got.HostIDs, []string{"host_b", "host_a"}) {
		t.Errorf("host ids = %v, want the two it was started for", got.HostIDs)
	}
	if _, err := s.FinishUpdateRollout(context.Background(), named.ID, RolloutDone); err != nil {
		t.Fatal(err)
	}
	every := mustCreateRollout(t, s, newRollout())
	if got := mustRollout(t, s, every.ID); len(got.HostIDs) != 0 {
		t.Errorf("host ids = %v, want none: a rollout of every host behind", got.HostIDs)
	}
}

// The failure a rollout halted on happened before it was resumed; the planner
// reads that from resumed_at, so a resume that did not move it would halt the
// rollout again on the next pass.
func TestResumingARolloutStampsWhenItResumed(t *testing.T) {
	ctx := context.Background()
	s, clock := rolloutStore(t)
	r := mustCreateRollout(t, s, newRollout())
	if got := mustRollout(t, s, r.ID); !got.ResumedAt.Equal(updateAttemptsNow) {
		t.Fatalf("resumed at %v, want the start", got.ResumedAt)
	}
	if ok, err := s.HaltUpdateRollout(ctx, r.ID, "vm-2 did not come back"); err != nil || !ok {
		t.Fatalf("HaltUpdateRollout = %v, %v", ok, err)
	}
	clock.at = updateAttemptsNow.Add(time.Hour)
	if ok, err := s.ResumeUpdateRollout(ctx, r.ID); err != nil || !ok {
		t.Fatalf("ResumeUpdateRollout = %v, %v", ok, err)
	}
	got := mustRollout(t, s, r.ID)
	if !got.ResumedAt.Equal(updateAttemptsNow.Add(time.Hour)) || !got.StartedAt.Equal(updateAttemptsNow) {
		t.Errorf("after the resume started %v and resumed %v, want the start kept and the resume stamped", got.StartedAt, got.ResumedAt)
	}
}

// A person's cancel is a decision auto honours, and the planner's own is not,
// so who cancelled is kept; and the last ended rollout is what the planner
// reads it from.
func TestACancelByAPersonIsRecordedAndTheLastEndedRolloutIsFound(t *testing.T) {
	ctx := context.Background()
	s, clock := rolloutStore(t)
	if _, err := s.LastEndedUpdateRollout(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LastEndedUpdateRollout with none = %v, want ErrNotFound", err)
	}
	first := mustCreateRollout(t, s, newRollout())
	clock.at = updateAttemptsNow.Add(time.Minute)
	if ok, err := s.FinishUpdateRollout(ctx, first.ID, RolloutCancelled); err != nil || !ok {
		t.Fatalf("FinishUpdateRollout = %v, %v", ok, err)
	}
	second := mustCreateRollout(t, s, newRollout())
	if _, err := s.CancelUpdateRollout(ctx, second.ID, ""); err == nil {
		t.Error("a cancel naming nobody was accepted")
	}
	clock.at = updateAttemptsNow.Add(time.Hour)
	if ok, err := s.CancelUpdateRollout(ctx, second.ID, "alice"); err != nil || !ok {
		t.Fatalf("CancelUpdateRollout = %v, %v", ok, err)
	}
	if ok, err := s.CancelUpdateRollout(ctx, second.ID, "bob"); err != nil || ok {
		t.Errorf("a second cancel = %v, %v, want false: the first ending stands", ok, err)
	}
	got, err := s.LastEndedUpdateRollout(ctx)
	if err != nil || got.ID != second.ID || got.State != RolloutCancelled || got.CancelledBy != "alice" {
		t.Fatalf("LastEndedUpdateRollout = %+v (%v), want the second, cancelled by alice", got, err)
	}
	if got := mustRollout(t, s, first.ID); got.CancelledBy != "" {
		t.Errorf("the planner's cancel recorded %q as who cancelled", got.CancelledBy)
	}
	// An open rollout is not the last ended one.
	mustCreateRollout(t, s, newRollout())
	if got, err := s.LastEndedUpdateRollout(ctx); err != nil || got.ID != second.ID {
		t.Errorf("LastEndedUpdateRollout with one open = %+v (%v), want the second still", got, err)
	}
}

// A rollout's step is written only while the rollout runs. The planner reads
// the rollout and then asks the host, and a person's cancel can land between
// the two; checking in the insert's own transaction means a host is never
// asked to restart for a rollout somebody has just stopped.
func TestARolloutsStepIsRecordedOnlyWhileTheRolloutRuns(t *testing.T) {
	s, _ := rolloutStore(t)
	ctx := context.Background()
	r := mustCreateRollout(t, s, newRollout())

	step := hostAttempt("hst_a")
	step.RolloutID = r.ID
	mustCreateAttempt(t, s, step)
	if _, err := s.FinishUpdateAttempt(ctx, step.ID, UpdateSucceeded, ""); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		move func() (bool, error)
	}{
		{"halted", func() (bool, error) { return s.HaltUpdateRollout(ctx, r.ID, "vm-a did not come back") }},
		{"cancelled", func() (bool, error) { return s.CancelUpdateRollout(ctx, r.ID, "alice") }},
	} {
		if moved, err := tc.move(); err != nil || !moved {
			t.Fatalf("moving the rollout to %s: %v, %v", tc.name, moved, err)
		}
		late := hostAttempt("hst_b")
		late.RolloutID = r.ID
		if err := s.CreateUpdateAttempt(ctx, late); !errors.Is(err, ErrRolloutNotRunning) {
			t.Errorf("a step of a %s rollout: err = %v, want ErrRolloutNotRunning", tc.name, err)
		}
		if open, err := s.OpenUpdateAttempts(ctx); err != nil || len(open) != 0 {
			t.Errorf("a step of a %s rollout left %d open attempt(s) (%v)", tc.name, len(open), err)
		}
	}

	unknown := hostAttempt("hst_c")
	unknown.RolloutID = "rol_nothere"
	if err := s.CreateUpdateAttempt(ctx, unknown); !errors.Is(err, ErrRolloutNotRunning) {
		t.Errorf("a step of a rollout that does not exist: err = %v, want ErrRolloutNotRunning", err)
	}
	// A person's press belongs to no rollout and is not held to one.
	mustCreateAttempt(t, s, hostAttempt("hst_d"))
}
