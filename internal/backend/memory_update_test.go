package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

const mib = int64(1) << 20

// memoryEngine is a fake daemon that holds the memory limits of a few
// containers and applies an update as the real one does, including the rule
// that makes the valve send the limit and the swap together: a memory limit
// above the swap limit already set is refused.
type memoryEngine struct {
	*fakeEngine
	mu       sync.Mutex
	hosts    map[string]*HostConfig
	labels   map[string]map[string]string
	updates  []UpdateConfig
	statsURL []string
	// noUpdateRoute makes the update endpoint a 404, as a runtime without one
	// answers, while the container still inspects.
	noUpdateRoute bool
	// stats is what a stats call answers for any container.
	stats statsJSON
}

func newMemoryEngine(t *testing.T) *memoryEngine {
	t.Helper()
	e := &memoryEngine{hosts: map[string]*HostConfig{}, labels: map[string]map[string]string{}}
	e.fakeEngine = newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			var out []ContainerSummary
			for id, labels := range e.labels {
				if labels[LabelRole] == roleDinD {
					out = append(out, ContainerSummary{ID: id, State: "running", Labels: labels})
				}
			}
			writeJSON(w, 200, out)
		},
		"GET " + v + "/containers/{id}/json": func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			id := r.PathValue("id")
			hc, ok := e.hosts[id]
			if !ok {
				writeJSON(w, 404, map[string]string{"message": "No such container: " + id})
				return
			}
			writeJSON(w, 200, &ContainerInspect{ID: id, State: &ContainerState{Status: "running", Running: true},
				Config: &ContainerConfig{Labels: e.labels[id]}, HostConfig: hc})
		},
		"POST " + v + "/containers/{id}/update": func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			defer e.mu.Unlock()
			id := r.PathValue("id")
			hc, ok := e.hosts[id]
			if !ok || e.noUpdateRoute {
				writeJSON(w, 404, map[string]string{"message": "page not found"})
				return
			}
			var body UpdateConfig
			_ = json.NewDecoder(r.Body).Decode(&body)
			e.updates = append(e.updates, body)
			if body.Memory > 0 && body.MemorySwap >= 0 && body.MemorySwap > 0 && body.Memory > body.MemorySwap {
				writeJSON(w, 500, map[string]string{"message": "Memory limit should be smaller than already set memoryswap limit, update the memoryswap at the same time"})
				return
			}
			if body.Memory > 0 && hc.MemorySwap > 0 && body.MemorySwap == 0 && body.Memory > hc.MemorySwap {
				writeJSON(w, 500, map[string]string{"message": "Memory limit should be smaller than already set memoryswap limit, update the memoryswap at the same time"})
				return
			}
			if body.Memory > 0 {
				hc.Memory = body.Memory
			}
			if body.MemorySwap != 0 {
				hc.MemorySwap = body.MemorySwap
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"Warnings":[]}`))
		},
		"GET " + v + "/containers/{id}/stats": func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			e.statsURL = append(e.statsURL, r.URL.RawQuery)
			s := e.stats
			e.mu.Unlock()
			writeJSON(w, 200, s)
		},
	})
	return e
}

// container adds one the daemon knows: limit and swap in whole megabytes, swap
// being the total the daemon calls MemorySwap, and guarantee the label the
// create stamped.
func (e *memoryEngine) container(id string, limitMB, swapTotalMB, guaranteeMB int64, extra map[string]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hosts[id] = &HostConfig{Memory: limitMB * mib, MemorySwap: swapTotalMB * mib}
	labels := map[string]string{LabelManaged: "true", LabelRole: roleRunner}
	if guaranteeMB > 0 {
		labels[LabelMemoryMB] = strconv.FormatInt(guaranteeMB, 10)
	}
	for k, v := range extra {
		labels[k] = v
	}
	e.labels[id] = labels
}

func (e *memoryEngine) held(id string) HostConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return *e.hosts[id]
}

func (e *memoryEngine) sent() []UpdateConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]UpdateConfig(nil), e.updates...)
}

func memoryBackend(t *testing.T, e *memoryEngine) *DockerBackend {
	t.Helper()
	return dockerBackendFor(t, e.fakeEngine, DockerOptions{})
}

// The daemon refuses a limit above the swap limit already set, and a container
// is made with the two equal, so a raise that sent only the limit would be
// refused every time. Both go, together, and the swap is the total.
func TestRaisingAContainersMemorySendsTheLimitAndTheSwapTogether(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, 4096, 4096, nil)
	b := memoryBackend(t, e)

	if err := b.RaiseMemory(context.Background(), "run1", 5120, 0); err != nil {
		t.Fatalf("RaiseMemory: %v", err)
	}
	got := e.held("run1")
	if got.Memory != 5120*mib || got.MemorySwap != 5120*mib {
		t.Fatalf("daemon holds memory %d and swap %d MiB, want both 5120", got.Memory/mib, got.MemorySwap/mib)
	}
	sent := e.sent()
	if len(sent) != 1 || sent[0].Memory != 5120*mib || sent[0].MemorySwap != 5120*mib {
		t.Fatalf("update bodies = %+v, want one carrying both figures", sent)
	}
}

func TestSwapIsAllowedBeyondTheLimitAsATotalTheDaemonTakes(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, 4096, 4096, nil)
	b := memoryBackend(t, e)

	if err := b.RaiseMemory(context.Background(), "run1", 5120, 2048); err != nil {
		t.Fatalf("RaiseMemory: %v", err)
	}
	if got := e.held("run1"); got.Memory != 5120*mib || got.MemorySwap != 7168*mib {
		t.Fatalf("daemon holds memory %d and swap total %d MiB, want 5120 and 7168", got.Memory/mib, got.MemorySwap/mib)
	}
}

// Lowering a live limit is refused by the daemon or kills the process, and a
// caller that asks for it has a bug: it is refused here, where it can be read,
// and nothing is sent.
func TestARaiseThatWouldLowerALimitIsRefusedAndNothingIsSent(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, 4096, 4096, nil)
	b := memoryBackend(t, e)

	err := b.RaiseMemory(context.Background(), "run1", 3000, 0)
	if !errors.Is(err, ErrMemoryLowering) {
		t.Fatalf("err = %v, want ErrMemoryLowering", err)
	}
	if got := e.sent(); len(got) != 0 {
		t.Fatalf("an update was sent for a refused request: %+v", got)
	}
	if got := e.held("run1"); got.Memory != 4096*mib {
		t.Fatalf("the limit moved to %d MiB", got.Memory/mib)
	}
}

// The guard asks for what it wants on every look, and holds no record of what it
// asked last: a request that changes nothing costs the daemon nothing.
func TestARequestThatChangesNothingIsNotSent(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, 6144, 4096, nil)
	b := memoryBackend(t, e)

	if err := b.RaiseMemory(context.Background(), "run1", 4096, 2048); err != nil {
		t.Fatalf("RaiseMemory: %v", err)
	}
	if got := e.sent(); len(got) != 0 {
		t.Fatalf("an update was sent for a request that changed nothing: %+v", got)
	}
}

// A raise that asks for less swap than the container already holds must not
// quietly lower what it may use in all.
func TestRaisingTheLimitNeverShrinksTheSwapAContainerHas(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, 8192, 4096, nil)
	b := memoryBackend(t, e)

	if err := b.RaiseMemory(context.Background(), "run1", 5120, 0); err != nil {
		t.Fatalf("RaiseMemory: %v", err)
	}
	if got := e.held("run1"); got.Memory != 5120*mib || got.MemorySwap != 8192*mib {
		t.Fatalf("daemon holds memory %d and swap total %d MiB, want 5120 and 8192", got.Memory/mib, got.MemorySwap/mib)
	}
}

func TestAContainerWithUnlimitedSwapStaysSoWhenItsLimitRises(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, -1, 4096, nil)
	b := memoryBackend(t, e)

	if err := b.RaiseMemory(context.Background(), "run1", 5120, 0); err != nil {
		t.Fatalf("RaiseMemory: %v", err)
	}
	if got := e.held("run1"); got.Memory != 5120*mib || got.MemorySwap != -1 {
		t.Fatalf("daemon holds memory %d and swap total %d, want 5120 MiB and -1", got.Memory/mib, got.MemorySwap)
	}
}

func TestAContainerWithNoMemoryLimitHasNothingToRaise(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 0, 0, 0, nil)
	b := memoryBackend(t, e)

	if err := b.RaiseMemory(context.Background(), "run1", 4096, 0); err != nil {
		t.Fatalf("RaiseMemory: %v", err)
	}
	if got := e.sent(); len(got) != 0 {
		t.Fatalf("an update was sent for a container with no limit: %+v", got)
	}
	if got, err := b.MemoryContainers(context.Background(), "run1"); err != nil || len(got) != 0 {
		t.Fatalf("MemoryContainers = %+v, %v; a container with no limit is not listed", got, err)
	}
}

// A runtime that answers but has no update endpoint will refuse every request
// the same way, and is not to be retried like a container that has finished.
func TestARuntimeWithNoUpdateEndpointIsToldApartFromAFinishedContainer(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, 4096, 4096, nil)
	e.noUpdateRoute = true
	b := memoryBackend(t, e)

	err := b.RaiseMemory(context.Background(), "run1", 5120, 0)
	if !errors.Is(err, ErrMemoryUpdateUnsupported) {
		t.Fatalf("err = %v, want ErrMemoryUpdateUnsupported", err)
	}

	err = b.RaiseMemory(context.Background(), "gone", 5120, 0)
	if !errors.Is(err, ErrNotFound) || errors.Is(err, ErrMemoryUpdateUnsupported) {
		t.Fatalf("err = %v for a container that is gone, want ErrNotFound", err)
	}
}

// What a runner was created with is on its label, and what it holds now is the
// daemon's: the difference is the loan, and it survives an agent restart because
// neither lives in the agent.
func TestAPairListsEachContainerWithWhatItWasCreatedWithAndHoldsNow(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 3072, 3072, 2048, map[string]string{
		LabelName: "runner-1", LabelDockerMode: string(store.DockerDinD)})
	e.container("dind1", 2048, 4096, 2048, map[string]string{
		LabelRole: roleDinD, LabelName: "runner-1-dind", LabelDinDFor: "runner-1"})
	b := memoryBackend(t, e)

	got, err := b.MemoryContainers(context.Background(), "run1")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]MemoryContainer{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if len(got) != 2 {
		t.Fatalf("containers = %+v, want the runner and its sidecar", got)
	}
	if r := byID["run1"]; r.Daemon || r.GuaranteeMB != 2048 || r.LimitMB != 3072 || r.SwapMB != 0 {
		t.Errorf("runner = %+v, want created with 2048, holding 3072 and no swap", r)
	}
	if d := byID["dind1"]; !d.Daemon || d.GuaranteeMB != 2048 || d.LimitMB != 2048 || d.SwapMB != 2048 {
		t.Errorf("sidecar = %+v, want created with 2048, holding 2048 and 2048 of swap", d)
	}
}

// A container from a release that stamped no label is taken to have been
// created with what it holds, which reads as nothing lent: better than inventing
// a loan out of an old container's size.
func TestAContainerWithNoLabelIsTakenToHoldWhatItWasCreatedWith(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, 4096, 0, nil)
	b := memoryBackend(t, e)

	got, err := b.MemoryContainers(context.Background(), "run1")
	if err != nil || len(got) != 1 {
		t.Fatalf("containers = %+v, %v", got, err)
	}
	if got[0].GuaranteeMB != 4096 || got[0].LimitMB != 4096 {
		t.Fatalf("container = %+v, want guarantee and limit both 4096", got[0])
	}
}

// The guard looks every second at a runner close to its limit, so a reading may
// not take the second a CPU percentage does.
func TestAMemoryReadingAsksTheDaemonForOneSampleNotTwo(t *testing.T) {
	e := newMemoryEngine(t)
	e.container("run1", 4096, 4096, 4096, nil)
	e.stats.MemoryStats.Usage = uint64(3500 * mib)
	e.stats.MemoryStats.Limit = uint64(4096 * mib)
	e.stats.MemoryStats.Stats = map[string]uint64{"inactive_file": uint64(500 * mib)}
	b := memoryBackend(t, e)

	got, err := b.MemoryUsage(context.Background(), "run1")
	if err != nil {
		t.Fatal(err)
	}
	if got.UsageBytes != 3000*mib || got.LimitBytes != 4096*mib {
		t.Fatalf("reading = %+v, want the working set (3000 MiB) against a 4096 MiB limit", got)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.statsURL) != 1 || e.statsURL[0] != "one-shot=true&stream=false" {
		t.Fatalf("stats queries = %v, want one with one-shot=true", e.statsURL)
	}
}
