package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// richPool is a pool with something set in every part of it the document is
// meant to carry, so a round trip that dropped one would show.
func richPool(h *harness, inst *store.Installation) *store.Pool {
	h.t.Helper()
	cost := 0.12
	provision := store.Duration(12 * time.Minute)
	drain := store.Duration(0)
	p := &store.Pool{
		Name:                   "zoomies-builders",
		InstallationID:         inst.ID,
		Labels:                 store.BrandLabels([]string{"builders", "gpu"}),
		RunnerGroup:            "release",
		Backend:                store.BackendDocker,
		Platform:               store.Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "x64"},
		PullPolicy:             store.PullAlways,
		MinRunners:             1,
		MaxRunners:             7,
		RepositoryScaleUpLimit: 3,
		CostPerRunnerHour:      &cost,
		Priority:               5,
		IdleTimeout:            store.Duration(9 * time.Minute),
		Ephemeral:              true,
		DockerMode:             store.DockerNone,
		Resources:              store.Resources{CPUs: 2, MemoryMB: 4096, DiskGB: 20, PidsLimit: 2048},
		MemoryBurst:            store.MemoryBurstPolicy{Mode: store.MemoryBurstAutomatic, MaxMemoryMB: 6144, SpillMB: 1024},
		RunnerSettings:         store.RunnerSettings{ProvisionTimeout: &provision, DrainTimeout: &drain},
		Cache:                  store.CacheConfig{Enabled: true, Scope: store.CacheScopePool},
		HostSelector:           store.StringMap{"zone": "a"},
		Env:                    store.StringMap{"REGISTRY_PASSWORD": "hunter2-never-exported"},
		Enabled:                true,
	}
	if err := h.st.CreatePool(h.ctx, p); err != nil {
		h.t.Fatalf("CreatePool: %v", err)
	}
	return p
}

// zonedHost is a host the rich pool can run on, so its plan carries no
// stranding refusal and no warning that are not what a test is about.
func zonedHost(h *harness) *store.Host {
	h.t.Helper()
	host := h.host("vm-zone-a")
	host.Labels = store.StringMap{"zone": "a"}
	host.CPUs, host.MemoryMB = 16, 65536
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		h.t.Fatalf("UpdateHost: %v", err)
	}
	return host
}

func exportPoolsDoc(t *testing.T, h *harness, cookie string) (poolsExport, []byte) {
	t.Helper()
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/pools/export", cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "export")
	var doc poolsExport
	resp.into(t, &doc)
	return doc, resp.body
}

func importPools(t *testing.T, h *harness, cookie string, body map[string]any, want int) poolsImportResponse {
	t.Helper()
	resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie, body: body})
	resp.mustStatus(t, want, "import")
	var out poolsImportResponse
	if want == http.StatusOK {
		resp.into(t, &out)
	}
	return out
}

func auditCount(t *testing.T, h *harness, action string) int {
	t.Helper()
	rows, _, err := h.st.ListAudit(h.ctx, store.AuditFilter{Actions: []string{action}}, store.Page{Limit: 100})
	if err != nil {
		t.Fatalf("ListAudit(%s): %v", action, err)
	}
	return len(rows)
}

