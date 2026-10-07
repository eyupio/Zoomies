package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// A throttle scales a container's quota from what it was created with, and an
// agent that adopted the container after a restart has no memory of that. The
// container itself has to say, and both halves of a docker-in-docker pair
// have to say it, because each is throttled on its own.
func TestBothContainersOfARunnerCarryTheirLimitsAndWhereTheyCameFrom(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	// Each container carries what it was actually given, which under a slot's
	// share is its half of the pair: a label saying what the two have between
	// them would have the throttle scale one container by the other's quota.
	cases := []struct {
		name      string
		resources store.Resources
		source    string
		runner    [3]string // cpus, memory, where they came from
		daemon    [3]string
	}{
		// Typed limits say what the job may have, and the daemon runs the
		// build, so both are given them in full and the host is charged twice.
		{"the pool's own limits", store.Resources{CPUs: 2.5, MemoryMB: 4096}, store.AllocationFromPool,
			[3]string{"2.5", "4096", "pool"}, [3]string{"2.5", "4096", "pool"}},
		// One slot, split between the two halves of one runner.
		{"the host's default share", store.Resources{CPUs: 0.75, MemoryMB: 1900}, store.AllocationFromHost,
			[3]string{"0.375", "950", "host"}, [3]string{"0.375", "950", "host"}},
		// An odd figure leaves the remainder with the daemon, which is the
		// half that runs the build.
		{"a share that does not halve evenly", store.Resources{CPUs: 1, MemoryMB: 1901}, store.AllocationFromHost,
			[3]string{"0.5", "950", "host"}, [3]string{"0.5", "951", "host"}},
		// A controller that predates the source field still sets limits, and
		// the only limits it knows are the pool's.
		{"a limit with no source named", store.Resources{CPUs: 1}, "",
			[3]string{"1", "", "pool"}, [3]string{"1", "", "pool"}},
		{"a memory limit alone", store.Resources{MemoryMB: 1024}, store.AllocationFromHost,
			[3]string{"", "512", "host"}, [3]string{"", "512", "host"}},
		{"no limits at all", store.Resources{}, store.AllocationFromHost,
			[3]string{"", "", ""}, [3]string{"", "", ""}},
	}
	for _, c := range cases {
		spec := jitSpec()
		spec.Resources = c.resources
		spec.ResourcesSource = c.source
		spec.DockerMode = store.DockerDinD
		for _, cfg := range []struct {
			what   string
			labels map[string]string
			want   [3]string
		}{
			{"runner", buildRunnerConfig(spec, dockerFlavor(), containerOptions{Now: now}).Labels, c.runner},
			{"sidecar", buildDinDConfig(spec, dockerFlavor(), containerOptions{Now: now, DinDImage: DefaultDinDImage}).Labels, c.daemon},
		} {
			got := [3]string{cfg.labels[LabelCPUs], cfg.labels[LabelMemoryMB], cfg.labels[LabelLimitsFrom]}
			if got != cfg.want {
				t.Errorf("%s, %s: labels cpus/memory/from = %q, want %q", c.name, cfg.what, got, cfg.want)
			}
		}
	}
}

// The listing is where an adopted runner's base allocation comes from, so a
// label that is missing or mangled must read as "no limit" rather than break
// the listing: the worst outcome of a bad label is a runner the throttle
// leaves alone.
func TestListReadsARunnersLimitsBackFromItsLabels(t *testing.T) {
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{
				{ID: "c1", State: "running", Labels: map[string]string{
					LabelRole: roleRunner, LabelName: "runner-1", LabelRunnerID: "run_1",
					LabelCPUs: "1.5", LabelMemoryMB: "2048", LabelLimitsFrom: "host",
				}},
				{ID: "c2", State: "running", Labels: map[string]string{
					LabelRole: roleRunner, LabelName: "runner-2", LabelRunnerID: "run_2",
					LabelCPUs: "lots", LabelMemoryMB: "-3",
				}},
				{ID: "c3", State: "running", Labels: map[string]string{
					LabelRole: roleRunner, LabelName: "runner-3", LabelRunnerID: "run_3",
				}},
			})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	got, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[Handle]store.Resources{}
	for _, w := range got {
		byID[w.Handle] = w.Resources
	}
	if r := byID["c1"]; r.CPUs != 1.5 || r.MemoryMB != 2048 {
		t.Fatalf("c1 resources = %+v, want 1.5 CPUs and 2048 MB from its labels", r)
	}
	if r := byID["c2"]; r != (store.Resources{}) {
		t.Fatalf("c2 resources = %+v, want nothing from labels that do not parse", r)
	}
	if r := byID["c3"]; r != (store.Resources{}) {
		t.Fatalf("c3 resources = %+v, want nothing from a container with no labels", r)
	}
}

