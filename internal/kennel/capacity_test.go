package kennel

import (
	"strings"
	"testing"
	"time"
)

func withMatrix(s Snapshot, pool string, jobs int, waited time.Duration) Snapshot {
	s.Fleet.Jobs.Matrices = append(s.Fleet.Jobs.Matrices, Matrix{PoolID: pool, Jobs: jobs, Waited: waited})
	return s
}

func matrixRepo(maxRunners int) Snapshot {
	s := privateRepo()
	s.Fleet.Pools = []PoolFact{{ID: "pool_a1", Name: "zoomies-ubuntu-2404", JobsRun: 6, MaxRunners: maxRunners}}
	return s
}

// A matrix wider than its pool runs in waves, and the wait is the symptom an
// operator sees: the finding says the width, the ceiling and the wait, and
// points at the pool, which is where the change is made.
func TestAMatrixWiderThanItsPoolThatWaitedIsAnInfoFindingOnThePool(t *testing.T) {
	ev := Evaluate(withMatrix(matrixRepo(2), "pool_a1", 6, 3*time.Minute), Policy{})
	f := findingIn(t, ev, CodeMatrixExceedsPool)
	if f.Severity != SeverityInfo || f.Subject != "pool_a1" {
		t.Errorf("finding = %+v", f)
	}
	if !strings.Contains(f.Detail, "6 jobs") || !strings.Contains(f.Detail, "2 runners") || !strings.Contains(f.Detail, "3 minutes") {
		t.Errorf("detail = %q", f.Detail)
	}
	if len(f.Evidence) != 1 || f.Evidence[0].Kind != EvidencePool || f.Evidence[0].Ref != "pool_a1" {
		t.Errorf("evidence = %+v, want the pool", f.Evidence)
	}
}

// A pool with no ceiling cannot be too small, and a matrix whose jobs all
// started at once was not held up by the pool, whatever its width.
func TestAMatrixOnAPoolWithNoCeilingOrThatDidNotWaitRaisesNothing(t *testing.T) {
	for name, s := range map[string]Snapshot{
		"no ceiling":   withMatrix(matrixRepo(0), "pool_a1", 6, 10*time.Minute),
		"did not wait": withMatrix(matrixRepo(2), "pool_a1", 6, 59*time.Second),
		"fits":         withMatrix(matrixRepo(6), "pool_a1", 6, 10*time.Minute),
		"unknown pool": withMatrix(matrixRepo(2), "pool_gone", 6, 10*time.Minute),
	} {
		if ev := Evaluate(s, Policy{}); hasFinding(ev, CodeMatrixExceedsPool) {
			t.Errorf("%s: a finding was raised", name)
		}
	}
}

func TestOneFindingPerPoolHoweverManyMatricesRanOnIt(t *testing.T) {
	s := matrixRepo(2)
	s.Fleet.Pools = append(s.Fleet.Pools, PoolFact{ID: "pool_b2", Name: "zoomies-ubuntu-2404-b", JobsRun: 3, MaxRunners: 1})
	s = withMatrix(s, "pool_a1", 6, 3*time.Minute)
	s = withMatrix(s, "pool_a1", 4, 9*time.Minute)
	s = withMatrix(s, "pool_b2", 3, 2*time.Minute)
	ev := Evaluate(s, Policy{})
	var n int
	for _, f := range ev.Findings {
		if f.Code == CodeMatrixExceedsPool {
			n++
			if f.Subject == "pool_a1" && (!strings.Contains(f.Detail, "2 matrices") || !strings.Contains(f.Detail, "9 minutes")) {
				t.Errorf("pool_a1 detail = %q, want both matrices and the longest wait", f.Detail)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d findings, want one per pool", n)
	}
}

func findingIn(t *testing.T, ev Evaluation, code Code) Finding {
	t.Helper()
	for _, f := range ev.Findings {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no %s among %+v", code, ev.Findings)
	return Finding{}
}

func hasFinding(ev Evaluation, code Code) bool {
	for _, f := range ev.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}
