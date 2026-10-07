package controller

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

// An administrator may stop Kennel Club looking at a repository and an operator
// may only start it again, which is the whole of the rule: the controller is told
// what the caller holds and does not look it up.
var (
	trackingAdmin    = KennelActor{ID: "usr_ada", Name: "ada", CanUntrack: true}
	trackingOperator = KennelActor{ID: "usr_oscar", Name: "oscar"}
)

const sandboxReason = "a sandbox nobody keeps up, and its jobs are not ours to judge"

// stopTracking is an administrator saying Kennel Club is not to look at a
// repository, and returns the repository as the page would then show it.
func (f *kennelFixture) stopTracking(name string) KennelRepositoryView {
	f.t.Helper()
	v, _, changed, err := f.c.SetKennelTracking(f.ctx, f.row(name).ID, KennelTrackingInput{Reason: sandboxReason}, trackingAdmin)
	if err != nil || !changed {
		f.t.Fatalf("stopping tracking %s: changed %v, %v", name, changed, err)
	}
	return *v
}

func (f *kennelFixture) startTracking(name string) {
	f.t.Helper()
	if _, _, changed, err := f.c.SetKennelTracking(f.ctx, f.row(name).ID, KennelTrackingInput{Tracked: true}, trackingOperator); err != nil || !changed {
		f.t.Fatalf("starting tracking %s: changed %v, %v", name, changed, err)
	}
}

// ---------------------------------------------------------------------------
// The loop leaves what is not tracked alone
// ---------------------------------------------------------------------------

// A stopped row is put back to before anything evaluated it, which is a row that
// reads as due. If that were enough to make the pass list the installation from
// GitHub, a repository nobody is looking at would cost a request every minute
// for as long as it was untracked.
func TestARepositoryNobodyTracksNeverMakesAPassListTheInstallation(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/sandbox", "private")
	f.ran("acme/sandbox", 1, f.pool)
	f.pass()
	if n := f.requestsTo("/installation/repositories"); n != 1 {
		t.Fatalf("listed %d times in the first pass, want 1", n)
	}
	f.stopTracking("acme/sandbox")

	// It is still served, and its row is still there, so it is neither a repository
	// the pass has no row for nor one that is due.
	for range 3 {
		f.advance(time.Minute)
		f.pass()
	}
	if n := f.requestsTo("/installation/repositories"); n != 1 {
		t.Errorf("the installation was listed %d times with only an untracked repository to look at, want the first alone", n)
	}
}

// What is tracked beside it is read as it was. The untracked repository is not:
// no run is read for it, even a new one, and nothing is evaluated.
func TestAPassThatReadsOnePublicRepositoryDoesNotReadTheOneNobodyTracks(t *testing.T) {
	f := newKennelFixture(t)
	f.persistent()
	for i, name := range []string{"acme/sandbox", "acme/live"} {
		f.repo(name, "public")
		f.ran(name, int64(10+i), f.pool)
		f.trigger(name, int64(10+i), "push", false)
	}
	f.pass()
	f.stopTracking("acme/sandbox")
	liveBefore := f.view("acme/live").EvaluatedAt

	// A new run in each, and a day on, so both would be due.
	f.ran("acme/sandbox", 20, f.pool)
	f.trigger("acme/sandbox", 20, "push", false)
	f.ran("acme/live", 21, f.pool)
	f.trigger("acme/live", 21, "push", false)
	f.dueAgain()
	before := f.runReads()
	f.pass()

	if n := f.requestsTo("acme/sandbox/actions/runs/"); n != 1 {
		t.Errorf("%d reads of the sandbox's runs in all, want only the one made before it was untracked", n)
	}
	if got := f.runReads() - before; got != 1 {
		t.Errorf("%d runs read in the pass, want the one run of the repository that is tracked", got)
	}
	if live := f.view("acme/live"); live.EvaluatedAt == nil || liveBefore == nil || !live.EvaluatedAt.After(*liveBefore) {
		t.Errorf("the repository that is tracked was not evaluated again in the pass: was %v, now %v", liveBefore, live.EvaluatedAt)
	}
	if got := f.row("acme/sandbox"); got.EvaluatedAt != nil || got.State != "pending" {
		t.Errorf("the untracked repository was evaluated: state %s, at %v", got.State, got.EvaluatedAt)
	}
}

