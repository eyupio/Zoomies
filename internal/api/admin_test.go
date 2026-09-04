package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// TestUserLifecycle covers the account management an admin does, including the
// refusal that stops an instance being locked out of itself.
func TestUserLifecycle(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("root", store.RoleAdmin)
	cookie := h.session(admin)

	created := h.do(request{method: http.MethodPost, path: "/api/v1/users", cookie: cookie,
		body: map[string]any{"username": "Bob", "password": testPassword, "role": "operator", "email": "bob@example.com"}})
	created.mustStatus(t, http.StatusCreated, "create user")
	var bob userResponse
	created.into(t, &bob)
	if bob.Username != "bob" {
		t.Errorf("username = %q, want it lowercased", bob.Username)
	}
	if !bob.MustChangePassword {
		t.Error("an account created with somebody else's password should have to change it")
	}

	dup := h.do(request{method: http.MethodPost, path: "/api/v1/users", cookie: cookie,
		body: map[string]any{"username": "bob", "password": testPassword, "role": "viewer"}})
	dup.mustStatus(t, http.StatusConflict, "duplicate username")

	short := h.do(request{method: http.MethodPost, path: "/api/v1/users", cookie: cookie,
		body: map[string]any{"username": "carol", "password": "short", "role": "viewer"}})
	short.mustStatus(t, http.StatusUnprocessableEntity, "short password")

	patched := h.do(request{method: http.MethodPatch, path: "/api/v1/users/" + bob.ID, cookie: cookie,
		body: map[string]any{"role": "viewer", "display_name": "Bob"}})
	patched.mustStatus(t, http.StatusOK, "patch user")
	var updated userResponse
	patched.into(t, &updated)
	if updated.Role != store.RoleViewer || updated.DisplayName != "Bob" {
		t.Errorf("updated user = %+v", updated)
	}

	// The last administrator cannot be demoted, disabled or deleted.
	demote := h.do(request{method: http.MethodPatch, path: "/api/v1/users/" + admin.ID, cookie: cookie,
		body: map[string]any{"role": "viewer"}})
	demote.mustStatus(t, http.StatusConflict, "demote the last admin")
	if !strings.Contains(demote.errorMessage(t), "administrator") {
		t.Errorf("the refusal does not explain itself: %q", demote.errorMessage(t))
	}
	remove := h.do(request{method: http.MethodDelete, path: "/api/v1/users/" + admin.ID, cookie: cookie})
	remove.mustStatus(t, http.StatusConflict, "delete the last admin")

	reset := h.do(request{method: http.MethodPost, path: "/api/v1/users/" + bob.ID + "/password", cookie: cookie,
		body: map[string]any{"new_password": "a-brand-new-password"}})
	reset.mustStatus(t, http.StatusNoContent, "reset a password")

	deleted := h.do(request{method: http.MethodDelete, path: "/api/v1/users/" + bob.ID, cookie: cookie})
	deleted.mustStatus(t, http.StatusNoContent, "delete user")
	gone := h.do(request{method: http.MethodGet, path: "/api/v1/users/" + bob.ID, cookie: cookie})
	gone.mustStatus(t, http.StatusNotFound, "get a deleted user")
}

