package kennel

import "testing"

func completeSetup() *SetupFacts {
	s := &SetupFacts{Present: map[Code]bool{}, HasDependencies: true}
	for _, c := range Checks() {
		if c.Area == AreaSetup {
			s.Present[c.Code] = true
		}
	}
	return s
}

func init() {
	for _, c := range Checks() {
		if c.Area != AreaSetup {
			continue
		}
		code := c.Code
		positives[code] = func() Snapshot {
			s := publicRepo()
			delete(s.Setup.Present, code)
			return s
		}
	}
}

func TestSetupAdviceNeverTurnsAnUnreadableInventoryIntoMissingFiles(t *testing.T) {
	for _, state := range allStates {
		s := publicRepo()
		s.Setup = &SetupFacts{Present: map[Code]bool{}, HasDependencies: true}
		s.Coverage[SourceSetup] = SourceState{State: state}
		ev := Evaluate(s, Policy{})
		for _, f := range ev.Findings {
			if f.Code.Area() == AreaSetup && state != CoverageOK {
				t.Errorf("%s raised %s", state, f.Code)
			}
		}
		if state != CoverageOK && ev.Complete {
			t.Errorf("%s was called complete", state)
		}
	}
}

func TestDependencyUpdateAdviceNeedsARecognisedManifest(t *testing.T) {
	s := positives[CodeSetupDependencyUpdates]()
	s.Setup.HasDependencies = false
	if _, ok := finding(Evaluate(s, Policy{}), CodeSetupDependencyUpdates); ok {
		t.Fatal("advice without dependencies")
	}
}

func TestPublicCommunityAdviceDoesNotApplyToPrivateRepositories(t *testing.T) {
	for _, code := range []Code{CodeSetupLicence, CodeSetupContributing, CodeSetupCodeOfConduct, CodeSetupIssueTemplate} {
		s := positives[code]()
		s.Repo.Visibility = VisibilityPrivate
		if _, ok := finding(Evaluate(s, Policy{}), code); ok {
			t.Errorf("%s fired for a private repository", code)
		}
	}
}
