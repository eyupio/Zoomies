package controller

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

var (
	admin    = KennelActor{ID: "usr_admin", Name: "Ada", CanWaiveErrors: true}
	operator = KennelActor{ID: "usr_op", Name: "Olu"}
)

// exposedFixture is a public repository on a persistent pool, which has two
// error findings, and a second private one with none.
func exposedFixture(t *testing.T) *kennelFixture {
	t.Helper()
	f := newKennelFixture(t)
	f.persistent()
	f.repo("acme/exposed", "public")
	f.ran("acme/exposed", 11, f.pool)
	f.trigger("acme/exposed", 11, "push", false)
	f.repo("acme/quiet", "private")
	f.ran("acme/quiet", 12, f.pool)
	f.pass()
	return f
}

func okWaiver(f *kennelFixture, code string) KennelWaiverInput {
	return KennelWaiverInput{Code: code, Reason: "an open-source project whose runners are rebuilt for every job", ExpiresAt: f.c.Now().Add(30 * 24 * time.Hour)}
}

func fieldsOf(err error) []string {
	var inv *KennelInvalidError
	if !errors.As(err, &inv) {
		return nil
	}
	var out []string
	for _, f := range inv.Fields {
		out = append(out, f.Field)
	}
	return out
}

// ---------------------------------------------------------------------------
// Off, and the catalogue
// ---------------------------------------------------------------------------

// Everything that needs Kennel Club's rows says so when it is off, rather than
// answering with an empty list that reads as a fleet with nothing wrong.
func TestEveryOperationOnTheRowsRefusesWhileKennelClubIsOff(t *testing.T) {
	f := exposedFixture(t)
	id := f.row("acme/exposed").ID
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.Enabled = false })

	if _, _, err := f.c.KennelRepositories(f.ctx, KennelListFilter{}, store.Page{Limit: 10}); !errors.Is(err, ErrKennelOff) {
		t.Errorf("list: %v", err)
	}
	if _, err := f.c.KennelRepository(f.ctx, id); !errors.Is(err, ErrKennelOff) {
		t.Errorf("get: %v", err)
	}
	// Refusing is not enough: nothing is to have been written on the way to it,
	// and the later read that would refuse in any case is not what is being held.
	due := f.row("acme/exposed").NextDueAt
	select {
	case <-f.c.kennel.wake:
	default:
	}
	if _, err := f.c.RecheckKennelRepository(f.ctx, id); !errors.Is(err, ErrKennelOff) {
		t.Errorf("recheck: %v", err)
	}
	if got := f.row("acme/exposed").NextDueAt; !got.Equal(due) {
		t.Errorf("a recheck while off made the repository due: %s then %s", due, got)
	}
	select {
	case <-f.c.kennel.wake:
		t.Error("a recheck while off woke the loop")
	default:
	}
	if _, _, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_on_fleet"), admin); !errors.Is(err, ErrKennelOff) {
		t.Errorf("waive: %v", err)
	}
	if ws, _ := f.st.ListKennelWaivers(f.ctx, id); len(ws) != 0 {
		t.Errorf("a waiver was written while Kennel Club was off: %+v", ws)
	}
	if _, _, err := f.c.UnwaiveKennelFinding(f.ctx, id, "kcw_x"); !errors.Is(err, ErrKennelOff) {
		t.Errorf("unwaive: %v", err)
	}
	// The catalogue is what Kennel Club checks, which the Off page explains; it
	// needs no row.
	if len(f.c.KennelChecks()) == 0 {
		t.Error("the catalogue is empty while Kennel Club is off")
	}
}

