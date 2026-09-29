package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// The support bundle is the document an operator attaches to a bug report, so
// what these tests protect is what a stranger reading it can work out: that
// the fleet is described, that a section which could not be gathered says so
// by name rather than taking the rest of the document with it, and that no
// workflow log body is in there for either of them to have to think about.

func TestTheBundleDescribesTheInstanceAndTheFleet(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	h.runner(pool, host, store.RunnerBusy)
	h.job(pool, store.JobQueued)
	// Platform: this is about what a whole bundle carries, and the whole one
	// is the platform's. What a fleet's administrator gets instead -- the
	// build it all ran on, and not the machine -- is
	// TestTheFleetsBundleIdentifiesTheBuildAndNotTheMachine.
	u, _ := h.user("admin", store.RolePlatform)
	cookie := h.session(u)

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/diagnostics/bundle", cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "support bundle")

	var b supportBundle
	resp.into(t, &b)

	if len(b.Errors) != 0 {
		t.Errorf("a healthy instance produced section errors: %+v", b.Errors)
	}
	if b.BundleVersion != bundleVersion {
		t.Errorf("bundle_version = %d, want %d", b.BundleVersion, bundleVersion)
	}
	if b.GeneratedAt.IsZero() {
		t.Error("the bundle does not say when it was taken, which is the first thing a reader asks")
	}
	// The build and the process. A bug report without them is a report about
	// an unknown binary.
	if b.Instance.Go == "" || b.Instance.OS == "" || b.Instance.Goroutines == 0 {
		t.Errorf("the instance section is not describing this process: %+v", b.Instance)
	}
	if b.Instance.SchemaApplied == 0 {
		t.Error("the instance section does not say which migrations this database has taken")
	}
	// The fleet.
	if len(b.Pools) == 0 || len(b.Hosts) == 0 || len(b.Runners) == 0 || len(b.Installations) == 0 {
		t.Errorf("the bundle is missing a fleet section: %d pools, %d hosts, %d runners, %d installations",
			len(b.Pools), len(b.Hosts), len(b.Runners), len(b.Installations))
	}
	if b.Config == nil {
		t.Error("the bundle carries no configuration, which is half of every support case")
	}
	if b.Problems == nil {
		t.Error("the bundle carries no problems section")
	}

	// The work in flight, with the controller's own answer beside it -- the
	// same answer the drawer and the CLI render, so a bundle and the page the
	// operator was looking at cannot disagree.
	if len(b.Jobs) == 0 {
		t.Fatal("the queued job is not in the bundle")
	}
	if len(b.Explanations) == 0 {
		t.Fatal("the queued job has no explanation in the bundle")
	}
	if b.Explanations[0].Summary == "" {
		t.Error("the explanation carries no sentence")
	}
}

// A bundle is taken at the moment a query is most likely to fail, so a section
// that cannot be gathered costs its own contents and nothing else. Before this,
// the alternative was a 500: a support case with no document at all, on the one
// instance whose trouble somebody was trying to report.
func TestABundleSurvivesTheSectionsItCannotGather(t *testing.T) {
	h := newHarness(t)
	h.pool(h.installation(), "linux-x64")

	// A cancelled context is every store read failing at once, which is the
	// hardest version of the case: nothing at all can be gathered.
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	b := h.api.supportBundle(ctx)

	if len(b.Errors) == 0 {
		t.Fatal("nothing could be read and the bundle reported no errors")
	}
	for _, e := range b.Errors {
		if e.Section == "" || e.Error == "" {
			t.Errorf("an error entry does not name its section and cause: %+v", e)
		}
	}
	// What survives is what does not need the database: the build, the
	// process, and the configuration this controller is running.
	if b.Instance.Go == "" {
		t.Error("the instance section went with the database, and it is the half that did not need it")
	}
	if b.Config == nil {
		t.Error("the configuration went with the database, and it is read from memory")
	}
	if b.GeneratedAt.IsZero() {
		t.Error("a bundle that gathered nothing still has to say when it was taken")
	}
}

