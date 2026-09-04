package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// poolBody is a valid pool definition, which each test then breaks in one way.
func poolBody(instID string) map[string]any {
	return map[string]any{
		"name":            "linux-x64",
		"installation_id": instID,
		"labels":          []string{"self-hosted", "linux", "x64", "linux-x64"},
		"backend":         "docker",
		"image":           "ghcr.io/eyupio/zoomies-runner:test",
		"min_runners":     0,
		"max_runners":     4,
		"idle_timeout":    "5m",
		"ephemeral":       true,
		"docker_mode":     "none",
		"enabled":         true,
	}
}

// TestPoolRoundTrip walks the whole pool lifecycle the way the UI does:
// validate, create, read, edit, disable, enable, delete.
func TestPoolRoundTrip(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.host("vm-1")
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	// The wizard's review step.
	validate := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate",
		cookie: cookie, body: poolBody(inst.ID)})
	validate.mustStatus(t, http.StatusOK, "validate")
	var verdict validatePoolResponse
	validate.into(t, &verdict)
	if !verdict.Valid {
		t.Fatalf("a valid pool was rejected: %+v", verdict.Errors)
	}
	if verdict.MatchingHosts != 1 {
		t.Errorf("matching_hosts = %d, want 1", verdict.MatchingHosts)
	}
	if len(verdict.Warnings) != 0 {
		t.Errorf("a safe pool produced warnings: %+v", verdict.Warnings)
	}
	// Nothing was created.
	if pools, err := h.st.ListPools(h.ctx); err != nil || len(pools) != 0 {
		t.Fatalf("validate created a pool: %v %v", pools, err)
	}

	created := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: poolBody(inst.ID)})
	created.mustStatus(t, http.StatusCreated, "create")
	var pool poolResponse
	created.into(t, &pool)
	if pool.ID == "" || pool.Name != "linux-x64" {
		t.Fatalf("created pool = %+v", pool)
	}
	if pool.InstallationTarget != "acme" {
		t.Errorf("installation_target = %q, want acme", pool.InstallationTarget)
	}
	if pool.Counts.Live != 0 || pool.QueuedJobs != 0 {
		t.Errorf("a new pool reports work it does not have: %+v", pool.Counts)
	}

	// The same name twice is a conflict rather than a second pool.
	dup := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: poolBody(inst.ID)})
	dup.mustStatus(t, http.StatusUnprocessableEntity, "duplicate name")

	got := h.do(request{method: http.MethodGet, path: "/api/v1/pools/" + pool.ID, cookie: cookie})
	got.mustStatus(t, http.StatusOK, "get")

	// A patch changes one field and leaves the rest alone.
	patched := h.do(request{method: http.MethodPatch, path: "/api/v1/pools/" + pool.ID, cookie: cookie,
		body: map[string]any{"max_runners": 8}})
	patched.mustStatus(t, http.StatusOK, "patch")
	var updated poolResponse
	patched.into(t, &updated)
	if updated.MaxRunners != 8 {
		t.Errorf("max_runners = %d, want 8", updated.MaxRunners)
	}
	if updated.Name != "linux-x64" || len(updated.Labels) != 4 {
		t.Errorf("a partial update lost fields: %+v", updated)
	}

	disabled := h.do(request{method: http.MethodPost, path: "/api/v1/pools/" + pool.ID + "/disable", cookie: cookie})
	disabled.mustStatus(t, http.StatusOK, "disable")
	var afterDisable poolResponse
	disabled.into(t, &afterDisable)
	if afterDisable.Enabled {
		t.Error("the pool is still enabled after being disabled")
	}

	enabled := h.do(request{method: http.MethodPost, path: "/api/v1/pools/" + pool.ID + "/enable", cookie: cookie})
	enabled.mustStatus(t, http.StatusOK, "enable")

	deleted := h.do(request{method: http.MethodDelete, path: "/api/v1/pools/" + pool.ID, cookie: cookie})
	deleted.mustStatus(t, http.StatusOK, "delete")
	var deletion deletePoolResponse
	deleted.into(t, &deletion)
	if deletion.RunnersAffected != 0 {
		t.Errorf("runners_affected = %d, want 0", deletion.RunnersAffected)
	}

	gone := h.do(request{method: http.MethodGet, path: "/api/v1/pools/" + pool.ID, cookie: cookie})
	gone.mustStatus(t, http.StatusNotFound, "get after delete")

	// Every one of those mutations left an audit row.
	audit := h.do(request{method: http.MethodGet, path: "/api/v1/audit?target_kind=pool", cookie: cookie})
	audit.mustStatus(t, http.StatusOK, "audit")
	var events page[store.AuditEvent]
	audit.into(t, &events)
	seen := map[string]bool{}
	for _, e := range events.Items {
		seen[e.Action] = true
	}
	for _, want := range []string{"pool.create", "pool.update", "pool.enable", "pool.disable", "pool.delete"} {
		if !seen[want] {
			t.Errorf("no %s row in the audit log (saw %v)", want, seen)
		}
	}
}

