package kennel

import (
	"slices"
	"strings"
	"testing"
)

func skipped(ev Evaluation, c Code) (Skipped, bool) {
	for _, sk := range ev.Skipped {
		if sk.Code == c {
			return sk, true
		}
	}
	return Skipped{}, false
}

func ran(ev Evaluation, c Code) bool { return slices.Contains(ev.Ran, c) }

func incomplete(ev Evaluation, c Code) bool { return slices.Contains(ev.Incomplete, c) }

func withCoverage(s Snapshot, src Source, st CoverageState) Snapshot {
	cov := Coverage{}
	for k, v := range s.Coverage {
		cov[k] = v
	}
	cov[src] = SourceState{State: st}
	s.Coverage = cov
	return s
}

// The default token is the one setting every repository has, so the finding
// does not depend on who can open a pull request: public or private, a token that
// can write is a token a compromised step can use.
func TestADefaultTokenThatCanWriteIsAFindingWhateverTheVisibility(t *testing.T) {
	for _, base := range []func() Snapshot{publicRepo, privateRepo} {
		s := base()
		s.Settings = &SettingsFacts{DefaultTokenWrite: true}
		ev := Evaluate(s, Policy{})
		f, ok := finding(ev, CodeDefaultTokenWrite)
		if !ok || f.Severity != SeverityWarning {
			t.Errorf("%s: finding %+v, open %v", s.Repo.Visibility, f, openCodes(ev))
		}
		s.Settings = &SettingsFacts{DefaultTokenWrite: false}
		if _, ok := finding(Evaluate(s, Policy{}), CodeDefaultTokenWrite); ok {
			t.Errorf("%s: a read-only default token was reported", s.Repo.Visibility)
		}
	}
}

// A check about a setting is skipped, with the permission it needs named, when
// the setting could not be read, and it is never silently clear.
func TestChecksAboutSettingsAreSkippedWithTheRightReasonWhenTheyCannotBeRead(t *testing.T) {
	for _, st := range []CoverageState{CoverageDenied, CoverageUnavailable, CoverageHeld, CoverageError, CoverageNotRead} {
		s := withCoverage(positives[CodeDefaultTokenWrite](), SourceSettings, st)
		ev := Evaluate(s, Policy{})
		sk, ok := skipped(ev, CodeDefaultTokenWrite)
		if !ok || sk.Source != SourceSettings || sk.State != st {
			t.Errorf("%s: skipped = %+v, %v", st, sk, ok)
		}
		if st == CoverageDenied && !strings.Contains(sk.Reason(), "Administration") {
			t.Errorf("a refused settings read does not name the permission: %q", sk.Reason())
		}
		if ev.Complete {
			t.Errorf("%s: an evaluation that skipped a check was called complete", st)
		}
		p := withCoverage(positives[CodeRequiredCheckNeverReports](), SourceProtection, st)
		if sk, ok := skipped(Evaluate(p, Policy{}), CodeRequiredCheckNeverReports); !ok || sk.Source != SourceProtection {
			t.Errorf("%s: protection skipped = %+v, %v", st, sk, ok)
		}
	}
}

// A partial read stands as far as it goes: what was found is reported, and
// nothing found is not an all-clear.
func TestAPartialSettingsReadStillReportsAndIsNotAnAllClear(t *testing.T) {
	s := withCoverage(positives[CodeDefaultTokenWrite](), SourceSettings, CoveragePartial)
	ev := Evaluate(s, Policy{})
	if _, ok := finding(ev, CodeDefaultTokenWrite); !ok {
		t.Error("a partial read lost its finding")
	}
	s.Settings = &SettingsFacts{}
	ev = Evaluate(s, Policy{})
	if !incomplete(ev, CodeDefaultTokenWrite) || ev.Complete {
		t.Errorf("a partial read that found nothing was called clear: incomplete %v", ev.Incomplete)
	}
	// Readable coverage with no facts is a controller that lost them, and is no
	// more an all-clear than a partial read.
	s = positives[CodeDefaultTokenWrite]()
	s.Settings = nil
	if ev := Evaluate(s, Policy{}); !incomplete(ev, CodeDefaultTokenWrite) || ev.Complete {
		t.Errorf("settings coverage with no settings was called clear: incomplete %v", ev.Incomplete)
	}
}

