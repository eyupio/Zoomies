package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// measuredHost is a host whose agent has said what machine it is: 12 CPUs and
// 32 GB, which leave 11.4 CPUs and 31130 MB once the floors under the reserve
// are held back.
func (h *harness) measuredHost(name string) *store.Host {
	h.t.Helper()
	host := h.host(name)
	host.CPUs, host.MemoryMB = 12, 32768
	host.DiskTotalMB, host.DiskFreeMB = 500*1024, 400*1024
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		h.t.Fatalf("UpdateHost: %v", err)
	}
	return host
}

// An operator can say how big a runner is on a host, the answer carries the
// slots it gives and each figure's source, and saving a capacity afterwards
// leaves the profile where it was.
func TestAnOperatorCanSetAHostsRunnerProfile(t *testing.T) {
	h := newHarness(t)
	host := h.measuredHost("big")
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	set := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie,
		body: map[string]any{"capacity": 8, "runner_profile": map[string]any{
			"minimum":  map[string]any{"cpus": 1},
			"standard": map[string]any{"cpus": 3, "memory_mb": 8192, "burst_max_cpus": 6},
		}}})
	set.mustStatus(t, http.StatusOK, "set the runner profile")
	var view hostResponse
	set.into(t, &view)
	if view.RunnerProfile == nil || view.RunnerProfile.Standard.CPUs != 3 || view.RunnerProfile.Minimum.CPUs != 1 {
		t.Fatalf("the response does not carry the profile that was set: %+v", view.RunnerProfile)
	}
	if view.Slots != 3 || view.EffectiveCapacity != 3 || view.Capacity != 8 || view.SlotsLimitedBy != "cpu" {
		t.Errorf("slots = %d (effective %d) of capacity %d limited by %q, want 3 of 8 limited by cpu: 11.4 CPUs hold three of 3",
			view.Slots, view.EffectiveCapacity, view.Capacity, view.SlotsLimitedBy)
	}
	if got := view.EffectiveProfile.Standard; got.CPUsSource != "host" || got.BurstMaxCPUs != 6 || got.BurstMaxCPUsSource != "host" {
		t.Errorf("the effective standard does not say it came from the host: %+v", got)
	}
	if got := view.EffectiveProfile.Minimum; got.CPUs != 1 || got.CPUsSource != "host" {
		t.Errorf("the effective minimum = %+v", got)
	}

	stored, err := h.st.GetHost(h.ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RunnerProfile.Standard.MemoryMB != 8192 {
		t.Fatalf("the stored host does not carry the profile: %+v", stored.RunnerProfile)
	}

	// Every field is independent: a capacity alone leaves the profile, and an
	// unprofiled request cannot clear it by omission.
	other := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie,
		body: map[string]any{"capacity": 6}})
	other.mustStatus(t, http.StatusOK, "set capacity alone")
	var after hostResponse
	other.into(t, &after)
	if after.RunnerProfile == nil || after.RunnerProfile.Standard.CPUs != 3 || after.Capacity != 6 {
		t.Errorf("changing the capacity changed the profile: %+v", after.RunnerProfile)
	}

	// An empty object clears it, and the host follows the fleet again.
	clear := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie,
		body: map[string]any{"runner_profile": map[string]any{}}})
	clear.mustStatus(t, http.StatusOK, "clear the runner profile")
	var cleared hostResponse
	clear.into(t, &cleared)
	if cleared.RunnerProfile != nil || cleared.Slots != 6 || cleared.EffectiveProfile.Standard.CPUsSource != "global" {
		t.Errorf("an empty profile did not clear it: %+v, %d slots, standard from %q",
			cleared.RunnerProfile, cleared.Slots, cleared.EffectiveProfile.Standard.CPUsSource)
	}

	// A viewer may read the profile and may not write it.
	_, viewer := h.user("viewer", store.RoleViewer)
	h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: viewer,
		body: map[string]any{"runner_profile": map[string]any{"standard": map[string]any{"cpus": 2}}}}).
		mustStatus(t, http.StatusForbidden, "a viewer writing a profile")
}