func TestTheCatalogueIsTheRegistryWithThePermissionEachCheckNeedsAndWhatIsTurnedOff(t *testing.T) {
	f := newKennelFixture(t)
	f.c.UpdateConfig(func(c *config.Config) { c.Kennel.DisabledChecks = []string{"capacity", "exposure.fork_code_ran"} })
	got := f.c.KennelChecks()
	reg := kennel.Checks()
	if len(got) != len(reg) {
		t.Fatalf("%d entries for %d checks", len(got), len(reg))
	}
	for i, ck := range reg {
		g := got[i]
		if g.Code != ck.Code || g.Area != ck.Area || g.Severity != ck.Severity || g.Detects != ck.Detects || len(g.Needs) != len(ck.Needs)+len(ck.Conditional) {
			t.Errorf("entry %d = %+v, want the registry's %+v", i, g, ck)
		}
		// Both opt-in switches are off in the fixture, so a check that reads
		// either gated source is off too, whatever its area.
		reads := func(src kennel.Source) bool {
			return slices.Contains(ck.Needs, src) || slices.Contains(ck.Conditional, src)
		}
		wantOff := ck.Area == kennel.AreaCapacity || ck.Code == kennel.CodeForkCodeRan || ck.Area == kennel.AreaSetup || ck.Area == kennel.AreaCI || ck.Area == kennel.AreaToken || reads(kennel.SourceWorkflows) || reads(kennel.SourceSetup) || reads(kennel.SourceGuidance) ||
			// Nothing reads settings or required checks yet, so the checks that need
			// them are off whatever is switched on.
			reads(kennel.SourceSettings) || reads(kennel.SourceProtection)
		if g.Disabled != wantOff {
			t.Errorf("%s disabled = %v, want %v", ck.Code, g.Disabled, wantOff)
		}
	}
	for _, g := range got {
		if g.Code == kennel.CodeForkCodeRan {
			var sawRuns bool
			for _, n := range g.Needs {
				if n.Source == kennel.SourceRuns && strings.Contains(n.Permission, "Actions: Read-only") {
					sawRuns = true
				}
				if n.Source == kennel.SourceFleet && n.Permission != "" {
					t.Errorf("the fleet's own record names a permission: %+v", n)
				}
				if n.Source == kennel.SourceMetadata && !strings.Contains(n.Permission, "Metadata: Read-only") {
					t.Errorf("the repository's own details do not name their permission: %+v", n)
				}
				if n.Conditional != (n.Source == kennel.SourceRuns || n.Source == kennel.SourceSetup) {
					t.Errorf("%+v: only the run history is read for some repositories and not others", n)
				}
			}
			if !sawRuns {
				t.Errorf("%s does not say it needs the Actions permission: %+v", g.Code, g.Needs)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

func TestTheListIsFilteredAndPagedAndSaysWhatIsWrongWithAFilter(t *testing.T) {
	f := exposedFixture(t)
	list := func(filter KennelListFilter, page store.Page) ([]KennelRepositoryView, int, error) {
		return f.c.KennelRepositories(f.ctx, filter, page)
	}
	all, total, err := list(KennelListFilter{}, store.Page{Limit: 10})
	if err != nil || total != 2 || len(all) != 2 || all[0].Name != "acme/exposed" {
		t.Fatalf("list = %d (total %d), %v; want both, the exposed one first", len(all), total, err)
	}
	for name, tc := range map[string]struct {
		filter KennelListFilter
		want   string
	}{
		"by name":         {KennelListFilter{Q: "QUIET"}, "acme/quiet"},
		"by severity":     {KennelListFilter{Severity: "error"}, "acme/exposed"},
		"by code":         {KennelListFilter{Code: "exposure.public_repo_weak_pool"}, "acme/exposed"},
		"by state":        {KennelListFilter{State: "best_in_show"}, "acme/quiet"},
		"by installation": {KennelListFilter{InstallationID: f.inst.ID, Q: "exposed"}, "acme/exposed"},
	} {
		got, total, err := list(tc.filter, store.Page{Limit: 10})
		if err != nil || total != 1 || len(got) != 1 || got[0].Name != tc.want {
			t.Errorf("%s: %d (total %d), %v; want only %s", name, len(got), total, err, tc.want)
		}
	}
	if _, total, _ := list(KennelListFilter{InstallationID: "ins_elsewhere"}, store.Page{Limit: 10}); total != 0 {
		t.Errorf("another installation's filter matched %d", total)
	}
	page, total, _ := list(KennelListFilter{}, store.Page{Limit: 1, Offset: 1})
	if total != 2 || len(page) != 1 || page[0].Name != "acme/quiet" {
		t.Errorf("second page = %d (total %d)", len(page), total)
	}

	for field, filter := range map[string]KennelListFilter{
		"severity": {Severity: "critical"}, "code": {Code: "exposure.nonsense"}, "state": {State: "fine"},
	} {
		if _, _, err := list(filter, store.Page{Limit: 10}); len(fieldsOf(err)) != 1 || fieldsOf(err)[0] != field {
			t.Errorf("a bad %s gave %v, want a refusal naming it", field, err)
		}
	}
}

func TestOneRepositoryIsReadByItsIDAndAnUnknownOneIsNotFound(t *testing.T) {
	f := exposedFixture(t)
	row := f.row("acme/exposed")
	v, err := f.c.KennelRepository(f.ctx, row.ID)
	if err != nil || v.ID != row.ID || v.Name != "acme/exposed" || v.Counts.Error != 2 {
		t.Fatalf("got %+v, %v", v, err)
	}
	if _, err := f.c.KennelRepository(f.ctx, "kcr_nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown id: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Recheck
// ---------------------------------------------------------------------------

// A recheck makes the repository due and wakes the loop; it reads nothing, so it
// waits on the same budget as everything else, and it can be asked for only so
// often.
func TestARecheckMakesARepositoryDueAndCanOnlyBeAskedForEveryFiveMinutes(t *testing.T) {
	f := exposedFixture(t)
	id := f.row("acme/exposed").ID
	select {
	case <-f.c.kennel.wake:
	default:
	}
	reads := len(f.gh.Requests())

	v, err := f.c.RecheckKennelRepository(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if v.NextDueAt != nil {
		t.Errorf("next due %v: a recheck makes the repository due at once", v.NextDueAt)
	}
	select {
	case <-f.c.kennel.wake:
	default:
		t.Error("a recheck did not wake the loop")
	}
	if len(f.gh.Requests()) != reads {
		t.Error("a recheck read from GitHub itself, outside the loop's budget")
	}

	_, err = f.c.RecheckKennelRepository(f.ctx, id)
	var cool *KennelCooldownError
	if !errors.As(err, &cool) {
		t.Fatalf("a second recheck at once: %v", err)
	}
	if want := f.c.Now().Add(KennelRecheckCooldown); cool.Until.After(want) || cool.Until.Before(want.Add(-time.Minute)) {
		t.Errorf("allowed again at %s, want about %s", cool.Until, want)
	}

	f.advance(KennelRecheckCooldown + time.Second)
	if _, err := f.c.RecheckKennelRepository(f.ctx, id); err != nil {
		t.Errorf("a recheck after the cooldown: %v", err)
	}
	if _, err := f.c.RecheckKennelRepository(f.ctx, "kcr_nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown repository: %v", err)
	}
	// One repository's cooldown is not another's.
	other := f.row("acme/quiet").ID
	if _, err := f.c.RecheckKennelRepository(f.ctx, other); err != nil {
		t.Errorf("another repository was held to the first one's cooldown: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Waiving
// ---------------------------------------------------------------------------

func TestAWaiverRecordsTheFindingsSeverityAndWhoMadeItAndTheAnswerAlreadySaysSo(t *testing.T) {
	f := exposedFixture(t)
	id := f.row("acme/exposed").ID
	v, w, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_weak_pool"), admin)
	if err != nil {
		t.Fatal(err)
	}
	if w.Severity != "error" || w.CreatedBy != "usr_admin" || w.CreatedByName != "Ada" || !strings.HasPrefix(w.ID, "kcw_") {
		t.Errorf("waiver = %+v", w)
	}
	if len(v.Waived) != 1 || v.Counts.Waived != 1 || v.Counts.Error != 1 {
		t.Errorf("view = %+v: the repository should already say it is waived, with one error left", v.Counts)
	}
	// The same decision again renews it and keeps its id, so a link to it holds.
	_, again, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_weak_pool"), admin)
	if err != nil {
		t.Fatalf("renewing a decision already made: %v", err)
	}
	if again.ID != w.ID {
		t.Errorf("renewal gave id %s, want the %s it had", again.ID, w.ID)
	}
}

// An error is a stranger running code on the fleet, and the decision that this is
// acceptable is the senior role's. A warning is the operator's.
func TestAnOperatorMayWaiveAWarningAndOnlyAnAdminMayWaiveAnError(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/widgets", "public")
	f.ran("acme/widgets", 11, f.pool)
	f.trigger("acme/widgets", 11, "push", false)
	f.pass()
	id := f.row("acme/widgets").ID
	if v := f.view("acme/widgets"); v.Counts.Warning != 1 || v.Counts.Error != 0 {
		t.Fatalf("counts = %+v, want a warning for the test to mean anything", v.Counts)
	}
	if _, _, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_on_fleet"), operator); err != nil {
		t.Errorf("an operator waiving a warning: %v", err)
	}

	g := exposedFixture(t)
	gid := g.row("acme/exposed").ID
	_, _, err := g.c.WaiveKennelFinding(g.ctx, gid, okWaiver(g, "exposure.public_repo_weak_pool"), operator)
	var needs *KennelNeedsAdminError
	if !errors.As(err, &needs) || needs.Code != kennel.CodePublicRepoWeakPool {
		t.Errorf("an operator waiving an error: %v", err)
	}
	if ws, _ := g.st.ListKennelWaivers(g.ctx, gid); len(ws) != 0 {
		t.Errorf("a refused waiver was stored: %+v", ws)
	}
}

func TestAWaiverIsRefusedForEveryWayItCouldBeWrong(t *testing.T) {
	f := exposedFixture(t)
	id := f.row("acme/exposed").ID
	now := f.c.Now()
	long := func(n int) string { return strings.Repeat("a", n) }
	for _, tc := range []struct {
		name  string
		edit  func(*KennelWaiverInput)
		field string // empty: allowed
	}{
		{"a code that is not a check", func(w *KennelWaiverInput) { w.Code = "exposure.nonsense" }, "code"},
		{"no code", func(w *KennelWaiverInput) { w.Code = "" }, "code"},
		{"a check with nothing open", func(w *KennelWaiverInput) { w.Code = "exposure.fork_code_ran" }, "code"},
		{"a subject no finding has", func(w *KennelWaiverInput) { w.Subject = "somewhere else" }, "code"},
		{"a subject too long", func(w *KennelWaiverInput) { w.Subject = long(201) }, "subject"},
		{"a reason one short", func(w *KennelWaiverInput) { w.Reason = long(kennelMinReasonForTest - 1) }, "reason"},
		{"a reason exactly long enough", func(w *KennelWaiverInput) { w.Reason = long(KennelMinReason) }, ""},
		{"a reason exactly as long as allowed", func(w *KennelWaiverInput) { w.Reason = long(KennelMaxReason) }, ""},
		{"a reason one too long", func(w *KennelWaiverInput) { w.Reason = long(KennelMaxReason + 1) }, "reason"},
		{"a reason of spaces", func(w *KennelWaiverInput) { w.Reason = strings.Repeat(" ", 40) }, "reason"},
		{"a reason with a control character", func(w *KennelWaiverInput) { w.Reason = "this is acceptable\x07 here today" }, "reason"},
		{"a reason that reverses the text", func(w *KennelWaiverInput) { w.Reason = "this is acceptable \u202e here today" }, "reason"},
		{"a reason with a line break", func(w *KennelWaiverInput) { w.Reason = "this is acceptable:\nthe hosts are rebuilt" }, ""},
		{"a reason counted in characters, not bytes", func(w *KennelWaiverInput) { w.Reason = strings.Repeat("é", KennelMinReason) }, ""},
		{"no end", func(w *KennelWaiverInput) { w.ExpiresAt = time.Time{} }, "expires_at"},
		{"an end that has passed", func(w *KennelWaiverInput) { w.ExpiresAt = now.Add(-time.Hour) }, "expires_at"},
		{"an end a day past a year", func(w *KennelWaiverInput) { w.ExpiresAt = now.AddDate(0, 0, KennelMaxWaiverDays+1) }, "expires_at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := okWaiver(f, "exposure.public_repo_on_fleet")
			tc.edit(&in)
			_, _, err := f.c.WaiveKennelFinding(f.ctx, id, in, admin)
			switch {
			case tc.field == "" && err != nil:
				t.Errorf("refused a waiver that is allowed: %v", err)
			case tc.field != "":
				if got := fieldsOf(err); len(got) == 0 || got[0] != tc.field {
					t.Errorf("error = %v, want a refusal of %s", err, tc.field)
				}
			}
		})
	}
	if _, _, err := f.c.WaiveKennelFinding(f.ctx, "kcr_nope", okWaiver(f, "exposure.public_repo_on_fleet"), admin); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown repository: %v", err)
	}
}

const kennelMinReasonForTest = 10

// Everything wrong with one request is said at once, so a form does not send a
// person round it three times.
func TestEveryFieldThatIsWrongIsNamedInOneAnswer(t *testing.T) {
	f := exposedFixture(t)
	_, _, err := f.c.WaiveKennelFinding(f.ctx, f.row("acme/exposed").ID,
		KennelWaiverInput{Code: "nonsense", Reason: "short"}, admin)
	got := fieldsOf(err)
	if len(got) != 3 || got[0] != "code" || got[1] != "reason" || got[2] != "expires_at" {
		t.Errorf("fields = %v, want code, reason and expires_at together", got)
	}
}

// A decision is about a finding that exists. A waiver made ahead of one is a
// standing exception nobody has looked at.
func TestAFindingThatIsNotThereCannotBeWaivedInAdvance(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/widgets", "private")
	f.ran("acme/widgets", 1, f.pool)
	f.pass()
	id := f.row("acme/widgets").ID
	_, _, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.fork_code_ran"), admin)
	if got := fieldsOf(err); len(got) != 1 || got[0] != "code" || !strings.Contains(err.Error(), "nothing to waive") {
		t.Errorf("error = %v", err)
	}
}

// The limit is a backstop: a waiver for a finding nobody reports is retired by
// the next evaluation, so a repository only reaches it by carrying many decisions
// about findings that exist. The count is read before the evaluation, and a renewal
// of a decision already made is never a new one.
func TestARepositoryCanHoldOnlyAsManyWaiversAsItMayAndRenewalIsNotANewOne(t *testing.T) {
	f := exposedFixture(t)
	id := f.row("acme/exposed").ID
	if _, _, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_weak_pool"), admin); err != nil {
		t.Fatal(err)
	}
	// Forty-nine more, straight into the store, with no evaluation in between.
	for i := range KennelMaxWaivers - 1 {
		if err := f.st.UpsertKennelWaiver(f.ctx, &store.KennelWaiver{
			RepositoryPK: id, Code: "capacity.unserved_label", Subject: fmt.Sprintf("s%02d", i), Severity: "warning",
			Reason: "a decision made earlier", CreatedBy: "usr_1", CreatedByName: "Ada", ExpiresAt: f.c.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := f.st.CountKennelWaivers(f.ctx, id); n != KennelMaxWaivers {
		t.Fatalf("%d waivers held, want exactly the limit of %d for the test to mean anything", n, KennelMaxWaivers)
	}
	if _, _, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_on_fleet"), admin); !errors.Is(err, ErrKennelWaiverLimit) {
		t.Errorf("one more than may be held: %v", err)
	}
	if _, _, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_weak_pool"), admin); err != nil {
		t.Errorf("renewing at the limit: %v", err)
	}
	// And that evaluation retired the forty-nine that were about nothing.
	if n, _ := f.st.CountKennelWaivers(f.ctx, id); n != 1 {
		t.Errorf("%d waivers held after the evaluation, want only the one about a real finding", n)
	}
}

// ---------------------------------------------------------------------------
// Unwaiving
// ---------------------------------------------------------------------------

func TestEndingAWaiverBringsTheFindingBackAndReturnsWhatItEnded(t *testing.T) {
	f := exposedFixture(t)
	id := f.row("acme/exposed").ID
	_, w, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_weak_pool"), admin)
	if err != nil {
		t.Fatal(err)
	}
	v, ended, err := f.c.UnwaiveKennelFinding(f.ctx, id, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ended.ID != w.ID || ended.Reason != w.Reason {
		t.Errorf("ended %+v, want the waiver that was there", ended)
	}
	if v.Counts.Error != 2 || len(v.Waived) != 0 {
		t.Errorf("counts = %+v: the finding should be back", v.Counts)
	}
	if _, _, err := f.c.UnwaiveKennelFinding(f.ctx, id, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ending it again: %v", err)
	}
}

// A waiver is found by its own id, and it must be that repository's: naming the
// wrong repository is a 404, not a way to end a decision about another.
func TestAWaiverCannotBeEndedThroughAnotherRepository(t *testing.T) {
	f := exposedFixture(t)
	exposed, quiet := f.row("acme/exposed").ID, f.row("acme/quiet").ID
	_, w, err := f.c.WaiveKennelFinding(f.ctx, exposed, okWaiver(f, "exposure.public_repo_weak_pool"), admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.c.UnwaiveKennelFinding(f.ctx, quiet, w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ending it through the wrong repository: %v", err)
	}
	if _, err := f.st.GetKennelWaiver(f.ctx, w.ID); err != nil {
		t.Errorf("the waiver was ended by a request for another repository: %v", err)
	}
}

// A decision with a reason and an owner does not vanish when the finding it was
// about does: the audit log says whose it was.
func TestARetiredWaiverLeavesAnAuditRowSayingWhoseItWasAndWhy(t *testing.T) {
	f := newKennelFixture(t)
	f.repo("acme/widgets", "private")
	f.ran("acme/widgets", 11, f.pool)
	f.pass()
	row := f.row("acme/widgets")
	w := &store.KennelWaiver{
		RepositoryPK: row.ID, Code: "exposure.fork_code_ran", Severity: "error", Reason: "a decision about something that went away",
		CreatedBy: "usr_1", CreatedByName: "Ada", ExpiresAt: f.c.Now().Add(24 * time.Hour),
	}
	if err := f.st.UpsertKennelWaiver(f.ctx, w); err != nil {
		t.Fatal(err)
	}
	f.pass()

	rows, _, err := f.st.ListAudit(f.ctx, store.AuditFilter{Actions: []string{"kennel.waiver_retired"}}, store.Page{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows = %d, %v; want one", len(rows), err)
	}
	if rows[0].TargetID != row.ID || !strings.Contains(rows[0].Before, "a decision about something that went away") || !strings.Contains(rows[0].Before, "Ada") {
		t.Errorf("audit row = %+v: it should say whose decision it was and what it said", rows[0])
	}
}

// Everybody looking at the repository is told it is waiting to be read again, not
// only the person who pressed the button.
func TestARecheckAndAWaiverAreAnnouncedToEveryoneLooking(t *testing.T) {
	f := exposedFixture(t)
	id := f.row("acme/exposed").ID
	sub := f.listen(events.KindKennelUpdated)

	if _, err := f.c.RecheckKennelRepository(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	got := nextOfKind(t, sub, events.KindKennelUpdated)
	if got["id"] != id || got["next_due_at"] != nil {
		t.Errorf("recheck frame = %v, want this repository with its reads due now", got)
	}

	if _, _, err := f.c.WaiveKennelFinding(f.ctx, id, okWaiver(f, "exposure.public_repo_weak_pool"), admin); err != nil {
		t.Fatal(err)
	}
	got = nextOfKind(t, sub, events.KindKennelUpdated)
	waived, _ := got["waived"].([]any)
	if got["id"] != id || len(waived) != 1 {
		t.Errorf("waiver frame = %v, want this repository with one waived finding", got)
	}
}

// The rules of a waiver request, at the exact instant: the shortest reason, the
// longest, the last moment a waiver may run to. The clock is the test's own, so
// "exactly now" and "exactly a year" are real cases and not whichever way the
// seconds fell.
func TestTheWaiverRulesHoldAtTheirExactEdges(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	good := KennelWaiverInput{
		Code: "exposure.public_repo_on_fleet", Reason: strings.Repeat("a", KennelMinReason), ExpiresAt: now.Add(time.Nanosecond),
	}
	for _, tc := range []struct {
		name string
		edit func(*KennelWaiverInput)
		want string // the field, or "" for allowed
		says string // a word the sentence must contain
	}{
		{"as short a reason as is allowed", func(*KennelWaiverInput) {}, "", ""},
		{"a reason one character short", func(w *KennelWaiverInput) { w.Reason = strings.Repeat("a", KennelMinReason-1) }, "reason", "10 to 500"},
		{"a reason as long as is allowed", func(w *KennelWaiverInput) { w.Reason = strings.Repeat("a", KennelMaxReason) }, "", ""},
		{"a reason one character too long", func(w *KennelWaiverInput) { w.Reason = strings.Repeat("a", KennelMaxReason+1) }, "reason", "10 to 500"},
		{"a reason counted in characters", func(w *KennelWaiverInput) { w.Reason = strings.Repeat("é", KennelMinReason) }, "", ""},
		{"a reason of that many bytes but fewer characters", func(w *KennelWaiverInput) { w.Reason = strings.Repeat("é", KennelMinReason-1) }, "reason", "10 to 500"},
		{"no end", func(w *KennelWaiverInput) { w.ExpiresAt = time.Time{} }, "expires_at", "required"},
		{"an end exactly now", func(w *KennelWaiverInput) { w.ExpiresAt = now }, "expires_at", "future"},
		{"an end an instant ago", func(w *KennelWaiverInput) { w.ExpiresAt = now.Add(-time.Nanosecond) }, "expires_at", "future"},
		{"an end an instant from now", func(w *KennelWaiverInput) { w.ExpiresAt = now.Add(time.Nanosecond) }, "", ""},
		{"an end exactly a year out", func(w *KennelWaiverInput) { w.ExpiresAt = now.AddDate(0, 0, KennelMaxWaiverDays) }, "", ""},
		{"an end an instant past a year", func(w *KennelWaiverInput) { w.ExpiresAt = now.AddDate(0, 0, KennelMaxWaiverDays).Add(time.Nanosecond) }, "expires_at", "365"},
		{"a subject as long as is allowed", func(w *KennelWaiverInput) { w.Subject = strings.Repeat("s", kennelMaxSubject) }, "", ""},
		{"a subject one byte too long", func(w *KennelWaiverInput) { w.Subject = strings.Repeat("s", kennelMaxSubject+1) }, "subject", "200"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := good
			tc.edit(&in)
			bad := checkKennelWaiverInput(in, now)
			if tc.want == "" {
				if len(bad) != 0 {
					t.Errorf("refused: %+v", bad)
				}
				return
			}
			if len(bad) != 1 || bad[0].Field != tc.want || !strings.Contains(bad[0].Message, tc.says) {
				t.Errorf("answer = %+v, want one refusal of %s saying %q", bad, tc.want, tc.says)
			}
		})
	}
}