// TestPoolValidationNamesTheField checks that a refusal is something an
// operator can act on rather than "invalid request".
func TestPoolValidationNamesTheField(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	cases := []struct {
		name   string
		mutate func(map[string]any)
		field  string
		says   string
	}{
		{"no name", func(b map[string]any) { delete(b, "name") }, "name", "name"},
		{"no labels", func(b map[string]any) { b["labels"] = []string{} }, "labels", "at least one label"},
		{"only implicit labels", func(b map[string]any) { b["labels"] = []string{"self-hosted", "linux"} }, "labels", "at least one label"},
		{"unknown backend", func(b map[string]any) { b["backend"] = "kubernetes" }, "backend", "docker, podman or process"},
		{"max below min", func(b map[string]any) { b["min_runners"] = 5; b["max_runners"] = 2 }, "min_runners", "maximum"},
		{"zero max", func(b map[string]any) { b["max_runners"] = 0 }, "max_runners", "at least 1"},
		{"bad duration", func(b map[string]any) { b["idle_timeout"] = "5 munutes" }, "idle_timeout", "5m"},
		{"unknown installation", func(b map[string]any) { b["installation_id"] = "ins_nope" }, "installation_id", "no installation"},
		{"docker on the process backend", func(b map[string]any) { b["backend"] = "process"; b["docker_mode"] = "dind" }, "docker_mode", "process backend"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := poolBody(inst.ID)
			tc.mutate(body)

			create := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: body})
			create.mustStatus(t, http.StatusUnprocessableEntity, "create")
			var env errorEnvelope
			create.into(t, &env)
			if env.Error.Code != codeUnprocessable {
				t.Fatalf("code = %q, want %q", env.Error.Code, codeUnprocessable)
			}
			found := ""
			for _, fe := range env.Errors {
				if fe.Field == tc.field {
					found = fe.Message
				}
			}
			if found == "" {
				t.Fatalf("no error on %q; got %+v", tc.field, env.Errors)
			}
			if !strings.Contains(found, tc.says) {
				t.Errorf("the message on %s does not say %q: %q", tc.field, tc.says, found)
			}

			// The dry run reports exactly the same thing without creating
			// anything, which is the property the wizard depends on.
			validate := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate", cookie: cookie, body: body})
			validate.mustStatus(t, http.StatusOK, "validate")
			var verdict validatePoolResponse
			validate.into(t, &verdict)
			if verdict.Valid {
				t.Fatal("validate accepted a pool that create refused")
			}
			if len(verdict.Errors) != len(env.Errors) {
				t.Errorf("validate reported %d errors, create reported %d", len(verdict.Errors), len(env.Errors))
			}
		})
	}
}

// TestPoolValidateWarnsAboutDangerousSettings is the other half of the review
// step: the settings that are allowed but weaken isolation are named before the
// pool is created, not after.
func TestPoolValidateWarnsAboutDangerousSettings(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	h.host("vm-1")
	u, _ := h.user("operator", store.RoleOperator)

	body := poolBody(inst.ID)
	body["docker_mode"] = "host-socket"
	body["run_as_root"] = true
	body["ephemeral"] = false

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate", cookie: h.session(u), body: body})
	resp.mustStatus(t, http.StatusOK, "validate")
	var verdict validatePoolResponse
	resp.into(t, &verdict)
	if !verdict.Valid {
		t.Fatalf("a dangerous pool is still a valid one: %+v", verdict.Errors)
	}
	if len(verdict.Warnings) < 3 {
		t.Fatalf("expected a warning for each dangerous setting, got %d: %+v", len(verdict.Warnings), verdict.Warnings)
	}
	joined := ""
	for _, w := range verdict.Warnings {
		joined += w.Title + "|"
		if w.Fix == "" {
			t.Errorf("warning %q has no fix", w.Title)
		}
	}
	for _, want := range []string{"root on the host", "as root", "persistent"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning mentions %q; got %s", want, joined)
		}
	}
}

func TestPoolValidateWarnsWhenNoHostMatches(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)

	resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate",
		cookie: h.session(u), body: poolBody(inst.ID)})
	resp.mustStatus(t, http.StatusOK, "validate")

	var verdict validatePoolResponse
	resp.into(t, &verdict)
	if verdict.MatchingHosts != 0 {
		t.Fatalf("matching_hosts = %d with no hosts registered", verdict.MatchingHosts)
	}
	found := false
	for _, w := range verdict.Warnings {
		if w.Code == "pool.no_matching_hosts" {
			found = true
		}
	}
	if !found {
		t.Errorf("a pool no host can run produced no warning: %+v", verdict.Warnings)
	}
}

// TestDeletePoolDrainsItsRunners checks the default: deleting a pool finishes
// the work in flight rather than interrupting it.
func TestDeletePoolDrainsItsRunners(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	h.runner(pool, host, store.RunnerIdle)
	h.runner(pool, host, store.RunnerIdle)
	u, _ := h.user("operator", store.RoleOperator)

	resp := h.do(request{method: http.MethodDelete, path: "/api/v1/pools/" + pool.ID, cookie: h.session(u)})
	resp.mustStatus(t, http.StatusOK, "delete")
	var out deletePoolResponse
	resp.into(t, &out)
	if out.RunnersAffected != 2 {
		t.Fatalf("runners_affected = %d, want 2", out.RunnersAffected)
	}
}