// ---------------------------------------------------------------------------
// Stopping and starting
// ---------------------------------------------------------------------------

func TestOnlyAnAdministratorMayStopKennelClubLookingAndTheReasonIsHeldToAWaiversRules(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/sandbox", "private")
	f.ran("acme/sandbox", 1, f.pool)
	f.pass()
	id := f.row("acme/sandbox").ID
	stop := func(reason string, by KennelActor) error {
		_, _, _, err := f.c.SetKennelTracking(f.ctx, id, KennelTrackingInput{Reason: reason}, by)
		return err
	}

	// The role is asked before the reason, so a person who may not do it is told so
	// and not how to ask better.
	if err := stop("", trackingOperator); !errors.Is(err, ErrKennelUntrackNeedsAdmin) {
		t.Errorf("an operator stopping tracking with no reason: %v, want the refusal about the role", err)
	}
	for name, reason := range map[string]string{
		"none":          "",
		"too short":     "a sandbox",
		"too long":      strings.Repeat("x", KennelMaxReason+1),
		"a bidi marker": "a sandbox nobody keeps \u202e up",
	} {
		var invalid *KennelInvalidError
		if err := stop(reason, trackingAdmin); !errors.As(err, &invalid) || len(invalid.Fields) != 1 || invalid.Fields[0].Field != "reason" {
			t.Errorf("%s: %v, want a refusal naming the reason", name, err)
		}
	}
	if err := stop(strings.Repeat("x", KennelMinReason), trackingAdmin); err != nil {
		t.Errorf("a reason of exactly the shortest length: %v", err)
	}
	if got := f.row("acme/sandbox"); got.Untracked == nil {
		t.Fatal("nothing stopped it")
	}
}

func TestStoppingTrackingNamesWhoWhenAndWhyAndPublishesTheFrameAFetchWouldReturn(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/sandbox", "private")
	f.ran("acme/sandbox", 1, f.pool)
	f.pass()
	sub := f.listen(events.KindKennelUpdated)

	v := f.stopTracking("acme/sandbox")

	// When is the moment it was recorded, which the store's clock says and not the
	// controller's, so it is read as near to now and not as equal to anything.
	if v.Tracking.Tracked || v.Tracking.Reason != sandboxReason || v.Tracking.By != "ada" ||
		v.Tracking.Since == nil || time.Since(*v.Tracking.Since) > time.Minute || time.Until(*v.Tracking.Since) > time.Minute {
		t.Errorf("tracking = %+v, want who, when and why", v.Tracking)
	}
	if v.State != kennel.StatePending || len(v.Findings) != 0 || v.EvaluatedAt != nil || v.NextDueAt != nil || len(v.Coverage) != 0 {
		t.Errorf("view = state %s, %d findings, evaluated %v, next due %v, %d coverage: nothing is evaluated for it and it is due for nothing",
			v.State, len(v.Findings), v.EvaluatedAt, v.NextDueAt, len(v.Coverage))
	}
	frame := nextOfKind(t, sub, events.KindKennelUpdated)
	if tr, _ := frame["tracking"].(map[string]any); tr == nil || tr["tracked"] != false || tr["by"] != "ada" {
		t.Errorf("the frame's tracking = %v, want the one a fetch returns", frame["tracking"])
	}
}

// A request to be in the state a repository is already in has nothing to say. It
// must not replace who made the decision, announce a change that did not happen,
// or be audited as one.
func TestAskingForTheStateARepositoryIsAlreadyInChangesAndAnnouncesNothing(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/sandbox", "private")
	f.ran("acme/sandbox", 1, f.pool)
	f.pass()
	id := f.row("acme/sandbox").ID

	if _, _, changed, err := f.c.SetKennelTracking(f.ctx, id, KennelTrackingInput{Tracked: true}, trackingOperator); err != nil || changed {
		t.Errorf("starting a repository that is tracked: changed %v, %v", changed, err)
	}
	f.stopTracking("acme/sandbox")
	sub := f.listen(events.KindKennelUpdated)
	v, was, changed, err := f.c.SetKennelTracking(f.ctx, id, KennelTrackingInput{Reason: "somebody else's reason entirely"},
		KennelActor{ID: "usr_bob", Name: "bob", CanUntrack: true})
	if err != nil || changed || was == nil {
		t.Fatalf("stopping it again: changed %v, was %v, %v", changed, was, err)
	}
	if v.Tracking.By != "ada" || v.Tracking.Reason != sandboxReason {
		t.Errorf("tracking = %+v, want the first decision kept", v.Tracking)
	}
	nothingFor(t, sub)
}

