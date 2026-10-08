package hosttune

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gatedSystem holds every run of the checks at its first read until the test
// opens the gate, and counts the cache writes, which is one per stored run. It
// is how a test makes "a run is in flight" a fact rather than a race.
type gatedSystem struct {
	*fakeSystem
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
	stored  atomic.Int32
}

func newGated() (*Engine, *gatedSystem) {
	e, f := fixture()
	g := &gatedSystem{fakeSystem: f, gate: make(chan struct{}), entered: make(chan struct{})}
	e.System = g
	return e, g
}

func (g *gatedSystem) ReadFile(p string) ([]byte, error) {
	// Latest reads the cache under the monitor's lock before it starts a run;
	// gating that read would hold the lock the test needs to observe the run.
	if strings.HasPrefix(p, "/work") {
		return g.fakeSystem.ReadFile(p)
	}
	g.once.Do(func() { close(g.entered) })
	<-g.gate
	return g.fakeSystem.ReadFile(p)
}

func (g *gatedSystem) WriteFile(p string, b []byte, m fs.FileMode) error {
	g.stored.Add(1)
	return g.fakeSystem.WriteFile(p, b, m)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func queuedWaiters(m *Monitor) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.waiting == nil {
		return 0
	}
	return m.waiting.waiters
}

// A container install only re-reads a file another unit writes, so a press
// there must say so and run nothing -- never quietly run the engine against a
// host it is not allowed to inspect.
func TestCheckNowRefusesWhereTheMonitorCannotRunTheChecks(t *testing.T) {
	for name, tc := range map[string]func(*Engine){
		"in a container":      func(e *Engine) { e.Container = true },
		"off Linux":           func(e *Engine) { e.OS = "darwin" },
		"container with file": func(e *Engine) { e.Container = true; e.HostReportPath = "/shared/report.json" },
	} {
		t.Run(name, func(t *testing.T) {
			e, f := fixture()
			tc(e)
			m := NewMonitor(e)
			if m.CanCheckNow() {
				t.Fatal("CanCheckNow is true")
			}
			rep, err := m.CheckNow(context.Background())
			if !errors.Is(err, ErrCheckNowUnsupported) || rep != nil {
				t.Fatalf("got %v, %v", rep, err)
			}
			if len(f.calls) > 0 || f.writes > 0 {
				t.Fatal("an unsupported check still touched the host")
			}
		})
	}
	var nilMonitor *Monitor
	if nilMonitor.CanCheckNow() {
		t.Fatal("a nil monitor claims it can check")
	}
	if _, err := nilMonitor.CheckNow(context.Background()); !errors.Is(err, ErrCheckNowUnsupported) {
		t.Fatalf("nil monitor: %v", err)
	}
}

// The report an operator waits for must be the one taken after the press, and
// the periodic path must then serve it rather than run again at once.
func TestCheckNowRunsTheChecksAndKeepsTheReport(t *testing.T) {
	e, f := fixture()
	m := NewMonitor(e)
	if !m.CanCheckNow() {
		t.Fatal("a native Linux monitor cannot check on request")
	}
	rep, err := m.CheckNow(context.Background())
	if err != nil || rep == nil || len(rep.Results) == 0 {
		t.Fatalf("got %+v, %v", rep, err)
	}
	if f.writes != 1 {
		t.Fatalf("cache writes = %d, want 1", f.writes)
	}
	if got := m.Latest(context.Background()); got == nil || !got.CheckedAt.Equal(rep.CheckedAt) {
		t.Fatalf("Latest did not serve the report just taken: %+v", got)
	}
	m.mu.Lock()
	due := m.due
	m.mu.Unlock()
	if !due.After(e.Now()) {
		t.Fatal("an on-request run left the periodic run due straight away")
	}
}

