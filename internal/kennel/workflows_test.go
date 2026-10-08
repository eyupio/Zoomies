package kennel

import "testing"

func init() {
	positives[CodeNoTimeout] = func() Snapshot { s := privateRepo(); s.Workflows.NoTimeout = 1; return s }
	positives[CodeNoConcurrency] = func() Snapshot { s := privateRepo(); s.Workflows.NoConcurrency = 1; return s }
	positives[CodeActionNotPinned] = func() Snapshot { s := privateRepo(); s.Workflows.OtherUnpinned = 1; return s }
	positives[CodePermissionsUnset] = func() Snapshot { s := publicRepo(); s.Workflows.PermissionsUnset = 1; return s }
}

func TestWorkflowAdviceRequiresReadableCoverageAndNeverCallsPartialReadsClear(t *testing.T) {
	for _, state := range allStates {
		s := privateRepo()
		s.Coverage[SourceWorkflows] = SourceState{State: state}
		ev := Evaluate(s, Policy{})
		if state != CoverageOK && ev.Complete {
			t.Errorf("%s was called complete", state)
		}
		if state != CoverageOK && state != CoveragePartial && !hasCode(ev.Disabled, CodeNoTimeout) && hasCode(ev.Ran, CodeNoTimeout) {
			t.Errorf("%s ran without facts", state)
		}
	}
	s := positives[CodeNoTimeout]()
	s.Coverage[SourceWorkflows] = SourceState{State: CoveragePartial}
	if ev := Evaluate(s, Policy{}); ev.Complete {
		t.Fatal("partial positive evidence was called complete")
	}
	if _, ok := finding(Evaluate(s, Policy{}), CodeNoTimeout); !ok {
		t.Fatal("positive evidence from a partial read was lost")
	}
}
func TestFirstPartyPinsAndPrivateTokenDeclarationsAreAdvice(t *testing.T) {
	s := privateRepo()
	s.Workflows.FirstPartyUnpinned = 1
	s.Workflows.PermissionsUnset = 1
	for _, code := range []Code{CodeActionNotPinned, CodePermissionsUnset} {
		f, ok := finding(Evaluate(s, Policy{}), code)
		if !ok || f.Severity != SeverityInfo {
			t.Errorf("%s = %+v", code, f)
		}
	}
}

func TestPartialWorkflowAdviceCannotEarnABadgeEvenWhenOnlyInfoChecksAreEnabled(t *testing.T) {
	s := privateRepo()
	s.Workflows.NoConcurrency = 1
	s.Coverage[SourceWorkflows] = SourceState{State: CoveragePartial}
	p := Policy{Disabled: map[string]bool{"exposure": true, "capacity": true, "setup": true, "token": true, string(CodeNoTimeout): true, string(CodeActionNotPinned): true}}
	ev := Evaluate(s, p)
	if ev.Complete || ev.State() != StatePartial || !hasCode(ev.Incomplete, CodeNoConcurrency) {
		t.Fatalf("evaluation=%+v", ev)
	}
}
