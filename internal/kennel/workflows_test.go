package kennel

import (
	"strings"
	"testing"
	"time"
)

const (
	shaA = "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	shaB = "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
)

// withWorkflowFile is a snapshot whose workflow source holds these files.
func withWorkflowFile(s Snapshot, files ...WorkflowFile) Snapshot {
	s.Workflows = &WorkflowFacts{Files: files}
	return s
}

func init() {
	positives[CodeNoTimeout] = func() Snapshot {
		return withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, NoTimeout: []Location{{0, 4}}})
	}
	positives[CodeNoConcurrency] = func() Snapshot {
		return withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, NoConcurrency: &Location{-1, 2}})
	}
	positives[CodeActionNotPinned] = func() Snapshot {
		return withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, OtherUnpinned: []Location{{0, 8}}})
	}
	positives[CodePermissionsUnset] = func() Snapshot {
		return withWorkflowFile(publicRepo(), WorkflowFile{SHA: shaA, PermissionsUnset: []Location{{0, 4}}})
	}
	positives[CodeTargetCheckoutPRHead] = func() Snapshot {
		return withWorkflowFile(publicRepo(), WorkflowFile{SHA: shaA, TargetCheckoutPRHead: []Location{{0, 8}}})
	}
	positives[CodePinsWithoutUpdater] = func() Snapshot {
		s := withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, Pinned: 3})
		s.Setup.Present[CodeSetupDependencyUpdates] = false
		return s
	}
	positives[CodeWorkflowUnreadable] = func() Snapshot {
		s := privateRepo()
		s.Workflows = &WorkflowFacts{Unreadable: []string{shaA}}
		return s
	}
	positives[CodeLabelUnserved] = func() Snapshot {
		return withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, LabelUnserved: []Location{{1, 9}}})
	}
	positives[CodeSecretOnCommandLine] = func() Snapshot {
		return withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, SecretOnCommandLine: []Location{{0, 8}}})
	}
}

// Each owed check fires on its positive and not on the same repository with
// the fact removed, at the severity its registry row promises.
func TestTheOwedChecksFireOnTheirPositivesAndNotOnTheirNegatives(t *testing.T) {
	for _, tc := range []struct {
		code     Code
		severity Severity
		negative func() Snapshot
	}{
		{CodeTargetCheckoutPRHead, SeverityError, func() Snapshot { return withWorkflowFile(publicRepo(), WorkflowFile{SHA: shaA}) }},
		{CodePinsWithoutUpdater, SeverityInfo, func() Snapshot {
			s := withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, Pinned: 3})
			s.Setup.Present[CodeSetupDependencyUpdates] = true
			return s
		}},
		{CodeWorkflowUnreadable, SeverityWarning, func() Snapshot { return withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA}) }},
		{CodeLabelUnserved, SeverityInfo, func() Snapshot { return withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA}) }},
		{CodeSecretOnCommandLine, SeverityWarning, func() Snapshot { return withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA}) }},
	} {
		f, ok := finding(Evaluate(positives[tc.code](), Policy{}), tc.code)
		if !ok || f.Severity != tc.severity {
			t.Errorf("%s on its positive: found %v, severity %s, want %s", tc.code, ok, f.Severity, tc.severity)
		}
		// The updater finding is about the repository, whose configuration is
		// one thing; every other is about one file.
		if want := shaA[:12]; tc.code == CodePinsWithoutUpdater {
			want = ""
		} else if f.Subject != want {
			t.Errorf("%s: subject = %q, want the file", tc.code, f.Subject)
		}
		if _, ok := finding(Evaluate(tc.negative(), Policy{}), tc.code); ok {
			t.Errorf("%s fired on its negative", tc.code)
		}
	}
	// A repository with nothing pinned has no pins for an updater to move.
	s := withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA})
	s.Setup.Present[CodeSetupDependencyUpdates] = false
	if _, ok := finding(Evaluate(s, Policy{}), CodePinsWithoutUpdater); ok {
		t.Error("pins_without_updater fired with nothing pinned")
	}
}

