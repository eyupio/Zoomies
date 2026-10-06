package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// autopilotFleet is remedyFleet with the switch on, and a way to say a proposal
// has stood for as long as the controller waits for: the steady wait is the part a
// test cannot sit through, so it is aged by hand and everything else is real.
func autopilotFleet(t *testing.T, on bool) (*harness, *store.Host, string) {
	t.Helper()
	h, host := remedyFleet(t)
	h.cfg.Security.AutoApplyRemedies = map[bool]string{true: "on", false: "off"}[on]
	viewer := h.token("reader", store.RoleViewer)
	id, _ := h.remedyFor(viewer, "host.slots_below_capacity", host.ID)
	if id == "" {
		t.Fatal("the fixture must carry a remedy")
	}
	return h, host, id
}

func (h *harness) ageProposal(id string) {
	h.api.autopilot.mu.Lock()
	defer h.api.autopilot.mu.Unlock()
	h.api.autopilot.seen[id] = time.Now().Add(-3 * autopilotSteady)
}

func (h *harness) autoApplied() []autoAppliedChange {
	h.t.Helper()
	r := h.do(request{method: http.MethodGet, path: "/api/v1/problems/auto-applied", token: h.token("lister", store.RoleViewer)})
	if r.status != http.StatusOK {
		h.t.Fatalf("GET /problems/auto-applied = %d: %s", r.status, r.body)
	}
	var out struct {
		Items []autoAppliedChange `json:"items"`
	}
	if err := json.Unmarshal(r.body, &out); err != nil {
		h.t.Fatal(err)
	}
	return out.Items
}

// Off, the controller only proposes: a proposal can stand as long as it likes.
func TestTheControllerMakesNoChangeWhileAutomaticApplyIsOff(t *testing.T) {
	h, host, id := autopilotFleet(t, false)
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.api.autopilot.mu.Lock()
	h.api.autopilot.seen = map[string]time.Time{id: time.Now().Add(-3 * autopilotSteady)}
	h.api.autopilot.mu.Unlock()
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs != 6 {
		t.Errorf("the host was changed with the switch off: %+v", got.RunnerProfile)
	}
}

// The first pass only notes a proposal; one that has not stood long enough is left
// alone, and the change is then made as the route makes it, recorded with what it
// replaced.
func TestAProposalIsAppliedOnlyOnceItHasStoodAndIsRecordedWithWhatItReplaced(t *testing.T) {
	h, host, id := autopilotFleet(t, true)
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs != 6 {
		t.Fatal("a proposal seen for the first time was applied")
	}

	h.ageProposal(id)
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := h.st.GetHost(h.ctx, host.ID)
	if after.RunnerProfile.Standard.CPUs >= 6 {
		t.Fatalf("the steady proposal was not applied: %+v", after.RunnerProfile)
	}
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{auditAutoApplied}}, store.Page{Limit: 5})
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows = %v, %v; want one", rows, err)
	}
	if rows[0].ActorKind != "system" || rows[0].TargetID != host.ID || !strings.Contains(rows[0].Before, `"cpus":6`) {
		t.Errorf("the row must say who, where and what it replaced: %+v", rows[0])
	}
	// The route's own row is there too: this went through the apply route, not
	// around it.
	if rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{"problem.remedy_applied"}}, store.Page{Limit: 5}); len(rows) != 1 {
		t.Errorf("the apply route's audit rows = %d; want 1", len(rows))
	}
	items := h.autoApplied()
	if len(items) != 1 || items[0].Undone || !items[0].Undoable || items[0].TargetID != host.ID {
		t.Errorf("list = %+v", items)
	}
}

