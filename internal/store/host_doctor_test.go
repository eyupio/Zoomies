package store

import (
	"context"
	"errors"
	"github.com/eyupio/zoomies/internal/hosttune"
	"testing"
	"time"
)

func TestHostDoctorSurvivesUnrelatedHostUpdates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, h := seedPool(t, s)
	r := &hosttune.Report{CheckedAt: time.Now().UTC(), RebootPending: true}
	if err := s.SetHostDoctor(ctx, h.ID, r); err != nil {
		t.Fatal(err)
	}
	h.Capacity = 9
	if err := s.UpdateHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Doctor.Report == nil || !got.Doctor.RebootPending {
		t.Fatal("unrelated edit cleared health")
	}
}

// An unchanged report moves only the freshness column, so the page can say
// "checked just now" without the 8 KB body being rewritten.
func TestHeartbeatWithReportMovesFreshnessAndLeavesTheBody(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, h := seedPool(t, s)
	first := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.SetHostDoctor(ctx, h.ID, &hosttune.Report{CheckedAt: first, OS: "linux"}); err != nil {
		t.Fatal(err)
	}
	later := first.Add(90 * time.Second)
	if err := s.HeartbeatWithReport(ctx, h.ID, later, later); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Doctor.CheckedAt.Equal(later) {
		t.Fatalf("freshness = %v, want %v", got.Doctor.CheckedAt, later)
	}
	if !got.DoctorBodyAt.Equal(first) {
		t.Fatalf("body time = %v, want %v: the body must not have been rewritten", got.DoctorBodyAt, first)
	}
	// A beat that overtook a newer one must not move freshness backwards.
	if err := s.HeartbeatWithReport(ctx, h.ID, later, first); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetHost(ctx, h.ID)
	if !got.Doctor.CheckedAt.Equal(later) {
		t.Fatalf("freshness went backwards to %v", got.Doctor.CheckedAt)
	}
	// Writing a new body resets both.
	newer := later.Add(time.Minute)
	if err := s.SetHostDoctor(ctx, h.ID, &hosttune.Report{CheckedAt: newer}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetHost(ctx, h.ID)
	if !got.Doctor.CheckedAt.Equal(newer) || !got.DoctorBodyAt.Equal(newer) {
		t.Fatalf("a new body should set both times, got %v / %v", got.Doctor.CheckedAt, got.DoctorBodyAt)
	}
}

// A task result is not a heartbeat: if the freshness write also moved
// last_heartbeat, a wedged heartbeat loop would look alive for as long as an
// operator kept pressing Check now.
func TestSetHostDoctorCheckedMovesFreshnessOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, h := seedPool(t, s)
	first := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.SetHostDoctor(ctx, h.ID, &hosttune.Report{CheckedAt: first, OS: "linux"}); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	later := first.Add(time.Minute)
	if err := s.SetHostDoctorChecked(ctx, h.ID, later); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetHost(ctx, h.ID)
	if !got.Doctor.CheckedAt.Equal(later) {
		t.Fatalf("freshness = %v, want %v", got.Doctor.CheckedAt, later)
	}
	if !got.DoctorBodyAt.Equal(first) || got.Doctor.OS != "linux" {
		t.Fatalf("the body must not be rewritten, body time %v", got.DoctorBodyAt)
	}
	if !got.LastHeartbeat.Equal(before.LastHeartbeat) {
		t.Fatalf("last_heartbeat moved from %v to %v", before.LastHeartbeat, got.LastHeartbeat)
	}
	// An older answer arriving late must not move freshness backwards.
	if err := s.SetHostDoctorChecked(ctx, h.ID, first); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetHost(ctx, h.ID)
	if !got.Doctor.CheckedAt.Equal(later) {
		t.Fatalf("freshness went backwards to %v", got.Doctor.CheckedAt)
	}
}

func TestSetHostDoctorCheckedOnAMissingHostIsNotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.SetHostDoctorChecked(context.Background(), "host_missing", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
