package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

// fakeDoctor answers a check from a script, so the task handler is tested
// without running a single host command.
type fakeDoctor struct {
	mu      sync.Mutex
	can     bool
	report  *hosttune.Report
	err     error
	gate    chan struct{}
	started chan struct{}
	runs    int
}

func (f *fakeDoctor) Latest(context.Context) *hosttune.Report { return nil }
func (f *fakeDoctor) CanCheckNow() bool                       { return f.can }
func (f *fakeDoctor) CheckNow(ctx context.Context) (*hosttune.Report, error) {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.report, f.err
}

// quietMonitor is a real monitor whose periodic report is never read, so a
// test can give it a bare engine (no filesystem behind it) and still be asked
// the one question that matters here: can it run the checks on request.
type quietMonitor struct{ *hosttune.Monitor }

func (quietMonitor) Latest(context.Context) *hosttune.Report { return nil }

func harnessWithDoctor(t *testing.T, capacity int, d HostDoctor) *harness {
	t.Helper()
	tr := newFakeTransport()
	be := newFakeBackend(store.BackendDocker)
	a, err := New(Options{
		Name: "test-host", WorkDir: t.TempDir(), Capacity: capacity, Backends: backend.NewRegistry(be),
		DefaultBackend: store.BackendDocker, Transport: tr, HeartbeatInterval: time.Second,
		Logger: testLogger(), Clock: newTestClock().Now, Doctor: d,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Join(context.Background(), "join-token"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{t: t, agent: a, tr: tr, be: be, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- a.Run(ctx) }()
	t.Cleanup(func() { h.stop() })
	return h
}

func checkTask(id string) Task { return Task{ID: id, Kind: TaskCheckHost} }

func TestACheckTaskAnswersWithTheReportItTook(t *testing.T) {
	rep := &hosttune.Report{CheckedAt: time.Now().UTC(), OS: "linux"}
	h := harnessWithDoctor(t, 1, &fakeDoctor{can: true, report: rep})
	h.tr.tasks <- []Task{checkTask("check-1")}
	res := h.nextResult()
	if !res.OK || res.TaskID != "check-1" || res.Kind != TaskCheckHost || res.Doctor == nil || !res.Doctor.CheckedAt.Equal(rep.CheckedAt) {
		t.Fatalf("result = %+v, want OK with the report", res)
	}
	if res.RunnerID != "" {
		t.Errorf("a check names no runner, got %q", res.RunnerID)
	}
}

// Every path must answer, or the controller's lease on the request stands
// until it expires and the button looks broken.
func TestACheckTaskAnswersOnEveryFailurePath(t *testing.T) {
	tests := []struct {
		name string
		d    HostDoctor
		want string
	}{
		{"no monitor at all", nil, "cannot run its host checks"},
		{"unsupported here", &fakeDoctor{err: hosttune.ErrCheckNowUnsupported}, "cannot run its host checks"},
		{"nothing returned", &fakeDoctor{can: true}, "cannot run its host checks"},
		{"the run failed", &fakeDoctor{can: true, err: errors.New("boom")}, "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := harnessWithDoctor(t, 1, tc.d)
			h.tr.tasks <- []Task{checkTask("check-1")}
			res := h.nextResult()
			if res.OK || res.Doctor != nil || res.NotStarted || !strings.Contains(res.Error, tc.want) {
				t.Fatalf("result = %+v, want a plain failure mentioning %q", res, tc.want)
			}
		})
	}
}

// A check is not lifecycle work: on a host with every slot taken it must still
// answer, and it must not take a slot from the runner that is waiting.
func TestACheckTaskNeitherWaitsForNorTakesACapacitySlot(t *testing.T) {
	d := &fakeDoctor{can: true, report: &hosttune.Report{OS: "linux"}}
	h := harnessWithDoctor(t, 1, d)
	h.be.mu.Lock()
	h.be.createDelay = 700 * time.Millisecond
	h.be.mu.Unlock()
	h.tr.tasks <- []Task{createTask("create-1", "runner-1")}
	time.Sleep(100 * time.Millisecond)
	h.tr.tasks <- []Task{checkTask("check-1")}
	if res := h.nextResult(); res.TaskID != "check-1" || !res.OK {
		t.Fatalf("while the only slot was busy the first result was %+v, want the check", res)
	}
	if res := h.nextResult(); res.TaskID != "create-1" || !res.OK {
		t.Fatalf("create result = %+v", res)
	}
}

func TestACheckTaskStillInFlightAtShutdownIsGivenBack(t *testing.T) {
	d := &fakeDoctor{can: true, gate: make(chan struct{}), started: make(chan struct{}, 1)}
	h := harnessWithDoctor(t, 1, d)
	h.tr.tasks <- []Task{checkTask("check-1")}
	<-d.started
	h.cancel()
	res := h.nextResult()
	if res.OK || !res.NotStarted || res.TaskID != "check-1" {
		t.Fatalf("result = %+v, want not-started so the controller may offer it again", res)
	}
}

func TestACheckTaskNeedsNothingButAnID(t *testing.T) {
	if err := validateTask(checkTask("check-1")); err != nil {
		t.Errorf("a check with no runner and no spec was refused: %v", err)
	}
	if err := validateTask(Task{Kind: TaskCheckHost}); err == nil {
		t.Error("a task with no ID was accepted")
	}
}

// The controller sends the kind only to an agent that says it can answer, and a
// flag that is true on a container would send it to one that cannot.
func TestTheAgentAdvertisesHostChecksOnlyWhereItsMonitorCanRunThem(t *testing.T) {
	var typedNil *hosttune.Monitor
	tests := []struct {
		name string
		d    HostDoctor
		want bool
	}{
		{"native linux", quietMonitor{hosttune.NewMonitor(&hosttune.Engine{OS: "linux"})}, true},
		{"container", quietMonitor{hosttune.NewMonitor(&hosttune.Engine{OS: "linux", Container: true})}, false},
		{"another operating system", quietMonitor{hosttune.NewMonitor(&hosttune.Engine{OS: "darwin"})}, false},
		{"a monitor with no engine", quietMonitor{hosttune.NewMonitor(nil)}, false},
		{"a typed nil monitor", typedNil, false},
		{"no monitor", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := harnessWithDoctor(t, 1, tc.d)
			feats := h.agent.features()
			got := false
			for _, f := range feats {
				got = got || f == FeatureHostCheck
			}
			if got != tc.want {
				t.Fatalf("features = %v, host-check advertised = %v, want %v", feats, got, tc.want)
			}
			if tc.want && feats[len(feats)-1] != FeatureHostCheck && feats[len(feats)-2] != FeatureHostCheck {
				t.Errorf("host-check should follow the existing flags, got %v", feats)
			}
		})
	}
}

// Nothing in the protocol moves: an agent that ignores the field and a
// controller that has never heard of it both keep working.
func TestTheCheckAdditionsLeaveTheProtocolAlone(t *testing.T) {
	if ProtocolVersion != 1 {
		t.Fatalf("ProtocolVersion = %d; a bump marks every older agent incompatible", ProtocolVersion)
	}
	b, err := json.Marshal(TaskResult{TaskID: "t", OK: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "doctor") {
		t.Errorf("a result with no report carries a doctor field: %s", b)
	}
	b, _ = json.Marshal(TaskResult{TaskID: "t", Kind: TaskCheckHost, OK: true, Doctor: &hosttune.Report{OS: "linux"}})
	var back TaskResult
	if err := json.Unmarshal(b, &back); err != nil || back.Doctor == nil || back.Doctor.OS != "linux" {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
}