// Undo puts back what was there, as the caller; and an undone proposal is never
// made again, which is the one answer an administrator gave.
func TestUndoPutsBackWhatTheChangeReplacedAndIsNotMadeAgain(t *testing.T) {
	h, host, id := autopilotFleet(t, true)
	h.api.autopilotPass(h.ctx)
	h.ageProposal(id)
	h.api.autopilotPass(h.ctx)
	items := h.autoApplied()
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	path := "/api/v1/problems/auto-applied/" + items[0].ID + "/undo"

	viewer := h.token("reader2", store.RoleViewer)
	if r := h.do(request{method: http.MethodPost, path: path, token: viewer}); r.status != http.StatusForbidden {
		t.Errorf("a viewer undoing = %d %s; want 403", r.status, r.body)
	}
	operator := h.token("actor", store.RoleOperator)
	if r := h.do(request{method: http.MethodPost, path: path, token: operator}); r.status != http.StatusOK {
		t.Fatalf("undoing = %d: %s", r.status, r.body)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs != 6 {
		t.Errorf("the host was not put back: %+v", got.RunnerProfile)
	}
	if r := h.do(request{method: http.MethodPost, path: path, token: operator}); r.status != http.StatusConflict {
		t.Errorf("a second undo = %d; want 409", r.status)
	}
	if items := h.autoApplied(); !items[0].Undone || items[0].Undoable {
		t.Errorf("list after undo = %+v", items)
	}

	// The proposal is back, because the problem is: it is the same change, and it
	// stands as long as you like. It is not made again.
	newID, _ := h.remedyFor(h.token("reader3", store.RoleViewer), "host.slots_below_capacity", host.ID)
	if newID != id {
		t.Fatalf("the fixture should propose the same change again: %q vs %q", newID, id)
	}
	h.api.autopilotPass(h.ctx)
	h.ageProposal(id)
	h.api.autopilotPass(h.ctx)
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs != 6 {
		t.Errorf("an undone proposal was applied again: %+v", got.RunnerProfile)
	}
}

// Putting the old value back over somebody's edit would undo the edit.
func TestUndoIsRefusedOverAnEditMadeSince(t *testing.T) {
	h, host, id := autopilotFleet(t, true)
	h.api.autopilotPass(h.ctx)
	h.ageProposal(id)
	h.api.autopilotPass(h.ctx)
	items := h.autoApplied()
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	edited := store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 1, MemoryMB: 1024}}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{RunnerProfile: &edited}); err != nil {
		t.Fatal(err)
	}
	r := h.do(request{method: http.MethodPost, path: "/api/v1/problems/auto-applied/" + items[0].ID + "/undo", token: h.token("actor", store.RoleOperator)})
	if r.status != http.StatusConflict || !strings.Contains(string(r.body), "edited") {
		t.Errorf("undo over an edit = %d %s; want 409 naming the edit", r.status, r.body)
	}
	if h.autoApplied()[0].Undoable {
		t.Error("an edited target must not be offered an undo")
	}
}

// A pool or host is changed at most once a day: a second proposal for the same
// target waits.
func TestATargetIsChangedAutomaticallyAtMostOncePerCooldown(t *testing.T) {
	h, host, id := autopilotFleet(t, true)
	h.api.autopilotPass(h.ctx)
	h.ageProposal(id)
	h.api.autopilotPass(h.ctx)
	// A new, different proposal for the same host, as if the fleet had moved on.
	h.api.autopilot.mu.Lock()
	h.api.autopilot.seen["rem_other"] = time.Now().Add(-3 * autopilotSteady)
	h.api.autopilot.mu.Unlock()
	other := &controller.Problem{Remedy: &controller.Remedy{ID: "rem_other", TargetID: host.ID}}
	ok, err := h.api.mayAutoApply(h.ctx, other, time.Now())
	if err != nil || ok {
		t.Errorf("mayAutoApply within the cooldown = %v, %v; want false", ok, err)
	}
	ok, err = h.api.mayAutoApply(h.ctx, other, time.Now().Add(autopilotCooldown+time.Hour))
	if err != nil || !ok {
		t.Errorf("mayAutoApply after the cooldown = %v, %v; want true", ok, err)
	}
}

