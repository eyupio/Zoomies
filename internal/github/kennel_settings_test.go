package github

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func settingsReader(t *testing.T, f *FakeGitHub) KennelSettingsReader {
	t.Helper()
	r, ok := f.Client("acme", store.TargetOrg).(KennelSettingsReader)
	if !ok {
		t.Fatal("the client does not implement KennelSettingsReader")
	}
	return r
}

// withoutAdministration is an App that holds what the default App holds and
// nothing more: no Administration read.
func withoutAdministration(f *FakeGitHub) {
	f.SetPermissions(map[string]string{"actions": "read", "metadata": "read", "organization_self_hosted_runners": "write"})
}

// requestsTo is the requests the fake recorded whose path contains any of the
// words, so a test can say which of two endpoints a read reached for.
func requestsTo(f *FakeGitHub, words ...string) []string {
	var out []string
	for _, req := range f.Requests() {
		for _, w := range words {
			if strings.Contains(req, w) {
				out = append(out, req)
				break
			}
		}
	}
	return out
}

func noWrites(t *testing.T, f *FakeGitHub) {
	t.Helper()
	for _, req := range f.Requests() {
		if !strings.HasPrefix(req, "GET ") {
			t.Errorf("a settings read made a request that is not a GET: %s", req)
		}
	}
}

// A public repository has an approval policy for fork pull requests and a
// private one has the rules for them. Each makes the request that is its own and
// not the other, because the other is a request to an endpoint that does not
// describe it.
func TestAPublicRepositoryAsksForItsApprovalPolicyAndAPrivateOneForItsForkRules(t *testing.T) {
	f := newFake(t)
	f.SetActionsSettings("acme/pub", FakeActionsSettings{DefaultTokenWrite: true, ForkApproval: "first_time_contributors_new_to_github"})
	f.SetActionsSettings("acme/priv", FakeActionsSettings{PrivateForkRuns: true, PrivateForkSecrets: true})
	r := settingsReader(t, f)

	pub, err := r.KennelSettings(context.Background(), "acme/pub", true)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.DefaultTokenWrite || pub.ForkApproval != "first_time_contributors_new_to_github" || pub.PrivateFork != nil || pub.Partial {
		t.Errorf("public settings = %+v", pub)
	}
	if got := requestsTo(f, "fork-pr-workflows-private-repos"); len(got) != 0 {
		t.Errorf("a public repository asked the private fork endpoint: %v", got)
	}

	priv, err := r.KennelSettings(context.Background(), "acme/priv", false)
	if err != nil {
		t.Fatal(err)
	}
	want := &KennelPrivateFork{Runs: true, Secrets: true, WriteToken: false}
	if priv.DefaultTokenWrite || priv.ForkApproval != "" || priv.PrivateFork == nil || *priv.PrivateFork != *want || priv.Partial {
		t.Errorf("private settings = %+v, fork %+v", priv, priv.PrivateFork)
	}
	if got := requestsTo(f, "acme/priv/actions/permissions/fork-pr-contributor-approval"); len(got) != 0 {
		t.Errorf("a private repository asked the approval policy: %v", got)
	}
	noWrites(t, f)
}

// A repository nobody has configured has the read-only token GitHub gives it.
func TestAnUnconfiguredRepositoryHasTheReadOnlyDefault(t *testing.T) {
	f := newFake(t)
	s, err := settingsReader(t, f).KennelSettings(context.Background(), "acme/api", true)
	if err != nil || s.DefaultTokenWrite {
		t.Errorf("settings = %+v, %v", s, err)
	}
}

