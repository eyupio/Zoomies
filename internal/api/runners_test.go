package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// TestDrainRunnerIsAccepted covers the two answers a drain can have: 202 when
// the runner has been told, and 409 when there is nothing left to tell.
func TestDrainRunnerIsAccepted(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	idle := h.runner(pool, host, store.RunnerIdle)
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/runners/" + idle.ID + "/drain", cookie: cookie})
	resp.mustStatus(t, http.StatusAccepted, "drain")
	var out runnerResponse
	resp.into(t, &out)
	if out.State != store.RunnerDraining {
		t.Errorf("state = %q, want draining", out.State)
	}
	if out.PoolName != pool.Name || out.HostName != host.Name {
		t.Errorf("the response does not name the pool and host: %+v", out)
	}

	// A terminal runner cannot be drained, and saying so is better than
	// pretending it worked.
	removed := h.runner(pool, host, store.RunnerRemoved)
	conflict := h.do(request{method: http.MethodPost, path: "/api/v1/runners/" + removed.ID + "/drain", cookie: cookie})
	conflict.mustStatus(t, http.StatusConflict, "drain a removed runner")
	if code := conflict.errorCode(t); code != codeConflict {
		t.Errorf("error code = %q, want %q", code, codeConflict)
	}

	missing := h.do(request{method: http.MethodPost, path: "/api/v1/runners/run_nope/drain", cookie: cookie})
	missing.mustStatus(t, http.StatusNotFound, "drain an unknown runner")
}

func TestDeleteRunnerIsAccepted(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	run := h.runner(pool, host, store.RunnerIdle)
	u, _ := h.user("operator", store.RoleOperator)

	resp := h.do(request{method: http.MethodDelete, path: "/api/v1/runners/" + run.ID + "?force=true", cookie: h.session(u)})
	resp.mustStatus(t, http.StatusAccepted, "delete")

	after, err := h.st.GetRunner(h.ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if !after.State.Terminal() {
		t.Errorf("state = %q after a forced delete, want a terminal state", after.State)
	}
}

// TestBulkRunnerActionReportsEachID is what makes a partial failure visible:
// forty runners and one that has already finished must not collapse into one
// status code.
func TestBulkRunnerActionReportsEachID(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	good := h.runner(pool, host, store.RunnerIdle)
	terminal := h.runner(pool, host, store.RunnerRemoved)
	u, _ := h.user("operator", store.RoleOperator)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/runners/bulk", cookie: h.session(u),
		body: map[string]any{"action": "drain", "ids": []string{good.ID, terminal.ID, "run_nope"}}})
	resp.mustStatus(t, http.StatusOK, "bulk drain")

	var out bulkRunnerResponse
	resp.into(t, &out)
	if len(out.Results) != 3 {
		t.Fatalf("got %d results, want 3: %+v", len(out.Results), out.Results)
	}
	byID := map[string]bulkRunnerResult{}
	for _, r := range out.Results {
		byID[r.ID] = r
	}
	if !byID[good.ID].OK {
		t.Errorf("the idle runner was not drained: %+v", byID[good.ID])
	}
	if byID[terminal.ID].OK || byID[terminal.ID].Error == "" {
		t.Errorf("a terminal runner reported success: %+v", byID[terminal.ID])
	}
	if byID["run_nope"].OK || byID["run_nope"].Error == "" {
		t.Errorf("an unknown ID reported success: %+v", byID["run_nope"])
	}
}

// TestBulkDeleteNeedsTheDeletePermission checks the second half of the bulk
// endpoint's authorisation: the route gate is the weaker action, so the handler
// has to check the stronger one itself.
func TestBulkDeleteNeedsTheDeletePermission(t *testing.T) {
	h := newHarness(t)
	// A token scoped to draining only. Its role is high enough; its scopes are
	// not, which is exactly the case a route-level check would miss.
	token := h.token("drainer", store.RoleOperator, "runners:drain")

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/runners/bulk", token: token,
		body: map[string]any{"action": "delete", "ids": []string{"run_nope"}}})
	resp.mustStatus(t, http.StatusForbidden, "bulk delete with a drain-only token")
	if !strings.Contains(resp.errorMessage(t), "runners:delete") {
		t.Errorf("the refusal does not name the scope: %q", resp.errorMessage(t))
	}

	// The same token may still drain.
	drain := h.do(request{method: http.MethodPost, path: "/api/v1/runners/bulk", token: token,
		body: map[string]any{"action": "drain", "ids": []string{"run_nope"}}})
	drain.mustStatus(t, http.StatusOK, "bulk drain with a drain-only token")
}