// TestSettings covers what may be changed at runtime and what may not.
func TestSettings(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("root", store.RoleAdmin)
	cookie := h.session(admin)

	get := h.do(request{method: http.MethodGet, path: "/api/v1/settings", cookie: cookie})
	get.mustStatus(t, http.StatusOK, "settings")
	var settings settingsResponse
	get.into(t, &settings)
	if settings.Version == "" || settings.DatabasePath == "" {
		t.Errorf("settings = %+v", settings)
	}
	if len(settings.RestartRequiredKeys) == 0 {
		t.Error("no restart-required keys, so the UI cannot tell which fields are read-only")
	}
	// The encryption key is reported as configured, never as a value.
	security, _ := settings.Config["security"].(map[string]any)
	if security["encryption_key_configured"] != true {
		t.Errorf("security = %v", security)
	}
	if _, leaked := security["encryption_key"]; leaked {
		t.Error("the settings response carries the encryption key")
	}

	patched := h.do(request{method: http.MethodPatch, path: "/api/v1/settings", cookie: cookie,
		body: map[string]any{"retention": map[string]any{"jobs": "48h"}, "log.level": "debug"}})
	patched.mustStatus(t, http.StatusOK, "patch settings")
	var after settingsResponse
	patched.into(t, &after)
	retention, _ := after.Config["retention"].(map[string]any)
	if retention["jobs"] != "48h0m0s" {
		t.Errorf("retention.jobs = %v, want 48h0m0s", retention["jobs"])
	}
	if h.cfg.Retention.Jobs.String() != "48h0m0s" {
		t.Errorf("the running configuration was not changed: %s", h.cfg.Retention.Jobs)
	}

	// A setting that needs a restart is refused with a message that says so.
	refused := h.do(request{method: http.MethodPatch, path: "/api/v1/settings", cookie: cookie,
		body: map[string]any{"server": map[string]any{"bind": "0.0.0.0:9000"}}})
	refused.mustStatus(t, http.StatusUnprocessableEntity, "patch a restart-only setting")
	var env errorEnvelope
	refused.into(t, &env)
	if len(env.Errors) == 0 || env.Errors[0].Field != "server.bind" {
		t.Fatalf("expected a field error on server.bind: %+v", env)
	}
	if !strings.Contains(env.Errors[0].Message, "restart") {
		t.Errorf("the message does not say a restart is needed: %q", env.Errors[0].Message)
	}
	if h.cfg.Server.Bind == "0.0.0.0:9000" {
		t.Error("a refused setting was applied anyway")
	}

	// So is an unparseable value.
	bad := h.do(request{method: http.MethodPatch, path: "/api/v1/settings", cookie: cookie,
		body: map[string]any{"retention.audit": "forever"}})
	bad.mustStatus(t, http.StatusUnprocessableEntity, "patch with a bad duration")
}

// TestJoinTokenLifecycle covers minting the credential a new host enrols with.
func TestJoinTokenLifecycle(t *testing.T) {
	h := newHarness(t)
	admin, _ := h.user("root", store.RoleAdmin)
	cookie := h.session(admin)

	created := h.do(request{method: http.MethodPost, path: "/api/v1/join-tokens", cookie: cookie,
		body: map[string]any{"ttl": "1h", "capacity": 4, "labels": map[string]string{"zone": "eu"}}})
	created.mustStatus(t, http.StatusCreated, "create join token")
	var token createJoinTokenResponse
	created.into(t, &token)
	if !token.Usable || token.Capacity != 4 || token.Labels["zone"] != "eu" {
		t.Errorf("join token = %+v", token)
	}
	if !strings.Contains(token.Command, "--mode agent") {
		t.Errorf("the command is not the one-liner for a new host: %q", token.Command)
	}

	listed := h.do(request{method: http.MethodGet, path: "/api/v1/join-tokens", cookie: cookie})
	listed.mustStatus(t, http.StatusOK, "list join tokens")
	if strings.Contains(string(listed.body), token.Token) {
		t.Fatal("the join token list contains the secret")
	}

	revoked := h.do(request{method: http.MethodDelete, path: "/api/v1/join-tokens/" + token.ID, cookie: cookie})
	revoked.mustStatus(t, http.StatusNoContent, "revoke")
	again := h.do(request{method: http.MethodDelete, path: "/api/v1/join-tokens/" + token.ID, cookie: cookie})
	again.mustStatus(t, http.StatusNotFound, "revoke twice")

	bad := h.do(request{method: http.MethodPost, path: "/api/v1/join-tokens", cookie: cookie,
		body: map[string]any{"ttl": "soon"}})
	bad.mustStatus(t, http.StatusUnprocessableEntity, "create with a bad ttl")
}