// Starting again makes the repository due at once and wakes the loop, so the next
// thing that happens to it is a read, and not a schedule from before it stopped.
func TestStartingTrackingMakesTheRepositoryDueAtOnceAndWakesTheLoop(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/sandbox", "private")
	f.ran("acme/sandbox", 1, f.pool)
	f.pass()
	select { // a flag, not a queue: whatever woke it before is not what is under test
	case <-f.c.kennel.wake:
	default:
	}
	f.stopTracking("acme/sandbox")
	// Stopping leaves nothing to read, so it has no reason to wake the loop.
	select {
	case <-f.c.kennel.wake:
		t.Error("stopping tracking woke the loop, though there is nothing to read for it")
	default:
	}

	sub := f.listen(events.KindKennelUpdated)
	_, was, changed, err := f.c.SetKennelTracking(f.ctx, f.row("acme/sandbox").ID, KennelTrackingInput{Tracked: true}, trackingOperator)
	if err != nil || !changed || was == nil || was.ByName != "ada" || was.Reason != sandboxReason {
		t.Fatalf("starting: changed %v, was %+v, %v; want the decision it ended", changed, was, err)
	}
	select {
	case <-f.c.kennel.wake:
	default:
		t.Error("the loop was not woken, so the repository waits out the minute before anybody reads it")
	}
	frame := nextOfKind(t, sub, events.KindKennelUpdated)
	if tr, _ := frame["tracking"].(map[string]any); tr == nil || tr["tracked"] != true {
		t.Errorf("the frame's tracking = %v", frame["tracking"])
	}
	if v := f.view("acme/sandbox"); v.NextDueAt != nil || !v.Tracking.Tracked {
		t.Errorf("view = %+v, want it tracked and due now", v)
	}

	// Starting a repository that is not stopped does not wake anything.
	select {
	case <-f.c.kennel.wake:
	default:
	}
	f.startTrackingNoChange("acme/sandbox")
	select {
	case <-f.c.kennel.wake:
		t.Error("a request that changed nothing woke the loop")
	default:
	}
}

func (f *kennelFixture) startTrackingNoChange(name string) {
	f.t.Helper()
	if _, _, changed, err := f.c.SetKennelTracking(f.ctx, f.row(name).ID, KennelTrackingInput{Tracked: true}, trackingOperator); err != nil || changed {
		f.t.Fatalf("starting %s again: changed %v, %v", name, changed, err)
	}
}

func TestTrackingAnUnknownRepositoryOrWithKennelClubOffIsRefused(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/sandbox", "private")
	f.ran("acme/sandbox", 1, f.pool)
	f.pass()
	if _, _, _, err := f.c.SetKennelTracking(f.ctx, "kcr_nope", KennelTrackingInput{Tracked: true}, trackingOperator); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown repository: %v, want not found", err)
	}
	id := f.row("acme/sandbox").ID
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.Enabled = false })
	if _, _, _, err := f.c.SetKennelTracking(f.ctx, id, KennelTrackingInput{Reason: sandboxReason}, trackingAdmin); !errors.Is(err, ErrKennelOff) {
		t.Errorf("with Kennel Club off: %v, want ErrKennelOff", err)
	}
	if f.row("acme/sandbox").Untracked != nil {
		t.Error("a request refused because Kennel Club is off stopped it")
	}
}

// ---------------------------------------------------------------------------
// What is counted, and what is said
// ---------------------------------------------------------------------------