// The whole point of the pair: a fleet's pools, written on one instance and
// read on another that has the same GitHub App installed, are the same pools
// -- and reading the file a second time changes nothing at all.
func TestPoolsExportedFromOneInstanceAreTheSamePoolsOnAnother(t *testing.T) {
	source := newHarness(t)
	srcInst := source.installation()
	zonedHost(source)
	richPool(source, srcInst)
	srcOp, _ := source.user("operator", store.RoleOperator)
	srcCookie := source.session(srcOp)

	doc, body := exportPoolsDoc(t, source, srcCookie)
	if doc.ExportVersion != poolsExportVersion || doc.Version == "" || doc.ExportedAt.IsZero() {
		t.Errorf("the document does not say what it is: %+v", doc)
	}
	if strings.Contains(string(body), "hunter2-never-exported") {
		t.Fatal("an environment value is in the export")
	}
	if strings.Contains(string(body), srcInst.ID) {
		t.Error("the export names the installation by an id that means nothing on another instance")
	}
	if len(doc.Pools) != 1 || doc.Pools[0].Installation != "acme" ||
		len(doc.Pools[0].EnvKeys) != 1 || doc.Pools[0].EnvKeys[0] != "REGISTRY_PASSWORD" {
		t.Fatalf("pools = %+v", doc.Pools)
	}
	assertShape(t, loadSpec(t), "PoolsExport", body)

	target := newHarness(t)
	tgtInst := target.installation()
	if tgtInst.ID == srcInst.ID {
		t.Fatal("the two instances share an installation id, so this test proves nothing")
	}
	zonedHost(target)
	tgtOp, _ := target.user("operator", store.RoleOperator)
	tgtCookie := target.session(tgtOp)

	// A dry run says what would happen and writes nothing.
	preview := target.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: tgtCookie,
		body: map[string]any{"document": string(body), "dry_run": true}})
	preview.mustStatus(t, http.StatusOK, "dry run")
	assertShape(t, loadSpec(t), "PoolsImport", preview.body)
	var planned poolsImportResponse
	preview.into(t, &planned)
	if planned.Applied || planned.Summary.Create != 1 || len(planned.Changes) != 1 ||
		planned.Changes[0].Action != poolCreateAction || len(planned.Changes[0].Fields) == 0 {
		t.Fatalf("dry run = %+v", planned)
	}
	if pools, _ := target.st.ListPools(target.ctx); len(pools) != 0 {
		t.Fatalf("a dry run created %d pools", len(pools))
	}

	applied := importPools(t, target, tgtCookie, map[string]any{"document": string(body)}, http.StatusOK)
	if !applied.Applied || applied.Summary.Create != 1 {
		t.Fatalf("apply = %+v", applied)
	}
	got, err := target.st.GetPoolByName(target.ctx, "zoomies-builders")
	if err != nil {
		t.Fatalf("the pool was not created: %v", err)
	}
	if got.InstallationID != tgtInst.ID {
		t.Errorf("installation = %s, want this instance's %s", got.InstallationID, tgtInst.ID)
	}
	// Checked on the row as well as through a second export, because a field
	// the document forgot would be missing from both exports alike.
	if got.Platform.OSVersion != "24.04" || got.MaxRunners != 7 || got.MinRunners != 1 || got.Resources.MemoryMB != 4096 ||
		got.RunnerSettings.ProvisionTimeout == nil || got.RunnerSettings.ProvisionTimeout.Duration() != 12*time.Minute ||
		got.RunnerSettings.DrainTimeout == nil || got.PullPolicy != store.PullAlways || got.HostSelector["zone"] != "a" ||
		!strings.Contains(strings.Join(got.Labels, ","), "gpu") || got.RunnerGroup != "release" {
		t.Errorf("the created pool is not the source's: %+v", got)
	}
	if got.MemoryBurst != (store.MemoryBurstPolicy{Mode: store.MemoryBurstAutomatic, MaxMemoryMB: 6144, SpillMB: 1024}) {
		t.Errorf("the memory valve did not cross with the pool: %+v", got.MemoryBurst)
	}
	if len(got.Env) != 0 {
		t.Errorf("env = %v; an import has no values to set", got.Env)
	}
	again, _ := exportPoolsDoc(t, target, tgtCookie)
	want, _ := json.Marshal(doc.Pools[0])
	have, _ := json.Marshal(again.Pools[0])
	// The one difference is the environment, whose names came across as work
	// to do rather than as variables.
	want = []byte(strings.Replace(string(want), `"env_keys":["REGISTRY_PASSWORD"]`, `"env_keys":[]`, 1))
	if string(want) != string(have) {
		t.Errorf("the pool did not survive the trip:\nsource %s\ntarget %s", want, have)
	}

	// Repeat apply: every pool already so, and nothing written but the one
	// row saying the import happened, as the settings import leaves.
	updated := got.UpdatedAt
	before := auditCount(t, target, "pool.update")
	imports := auditCount(t, target, "pools.import")
	repeat := importPools(t, target, tgtCookie, map[string]any{"document": string(body)}, http.StatusOK)
	if repeat.Summary.Unchanged != 1 || repeat.Summary.Create+repeat.Summary.Change+repeat.Summary.Refused != 0 {
		t.Fatalf("repeat apply = %+v", repeat)
	}
	if n := auditCount(t, target, "pool.update"); n != before {
		t.Errorf("a repeat apply wrote %d pool.update rows", n-before)
	}
	if n := auditCount(t, target, "pools.import"); n != imports+1 {
		t.Errorf("pools.import rows went from %d to %d, want one more", imports, n)
	}
	if after, _ := target.st.GetPoolByName(target.ctx, "zoomies-builders"); !after.UpdatedAt.Equal(updated) {
		t.Error("a repeat apply rewrote the pool")
	}
}

