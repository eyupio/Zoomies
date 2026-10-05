package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// sizeOn turns both switches on, with no hold, so that a test is about what the
// routes do and not about waiting.
func sizeOn(cfg *config.Config) {
	cfg.Scheduler.SizeRouting = scheduler.SizeOn
	cfg.Scheduler.AutoPools = scheduler.SizeOn
	cfg.Scheduler.SizeClassHold = 0
}

// keptMediumPool is a fleet with its automatic pool made: one medium host and
// the pool the controller keeps for it.
func (h *harness) keptMediumPool() (*store.Installation, *store.Host, *store.Pool) {
	h.t.Helper()
	inst := h.installation()
	host := h.measuredHost("build-1")
	if err := h.ctrl.ReconcileAutoPools(h.ctx); err != nil {
		h.t.Fatalf("ReconcileAutoPools: %v", err)
	}
	pool, err := h.st.GetPoolByName(h.ctx, "zoomies-medium")
	if err != nil {
		h.t.Fatalf("the controller made no pool for a medium host: %v", err)
	}
	return inst, host, pool
}

// ---------------------------------------------------------------------------
// The state of it
// ---------------------------------------------------------------------------

func TestTheAutoPoolsRouteSaysWhatTheControllerKeepsAndWhatEachClassIs(t *testing.T) {
	h := newHarness(t, sizeOn)
	h.keptMediumPool()
	_, viewer := h.user("viewer", store.RoleViewer)

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/auto-pools", cookie: viewer})
	resp.mustStatus(t, http.StatusOK, "reading the automatic pools as a viewer")
	var view controller.AutoPoolsView
	resp.into(t, &view)
	if view.SizeRouting != "on" || view.AutoPools != "on" || view.Installation != "acme" || len(view.Pools) != 1 ||
		view.Pools[0].Name != "zoomies-medium" || view.Pools[0].Slots != 4 || len(view.Classes) != 3 {
		t.Fatalf("the view is %+v", view)
	}

	// Nothing has been asked for on a fleet that has not turned it on.
	plain := newHarness(t)
	_, viewer = plain.user("viewer", store.RoleViewer)
	resp = plain.do(request{method: http.MethodGet, path: "/api/v1/auto-pools", cookie: viewer})
	resp.mustStatus(t, http.StatusOK, "reading the automatic pools with the feature off")
	resp.into(t, &view)
	if view.SizeRouting != "off" || view.AutoPools != "off" || len(view.Pools) != 0 {
		t.Fatalf("a fleet with the feature off reports %+v", view)
	}
	if !strings.Contains(string(resp.body), `"pools":[]`) || !strings.Contains(string(resp.body), `"findings":[]`) {
		t.Fatalf("the lists are null, not empty: %s", resp.body)
	}
}

func TestAHostPoolAndJobCarryTheSizeFields(t *testing.T) {
	h := newHarness(t, sizeOn)
	_, host, pool := h.keptMediumPool()
	_, viewer := h.user("viewer", store.RoleViewer)

	var hostView controller.HostView
	h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, cookie: viewer}).into(t, &hostView)
	if hostView.SizeClass == nil || hostView.SizeClass.Class != store.SizeMedium || hostView.AutoPool == nil ||
		hostView.AutoPool.Pool != "zoomies-medium" || len(hostView.Tags) == 0 {
		t.Fatalf("the host view is %+v / %+v / %+v", hostView.SizeClass, hostView.AutoPool, hostView.Tags)
	}

	var poolView controller.PoolView
	h.do(request{method: http.MethodGet, path: "/api/v1/pools/" + pool.ID, cookie: viewer}).into(t, &poolView)
	if poolView.Auto == nil || poolView.Auto.Key != "amd64/medium" || poolView.Auto.Slots != 4 || len(poolView.Auto.Hosts) != 1 {
		t.Fatalf("the pool view is %+v", poolView.Auto)
	}

	job := h.job(pool, store.JobQueued)
	if _, _, err := h.st.StampJobClass(h.ctx, job.ID, store.JobClassing{Class: store.SizeLarge, Reason: "an operator pinned it", Basis: store.SizeBasisPin, Route: true}); err != nil {
		t.Fatal(err)
	}
	var jobView controller.JobView
	h.do(request{method: http.MethodGet, path: "/api/v1/jobs/" + job.ID, cookie: viewer}).into(t, &jobView)
	if jobView.SizeClass != store.SizeLarge || jobView.SizeBasis != store.SizeBasisPin || jobView.RoutedClass != store.SizeLarge {
		t.Fatalf("the job view is %+v", jobView)
	}
}

