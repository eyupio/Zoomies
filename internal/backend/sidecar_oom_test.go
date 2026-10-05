package backend

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// sidecarEngine is a fake daemon holding one docker-in-docker runner and its
// sidecar, each as inspect reports it, and counting how often each is asked
// about.
type sidecarEngine struct {
	*fakeEngine
	mu      sync.Mutex
	inspect map[string]int
}

func newSidecarEngine(t *testing.T, runnerState, sidecarState ContainerState) *sidecarEngine {
	t.Helper()
	e := &sidecarEngine{inspect: map[string]int{}}
	runnerLabels := map[string]string{
		LabelRole: roleRunner, LabelName: "runner-1", LabelRunnerID: "run_1",
		LabelDockerMode: string(store.DockerDinD), LabelLimitsFrom: "host",
	}
	sidecarLabels := map[string]string{
		LabelRole: roleDinD, LabelName: "runner-1-dind", LabelDinDFor: "runner-1", LabelRunnerID: "run_1",
		LabelLimitsFrom: "host",
	}
	serve := func(id string, state ContainerState, labels map[string]string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			e.mu.Lock()
			e.inspect[id]++
			e.mu.Unlock()
			writeJSON(w, 200, &ContainerInspect{ID: id, State: &state, Config: &ContainerConfig{Labels: labels}})
		}
	}
	e.fakeEngine = newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, 200, []ContainerSummary{
				{ID: "run1", State: runnerState.Status, Labels: runnerLabels},
				{ID: "dind1", State: sidecarState.Status, Labels: sidecarLabels},
			})
		},
		"GET " + v + "/containers/run1/json":  serve("run1", runnerState, runnerLabels),
		"GET " + v + "/containers/dind1/json": serve("dind1", sidecarState, sidecarLabels),
	})
	return e
}

func (e *sidecarEngine) inspected(id string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.inspect[id]
}

func runnerWorkload(t *testing.T, e *sidecarEngine) Workload {
	t.Helper()
	b := dockerBackendFor(t, e.fakeEngine, DockerOptions{})
	got, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, w := range got {
		if !w.Sidecar {
			return w
		}
	}
	t.Fatalf("list returned no runner: %+v", got)
	return Workload{}
}

// A docker build that runs out of memory runs out in the sidecar, under the
// sidecar's own limit, and the runner beside it finishes its job and exits
// cleanly. Only the runner's flag was ever read, so the kill was never
// recorded: the job read as the workflow's own failure.
func TestAKillInTheSidecarIsAttributedToTheRunnerThatFinishedItsJob(t *testing.T) {
	e := newSidecarEngine(t,
		ContainerState{Status: "exited", ExitCode: 0},
		ContainerState{Status: "running", Running: true, OOMKilled: true})

	w := runnerWorkload(t, e)
	if !w.Status.OOMKilled || !w.Status.SidecarOOMKilled {
		t.Fatalf("status = %+v, want the sidecar's kill carried onto the runner", w.Status)
	}
	if w.Status.Phase != PhaseFailed {
		t.Errorf("phase = %q, want failed: a clean exit with a kill behind it is not a clean job", w.Status.Phase)
	}
	// What to change is the sidecar's half of the slot, not a pool field nobody set.
	for _, want := range []string{"Docker sidecar", "its half of the host's share", "Docker sidecar's share"} {
		if !strings.Contains(w.Status.Message, want) {
			t.Errorf("message %q does not say %q", w.Status.Message, want)
		}
	}
}

// The runner's own flag says which container was killed, and asking the sidecar
// as well would be a request that cannot change the answer.
func TestARunnersOwnKillIsNotQuestionedAgainstItsSidecar(t *testing.T) {
	e := newSidecarEngine(t,
		ContainerState{Status: "exited", ExitCode: 137, OOMKilled: true},
		ContainerState{Status: "running", Running: true, OOMKilled: true})

	w := runnerWorkload(t, e)
	if !w.Status.OOMKilled || w.Status.SidecarOOMKilled {
		t.Fatalf("status = %+v, want the runner's own kill, not the sidecar's", w.Status)
	}
	if got := e.inspected("dind1"); got != 0 {
		t.Fatalf("the sidecar was inspected %d times; the runner's flag had already answered", got)
	}
}

// A running fleet is where this is called most, and a runner that has not
// stopped has no end of job to attribute a kill to: nothing extra is asked.
func TestARunnerThatIsStillRunningCostsNoRequestAboutItsSidecar(t *testing.T) {
	e := newSidecarEngine(t,
		ContainerState{Status: "running", Running: true},
		ContainerState{Status: "running", Running: true, OOMKilled: true})

	w := runnerWorkload(t, e)
	if w.Status.OOMKilled {
		t.Fatalf("status = %+v, want a live runner left as it is", w.Status)
	}
	if got := e.inspected("dind1"); got != 0 {
		t.Fatalf("the sidecar was inspected %d times for a runner that is still running", got)
	}
}

// A sidecar that was never killed leaves a clean exit a clean exit.
func TestASidecarThatWasNotKilledLeavesACleanExitClean(t *testing.T) {
	e := newSidecarEngine(t,
		ContainerState{Status: "exited", ExitCode: 0},
		ContainerState{Status: "running", Running: true})

	w := runnerWorkload(t, e)
	if w.Status.OOMKilled || w.Status.SidecarOOMKilled || w.Status.Phase != PhaseExited {
		t.Fatalf("status = %+v, want a clean exit", w.Status)
	}
}
