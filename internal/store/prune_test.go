package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Retention is the only thing standing between a controller and a database that
// grows for ever, and three of its five queries had no test at all: a cutoff
// comparison written the wrong way round deletes everything or nothing, and
// both look like a working fleet until somebody goes looking for last week.
//
// Each of these seeds one row either side of the cutoff and checks that exactly
// the old one goes, which is the whole contract: strictly older than the
// cutoff, and the count is what the caller logs.

func TestPruningScalingEventsKeepsTheOnesInsideTheWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, pool, _ := seedPool(t, s)
	now := time.Now()

	old := &ScalingEvent{PoolID: pool.ID, PoolName: pool.Name, From: 0, To: 1,
		Reason: "old", CreatedAt: now.Add(-48 * time.Hour)}
	recent := &ScalingEvent{PoolID: pool.ID, PoolName: pool.Name, From: 1, To: 2,
		Reason: "recent", CreatedAt: now.Add(-time.Hour)}
	for _, e := range []*ScalingEvent{old, recent} {
		if err := s.AppendScalingEvent(ctx, e); err != nil {
			t.Fatalf("AppendScalingEvent: %v", err)
		}
	}

	n, err := s.PruneScalingEvents(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneScalingEvents: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1: the count is what the controller logs when somebody asks where their history went", n)
	}
	left, err := s.ListScalingEvents(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListScalingEvents: %v", err)
	}
	if len(left) != 1 || left[0].Reason != "recent" {
		t.Fatalf("scaling events after the prune = %+v, want just the recent one", left)
	}
}

func TestPruningDeliveriesKeepsTheOnesInsideTheWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	for _, d := range []*WebhookDelivery{
		{DeliveryID: "old", Event: "workflow_job", Status: "accepted", ReceivedAt: now.Add(-48 * time.Hour)},
		{DeliveryID: "recent", Event: "workflow_job", Status: "accepted", ReceivedAt: now.Add(-time.Hour)},
	} {
		if err := s.RecordDelivery(ctx, d); err != nil {
			t.Fatalf("RecordDelivery: %v", err)
		}
	}

	n, err := s.PruneDeliveries(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneDeliveries: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d deliveries, want 1", n)
	}
	left, err := s.ListDeliveries(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(left) != 1 || left[0].DeliveryID != "recent" {
		t.Fatalf("deliveries after the prune = %+v, want just the recent one", left)
	}
}

func TestPruningSamplesKeepsTheOnesInsideTheWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	for _, at := range []time.Time{now.Add(-48 * time.Hour), now.Add(-time.Hour)} {
		if err := s.RecordSample(ctx, FleetSample{At: at, TotalRunners: 1}); err != nil {
			t.Fatalf("RecordSample: %v", err)
		}
	}

	n, err := s.PruneSamples(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneSamples: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d samples, want 1", n)
	}
	left, err := s.ListSamples(ctx, now.Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("ListSamples: %v", err)
	}
	if len(left) != 1 {
		t.Fatalf("samples after the prune = %d, want 1: the sparkline is drawn from these, and a prune that took both would flatten it", len(left))
	}
	// Which one survived, not merely how many: counting alone passes just as
	// happily when the comparison is the wrong way round and the prune has kept
	// the ancient sample and deleted the recent one.
	if kept := left[0].At; kept.Before(now.Add(-24 * time.Hour)) {
		t.Errorf("the sample left behind is from %s, which is older than the cutoff: the prune kept the wrong side", kept.UTC())
	}
}

// Each prune deletes a bounded batch so the single writer is never held for
// long, and a pass used to run exactly one. Retention then stopped bounding a
// table as soon as its inflow beat 500 jobs or 1000 rows an hour, and turning
// retention on for an old database drained a backlog at that rate for months.
// A pass now repeats the batch, each one its own transaction, until a batch
// comes back short. These seed more than one batch of old rows and one recent
// row, and expect the whole backlog gone and the recent row kept.

func TestPruningJobsDrainsABacklogLargerThanOneBatchInOnePass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-48 * time.Hour)
	done := old.Add(time.Minute)

	const backlog = 1200
	for i := 1; i <= backlog; i++ {
		j, err := s.UpsertJob(ctx, &Job{GitHubJobID: int64(i), State: JobCompleted, Conclusion: "success", QueuedAt: old, CompletedAt: &done})
		if err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
		// Timelines go with their job in every batch, not just the first.
		if i%100 == 0 {
			if err := s.AppendJobEvent(ctx, &JobEvent{JobID: j.ID, Kind: JobEventQueued, Source: "webhook", Message: "queued", At: old}); err != nil {
				t.Fatalf("AppendJobEvent: %v", err)
			}
		}
	}
	if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: backlog + 1, State: JobQueued, QueuedAt: now}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	n, err := s.PruneJobs(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneJobs: %v", err)
	}
	if n != backlog {
		t.Errorf("pruned %d jobs in one pass, want %d", n, backlog)
	}
	var jobs, events int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || events != 0 {
		t.Fatalf("after the prune there are %d jobs and %d events, want the one recent job and no events", jobs, events)
	}
}