// ---------------------------------------------------------------------------
// Pins
// ---------------------------------------------------------------------------

func TestAnOperatorCanPinJobsToAClassAndItReachesTheOnesAlreadyWaiting(t *testing.T) {
	h := newHarness(t, sizeOn)
	_, _, pool := h.keptMediumPool()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	_, viewer := h.user("viewer", store.RoleViewer)

	job := h.job(pool, store.JobQueued)
	if _, _, err := h.st.StampJobClass(h.ctx, job.ID, store.JobClassing{Class: store.SizeMedium, Reason: "it has no measured runs yet", Basis: store.SizeBasisDefault, Route: true}); err != nil {
		t.Fatal(err)
	}

	put := h.do(request{method: http.MethodPut, path: "/api/v1/size-pins", cookie: cookie,
		body: map[string]any{"repo": "Acme/Widgets", "class": "large"}})
	put.mustStatus(t, http.StatusOK, "pinning a repository")
	var result struct {
		Pin          store.SizePin `json:"pin"`
		Reclassified int           `json:"reclassified"`
	}
	put.into(t, &result)
	if result.Pin.Repo != "acme/widgets" || result.Pin.Class != store.SizeLarge || result.Pin.CreatedBy != "operator" || result.Reclassified != 1 {
		t.Fatalf("the result is %+v", result)
	}
	if got, _ := h.st.GetJob(h.ctx, job.ID); got.SizeClass != store.SizeLarge || got.SizeBasis != store.SizeBasisPin || got.RoutedClass != store.SizeLarge {
		t.Fatalf("the waiting job is %+v; the pin should have reached it", got)
	}

	var list struct {
		Items []store.SizePin `json:"items"`
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/size-pins", cookie: viewer}).into(t, &list)
	if len(list.Items) != 1 || list.Items[0].Repo != "acme/widgets" {
		t.Fatalf("the pins are %+v", list.Items)
	}

	// A job's own pin is a different key from its repository's.
	h.do(request{method: http.MethodPut, path: "/api/v1/size-pins", cookie: cookie,
		body: map[string]any{"repo": "acme/widgets", "workflow": "ci", "job_name": "build", "class": "small"}}).
		mustStatus(t, http.StatusOK, "pinning one job")
	if got, _ := h.st.GetJob(h.ctx, job.ID); got.SizeClass != store.SizeSmall {
		t.Fatalf("the job's own pin did not win over its repository's: %+v", got)
	}

	del := h.do(request{method: http.MethodDelete, path: "/api/v1/size-pins?" + url.Values{"repo": {"acme/widgets"}, "workflow": {"ci"}, "job_name": {"build"}}.Encode(), cookie: cookie})
	del.mustStatus(t, http.StatusOK, "removing a job's pin")
	if got, _ := h.st.GetJob(h.ctx, job.ID); got.SizeClass != store.SizeLarge {
		t.Fatalf("with the job's pin gone the repository's should apply again: %+v", got)
	}
	h.do(request{method: http.MethodDelete, path: "/api/v1/size-pins?repo=acme/widgets", cookie: cookie}).mustStatus(t, http.StatusOK, "removing the repository's pin")
	if got, _ := h.st.GetJob(h.ctx, job.ID); got.SizeBasis != store.SizeBasisDefault {
		t.Fatalf("with every pin gone the job should be classed by the default: %+v", got)
	}
	h.do(request{method: http.MethodDelete, path: "/api/v1/size-pins?repo=acme/widgets", cookie: cookie}).mustStatus(t, http.StatusNotFound, "removing a pin that is not there")

	for _, action := range []string{"size_pin.set", "size_pin.delete"} {
		rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{action}}, store.Page{Limit: 10})
		if err != nil || len(rows) == 0 {
			t.Errorf("no audit row for %s: %v", action, err)
		}
	}
}