// An untracked repository is a choice, so it is counted apart and nowhere else:
// not among the repositories, not as pending, and above all not as the error it
// had open when it was stopped.
func TestTheOverviewCountsWhatIsNotTrackedApartAndTheExposureProblemFollowsWhatIsTracked(t *testing.T) {
	f := newKennelFixture(t)
	exposeRepositories(f, "acme/exposed", "acme/also-exposed")
	sub := f.listen(events.KindKennelSummary)
	f.c.publishDerived(f.ctx)
	nextOfKind(t, sub, events.KindKennelSummary)

	o, _ := f.c.KennelOverview(f.ctx)
	if o.Repositories != 2 || o.NotTracked != 0 || o.States.Attention != 2 {
		t.Fatalf("overview = %d repositories, %d not tracked, %d attention", o.Repositories, o.NotTracked, o.States.Attention)
	}
	each := f.view("acme/also-exposed").Counts.Error
	if each == 0 || o.Counts.Error != 2*each {
		t.Fatalf("%d errors in all and %d in each: the fixture should give both repositories the same errors", o.Counts.Error, each)
	}

	f.stopTracking("acme/exposed")
	o, _ = f.c.KennelOverview(f.ctx)
	if o.Repositories != 1 || o.NotTracked != 1 || o.States.Attention != 1 || o.States.Pending != 0 {
		t.Errorf("overview = %d repositories, %d not tracked, %d attention, %d pending; want the stopped one counted apart and in nothing else",
			o.Repositories, o.NotTracked, o.States.Attention, o.States.Pending)
	}
	if o.Counts.Error != each {
		t.Errorf("%d errors, want the %d of the repository that is tracked and none of the one that was stopped", o.Counts.Error, each)
	}
	if len(o.Attention) != 1 || o.Attention[0].Name != "acme/also-exposed" {
		t.Errorf("attention = %+v, want only the repository that is tracked", o.Attention)
	}
	if p := kennelProblemsOf(t, f, "kennel.exposure"); len(p) != 1 || p[0].Title != "Kennel Club: 1 repository has an exposure error open" {
		t.Errorf("problems = %+v, want one for the repository still tracked", p)
	}
	// The summary frame is computed, so the change reaches an open page by itself.
	f.c.publishDerived(f.ctx)
	if frame := nextOfKind(t, sub, events.KindKennelSummary); frame["not_tracked"] != float64(1) || frame["repositories"] != float64(1) {
		t.Errorf("summary frame = %v", frame)
	}

	f.stopTracking("acme/also-exposed")
	if p := kennelProblemsOf(t, f, "kennel.exposure"); len(p) != 0 {
		t.Errorf("problems = %+v: nobody is looking at either repository, so nothing is exposed that Kennel Club knows of", p)
	}

	// Looking again raises it again, because the repositories are read afresh.
	f.startTracking("acme/exposed")
	f.startTracking("acme/also-exposed")
	f.pass()
	if p := kennelProblemsOf(t, f, "kennel.exposure"); len(p) != 1 || p[0].Title != "Kennel Club: 2 repositories have an exposure error open" {
		t.Errorf("problems = %+v after tracking both again and a pass", p)
	}
	if o, _ = f.c.KennelOverview(f.ctx); o.Repositories != 2 || o.NotTracked != 0 {
		t.Errorf("overview = %d repositories, %d not tracked after tracking both again", o.Repositories, o.NotTracked)
	}
}

// A waiver is a decision about a finding, and stopping is not the finding going
// away. They wait, do nothing, and cover it again the pass after it is tracked.
func TestWaiversWaitWhileARepositoryIsNotTrackedAndCoverItAgainWhenItIs(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/widgets", "public")
	f.ran("acme/widgets", 11, f.pool)
	f.trigger("acme/widgets", 11, "push", false)
	f.pass()
	id := f.row("acme/widgets").ID
	w := &store.KennelWaiver{
		RepositoryPK: id, Code: "exposure.public_repo_on_fleet", Severity: "warning",
		Reason: "an open-source project whose runners are throwaway virtual machines", CreatedBy: "usr_1", CreatedByName: "Ada",
		ExpiresAt: f.c.Now().Add(48 * time.Hour),
	}
	if err := f.st.UpsertKennelWaiver(f.ctx, w); err != nil {
		t.Fatal(err)
	}
	f.pass()
	if v := f.view("acme/widgets"); len(v.Waived) != 1 {
		t.Fatalf("waived = %d, want the waiver to cover the finding first", len(v.Waived))
	}

	f.stopTracking("acme/widgets")
	f.advance(time.Hour)
	f.pass()
	if got, err := f.st.ListKennelWaivers(f.ctx, id); err != nil || len(got) != 1 {
		t.Fatalf("waivers = %v, %v: they are kept while it is untracked, and not retired as if the finding had gone", got, err)
	}
	if v := f.view("acme/widgets"); len(v.Waived) != 0 || len(v.Findings) != 0 || v.Counts.Waived != 0 {
		t.Errorf("waived %d, findings %d: a waiver does nothing while nothing is evaluated", len(v.Waived), len(v.Findings))
	}

	f.startTracking("acme/widgets")
	f.pass()
	v := f.view("acme/widgets")
	if len(v.Waived) != 1 || v.Waived[0].Waiver.ID != w.ID || len(v.Findings) != 0 {
		t.Errorf("waived %d, findings %v after tracking again, want the same waiver covering the finding", len(v.Waived), findingCodes(v))
	}
}