// Public and private repositories each have the fork setting that is their own.
// A source named as needed always would skip the check, and mark the repository
// partial, on every repository the check does not apply to.
func TestAForkSettingIsOnlyAskedOfTheRepositoriesItAppliesTo(t *testing.T) {
	pub := withCoverage(publicRepo(), SourceSettings, CoverageDenied)
	ev := Evaluate(pub, Policy{})
	if _, ok := skipped(ev, CodePrivateForkSecrets); ok || !ran(ev, CodePrivateForkSecrets) {
		t.Errorf("a public repository was held to the private fork check: skipped %v", ev.Skipped)
	}
	if _, ok := skipped(ev, CodeForkApprovalWeak); !ok {
		t.Errorf("a served public repository with settings refused did not skip the approval check: %v", ev.Skipped)
	}

	priv := withCoverage(privateRepo(), SourceSettings, CoverageDenied)
	ev = Evaluate(priv, Policy{})
	if _, ok := skipped(ev, CodeForkApprovalWeak); ok || !ran(ev, CodeForkApprovalWeak) {
		t.Errorf("a private repository was held to the approval check: skipped %v", ev.Skipped)
	}
	if _, ok := skipped(ev, CodePrivateForkSecrets); !ok {
		t.Errorf("a private repository with settings refused did not skip the fork secrets check: %v", ev.Skipped)
	}
}

// The approval policy is a question about a stranger starting a job here. A
// public repository the fleet has not served has nothing at stake and nothing to
// read, and refusing the settings does not make it a gap.
func TestAnApprovalPolicyIsOnlyJudgedForARepositoryTheFleetServes(t *testing.T) {
	s := publicRepo()
	s.Fleet.Jobs = JobFacts{}
	s.Settings = &SettingsFacts{ForkApproval: ApprovalNewToGitHub}
	ev := Evaluate(s, Policy{})
	if _, ok := finding(ev, CodeForkApprovalWeak); ok {
		t.Error("a repository the fleet has not served was reported")
	}
	s = withCoverage(s, SourceSettings, CoverageDenied)
	if _, ok := skipped(Evaluate(s, Policy{}), CodeForkApprovalWeak); ok {
		t.Error("a repository with nothing at stake was skipped for want of a permission")
	}
	// Queued jobs count as served, as they do for the public repository finding.
	s = publicRepo()
	s.Fleet.Jobs = JobFacts{Queued: 1}
	s.Settings = &SettingsFacts{ForkApproval: ApprovalNewToGitHub}
	if _, ok := finding(Evaluate(s, Policy{}), CodeForkApprovalWeak); !ok {
		t.Error("a repository with a job queued for it was not judged")
	}
}

func TestOnlyTheWeakestApprovalPolicyIsAFindingAndAnUnknownOneIsNotAnAllClear(t *testing.T) {
	for _, tc := range []struct {
		policy  ForkApproval
		fires   bool
		unknown bool
	}{
		{ApprovalNewToGitHub, true, false},
		{ApprovalFirstTime, false, false},
		{ApprovalAll, false, false},
		{"", false, true},
		{ForkApproval(hostile), false, true},
	} {
		s := publicRepo()
		s.Settings = &SettingsFacts{ForkApproval: tc.policy}
		ev := Evaluate(s, Policy{})
		_, fired := finding(ev, CodeForkApprovalWeak)
		if fired != tc.fires || incomplete(ev, CodeForkApprovalWeak) != tc.unknown {
			t.Errorf("policy %q: fired %v, incomplete %v", tc.policy, fired, incomplete(ev, CodeForkApprovalWeak))
		}
	}
}