// updateEngine is a fake daemon holding one runner and, optionally, its
// sidecar, that records every quota update it is sent.
type updateEngine struct {
	*fakeEngine
	mu      sync.Mutex
	updates map[string][]map[string]any
	// cpu is each container's CPU use in hundredths of a core, served as a
	// stats sample; a container without one answers 404, as if unsampled.
	cpu map[string]uint64
}

func newUpdateEngine(t *testing.T, runnerNanos, sidecarNanos int64, withSidecar bool) *updateEngine {
	t.Helper()
	return newHalvedUpdateEngine(t, runnerNanos, sidecarNanos, withSidecar, 0)
}

// newHalvedUpdateEngine is newUpdateEngine with each container labelled as
// created with half CPUs, which is what a boost of the pair is split by.
func newHalvedUpdateEngine(t *testing.T, runnerNanos, sidecarNanos int64, withSidecar bool, half float64) *updateEngine {
	t.Helper()
	u := &updateEngine{updates: map[string][]map[string]any{}, cpu: map[string]uint64{}}
	inspect := func(id string, nanos int64, labels map[string]string) *ContainerInspect {
		return &ContainerInspect{
			ID:         id,
			State:      &ContainerState{Status: "running", Running: true},
			Config:     &ContainerConfig{Labels: labels},
			HostConfig: &HostConfig{NanoCPUs: nanos},
		}
	}
	runnerLabels := map[string]string{LabelRole: roleRunner, LabelName: "runner-1", LabelRunnerID: "run_1"}
	sidecarLabels := map[string]string{LabelRole: roleDinD, LabelName: "runner-1-dind", LabelDinDFor: "runner-1", LabelRunnerID: "run_1"}
	if half > 0 {
		runnerLabels[LabelCPUs] = strconv.FormatFloat(half, 'f', -1, 64)
		sidecarLabels[LabelCPUs] = strconv.FormatFloat(half, 'f', -1, 64)
	}
	u.fakeEngine = newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/{id}/json": func(w http.ResponseWriter, r *http.Request) {
			switch r.PathValue("id") {
			case "c1":
				writeJSON(w, 200, inspect("c1", runnerNanos, runnerLabels))
			case "d1":
				if withSidecar {
					writeJSON(w, 200, inspect("d1", sidecarNanos, sidecarLabels))
					return
				}
				fallthrough
			default:
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such container"})
			}
		},
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			// The sidecar is found by the runner's name, and by nothing looser:
			// a filter on the managed label alone would throttle every sidecar
			// on the host for one runner's sake.
			if !strings.Contains(r.Form.Get("filters"), LabelDinDFor+"=runner-1") {
				t.Errorf("sidecar listing did not filter on the runner's name: %s", r.Form.Get("filters"))
			}
			if !withSidecar {
				writeJSON(w, 200, []ContainerSummary{})
				return
			}
			writeJSON(w, 200, []ContainerSummary{{ID: "d1", State: "running", Labels: sidecarLabels}})
		},
		"GET " + v + "/containers/{id}/stats": func(w http.ResponseWriter, r *http.Request) {
			u.mu.Lock()
			total, ok := u.cpu[r.PathValue("id")]
			u.mu.Unlock()
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"message": "no stats"})
				return
			}
			writeJSON(w, 200, map[string]any{"cpu_stats": map[string]any{
				"cpu_usage": map[string]any{"total_usage": total}, "system_cpu_usage": 100, "online_cpus": 1,
			}})
		},
		"POST " + v + "/containers/{id}/update": func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			u.mu.Lock()
			u.updates[r.PathValue("id")] = append(u.updates[r.PathValue("id")], body)
			u.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"Warnings": []string{}})
		},
	})
	return u
}

func (u *updateEngine) sent(id string) []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.updates[id]
}

