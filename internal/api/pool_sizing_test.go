package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// A pool that names no size leaves it to the host each runner lands on.
//
// That is the sizing most fleets want and the one this API now makes
// reachable: the scheduler charges such a runner one slot's share of the
// machine it is placed on, and gives it exactly that share as a real cgroup
// limit, so the books and the cgroups say the same thing. The API used to
// overwrite the absent figures with the fleet's default instead, which froze
// every pool on a number chosen before any of its hosts existed.
func TestAPoolThatNamesNoSizeIsSizedByItsHost(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: poolBody(inst.ID)})
	res.mustStatus(t, http.StatusCreated, "create")
	var created controller.PoolView
	res.into(t, &created)

	if created.Resources.CPUs != 0 || created.Resources.MemoryMB != 0 {
		t.Errorf("resources = %+v, want none: the host decides", created.Resources)
	}
	if created.Sizing != controller.SizingAutomatic {
		t.Errorf("sizing = %q, want %q", created.Sizing, controller.SizingAutomatic)
	}
	if created.CPUBurst.Mode != store.CPUBurstObserve {
		t.Errorf("new automatic pool CPU burst mode = %q, want safe observe-only rollout", created.CPUBurst.Mode)
	}

	// And it is still automatic after the round trip through the store, which
	// is what the pool page reads.
	stored, err := h.st.GetPool(h.ctx, created.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if !stored.Automatic() {
		t.Errorf("the stored pool is not automatic: %+v", stored.Resources)
	}
}

// A size somebody typed is kept exactly, because "fixed" is the other half of
// the choice and a pool that asks for eight cores on every host means it.
func TestASizeSomebodyTypedIsKept(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	body := poolBody(inst.ID)
	body["resources"] = map[string]any{"cpus": 8, "memory_mb": 16384}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusCreated, "create")
	var created controller.PoolView
	res.into(t, &created)
	if created.Resources.CPUs != 8 || created.Resources.MemoryMB != 16384 {
		t.Errorf("resources = %+v, want the 8 CPU and 16 GB the request asked for", created.Resources)
	}
	if created.Sizing != controller.SizingFixed {
		t.Errorf("sizing = %q, want %q", created.Sizing, controller.SizingFixed)
	}
	if created.CPUBurst.Mode != store.CPUBurstOff {
		t.Errorf("fixed pool CPU burst mode = %q, want off", created.CPUBurst.Mode)
	}
}

func TestAnAutomaticPoolCanOptIntoElasticCPU(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	body := poolBody(inst.ID)
	body["cpu_burst"] = map[string]any{"mode": "automatic", "max_cpus": 6}

	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: h.session(u), body: body})
	res.mustStatus(t, http.StatusCreated, "create")
	var created controller.PoolView
	res.into(t, &created)
	if created.Sizing != controller.SizingElastic || created.CPUBurst.Mode != store.CPUBurstAutomatic || created.CPUBurst.MaxCPUs != 6 {
		t.Fatalf("created pool = %+v, want elastic sizing with a 6 CPU ceiling", created)
	}
}

func TestAProcessPoolKeepsElasticCPUOff(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	body := poolBody(inst.ID)
	body["backend"] = "process"

	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: h.session(u), body: body})
	res.mustStatus(t, http.StatusCreated, "create")
	var created controller.PoolView
	res.into(t, &created)
	if created.CPUBurst.Mode != store.CPUBurstOff {
		t.Fatalf("process pool CPU burst mode = %q, want off", created.CPUBurst.Mode)
	}

	body["cpu_burst"] = map[string]any{"mode": "observe"}
	res = h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: h.session(u), body: body})
	res.mustStatus(t, http.StatusUnprocessableEntity, "create")
	var env errorEnvelope
	res.into(t, &env)
	for _, field := range env.Errors {
		if field.Field == "cpu_burst.mode" {
			return
		}
	}
	t.Fatalf("process pool elasticity error did not name cpu_burst.mode: %s", res.body)
}