// A weak approval policy is a warning until a fork's code has run here, when it
// is the reason it did, and then it is an error, before any waiver is applied.
func TestAWeakApprovalPolicyIsAnErrorOnceAForkRanHere(t *testing.T) {
	calm := positives[CodeForkApprovalWeak]()
	f, _ := finding(Evaluate(calm, Policy{}), CodeForkApprovalWeak)
	if f.Severity != SeverityWarning {
		t.Errorf("severity = %s without a fork run", f.Severity)
	}
	ran := withRuns(calm, Run{ID: 7, Event: "pull_request", FromFork: true})
	f, ok := finding(Evaluate(ran, Policy{}), CodeForkApprovalWeak)
	if !ok || f.Severity != SeverityError {
		t.Errorf("severity = %s after a fork's code ran", f.Severity)
	}
	// A run that is not a fork's does not raise it.
	notFork := withRuns(calm, Run{ID: 8, Event: "pull_request", FromFork: false})
	if f, _ := finding(Evaluate(notFork, Policy{}), CodeForkApprovalWeak); f.Severity != SeverityWarning {
		t.Errorf("severity = %s after a run that was not a fork's", f.Severity)
	}
	// Nor does the other thing that raises the public repository warning: a weak
	// pool is about the pool, and only a fork's code having run is about this.
	weak := withWeakPool(calm, DangerRoot)
	if f, _ := finding(Evaluate(weak, Policy{}), CodeForkApprovalWeak); f.Severity != SeverityWarning {
		t.Errorf("severity = %s with a weak pool and no fork's code", f.Severity)
	}
}

func TestAPrivateRepositoryIsOnlyAFindingWhenForksRunAndAreSentSomething(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts *PrivateForkFacts
		fires bool
		said  []string
	}{
		{"secrets", &PrivateForkFacts{Runs: true, Secrets: true}, true, []string{"its secrets and variables"}},
		{"a write token", &PrivateForkFacts{Runs: true, WriteToken: true}, true, []string{"a token that can write"}},
		{"both", &PrivateForkFacts{Runs: true, Secrets: true, WriteToken: true}, true, []string{"its secrets and variables and a token that can write"}},
		{"sent something but forks do not run", &PrivateForkFacts{Secrets: true, WriteToken: true}, false, nil},
		{"runs but is sent nothing", &PrivateForkFacts{Runs: true}, false, nil},
	} {
		for _, vis := range []Visibility{VisibilityPrivate, VisibilityInternal} {
			s := privateRepo()
			s.Repo.Visibility = vis
			s.Settings = &SettingsFacts{PrivateFork: tc.facts}
			f, fired := finding(Evaluate(s, Policy{}), CodePrivateForkSecrets)
			if fired != tc.fires {
				t.Errorf("%s (%s): fired %v", tc.name, vis, fired)
			}
			for _, want := range tc.said {
				if !strings.Contains(f.Detail, want) {
					t.Errorf("%s: the detail does not say %q: %s", tc.name, want, f.Detail)
				}
			}
			if tc.name == "secrets" && strings.Contains(f.Detail, "token that can write") {
				t.Errorf("a finding about secrets also claims a write token: %s", f.Detail)
			}
		}
	}
	s := privateRepo()
	s.Settings = &SettingsFacts{}
	if ev := Evaluate(s, Policy{}); !incomplete(ev, CodePrivateForkSecrets) {
		t.Error("a private repository whose fork rules were not read was called clear")
	}
}

