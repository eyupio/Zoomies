package controller

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
)

func (f *kennelFixture) settingsOn() {
	f.t.Helper()
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.SettingsChecks = true })
}

// settingsRequests is how many reads of a repository's Actions settings have
// been made.
func (f *kennelFixture) settingsRequests() int { return f.requestsTo("/actions/permissions/") }

func coverageOf(v KennelRepositoryView, src kennel.Source) (KennelCoverageView, bool) {
	for _, c := range v.Coverage {
		if c.Source == src {
			return c, true
		}
	}
	return KennelCoverageView{}, false
}

func skippedBecause(v KennelRepositoryView, code kennel.Code) (KennelSkippedView, bool) {
	for _, s := range v.Skipped {
		if s.Code == code {
			return s, true
		}
	}
	return KennelSkippedView{}, false
}

// The settings need a permission GitHub offers no narrower form of, so nothing
// is asked for them until the operator says so, and the three checks that read
// them are shown as turned off in the meantime and not as a gap.
func TestRepositorySettingsAreReadOnlyAfterOptingIn(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.SetActionsSettings("acme/api", github.FakeActionsSettings{DefaultTokenWrite: true})
	f.pass()
	if n := f.settingsRequests(); n != 0 {
		t.Fatalf("%d settings requests with the switch off", n)
	}
	v := f.view("acme/api")
	if _, ok := coverageOf(v, kennel.SourceSettings); ok {
		t.Errorf("a source nobody read is in the coverage: %+v", v.Coverage)
	}
	for _, code := range []kennel.Code{kennel.CodeDefaultTokenWrite, kennel.CodeForkApprovalWeak, kennel.CodePrivateForkSecrets} {
		if !slices.Contains(v.Disabled, code) {
			t.Errorf("%s is not shown as turned off: %v", code, v.Disabled)
		}
	}
	if len(v.Skipped) != 0 {
		t.Errorf("a switch that is off left checks skipped: %+v", v.Skipped)
	}

	f.settingsOn()
	f.pass()
	first := f.settingsRequests()
	if first == 0 {
		t.Fatal("the settings were not read after the switch was turned on, and not even on the next pass")
	}
	// Read once, then left for the refresh interval: a second pass a minute later
	// asks GitHub nothing, or the budget would go on settings that cannot have
	// changed.
	f.pass()
	if again := f.settingsRequests(); again != first {
		t.Errorf("a second pass made %d more settings requests", again-first)
	}
	v = f.view("acme/api")
	if !slices.Contains(findingCodes(v), "token.default_write") {
		t.Errorf("findings = %v", findingCodes(v))
	}
	if c, ok := coverageOf(v, kennel.SourceSettings); !ok || c.State != kennel.CoverageOK {
		t.Errorf("settings coverage = %+v, %v", c, ok)
	}
	if slices.Contains(v.Disabled, kennel.CodeDefaultTokenWrite) {
		t.Errorf("the check is still turned off: %v", v.Disabled)
	}

	// Turning it off again forgets what was read, and a check that is off is not
	// a finding that lingers.
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.SettingsChecks = false })
	f.pass()
	f.dueAgain()
	f.pass()
	if wm := parseKennelWatermark(f.row("acme/api").Watermark); wm.Settings != nil || wm.SettingsState != "" {
		t.Errorf("settings kept after the switch was turned off: %+v", wm)
	}
	if slices.Contains(findingCodes(f.view("acme/api")), "token.default_write") {
		t.Error("a finding lingered after its check was turned off")
	}
}