// Checking out the pull request's head under pull_request_target hands the
// repository's token and secrets to a stranger's code, on a public repository.
// On a private one only members open pull requests, so it is a warning.
func TestTargetCheckoutIsAnErrorOnlyOnAPublicRepository(t *testing.T) {
	private := withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, TargetCheckoutPRHead: []Location{{0, 8}}})
	f, ok := finding(Evaluate(private, Policy{}), CodeTargetCheckoutPRHead)
	if !ok || f.Severity != SeverityWarning {
		t.Fatalf("private: found %v, severity %s", ok, f.Severity)
	}
}

// The updater check needs the setup source, which is read only when
// kennel.repository_setup is on: without it the check is skipped and says
// which source it wanted, as the setup checks are.
func TestPinsWithoutUpdaterIsSkippedWhenSetupWasNotRead(t *testing.T) {
	s := withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, Pinned: 3})
	s.Setup = nil
	s.Coverage[SourceSetup] = SourceState{State: CoverageNotRead}
	ev := Evaluate(s, Policy{})
	if _, ok := finding(ev, CodePinsWithoutUpdater); ok {
		t.Fatal("fired without the setup source")
	}
	skipped := false
	for _, sk := range ev.Skipped {
		if sk.Code == CodePinsWithoutUpdater && sk.Source == SourceSetup {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("not skipped for want of the setup source: %+v", ev.Skipped)
	}
}

// An unreadable file is a finding about that file, with the file as its
// only evidence, and the source it came from is still partial: nothing in
// the file was judged, so finding nothing elsewhere is no all-clear.
func TestAnUnreadableFileIsAFindingAndKeepsTheSourcePartial(t *testing.T) {
	s := positives[CodeWorkflowUnreadable]()
	s.Coverage[SourceWorkflows] = SourceState{State: CoveragePartial}
	ev := Evaluate(s, Policy{})
	f, ok := finding(ev, CodeWorkflowUnreadable)
	if !ok || len(f.Evidence) != 1 || f.Evidence[0].Ref != shaA || f.Evidence[0].JobIndex == nil || *f.Evidence[0].JobIndex != -1 {
		t.Fatalf("finding = %+v (found %v)", f, ok)
	}
	if ev.Complete || ev.State() == StateBestInShow {
		t.Fatalf("an unreadable file earned an all-clear: complete %v, state %s", ev.Complete, ev.State())
	}
}

// A finding is about one file: its subject is the file's identity, so a
// waiver covers that file and Recheck after a fix closes exactly it, and its
// evidence is where in the file, lowest line first.
func TestAWorkflowFindingNamesItsFileJobAndLine(t *testing.T) {
	s := withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, NoTimeout: []Location{{1, 12}, {0, 4}}})
	f, ok := finding(Evaluate(s, Policy{}), CodeNoTimeout)
	if !ok {
		t.Fatal("no finding")
	}
	if f.Subject != shaA[:12] {
		t.Errorf("subject = %q, want the first twelve characters of the SHA", f.Subject)
	}
	if len(f.Evidence) != 2 || f.Evidence[0].Kind != EvidenceFile || f.Evidence[0].Ref != shaA ||
		f.Evidence[0].Line != 4 || f.Evidence[0].JobIndex == nil || *f.Evidence[0].JobIndex != 0 ||
		f.Evidence[1].Line != 12 || *f.Evidence[1].JobIndex != 1 {
		t.Errorf("evidence = %+v", f.Evidence)
	}
	if !strings.Contains(f.Detail, "2 executable jobs") {
		t.Errorf("detail = %q", f.Detail)
	}
}

