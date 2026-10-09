package kennel

import (
	"strings"
	"testing"
)

func init() {
	for _, code := range []Code{CodeGuidanceMissing, CodeGuidanceBroken, CodeGuidanceDuplicated, CodeGuidanceUnreadable} {
		positives[code] = func() Snapshot {
			s := privateRepo()
			loc := []GuidanceLocation{{SHA: strings.Repeat("a", 40), Line: 2}}
			switch code {
			case CodeGuidanceMissing:
				s.Guidance.Missing = true
			case CodeGuidanceBroken:
				s.Guidance.Broken = loc
			case CodeGuidanceDuplicated:
				s.Guidance.Duplicated = loc
			case CodeGuidanceUnreadable:
				s.Guidance.Unreadable = loc
			}
			return s
		}
	}
}

func TestAnIncompleteGuidanceReadCannotEstablishAbsence(t *testing.T) {
	s := positives[CodeGuidanceMissing]()
	s.Coverage[SourceGuidance] = SourceState{State: CoveragePartial}
	ev := Evaluate(s, Policy{})
	if _, ok := finding(ev, CodeGuidanceMissing); ok || ev.Complete {
		t.Fatalf("evaluation=%+v", ev)
	}
	s.Guidance.Broken = []GuidanceLocation{{SHA: strings.Repeat("b", 40)}}
	if _, ok := finding(Evaluate(s, Policy{}), CodeGuidanceBroken); !ok {
		t.Fatal("positive evidence from a partial read was discarded")
	}
}