// The agent sends the same figure on every beat and keeps no durable record
// of what it last sent, so a container whose quota already matches must cost
// no request; and when it does not match, the request must carry the quota
// and nothing else, because a zero memory field in that body would be read by
// the daemon as "remove the memory limit".
func TestUpdateResourcesMovesTheCPUQuotaOnlyWhenItDiffersAndSendsNothingElse(t *testing.T) {
	cases := []struct {
		name        string
		current     int64
		cpus        float64
		wantUpdates int
		wantNanos   float64
	}{
		{"already at the quota", 1_000_000_000, 1, 0, 0},
		{"throttled to half", 2_000_000_000, 1, 1, 1_000_000_000},
		{"restored to the base", 1_000_000_000, 2, 1, 2_000_000_000},
		{"a share in hundredths", 750_000_000, 0.38, 1, 380_000_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := newUpdateEngine(t, c.current, 0, false)
			b := dockerBackendFor(t, u.fakeEngine, DockerOptions{})
			err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: c.cpus, MemoryMB: 4096, PidsLimit: 512})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			got := u.sent("c1")
			if len(got) != c.wantUpdates {
				t.Fatalf("updates sent = %d (%v), want %d", len(got), got, c.wantUpdates)
			}
			for _, body := range got {
				if body["NanoCpus"] != c.wantNanos {
					t.Fatalf("NanoCpus = %v, want %v", body["NanoCpus"], c.wantNanos)
				}
				if _, ok := body["Memory"]; ok || len(body) != 1 {
					t.Fatalf("the update carried more than the CPU quota: %v", body)
				}
			}
		})
	}
}

// Under docker_mode dind the builds run in the sidecar, so a throttle that
// only reached the runner process would slow the wrong container. The sidecar
// gets the same quota, and the same "only when it differs" rule.
func TestUpdateResourcesThrottlesTheDinDSidecarWithItsRunner(t *testing.T) {
	u := newUpdateEngine(t, 2_000_000_000, 2_000_000_000, true)
	b := dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: 1}); err != nil {
		t.Fatalf("update: %v", err)
	}
	for _, id := range []string{"c1", "d1"} {
		got := u.sent(id)
		if len(got) != 1 || got[0]["NanoCpus"] != float64(1_000_000_000) {
			t.Fatalf("%s updates = %v, want one update to 1 CPU", id, got)
		}
	}

	// The runner already throttled but the sidecar not: only the sidecar moves.
	u = newUpdateEngine(t, 1_000_000_000, 2_000_000_000, true)
	b = dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: 1}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := u.sent("c1"); len(got) != 0 {
		t.Fatalf("the runner was updated although its quota already matched: %v", got)
	}
	if got := u.sent("d1"); len(got) != 1 {
		t.Fatalf("sidecar updates = %v, want exactly one", got)
	}
}

// A runner that finished between the beat and the update is not a failure to
// throttle; the agent needs to tell that apart from a daemon that refused.
func TestUpdateResourcesReportsAGoneContainerAsNotFound(t *testing.T) {
	u := newUpdateEngine(t, 0, 0, false)
	b := dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	err := b.UpdateResources(context.Background(), "vanished", store.Resources{CPUs: 1})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A runner with no CPU limit has no quota to scale, and the update endpoint
// reads a zero as "leave it alone" rather than "remove the limit", so asking
// would be a request that means nothing.
func TestUpdateResourcesAsksNothingForARunnerWithNoCPULimit(t *testing.T) {
	u := newUpdateEngine(t, 0, 0, false)
	b := dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{MemoryMB: 4096}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if u.request(http.MethodPost, v+"/containers/c1/update") != nil || u.request(http.MethodGet, v+"/containers/c1/json") != nil {
		t.Fatal("the daemon was asked about a runner that has no quota to move")
	}
}

// An operator told to raise a pool field nobody set is sent to the wrong
// page. A limit that was the host's share moves with the pool's minimum, the
// host's capacity, or a limit of the pool's own, and the message has to say so.
func TestAnOOMKillSaysWhatToChangeForEachSourceOfTheLimit(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   []string
		unwant []string
	}{
		{"the pool's limit", map[string]string{LabelLimitsFrom: "pool"}, []string{"raise the pool's memory_mb"}, []string{"capacity"}},
		{"no source recorded", nil, []string{"raise the pool's memory_mb"}, []string{"capacity"}},
		{"the host's default share", map[string]string{LabelLimitsFrom: "host"},
			[]string{"host's share", "raise the pool's minimum memory", "memory_mb on the pool", "lower the host's capacity"}, []string{"raise the pool's memory_mb"}},
	}
	for _, c := range cases {
		st := statusFromInspect("c", &ContainerInspect{
			State:  &ContainerState{Status: "exited", ExitCode: 137, OOMKilled: true},
			Config: &ContainerConfig{Labels: c.labels},
		})
		if st.Phase != PhaseFailed {
			t.Errorf("%s: phase = %q, want failed", c.name, st.Phase)
		}
		for _, w := range c.want {
			if !strings.Contains(st.Message, w) {
				t.Errorf("%s: message %q does not say %q", c.name, st.Message, w)
			}
		}
		for _, w := range c.unwant {
			if strings.Contains(st.Message, w) {
				t.Errorf("%s: message %q should not say %q", c.name, st.Message, w)
			}
		}
	}
}

