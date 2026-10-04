package store

import (
	"context"
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
