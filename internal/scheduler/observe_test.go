package scheduler

import (
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func measured(n int) []store.JobRun {
	runs := make([]store.JobRun, n)
	for i := range runs {
		runs[i] = store.JobRun{PeakCPUs: float64(i+1) / 10, PeakMemoryMB: int64((i + 1) * 100)}
	}
	return runs
}

// The figures are the nearest-rank percentiles of what the runs used, the same
// rank rule the class is decided by, so a reader can put the p95 beside the
// class's reason and see them agree.
func TestObserveIsTheNearestRankPercentilesOfWhatRunsUsed(t *testing.T) {
	got := Observe(measured(10))
	if got.Runs != 10 {
		t.Fatalf("runs = %d, want 10", got.Runs)
	}
	if got.MemoryMB != (Figures{P50: 500, P95: 1000, Max: 1000}) {
		t.Fatalf("memory = %+v, want p50 500, p95 1000, max 1000", got.MemoryMB)
	}
	if got.CPU != (Figures{P50: 0.5, P95: 1, Max: 1}) {
		t.Fatalf("cpu = %+v, want p50 0.5, p95 1, max 1", got.CPU)
	}
}

// One run is one figure three times over: never a division by zero and never
// an empty block, because a sparse row still shows what it has.
func TestObserveOfOneRunIsThatRun(t *testing.T) {
	got := Observe([]store.JobRun{{PeakCPUs: 2.5, PeakMemoryMB: 3000}})
	if got.Runs != 1 || got.MemoryMB != (Figures{P50: 3000, P95: 3000, Max: 3000}) || got.CPU != (Figures{P50: 2.5, P95: 2.5, Max: 2.5}) {
		t.Fatalf("observed = %+v", got)
	}
}

// The figures are what happened; the class is the rule. A run the kernel
// killed is treated as having needed half as much again when the class is
// decided, and that treated figure must not appear as a measurement.
func TestObserveReportsAKilledRunsRecordedPeakNotItsTreatedFigure(t *testing.T) {
	got := Observe([]store.JobRun{{PeakMemoryMB: 3000, GrantedMemoryMB: 3000, OOMKilled: true}})
	if got.MemoryMB.Max != 3000 {
		t.Fatalf("max memory = %v, want the recorded peak 3000", got.MemoryMB.Max)
	}
}

// A run that measured nothing in a dimension is not a zero in that dimension:
// a zero would pull the p50 down to a figure no run showed.
func TestObserveLeavesOutADimensionARunDidNotMeasure(t *testing.T) {
	got := Observe([]store.JobRun{{PeakMemoryMB: 3000}, {PeakMemoryMB: 1000, PeakCPUs: 2}})
	if got.Runs != 2 || got.MemoryMB.P50 != 1000 || got.CPU != (Figures{P50: 2, P95: 2, Max: 2}) {
		t.Fatalf("observed = %+v", got)
	}
}

func TestObserveOfNothingIsZero(t *testing.T) {
	if got := Observe(nil); got != (Observed{}) {
		t.Fatalf("observed = %+v, want the zero value", got)
	}
}