// A profile the machine could never honour, or that contradicts itself, is
// refused with the field and the reason, and nothing is written.
func TestARunnerProfileTheHostCannotHonourIsRefused(t *testing.T) {
	h := newHarness(t)
	measured := h.measuredHost("big")
	silent := h.host("old") // an agent too old to say what machine it is
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	patch := func(host *store.Host, profile map[string]any) *response {
		return h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie,
			body: map[string]any{"runner_profile": profile}})
	}
	for _, tc := range []struct {
		name    string
		profile map[string]any
		field   string
		reason  string
	}{
		{"a negative size", map[string]any{"standard": map[string]any{"cpus": -1}}, "runner_profile.standard.cpus", "cannot be negative"},
		{"a runner too small to run", map[string]any{"standard": map[string]any{"cpus": 0.1}}, "runner_profile.standard.cpus", "a quarter of a core"},
		{"memory too small to run", map[string]any{"minimum": map[string]any{"memory_mb": 100}}, "runner_profile.minimum.memory_mb", "512 MB"},
		{"a minimum above the standard", map[string]any{"minimum": map[string]any{"cpus": 4}, "standard": map[string]any{"cpus": 2}}, "runner_profile.minimum.cpus", "is above the standard size"},
		{"a memory minimum above the standard", map[string]any{"minimum": map[string]any{"memory_mb": 8192}, "standard": map[string]any{"memory_mb": 4096}}, "runner_profile.minimum.memory_mb", "is above the standard size"},
		{"a ceiling below the standard", map[string]any{"standard": map[string]any{"cpus": 4, "burst_max_cpus": 2}}, "runner_profile.standard.burst_max_cpus", "at least the standard"},
		{"a standard larger than the machine", map[string]any{"standard": map[string]any{"cpus": 20}}, "runner_profile.standard.cpus", "could never run a single runner here"},
		{"a memory standard larger than the machine", map[string]any{"standard": map[string]any{"memory_mb": 65536}}, "runner_profile.standard.memory_mb", "could never run a single runner here"},
		{"a minimum larger than the machine", map[string]any{"minimum": map[string]any{"cpus": 12}}, "runner_profile.minimum.cpus", "more than it can give a runner"},
		// The host's say over in-memory folders: a ceiling below the floor leaves no
		// folder a checkout fits in, and a host that keeps them off has nothing to
		// cap, so saying both is a contradiction rather than caution.
		{"a negative folder ceiling", map[string]any{"tmpfs": map[string]any{"max_mb": -1}}, "runner_profile.tmpfs.max_mb", "cannot be negative"},
		{"a folder ceiling below the floor", map[string]any{"tmpfs": map[string]any{"max_mb": 32}}, "runner_profile.tmpfs.max_mb", "below 64 MB"},
		{"folders off and capped together", map[string]any{"tmpfs": map[string]any{"disabled": true, "max_mb": 1024}}, "runner_profile.tmpfs.max_mb", "no folder to put a ceiling on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := patch(measured, tc.profile)
			resp.mustStatus(t, http.StatusUnprocessableEntity, tc.name)
			if body := string(resp.body); !strings.Contains(body, tc.field) || !strings.Contains(body, tc.reason) {
				t.Fatalf("the refusal does not name %s and say %q: %s", tc.field, tc.reason, body)
			}
		})
	}
	if stored, _ := h.st.GetHost(h.ctx, measured.ID); stored.RunnerProfile.Set() {
		t.Fatalf("a refused profile was written anyway: %+v", stored.RunnerProfile)
	}

	// A host that has measured nothing has nothing to compare a size with, so a
	// sane profile is saved and used once it reports.
	patch(silent, map[string]any{"standard": map[string]any{"cpus": 64}}).mustStatus(t, http.StatusOK, "a profile on an unmeasured host")
}