func TestBulkRunnerActionValidates(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	empty := h.do(request{method: http.MethodPost, path: "/api/v1/runners/bulk", cookie: cookie,
		body: map[string]any{"action": "drain", "ids": []string{}}})
	empty.mustStatus(t, http.StatusUnprocessableEntity, "bulk with no ids")

	unknown := h.do(request{method: http.MethodPost, path: "/api/v1/runners/bulk", cookie: cookie,
		body: map[string]any{"action": "explode", "ids": []string{"run_1"}}})
	unknown.mustStatus(t, http.StatusUnprocessableEntity, "bulk with an unknown action")
	if !strings.Contains(unknown.errorMessage(t), "carried out") {
		t.Errorf("unexpected message: %q", unknown.errorMessage(t))
	}
}

// TestRunnerListFilters covers the filters the runners grid uses, including the
// repeated-key array style the OpenAPI document specifies.
func TestRunnerListFilters(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	linux := h.pool(inst, "linux-x64")
	arm := h.pool(inst, "linux-arm64")
	host := h.host("vm-1")
	idle := h.runner(linux, host, store.RunnerIdle)
	busy := h.runner(linux, host, store.RunnerBusy)
	h.runner(arm, host, store.RunnerIdle)
	removed := h.runner(linux, host, store.RunnerRemoved)
	u, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(u)

	all := h.do(request{method: http.MethodGet, path: "/api/v1/runners", cookie: cookie})
	all.mustStatus(t, http.StatusOK, "list runners")
	var listed page[runnerResponse]
	all.into(t, &listed)
	if listed.Total != 3 {
		t.Errorf("total = %d, want 3 (terminal runners are hidden by default)", listed.Total)
	}
	if listed.Limit != defaultLimit || listed.Offset != 0 {
		t.Errorf("page envelope = limit %d offset %d, want %d/0", listed.Limit, listed.Offset, defaultLimit)
	}

	withRemoved := h.do(request{method: http.MethodGet, path: "/api/v1/runners?include_removed=true", cookie: cookie})
	var everything page[runnerResponse]
	withRemoved.into(t, &everything)
	if everything.Total != 4 {
		t.Errorf("total with include_removed = %d, want 4", everything.Total)
	}

	states := h.do(request{method: http.MethodGet, path: "/api/v1/runners?state=idle&state=busy", cookie: cookie})
	var byState page[runnerResponse]
	states.into(t, &byState)
	if byState.Total != 3 {
		t.Errorf("idle+busy total = %d, want 3", byState.Total)
	}

	byPool := h.do(request{method: http.MethodGet, path: "/api/v1/runners?pool_id=" + linux.ID, cookie: cookie})
	var pooled page[runnerResponse]
	byPool.into(t, &pooled)
	if pooled.Total != 2 {
		t.Errorf("pool total = %d, want 2", pooled.Total)
	}

	bad := h.do(request{method: http.MethodGet, path: "/api/v1/runners?state=sleepy", cookie: cookie})
	bad.mustStatus(t, http.StatusBadRequest, "unknown state")

	_ = idle
	_ = busy
	_ = removed
}

