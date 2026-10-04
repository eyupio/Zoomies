package hosttune

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"
)

const ReportFile = "host-doctor.json"

// Monitor publishes bounded, read-only observations independently of the
// heartbeat deadline. It writes only its observation cache, never OS settings.
type Monitor struct {
	mu       sync.Mutex
	Engine   *Engine
	latest   *Report
	checking bool
	due      time.Time
	interval time.Duration
}

func NewMonitor(e *Engine) *Monitor { return &Monitor{Engine: e, interval: time.Minute} }
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
			c, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			r := m.Engine.Run(c, Dedicated)
			b, err := json.Marshal(r)
			if err == nil {
				_ = m.Engine.System.WriteFile(filepath.Join(m.Engine.WorkDir, ReportFile), b, 0640)
			}
			m.mu.Lock()
			m.latest = &r
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