// The fleet's own figures are where a size form opens, and they are no longer
// what an unspecified pool becomes.
//
// The two are separate questions and the defaults endpoint answers both: a
// wizard offering a fixed size should open on the fleet's answer rather than a
// constant of its own, and a pool that names nothing should stay automatic.
func TestTheFleetsOwnSizeIsWhatASizeFormOpensOn(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Runners.DefaultCPUs = 4
		c.Runners.DefaultMemoryMB = 8192
	})
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	defaults := h.do(request{method: http.MethodGet, path: "/api/v1/pools/defaults", cookie: cookie})
	defaults.mustStatus(t, http.StatusOK, "defaults")
	var shown poolDefaultsResponse
	defaults.into(t, &shown)
	if shown.SuggestedResources.CPUs != 4 || shown.SuggestedResources.MemoryMB != 8192 {
		t.Fatalf("suggested = %+v, want the fleet's 4 CPU and 8192 MB", shown.SuggestedResources)
	}
	if shown.Resources.CPUs != 0 || shown.Resources.MemoryMB != 0 {
		t.Errorf("a new pool's resources = %+v, want none: the host decides", shown.Resources)
	}

	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: poolBody(inst.ID)})
	res.mustStatus(t, http.StatusCreated, "create")
	var created controller.PoolView
	res.into(t, &created)
	if created.Sizing != controller.SizingAutomatic {
		t.Errorf("a pool created with no size is %q, want automatic even where the fleet names a default", created.Sizing)
	}
}

// A size below what a runner needs to be a runner is refused, because the job
// it fails is failed in a way that looks like a broken image rather than like a
// limit somebody typed.
func TestASizeTooSmallToRunAJobIsRefused(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	for _, tc := range []struct {
		name  string
		size  map[string]any
		field string
	}{
		{"a tenth of a core", map[string]any{"cpus": 0.1, "memory_mb": 4096}, "resources.cpus"},
		{"256 MB", map[string]any{"cpus": 2, "memory_mb": 256}, "resources.memory_mb"},
		// A minimum is a floor under the size: it cannot sit above a size the
		// pool states, and is held to what any runner needs -- including under
		// a size left to the host.
		{"a minimum above the standard", map[string]any{"cpus": 2, "memory_mb": 4096, "min_memory_mb": 8192}, "resources.min_memory_mb"},
		{"a minimum below a runner", map[string]any{"cpus": 2, "memory_mb": 4096, "min_memory_mb": 256}, "resources.min_memory_mb"},
		{"an automatic minimum below a runner", map[string]any{"min_cpus": 0.1}, "resources.min_cpus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := poolBody(inst.ID)
			body["resources"] = tc.size
			res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: body})
			res.mustStatus(t, http.StatusUnprocessableEntity, "create")
			var env errorEnvelope
			res.into(t, &env)
			named := false
			for _, fe := range env.Errors {
				if fe.Field == tc.field {
					named = true
				}
			}
			if !named {
				t.Errorf("no error on %s: %s", tc.field, res.body)
			}
		})
	}
}

// The room is what a maximum is worth comparing against, and a maximum above it
// is runners the scheduler will never create with nothing else saying so.
func TestTheRoomIsCountedFromTheMachinesAndTheSize(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	// Ten cores and 20 GB, set to eight slots. Half a core and 512 MB are held
	// back by the floors, so the machine has room for four 2-core, 4 GB
	// runners -- and four of its slots are promises it cannot keep.
	host := &store.Host{
		Name: "big-1", Capacity: 8,
		CPUs: 10, MemoryMB: 20480,
		Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", LastHeartbeat: time.Now(),
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	body := poolBody(inst.ID)
	body["max_runners"] = 8
	// A fixed size, because this is the question a fixed size raises: the same
	// figure on every host, against machines that are not all the same.
	body["resources"] = map[string]any{"cpus": 2, "memory_mb": 4096}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusOK, "validate")
	var verdict validatePoolResponse
	res.into(t, &verdict)

	if verdict.Room.Runners != 4 {
		t.Errorf("room = %d runners, want 4 on ten cores and 20 GB at 2 CPU and 4 GB each", verdict.Room.Runners)
	}
	if len(verdict.Room.Hosts) != 1 {
		t.Fatalf("room lists %d hosts, want 1", len(verdict.Room.Hosts))
	}
	only := verdict.Room.Hosts[0]
	if only.Slots != 8 || only.Fits != 4 || only.Room != 4 {
		t.Errorf("host room = %+v, want 8 slots, 4 fits, 4 room", only)
	}
	if only.LimitedBy != "cpu" {
		t.Errorf("limited_by = %q, want cpu -- the machine runs out before the slots do", only.LimitedBy)
	}
	if !hasWarning(verdict.Warnings, "pool.max_above_room") {
		t.Errorf("a maximum of 8 on a fleet with room for 4 raised no warning: %+v", verdict.Warnings)
	}
	if !hasWarning(verdict.Warnings, "pool.host_overcommitted") {
		t.Errorf("a host promising 8 slots with room for 4 raised no warning: %+v", verdict.Warnings)
	}
}