// A host's profile and a pool's size are two halves of one sentence, edited on
// different pages. An edit that takes away the last host a pool could run on is
// refused, with the limit named, until the operator says they meant it.
func TestAProfileThatWouldStrandAPoolIsRefusedUnlessConfirmed(t *testing.T) {
	h := newHarness(t)
	host := h.measuredHost("only")
	pool := h.pool(h.installation(), "small")
	pool.Resources = store.Resources{CPUs: 2, MemoryMB: 2048}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	body := map[string]any{"runner_profile": map[string]any{"minimum": map[string]any{"cpus": 3}}}

	refused := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie, body: body})
	refused.mustStatus(t, http.StatusConflict, "a minimum above the only pool's size")
	for _, want := range []string{"minimum runner size is 3 CPU", "above the 2 CPU"} {
		if !strings.Contains(string(refused.body), want) {
			t.Errorf("the refusal does not say %q: %s", want, refused.body)
		}
	}
	if stored, _ := h.st.GetHost(h.ctx, host.ID); stored.RunnerProfile.Set() {
		t.Fatalf("a refused edit was written: %+v", stored.RunnerProfile)
	}

	h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID + "?confirm=true", cookie: cookie, body: body}).
		mustStatus(t, http.StatusOK, "the same edit, confirmed")
	if stored, _ := h.st.GetHost(h.ctx, host.ID); stored.RunnerProfile.Minimum.CPUs != 3 {
		t.Fatalf("a confirmed edit was not written: %+v", stored.RunnerProfile)
	}
}

// A throttle was decided against the sizes the host had; answering it with a
// different size is the operator responding, so the rung is lifted with it.
func TestChangingAHostsRunnerProfileLiftsItsThrottle(t *testing.T) {
	h := newHarness(t)
	host := h.measuredHost("busy")
	if err := h.st.SetHostThrottle(h.ctx, host.ID, store.HostThrottle{Level: 1, Reason: "CPU stayed above 85%"}, 0); err != nil {
		t.Fatal(err)
	}
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	profile := map[string]any{"runner_profile": map[string]any{"standard": map[string]any{"cpus": 3}}}

	resp := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie, body: profile})
	resp.mustStatus(t, http.StatusOK, "a new profile on a throttled host")
	var view hostResponse
	resp.into(t, &view)
	if view.Throttle != nil {
		t.Fatalf("the throttle stood after the profile changed: %+v", view.Throttle)
	}

	// Repeating the same profile is not an answer to anything.
	if err := h.st.SetHostThrottle(h.ctx, host.ID, store.HostThrottle{Level: 1, Reason: "again"}, 0); err != nil {
		t.Fatal(err)
	}
	again := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie, body: profile})
	again.mustStatus(t, http.StatusOK, "the same profile again")
	var same hostResponse
	again.into(t, &same)
	if same.Throttle == nil {
		t.Fatal("repeating the profile the host already had lifted the throttle")
	}
}

// A pool takes its size from the host or states one. Both at once is a setting
// that looks like it does something and does not.
func TestAPoolCanTakeItsSizeFromItsHostAndNotAlsoStateOne(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	create := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: map[string]any{
		"name": "sized-by-host", "installation_id": inst.ID, "labels": []string{"sized"},
		"backend": "docker", "image": "ghcr.io/eyupio/zoomies-runner:test", "max_runners": 3,
		"size_from_profile": true,
	}})
	create.mustStatus(t, http.StatusCreated, "create a pool sized by its host")
	var pool struct {
		ID              string `json:"id"`
		Sizing          string `json:"sizing"`
		SizeFromProfile bool   `json:"size_from_profile"`
		FleetStandard   *struct {
			CPUs     float64 `json:"cpus"`
			MemoryMB int64   `json:"memory_mb"`
		} `json:"fleet_standard"`
	}
	create.into(t, &pool)
	if pool.Sizing != "profile" || !pool.SizeFromProfile {
		t.Fatalf("the pool reads as %q, from profile %v", pool.Sizing, pool.SizeFromProfile)
	}
	if pool.FleetStandard == nil || pool.FleetStandard.CPUs != 2 || pool.FleetStandard.MemoryMB != 4096 {
		t.Fatalf("the pool does not say what a host with no standard gives it: %+v", pool.FleetStandard)
	}

	both := h.do(request{method: http.MethodPatch, path: "/api/v1/pools/" + pool.ID, cookie: cookie,
		body: map[string]any{"resources": map[string]any{"cpus": 2, "memory_mb": 2048}}})
	both.mustStatus(t, http.StatusUnprocessableEntity, "a size on a pool that takes it from the host")
	if body := string(both.body); !strings.Contains(body, "size_from_profile") || !strings.Contains(body, "cannot also state one") {
		t.Errorf("the refusal does not say what conflicts: %s", body)
	}

	// Moving to a stated size takes both halves of the change at once -- and,
	// as for any pool, the elasticity a new pool is given is for a size the host
	// decides, so a stated size turns it off.
	h.do(request{method: http.MethodPatch, path: "/api/v1/pools/" + pool.ID, cookie: cookie,
		body: map[string]any{"size_from_profile": false, "cpu_burst": map[string]any{"mode": "off"},
			"resources": map[string]any{"cpus": 2, "memory_mb": 2048}}}).
		mustStatus(t, http.StatusOK, "move the pool to a stated size")
	got, err := h.st.GetPool(h.ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SizeFromProfile || got.Resources.CPUs != 2 {
		t.Fatalf("the pool after the move = from profile %v, %+v", got.SizeFromProfile, got.Resources)
	}
}