func TestAPinThatIsNotOneIsRefusedWithTheFieldAndAViewerCannotWriteOne(t *testing.T) {
	h := newHarness(t, sizeOn)
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	_, viewer := h.user("viewer", store.RoleViewer)

	for _, tc := range []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"no repository", map[string]any{"class": "large"}, "repo"},
		{"a repository that is not owner/name", map[string]any{"repo": "widgets", "class": "large"}, "repo"},
		{"a class that is not one", map[string]any{"repo": "acme/widgets", "class": "huge"}, "class"},
		{"a workflow without a job", map[string]any{"repo": "acme/widgets", "workflow": "ci", "class": "large"}, "job_name"},
		{"a repository that is only a slash", map[string]any{"repo": "/", "class": "large"}, "repo"},
		{"a repository with no name", map[string]any{"repo": "acme/", "class": "large"}, "repo"},
		{"a workflow with a space at its end", map[string]any{"repo": "acme/widgets", "workflow": "ci ", "job_name": "build", "class": "large"}, "workflow"},
		{"a job name that is far too long", map[string]any{"repo": "acme/widgets", "workflow": "ci", "job_name": strings.Repeat("b", 300), "class": "large"}, "job_name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(request{method: http.MethodPut, path: "/api/v1/size-pins", cookie: cookie, body: tc.body})
			resp.mustStatus(t, http.StatusUnprocessableEntity, tc.name)
			if !strings.Contains(string(resp.body), `"field":"`+tc.field+`"`) {
				t.Fatalf("the refusal does not name %s: %s", tc.field, resp.body)
			}
		})
	}
	h.do(request{method: http.MethodPut, path: "/api/v1/size-pins", cookie: viewer, body: map[string]any{"repo": "acme/widgets", "class": "large"}}).
		mustStatus(t, http.StatusForbidden, "a viewer pinning a job")
	h.do(request{method: http.MethodDelete, path: "/api/v1/size-pins?repo=acme/widgets", cookie: viewer}).
		mustStatus(t, http.StatusForbidden, "a viewer removing a pin")
}

// ---------------------------------------------------------------------------
// Label advice
// ---------------------------------------------------------------------------

func (h *harness) keptClass(job string, class store.SizeClass, labels ...string) {
	h.t.Helper()
	at := time.Now().Add(-time.Hour)
	done := at.Add(5 * time.Minute)
	runner := "run_advice_" + job
	base := store.Job{GitHubJobID: time.Now().UnixNano(), Repo: "acme/widgets", Workflow: "CI", JobName: job, Labels: labels, QueuedAt: at}
	running := base
	running.State, running.RunnerID, running.StartedAt = store.JobInProgress, runner, &at
	if _, _, err := h.st.ApplyJob(h.ctx, &running); err != nil {
		h.t.Fatal(err)
	}
	if err := h.st.RecordJobUsage(h.ctx, runner, 1, 3000); err != nil {
		h.t.Fatal(err)
	}
	finished := base
	finished.State, finished.Conclusion, finished.StartedAt, finished.CompletedAt = store.JobCompleted, "success", &at, &done
	if _, _, err := h.st.ApplyJob(h.ctx, &finished); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.st.PutJobClass(h.ctx, &store.JobClass{Repo: "acme/widgets", Workflow: "CI", JobName: job, Class: class,
		Basis: store.SizeBasisHistory, Reason: "its memory needs about 6.2 GB", Runs: 12}); err != nil {
		h.t.Fatal(err)
	}
}

func TestLabelAdviceIsPagedFilteredAndCounted(t *testing.T) {
	h := newHarness(t, sizeOn)
	h.keptClass("e2e", store.SizeLarge, "self-hosted", "zoomies-medium")
	h.keptClass("docs", store.SizeSmall, "self-hosted", "zoomies-large")
	h.keptClass("build", store.SizeLarge, "self-hosted", "zoomies")
	h.keptClass("fine", store.SizeLarge, "self-hosted", "zoomies-large")
	_, viewer := h.user("viewer", store.RoleViewer)

	type page struct {
		Items  []scheduler.Advice `json:"items"`
		Total  int                `json:"total"`
		Limit  int                `json:"limit"`
		Offset int                `json:"offset"`
		Counts map[string]int     `json:"counts"`
	}
	get := func(query string) page {
		t.Helper()
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/label-advice" + query, cookie: viewer})
		resp.mustStatus(t, http.StatusOK, "label advice "+query)
		var out page
		resp.into(t, &out)
		return out
	}

	all := get("")
	if all.Total != 3 || len(all.Items) != 3 || all.Items[0].JobName != "e2e" || all.Items[0].Kind != scheduler.AdviceTooSmall ||
		all.Items[1].Kind != scheduler.AdviceUnguaranteed || all.Items[2].Kind != scheduler.AdviceTooLarge {
		t.Fatalf("the advice is %+v", all)
	}
	if all.Counts[scheduler.AdviceTooSmall] != 1 || all.Counts[scheduler.AdviceUnguaranteed] != 1 || all.Counts[scheduler.AdviceTooLarge] != 1 {
		t.Fatalf("the counts are %v", all.Counts)
	}

	second := get("?limit=1&offset=1")
	if len(second.Items) != 1 || second.Items[0].Kind != scheduler.AdviceUnguaranteed || second.Total != 3 || second.Limit != 1 || second.Offset != 1 {
		t.Fatalf("the second page is %+v", second)
	}
	past := get("?limit=5&offset=10")
	if len(past.Items) != 0 || past.Total != 3 {
		t.Fatalf("a page past the end is %+v", past)
	}
	small := get("?kind=too_small")
	if small.Total != 1 || small.Items[0].JobName != "e2e" || small.Counts[scheduler.AdviceTooLarge] != 1 {
		t.Fatalf("filtering by kind: %+v; the counts are for everything, whatever was asked for", small)
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/label-advice?kind=nonsense", cookie: viewer}).
		mustStatus(t, http.StatusBadRequest, "an unknown kind")

	// Not a thing to say while nothing is being classed.
	h.ctrl.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.SizeRouting = scheduler.SizeOff })
	if off := get(""); off.Total != 0 || len(off.Items) != 0 {
		t.Fatalf("with size routing off the advice is %+v", off)
	}
}