// A 403 is the App not holding Administration read, and the error has to say
// that. The shared hint for a 403 sends an operator to the runner permissions,
// which are the wrong checkbox for this read.
func TestRefusedSettingsNameTheAdministrationPermissionAndNotTheRunnerOnes(t *testing.T) {
	f := newFake(t)
	withoutAdministration(f)
	r := settingsReader(t, f)
	for name, read := range map[string]func() error{
		"settings": func() error { _, err := r.KennelSettings(context.Background(), "acme/api", true); return err },
		"protection": func() error {
			f.SetError("rules/branches", 403, "Resource not accessible by integration")
			_, err := r.KennelProtection(context.Background(), "acme/api", "main")
			return err
		},
	} {
		err := read()
		if !errors.Is(err, ErrForbidden) {
			t.Errorf("%s: %v is not ErrForbidden", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "Administration") {
			t.Errorf("%s: the error does not name the permission: %v", name, err)
		}
		if strings.Contains(err.Error(), "self_hosted_runners") || strings.Contains(err.Error(), "workflow_job") {
			t.Errorf("%s: the error sends the operator to the runner permissions: %v", name, err)
		}
	}
}

// The two fork-policy endpoints are newer than the rest, and a GitHub that lacks
// one is a fact about that server. The default token is still read and the
// repository is marked partial, so a finding that needed the missing answer is
// not judged and the one that did not is. A 405 is the same fact, and the shared
// classify would leave it as a failure retried every half hour.
func TestAMissingForkEndpointLeavesTheSettingsPartialAndNotFailed(t *testing.T) {
	for _, status := range []int{404, 405} {
		for _, public := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d public=%v", status, public), func(t *testing.T) {
				f := newFake(t)
				f.SetActionsSettings("acme/api", FakeActionsSettings{DefaultTokenWrite: true})
				f.SetError("fork-pr-", status, "Not Found")
				s, err := settingsReader(t, f).KennelSettings(context.Background(), "acme/api", public)
				if err != nil {
					t.Fatalf("a missing fork endpoint failed the read: %v", err)
				}
				if !s.Partial || !s.DefaultTokenWrite || s.ForkApproval != "" || s.PrivateFork != nil {
					t.Errorf("settings = %+v, fork %+v", s, s.PrivateFork)
				}
			})
		}
	}
}

// The default token is the one answer every finding here rests on, so GitHub
// lacking it is a failure the controller reads as "not offered", not a quiet
// partial.
func TestAMissingWorkflowPermissionsEndpointFailsTheRead(t *testing.T) {
	f := newFake(t)
	f.SetError("permissions/workflow", 404, "Not Found")
	_, err := settingsReader(t, f).KennelSettings(context.Background(), "acme/api", true)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	f = newFake(t)
	f.SetError("permissions/workflow", 500, "boom")
	if _, err := settingsReader(t, f).KennelSettings(context.Background(), "acme/api", true); err == nil {
		t.Error("a server error was read as settings")
	}
}

func actions(contexts ...string) []FakeRequiredCheck {
	var out []FakeRequiredCheck
	for _, c := range contexts {
		out = append(out, FakeRequiredCheck{Context: c, AppID: GitHubActionsAppID})
	}
	return out
}

func readProtection(t *testing.T, f *FakeGitHub, branch string) *KennelProtection {
	t.Helper()
	p, err := settingsReader(t, f).KennelProtection(context.Background(), "acme/api", branch)
	if err != nil {
		t.Fatalf("KennelProtection(%q): %v", branch, err)
	}
	return p
}

// Classic protection answers 404 "Branch not protected" when a branch has none.
// That is a normal answer and not a failure, and it is not the repository being
// missing: reading it as unavailable would hide every repository that is
// protected only by its rules.
func TestABranchWithNoClassicProtectionIsNotAFailure(t *testing.T) {
	f := newFake(t)
	p := readProtection(t, f, "main")
	if len(p.Required) != 0 || p.Unpinned != 0 || p.Partial {
		t.Errorf("an unprotected branch read as %+v", p)
	}
	if got := requestsTo(f, "/protection"); len(got) != 1 {
		t.Errorf("classic protection was read %d times: %v", len(got), got)
	}
}