// TestHostCordonAndDelete covers taking a machine out of the fleet safely.
func TestHostCordonAndDelete(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	h.runner(pool, host, store.RunnerIdle)

	operator, _ := h.user("operator", store.RoleOperator)
	admin, _ := h.user("root", store.RoleAdmin)

	cordon := h.do(request{method: http.MethodPost, path: "/api/v1/hosts/" + host.ID + "/cordon",
		cookie: h.session(operator), body: map[string]any{"cordoned": true}})
	cordon.mustStatus(t, http.StatusOK, "cordon")
	var out hostResponse
	cordon.into(t, &out)
	if !out.Cordoned {
		t.Error("the host is not cordoned")
	}
	if out.ActiveRunners != 1 {
		t.Errorf("cordoning changed the runner count: %+v", out)
	}

	capacity := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID,
		cookie: h.session(operator), body: map[string]any{"capacity": 8, "labels": map[string]string{"zone": "eu"}}})
	capacity.mustStatus(t, http.StatusOK, "patch host")
	var patched hostResponse
	capacity.into(t, &patched)
	if patched.Capacity != 8 || patched.Labels["zone"] != "eu" {
		t.Errorf("patched host = %+v", patched)
	}

	// A host with live runners is not deleted by accident.
	refused := h.do(request{method: http.MethodDelete, path: "/api/v1/hosts/" + host.ID, cookie: h.session(admin)})
	refused.mustStatus(t, http.StatusConflict, "delete a busy host")
	if !strings.Contains(refused.errorMessage(t), "force") {
		t.Errorf("the refusal does not say how to proceed: %q", refused.errorMessage(t))
	}

	forced := h.do(request{method: http.MethodDelete, path: "/api/v1/hosts/" + host.ID + "?force=true",
		cookie: h.session(admin)})
	forced.mustStatus(t, http.StatusNoContent, "forced delete")
}

// TestJobsListAndFacets covers the job history page.
func TestJobsListAndFacets(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	h.job(pool, store.JobQueued)
	done := h.job(pool, store.JobCompleted)
	done.Conclusion = "success"
	if _, err := h.st.UpsertJob(h.ctx, done); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	viewer, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(viewer)

	list := h.do(request{method: http.MethodGet, path: "/api/v1/jobs", cookie: cookie})
	list.mustStatus(t, http.StatusOK, "list jobs")
	var jobs page[jobResponse]
	list.into(t, &jobs)
	if jobs.Total != 2 {
		t.Fatalf("total = %d, want 2", jobs.Total)
	}
	for _, j := range jobs.Items {
		if j.PoolName != pool.Name {
			t.Errorf("job %s does not name its pool: %+v", j.ID, j)
		}
	}

	filtered := h.do(request{method: http.MethodGet, path: "/api/v1/jobs?state=completed", cookie: cookie})
	var completed page[jobResponse]
	filtered.into(t, &completed)
	if completed.Total != 1 {
		t.Errorf("completed total = %d, want 1", completed.Total)
	}

	badState := h.do(request{method: http.MethodGet, path: "/api/v1/jobs?state=nope", cookie: cookie})
	badState.mustStatus(t, http.StatusBadRequest, "unknown job state")

	badSince := h.do(request{method: http.MethodGet, path: "/api/v1/jobs?since=yesterday", cookie: cookie})
	badSince.mustStatus(t, http.StatusBadRequest, "unparseable since")

	facets := h.do(request{method: http.MethodGet, path: "/api/v1/jobs/facets", cookie: cookie})
	facets.mustStatus(t, http.StatusOK, "facets")
	var f jobFacetsResponse
	facets.into(t, &f)
	if len(f.Repos) == 0 || f.Repos[0] != "acme/widgets" {
		t.Errorf("repos = %v", f.Repos)
	}
	if len(f.Conclusions) == 0 {
		t.Errorf("conclusions = %v, want the one that exists", f.Conclusions)
	}
}