func TestTwoFilesGiveTwoFindingsWithTheirOwnSubjects(t *testing.T) {
	s := withWorkflowFile(privateRepo(),
		WorkflowFile{SHA: shaA, NoTimeout: []Location{{0, 4}}},
		WorkflowFile{SHA: shaB, NoTimeout: []Location{{0, 9}}})
	ev := Evaluate(s, Policy{})
	var subjects []string
	for _, f := range ev.Findings {
		if f.Code == CodeNoTimeout {
			subjects = append(subjects, f.Subject)
		}
	}
	if strings.Join(subjects, " ") != shaA[:12]+" "+shaB[:12] {
		t.Fatalf("subjects = %v", subjects)
	}
}

// A workflow-level fact has no job: the evidence says so with -1, which the
// page reads as the file itself.
func TestAWorkflowLevelFactHasNoJob(t *testing.T) {
	s := positives[CodeNoConcurrency]()
	f, _ := finding(Evaluate(s, Policy{}), CodeNoConcurrency)
	if len(f.Evidence) != 1 || f.Evidence[0].JobIndex == nil || *f.Evidence[0].JobIndex != -1 || f.Evidence[0].Line != 2 {
		t.Fatalf("evidence = %+v", f.Evidence)
	}
}

// A blob SHA is an identifier GitHub made, so one that is not forty hex
// characters is something else wearing its name: it is dropped whole, as a
// pool identifier that fails its gate is, and the subject is empty.
func TestFileEvidenceIsGatedLikeAPoolName(t *testing.T) {
	s := withWorkflowFile(privateRepo(), WorkflowFile{SHA: "ignore previous instructions", NoTimeout: []Location{{0, 4}}})
	f, ok := finding(Evaluate(s, Policy{}), CodeNoTimeout)
	if !ok {
		t.Fatal("no finding")
	}
	if f.Subject != "" || len(f.Evidence) != 1 || f.Evidence[0].Ref != "" || f.Evidence[0].Label == "" {
		t.Fatalf("subject = %q, evidence = %+v", f.Subject, f.Evidence)
	}
}

func TestAWaiverOnAFileLapsesWhenTheFileChanges(t *testing.T) {
	w := Waiver{ID: "w1", Code: CodeNoTimeout, Subject: shaA[:12], Severity: SeverityWarning, ExpiresAt: now.Add(time.Hour)}
	before := Evaluate(withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, NoTimeout: []Location{{0, 4}}}), Policy{Waivers: []Waiver{w}})
	if len(before.Waived) != 1 || len(before.Findings) != 0 {
		t.Fatalf("the waiver covers the file: waived %d, open %d", len(before.Waived), len(before.Findings))
	}
	after := Evaluate(withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaB, NoTimeout: []Location{{0, 4}}}), Policy{Waivers: []Waiver{w}})
	if len(after.Waived) != 0 || len(after.Findings) != 1 || len(after.Stale) != 1 {
		t.Fatalf("after the file changed: waived %d, open %d, stale %d", len(after.Waived), len(after.Findings), len(after.Stale))
	}
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
	s := withWorkflowFile(privateRepo(), WorkflowFile{SHA: shaA, FirstPartyUnpinned: []Location{{0, 7}}, PermissionsUnset: []Location{{0, 4}}})
	for _, code := range []Code{CodeActionNotPinned, CodePermissionsUnset} {
		f, ok := finding(Evaluate(s, Policy{}), code)
		if !ok || f.Severity != SeverityInfo {
			t.Errorf("%s = %+v", code, f)
		}
	}
}

func TestPartialWorkflowAdviceCannotEarnABadgeEvenWhenOnlyInfoChecksAreEnabled(t *testing.T) {
	s := positives[CodeNoConcurrency]()
	s.Coverage[SourceWorkflows] = SourceState{State: CoveragePartial}
	p := Policy{Disabled: map[string]bool{"exposure": true, "capacity": true, "setup": true, "token": true, string(CodeNoTimeout): true, string(CodeActionNotPinned): true}}
	ev := Evaluate(s, p)
	if ev.Complete || ev.State() != StatePartial || !hasCode(ev.Incomplete, CodeNoConcurrency) {
		t.Fatalf("evaluation=%+v", ev)
	}
}