// A boost of a docker-in-docker pair is for the build, and the build runs in
// the daemon. Lent to both halves alike, the runner process was given cores it
// never used and the daemon only half the loan; the runner keeps its half and
// the sidecar is given the rest of the pair's target. The agent sends what it
// always has -- the runner's half times the factor -- so neither side of the
// protocol changed.
func TestUpdateResourcesLendsADinDBoostToTheSidecar(t *testing.T) {
	// Two halves of 2 CPUs, boosted 2x: the pair is to have 8.
	u := newHalvedUpdateEngine(t, 2_000_000_000, 2_000_000_000, true, 2)
	b := dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: 4}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := u.sent("c1"); len(got) != 0 {
		t.Fatalf("runner updates = %v, want none: it stays at its own half", got)
	}
	if got := u.sent("d1"); len(got) != 1 || got[0]["NanoCpus"] != float64(6_000_000_000) {
		t.Fatalf("sidecar updates = %v, want one to 6 CPUs, the pair's 8 less the runner's 2", got)
	}

	// Restored, both go back to their own halves.
	u = newHalvedUpdateEngine(t, 2_000_000_000, 6_000_000_000, true, 2)
	b = dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: 2}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := u.sent("d1"); len(got) != 1 || got[0]["NanoCpus"] != float64(2_000_000_000) {
		t.Fatalf("sidecar updates = %v, want one back to its 2 CPU half", got)
	}

	// A pair from a release that stamped no halves has nothing to split by,
	// and keeps the old behaviour rather than a guess.
	u = newHalvedUpdateEngine(t, 2_000_000_000, 2_000_000_000, true, 0)
	b = dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: 4}); err != nil {
		t.Fatalf("update: %v", err)
	}
	for _, id := range []string{"c1", "d1"} {
		if got := u.sent(id); len(got) != 1 || got[0]["NanoCpus"] != float64(4_000_000_000) {
			t.Fatalf("%s updates = %v, want the unlabelled pair moved together to 4 CPUs", id, got)
		}
	}
}

// The daemon saturating its half while the runner idles reads as a pair half
// busy; the stats say how busy the busier half is, against its own quota, so
// the controller can see a build waiting on its CPU.
func TestDinDStatsSayHowBusyTheBusierHalfIs(t *testing.T) {
	stats := func(total uint64) map[string]any {
		return map[string]any{"cpu_stats": map[string]any{
			"cpu_usage": map[string]any{"total_usage": total}, "system_cpu_usage": 100, "online_cpus": 4,
		}}
	}
	runnerLabels := map[string]string{LabelName: "runner-1", LabelDockerMode: string(store.DockerDinD), LabelCPUs: "2"}
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/stats": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, stats(10)) },
		"GET " + v + "/containers/d1/stats": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, stats(48)) },
		"GET " + v + "/containers/c1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, &ContainerInspect{ID: "c1", Config: &ContainerConfig{Labels: runnerLabels}})
		},
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{{ID: "d1", Labels: map[string]string{LabelDinDFor: "runner-1", LabelCPUs: "2"}}})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})
	got, err := b.Stats(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	// 40% and 192% of a core: the pair uses 58% of its 4 CPUs, and the
	// daemon 96% of its own 2.
	if got.CPUPercent != 232 || got.BusiestHalfPercent != 96 {
		t.Fatalf("stats = %+v, want 232%% for the pair and 96%% for its busier half", got)
	}
	// And each half on its own, beside the quota it was created with, which is
	// what lets the controller judge how the slot was divided.
	if got.Halves == nil || got.Halves.Runner.CPULimit != 2 || got.Halves.Daemon.CPULimit != 2 ||
		got.Halves.Runner.CPUs != 0.4 || got.Halves.Daemon.CPUs != 1.92 {
		t.Fatalf("halves = %+v, want the runner at 0.4 and the daemon at 1.92 cores, each of 2", got.Halves)
	}
}