// TestRunnerDetailAndTimeline covers the detail page's payload.
func TestRunnerDetailAndTimeline(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	run := h.runner(pool, host, store.RunnerBusy)
	job := h.job(pool, store.JobInProgress)
	if err := h.st.AssignRunnerJob(h.ctx, run.ID, job.ID); err != nil {
		t.Fatalf("AssignRunnerJob: %v", err)
	}
	u, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(u)

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/runners/" + run.ID, cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "runner detail")

	var detail runnerDetailResponse
	resp.into(t, &detail)
	if detail.Host == nil || detail.Host.ID != host.ID {
		t.Errorf("the detail does not carry the host: %+v", detail.Host)
	}
	if detail.Pool == nil || detail.Pool.ID != pool.ID {
		t.Errorf("the detail does not carry the pool: %+v", detail.Pool)
	}
	if detail.CurrentJob == nil || detail.CurrentJob.ID != job.ID {
		t.Errorf("the detail does not carry the current job: %+v", detail.CurrentJob)
	}
	if !detail.LogsAvailable {
		t.Error("logs_available is false for a running runner on a healthy host")
	}
	if len(detail.Timeline) == 0 {
		t.Fatal("the timeline is empty")
	}
	if detail.Timeline[0].State != store.RunnerProvisioning {
		t.Errorf("the timeline does not start at provisioning: %+v", detail.Timeline[0])
	}
	last := detail.Timeline[len(detail.Timeline)-1]
	if last.State != store.RunnerBusy {
		t.Errorf("the timeline does not end in the current state: %+v", last)
	}

	timeline := h.do(request{method: http.MethodGet, path: "/api/v1/runners/" + run.ID + "/timeline", cookie: cookie})
	timeline.mustStatus(t, http.StatusOK, "timeline")
	var entries list[timelineEntry]
	timeline.into(t, &entries)
	if len(entries.Items) != len(detail.Timeline) {
		t.Errorf("the timeline endpoint and the detail disagree: %d vs %d", len(entries.Items), len(detail.Timeline))
	}
}

// A runner stuck in `registering` has one symptom and two unrelated causes: a
// container that never started is a backend or image problem on its host, and
// one that started and never reached GitHub is a credential, network or GitHub
// problem. The runner page has to tell those apart on its own, or an operator
// diagnosing one leaves for the Hosts page and guesses.
func TestARunnerCarriesBothHalvesOfComingUp(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	host := h.host("vm-1")
	pool := h.pool(inst, "linux-x64")
	run := h.runner(pool, host, store.RunnerRegistering)

	started := time.Now().Add(-90 * time.Second).UTC().Truncate(time.Millisecond)
	run.ContainerStartedAt = &started
	if err := h.st.UpdateRunner(h.ctx, run); err != nil {
		t.Fatalf("UpdateRunner: %v", err)
	}

	u, _ := h.user("operator", store.RoleOperator)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/runners/" + run.ID, cookie: h.session(u)})
	resp.mustStatus(t, http.StatusOK, "runner detail")
	var detail struct {
		ContainerStartedAt *time.Time `json:"container_started_at"`
		RegisteredAt       *time.Time `json:"registered_at"`
		Host               *struct {
			LastHeartbeat time.Time `json:"last_heartbeat"`
		} `json:"host"`
	}
	resp.into(t, &detail)

	if detail.ContainerStartedAt == nil {
		t.Error("the container-started stamp is not on the runner, so the page cannot say the container came up")
	}
	// The other half is absent, which is the whole diagnosis: it started and
	// never registered. Absent rather than a zero time, so the page can leave
	// the row out instead of rendering the year 1.
	if detail.RegisteredAt != nil {
		t.Errorf("a runner that never registered reported a registration at %v", detail.RegisteredAt)
	}
	// And the host's heartbeat comes with it, because "is the agent even
	// alive?" is the next question and it used to need another page.
	if detail.Host == nil || detail.Host.LastHeartbeat.IsZero() {
		t.Error("the runner detail does not carry its host's last heartbeat")
	}
}

// The timeline's two registering rows are the same two halves, and without the
// stage they render as two rows both labelled "Registering", which tells an
// operator nothing at all.
func TestTheTimelineNamesTheStageWithinAState(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	host := h.host("vm-1")
	pool := h.pool(inst, "linux-x64")
	run := h.runner(pool, host, store.RunnerRegistering)

	started := time.Now().Add(-2 * time.Minute).UTC().Truncate(time.Millisecond)
	registered := started.Add(30 * time.Second)
	run.ContainerStartedAt, run.RegisteredAt = &started, &registered
	if err := h.st.UpdateRunner(h.ctx, run); err != nil {
		t.Fatalf("UpdateRunner: %v", err)
	}

	u, _ := h.user("operator", store.RoleOperator)
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/runners/" + run.ID + "/timeline", cookie: h.session(u)})
	resp.mustStatus(t, http.StatusOK, "timeline")
	var out struct {
		Items []struct {
			State string `json:"state"`
			Stage string `json:"stage"`
		} `json:"items"`
	}
	resp.into(t, &out)

	stages := map[string]bool{}
	registering := 0
	for _, e := range out.Items {
		if e.Stage != "" {
			stages[e.Stage] = true
		}
		if e.State == string(store.RunnerRegistering) {
			registering++
		}
	}
	if registering < 2 {
		t.Fatalf("expected both registering rows, got %d: %+v", registering, out.Items)
	}
	for _, want := range []string{"container_started", "registered"} {
		if !stages[want] {
			t.Errorf("the timeline does not carry the %q stage: %+v", want, out.Items)
		}
	}
}

