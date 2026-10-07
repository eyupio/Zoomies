package controller

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

var errTest = errors.New("connection reset")

func kennelProblemsOf(t *testing.T, f *kennelFixture, code string) []Problem {
	t.Helper()
	all, err := f.c.Problems(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []Problem
	for _, p := range all {
		if p.Code == code {
			out = append(out, p)
		}
	}
	return out
}

// exposeRepositories makes each named repository public, with a fork-free push
// that ran on a persistent pool: a weak pool, so each has an error open.
func exposeRepositories(f *kennelFixture, names ...string) {
	f.persistent()
	for i, name := range names {
		f.repo(name, "public")
		f.ran(name, int64(11+i), f.pool)
		f.trigger(name, int64(11+i), "push", false)
	}
	f.pass()
}

// A stranger's code running on the fleet is the problems list's business, and a
// repository's falling short of a standard is not: the list carries the first,
// as one entry however many repositories it is about, and Kennel Club's own page
// carries each of them.
func TestExposedRepositoriesRaiseOneErrorBetweenThemLinkingToKennelClub(t *testing.T) {
	f := newKennelFixture(t)
	exposeRepositories(f, "acme/exposed", "acme/also-exposed")

	got := kennelProblemsOf(t, f, "kennel.exposure")
	if len(got) != 1 {
		t.Fatalf("%d problems, want one for the fleet: %+v", len(got), got)
	}
	p := got[0]
	if p.Severity != config.SeverityError || p.TargetKind != "kennel" || p.TargetID != "" {
		t.Errorf("problem = %+v, want an error linking to Kennel Club and to no repository", p)
	}
	if p.Audience != AudienceFleet {
		t.Errorf("audience = %s: a stranger's code on the fleet is the fleet's to act on", p.Audience)
	}
	if p.Title != "Kennel Club: 2 repositories have an exposure error open" {
		t.Errorf("title = %q, want the count of repositories", p.Title)
	}
	// Kennel Club has the repositories, their findings and their evidence. The
	// drawer saying so again is what this entry exists not to do.
	for name, text := range map[string]string{"title": p.Title, "detail": p.Detail, "fix": p.Fix} {
		if strings.Contains(text, "acme/") {
			t.Errorf("%s = %q: it names a repository", name, text)
		}
	}
	// What is wrong, in the two ways it can be, and where to go for the rest.
	if !strings.Contains(p.Detail, "stranger's pull request") || !strings.Contains(p.Detail, "reach the host") {
		t.Errorf("detail = %q: it should say what the two causes are", p.Detail)
	}
	if !strings.HasPrefix(p.Fix, "Open Kennel Club") || !strings.Contains(p.Fix, "waived with a reason") {
		t.Errorf("fix = %q: the entry should send the reader to Kennel Club, where a finding is waived", p.Fix)
	}
}

func TestOneExposedRepositoryIsSaidInTheSingular(t *testing.T) {
	f := newKennelFixture(t)
	exposeRepositories(f, "acme/exposed")
	got := kennelProblemsOf(t, f, "kennel.exposure")
	if len(got) != 1 || got[0].Title != "Kennel Club: 1 repository has an exposure error open" {
		t.Fatalf("problems = %+v, want one that says 1 repository has", got)
	}
}

// The drawer dismisses and snoozes by code and target. If the count were in the
// target, every repository that became exposed would be a new problem to dismiss;
// if the target changed with the repositories, a dismissal would never hold.
func TestTheEntryIsTheSameProblemWhateverTheCount(t *testing.T) {
	f := newKennelFixture(t)
	exposeRepositories(f, "acme/exposed")
	one := kennelProblemsOf(t, f, "kennel.exposure")
	if len(one) != 1 {
		t.Fatalf("problems = %+v, want one", one)
	}

	f.repo("acme/also-exposed", "public")
	f.ran("acme/also-exposed", 12, f.pool)
	f.trigger("acme/also-exposed", 12, "push", false)
	f.pass()
	two := kennelProblemsOf(t, f, "kennel.exposure")
	if len(two) != 1 {
		t.Fatalf("problems = %+v, want still one", two)
	}
	if one[0].Title == two[0].Title {
		t.Errorf("both said %q: the count did not move", one[0].Title)
	}
	if one[0].Code != two[0].Code || one[0].TargetKind != two[0].TargetKind || one[0].TargetID != two[0].TargetID {
		t.Errorf("%+v became %+v: a dismissal would not carry across", one[0], two[0])
	}
}

func TestARepositoryWithOnlyWarningsRaisesNothingOnTheList(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/widgets", "public")
	f.ran("acme/widgets", 11, f.pool)
	f.trigger("acme/widgets", 11, "push", false)
	f.pass()
	if v := f.view("acme/widgets"); v.Counts.Warning != 1 || v.Counts.Error != 0 {
		t.Fatalf("counts = %+v, want one warning for the test to mean anything", v.Counts)
	}
	if got := kennelProblemsOf(t, f, "kennel.exposure"); len(got) != 0 {
		t.Errorf("a warning raised a problem: %+v", got)
	}
}

func TestAWaivedErrorRaisesNothingAndOnlyTheOpenOneDoes(t *testing.T) {
	f := newKennelFixture(t)
	f.persistent()
	f.repo("acme/exposed", "public")
	f.ran("acme/exposed", 11, f.pool)
	f.trigger("acme/exposed", 11, "push", false)
	f.pass()
	row := f.row("acme/exposed")
	waive := func(code string) {
		t.Helper()
		if err := f.st.UpsertKennelWaiver(f.ctx, &store.KennelWaiver{
			RepositoryPK: row.ID, Code: code, Severity: "error", Reason: "isolated hosts that are rebuilt for every job",
			CreatedBy: "usr_1", CreatedByName: "Ada", ExpiresAt: f.c.Now().Add(24 * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		f.pass()
	}
	waive("exposure.public_repo_weak_pool")
	got := kennelProblemsOf(t, f, "kennel.exposure")
	if len(got) != 1 || !strings.Contains(got[0].Title, "1 repository has") {
		t.Errorf("with one error waived, problems = %+v, want the entry for the error left", got)
	}
	waive("exposure.public_repo_on_fleet")
	if got := kennelProblemsOf(t, f, "kennel.exposure"); len(got) != 0 {
		t.Errorf("with every error waived, problems = %+v", got)
	}
}

// Off means off, including on the list: the rows an earlier run left behind are
// not announced by a feature nobody has switched on.
func TestNothingIsRaisedWhileKennelClubIsOff(t *testing.T) {
	f := newKennelFixture(t)
	f.persistent()
	f.repo("acme/exposed", "public")
	f.ran("acme/exposed", 11, f.pool)
	f.trigger("acme/exposed", 11, "push", false)
	f.pass()
	f.c.kennelNoteResult(f.inst.ID, kennel.CoverageError, f.c.Now().Add(-24*time.Hour))
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.Enabled = false })

	for _, code := range []string{"kennel.exposure", "kennel.unavailable"} {
		if got := kennelProblemsOf(t, f, code); len(got) != 0 {
			t.Errorf("%s was raised with Kennel Club off: %+v", code, got)
		}
	}
}

// Six hours, because a held installation clears itself and a transient failure
// is retried within half an hour; and what it says is why, and what to do.
func TestKennelClubBeingUnableToReadAnInstallationIsAWarningAfterSixHours(t *testing.T) {
	f := newKennelFixture(t)
	now := f.c.Now()
	// The figure is written out here and not read from the constant, so changing
	// the constant is a change this test notices.
	f.c.kennelNoteResult(f.inst.ID, kennel.CoverageDenied, now.Add(-6*time.Hour+time.Minute))
	if got := kennelProblemsOf(t, f, "kennel.unavailable"); len(got) != 0 {
		t.Fatalf("a failure five hours and fifty-nine minutes old raised %+v", got)
	}

	f.c.kennel.mu.Lock()
	f.c.kennel.notes[f.inst.ID] = kennelNote{State: kennel.CoverageDenied, Since: now.Add(-6*time.Hour - time.Minute)}
	f.c.kennel.mu.Unlock()
	got := kennelProblemsOf(t, f, "kennel.unavailable")
	if len(got) != 1 {
		t.Fatalf("problems = %+v, want one", got)
	}
	p := got[0]
	if p.Severity != config.SeverityWarning || p.Audience != AudiencePlatform || p.TargetKind != "installation" || p.TargetID != f.inst.ID {
		t.Errorf("problem = %+v", p)
	}
	if p.Since == nil || !strings.Contains(p.Title, "acme") || !strings.Contains(p.Fix, "Verify the installation") {
		t.Errorf("problem = %+v: it should say since when, which installation, and what to do", p)
	}
}

func TestEveryWayAnInstallationCanFailHasAFixThatSaysWhatToDo(t *testing.T) {
	seen := map[string]kennel.CoverageState{}
	for _, state := range []kennel.CoverageState{kennel.CoverageHeld, kennel.CoverageDenied, kennel.CoverageUnavailable, kennel.CoverageError} {
		fix := kennelUnavailableFix(state)
		if !strings.HasSuffix(fix, ".") || strings.ContainsAny(fix, "—–") {
			t.Errorf("%s fix does not end like a sentence or has a dash: %q", state, fix)
		}
		if other, dup := seen[fix]; dup {
			t.Errorf("%s and %s share a fix, so one of them is not being told what is true of it", state, other)
		}
		seen[fix] = state
	}
}

// A repository falling short of a standard is nothing to a developer whose job is
// queued, and a status page that named an exposed repository would be a map for
// the person the finding is about.
func TestAnExposureFindingNeverMovesOrAppearsOnThePublicStatus(t *testing.T) {
	st := ProjectStatus([]Problem{
		{Code: "kennel.exposure", Severity: config.SeverityError, Title: "Kennel Club: 1 repository has an exposure error open", Audience: AudienceFleet},
	}, nil)
	if st.State != FleetHealthy || len(st.Reasons) != 0 || len(st.Explanations) != 0 {
		t.Errorf("status = %+v, want healthy with no reasons", st)
	}
	if !statusExempt("kennel.exposure") || !statusExempt("kennel.unavailable") || statusExempt("host.unhealthy") {
		t.Error("statusExempt does not hold the two Kennel Club codes apart from the rest")
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestWhatKennelClubAsksOfGitHubIsCountedApartFromEverythingElse(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/widgets", "public")
	for run := int64(1); run <= 3; run++ {
		f.ran("acme/widgets", run, f.pool)
		f.trigger("acme/widgets", run, "push", false)
	}
	f.pass()
	// The limit read, the listing and three runs.
	if got := testutil.ToFloat64(f.c.metrics.kennelRequests.WithLabelValues("ok")); got != 5 {
		t.Errorf("%v requests counted, want 5", got)
	}
	if got := testutil.ToFloat64(f.c.metrics.githubRequests.WithLabelValues(f.inst.ID, "ok")); got < 5 {
		t.Errorf("the installation's own counter says %v, which is not at least Kennel Club's share", got)
	}
}

func TestARefusalIsCountedByHowItWasRefusedAndARateLimitIsAHold(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/widgets", "public")
	f.ran("acme/widgets", 11, f.pool)
	f.gh.SetError("/installation/repositories", 429, "API rate limit exceeded")
	f.pass()
	if got := testutil.ToFloat64(f.c.metrics.kennelRequests.WithLabelValues("rate_limited")); got != 1 {
		t.Errorf("rate_limited = %v, want 1", got)
	}
	if got := testutil.ToFloat64(f.c.metrics.kennelHolds); got != 1 {
		t.Errorf("holds = %v, want 1: the target is zero, so every one has to show", got)
	}
}

func TestAnOutcomeIsNamedTheSameWayForEveryCounterOfGitHubRequests(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "ok"}, {github.ErrRateLimited, "rate_limited"}, {github.ErrForbidden, "forbidden"},
		{github.ErrNotFound, "not_found"}, {errTest, "error"},
	} {
		if got := githubResult(tc.err); got != tc.want {
			t.Errorf("githubResult(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func finding(code kennel.Code, subject string) kennel.Finding {
	return kennel.Finding{Code: code, Subject: subject}
}

func waivedFinding(code kennel.Code, subject string) kennel.WaivedFinding {
	return kennel.WaivedFinding{Finding: finding(code, subject)}
}

func rowWith(t *testing.T, ev kennel.Evaluation) *store.KennelRepository {
	t.Helper()
	raw, err := marshalJSON(ev)
	if err != nil {
		t.Fatal(err)
	}
	return &store.KennelRepository{Evaluation: raw}
}

// What a check is for is answered by what happens to its findings: opened,
// closed because they were fixed, or waived. A waiver ending is none of these,
// because nothing was fixed and nothing is new.
func TestWhatAnEvaluationChangedAboutAFindingIsCountedOnceAndInTheRightPlace(t *testing.T) {
	a, b, c := kennel.CodeForkCodeRan, kennel.CodeTargetEventRan, kennel.CodeUnservedLabel
	for _, tc := range []struct {
		name                  string
		before, after         kennel.Evaluation
		opened, closed, waive []kennel.Code
	}{
		{"the first evaluation", kennel.Evaluation{}, kennel.Evaluation{Findings: []kennel.Finding{finding(a, "")}}, []kennel.Code{a}, nil, nil},
		{"nothing moved", kennel.Evaluation{Findings: []kennel.Finding{finding(a, "")}}, kennel.Evaluation{Findings: []kennel.Finding{finding(a, "")}}, nil, nil, nil},
		{"a finding was fixed", kennel.Evaluation{Findings: []kennel.Finding{finding(a, "")}}, kennel.Evaluation{}, nil, []kennel.Code{a}, nil},
		{"a finding was waived", kennel.Evaluation{Findings: []kennel.Finding{finding(a, "")}}, kennel.Evaluation{Waived: []kennel.WaivedFinding{waivedFinding(a, "")}}, nil, nil, []kennel.Code{a}},
		{"a finding that stays waived is counted once, when it was waived", kennel.Evaluation{Waived: []kennel.WaivedFinding{waivedFinding(a, "")}}, kennel.Evaluation{Waived: []kennel.WaivedFinding{waivedFinding(a, "")}}, nil, nil, nil},
		{"a waiver ended", kennel.Evaluation{Waived: []kennel.WaivedFinding{waivedFinding(a, "")}}, kennel.Evaluation{Findings: []kennel.Finding{finding(a, "")}}, nil, nil, nil},
		{"a waived finding was then fixed", kennel.Evaluation{Waived: []kennel.WaivedFinding{waivedFinding(a, "")}}, kennel.Evaluation{}, nil, []kennel.Code{a}, nil},
		{"a finding appeared already waived", kennel.Evaluation{}, kennel.Evaluation{Waived: []kennel.WaivedFinding{waivedFinding(a, "")}}, []kennel.Code{a}, nil, nil},
		{"two subjects of one code are two findings", kennel.Evaluation{Findings: []kennel.Finding{finding(a, "x")}}, kennel.Evaluation{Findings: []kennel.Finding{finding(a, "x"), finding(a, "y")}}, []kennel.Code{a}, nil, nil},
		{"one opens as another closes", kennel.Evaluation{Findings: []kennel.Finding{finding(a, "")}}, kennel.Evaluation{Findings: []kennel.Finding{finding(b, "")}}, []kennel.Code{b}, []kennel.Code{a}, nil},
		{"several at once", kennel.Evaluation{Findings: []kennel.Finding{finding(a, ""), finding(b, "")}}, kennel.Evaluation{Findings: []kennel.Finding{finding(c, "")}, Waived: []kennel.WaivedFinding{waivedFinding(a, "")}}, []kennel.Code{c}, []kennel.Code{b}, []kennel.Code{a}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened, closed, waived := kennelMovement(rowWith(t, tc.before), tc.after)
			if !sameCodes(opened, tc.opened) || !sameCodes(closed, tc.closed) || !sameCodes(waived, tc.waive) {
				t.Errorf("opened %v closed %v waived %v; want %v %v %v", opened, closed, waived, tc.opened, tc.closed, tc.waive)
			}
		})
	}
}

func sameCodes(a, b []kennel.Code) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The counters move when an evaluation is kept, and only then: a pass that finds
// nothing changed counts nothing.
func TestTheFindingCountersMoveWhenAnEvaluationIsKeptAndNotOtherwise(t *testing.T) {
	f := newKennelFixture(t)
	f.persistent()
	f.repo("acme/exposed", "public")
	f.ran("acme/exposed", 11, f.pool)
	f.trigger("acme/exposed", 11, "push", false)
	f.pass()
	opened := func(code string) float64 { return testutil.ToFloat64(f.c.metrics.kennelOpened.WithLabelValues(code)) }
	if opened("exposure.public_repo_on_fleet") != 1 || opened("exposure.public_repo_weak_pool") != 1 {
		t.Fatalf("opened = %v and %v, want one each", opened("exposure.public_repo_on_fleet"), opened("exposure.public_repo_weak_pool"))
	}

	f.advance(time.Hour)
	f.pass()
	if opened("exposure.public_repo_weak_pool") != 1 {
		t.Error("a pass that changed nothing counted a finding again")
	}

	row := f.row("acme/exposed")
	if err := f.st.UpsertKennelWaiver(f.ctx, &store.KennelWaiver{
		RepositoryPK: row.ID, Code: "exposure.public_repo_weak_pool", Severity: "error", Reason: "isolated hosts that are rebuilt for every job",
		CreatedBy: "usr_1", CreatedByName: "Ada", ExpiresAt: f.c.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	f.pass()
	if got := testutil.ToFloat64(f.c.metrics.kennelWaived.WithLabelValues("exposure.public_repo_weak_pool")); got != 1 {
		t.Errorf("waived = %v, want 1", got)
	}

	f.pool.Ephemeral = true
	if err := f.st.UpdatePool(f.ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	f.advance(kennelLocalInterval + time.Minute)
	f.pass()
	// The pool is fixed, so the weak-pool finding is gone -- waived, and no longer
	// reported at all -- and the escalated finding is a warning again but is the
	// same finding.
	if got := testutil.ToFloat64(f.c.metrics.kennelClosed.WithLabelValues("exposure.public_repo_weak_pool")); got != 1 {
		t.Errorf("closed = %v, want 1: the pool was fixed", got)
	}
	if opened("exposure.public_repo_on_fleet") != 1 {
		t.Error("a finding that only changed severity was counted as new")
	}
}

func TestOnlyAnErrorInTheExposureAreaRaisesAProblem(t *testing.T) {
	findings := []kennel.Finding{
		{Code: kennel.CodeForkCodeRan, Severity: kennel.SeverityError},
		{Code: kennel.CodeTargetEventRan, Severity: kennel.SeverityWarning},
		{Code: kennel.CodeUnservedLabel, Severity: kennel.SeverityError}, // an error in another area
		{Code: kennel.CodePublicRepoWeakPool, Severity: kennel.SeverityError},
	}
	got := exposureErrors(findings)
	if len(got) != 2 || got[0].Code != kennel.CodeForkCodeRan || got[1].Code != kennel.CodePublicRepoWeakPool {
		t.Errorf("exposure errors = %+v, want the fork and weak-pool errors, in order", got)
	}
	if len(exposureErrors(nil)) != 0 {
		t.Error("no findings gave an exposure error")
	}
}

// More repositories than a page, with errors on the second: the walk must not
// stop at the first, or the count would be a page's worth.
func TestEveryRepositoryWithAnOpenErrorIsCountedNotJustTheFirstPage(t *testing.T) {
	f := newKennelFixture(t)
	f.persistent()
	for i := range 130 {
		name := fmt.Sprintf("acme/r%03d", i)
		f.repo(name, "public")
		f.ran(name, int64(i+1), f.pool)
		f.trigger(name, int64(i+1), "push", false)
	}
	f.pass()
	got := kennelProblemsOf(t, f, "kennel.exposure")
	if len(got) != 1 || got[0].Title != "Kennel Club: 130 repositories have an exposure error open" {
		t.Errorf("problems = %+v, want one that counts all 130 exposed repositories", got)
	}
}