// Fewer jobs than the floor cannot tell a check nobody produces from a window
// the fleet barely saw, so it is unknown: no finding, and not clear either.
func TestARequiredCheckIsOnlyCalledMissingOnceEnoughJobsWereSeen(t *testing.T) {
	judge := func(p ProtectionFacts) Evaluation {
		s := privateRepo()
		s.Protection = &p
		return Evaluate(s, Policy{})
	}
	for _, tc := range []struct {
		name       string
		facts      ProtectionFacts
		fires      bool
		incomplete bool
	}{
		{"nothing required", ProtectionFacts{JobsSeen: 100}, false, false},
		{"all reported", ProtectionFacts{Required: 2, JobsSeen: 100}, false, false},
		{"one missing at the floor", ProtectionFacts{Required: 2, NeverReported: 1, JobsSeen: ProtectionJobFloor}, true, false},
		{"one missing under the floor", ProtectionFacts{Required: 2, NeverReported: 1, JobsSeen: ProtectionJobFloor - 1}, false, true},
		{"nothing required and few jobs", ProtectionFacts{JobsSeen: 3}, false, false},
	} {
		ev := judge(tc.facts)
		_, fired := finding(ev, CodeRequiredCheckNeverReports)
		if fired != tc.fires || incomplete(ev, CodeRequiredCheckNeverReports) != tc.incomplete {
			t.Errorf("%s: fired %v, incomplete %v", tc.name, fired, incomplete(ev, CodeRequiredCheckNeverReports))
		}
	}
	s := privateRepo()
	s.Protection = nil
	if ev := Evaluate(s, Policy{}); !incomplete(ev, CodeRequiredCheckNeverReports) {
		t.Error("a repository whose required checks were never compared was called clear")
	}
}

// The sentence agrees with the numbers, and carries only numbers: a required
// check's name is text a repository chose.
func TestTheRequiredCheckSentenceAgreesWithItsNumbers(t *testing.T) {
	say := func(p ProtectionFacts) string {
		s := privateRepo()
		s.Protection = &p
		f, ok := finding(Evaluate(s, Policy{}), CodeRequiredCheckNeverReports)
		if !ok {
			t.Fatalf("%+v did not fire", p)
		}
		return f.Detail
	}
	for _, tc := range []struct {
		facts ProtectionFacts
		want  []string
		not   []string
	}{
		{ProtectionFacts{Required: 1, NeverReported: 1, JobsSeen: 30}, []string{"The status check this repository requires before merging", "was not posted by any of the 30 jobs"}, []string{"1 of the 1"}},
		{ProtectionFacts{Required: 3, NeverReported: 3, JobsSeen: 30}, []string{"None of the 3 status checks", "was posted by any of the 30 jobs"}, []string{"3 of the 3"}},
		{ProtectionFacts{Required: 3, NeverReported: 1, JobsSeen: 30}, []string{"1 of the 3 status checks", "was not posted"}, nil},
		{ProtectionFacts{Required: 4, NeverReported: 2, JobsSeen: 30}, []string{"2 of the 4 status checks", "were not posted"}, nil},
		{ProtectionFacts{Required: 2, NeverReported: 1, Unpinned: 1, JobsSeen: 30}, []string{"1 more required check can be posted by any app, and was not judged"}, nil},
		{ProtectionFacts{Required: 2, NeverReported: 1, Unpinned: 3, JobsSeen: 30}, []string{"3 more required checks can be posted by any app, and were not judged"}, nil},
		{ProtectionFacts{Required: 2, NeverReported: 1, JobsSeen: 30}, []string{"in the last 30 days"}, []string{"can be posted by any app"}},
	} {
		got := say(tc.facts)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%+v: %q does not contain %q", tc.facts, got, w)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(got, n) {
				t.Errorf("%+v: %q contains %q", tc.facts, got, n)
			}
		}
	}
}