// An elastic pool on a host whose agent cannot lend CPU is a pool that reads
// automatic and behaves as off there. The dry run names the host, because the
// review step is where the pool is saved and the metric that counts each
// decision as unsupported_agent is not.
func TestAnElasticPoolIsWarnedAboutHostsThatCannotLendCPU(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	old := &store.Host{
		Name: "old-1", Capacity: 2, CPUs: 8, MemoryMB: 16384,
		Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", LastHeartbeat: time.Now(),
	}
	if err := h.st.CreateHost(h.ctx, old); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	body := poolBody(inst.ID)
	body["cpu_burst"] = map[string]any{"mode": "automatic"}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusOK, "validate")
	var verdict validatePoolResponse
	res.into(t, &verdict)
	if !hasWarning(verdict.Warnings, "pool.elastic_cpu_unsupported") {
		t.Errorf("an elastic pool on an agent that cannot lend CPU raised no warning: %+v", verdict.Warnings)
	}
	if len(verdict.Room.Hosts) != 1 || verdict.Room.Hosts[0].ElasticCPU {
		t.Errorf("room = %+v, want the host marked as unable to lend CPU", verdict.Room.Hosts)
	}

	// The agent says it can, and the warning is gone with nothing else changed.
	old.Features = store.StringSlice{agent.FeatureElasticCPU}
	if err := h.st.SetHostReported(h.ctx, old); err != nil {
		t.Fatalf("SetHostReported: %v", err)
	}
	res = h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusOK, "validate")
	res.into(t, &verdict)
	if hasWarning(verdict.Warnings, "pool.elastic_cpu_unsupported") {
		t.Errorf("a capable agent was still warned about: %+v", verdict.Warnings)
	}
	if len(verdict.Room.Hosts) != 1 || !verdict.Room.Hosts[0].ElasticCPU {
		t.Errorf("room = %+v, want the host marked as able to lend CPU", verdict.Room.Hosts)
	}
}

// A maximum the fleet can actually place says nothing, because a warning an
// operator sees on every correct pool is one they stop reading.
func TestAMaximumTheFleetCanPlaceRaisesNothing(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	host := &store.Host{
		Name: "big-1", Capacity: 4,
		CPUs: 10, MemoryMB: 20480,
		Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		// A current agent: a new pool watches memory by default, and a host that
		// cannot is the case the warning is for, not this one.
		Features: store.StringSlice{agent.FeatureElasticMemory},
		OS:       "linux", Arch: "amd64", LastHeartbeat: time.Now(),
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	body := poolBody(inst.ID)
	body["max_runners"] = 4
	body["resources"] = map[string]any{"cpus": 2, "memory_mb": 4096}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusOK, "validate")
	var verdict validatePoolResponse
	res.into(t, &verdict)
	if len(verdict.Warnings) != 0 {
		t.Errorf("a pool that fits its fleet warned about it: %+v", verdict.Warnings)
	}
	if verdict.Resources.CPUs != 2 || verdict.Sizing != controller.SizingFixed {
		t.Errorf("the verdict did not carry the size the pool would run at: %+v (%s)", verdict.Resources, verdict.Sizing)
	}
}