func TestAutomaticApplyIsShadowByDefaultAndNeverChangesAnythingThere(t *testing.T) {
	if mode := config.Default().Security.AutoApplyMode(); mode != "shadow" {
		t.Errorf("automatic apply defaults to %q; it must only record what it would do until an administrator turns it on", mode)
	}
	// The older spellings are still read, as on and off.
	for in, want := range map[string]string{"true": "on", "false": "off", "on": "on", "off": "off", "shadow": "shadow", "": "shadow", "bogus": "shadow"} {
		if got := (config.Security{AutoApplyRemedies: in}).AutoApplyMode(); got != want {
			t.Errorf("AutoApplyMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// A person who puts an automatic change back by hand has said no as plainly as one
// who clicked Undo: the same proposal returning after the cooldown is not made again.
func TestAProposalAlreadyMadeIsNotMadeAgainAfterTheCooldownEvenIfRevertedByHand(t *testing.T) {
	h, host, id := autopilotFleet(t, true)
	h.api.autopilotPass(h.ctx)
	h.ageProposal(id)
	h.api.autopilotPass(h.ctx)
	original := store.RunnerProfile{Standard: store.RunnerStandard{CPUs: 6, MemoryMB: 6656}}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{RunnerProfile: &original}); err != nil {
		t.Fatal(err)
	}
	same := &controller.Problem{Remedy: &controller.Remedy{ID: id, TargetID: host.ID}}
	ok, err := h.api.mayAutoApply(h.ctx, same, time.Now().Add(autopilotCooldown+time.Hour))
	if err != nil || ok {
		t.Errorf("mayAutoApply for a proposal already made = %v, %v; want false", ok, err)
	}
}

// Shadow mode does the whole of the work except the change: a proposal that has stood is
// recorded as one it would have made, once, and the target is left exactly as it was. The
// record is not an applied change, so the cooldown and the undo list do not see it and
// turning the setting on afterwards still makes the change.
func TestShadowModeRecordsWhatItWouldHaveDoneOnceAndChangesNothing(t *testing.T) {
	h, host, id := autopilotFleet(t, true)
	h.cfg.Security.AutoApplyRemedies = "shadow"
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.ageProposal(id)
	for i := 0; i < 3; i++ {
		if err := h.api.autopilotPass(h.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs != 6 {
		t.Fatalf("shadow mode changed the host: %+v", got.RunnerProfile)
	}
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{auditAutoShadowed}}, store.Page{Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("shadow rows after three passes = %d, %v; want one for the one proposal", len(rows), err)
	}
	if applied, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{auditAutoApplied, "problem.remedy_applied"}}, store.Page{Limit: 10}); len(applied) != 0 {
		t.Errorf("shadow mode wrote an applied row: %+v", applied)
	}
	if len(h.autoApplied()) != 0 {
		t.Error("a shadowed change is listed as applied")
	}

	// Turned on, the same proposal is made: shadow did not use it up.
	h.cfg.Security.AutoApplyRemedies = "on"
	h.ageProposal(id)
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetHost(h.ctx, host.ID); got.RunnerProfile.Standard.CPUs >= 6 {
		t.Errorf("turning it on after shadow did not make the change: %+v", got.RunnerProfile)
	}
}

// sharePool is a docker-in-docker pool whose sidecar memory share autopilot has just
// raised from 50% to 80%, recorded as an applied change, so the revert has something to
// look at without sitting through the evidence a real proposal needs.
func sharePool(t *testing.T) (*harness, *store.Pool, string) {
	t.Helper()
	h := newHarness(t)
	pool := h.pool(h.installation(), "builders")
	pool.DockerMode = store.DockerDinD
	r := &controller.Remedy{Kind: controller.RemedyPoolUpdate, TargetID: pool.ID}
	before, err := h.api.targetSnapshot(h.ctx, r, []string{"resources"})
	if err != nil {
		t.Fatal(err)
	}
	pool.Resources.DaemonMemorySharePercent = 80
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	applied, err := h.api.targetSnapshot(h.ctx, r, []string{"resources"})
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.Security.AutoApplyRemedies = "on"
	h.api.auth.Auditor().Record(h.ctx, autopilotIdentity, auditAutoApplied, "pool", pool.ID, before,
		map[string]any{"code": "pool.daemon_share_suggested", "remedy": "rem_share", "label": "Give the sidecar 80% of the memory", "kind": controller.RemedyPoolUpdate, "applied": applied})
	return h, pool, "rem_share"
}

func (h *harness) killForMemory(pool *store.Pool, n int64) {
	h.t.Helper()
	started := time.Now().Add(time.Minute)
	runner := "run_killed"
	if _, _, err := h.st.ApplyJob(h.ctx, &store.Job{GitHubJobID: n, Repo: "acme/widgets", Workflow: "ci", JobName: "build",
		Labels: store.StringSlice{"self-hosted"}, State: store.JobInProgress, PoolID: pool.ID, Matched: true,
		RunnerID: runner, QueuedAt: started, StartedAt: &started}); err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.RecordJobUsage(h.ctx, runner, 1, 3000); err != nil {
		h.t.Fatal(err)
	}
	done := started.Add(time.Minute)
	if _, _, err := h.st.ApplyJob(h.ctx, &store.Job{GitHubJobID: n, Repo: "acme/widgets", State: store.JobCompleted,
		Conclusion: "failure", StartedAt: &started, CompletedAt: &done}); err != nil {
		h.t.Fatal(err)
	}
	if _, _, err := h.st.MarkJobOOMKilled(h.ctx, runner, "killed for memory"); err != nil {
		h.t.Fatal(err)
	}
}

// Autopilot sized the share by what jobs used, and a kill for memory is the proof it
// was wrong: the share is put back to what it replaced, as an undo would, and the
// proposal is not made again.
func TestAMemoryShareIsTakenBackWhenAJobInThePoolIsKilledAfterIt(t *testing.T) {
	h, pool, remedy := sharePool(t)
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetPool(h.ctx, pool.ID); got.Resources.DaemonMemorySharePercent != 80 {
		t.Fatalf("a share nothing has killed was taken back: %+v", got.Resources)
	}
	h.killForMemory(pool, 9001)
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetPool(h.ctx, pool.ID); got.Resources.DaemonMemorySharePercent != 0 {
		t.Fatalf("the share was not taken back after a memory kill: %+v", got.Resources)
	}
	items := h.autoApplied()
	if len(items) != 1 || !items[0].Undone || items[0].Undoable {
		t.Errorf("list = %+v; want the change shown as undone", items)
	}
	rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{auditAutoUndone}}, store.Page{Limit: 5})
	if len(rows) != 1 || !strings.Contains(rows[0].After, remedy) || !strings.Contains(rows[0].After, "killed for memory") {
		t.Errorf("audit rows = %+v; want one saying why", rows)
	}
	// Nothing is left to do, and a second pass does not do it again.
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{auditAutoUndone}}, store.Page{Limit: 5}); len(rows) != 1 {
		t.Errorf("the change was taken back %d times", len(rows))
	}
}