// The repository's default token only matters to a workflow that sets no
// permissions, so the check about those workflows is judged against it: silent
// when the default is read-only, specific when it can write, and unchanged when
// the setting is not known. It is never skipped for want of the setting, which
// is the point of reading it without asking for it.
func TestUnsetPermissionsAreJudgedAgainstTheDefaultTokenWhenItIsKnown(t *testing.T) {
	workflowWith := func(settings *SettingsFacts, st CoverageState) Snapshot {
		s := publicRepo()
		s.Workflows = &WorkflowFacts{Files: []WorkflowFile{{SHA: ciSHA, PermissionsUnset: []Location{{JobIndex: 0, Line: 4}}}}}
		s.Settings = settings
		return withCoverage(s, SourceSettings, st)
	}
	for _, tc := range []struct {
		name     string
		snapshot Snapshot
		fires    bool
		say      string
	}{
		{"default is read-only", workflowWith(&SettingsFacts{}, CoverageOK), false, ""},
		{"default can write", workflowWith(&SettingsFacts{DefaultTokenWrite: true}, CoverageOK), true, "the repository's default token, which can write"},
		{"settings not read", workflowWith(nil, CoverageNotRead), true, "that default has not been checked"},
		{"settings refused", workflowWith(nil, CoverageDenied), true, "that default has not been checked"},
		{"settings present but their read failed", workflowWith(&SettingsFacts{}, CoverageError), true, "that default has not been checked"},
		{"settings partly read", workflowWith(&SettingsFacts{}, CoveragePartial), false, ""},
	} {
		ev := Evaluate(tc.snapshot, Policy{})
		f, fired := finding(ev, CodePermissionsUnset)
		if fired != tc.fires {
			t.Errorf("%s: fired %v", tc.name, fired)
		}
		if tc.say != "" && !strings.Contains(f.Detail, tc.say) {
			t.Errorf("%s: %q does not say %q", tc.name, f.Detail, tc.say)
		}
		if _, skippedIt := skipped(ev, CodePermissionsUnset); skippedIt || !ran(ev, CodePermissionsUnset) {
			t.Errorf("%s: the workflow check was skipped for want of the settings: %v", tc.name, ev.Skipped)
		}
	}
}

// The prompt for a finding about a setting says the setting is the person's to
// change. An agent handed the instructions for a file would look for something
// to edit, such as the workflows' permissions, and change that instead.
func TestAPromptForASettingTellsAnAgentNotToEditFiles(t *testing.T) {
	for _, code := range []Code{CodeDefaultTokenWrite, CodeForkApprovalWeak, CodePrivateForkSecrets, CodeRequiredCheckNeverReports} {
		if !aSetting(code) {
			t.Errorf("%s is not recognised as a setting", code)
			continue
		}
		f, ok := finding(Evaluate(positives[code](), Policy{}), code)
		if !ok {
			t.Fatalf("%s did not fire", code)
		}
		p := Prompt(f, nil)
		for _, want := range []string{"is a setting on GitHub, not a file in the repository", "leave the change to them"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s: the prompt lacks %q:\n%s", code, want, p)
			}
		}
		for _, banned := range []string{"read the file's history", "smallest change", "in the pull request"} {
			if strings.Contains(p, banned) {
				t.Errorf("%s: the prompt of a setting says %q:\n%s", code, banned, p)
			}
		}
	}
	// What reads the settings to judge a file is still about a file.
	for _, code := range []Code{CodePermissionsUnset, CodeNoTimeout, CodeForkCodeRan} {
		if aSetting(code) {
			t.Errorf("%s is about a file or an observation, and was taken for a setting", code)
		}
	}
	if aSetting("exposure.nonsense") {
		t.Error("a code that is not registered was taken for a setting")
	}
}

// Nothing a repository or a controller put into a field reaches a sentence: the
// facts are flags, counts and one enumerated word, and a word that is not one
// of the known ones is unjudged and not repeated.
func TestSettingsFactsNeverReachASentence(t *testing.T) {
	s := hostileSnapshot()
	s.Settings = &SettingsFacts{DefaultTokenWrite: true, ForkApproval: ForkApproval(hostile)}
	s.Protection = &ProtectionFacts{Required: 2, NeverReported: 1, JobsSeen: 50}
	ev := Evaluate(s, Policy{})
	for _, f := range ev.Findings {
		for _, text := range []string{f.Title, f.Detail, f.Fix, Prompt(f, nil)} {
			if strings.Contains(text, hostile) {
				t.Errorf("%s repeats hostile text: %q", f.Code, text)
			}
		}
	}
	if _, ok := finding(ev, CodeDefaultTokenWrite); !ok {
		t.Error("the hostile snapshot did not raise the default token finding")
	}
	if !incomplete(ev, CodeForkApprovalWeak) {
		t.Error("an approval policy this version does not know was not left unjudged")
	}
}