// ---------------------------------------------------------------------------
// Pools the controller keeps
// ---------------------------------------------------------------------------

func TestOnlyWhatIsAnOperatorsToChangeCanBeChangedOnAPoolTheControllerKeeps(t *testing.T) {
	h := newHarness(t, sizeOn)
	_, _, pool := h.keptMediumPool()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	path := "/api/v1/pools/" + pool.ID

	patch := func(body map[string]any) *response {
		return h.do(request{method: http.MethodPatch, path: path, cookie: cookie, body: body})
	}
	resp := patch(map[string]any{"idle_timeout": "7m", "auto": map[string]any{"warm": 1, "cap": 3}})
	resp.mustStatus(t, http.StatusOK, "the settings that are the operator's")
	var view controller.PoolView
	resp.into(t, &view)
	if view.Auto == nil || view.Auto.Warm != 1 || view.Auto.Cap != 3 || view.MaxRunners != 3 || view.MinRunners != 1 || view.IdleTimeout.Duration() != 7*time.Minute {
		t.Fatalf("the pool after the edit: max %d min %d idle %s auto %+v; the cap should already be in force",
			view.MaxRunners, view.MinRunners, view.IdleTimeout.Duration(), view.Auto)
	}

	for _, tc := range []struct {
		name  string
		body  map[string]any
		field string
		say   string
	}{
		{"its name", map[string]any{"name": "mine"}, "name", "worked out and not typed"},
		{"its image", map[string]any{"image": "example.com/x:1"}, "image", "make a pool of your own"},
		{"its maximum", map[string]any{"max_runners": 9}, "max_runners", "auto.cap"},
		{"its minimum", map[string]any{"min_runners": 2}, "min_runners", "auto.warm"},
		{"its labels", map[string]any{"labels": []string{"x"}}, "labels", "worked out and not typed"},
		{"its host selector", map[string]any{"host_selector": map[string]string{"size": "large"}}, "host_selector", "worked out and not typed"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			resp := patch(tc.body)
			resp.mustStatus(t, http.StatusUnprocessableEntity, tc.name)
			if !strings.Contains(string(resp.body), `"field":"`+tc.field+`"`) || !strings.Contains(string(resp.body), tc.say) {
				t.Fatalf("the refusal does not name %s and say %q: %s", tc.field, tc.say, resp.body)
			}
		})
	}
	patch(map[string]any{"auto": map[string]any{"warm": -1}}).mustStatus(t, http.StatusUnprocessableEntity, "a negative warm count")
	patch(map[string]any{"auto": map[string]any{"cap": -1}}).mustStatus(t, http.StatusUnprocessableEntity, "a negative cap")
	patch(map[string]any{"enabled": false, "auto": map[string]any{"paused": false}}).mustStatus(t, http.StatusUnprocessableEntity, "enabled and paused together")
	if stored, _ := h.st.GetPool(h.ctx, pool.ID); stored.Name != "zoomies-medium" || stored.MaxRunners != 3 {
		t.Fatalf("a refused edit was written: %+v", stored)
	}

	// Enabled is read as the pause.
	off := patch(map[string]any{"enabled": false})
	off.mustStatus(t, http.StatusOK, "pausing with enabled false")
	off.into(t, &view)
	if !view.Auto.Paused || view.Enabled {
		t.Fatalf("a paused pool is %+v, enabled %t", view.Auto, view.Enabled)
	}
	on := h.do(request{method: http.MethodPost, path: path + "/enable", cookie: cookie})
	on.mustStatus(t, http.StatusOK, "enabling")
	on.into(t, &view)
	if view.Auto.Paused || !view.Enabled {
		t.Fatalf("a resumed pool is %+v, enabled %t", view.Auto, view.Enabled)
	}
	paused := h.do(request{method: http.MethodPost, path: path + "/disable", cookie: cookie})
	paused.mustStatus(t, http.StatusOK, "disabling")
	paused.into(t, &view)
	if !view.Auto.Paused || view.Enabled || view.MaxRunners != 3 {
		t.Fatalf("a pool put out of use by hand is %+v, enabled %t, maximum %d", view.Auto, view.Enabled, view.MaxRunners)
	}
}