// A public repository has an approval policy for a fork's pull requests and a
// private one has the rules for them, and each asks for the one that is its own.
func TestEachRepositoryAsksForTheForkPolicyThatIsItsOwn(t *testing.T) {
	f := newKennelFixture(t)
	f.settingsOn()
	f.repo("acme/pub", "public")
	f.repo("acme/priv", "private")
	f.ran("acme/pub", 1, f.pool)
	f.ran("acme/priv", 2, f.pool)
	f.gh.SetActionsSettings("acme/pub", github.FakeActionsSettings{ForkApproval: string(kennel.ApprovalNewToGitHub)})
	f.gh.SetActionsSettings("acme/priv", github.FakeActionsSettings{PrivateForkRuns: true, PrivateForkSecrets: true})
	f.pass()

	pub, priv := f.view("acme/pub"), f.view("acme/priv")
	if got := findingCodes(pub); !slices.Contains(got, "exposure.fork_approval_weak") || slices.Contains(got, "exposure.private_fork_secrets") {
		t.Errorf("public findings = %v", got)
	}
	if got := findingCodes(priv); !slices.Contains(got, "exposure.private_fork_secrets") || slices.Contains(got, "exposure.fork_approval_weak") {
		t.Errorf("private findings = %v", got)
	}
	for _, req := range f.gh.Requests() {
		switch {
		case strings.Contains(req, "acme/pub/actions/permissions/fork-pr-workflows-private-repos"):
			t.Errorf("a public repository asked the private rules: %s", req)
		case strings.Contains(req, "acme/priv/actions/permissions/fork-pr-contributor-approval"):
			t.Errorf("a private repository asked the approval policy: %s", req)
		}
	}
}

// A failed read is a gap in what was seen, said with the permission to grant
// when that is the cause, and never a clear repository.
func TestAFailedSettingsReadIsACoverageGapAndNeverAnAllClear(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		state  kennel.CoverageState
	}{{"refused", 403, kennel.CoverageDenied}, {"not offered", 404, kennel.CoverageUnavailable}, {"server error", 500, kennel.CoverageError}, {"rate limited", 429, kennel.CoverageHeld}} {
		t.Run(tt.name, func(t *testing.T) {
			f := newKennelFixture(t)
			f.settingsOn()
			f.repo("acme/api", "private")
			f.ran("acme/api", 1, f.pool)
			f.gh.SetActionsSettings("acme/api", github.FakeActionsSettings{DefaultTokenWrite: true})
			f.gh.SetError("/actions/permissions/workflow", tt.status, "read refused")
			f.pass()

			wm := parseKennelWatermark(f.row("acme/api").Watermark)
			if wm.SettingsState != tt.state || wm.Settings != nil {
				t.Fatalf("watermark = %+v", wm)
			}
			v := f.view("acme/api")
			if len(v.Findings) != 0 || v.State != kennel.StatePartial || v.Complete {
				t.Fatalf("a failed read gave findings or an all-clear: %+v", v)
			}
			c, ok := coverageOf(v, kennel.SourceSettings)
			if !ok || c.State != tt.state {
				t.Fatalf("coverage = %+v, %v", c, ok)
			}
			sk, ok := skippedBecause(v, kennel.CodeDefaultTokenWrite)
			if !ok || sk.Source != kennel.SourceSettings {
				t.Fatalf("skipped = %+v, %v", sk, ok)
			}
			if tt.state == kennel.CoverageDenied && !strings.Contains(sk.Reason, "Administration") {
				t.Errorf("a refused read does not name the permission: %q", sk.Reason)
			}
		})
	}
}

// Without the permission the App holds, the fake answers 403 as GitHub does, and
// the repository says what to grant.
func TestWithoutAdministrationReadTheRepositoryNamesThePermission(t *testing.T) {
	f := newKennelFixture(t)
	f.settingsOn()
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.SetPermissions(map[string]string{"actions": "read", "metadata": "read", "organization_self_hosted_runners": "write"})
	f.pass()
	v := f.view("acme/api")
	c, ok := coverageOf(v, kennel.SourceSettings)
	if !ok || c.State != kennel.CoverageDenied || !strings.Contains(c.Permission, "Administration") || !strings.Contains(c.Reason, "Administration") {
		t.Errorf("coverage = %+v, %v", c, ok)
	}
}