// A cache that may grow past the disk it lives on is not limited at all: the
// disk fills first, and a host below its disk reserve takes no runner of any
// pool.
func TestACacheLimitAboveTheDiskWarns(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	host := &store.Host{
		Name: "small-disk", Capacity: 2,
		CPUs: 8, MemoryMB: 16384, DiskTotalMB: 40960, DiskFreeMB: 10240,
		Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", LastHeartbeat: time.Now(),
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	body := poolBody(inst.ID)
	body["max_runners"] = 2
	body["cache"] = map[string]any{
		"enabled":    true,
		"scope":      "pool",
		"source":     "/var/lib/zoomies-cache",
		"size_limit": 64 * 1024 * 1024 * 1024,
	}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools/validate", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusOK, "validate")
	var verdict validatePoolResponse
	res.into(t, &verdict)
	if !hasWarning(verdict.Warnings, "pool.cache_above_disk") {
		t.Errorf("a 64 GB cache limit on a host with 10 GB free raised no warning: %+v", verdict.Warnings)
	}
}

// Editing a pool that names no size leaves the size where it is: with the
// host.
//
// The API used to size such a pool on its first edit, on the grounds that an
// unsized pool ran unlimited. It does not -- it runs at its host's share --
// so sizing it on the way past would take a working automatic pool and freeze
// it at a number nobody asked for, because somebody changed its priority.
func TestEditingAPoolWithNoSizeLeavesItToTheHost(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	legacy := &store.Pool{
		Name: "zoomies-legacy", InstallationID: inst.ID,
		Labels:  store.StringSlice{"self-hosted", "zoomies-legacy"},
		Backend: store.BackendDocker, PullPolicy: store.PullIfNotPresent,
		Image:      "ghcr.io/eyupio/zoomies-runner:test",
		MaxRunners: 2, IdleTimeout: store.Duration(5 * time.Minute),
		Ephemeral: true, DockerMode: store.DockerNone, Enabled: true,
	}
	if err := h.st.CreatePool(h.ctx, legacy); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}

	res := h.do(request{method: http.MethodPatch, path: "/api/v1/pools/" + legacy.ID,
		cookie: cookie, body: map[string]any{"priority": 1}})
	res.mustStatus(t, http.StatusOK, "patch")
	var updated controller.PoolView
	res.into(t, &updated)
	if updated.Resources.CPUs != 0 || updated.Resources.MemoryMB != 0 {
		t.Errorf("an edit sized a pool that had left the size to its host: %+v", updated.Resources)
	}
	if updated.Sizing != controller.SizingAutomatic {
		t.Errorf("sizing = %q, want automatic", updated.Sizing)
	}
}

func hasWarning(warnings []controller.Problem, code string) bool {
	for _, w := range warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// A minimum under a fixed size is kept, so a pool that asked to run on a host a
// little short of its size still does after an edit or a restart.
func TestAMinimumUnderTheSizeIsKept(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	body := poolBody(inst.ID)
	body["resources"] = map[string]any{"cpus": 8, "memory_mb": 32768, "min_cpus": 4, "min_memory_mb": 24576}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusCreated, "create")
	var got struct {
		Resources store.Resources `json:"resources"`
	}
	res.into(t, &got)
	if got.Resources.MinCPUs != 4 || got.Resources.MinMemoryMB != 24576 {
		t.Fatalf("resources = %+v, want the minimum kept", got.Resources)
	}
}

// An automatic pool's standard is a slot's share of whichever host it lands
// on, and a minimum is how it runs on a host with a free slot but less than a
// whole share left. It used to be refused for having no standard to sit under,
// which left such a pool waiting on a host with room for its job.
func TestAnAutomaticPoolKeepsItsMinimum(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	u, _ := h.user("operator", store.RoleOperator)
	cookie := h.session(u)

	body := poolBody(inst.ID)
	body["resources"] = map[string]any{"min_cpus": 1, "min_memory_mb": 2048}
	res := h.do(request{method: http.MethodPost, path: "/api/v1/pools", cookie: cookie, body: body})
	res.mustStatus(t, http.StatusCreated, "create")
	var got struct {
		Sizing    string          `json:"sizing"`
		Resources store.Resources `json:"resources"`
	}
	res.into(t, &got)
	if got.Resources.CPUs != 0 || got.Resources.MemoryMB != 0 {
		t.Fatalf("resources = %+v, want the size still left to the host", got.Resources)
	}
	if got.Resources.MinCPUs != 1 || got.Resources.MinMemoryMB != 2048 {
		t.Fatalf("resources = %+v, want the minimum kept", got.Resources)
	}
}