// A repository protected only by rulesets looks unprotected to a reader of
// classic protection alone, and the reverse is true of the rules, so both are
// read and either alone is enough.
func TestRequiredChecksComeFromClassicProtectionAndFromRulesAlike(t *testing.T) {
	t.Run("classic only", func(t *testing.T) {
		f := newFake(t)
		f.SetBranchProtection("acme/api", "main", actions("test", "build")...)
		p := readProtection(t, f, "main")
		if !slices.Equal(p.Required, []string{"build", "test"}) || p.Unpinned != 0 || p.Partial {
			t.Errorf("read %+v", p)
		}
	})
	t.Run("rules only", func(t *testing.T) {
		f := newFake(t)
		f.AddBranchRule("acme/api", "main", actions("lint")...)
		p := readProtection(t, f, "main")
		if !slices.Equal(p.Required, []string{"lint"}) || p.Partial {
			t.Errorf("read %+v", p)
		}
	})
	t.Run("both, with a repeat", func(t *testing.T) {
		f := newFake(t)
		f.SetBranchProtection("acme/api", "main", actions("test")...)
		f.AddBranchRule("acme/api", "main", actions("test", "lint")...)
		f.AddBranchRule("acme/api", "main", actions("lint")...)
		p := readProtection(t, f, "main")
		if !slices.Equal(p.Required, []string{"lint", "test"}) {
			t.Errorf("read %+v, want each check once, sorted", p)
		}
	})
}

// A check any app may post could come from somewhere the fleet cannot see, so it
// is counted and never judged: a pinned one is the only kind the jobs the fleet
// saw can speak for. One source leaving a check open to any app is enough to
// leave it open.
func TestOnlyACheckPinnedToTheActionsAppIsKeptForJudging(t *testing.T) {
	f := newFake(t)
	f.SetBranchProtection("acme/api", "main",
		FakeRequiredCheck{Context: "pinned", AppID: GitHubActionsAppID},
		FakeRequiredCheck{Context: "any app"},
		FakeRequiredCheck{Context: "other app", AppID: 99},
		FakeRequiredCheck{Context: "open in a rule", AppID: GitHubActionsAppID},
	)
	f.AddBranchRule("acme/api", "main", FakeRequiredCheck{Context: "open in a rule"})
	p := readProtection(t, f, "main")
	if !slices.Equal(p.Required, []string{"pinned"}) || p.Unpinned != 3 {
		t.Errorf("read %+v, want one pinned and three left open", p)
	}
}

// The deprecated list of contexts names no app, so it can only be unpinned.
func TestAProtectionRuleThatOnlyListsContextsPinsNothing(t *testing.T) {
	f := newFake(t)
	f.SetLegacyBranchProtection("acme/api", "main", "build", "test")
	p := readProtection(t, f, "main")
	if len(p.Required) != 0 || p.Unpinned != 2 {
		t.Errorf("read %+v, want two unpinned", p)
	}
}

// GitHub accepts a slash in a branch name, and go-github escapes it for one
// endpoint and not the other. Both reads have to find the branch.
func TestABranchNameWithASlashIsFoundByBothReads(t *testing.T) {
	f := newFake(t)
	f.SetBranchProtection("acme/api", "release/1.0", actions("classic")...)
	f.AddBranchRule("acme/api", "release/1.0", actions("ruled")...)
	p := readProtection(t, f, "release/1.0")
	if !slices.Equal(p.Required, []string{"classic", "ruled"}) {
		t.Errorf("read %+v", p)
	}
}

// Without Administration read the classic half is refused and the rules, which
// need only Metadata, are not. The checks the rules require are kept and the
// repository is partial, so nothing is judged as if the other half were empty.
func TestARefusedClassicHalfLeavesTheRulesAndMarksTheReadPartial(t *testing.T) {
	f := newFake(t)
	withoutAdministration(f)
	f.AddBranchRule("acme/api", "main", actions("lint")...)
	p := readProtection(t, f, "main")
	if !slices.Equal(p.Required, []string{"lint"}) || !p.Partial {
		t.Errorf("read %+v", p)
	}
}

func TestProtectionRefusedInBothHalvesFailsAsForbidden(t *testing.T) {
	f := newFake(t)
	withoutAdministration(f)
	f.SetError("rules/branches", 403, "Resource not accessible by integration")
	_, err := settingsReader(t, f).KennelProtection(context.Background(), "acme/api", "main")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v", err)
	}
}