// A pool somebody has edited since is theirs: putting the old value back would undo
// their work, so the change is only recorded as superseded.
func TestAMemoryShareIsNotTakenBackOverAnEditMadeSince(t *testing.T) {
	h, pool, _ := sharePool(t)
	pool, _ = h.st.GetPool(h.ctx, pool.ID)
	pool.Resources.DaemonMemorySharePercent = 90
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.killForMemory(pool, 9002)
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetPool(h.ctx, pool.ID); got.Resources.DaemonMemorySharePercent != 90 {
		t.Errorf("an operator's edit was overwritten: %+v", got.Resources)
	}
	rows, _, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{auditAutoUndone}}, store.Page{Limit: 5})
	if len(rows) != 1 || !strings.Contains(rows[0].After, "superseded") {
		t.Errorf("audit rows = %+v; want one marking the change superseded", rows)
	}
}

// Shadow mode makes no change, so it takes none back either.
func TestShadowModeTakesNothingBack(t *testing.T) {
	h, pool, _ := sharePool(t)
	h.cfg.Security.AutoApplyRemedies = "shadow"
	h.killForMemory(pool, 9003)
	if err := h.api.autopilotPass(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetPool(h.ctx, pool.ID); got.Resources.DaemonMemorySharePercent != 80 {
		t.Errorf("shadow mode changed a pool: %+v", got.Resources)
	}
}
