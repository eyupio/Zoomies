package controller

import (
	"sync"
	"testing"
)

// Eight operators opening the Overview at once used to mean eight computations
// of the list. They must all get the answer, and each must get a list of its
// own: the API filters and the event stream marshals what it is handed, and
// neither is allowed to see another's edits.
func TestOverlappingProblemRequestsEachGetTheirOwnCopyOfTheList(t *testing.T) {
	h := newHarness(t)
	h.cfg.Security.DisableAuth = true
	want, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("the harness reports no problems, so there is nothing for a copy to be wrong about")
	}

	results := make([][]Problem, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := h.c.SharedProblems(h.ctx)
			if err != nil {
				t.Errorf("SharedProblems: %v", err)
				return
			}
			results[i] = got
		}()
	}
	wg.Wait()

	for i, got := range results {
		if len(got) != len(want) {
			t.Fatalf("caller %d got %d problems, want %d", i, len(got), len(want))
		}
	}
	results[0][0].Title = "edited by the first caller"
	for i, got := range results[1:] {
		if got[0].Title == "edited by the first caller" {
			t.Fatalf("caller %d saw the first caller's edit: the list is shared, not copied", i+1)
		}
	}
}

// A caller that arrives after a computation has finished starts its own: there
// is no cache, so the list is never older than the question.
func TestAProblemsRequestAfterTheLastFinishedSeesWhatChangedSince(t *testing.T) {
	h := newHarness(t)
	h.cfg.Security.DisableAuth = true
	before, err := h.c.SharedProblems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("disabling auth raised no problem")
	}
	h.cfg.Security.DisableAuth = false
	after, err := h.c.SharedProblems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("the list still holds %d problems after the cause was removed: a finished computation was reused", len(after))
	}
}