// A pool that was edited on this side since the document was taken is shown
// setting by setting, with both values, so the operator can see what the file
// would undo before it does.
func TestAConflictingEditIsPlannedSettingBySetting(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	zonedHost(h)
	p := richPool(h, inst)
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)
	_, body := exportPoolsDoc(t, h, cookie)

	p.MaxRunners = 11
	p.Labels = store.BrandLabels([]string{"builders"})
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatal(err)
	}
	plan := importPools(t, h, cookie, map[string]any{"document": string(body), "dry_run": true}, http.StatusOK)
	if len(plan.Changes) != 1 || plan.Changes[0].Action != poolChangeAction {
		t.Fatalf("plan = %+v", plan)
	}
	fields := map[string]poolImportField{}
	for _, f := range plan.Changes[0].Fields {
		fields[f.Field] = f
	}
	if len(fields) != 2 {
		t.Errorf("fields = %+v, want exactly max_runners and labels", plan.Changes[0].Fields)
	}
	if f := fields["max_runners"]; f.Current != float64(11) || f.Incoming != float64(7) {
		t.Errorf("max_runners = %+v, want 11 now and 7 incoming", f)
	}
	if _, ok := fields["labels"]; !ok {
		t.Error("the label edit is not in the plan")
	}
	if got, _ := h.st.GetPool(h.ctx, p.ID); got.MaxRunners != 11 {
		t.Error("a dry run wrote the change")
	}
}

// A field a pool entry leaves out keeps its value, which is what the settings
// import does with a key a document leaves out: a hand-written file that
// says only "max_runners: 2" must not reset the rest of the pool to defaults.
func TestAFieldTheDocumentLeavesOutKeepsItsValue(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	zonedHost(h)
	p := richPool(h, inst)
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	document := "pools:\n  - name: zoomies-builders\n    installation: acme\n    max_runners: 2\n"
	out := importPools(t, h, cookie, map[string]any{"document": document}, http.StatusOK)
	if out.Summary.Change != 1 || len(out.Changes[0].Fields) != 1 || out.Changes[0].Fields[0].Field != "max_runners" {
		t.Fatalf("import = %+v", out)
	}
	got, _ := h.st.GetPool(h.ctx, p.ID)
	if got.MaxRunners != 2 || got.Priority != 5 || got.RunnerSettings.ProvisionTimeout == nil ||
		got.Env["REGISTRY_PASSWORD"] != "hunter2-never-exported" {
		t.Errorf("pool after import = %+v", got)
	}
}

// Every way a pool in the document can be refused, each with a sentence the
// operator can act on, and each blocking the real run until it is fixed or
// skipped -- one change or none.
func TestARefusedPoolBlocksTheImportUntilItIsSkipped(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	zonedHost(h)
	richPool(h, inst)
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	good := "  - name: zoomies-fresh\n    installation: acme\n    labels: [fresh]\n"
	cases := []struct {
		name   string
		entry  string
		pool   string
		reason string
	}{
		{"an installation this instance does not have", "  - name: zoomies-elsewhere\n    installation: globex\n    labels: [x]\n",
			"zoomies-elsewhere", "no installation on this instance covers globex"},
		{"no installation at all", "  - name: zoomies-nowhere\n    labels: [x]\n",
			"zoomies-nowhere", "name the installation"},
		{"a figure the pools API refuses", "  - name: zoomies-empty\n    installation: acme\n    labels: [x]\n    max_runners: 0\n",
			"zoomies-empty", "max_runners"},
		{"a setting that is not one", "  - name: zoomies-typo\n    installation: acme\n    labels: [x]\n    max_runner: 3\n",
			"zoomies-typo", "max_runner"},
		{"an edit that takes the pool's last host away", "  - name: zoomies-builders\n    installation: acme\n    backend: podman\n",
			"zoomies-builders", "nowhere to run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := "pools:\n" + good + tc.entry
			plan := importPools(t, h, cookie, map[string]any{"document": document, "dry_run": true}, http.StatusOK)
			var refused *poolImportChange
			for i := range plan.Changes {
				if plan.Changes[i].Pool == tc.pool {
					refused = &plan.Changes[i]
				}
			}
			if refused == nil || refused.Action != poolRefusedAction || !strings.Contains(refused.Reason, tc.reason) {
				t.Fatalf("plan = %+v, want %s refused mentioning %q", plan.Changes, tc.pool, tc.reason)
			}
			if plan.Summary.Create != 1 || plan.Summary.Refused != 1 {
				t.Errorf("summary = %+v", plan.Summary)
			}

			resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie,
				body: map[string]any{"document": document}})
			resp.mustStatus(t, http.StatusUnprocessableEntity, "a real run with a refusal in it")
			if _, err := h.st.GetPoolByName(h.ctx, "zoomies-fresh"); err == nil {
				t.Fatal("the good pool was created although the document was refused")
			}
		})
	}

	document := "pools:\n" + good + cases[0].entry
	out := importPools(t, h, cookie, map[string]any{"document": document, "skip": []string{"zoomies-elsewhere"}}, http.StatusOK)
	if !out.Applied || out.Summary.Create != 1 || out.Summary.Skipped != 1 {
		t.Fatalf("skipping the refused pool = %+v", out)
	}
	if _, err := h.st.GetPoolByName(h.ctx, "zoomies-fresh"); err != nil {
		t.Fatalf("the good pool was not created: %v", err)
	}
}