// A pool exported with its size taken from the host and imported without that
// would be sized by a slot's share instead -- a different size on every
// machine the other instance has.
func TestAProfilePoolSurvivesAnExportAndImport(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "sized")
	pool.SizeFromProfile = true
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)

	export := h.do(request{method: http.MethodGet, path: "/api/v1/pools/export", cookie: cookie})
	export.mustStatus(t, http.StatusOK, "export")
	if !strings.Contains(string(export.body), `"size_from_profile": true`) {
		t.Fatalf("the export does not carry the choice: %s", export.body)
	}

	// The document planned against an instance where the pool is sized the
	// ordinary way says the field would change.
	pool.SizeFromProfile = false
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	plan := h.do(request{method: http.MethodPost, path: "/api/v1/pools/import", cookie: cookie,
		body: map[string]any{"document": string(export.body), "dry_run": true}})
	plan.mustStatus(t, http.StatusOK, "plan the import")
	var out struct {
		Changes []struct {
			Pool   string `json:"pool"`
			Action string `json:"action"`
			Fields []struct {
				Field    string `json:"field"`
				Incoming any    `json:"incoming"`
			} `json:"fields"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(plan.body, &out); err != nil {
		t.Fatalf("%v: %s", err, plan.body)
	}
	found := false
	for _, c := range out.Changes {
		for _, f := range c.Fields {
			found = found || (f.Field == "size_from_profile" && f.Incoming == true)
		}
	}
	if !found {
		t.Fatalf("the plan does not move size_from_profile: %s", plan.body)
	}
}

// Jobs can be grouped by the size of the runner they ran on, and listed by it,
// in the words an operator would say it.
func TestJobStatsGroupBySizeAndTheSizeFilterOpensARow(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "linux")
	_, cookie := h.user("viewer", store.RoleViewer)
	now := time.Now()
	big := h.releaseJobs(pool, "", 3, 1_000, now.Add(-2*24*time.Hour))
	small := h.releaseJobs(pool, "", 2, 2_000, now.Add(-2*24*time.Hour))
	h.releaseJobs(pool, "", 1, 3_000, now.Add(-2*24*time.Hour)) // never recorded
	for _, j := range big {
		if _, err := h.st.StampJobGranted(h.ctx, j.ID, 3, 8192, store.AllocationFromProfile); err != nil {
			t.Fatal(err)
		}
	}
	for _, j := range small {
		if _, err := h.st.StampJobGranted(h.ctx, j.ID, 1.5, 4096, store.AllocationFromProfile); err != nil {
			t.Fatal(err)
		}
	}

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/jobs/stats?group_by=size", cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "job statistics by size")
	var got struct {
		Groups []struct {
			Keys  map[string]string `json:"keys"`
			Count int               `json:"count"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("%v: %s", err, resp.body)
	}
	counts := map[string]int{}
	for _, g := range got.Groups {
		counts[g.Keys["size"]] = g.Count
	}
	if counts["3 CPU / 8 GB"] != 3 || counts["1.5 CPU / 4 GB"] != 2 || counts["unknown"] != 1 {
		t.Fatalf("groups = %v, want 3 on 3 CPU / 8 GB, 2 on 1.5 CPU / 4 GB and 1 unknown", counts)
	}

	list := h.do(request{method: http.MethodGet, path: "/api/v1/jobs?size=" + url.QueryEscape("1.5 CPU / 4 GB"), cookie: cookie})
	list.mustStatus(t, http.StatusOK, "jobs of one size")
	var listed struct {
		Items []struct {
			GrantedCPUs     float64 `json:"granted_cpus"`
			GrantedMemoryMB int64   `json:"granted_memory_mb"`
			GrantedSource   string  `json:"granted_source"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(list.body, &listed); err != nil {
		t.Fatalf("%v: %s", err, list.body)
	}
	if listed.Total != 2 || len(listed.Items) != 2 || listed.Items[0].GrantedCPUs != 1.5 || listed.Items[0].GrantedSource != "profile" {
		t.Fatalf("the size filter returned %+v, want the two small jobs with their size on them", listed)
	}
	unknown := h.do(request{method: http.MethodGet, path: "/api/v1/jobs?size=unknown", cookie: cookie})
	unknown.mustStatus(t, http.StatusOK, "jobs with no recorded size")
	var unlisted struct{ Total int }
	if err := json.Unmarshal(unknown.body, &unlisted); err != nil || unlisted.Total != 1 {
		t.Fatalf("size=unknown listed %d jobs (%v), want the one never recorded", unlisted.Total, err)
	}
}

// A host's say over pools' in-memory folders is stored with its runner profile,
// comes back in the host's view, and is left alone by an edit that names only a
// capacity -- the policy is the operator's, and a heartbeat never writes it.
func TestAnOperatorCanKeepInMemoryFoldersOffAHostOrCapThem(t *testing.T) {
	h := newHarness(t)
	host := h.measuredHost("tight")
	operator, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(operator)
	patch := func(body map[string]any) hostResponse {
		t.Helper()
		resp := h.do(request{method: http.MethodPatch, path: "/api/v1/hosts/" + host.ID, cookie: cookie, body: body})
		resp.mustStatus(t, http.StatusOK, "patch")
		var view hostResponse
		resp.into(t, &view)
		return view
	}

	view := patch(map[string]any{"runner_profile": map[string]any{"tmpfs": map[string]any{"max_mb": 2048}}})
	if view.RunnerProfile == nil || view.RunnerProfile.Tmpfs != (store.HostTmpfs{MaxMB: 2048}) {
		t.Fatalf("the response does not carry the ceiling: %+v", view.RunnerProfile)
	}
	if stored, _ := h.st.GetHost(h.ctx, host.ID); stored.RunnerProfile.Tmpfs.MaxMB != 2048 {
		t.Fatalf("the stored host does not carry the ceiling: %+v", stored.RunnerProfile)
	}

	// A capacity alone leaves the policy where it was.
	patch(map[string]any{"capacity": 3})
	if stored, _ := h.st.GetHost(h.ctx, host.ID); stored.RunnerProfile.Tmpfs.MaxMB != 2048 {
		t.Fatalf("an unrelated edit changed the policy: %+v", stored.RunnerProfile)
	}

	// Replacing the profile replaces the policy too, which is how it is turned
	// off here and how it is handed back.
	patch(map[string]any{"runner_profile": map[string]any{"tmpfs": map[string]any{"disabled": true}}})
	if stored, _ := h.st.GetHost(h.ctx, host.ID); !stored.RunnerProfile.Tmpfs.Disabled || stored.RunnerProfile.Tmpfs.MaxMB != 0 {
		t.Fatalf("the host did not keep the folders off: %+v", stored.RunnerProfile)
	}
	patch(map[string]any{"runner_profile": map[string]any{}})
	if stored, _ := h.st.GetHost(h.ctx, host.ID); stored.RunnerProfile.Set() {
		t.Fatalf("clearing the profile left a policy behind: %+v", stored.RunnerProfile)
	}
}