// Every field a request can name is either one of the four that are an
// operator's or is refused by its own name, with a sentence that says something
// true about it. A field added to the request later is refused until somebody
// decides otherwise, and one that is not a pointer is neither a panic nor taken
// for left out.
func TestEveryFieldOfAPoolRequestIsEditableOrRefusedByNameOnAPoolTheControllerKeeps(t *testing.T) {
	typ := reflect.TypeOf(poolInput{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		in := poolInput{}
		v := reflect.ValueOf(&in).Elem().Field(i)
		switch v.Kind() {
		case reflect.Pointer:
			v.Set(reflect.New(field.Type.Elem()))
		case reflect.Map:
			v.Set(reflect.MakeMap(field.Type))
		case reflect.Slice:
			v.Set(reflect.MakeSlice(field.Type, 1, 1))
		default:
			t.Fatalf("poolInput.%s is a %s, which this test does not know how to name; teach it, and the check", field.Name, v.Kind())
		}
		errs := refusedForAutoPool(&in)
		if autoPoolEditable[name] {
			if len(errs) != 0 {
				t.Errorf("%s is an operator's to change and was refused: %+v", name, errs)
			}
			continue
		}
		if len(errs) != 1 || errs[0].Field != name || errs[0].Message == "" {
			t.Errorf("%s was not refused by its own name: %+v", name, errs)
			continue
		}
		worked := strings.Contains(errs[0].Message, "worked out")
		if worked != (autoPoolWorkedOut[name] || name == "min_runners" || name == "max_runners") {
			t.Errorf("%s: the refusal says %q, which does not match whether the controller works it out", name, errs[0].Message)
		}
	}
}

func TestAPoolAnOperatorMadeHasNoAutoSettingsAndNoneCanBeCreated(t *testing.T) {
	h := newHarness(t, sizeOn)
	inst, _, _ := h.keptMediumPool()
	mine := h.pool(inst, "mine")
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	h.do(request{method: http.MethodPatch, path: "/api/v1/pools/" + mine.ID, cookie: cookie, body: map[string]any{"auto": map[string]any{"cap": 2}}}).
		mustStatus(t, http.StatusUnprocessableEntity, "auto settings on a pool somebody made")
	create := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: map[string]any{
		"name": "another", "installation_id": inst.ID, "labels": []string{"another"}, "auto": map[string]any{"cap": 2},
	}})
	create.mustStatus(t, http.StatusUnprocessableEntity, "creating an automatic pool")
	if !strings.Contains(string(create.body), "made by the controller") {
		t.Fatalf("the refusal does not say who makes them: %s", create.body)
	}
}

func TestAPoolTheControllerKeepsIsNotDeletedWhileItWouldBeMadeAgain(t *testing.T) {
	h := newHarness(t, sizeOn)
	_, _, pool := h.keptMediumPool()
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)

	refused := h.do(request{method: http.MethodDelete, path: "/api/v1/pools/" + pool.ID, cookie: cookie})
	refused.mustStatus(t, http.StatusConflict, "deleting a pool the controller keeps")
	for _, want := range []string{"made again on the next pass", "scheduler.auto_pools"} {
		if !strings.Contains(string(refused.body), want) {
			t.Errorf("the refusal does not say %q: %s", want, refused.body)
		}
	}
	if _, err := h.st.GetPool(h.ctx, pool.ID); err != nil {
		t.Fatalf("the pool was deleted anyway: %v", err)
	}

	// With the switch off it is a leftover, and goes like any other.
	h.ctrl.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPools = scheduler.SizeOff })
	h.do(request{method: http.MethodDelete, path: "/api/v1/pools/" + pool.ID, cookie: cookie}).mustStatus(t, http.StatusOK, "deleting a leftover")
	if _, err := h.st.GetPool(h.ctx, pool.ID); err == nil {
		t.Fatal("the leftover was not deleted")
	}
}