// A new pool no host can run yet is created with a warning rather than
// refused, as the wizard allows: the host it needs may be the next thing an
// operator joins, and refusing the file would make them do it in the wrong
// order.
func TestANewPoolNoHostCanRunIsCreatedWithAWarning(t *testing.T) {
	h := newHarness(t)
	h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)
	document := "pools:\n  - name: zoomies-lonely\n    installation: acme\n    labels: [lonely]\n"
	plan := importPools(t, h, cookie, map[string]any{"document": document, "dry_run": true}, http.StatusOK)
	if plan.Changes[0].Action != poolCreateAction || len(plan.Changes[0].Warnings) == 0 {
		t.Fatalf("plan = %+v, want a create with a warning", plan.Changes)
	}
}

func TestAPoolsDocumentThatIsNotOneIsRefusedReadably(t *testing.T) {
	h := newHarness(t)
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)
	for _, tc := range []struct{ document, says string }{
		{"", "nothing to import"},
		{"settings:\n  retention:\n    jobs: 48h\n", "settings export"},
		{"export_version: 99\npools: []\n", "version 99"},
		{"pools: {}\n", "not a list"},
		{": : :", "does not parse"},
	} {
		resp := h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie,
			body: map[string]any{"document": tc.document, "dry_run": true}})
		resp.mustStatus(t, http.StatusUnprocessableEntity, tc.document)
		if !strings.Contains(string(resp.body), tc.says) {
			t.Errorf("%q: %s, want it to say %q", tc.document, resp.body, tc.says)
		}
	}
}

// Pools belong to the fleet, so the file follows the pool routes: whoever can
// read the pools may keep a copy, and bringing one in takes what creating and
// editing a pool take.
func TestPoolsTransferFollowsThePoolRoles(t *testing.T) {
	h := newHarness(t)
	h.installation()
	viewer, _ := h.user("viewer", store.RoleViewer)
	cookie := h.session(viewer)
	h.do(request{method: http.MethodGet, path: "/api/v1/pools/export?format=yaml", cookie: cookie}).
		mustStatus(t, http.StatusOK, "a viewer exporting")
	h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie,
		body: map[string]any{"document": "pools: []\n", "dry_run": true}}).
		mustStatus(t, http.StatusForbidden, "a viewer importing")
	h.do(request{method: http.MethodGet, path: "/api/v1/pools/export?format=toml", cookie: cookie}).
		mustStatus(t, http.StatusBadRequest, "an export format that is not one")
}

// The YAML form is a file somebody keeps in a repository, so it has to read
// back as itself.
func TestTheYAMLExportImportsBackUnchanged(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	zonedHost(h)
	richPool(h, inst)
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)
	y := h.do(request{method: http.MethodGet, path: "/api/v1/pools/export?format=yaml", cookie: cookie})
	y.mustStatus(t, http.StatusOK, "yaml export")
	if !strings.HasPrefix(string(y.body), "# Zoomies pools") || strings.Contains(string(y.body), "hunter2") {
		t.Fatalf("yaml export:\n%s", y.body)
	}
	plan := importPools(t, h, cookie, map[string]any{"document": string(y.body), "dry_run": true}, http.StatusOK)
	if plan.Summary.Unchanged != 1 || len(plan.Changes) != 1 {
		t.Fatalf("the YAML export does not read back as itself: %+v", plan.Changes)
	}
}

// A pool that registers with its own labels only would, imported without the
// setting, start answering to self-hosted and linux on the other instance --
// taking jobs its owner kept off it on purpose.
func TestAPoolKeepsItsOwnLabelsOnlyThroughAnExport(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	zonedHost(h)
	p := richPool(h, inst)
	p.Ephemeral = false
	p.NoDefaultLabels = true
	if err := h.st.UpdatePool(h.ctx, p); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	op, _ := h.user("operator", store.RoleOperator)
	doc, body := exportPoolsDoc(t, h, h.session(op))
	if len(doc.Pools) != 1 || !doc.Pools[0].NoDefaultLabels {
		t.Fatalf("the export dropped no_default_labels: %+v", doc.Pools)
	}
	assertShape(t, loadSpec(t), "PoolsExport", body)
}