// A word GitHub sends that this version does not know is neither a finding nor
// a clear: the check is left unjudged, and the repository is not called complete.
func TestAnApprovalPolicyThisVersionDoesNotKnowIsLeftUnjudged(t *testing.T) {
	f := newKennelFixture(t)
	f.settingsOn()
	f.repo("acme/api", "public")
	f.ran("acme/api", 1, f.pool)
	f.gh.SetActionsSettings("acme/api", github.FakeActionsSettings{ForkApproval: "a_policy_from_the_future"})
	f.pass()
	v := f.view("acme/api")
	if slices.Contains(findingCodes(v), "exposure.fork_approval_weak") {
		t.Errorf("an unknown policy was reported as weak: %v", findingCodes(v))
	}
	if v.Complete {
		t.Error("a repository with a policy nobody could judge was called complete")
	}
	if wm := parseKennelWatermark(f.row("acme/api").Watermark); wm.Settings == nil || wm.Settings.ForkApproval != "" {
		t.Errorf("the unknown word was kept: %+v", wm.Settings)
	}
	for _, text := range []string{fmt.Sprint(v.Findings), fmt.Sprint(v.Coverage)} {
		if strings.Contains(text, "a_policy_from_the_future") {
			t.Errorf("the unknown word reached the view: %s", text)
		}
	}
}

func TestTheApprovalPolicyWordsAreTheOnesGitHubSends(t *testing.T) {
	for word, want := range map[string]kennel.ForkApproval{
		"first_time_contributors_new_to_github": kennel.ApprovalNewToGitHub,
		"first_time_contributors":               kennel.ApprovalFirstTime,
		"all_external_contributors":             kennel.ApprovalAll,
		"":                                      "",
		"ALL_EXTERNAL_CONTRIBUTORS":             "",
		"anything else":                         "",
	} {
		if got := kennelApprovalPolicy(word); got != want {
			t.Errorf("kennelApprovalPolicy(%q) = %q, want %q", word, got, want)
		}
	}
}

// Unset workflow permissions are only as strong as the repository's default
// token, and with both switches on the check says so: silent when the default is
// read-only, specific when it can write.
func TestWithBothSwitchesOnUnsetPermissionsAreJudgedAgainstTheDefaultToken(t *testing.T) {
	const unset = "on: push\njobs:\n  build:\n    runs-on: self-hosted\n    timeout-minutes: 5\n    steps: []\n"
	for _, tt := range []struct {
		name  string
		write bool
		fires bool
		say   string
	}{{"read-only default", false, false, ""}, {"default can write", true, true, "the repository's default token, which can write"}} {
		t.Run(tt.name, func(t *testing.T) {
			f := newKennelFixture(t)
			f.settingsOn()
			f.c.UpdateConfig(func(c *config.Config) { c.Kennel.WorkflowChecks = true })
			f.repo("acme/api", "public")
			f.ran("acme/api", 1, f.pool)
			f.gh.AddWorkflow("acme/api", ".github/workflows/ci.yml", unset)
			f.gh.SetActionsSettings("acme/api", github.FakeActionsSettings{DefaultTokenWrite: tt.write, ForkApproval: string(kennel.ApprovalAll)})
			f.pass()
			fd := findingOf(f.view("acme/api"), kennel.CodePermissionsUnset)
			if (fd != nil) != tt.fires {
				t.Fatalf("token.permissions_unset fired = %v, want %v", fd != nil, tt.fires)
			}
			if fd != nil && !strings.Contains(fd.Detail, tt.say) {
				t.Errorf("detail = %q, want it to say %q", fd.Detail, tt.say)
			}
		})
	}
}