// A docker-in-docker pair's boost used to go to the daemon whatever the job
// was doing. A job whose steps run in the runner container -- setup-go and go
// build, not a docker build -- stayed capped at the runner's own half while
// the loan sat idle in the daemon: a runner showing "Maximum boost" at 10.4
// cores and using 1.04 of them. The loan now goes to whichever half is busy.
func TestUpdateResourcesLendsADinDBoostToTheHalfThatIsBusy(t *testing.T) {
	// Two halves of 2 CPUs, boosted 2x: the pair is to have 8.
	u := newHalvedUpdateEngine(t, 2_000_000_000, 2_000_000_000, true, 2)
	u.cpu["c1"], u.cpu["d1"] = 198, 3 // the runner saturates its half; the daemon idles
	b := dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: 4}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := u.sent("c1"); len(got) != 1 || got[0]["NanoCpus"] != float64(6_000_000_000) {
		t.Fatalf("runner updates = %v, want one to 6 CPUs, the pair's 8 less the daemon's 2", got)
	}
	if got := u.sent("d1"); len(got) != 0 {
		t.Fatalf("sidecar updates = %v, want none: an idle daemon keeps its own half", got)
	}

	// The daemon busier, as in a docker build: the loan goes to it.
	u = newHalvedUpdateEngine(t, 2_000_000_000, 2_000_000_000, true, 2)
	u.cpu["c1"], u.cpu["d1"] = 10, 199
	b = dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: 4}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := u.sent("c1"); len(got) != 0 {
		t.Fatalf("runner updates = %v, want none", got)
	}
	if got := u.sent("d1"); len(got) != 1 || got[0]["NanoCpus"] != float64(6_000_000_000) {
		t.Fatalf("sidecar updates = %v, want one to 6 CPUs", got)
	}

	// Moved from one half to the other, the half that had the loan gives it
	// back in the same update, so the pair never holds more than its target.
	u = newHalvedUpdateEngine(t, 2_000_000_000, 6_000_000_000, true, 2)
	u.cpu["c1"], u.cpu["d1"] = 198, 3
	b = dockerBackendFor(t, u.fakeEngine, DockerOptions{})
	if err := b.UpdateResources(context.Background(), "c1", store.Resources{CPUs: 4}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := u.sent("d1"); len(got) != 1 || got[0]["NanoCpus"] != float64(2_000_000_000) {
		t.Fatalf("sidecar updates = %v, want it back to its 2 CPU half", got)
	}
	if got := u.sent("c1"); len(got) != 1 || got[0]["NanoCpus"] != float64(6_000_000_000) {
		t.Fatalf("runner updates = %v, want it given the loan", got)
	}
}

// The two samples are taken together. A stats call is a second of the daemon's time at
// the least, and taken in turn they spent two of the agent's five before the pair had
// been looked at: here the runner's call is held until the sidecar's has arrived, which
// a sampler that waited for the first would never let happen.
func TestDinDStatsAsksForBothHalvesAtTheSameTime(t *testing.T) {
	runnerLabels := map[string]string{LabelName: "runner-1", LabelDockerMode: string(store.DockerDinD), LabelCPUs: "2"}
	sidecarAsked := make(chan struct{})
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/stats": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-sidecarAsked:
			case <-time.After(5 * time.Second):
				http.Error(w, "the sidecar was never asked for while the runner's call was out", http.StatusGatewayTimeout)
				return
			}
			writeJSON(w, 200, map[string]any{})
		},
		"GET " + v + "/containers/d1/stats": func(w http.ResponseWriter, r *http.Request) {
			close(sidecarAsked)
			writeJSON(w, 200, map[string]any{})
		},
		"GET " + v + "/containers/c1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, &ContainerInspect{ID: "c1", Config: &ContainerConfig{Labels: runnerLabels}})
		},
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{{ID: "d1", Labels: map[string]string{LabelDinDFor: "runner-1", LabelCPUs: "2"}}})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})
	got, err := b.Stats(context.Background(), "c1")
	if err != nil || got.Halves == nil {
		t.Fatalf("stats = %+v, err = %v; want both halves", got, err)
	}
}

// A sidecar that will not answer loses the whole reading rather than leave the runner's
// figure standing for the job: recorded as its peak with the daemon's share missing, it
// would be too low, which is the one error the sizing advice built on it cannot take. The
// agent keeps its last good sample instead.
func TestDinDStatsLoseTheWholeReadingWhenTheSidecarCannotBeSampled(t *testing.T) {
	runnerLabels := map[string]string{LabelName: "runner-1", LabelDockerMode: string(store.DockerDinD)}
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/c1/stats": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{}) },
		"GET " + v + "/containers/d1/stats": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "cgroup unavailable"})
		},
		"GET " + v + "/containers/c1/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, &ContainerInspect{ID: "c1", Config: &ContainerConfig{Labels: runnerLabels}})
		},
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{{ID: "d1", Labels: map[string]string{LabelDinDFor: "runner-1"}}})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})
	got, err := b.Stats(context.Background(), "c1")
	if err == nil || got != (Stats{}) {
		t.Fatalf("stats = %+v, err = %v; want the reading lost and the failure reported", got, err)
	}
}