// TestDrainingABusyRunnerOverTheAPINeedsConfirming is the HTTP half of the
// confirmation. The gate lives in the controller so that no entry point can
// bypass it, and this is what an operator, a script or curl actually meets.
func TestDrainingABusyRunnerOverTheAPINeedsConfirming(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	busy := h.runner(pool, host, store.RunnerBusy)
	_, cookie := h.user("operator", store.RoleOperator)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/runners/" + busy.ID + "/drain", cookie: cookie})
	resp.mustStatus(t, http.StatusConflict, "draining a busy runner without confirming")
	if body := string(resp.body); !strings.Contains(body, "confirm=true") {
		t.Errorf("the refusal does not say how to proceed:\n%s", body)
	}
	// It changed nothing.
	if after, _ := h.st.GetRunner(h.ctx, busy.ID); after.State != store.RunnerBusy {
		t.Errorf("runner state = %q, want busy", after.State)
	}

	ok := h.do(request{method: http.MethodPost, path: "/api/v1/runners/" + busy.ID + "/drain?confirm=true", cookie: cookie})
	ok.mustStatus(t, http.StatusAccepted, "draining a busy runner with confirm=true")
}

// TestDrainingAnIdleRunnerOverTheAPINeedsNothing is the limit: the confirmation
// must not become a tax on the ordinary case.
func TestDrainingAnIdleRunnerOverTheAPINeedsNothing(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	idle := h.runner(pool, host, store.RunnerIdle)
	_, cookie := h.user("operator", store.RoleOperator)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/runners/" + idle.ID + "/drain", cookie: cookie})
	resp.mustStatus(t, http.StatusAccepted, "draining an idle runner")
}

// TestARunnersPoolEnvIsOperatorOnly covers the second route that renders a pool.
//
// The runner page embeds the pool the runner belongs to, and a pool's env is
// where a registry password or proxy credential ends up. Reading a runner is a
// viewer action, so rendering the pool there without the role check handed a
// read-only token the values that GET /pools already withholds from it. The
// keys stay for both roles, because the pool page lists names and never values.
func TestARunnersPoolEnvIsOperatorOnly(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.Env = store.StringMap{"REGISTRY_PASSWORD": "hunter2", "HTTP_PROXY": "http://proxy:3128"}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	run := h.runner(pool, h.host("vm-1"), store.RunnerIdle)

	_, viewer := h.user("looker", store.RoleViewer)
	_, operator := h.user("doer", store.RoleOperator)
	path := "/api/v1/runners/" + run.ID

	resp := h.do(request{method: http.MethodGet, path: path, cookie: viewer})
	resp.mustStatus(t, http.StatusOK, "runner detail as a viewer")
	body := string(resp.body)
	if strings.Contains(body, "hunter2") || strings.Contains(body, "proxy:3128") {
		t.Errorf("GET %s as a viewer leaked a pool env value:\n%s", path, body)
	}
	if !strings.Contains(body, "REGISTRY_PASSWORD") {
		t.Errorf("GET %s as a viewer dropped the env keys as well as the values:\n%s", path, body)
	}

	resp = h.do(request{method: http.MethodGet, path: path, cookie: operator})
	resp.mustStatus(t, http.StatusOK, "runner detail as an operator")
	if !strings.Contains(string(resp.body), "hunter2") {
		t.Errorf("an operator no longer sees the env they set on the runner's pool:\n%s", resp.body)
	}
}
