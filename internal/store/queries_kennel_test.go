package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var kennelNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// kennelStore is a store whose clock the test holds, with one installation.
func kennelStore(t *testing.T) (*Store, *Installation, *time.Time) {
	t.Helper()
	clock := kennelNow
	s := newTestStoreAt(t, func() time.Time { return clock })
	inst := &Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(context.Background(), inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	return s, inst, &clock
}

func touch(t *testing.T, s *Store, inst *Installation, id int64, name, vis string) *KennelRepository {
	t.Helper()
	r, err := s.TouchKennelRepository(context.Background(), KennelRepositoryRef{
		GitHubHost: "github.com", RepositoryID: id, InstallationID: inst.ID, FullName: name, Visibility: vis,
	})
	if err != nil {
		t.Fatalf("TouchKennelRepository(%s): %v", name, err)
	}
	return r
}

func record(state string, errs, warns, infos int, findings ...string) KennelEvaluationRecord {
	var fs []map[string]string
	for _, c := range findings {
		fs = append(fs, map[string]string{"code": c})
	}
	doc, _ := json.Marshal(map[string]any{"findings": fs})
	return KennelEvaluationRecord{
		State: state, EvaluatorVersion: 1, EvaluatedAt: kennelNow, NextDueAt: kennelNow.Add(24 * time.Hour),
		InputsDigest: "d1", Coverage: json.RawMessage(`{}`), Evaluation: doc, Watermark: json.RawMessage(`{}`),
		OpenErrors: errs, OpenWarnings: warns, OpenInfos: infos,
	}
}

// A repository is its GitHub ID. Touching it again, under a new name or from a
// new installation, must find the same row: a rename that made a second one
// would strand the waivers on the first.
func TestTouchingARepositoryFindsTheSameRowWhateverItIsCalled(t *testing.T) {
	s, inst, _ := kennelStore(t)
	first := touch(t, s, inst, 77, "acme/api", "private")
	again := touch(t, s, inst, 77, "acme/renamed", "")
	if again.ID != first.ID || !strings.HasPrefix(first.ID, "kcr_") {
		t.Fatalf("ids = %q then %q, want one stable kcr_ id", first.ID, again.ID)
	}
	if again.FullName != "acme/renamed" {
		t.Errorf("name = %q, want the new one", again.FullName)
	}
	if again.Visibility != "private" {
		t.Errorf("visibility = %q: a read that did not learn it must keep what was known", again.Visibility)
	}
	other := touch(t, s, inst, 78, "acme/api", "private")
	if other.ID == first.ID {
		t.Error("a different GitHub ID shared a row")
	}
}

func TestTheSameIDOnAnotherHostIsAnotherRepository(t *testing.T) {
	s, inst, _ := kennelStore(t)
	a := touch(t, s, inst, 5, "acme/x", "public")
	b, err := s.TouchKennelRepository(context.Background(), KennelRepositoryRef{
		GitHubHost: "ghe.example", RepositoryID: 5, InstallationID: inst.ID, FullName: "acme/x"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Error("two hosts' repositories with one numeric ID shared a row")
	}
}

func TestARepositoryNeedsAnIdentityToBeKept(t *testing.T) {
	s, inst, _ := kennelStore(t)
	for name, ref := range map[string]KennelRepositoryRef{
		"no host":         {RepositoryID: 1, InstallationID: inst.ID, FullName: "a/b"},
		"no repository":   {GitHubHost: "github.com", InstallationID: inst.ID, FullName: "a/b"},
		"no installation": {GitHubHost: "github.com", RepositoryID: 1, FullName: "a/b"},
		"no name":         {GitHubHost: "github.com", RepositoryID: 1, InstallationID: inst.ID},
	} {
		if _, err := s.TouchKennelRepository(context.Background(), ref); !errors.Is(err, ErrInvalidKennelEvaluation) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
}

// A repository that has just become public is the one a stale answer is worst
// for, so a change of visibility makes it due at once. Re-reading the same
// visibility must not, or every daily read would reset its own schedule.
func TestOnlyAChangeOfVisibilityMakesARepositoryDueAtOnce(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	r := touch(t, s, inst, 1, "acme/api", "private")
	if err := s.SaveKennelEvaluation(ctx, r.ID, record("best_in_show", 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	same := touch(t, s, inst, 1, "acme/api", "private")
	if !same.NextDueAt.Equal(kennelNow.Add(24 * time.Hour)) {
		t.Errorf("next due = %s after an unchanged visibility, want the schedule kept", same.NextDueAt)
	}
	unknown := touch(t, s, inst, 1, "acme/api", "")
	if !unknown.NextDueAt.Equal(kennelNow.Add(24 * time.Hour)) {
		t.Error("a read that did not learn visibility reset the schedule")
	}
	flipped := touch(t, s, inst, 1, "acme/api", "public")
	if !flipped.NextDueAt.Equal(time.UnixMilli(0).UTC()) || flipped.Visibility != "public" {
		t.Errorf("next due = %s visibility = %s, want due now and public", flipped.NextDueAt, flipped.Visibility)
	}
}

func TestAnEvaluationRoundTripsAndReplacesTheLastOne(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	r := touch(t, s, inst, 1, "acme/api", "public")
	if r.State != "pending" || r.EvaluatedAt != nil {
		t.Fatalf("a new repository is %s, evaluated %v; want pending and never", r.State, r.EvaluatedAt)
	}
	rec := record("attention", 1, 2, 3, "exposure.fork_code_ran")
	rec.Waived = 4
	rec.Watermark = json.RawMessage(`{"after":99}`)
	if err := s.SaveKennelEvaluation(ctx, r.ID, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetKennelRepository(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "attention" || got.OpenErrors != 1 || got.OpenWarnings != 2 || got.OpenInfos != 3 || got.Waived != 4 ||
		got.EvaluatedAt == nil || !got.EvaluatedAt.Equal(kennelNow) || got.InputsDigest != "d1" || got.EvaluatorVersion != 1 {
		t.Errorf("round trip lost something: %+v", got)
	}
	if string(got.Watermark) != `{"after":99}` || !strings.Contains(string(got.Evaluation), "exposure.fork_code_ran") {
		t.Errorf("documents = %s / %s", got.Watermark, got.Evaluation)
	}
	if err := s.SaveKennelEvaluation(ctx, r.ID, record("best_in_show", 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetKennelRepository(ctx, r.ID)
	if got.State != "best_in_show" || got.OpenErrors != 0 || strings.Contains(string(got.Evaluation), "fork_code_ran") {
		t.Errorf("the last evaluation was not replaced: %+v", got)
	}
}

// SQLite's json_each aborts a whole query on a malformed document, so a bad row
// written once would stop the Overview counting every other repository. The
// write is where that is prevented, and a refused write must leave the row and
// the counts alone.
func TestADocumentThatIsNotJSONOrIsTooBigIsRefusedAndHarmsNothing(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	good := touch(t, s, inst, 1, "acme/a", "public")
	bad := touch(t, s, inst, 2, "acme/b", "public")
	if err := s.SaveKennelEvaluation(ctx, good.ID, record("attention", 1, 0, 0, "exposure.fork_code_ran")); err != nil {
		t.Fatal(err)
	}
	huge := json.RawMessage(`{"x":"` + strings.Repeat("a", MaxKennelDocumentBytes) + `"}`)
	for name, mutate := range map[string]func(*KennelEvaluationRecord){
		"malformed evaluation": func(r *KennelEvaluationRecord) { r.Evaluation = json.RawMessage(`{"findings":[`) },
		"malformed coverage":   func(r *KennelEvaluationRecord) { r.Coverage = json.RawMessage(`not json`) },
		"malformed watermark":  func(r *KennelEvaluationRecord) { r.Watermark = json.RawMessage(`{`) },
		"empty evaluation":     func(r *KennelEvaluationRecord) { r.Evaluation = nil },
		"an oversize document": func(r *KennelEvaluationRecord) { r.Evaluation = huge },
	} {
		rec := record("attention", 1, 0, 0, "exposure.fork_code_ran")
		mutate(&rec)
		if err := s.SaveKennelEvaluation(ctx, bad.ID, rec); !errors.Is(err, ErrInvalidKennelEvaluation) {
			t.Errorf("%s: err = %v, want a refusal", name, err)
		}
	}
	after, _ := s.GetKennelRepository(ctx, bad.ID)
	if after.State != "pending" || string(after.Evaluation) != "{}" {
		t.Errorf("a refused write changed the row: %+v", after)
	}
	counts, err := s.KennelCheckCounts(ctx)
	if err != nil || counts["exposure.fork_code_ran"] != 1 {
		t.Errorf("counts = %v, err = %v: one refused write must not disturb the Overview", counts, err)
	}
}

func TestSavingForARepositoryThatIsGoneSaysSo(t *testing.T) {
	s, _, _ := kennelStore(t)
	if err := s.SaveKennelEvaluation(context.Background(), "kcr_nope", record("pending", 0, 0, 0)); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetKennelRepository(context.Background(), "kcr_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get: err = %v, want ErrNotFound", err)
	}
	if err := s.RequestKennelRecheck(context.Background(), "kcr_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("recheck: err = %v, want ErrNotFound", err)
	}
}

func TestListingPutsTheWorstRepositoryFirstAndHonoursItsFilters(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	for _, c := range []struct {
		id   int64
		name string
		rec  KennelEvaluationRecord
	}{
		{1, "acme/calm", record("best_in_show", 0, 0, 0)},
		{2, "acme/Warn", record("attention", 0, 2, 0, "capacity.unserved_label")},
		{3, "acme/bad", record("attention", 1, 0, 0, "exposure.fork_code_ran", "exposure.public_repo_on_fleet")},
		{4, "acme/100%_odd", record("partial", 0, 0, 0)},
	} {
		r := touch(t, s, inst, c.id, c.name, "public")
		if err := s.SaveKennelEvaluation(ctx, r.ID, c.rec); err != nil {
			t.Fatal(err)
		}
	}
	names := func(f KennelFilter, p Page) []string {
		t.Helper()
		rs, _, err := s.ListKennelRepositories(ctx, f, p)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rs {
			out = append(out, r.FullName)
		}
		return out
	}
	if got := strings.Join(names(KennelFilter{}, Page{}), ","); got != "acme/bad,acme/Warn,acme/100%_odd,acme/calm" {
		t.Errorf("default order = %s, want errors, then warnings, then the rest by name", got)
	}
	if got := strings.Join(names(KennelFilter{}, Page{Sort: "name"}), ","); got != "acme/100%_odd,acme/bad,acme/calm,acme/Warn" {
		t.Errorf("by name = %s", got)
	}
	if got := names(KennelFilter{States: []string{"attention"}}, Page{}); len(got) != 2 {
		t.Errorf("state filter = %v", got)
	}
	if got := names(KennelFilter{Code: "exposure.fork_code_ran"}, Page{}); len(got) != 1 || got[0] != "acme/bad" {
		t.Errorf("code filter = %v, want only the repository that has it", got)
	}
	if got := names(KennelFilter{Q: "WARN"}, Page{}); len(got) != 1 || got[0] != "acme/Warn" {
		t.Errorf("search = %v, want a case-insensitive match", got)
	}
	// A percent sign in what somebody types is a character, not a wildcard.
	if got := names(KennelFilter{Q: "100%"}, Page{}); len(got) != 1 || got[0] != "acme/100%_odd" {
		t.Errorf("search for a percent sign = %v", got)
	}
	if got := names(KennelFilter{Q: "%"}, Page{}); len(got) != 1 {
		t.Errorf("a lone percent sign matched %v, want only the name that contains one", got)
	}
	rs, total, _ := s.ListKennelRepositories(ctx, KennelFilter{}, Page{Limit: 2})
	if total != 4 || len(rs) != 2 {
		t.Errorf("paging: total %d, page %d", total, len(rs))
	}
}

// "Active" is asked of the jobs, as "served" is, and not of the row. A row's
// last_served_at is when a pass last saw the repository in the served list, which
// under the installation scope is every pass for every repository the App can see,
// so it cannot tell a busy repository from one that has been quiet for a quarter.
func TestTheListCanBeNarrowedToTheRepositoriesTheFleetIsServing(t *testing.T) {
	ctx := context.Background()
	s, inst, clock := kennelStore(t)
	now := *clock
	since := now.Add(-30 * 24 * time.Hour)
	for i, name := range []string{"acme/busy", "acme/Mixed", "acme/quiet", "acme/never", "acme/hosted", "acme/edge", "acme/orphan"} {
		touch(t, s, inst, int64(i+1), name, "public")
	}
	ran := func(id int64, repo string, queued time.Time) {
		job(t, s, id, Job{Repo: repo, State: JobCompleted, QueuedAt: queued, RunnerID: "run_1", PoolID: "pool_1", Labels: StringSlice{"self-hosted"}})
	}
	ran(1, "acme/busy", now.Add(-time.Hour))
	// Recorded as the webhook spelt it, listed as the listing did.
	ran(2, "acme/mixed", now.Add(-48*time.Hour))
	ran(3, "acme/quiet", now.Add(-40*24*time.Hour))
	// Somebody else's runner is not this fleet having a hand in it.
	job(t, s, 4, Job{Repo: "acme/hosted", State: JobCompleted, QueuedAt: now.Add(-time.Hour), Labels: StringSlice{"ubuntu-latest"}})
	// Exactly at the start of the window is inside it.
	ran(5, "acme/edge", since)
	// A job that was never attributed to an installation is not one the fleet can
	// say it served, here as in the loop's own list.
	job(t, s, 6, Job{Repo: "acme/orphan", InstallationID: "-", State: JobCompleted, QueuedAt: now.Add(-time.Hour), RunnerID: "run_1", PoolID: "pool_1", Labels: StringSlice{"self-hosted"}})

	yes, no := true, false
	list := func(f KennelFilter, p Page) (string, int) {
		t.Helper()
		f.ServedSince = since
		p.Sort = "name"
		rs, total, err := s.ListKennelRepositories(ctx, f, p)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, r := range rs {
			names = append(names, r.FullName)
		}
		return strings.Join(names, ","), total
	}
	if got, total := list(KennelFilter{Served: &yes}, Page{}); got != "acme/busy,acme/edge,acme/Mixed" || total != 3 {
		t.Errorf("active = %s (total %d), want the three with a job in the window, the edge and the differently spelt one included", got, total)
	}
	if got, total := list(KennelFilter{Served: &no}, Page{}); got != "acme/hosted,acme/never,acme/orphan,acme/quiet" || total != 4 {
		t.Errorf("not active = %s (total %d), want the four with none: no job, an old one, a hosted one and one with no installation", got, total)
	}
	if got, total := list(KennelFilter{}, Page{}); total != 7 || strings.Count(got, ",") != 6 {
		t.Errorf("unfiltered = %s (total %d), want all seven: the filter is only there when asked for", got, total)
	}
	// The total is of what matches and not of the page, and the filter narrows
	// with the others rather than replacing them.
	if got, total := list(KennelFilter{Served: &yes}, Page{Limit: 1}); got != "acme/busy" || total != 3 {
		t.Errorf("a page of one = %s (total %d), want the first and a total of three", got, total)
	}
	if got, total := list(KennelFilter{Served: &yes, Q: "bus"}, Page{}); got != "acme/busy" || total != 1 {
		t.Errorf("active and searched = %s (total %d), want only acme/busy", got, total)
	}
	if got, _ := list(KennelFilter{Served: &no, Q: "bus"}, Page{}); got != "" {
		t.Errorf("not active and searched for the busy one = %s, want nothing", got)
	}
}

func TestDueRepositoriesAreThoseWhoseTimeHasComeAndRecheckMakesOneDue(t *testing.T) {
	ctx := context.Background()
	s, inst, clock := kennelStore(t)
	r := touch(t, s, inst, 1, "acme/api", "public")
	if err := s.SaveKennelEvaluation(ctx, r.ID, record("best_in_show", 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueKennelRepositories(ctx, 10); len(due) != 0 {
		t.Fatalf("due = %d straight after an evaluation", len(due))
	}
	*clock = kennelNow.Add(25 * time.Hour)
	if due, _ := s.DueKennelRepositories(ctx, 10); len(due) != 1 {
		t.Errorf("due = %d a day later, want 1", len(due))
	}
	*clock = kennelNow
	if err := s.RequestKennelRecheck(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueKennelRepositories(ctx, 10); len(due) != 1 {
		t.Errorf("due = %d after a recheck was asked for, want 1", len(due))
	}
}

func TestACheckCountsARepositoryOncePerCodeHoweverManyFindingsItHas(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	a := touch(t, s, inst, 1, "acme/a", "public")
	b := touch(t, s, inst, 2, "acme/b", "public")
	touch(t, s, inst, 3, "acme/never-evaluated", "public")
	_ = s.SaveKennelEvaluation(ctx, a.ID, record("attention", 2, 0, 0, "exposure.fork_code_ran", "exposure.fork_code_ran"))
	_ = s.SaveKennelEvaluation(ctx, b.ID, record("attention", 1, 0, 0, "exposure.fork_code_ran", "capacity.unserved_label"))
	got, err := s.KennelCheckCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["exposure.fork_code_ran"] != 2 || got["capacity.unserved_label"] != 1 || len(got) != 2 {
		t.Errorf("counts = %v", got)
	}
}

func TestCountsAddUpEveryRepositoryByStateAndSeverity(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	empty, err := s.KennelCounts(ctx)
	if err != nil || empty.Repositories != 0 || empty.OldestEvaluation != nil {
		t.Fatalf("empty counts = %+v, %v", empty, err)
	}
	a := touch(t, s, inst, 1, "acme/a", "public")
	b := touch(t, s, inst, 2, "acme/b", "public")
	touch(t, s, inst, 3, "acme/c", "public")
	ra := record("attention", 1, 2, 3)
	ra.Waived = 1
	_ = s.SaveKennelEvaluation(ctx, a.ID, ra)
	rb := record("best_in_show", 0, 0, 0)
	rb.EvaluatedAt = kennelNow.Add(-time.Hour)
	_ = s.SaveKennelEvaluation(ctx, b.ID, rb)
	c, err := s.KennelCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Repositories != 3 || c.ByState["attention"] != 1 || c.ByState["best_in_show"] != 1 || c.ByState["pending"] != 1 ||
		c.OpenErrors != 1 || c.OpenWarnings != 2 || c.OpenInfos != 3 || c.Waived != 1 {
		t.Errorf("counts = %+v", c)
	}
	if c.OldestEvaluation == nil || !c.OldestEvaluation.Equal(kennelNow.Add(-time.Hour)) {
		t.Errorf("oldest = %v, want the hour-old evaluation", c.OldestEvaluation)
	}
}

// Kennel Club's only delete is of its own rows, and what it takes with them is
// their waivers: a repository nobody has served for ninety days has no use for
// a decision about it.
func TestPruningRemovesOnlyWhatWasLastServedBeforeTheCutoffAndItsWaivers(t *testing.T) {
	ctx := context.Background()
	s, inst, clock := kennelStore(t)
	old := touch(t, s, inst, 1, "acme/old", "public")
	*clock = kennelNow.Add(100 * 24 * time.Hour)
	fresh := touch(t, s, inst, 2, "acme/fresh", "public")
	w := &KennelWaiver{RepositoryPK: old.ID, Code: "exposure.fork_code_ran", Severity: "error", Reason: "isolated hosts",
		CreatedBy: "usr_1", CreatedByName: "ada", ExpiresAt: clock.Add(24 * time.Hour)}
	if err := s.UpsertKennelWaiver(ctx, w); err != nil {
		t.Fatal(err)
	}
	gone, err := s.PruneKennelRepositories(ctx, clock.Add(-90*24*time.Hour))
	if err != nil || len(gone) != 1 || gone[0] != old.ID {
		t.Fatalf("pruned %v, %v; want only %s", gone, err, old.ID)
	}
	if _, err := s.GetKennelRepository(ctx, old.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old repository survived: %v", err)
	}
	if _, err := s.GetKennelRepository(ctx, fresh.ID); err != nil {
		t.Errorf("the fresh repository went too: %v", err)
	}
	if _, err := s.GetKennelWaiver(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the pruned repository's waiver survived: %v", err)
	}
}

func TestDeletingAnInstallationRemovesItsRepositoriesAndTheirWaivers(t *testing.T) {
	ctx := context.Background()
	s, inst, clock := kennelStore(t)
	r := touch(t, s, inst, 1, "acme/a", "public")
	w := &KennelWaiver{RepositoryPK: r.ID, Code: "x", Severity: "error", Reason: "r", CreatedBy: "u", CreatedByName: "n", ExpiresAt: clock.Add(time.Hour)}
	if err := s.UpsertKennelWaiver(ctx, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteInstallation(ctx, inst.ID); err != nil {
		t.Fatalf("DeleteInstallation: %v", err)
	}
	if _, err := s.GetKennelRepository(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("repository survived its installation: %v", err)
	}
	if _, err := s.GetKennelWaiver(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("waiver survived its installation: %v", err)
	}
}

func TestAWaiverRenewsRatherThanStackingAndKeepsItsID(t *testing.T) {
	ctx := context.Background()
	s, inst, clock := kennelStore(t)
	r := touch(t, s, inst, 1, "acme/a", "public")
	first := &KennelWaiver{RepositoryPK: r.ID, Code: "exposure.fork_code_ran", Severity: "error", Reason: "first reason",
		CreatedBy: "usr_1", CreatedByName: "ada", ExpiresAt: clock.Add(24 * time.Hour)}
	if err := s.UpsertKennelWaiver(ctx, first); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.ID, "kcw_") || !first.CreatedAt.Equal(kennelNow) {
		t.Fatalf("waiver = %+v, want a kcw_ id and the store's clock", first)
	}
	*clock = kennelNow.Add(time.Hour)
	again := &KennelWaiver{RepositoryPK: r.ID, Code: "exposure.fork_code_ran", Severity: "warning", Reason: "second reason",
		CreatedBy: "usr_2", CreatedByName: "grace", ExpiresAt: clock.Add(48 * time.Hour)}
	if err := s.UpsertKennelWaiver(ctx, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Errorf("renewed waiver got a new id %s, want %s kept", again.ID, first.ID)
	}
	list, _ := s.ListKennelWaivers(ctx, r.ID)
	if len(list) != 1 || list[0].Reason != "second reason" || list[0].CreatedByName != "grace" || list[0].Severity != "warning" {
		t.Errorf("waivers = %+v, want the one, renewed", list)
	}
	// A different subject is a different waiver.
	other := &KennelWaiver{RepositoryPK: r.ID, Code: "exposure.fork_code_ran", Subject: "workflow:ab12", Severity: "error", Reason: "r",
		CreatedBy: "u", CreatedByName: "n", ExpiresAt: clock.Add(time.Hour)}
	if err := s.UpsertKennelWaiver(ctx, other); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountKennelWaivers(ctx, r.ID); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
}

// A decision that has already ended is not a decision, and recording one would
// only be a way to put a reason on a finding that is open.
func TestAWaiverThatHasAlreadyEndedIsRefused(t *testing.T) {
	s, inst, clock := kennelStore(t)
	r := touch(t, s, inst, 1, "acme/a", "public")
	for _, end := range []time.Time{*clock, clock.Add(-time.Hour)} {
		w := &KennelWaiver{RepositoryPK: r.ID, Code: "x", Severity: "error", Reason: "r", CreatedBy: "u", CreatedByName: "n", ExpiresAt: end}
		if err := s.UpsertKennelWaiver(context.Background(), w); !errors.Is(err, ErrInvalidKennelEvaluation) {
			t.Errorf("ending %s: err = %v, want a refusal", end, err)
		}
	}
}

// The Go API always supplies an end, so the schema's NOT NULL is not what is
// protecting this today; it is what protects it from the next change. A decision
// that never ends must not be storable even by a mistake.
func TestAWaiverWithNoEndCannotBeStoredAtAll(t *testing.T) {
	ctx := context.Background()
	s, inst, clock := kennelStore(t)
	r := touch(t, s, inst, 1, "acme/a", "public")
	_, err := s.exec(ctx, `INSERT INTO kennel_waivers
		(id, repository_pk, code, subject, severity, reason, created_by, created_by_name, created_at, expires_at)
		VALUES ('kcw_x', ?, 'c', '', 'error', 'r', 'u', 'n', ?, NULL)`, r.ID, ms(*clock))
	if err == nil {
		t.Fatal("a waiver with no end was stored")
	}
}

func TestWaiversAreListedLongestFirstAndCanBeDeleted(t *testing.T) {
	ctx := context.Background()
	s, inst, clock := kennelStore(t)
	r := touch(t, s, inst, 1, "acme/a", "public")
	var ids []string
	for i, c := range []string{"a.one", "a.two", "a.three"} {
		w := &KennelWaiver{RepositoryPK: r.ID, Code: c, Severity: "warning", Reason: "r", CreatedBy: "u", CreatedByName: "n",
			ExpiresAt: clock.Add(time.Duration(i+1) * time.Hour)}
		if err := s.UpsertKennelWaiver(ctx, w); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, w.ID)
	}
	list, _ := s.ListKennelWaivers(ctx, r.ID)
	if len(list) != 3 || list[0].Code != "a.three" || list[2].Code != "a.one" {
		t.Errorf("order = %v, want the longest-lasting first", list)
	}
	if err := s.DeleteKennelWaiver(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteKennelWaiver(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: %v, want ErrNotFound", err)
	}
	if err := s.DeleteKennelWaivers(ctx, ids[1:]); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountKennelWaivers(ctx, r.ID); n != 0 {
		t.Errorf("count = %d after deleting them all", n)
	}
}

// The migration is the one place the brief's "schema changes are additive" can
// be held by a test rather than by memory. 0009 and 0044 rebuild tables, which
// is sometimes forced, but Kennel Club's files only ever create what is new.
func TestKennelMigrationsOnlyAddTables(t *testing.T) {
	files, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	create := regexp.MustCompile(`(?is)^CREATE\s+(UNIQUE\s+)?(TABLE|INDEX)\s`)
	for _, f := range files {
		if !strings.Contains(f.Name(), "kennel") {
			continue
		}
		checked++
		raw, err := migrationFS.ReadFile("migrations/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		var body strings.Builder
		for _, line := range strings.Split(string(raw), "\n") {
			if i := strings.Index(line, "--"); i >= 0 {
				line = line[:i]
			}
			body.WriteString(line + "\n")
		}
		for _, stmt := range strings.Split(body.String(), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt != "" && !create.MatchString(stmt) {
				t.Errorf("%s: %q is not a CREATE TABLE or CREATE INDEX; Kennel Club's schema changes only add", f.Name(), firstLine(stmt))
			}
		}
	}
	if checked == 0 {
		t.Fatal("no kennel migration was found; the test is looking in the wrong place")
	}
	// Belt and braces: the file must not so much as mention rebuilding the table
	// whose rebuild has a test of its own.
	raw, _ := os.ReadFile("migrations/0080_kennel_club.sql")
	if strings.Contains(strings.ToLower(string(raw)), "alter table jobs") {
		t.Error("0080 touches the jobs table")
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// ---------------------------------------------------------------------------
// Fleet facts
// ---------------------------------------------------------------------------

func job(t *testing.T, s *Store, id int64, j Job) {
	t.Helper()
	j.GitHubJobID = id
	if j.Repo == "" {
		j.Repo = "acme/api"
	}
	switch j.InstallationID {
	case "":
		j.InstallationID = "ins_a"
	case "-": // a job that was never attributed to an installation
		j.InstallationID = ""
	}
	if _, err := s.UpsertJob(context.Background(), &j); err != nil {
		t.Fatalf("UpsertJob(%d): %v", id, err)
	}
}

// "Served" means the same as on the Jobs page: a job a pool claimed or a runner
// of this fleet ran, or one nobody serves that is still waiting. Somebody
// else's hosted-runner job in the same repository is not this fleet's business.
func TestAServedRepositoryIsOneTheFleetHadAHandIn(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	q := *clock
	job(t, s, 1, Job{Repo: "acme/ran", State: JobCompleted, QueuedAt: q, RunnerID: "run_1", PoolID: "pool_1", Labels: StringSlice{"self-hosted"}})
	job(t, s, 2, Job{Repo: "acme/claimed", State: JobQueued, QueuedAt: q, Matched: true, Labels: StringSlice{"self-hosted"}})
	job(t, s, 3, Job{Repo: "acme/stuck", State: JobQueued, QueuedAt: q, Labels: StringSlice{"typo-linux"}})
	job(t, s, 4, Job{Repo: "acme/hosted", State: JobCompleted, QueuedAt: q, Labels: StringSlice{"ubuntu-latest"}})
	job(t, s, 5, Job{Repo: "acme/old", State: JobCompleted, QueuedAt: q.Add(-40 * 24 * time.Hour), RunnerID: "run_1", Labels: StringSlice{"self-hosted"}})
	job(t, s, 6, Job{Repo: "acme/noinstall", InstallationID: "-", State: JobCompleted, QueuedAt: q, RunnerID: "run_1"})
	got, err := s.KennelServedRepos(ctx, q.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, k := range got {
		names = append(names, k.Repo)
	}
	if want := "acme/claimed,acme/ran,acme/stuck"; strings.Join(names, ",") != want {
		t.Errorf("served = %v, want %s: a hosted job, a job outside the window and a job with no installation are not served", names, want)
	}
}

func TestFleetFactsCountWhatThisFleetRanQueuedAndLeftWaiting(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	now := *clock
	h := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	// Two jobs ran on pool_b, one on pool_a, one that somebody else ran. The
	// busier pool sorts second by name, so an order by name cannot pass for an
	// order by use.
	job(t, s, 1, Job{State: JobCompleted, QueuedAt: now.Add(-time.Hour), StartedAt: h(-59 * time.Minute), CompletedAt: h(-50 * time.Minute), RunnerID: "run_1", PoolID: "pool_b", Conclusion: "success"})
	job(t, s, 2, Job{State: JobCompleted, QueuedAt: now.Add(-time.Hour), StartedAt: h(-59 * time.Minute), CompletedAt: h(-50 * time.Minute), RunnerID: "run_2", PoolID: "pool_b", Conclusion: "success"})
	job(t, s, 3, Job{State: JobInProgress, QueuedAt: now.Add(-time.Hour), StartedAt: h(-59 * time.Minute), RunnerID: "run_3", PoolID: "pool_a"})
	job(t, s, 4, Job{State: JobCompleted, QueuedAt: now.Add(-time.Hour), StartedAt: h(-59 * time.Minute), CompletedAt: h(-50 * time.Minute), Conclusion: "success", Labels: StringSlice{"ubuntu-latest"}})
	// One a pool has claimed and is waiting, and one nobody has claimed: only the
	// first is waiting *for this fleet*.
	job(t, s, 5, Job{State: JobQueued, QueuedAt: now.Add(-time.Minute), Matched: true})
	job(t, s, 7, Job{State: JobQueued, QueuedAt: now.Add(-time.Minute), Labels: StringSlice{"typo-linux"}})
	// Another repository's job must not be counted.
	job(t, s, 6, Job{Repo: "acme/other", State: JobCompleted, QueuedAt: now.Add(-time.Hour), RunnerID: "run_9", PoolID: "pool_1", StartedAt: h(-59 * time.Minute), CompletedAt: h(-50 * time.Minute)})
	f, err := s.KennelFleetFacts(ctx, KennelFleetQuery{Repo: "acme/api", Since: now.Add(-30 * 24 * time.Hour), UnservedSince: now.Add(-7 * 24 * time.Hour), LongFloor: 5 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if f.Ran != 3 || f.Queued != 1 {
		t.Errorf("ran %d, queued %d; want 3 and 1", f.Ran, f.Queued)
	}
	if len(f.Pools) != 2 || f.Pools[0] != (KennelPoolJobs{"pool_b", 2}) || f.Pools[1] != (KennelPoolJobs{"pool_a", 1}) {
		t.Errorf("pools = %+v, want the busier pool_b first, then pool_a", f.Pools)
	}
}

// A job that waited for a label nobody serves, and was then cancelled, waited
// until it was cancelled. One still waiting has waited until now. One that
// started was run by something, and is not this fleet's unserved job.
func TestUnservedWaitsAreMeasuredToNowOrToTheCancellationAndSkipJobsThatRan(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	now := *clock
	h := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	job(t, s, 1, Job{State: JobQueued, QueuedAt: now.Add(-3 * time.Hour), Labels: StringSlice{"typo-linux"}})
	job(t, s, 2, Job{State: JobCompleted, Conclusion: "cancelled", QueuedAt: now.Add(-5 * time.Hour), CompletedAt: h(-4 * time.Hour), Labels: StringSlice{"typo-linux"}})
	job(t, s, 3, Job{State: JobCompleted, Conclusion: "success", QueuedAt: now.Add(-2 * time.Hour), StartedAt: h(-time.Hour), CompletedAt: h(-30 * time.Minute), Labels: StringSlice{"elsewhere"}})
	job(t, s, 4, Job{State: JobQueued, QueuedAt: now.Add(-time.Hour), Labels: StringSlice{"ubuntu-latest"}})
	job(t, s, 5, Job{State: JobQueued, QueuedAt: now.Add(-time.Hour), Matched: true, Labels: StringSlice{"self-hosted"}})
	job(t, s, 6, Job{State: JobQueued, QueuedAt: now.Add(-10 * 24 * time.Hour), Labels: StringSlice{"typo-linux"}})
	f, err := s.KennelFleetFacts(ctx, KennelFleetQuery{Repo: "acme/api", Since: now.Add(-30 * 24 * time.Hour), UnservedSince: now.Add(-7 * 24 * time.Hour), LongFloor: 5 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Unserved) != 2 || f.Unserved[0] != 3*time.Hour || f.Unserved[1] != time.Hour {
		t.Errorf("unserved = %v, want [3h, 1h]: still waiting counts to now, a cancelled job to its cancellation, and neither a job that ran, a hosted job, a claimed job nor one older than a week", f.Unserved)
	}
}

func TestLongJobsAreOnlyTheOnesThisFleetsRunnersHeldForAtLeastTheFloor(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	now := *clock
	h := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	job(t, s, 1, Job{State: JobCompleted, Conclusion: "cancelled", RunnerID: "run_1", QueuedAt: now.Add(-7 * time.Hour), StartedAt: h(-6*time.Hour - time.Minute), CompletedAt: h(-time.Minute)})
	job(t, s, 2, Job{State: JobCompleted, Conclusion: "success", RunnerID: "run_1", QueuedAt: now.Add(-7 * time.Hour), StartedAt: h(-2 * time.Hour), CompletedAt: h(-time.Minute)})
	job(t, s, 3, Job{State: JobCompleted, Conclusion: "cancelled", QueuedAt: now.Add(-7 * time.Hour), StartedAt: h(-6*time.Hour - time.Minute), CompletedAt: h(-time.Minute)})
	f, err := s.KennelFleetFacts(ctx, KennelFleetQuery{Repo: "acme/api", Since: now.Add(-30 * 24 * time.Hour), UnservedSince: now.Add(-7 * 24 * time.Hour), LongFloor: 5 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Long) != 1 || f.Long[0].Conclusion != "cancelled" || f.Long[0].Duration != 6*time.Hour {
		t.Errorf("long = %+v, want the one six-hour job a runner of this fleet held; a short job and a hosted one are not it", f.Long)
	}
}

func TestRunsAreListedNewestFirstAboveTheWatermarkWithTheirFirstQueueTime(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	now := *clock
	for i, run := range []int64{100, 101, 101, 102, 103} {
		job(t, s, int64(i+1), Job{State: JobCompleted, RunnerID: "run_1", GitHubRunID: run, QueuedAt: now.Add(-time.Duration(10-i) * time.Minute)})
	}
	job(t, s, 9, Job{State: JobCompleted, GitHubRunID: 500, QueuedAt: now}) // not run here
	job(t, s, 10, Job{Repo: "acme/other", State: JobCompleted, RunnerID: "run_1", GitHubRunID: 600, QueuedAt: now})
	runs, err := s.KennelRunsAfter(ctx, "acme/api", 100, now.Add(-time.Hour), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != 103 || runs[1].ID != 102 {
		t.Errorf("runs = %+v, want the two newest above the watermark, newest first", runs)
	}
	// The watermark is the highest run already examined, so it is not read again.
	above, _ := s.KennelRunsAfter(ctx, "acme/api", 100, now.Add(-time.Hour), 10)
	if len(above) != 3 || above[2].ID != 101 {
		t.Errorf("runs above the watermark = %+v, want 103, 102, 101 and not 100 itself", above)
	}
	all, _ := s.KennelRunsAfter(ctx, "acme/api", 0, now.Add(-time.Hour), 10)
	if len(all) != 4 {
		t.Errorf("runs = %d, want 4 distinct runs the fleet ran", len(all))
	}
	var r101 KennelRun
	for _, r := range all {
		if r.ID == 101 {
			r101 = r
		}
	}
	if !r101.QueuedAt.Equal(now.Add(-9 * time.Minute)) {
		t.Errorf("run 101 first queued %s, want the earlier of its two jobs", r101.QueuedAt)
	}
}

// An installation's rows go with it, silently. The controller announces each as
// gone before the delete, so it needs the list first, and it must be that
// installation's alone: announcing another's would blank a page for nothing.
func TestAnInstallationsRepositoriesAreListedByTheirIDsAndNobodyElses(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	other := &Installation{AppID: 1, InstallationID: 3, Target: "other", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, other); err != nil {
		t.Fatal(err)
	}
	// Enough of them that insertion order is not id order by luck.
	var want []string
	for i := range int64(8) {
		want = append(want, touch(t, s, inst, i+1, fmt.Sprintf("acme/r%d", i), "public").ID)
	}
	slices.Sort(want)
	foreign := touch(t, s, other, 100, "other/c", "public")

	ids, err := s.KennelRepositoryIDs(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v, in order, and not %s", ids, want, foreign.ID)
	}
	if none, err := s.KennelRepositoryIDs(ctx, "inst_nope"); err != nil || len(none) != 0 {
		t.Errorf("an unknown installation listed %v, %v", none, err)
	}
}

// The coverage panel counts a repository once for each source it has a state
// for. A repository nothing has evaluated has no states, so it is in none of
// them, and a stored document with no state for a source does not count as one.
func TestCoverageIsCountedPerSourceAndPerState(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	if empty, err := s.KennelCoverageCounts(ctx); err != nil || len(empty) != 0 {
		t.Fatalf("empty = %v, %v", empty, err)
	}
	a := touch(t, s, inst, 1, "acme/a", "public")
	b := touch(t, s, inst, 2, "acme/b", "public")
	c := touch(t, s, inst, 3, "acme/c", "private")
	touch(t, s, inst, 4, "acme/never-evaluated", "public")
	for repo, doc := range map[string]string{
		a.ID: `{"fleet":{"state":"ok"},"metadata":{"state":"ok"},"runs":{"state":"ok"}}`,
		b.ID: `{"fleet":{"state":"ok"},"metadata":{"state":"ok"},"runs":{"state":"denied"}}`,
		c.ID: `{"fleet":{"state":"ok"},"metadata":{"state":"ok"},"runs":{}}`,
	} {
		rec := record("attention", 0, 1, 0)
		rec.Coverage = json.RawMessage(doc)
		if err := s.SaveKennelEvaluation(ctx, repo, rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.KennelCoverageCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got["fleet"]["ok"] != 3 || got["metadata"]["ok"] != 3 || got["runs"]["ok"] != 1 || got["runs"]["denied"] != 1 {
		t.Errorf("counts = %v", got)
	}
	if len(got["runs"]) != 2 {
		t.Errorf("a source with no state was counted as one: %v", got["runs"])
	}
}

// A severity keeps the repositories with an open finding of exactly that
// severity, and one the store does not know keeps none: a typo in a filter must
// not read as "everything is fine" or as "everything is wrong".
func TestTheListCanBeNarrowedToRepositoriesWithAnOpenFindingOfASeverity(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	bad := touch(t, s, inst, 1, "acme/bad", "public")
	warned := touch(t, s, inst, 2, "acme/warned", "public")
	noted := touch(t, s, inst, 3, "acme/noted", "public")
	touch(t, s, inst, 4, "acme/new", "public")
	_ = s.SaveKennelEvaluation(ctx, bad.ID, record("attention", 1, 0, 0, "exposure.fork_code_ran"))
	_ = s.SaveKennelEvaluation(ctx, warned.ID, record("attention", 0, 2, 0, "capacity.unserved_label"))
	_ = s.SaveKennelEvaluation(ctx, noted.ID, record("best_in_show", 0, 0, 1))

	for severity, want := range map[string]string{"error": bad.ID, "warning": warned.ID, "info": noted.ID} {
		got, total, err := s.ListKennelRepositories(ctx, KennelFilter{Severity: severity}, Page{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(got) != 1 || got[0].ID != want {
			t.Errorf("%s: listed %d (total %d), want only %s", severity, len(got), total, want)
		}
	}
	for _, unknown := range []string{"critical", "Error", " error"} {
		if got, total, err := s.ListKennelRepositories(ctx, KennelFilter{Severity: unknown}, Page{Limit: 10}); err != nil || total != 0 || len(got) != 0 {
			t.Errorf("%q kept %d rows (total %d), %v: an unknown severity should keep none", unknown, len(got), total, err)
		}
	}
}

// The Overview's "Waived" card counts findings somebody decided are acceptable,
// and opens the repositories that hold them. The list has to be able to say so,
// or the card is a number with nothing behind it.
func TestTheListCanBeNarrowedToRepositoriesWithAWaivedFinding(t *testing.T) {
	ctx := context.Background()
	s, inst, _ := kennelStore(t)
	waived := touch(t, s, inst, 1, "acme/waived", "public")
	plain := touch(t, s, inst, 2, "acme/plain", "public")
	rec := record("best_in_show", 0, 0, 0)
	rec.Waived = 2
	_ = s.SaveKennelEvaluation(ctx, waived.ID, rec)
	_ = s.SaveKennelEvaluation(ctx, plain.ID, record("best_in_show", 0, 0, 0))

	yes, no := true, false
	for _, c := range []struct {
		name   string
		filter *bool
		want   string
	}{{"with", &yes, waived.ID}, {"without", &no, plain.ID}} {
		got, total, err := s.ListKennelRepositories(ctx, KennelFilter{Waived: c.filter}, Page{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(got) != 1 || got[0].ID != c.want {
			t.Errorf("%s a waiver: listed %d (total %d), want only %s", c.name, len(got), total, c.want)
		}
	}
}