func TestPruningDeliveriesDrainsABacklogLargerThanOneBatchInOnePass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	const backlog = 2500
	for i := 0; i < backlog; i++ {
		d := &WebhookDelivery{DeliveryID: fmt.Sprintf("old-%d", i), Event: "workflow_job", Status: "accepted", ReceivedAt: now.Add(-48 * time.Hour)}
		if err := s.RecordDelivery(ctx, d); err != nil {
			t.Fatalf("RecordDelivery: %v", err)
		}
	}
	if err := s.RecordDelivery(ctx, &WebhookDelivery{DeliveryID: "recent", Event: "workflow_job", Status: "accepted", ReceivedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}

	n, err := s.PruneDeliveries(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneDeliveries: %v", err)
	}
	if n != backlog {
		t.Errorf("pruned %d deliveries in one pass, want %d", n, backlog)
	}
	left, err := s.ListDeliveries(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(left) != 1 || left[0].DeliveryID != "recent" {
		t.Fatalf("deliveries after the prune = %+v, want just the recent one", left)
	}
}

func TestPruningScalingEventsDrainsABacklogLargerThanOneBatchInOnePass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, pool, _ := seedPool(t, s)
	now := time.Now()

	const backlog = 2500
	for i := 0; i < backlog; i++ {
		e := &ScalingEvent{PoolID: pool.ID, PoolName: pool.Name, From: 0, To: 1, Reason: "old", CreatedAt: now.Add(-48 * time.Hour)}
		if err := s.AppendScalingEvent(ctx, e); err != nil {
			t.Fatalf("AppendScalingEvent: %v", err)
		}
	}
	if err := s.AppendScalingEvent(ctx, &ScalingEvent{PoolID: pool.ID, PoolName: pool.Name, From: 1, To: 2, Reason: "recent", CreatedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatalf("AppendScalingEvent: %v", err)
	}

	n, err := s.PruneScalingEvents(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneScalingEvents: %v", err)
	}
	if n != backlog {
		t.Errorf("pruned %d scaling events in one pass, want %d", n, backlog)
	}
}

func TestPruningSamplesDrainsABacklogLargerThanOneBatchInOnePass(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	const backlog = 2500
	for i := 0; i < backlog; i++ {
		// A sample is keyed on its minute, so each old one gets its own.
		if err := s.RecordSample(ctx, FleetSample{At: now.Add(-72*time.Hour - time.Duration(i)*time.Minute), TotalRunners: 1}); err != nil {
			t.Fatalf("RecordSample: %v", err)
		}
	}
	if err := s.RecordSample(ctx, FleetSample{At: now.Add(-time.Hour), TotalRunners: 1}); err != nil {
		t.Fatalf("RecordSample: %v", err)
	}

	n, err := s.PruneSamples(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneSamples: %v", err)
	}
	if n != backlog {
		t.Errorf("pruned %d samples in one pass, want %d", n, backlog)
	}
}

// A pass must not be able to hold the writer, batch after batch, for as long
// as a backlog is deep: the bound leaves the rest for the next hourly pass, and
// a cancelled context ends the pass between batches.
func TestABatchedPruneStopsAtTheBoundAndWhenTheContextEnds(t *testing.T) {
	always := func(size int64) func(context.Context) (int64, error) {
		return func(context.Context) (int64, error) { return size, nil }
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	cancelling := func(context.Context) (int64, error) {
		calls++
		if calls == 3 {
			cancel()
		}
		return 10, nil
	}
	short := func() func(context.Context) (int64, error) {
		sizes := []int64{10, 10, 4}
		return func(context.Context) (int64, error) {
			n := sizes[0]
			sizes = sizes[1:]
			return n, nil
		}
	}

	tests := []struct {
		name string
		ctx  context.Context
		fn   func(context.Context) (int64, error)
		want int64
	}{
		{"a short batch ends the pass", context.Background(), short(), 24},
		{"a backlog that never runs short stops at the bound", context.Background(), always(10), 10 * 5},
		{"a cancelled context ends the pass between batches", ctx, cancelling, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pruneInBatches(tt.ctx, 10, 5, tt.fn)
			if err != nil {
				t.Fatalf("pruneInBatches: %v", err)
			}
			if got != tt.want {
				t.Errorf("pruned %d rows, want %d", got, tt.want)
			}
		})
	}
}
