package hosttune

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"
)

const ReportFile = "host-doctor.json"

// MonitorRunTimeout bounds one run of the checks. Exported so that whatever
// waits on a run (the controller's lease on a check request) can be pinned
// above it by a test instead of by a comment.
const MonitorRunTimeout = 45 * time.Second

// ErrCheckNowUnsupported is what CheckNow answers where the monitor cannot run
// the checks itself: inside a container it only re-reads a file another unit
// writes, and off Linux the engine has nothing to look at.
var ErrCheckNowUnsupported = errors.New("the host checks cannot be run on request here: this agent runs in a container or on an operating system other than Linux")

// Monitor publishes bounded, read-only observations independently of the
// heartbeat deadline. It writes only its observation cache, never OS settings.
type Monitor struct {
	mu       sync.Mutex
	Engine   *Engine
	latest   *Report
	checking bool
	due      time.Time
	interval time.Duration

	// runMu is the single flight: the periodic run and an on-request run never
	// overlap, so a press cannot double the load a busy host is already under.
	runMu sync.Mutex
	// waiting is the one on-request run that has not started yet. A press that
	// arrives while it is still queued joins it -- its report is taken after
	// that press too -- so any number of presses cost at most one extra run.
	waiting *checkCall
}

// checkCall is one queued on-request run and everyone waiting on its answer.
type checkCall struct {
	done    chan struct{}
	report  *Report
	err     error
	waiters int
}

func NewMonitor(e *Engine) *Monitor { return &Monitor{Engine: e, interval: time.Minute} }

// CanCheckNow reports whether CheckNow can run the checks here. It is safe on a
// nil monitor because the agent holds one behind an interface, where a typed nil
// is not a nil interface.
func (m *Monitor) CanCheckNow() bool {
	return m != nil && m.Engine != nil && m.Engine.OS == "linux" && !m.Engine.Container
}

// CheckNow runs the checks once, now, and returns the report. If a run is
// already in flight it waits for it and then runs again, because that report
// may have been taken before the caller asked. The report is returned even when
// it is no newer than the last one: whether that matters is the receiver's
// call.
func (m *Monitor) CheckNow(ctx context.Context) (*Report, error) {
	if !m.CanCheckNow() {
		return nil, ErrCheckNowUnsupported
	}
	m.mu.Lock()
	call := m.waiting
	if call == nil {
		call = &checkCall{done: make(chan struct{})}
		m.waiting = call
		go m.runWaiting(ctx, call)
	}
	call.waiters++
	m.mu.Unlock()
	select {
	case <-call.done:
		return call.report, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// runWaiting takes its turn on the single flight and runs for everyone who
// joined. It runs on its own goroutine so that a caller that stops waiting
// does not strand the others on a lock it still holds.
func (m *Monitor) runWaiting(ctx context.Context, call *checkCall) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	m.mu.Lock()
	m.waiting = nil
	m.mu.Unlock()
	call.report, call.err = m.run(ctx)
	close(call.done)
}

// run is the one place the checks execute, and is called with runMu held. A run cut off by its timeout is
// discarded rather than stored: the checks that did not get to run come back as
// errors, and publishing them would raise a host-health finding about the
// deadline instead of the host.
func (m *Monitor) run(ctx context.Context) (*Report, error) {
	c, cancel := context.WithTimeout(ctx, MonitorRunTimeout)
	defer cancel()
	r := m.Engine.Run(c, Dedicated)
	if err := c.Err(); err != nil {
		return nil, err
	}
	if b, err := json.Marshal(r); err == nil {
		_ = m.Engine.System.WriteFile(filepath.Join(m.Engine.WorkDir, ReportFile), b, 0640)
	}
	m.mu.Lock()
	m.latest = &r
	m.due = m.Engine.Now().Add(m.interval)
	m.mu.Unlock()
	return &r, nil
}

func (m *Monitor) Latest(ctx context.Context) *Report {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.Engine.Container {
		if r, err := ReadReport(m.Engine.System, m.Engine.WorkDir); err == nil && !r.Container && r.CheckedAt.Before(m.Engine.Now().Add(time.Minute)) && (m.latest == nil || r.CheckedAt.After(m.latest.CheckedAt)) {
			m.latest = r
		}
	}
	if m.Engine.Container && m.Engine.HostReportPath != "" {
		if b, err := m.Engine.System.ReadFile(m.Engine.HostReportPath); err == nil {
			var r Report
			if json.Unmarshal(b, &r) == nil && !r.Container && !r.CheckedAt.IsZero() && m.Engine.Now().Sub(r.CheckedAt) < 3*time.Minute && r.CheckedAt.Before(m.Engine.Now().Add(time.Minute)) && len(r.Results) <= 64 {
				m.latest = &r
				return m.latest
			}
		}
	}
	if !m.checking && !m.Engine.Now().Before(m.due) {
		m.checking = true
		go func() {
			// The error is dropped on purpose: a cut-off run stores nothing and the
			// next one is due after the interval either way, so a failing host is
			// not hammered.
			m.runMu.Lock()
			_, _ = m.run(ctx)
			m.runMu.Unlock()
			m.mu.Lock()
			m.checking = false
			m.due = m.Engine.Now().Add(m.interval)
			m.mu.Unlock()
		}()
	}
	return m.latest
}
func ReadReport(sys System, work string) (*Report, error) {
	b, err := sys.ReadFile(filepath.Join(work, ReportFile))
	if err != nil {
		return nil, err
	}
	var r Report
	if err = json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
func NewWarnings(before *Report, after Report) int {
	old := map[string]bool{}
	if before != nil {
		for _, r := range before.Results {
			if r.Status == Warn {
				old[r.ID] = true
			}
		}
	}
	n := 0
	for _, r := range after.Results {
		if r.Status == Warn && !old[r.ID] {
			n++
		}
	}
	return n
}