// A run that overran turns every check it did not reach into an error row, and
// storing it would raise a host-health finding about the deadline instead of
// the host. Both paths drop it.
func TestARunCutOffByItsTimeoutIsNotStored(t *testing.T) {
	t.Run("on request", func(t *testing.T) {
		e, g := newGated()
		m := NewMonitor(e)
		ctx, cancel := context.WithCancel(context.Background())
		errc := make(chan error, 1)
		go func() { _, err := m.CheckNow(ctx); errc <- err }()
		<-g.entered
		cancel()
		close(g.gate)
		if err := <-errc; err == nil {
			t.Fatal("a cut-off run reported success")
		}
		waitFor(t, "the run to finish", func() bool { return m.runMu.TryLock() })
		m.runMu.Unlock()
		if g.stored.Load() != 0 || m.latest != nil {
			t.Fatal("a cut-off run was stored")
		}
	})
	t.Run("periodic", func(t *testing.T) {
		e, g := newGated()
		m := NewMonitor(e)
		ctx, cancel := context.WithCancel(context.Background())
		m.Latest(ctx)
		<-g.entered
		cancel()
		close(g.gate)
		waitFor(t, "the periodic run to end", func() bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			return !m.checking
		})
		if g.stored.Load() != 0 || m.latest != nil {
			t.Fatal("a cut-off run was stored")
		}
	})
}

// A press during a periodic run cannot reuse that run's report, which may
// predate it, and presses piled up behind it must not each cost a run: the
// host being checked is the one under load.
func TestCheckNowWaitsForARunInFlightThenRunsOnceForEveryoneQueued(t *testing.T) {
	e, g := newGated()
	m := NewMonitor(e)
	m.Latest(context.Background())
	<-g.entered

	const callers = 3
	reports := make(chan *Report, callers)
	for i := 0; i < callers; i++ {
		go func() {
			r, err := m.CheckNow(context.Background())
			if err != nil {
				t.Error(err)
			}
			reports <- r
		}()
	}
	waitFor(t, "every press to queue", func() bool { return queuedWaiters(m) == callers })
	select {
	case <-reports:
		t.Fatal("a press was answered while the earlier run was still in flight")
	default:
	}
	close(g.gate)
	for i := 0; i < callers; i++ {
		select {
		case r := <-reports:
			if r == nil {
				t.Fatal("no report")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for the shared run")
		}
	}
	if got := g.stored.Load(); got != 2 {
		t.Fatalf("runs = %d, want the in-flight one plus exactly one for the presses", got)
	}
}

func TestContainerConsumesNativeHostReportWithoutRunningHostCommands(t *testing.T) {
	e, f := fixture()
	e.Container = true
	e.HostReportPath = "/shared/host-health/report.json"
	r := Report{CheckedAt: e.Now(), OS: "linux", Distro: "ubuntu 24.04", Results: []Result{{ID: "disk.space", Status: OK, Tier: Safe}}}
	b, _ := json.Marshal(r)
	f.put(e.HostReportPath, string(b))
	m := NewMonitor(e)
	got := m.Latest(context.Background())
	if got == nil || got.Container {
		t.Fatal("did not use native report")
	}
	if len(f.calls) > 0 || f.writes > 0 {
		t.Fatal("container unnecessarily inspected the OS")
	}
}
func TestNewWarningComparisonUsesCheckIDs(t *testing.T) {
	old := &Report{Results: []Result{{ID: "same", Status: Warn}, {ID: "fixed", Status: Warn}}}
	next := Report{Results: []Result{{ID: "same", Status: Warn}, {ID: "fixed", Status: OK}, {ID: "new", Status: Warn}}}
	if NewWarnings(old, next) != 1 {
		t.Fatal("warning comparison")
	}
	if NewWarnings(nil, next) != 2 {
		t.Fatal("missing baseline")
	}
}
func TestNativeMonitorCanImportAnImmediatePostTuneReport(t *testing.T) {
	e, f := fixture()
	r := Report{CheckedAt: e.Now(), Results: []Result{{ID: "disk.space", Tier: Safe, Status: OK}}}
	b, _ := json.Marshal(r)
	f.put(e.WorkDir+"/"+ReportFile, string(b))
	m := NewMonitor(e)
	m.due = e.Now().Add(time.Hour)
	got := m.Latest(context.Background())
	if got == nil || len(got.Results) != 1 {
		t.Fatal("post-tune cache not imported")
	}
}