// Workflow logs are never in a bundle: there is no redaction pass for them and
// a log holds whatever a workflow printed. What replaces them has to be enough
// to act on, which means the runners worth looking at and the route that
// fetches each one.
func TestTheBundlePointsAtLogsRatherThanCarryingThem(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := h.host("vm-1")
	failed := h.runner(pool, host, store.RunnerFailed)
	h.runner(pool, host, store.RunnerBusy)
	u, _ := h.user("admin", store.RoleAdmin)
	cookie := h.session(u)

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/diagnostics/bundle", cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "support bundle")
	var b supportBundle
	resp.into(t, &b)

	if b.Logs == nil {
		t.Fatal("the bundle has no logs section, so nothing tells a reader where the logs are")
	}
	if b.Logs.Note == "" {
		t.Error("the logs section does not say why the bodies are absent, which reads as an omission rather than a decision")
	}
	var found *bundleLogPointer
	for i, p := range b.Logs.Runners {
		if p.RunnerID == failed.ID {
			found = &b.Logs.Runners[i]
		}
	}
	if found == nil {
		t.Fatalf("the failed runner %s is not among the log pointers", failed.ID)
	}
	if !strings.HasSuffix(found.Download, "/runners/"+failed.ID+"/logs/download") {
		t.Errorf("the pointer does not name the route that fetches the log: %q", found.Download)
	}
	// A runner that is doing its job is not what a support case is about, and
	// a pointer per runner would bury the ones that are.
	for _, p := range b.Logs.Runners {
		if p.State == store.RunnerBusy || p.State == store.RunnerIdle {
			t.Errorf("a healthy runner (%s, %s) is in the log pointers", p.RunnerID, p.State)
		}
	}
}

// The byte cap is the backstop for the fleet whose rows are individually
// enormous, which no row-count cap can see. What matters as much as the cap is
// that a shortened bundle says so: a reader who cannot tell a capped section
// from an empty one will conclude the fleet has no runners.
func TestAnOversizeBundleShedsSectionsAndSaysWhichOnes(t *testing.T) {
	huge := strings.Repeat("x", 1024)
	b := supportBundle{BundleVersion: bundleVersion, Errors: []bundleError{}}
	for len(b.ScalingEvents)*len(huge) < bundleMaxBytes*2 {
		b.ScalingEvents = append(b.ScalingEvents, &store.ScalingEvent{ID: huge, Reason: huge})
	}
	b.Hosts = append(b.Hosts, hostResponse{})

	raw, err := marshalBundle(&b)
	if err != nil {
		t.Fatalf("marshalBundle: %v", err)
	}
	if len(raw) > bundleMaxBytes {
		t.Errorf("the bundle is %d bytes, over its %d-byte cap", len(raw), bundleMaxBytes)
	}

	var got supportBundle
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the shed bundle is not valid JSON: %v", err)
	}
	var shed bool
	for _, tr := range got.Truncated {
		if tr.Section == "scaling_events" {
			shed = true
			if tr.Reason == "" {
				t.Error("a shed section does not say why it went")
			}
		}
	}
	if !shed {
		t.Error("the scaling history was dropped and the bundle does not say so")
	}
	// The fleet's own shape is what a bundle is for, and is never what goes.
	if len(got.Hosts) == 0 {
		t.Error("the hosts section was shed; only the recomputable sections may be")
	}
}

// TestTheBundleKeepsAPoolsEnvKeysAndNotItsValues covers the one section of the
// bundle that was not "secret-free on its own route".
//
// A pool's env is where a registry password or a deploy key ends up. An
// administrator may read those values on the pool's own route, but the bundle
// is a file made to be attached to an issue or sent to whoever runs the
// platform, so a routine "send me a bundle" handed the values to a third
// party. The keys stay, because which variables a pool sets is what a reader
// diagnosing it needs, and the pool export carries the same.
func TestTheBundleKeepsAPoolsEnvKeysAndNotItsValues(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "linux-x64")
	pool.Env = store.StringMap{"NEXUS_PW": "hunter2", "DEPLOY_KEY": "deploy-key-value-9f3"}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	// Both roles that may take a bundle: an administrator holds pools.write,
	// so a role-based blanking would have let their bundle keep the values.
	for _, role := range []store.Role{store.RoleAdmin, store.RolePlatform} {
		u, _ := h.user("bundler-"+string(role), role)
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/diagnostics/bundle", cookie: h.session(u)})
		resp.mustStatus(t, http.StatusOK, "support bundle as "+string(role))
		body := string(resp.body)
		if strings.Contains(body, "hunter2") || strings.Contains(body, "deploy-key-value-9f3") {
			t.Errorf("the bundle taken by a %s carries a pool env value:\n%s", role, body)
		}
		for _, key := range []string{"NEXUS_PW", "DEPLOY_KEY"} {
			if !strings.Contains(body, key) {
				t.Errorf("the bundle taken by a %s dropped the env key %s as well as its value", role, key)
			}
		}
	}
}