// Watching makes no pool and changes none, so a pool left from when the switch
// was on is not one the next pass would make again: deleting it is allowed, and
// the refusal that said otherwise sent an operator to turn the whole feature off
// to remove one pool.
func TestAPoolIsDeletedWhileTheControllerIsOnlyWatchingBecauseNothingWouldMakeItAgain(t *testing.T) {
	h := newHarness(t, sizeOn)
	_, _, pool := h.keptMediumPool()
	admin, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(admin)
	h.ctrl.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPools = scheduler.SizeShadow })

	h.do(request{method: http.MethodDelete, path: "/api/v1/pools/" + pool.ID, cookie: cookie}).mustStatus(t, http.StatusOK, "deleting a pool while only watching")
	if err := h.ctrl.ReconcileAutoPools(h.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetPoolByName(h.ctx, "zoomies-medium"); err == nil {
		t.Fatal("a pass that only watches made the pool again")
	}
}

// A pause is what takes an automatic pool out of use, and the reconciler is what
// does it. While it is not acting nothing would, and the pool would go on taking
// work for as long as it stayed "paused": so the pause takes effect when it is
// asked for, the end of it puts the pool back, and the pool says that it is not
// being kept and what that means for what was asked of it.
func TestAPauseOnAPoolTheControllerIsNotKeepingTakesEffectAtOnce(t *testing.T) {
	h := newHarness(t, sizeOn)
	_, _, pool := h.keptMediumPool()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	path := "/api/v1/pools/" + pool.ID

	var view controller.PoolView
	h.do(request{method: http.MethodGet, path: path, cookie: cookie}).into(t, &view)
	if view.Auto == nil || !view.Auto.Kept || !strings.HasPrefix(view.Auto.Summary, "Kept by the controller") {
		t.Fatalf("a pool the controller is keeping says %+v", view.Auto)
	}

	h.ctrl.UpdateConfig(func(cfg *config.Config) { cfg.Scheduler.AutoPools = scheduler.SizeShadow })
	if err := h.ctrl.ReconcileAutoPools(h.ctx); err != nil {
		t.Fatal(err)
	}
	paused := h.do(request{method: http.MethodPatch, path: path, cookie: cookie, body: map[string]any{"enabled": false, "auto": map[string]any{"cap": 2, "warm": 1}}})
	paused.mustStatus(t, http.StatusOK, "pausing while only watching")
	paused.into(t, &view)
	if view.Enabled || !view.Auto.Paused || view.Auto.Kept {
		t.Fatalf("a pool paused while only watching is enabled %t, paused %t, kept %t; the pause should have taken effect", view.Enabled, view.Auto.Paused, view.Auto.Kept)
	}
	for _, want := range []string{"not being kept now", "only reporting", "You capped it at 2 runners, which takes effect when", "You paused it"} {
		if !strings.Contains(view.Auto.Summary, want) {
			t.Errorf("the summary does not say %q: %s", want, view.Auto.Summary)
		}
	}
	if strings.Contains(view.Auto.Summary, "out of use") && strings.Contains(view.Auto.Summary, "No host") {
		t.Errorf("the summary blames the hosts for a pool nobody is looking at: %s", view.Auto.Summary)
	}
	if stored, _ := h.st.GetPool(h.ctx, pool.ID); stored.Enabled || stored.AutoCap != 2 || stored.MaxRunners != 4 {
		t.Fatalf("the stored pool is enabled %t, cap %d, maximum %d; want it out of use with the cap kept and the maximum as it was", stored.Enabled, stored.AutoCap, stored.MaxRunners)
	}

	resumed := h.do(request{method: http.MethodPost, path: path + "/enable", cookie: cookie})
	resumed.mustStatus(t, http.StatusOK, "resuming while only watching")
	resumed.into(t, &view)
	if !view.Enabled || view.Auto.Paused {
		t.Fatalf("a resumed pool is enabled %t, paused %t", view.Enabled, view.Auto.Paused)
	}

	// An edit that leaves the pause alone leaves the pool's own switch alone.
	h.do(request{method: http.MethodPost, path: path + "/disable", cookie: cookie}).mustStatus(t, http.StatusOK, "pausing again")
	h.do(request{method: http.MethodPatch, path: path, cookie: cookie, body: map[string]any{"idle_timeout": "9m"}}).mustStatus(t, http.StatusOK, "an idle timeout")
	if stored, _ := h.st.GetPool(h.ctx, pool.ID); stored.Enabled || !stored.AutoPaused {
		t.Fatalf("an edit of the idle timeout put a paused pool back in use: enabled %t, paused %t", stored.Enabled, stored.AutoPaused)
	}
}