// ---------------------------------------------------------------------------
// What needs findings is refused for a repository that has none
// ---------------------------------------------------------------------------

func TestARepositoryNobodyTracksCannotBeRecheckedOrHaveWaiversMadeOrEnded(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/widgets", "public")
	f.ran("acme/widgets", 11, f.pool)
	f.trigger("acme/widgets", 11, "push", false)
	f.pass()
	id := f.row("acme/widgets").ID
	w := &store.KennelWaiver{
		RepositoryPK: id, Code: "exposure.public_repo_on_fleet", Severity: "warning",
		Reason: "an open-source project whose runners are throwaway virtual machines", CreatedBy: "usr_1", CreatedByName: "Ada",
		ExpiresAt: f.c.Now().Add(48 * time.Hour),
	}
	if err := f.st.UpsertKennelWaiver(f.ctx, w); err != nil {
		t.Fatal(err)
	}
	f.stopTracking("acme/widgets")
	select {
	case <-f.c.kennel.wake:
	default:
	}
	dueBefore := f.row("acme/widgets").NextDueAt

	if _, err := f.c.RecheckKennelRepository(f.ctx, id); !errors.Is(err, ErrKennelNotTracked) {
		t.Errorf("recheck: %v, want it refused: there is nothing of it to read", err)
	}
	select {
	case <-f.c.kennel.wake:
		t.Error("a refused recheck woke the loop")
	default:
	}
	if got := f.row("acme/widgets").NextDueAt; !got.Equal(dueBefore) {
		t.Errorf("a refused recheck made it due: %v, was %v", got, dueBefore)
	}
	in := KennelWaiverInput{Code: "exposure.public_repo_on_fleet", Reason: "an open-source project whose runners are throwaway", ExpiresAt: f.c.Now().Add(time.Hour)}
	if _, _, err := f.c.WaiveKennelFinding(f.ctx, id, in, KennelActor{ID: "usr_1", Name: "Ada"}); !errors.Is(err, ErrKennelNotTracked) {
		t.Errorf("waive: %v, want it refused: it has no findings to be acceptable", err)
	}
	if _, _, err := f.c.UnwaiveKennelFinding(f.ctx, id, w.ID); !errors.Is(err, ErrKennelNotTracked) {
		t.Errorf("unwaive: %v, want it refused", err)
	}
	if _, err := f.st.GetKennelWaiver(f.ctx, w.ID); err != nil {
		t.Errorf("a refused unwaive ended the waiver anyway: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The list
// ---------------------------------------------------------------------------

func TestTheListCanBeNarrowedToWhatIsTrackedAndNothingStopsAnUntrackedRepositoryBeingListed(t *testing.T) {
	f := newKennelFixture(t)
	for i, name := range []string{"acme/a", "acme/sandbox", "acme/c"} {
		f.repo(name, "private")
		f.ran(name, int64(1+i), f.pool)
	}
	f.pass()
	f.stopTracking("acme/sandbox")

	yes, no := true, false
	names := func(tracked *bool) []string {
		rows, total, err := f.c.KennelRepositories(f.ctx, KennelListFilter{Tracked: tracked}, store.Page{Limit: 50})
		if err != nil || total != len(rows) {
			t.Fatalf("list: %d of %d, %v", len(rows), total, err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.Name)
		}
		return out
	}
	if got := names(nil); len(got) != 3 {
		t.Errorf("left alone, the list = %v, want every repository, the untracked one included", got)
	}
	if got := names(&yes); len(got) != 2 || slicesContains(got, "acme/sandbox") {
		t.Errorf("tracked = %v", got)
	}
	if got := names(&no); len(got) != 1 || got[0] != "acme/sandbox" {
		t.Errorf("not tracked = %v", got)
	}
}