// Each switch turns off the checks that read its source and no others: with the
// workflow switch off and the settings switch on, a check in the token area that
// reads settings is on, and the one that reads workflow files is off.
func TestTheWorkflowSwitchDoesNotTurnOffACheckThatReadsSettings(t *testing.T) {
	off := kennelDisabled(config.Kennel{SettingsChecks: true})
	if off[string(kennel.CodeDefaultTokenWrite)] || off[string(kennel.AreaToken)] {
		t.Errorf("token.default_write is off with the settings switch on and the workflow switch off: %v", off)
	}
	if !off[string(kennel.CodePermissionsUnset)] {
		t.Errorf("token.permissions_unset reads workflow files and is on with that switch off: %v", off)
	}
	both := kennelDisabled(config.Kennel{SettingsChecks: true, WorkflowChecks: true})
	for _, code := range []kennel.Code{kennel.CodeDefaultTokenWrite, kennel.CodePermissionsUnset, kennel.CodeForkApprovalWeak, kennel.CodePrivateForkSecrets} {
		if both[string(code)] {
			t.Errorf("%s is off with its switches on: %v", code, both)
		}
	}
	none := kennelDisabled(config.Kennel{WorkflowChecks: true})
	for _, code := range []kennel.Code{kennel.CodeDefaultTokenWrite, kennel.CodeForkApprovalWeak, kennel.CodePrivateForkSecrets} {
		if !none[string(code)] {
			t.Errorf("%s is on with the settings switch off: %v", code, none)
		}
	}
}

// The switch is acted on at once, not after a day.
func TestTurningTheSettingsSwitchOnIsNoticedByTheLoop(t *testing.T) {
	if !kennelSettingsChanged(config.Kennel{}, config.Kennel{SettingsChecks: true}) {
		t.Error("a change of the settings switch is not one the loop acts on at once")
	}
	if kennelSettingsChanged(config.Kennel{SettingsChecks: true}, config.Kennel{SettingsChecks: true}) {
		t.Error("no change was reported as a change")
	}
}

// A GitHub that lacks one of the newer fork-policy endpoints still has a default
// token, so what was read stands and the repository is marked partly checked
// rather than failed or called clear.
func TestAGitHubWithoutTheForkEndpointLeavesTheSettingsPartial(t *testing.T) {
	f := newKennelFixture(t)
	f.settingsOn()
	f.repo("acme/api", "private")
	f.ran("acme/api", 1, f.pool)
	f.gh.SetActionsSettings("acme/api", github.FakeActionsSettings{DefaultTokenWrite: true})
	f.gh.SetError("/fork-pr-workflows-private-repos", 404, "Not Found")
	f.pass()
	v := f.view("acme/api")
	c, ok := coverageOf(v, kennel.SourceSettings)
	if !ok || c.State != kennel.CoveragePartial {
		t.Fatalf("coverage = %+v, %v", c, ok)
	}
	if !slices.Contains(findingCodes(v), "token.default_write") {
		t.Errorf("the default token was not judged: %v", findingCodes(v))
	}
	if v.Complete {
		t.Error("a repository whose fork rules could not be read was called complete")
	}
}

// Every settings read is charged to the installation's budget before it is made,
// like the others, so a fleet of many repositories cannot spend what scaling
// needs. At the smallest share of a limit of a hundred that is five requests an
// hour: one for the listing and four for repositories. A repository's settings
// and its required checks are two reads, so four requests reach two of them.
func TestTheBudgetStopsTheSettingsReadsWhenItIsSpent(t *testing.T) {
	f := newKennelFixture(t)
	f.settingsOn()
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.APIBudgetPercent = config.KennelBudgetMinPercent })
	names := []string{"acme/a", "acme/b", "acme/c", "acme/d", "acme/e", "acme/f"}
	for i, name := range names {
		f.repo(name, "private")
		f.ran(name, int64(i+1), f.pool)
	}
	f.gh.SetRateLimit(100, 100, f.c.Now().Add(time.Hour))
	f.pass()

	var read, held, protRead, protHeld int
	for _, name := range names {
		wm := parseKennelWatermark(f.row(name).Watermark)
		switch wm.SettingsState {
		case kennel.CoverageOK:
			read++
		case kennel.CoverageHeld:
			held++
		}
		switch wm.ProtectionState {
		case kennel.CoverageOK:
			protRead++
		case kennel.CoverageHeld:
			protHeld++
		}
	}
	if read != 2 || held != 4 || protRead != 2 || protHeld != 4 {
		t.Errorf("settings: %d read and %d held; required checks: %d read and %d held; want 2 and 4 of each: the budget leaves four after the listing, two a repository", read, held, protRead, protHeld)
	}
}