func TestAnExportLeavesOutThePoolsTheControllerKeeps(t *testing.T) {
	h := newHarness(t, sizeOn)
	inst, _, _ := h.keptMediumPool()
	h.pool(inst, "mine")
	_, viewer := h.user("viewer", store.RoleViewer)

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/pools/export", cookie: viewer})
	resp.mustStatus(t, http.StatusOK, "exporting pools")
	var doc struct {
		Pools []struct {
			Name string `json:"name"`
		} `json:"pools"`
	}
	resp.into(t, &doc)
	if len(doc.Pools) != 1 || !strings.HasSuffix(doc.Pools[0].Name, "mine") {
		t.Fatalf("the export holds %+v; the pool the controller keeps is worked out from this instance's hosts and is not for another", doc.Pools)
	}
}

// A pool the controller keeps is worked out from the hosts it has, and its name is
// the one the controller would use, so a document written by hand can reach it by
// accident. Importing over it would be undone by the next pass, or would change a
// figure its page says is not typed.
func TestAnImportDoesNotEditAPoolTheControllerKeeps(t *testing.T) {
	h := newHarness(t, sizeOn)
	_, _, pool := h.keptMediumPool()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	doc, err := json.Marshal(map[string]any{
		"export_version": poolsExportVersion,
		"pools": []any{map[string]any{
			"name": pool.Name, "installation": "acme", "labels": []string{"zoomies-medium"}, "max_runners": 40,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A dry run says it would be refused, and why...
	preview := h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie, body: map[string]any{"document": string(doc), "dry_run": true}})
	preview.mustStatus(t, http.StatusOK, "a dry run")
	var out poolsImportResponse
	preview.into(t, &out)
	if len(out.Changes) != 1 || out.Changes[0].Action != poolRefusedAction || !strings.Contains(out.Changes[0].Reason, "a pool the controller keeps from your hosts") {
		t.Fatalf("a dry run over a pool the controller keeps gave %+v; want it refused, and why", out)
	}
	// ...and a real run refuses to do any of it.
	applied := h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie, body: map[string]any{"document": string(doc)}})
	applied.mustStatus(t, http.StatusUnprocessableEntity, "a real run over a pool the controller keeps")
	if !strings.Contains(string(applied.body), "a pool the controller keeps from your hosts") {
		t.Fatalf("the refusal does not say why: %s", applied.body)
	}
	if stored, _ := h.st.GetPool(h.ctx, pool.ID); stored.MaxRunners != pool.MaxRunners || !stored.FromHosts() {
		t.Fatalf("an import changed the pool the controller keeps: %+v", stored)
	}
}

// Pools by hand on an organisation go in Zoomies' own runner group, and an
// automatic pool left in GitHub's Default group would be offered to every
// repository of the organisation while the pools beside it were not.
func TestAnAutomaticPoolIsInTheRunnerGroupAPoolMadeByHandOnTheInstallationGets(t *testing.T) {
	h := newHarness(t, sizeOn)
	inst, _, kept := h.keptMediumPool()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: map[string]any{
		"name": "twin", "installation_id": inst.ID, "labels": []string{"twin"},
	}})
	resp.mustStatus(t, http.StatusCreated, "creating a pool by hand")
	var byHand controller.PoolView
	resp.into(t, &byHand)
	if byHand.RunnerGroup != controller.ManagedRunnerGroupName || kept.RunnerGroup != byHand.RunnerGroup {
		t.Fatalf("the pool by hand is in runner group %q and the automatic pool in %q; both should be in %q",
			byHand.RunnerGroup, kept.RunnerGroup, controller.ManagedRunnerGroupName)
	}
}