// A GitHub that lacks rules has none that require anything, which is complete
// knowledge and not a gap.
func TestAGitHubWithoutRulesIsNotPartial(t *testing.T) {
	for _, status := range []int{404, 405} {
		f := newFake(t)
		f.SetBranchProtection("acme/api", "main", actions("test")...)
		f.SetError("rules/branches", status, "Not Found")
		p := readProtection(t, f, "main")
		if !slices.Equal(p.Required, []string{"test"}) || p.Partial {
			t.Errorf("status %d: read %+v", status, p)
		}
	}
}

// Rules are paged a hundred at a time, and one branch cannot make a refresh
// unbounded: the pages are read up to a limit and the read is marked partial
// where the limit cut it.
func TestRulesArePagedAndTheReadIsBounded(t *testing.T) {
	t.Run("every page of a long list", func(t *testing.T) {
		f := newFake(t)
		for i := 0; i < 150; i++ {
			f.AddBranchRule("acme/api", "main", actions(fmt.Sprintf("check-%d", i%10))...)
		}
		p := readProtection(t, f, "main")
		if len(p.Required) != 10 || p.Partial {
			t.Errorf("read %d checks, partial %v", len(p.Required), p.Partial)
		}
		if got := requestsTo(f, "/rules/branches/"); len(got) != 2 {
			t.Errorf("a list of 150 rules took %d requests: %v", len(got), got)
		}
	})
	t.Run("stops at the limit", func(t *testing.T) {
		// The limit is a budget: one branch's rules may cost a refresh no more
		// than this. Read from the constant alone, a test would follow the
		// constant wherever it was moved.
		const mostRuleRequests = 10
		if kennelRulePages > mostRuleRequests {
			t.Fatalf("kennelRulePages is %d; one branch's rules may cost at most %d requests a refresh", kennelRulePages, mostRuleRequests)
		}
		f := newFake(t)
		for i := 0; i < 100*kennelRulePages+50; i++ {
			f.AddBranchRule("acme/api", "main", actions(fmt.Sprintf("check-%d", i%10))...)
		}
		p := readProtection(t, f, "main")
		if !p.Partial {
			t.Error("a list cut at the limit was not marked partial")
		}
		if got := requestsTo(f, "/rules/branches/"); len(got) != kennelRulePages {
			t.Errorf("%d requests, want %d", len(got), kennelRulePages)
		}
	})
}

// A repository cannot make the kept list unbounded either.
func TestTheRequiredChecksKeptAreBounded(t *testing.T) {
	f := newFake(t)
	var names []string
	for i := 0; i < kennelRequiredCap+20; i++ {
		names = append(names, fmt.Sprintf("check-%03d", i))
	}
	f.SetBranchProtection("acme/api", "main", actions(names...)...)
	p := readProtection(t, f, "main")
	if len(p.Required) != kennelRequiredCap || !p.Partial {
		t.Errorf("kept %d, partial %v", len(p.Required), p.Partial)
	}
}

func TestAProtectionReadNeedsABranch(t *testing.T) {
	f := newFake(t)
	if _, err := settingsReader(t, f).KennelProtection(context.Background(), "acme/api", ""); err == nil {
		t.Error("a read with no branch was made")
	}
	if got := f.Requests(); len(got) != 0 {
		t.Errorf("requests made for no branch: %v", got)
	}
}

func TestEverySettingsReadIsAGet(t *testing.T) {
	f := newFake(t)
	f.SetBranchProtection("acme/api", "main", actions("test")...)
	f.AddBranchRule("acme/api", "main", actions("lint")...)
	r := settingsReader(t, f)
	for _, public := range []bool{true, false} {
		if _, err := r.KennelSettings(context.Background(), "acme/api", public); err != nil {
			t.Fatal(err)
		}
	}
	readProtection(t, f, "main")
	if len(f.Requests()) == 0 {
		t.Fatal("no requests were recorded; the test proves nothing")
	}
	noWrites(t, f)
}