// TestWebhookDeliveriesAndTest covers the two endpoints an operator uses when
// scaling has gone quiet.
func TestWebhookDeliveriesAndTest(t *testing.T) {
	h := newHarness(t)
	viewer, _ := h.user("viewer", store.RoleViewer)
	operator, _ := h.user("operator", store.RoleOperator)

	deliveries := h.do(request{method: http.MethodGet, path: "/api/v1/webhook-deliveries", cookie: h.session(viewer)})
	deliveries.mustStatus(t, http.StatusOK, "webhook deliveries")
	var out webhookDeliveriesResponse
	deliveries.into(t, &out)
	if out.LastReceivedAt != nil {
		t.Errorf("last_received_at = %v on an instance that has never had a delivery", out.LastReceivedAt)
	}
	if !strings.Contains(string(deliveries.body), `"last_received_at":null`) {
		t.Error("last_received_at is absent rather than null, so 'quiet' and 'broken' look the same")
	}

	badStatus := h.do(request{method: http.MethodGet, path: "/api/v1/webhook-deliveries?status=maybe",
		cookie: h.session(viewer)})
	badStatus.mustStatus(t, http.StatusBadRequest, "unknown delivery status")

	check := h.do(request{method: http.MethodPost, path: "/api/v1/webhook-test", cookie: h.session(operator)})
	check.mustStatus(t, http.StatusOK, "webhook test")
	var verdict webhookCheckResponse
	check.into(t, &verdict)
	if verdict.Message == "" {
		t.Error("the reachability check said nothing")
	}
	if !verdict.Reachable && verdict.Fix == "" {
		t.Error("the check reported a failure with no remedy")
	}
	if !verdict.PollingAvailable {
		t.Error("polling_available is false although github.poll_fallback is on")
	}
}

// TestAuditListAndFilters covers the page that answers "who did that?".
func TestAuditListAndFilters(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	create := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: poolBody(inst.ID)})
	create.mustStatus(t, http.StatusCreated, "create pool")

	audit := h.do(request{method: http.MethodGet, path: "/api/v1/audit", cookie: cookie})
	audit.mustStatus(t, http.StatusOK, "audit")
	var events page[store.AuditEvent]
	audit.into(t, &events)
	if events.Total == 0 {
		t.Fatal("the audit log is empty after a pool was created")
	}
	found := false
	for _, e := range events.Items {
		if e.Action == "pool.create" {
			found = true
			if e.ActorName != "operator" {
				t.Errorf("the row does not name the actor: %+v", e)
			}
			if e.After == "" {
				t.Error("the row has no after picture")
			}
		}
	}
	if !found {
		t.Fatal("no pool.create row")
	}

	actions := h.do(request{method: http.MethodGet, path: "/api/v1/audit/actions", cookie: cookie})
	actions.mustStatus(t, http.StatusOK, "audit actions")
	var names list[string]
	actions.into(t, &names)
	if len(names.Items) == 0 {
		t.Fatal("no distinct action names")
	}

	filtered := h.do(request{method: http.MethodGet, path: "/api/v1/audit?action=pool.create", cookie: cookie})
	var only page[store.AuditEvent]
	filtered.into(t, &only)
	for _, e := range only.Items {
		if e.Action != "pool.create" {
			t.Errorf("the action filter let %q through", e.Action)
		}
	}
}

// TestOverviewEndpoints covers the four reads the Overview makes.
func TestOverviewEndpoints(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	h.runner(pool, host, store.RunnerIdle)
	h.job(pool, store.JobQueued)
	viewer, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(viewer)

	stats := h.do(request{method: http.MethodGet, path: "/api/v1/stats?window=24h", cookie: cookie})
	stats.mustStatus(t, http.StatusOK, "stats")
	body := stats.json(t)
	if body["window"] != "24h0m0s" {
		t.Errorf("window = %v, want the requested one echoed back", body["window"])
	}
	runners, _ := body["runners"].(map[string]any)
	if runners["idle"] != float64(1) {
		t.Errorf("runners = %v", runners)
	}

	badWindow := h.do(request{method: http.MethodGet, path: "/api/v1/stats?window=soon", cookie: cookie})
	badWindow.mustStatus(t, http.StatusBadRequest, "unparseable window")

	samples := h.do(request{method: http.MethodGet, path: "/api/v1/samples?window=1h", cookie: cookie})
	samples.mustStatus(t, http.StatusOK, "samples")
	if !strings.Contains(string(samples.body), `"items"`) {
		t.Errorf("samples = %s", truncate(samples.body))
	}

	problems := h.do(request{method: http.MethodGet, path: "/api/v1/problems", cookie: cookie})
	problems.mustStatus(t, http.StatusOK, "problems")

	scaling := h.do(request{method: http.MethodGet, path: "/api/v1/scaling-events?limit=5", cookie: cookie})
	scaling.mustStatus(t, http.StatusOK, "scaling events")
}