// An automatic pool is the pool an operator would get from the API with nothing
// but these set. The scheduler writes the one and this package the other, and
// nothing else keeps their defaults together.
func TestAnAutomaticPoolIsMadeWithTheDefaultsAPoolCreatedByHandWouldGet(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	want := scheduler.NewAutoPool(scheduler.AutoPoolInput{InstallationID: inst.ID, DockerMode: store.DockerNone},
		"amd64", store.SizeMedium, store.BackendDocker)
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: map[string]any{
		"name":              "zoomies-twin",
		"installation_id":   inst.ID,
		"labels":            want.Labels,
		"platform":          map[string]any{"arch": "amd64"},
		"host_selector":     map[string]any{"size": "medium"},
		"size_from_profile": true,
	}})
	resp.mustStatus(t, http.StatusCreated, "creating the pool by hand")
	var made controller.PoolView
	resp.into(t, &made)

	if made.Backend != want.Backend || made.PullPolicy != want.PullPolicy || made.IdleTimeout != want.IdleTimeout ||
		made.Ephemeral != want.Ephemeral || made.DockerMode != want.DockerMode || made.Cache != want.Cache ||
		made.CPUBurst != want.CPUBurst || made.MemoryBurst != want.MemoryBurst ||
		made.SizeFromProfile != want.SizeFromProfile || made.Enabled != want.Enabled ||
		made.Resources != want.Resources || made.Platform.Arch != want.Platform.Arch {
		t.Fatalf("a pool made by hand with the same few settings differs from an automatic one:\n by hand: %+v\n automatic: %+v", made, want)
	}
}

// ---------------------------------------------------------------------------
// Hosts
// ---------------------------------------------------------------------------

func TestASizeTagMustBeAClassWhileTheControllerWorksClassesOut(t *testing.T) {
	on := newHarness(t, sizeOn)
	host := on.host("build-1")
	operator, _ := on.user("operator", store.RoleOperator)
	cookie := on.session(operator)

	resp := on.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie,
		body: map[string]any{"labels": map[string]string{"size": "huge"}}})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "a size tag that is not a class")
	if !strings.Contains(string(resp.body), "small, medium or large") {
		t.Fatalf("the refusal does not say what the tag may be: %s", resp.body)
	}
	if stored, _ := on.st.GetHost(on.ctx, host.ID); stored.Labels["size"] != "" {
		t.Fatalf("a refused tag was written: %v", stored.Labels)
	}
	on.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie,
		body: map[string]any{"labels": map[string]string{"size": "large", "rack": "b4"}}}).
		mustStatus(t, http.StatusOK, "a size tag that is a class")

	// The class has to be written as it is, because the pool's host selector
	// compares a label exactly: "Large" would be counted towards the large pool
	// and never matched by it.
	for _, loose := range []string{"Large", " large", "LARGE"} {
		on.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie,
			body: map[string]any{"labels": map[string]string{"size": loose}}}).
			mustStatus(t, http.StatusUnprocessableEntity, "a size tag written as "+loose)
	}

	// And the token that will put the label on a host is held to the same rule,
	// where the host would otherwise land in no pool and say nothing at the time.
	admin, _ := on.user("admin", store.RoleAdmin)
	adminCookie := on.session(admin)
	on.do(request{method: http.MethodPost, path: "/api/v1/join-tokens", cookie: adminCookie,
		body: map[string]any{"labels": map[string]string{"size": "huge"}}}).
		mustStatus(t, http.StatusUnprocessableEntity, "a join token carrying a size tag that is not a class")
	on.do(request{method: http.MethodPost, path: "/api/v1/join-tokens", cookie: adminCookie,
		body: map[string]any{"labels": map[string]string{"size": "small"}}}).
		mustStatus(t, http.StatusCreated, "a join token carrying a size tag that is a class")

	// With both switches off the key is an ordinary label.
	off := newHarness(t)
	host = off.host("build-1")
	operator, _ = off.user("operator", store.RoleOperator)
	cookie = off.session(operator)
	off.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie,
		body: map[string]any{"labels": map[string]string{"size": "huge"}}}).
		mustStatus(t, http.StatusOK, "a size label with the feature off")
	offAdmin, _ := off.user("admin", store.RoleAdmin)
	off.do(request{method: http.MethodPost, path: "/api/v1/join-tokens", cookie: off.session(offAdmin),
		body: map[string]any{"labels": map[string]string{"size": "huge"}}}).
		mustStatus(t, http.StatusCreated, "a join token with a size label and the feature off")
}
