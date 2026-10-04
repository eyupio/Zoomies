package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// diskBoundHost gives a host the usage the advice rests on: waiting on disk
// since a while ago, with memory to spare.
func (h *harness) diskBoundHost(host *store.Host, waitingFor time.Duration, availableMB int64) {
	h.t.Helper()
	now := h.c.Now()
	cpu, wait := 60.0, 35.0
	since := now.Add(-waitingFor)
	if err := h.st.SetHostUsage(h.ctx, host.ID, store.HostUsage{
		CPUPercent: &cpu, IOWaitPercent: &wait, IOWaitHighSince: &since,
		MemoryAvailableMB: &availableMB, SampledAt: now,
	}); err != nil {
		h.t.Fatalf("SetHostUsage: %v", err)
	}
}

// ranJobs records n finished jobs of the pool on the host, as the runner would.
func (h *harness) ranJobs(pool *store.Pool, host *store.Host, n int) {
	h.t.Helper()
	for i := 0; i < n; i++ {
		queued := h.c.Now().Add(-time.Duration(i+1) * 20 * time.Minute)
		started, done := queued.Add(10*time.Second), queued.Add(5*time.Minute)
		if _, err := h.st.UpsertJob(h.ctx, &store.Job{
			GitHubJobID: time.Now().UnixNano() + int64(i),
			Repo:        "acme/widgets", Workflow: "CI", JobName: "build",
			Labels: store.NormalizeLabels(pool.Labels),
			State:  store.JobCompleted, Conclusion: "success",
			PoolID: pool.ID, HostID: host.ID, Matched: true,
			QueuedAt: queued, StartedAt: &started, CompletedAt: &done,
		}); err != nil {
			h.t.Fatalf("UpsertJob: %v", err)
		}
	}
}

func (h *harness) problemOrNil(code string) *Problem {
	h.t.Helper()
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		h.t.Fatalf("Problems: %v", err)
	}
	for _, p := range ps {
		if p.Code == code {
			p := p
			return &p
		}
	}
	return nil
}

// The advice needs all three: the disk is the bottleneck, the pool ran there,
// and there is memory to put the folder in. It is information, it names the
// pool and the one setting, and where the pool has a memory limit it proposes
// the limit that leaves the job the room it has.
func TestAPoolWhoseHostsWaitOnDiskIsToldItsWorkFolderCouldLiveInMemory(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.Resources.MemoryMB = 8192
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.diskBoundHost(host, 25*time.Minute, 20_000)
	h.ranJobs(pool, host, 4)

	p := h.problemOrNil("pool.tmpfs_suggested")
	if p == nil {
		t.Fatal("a pool that has run on a disk-bound host with memory to spare was not told")
	}
	if p.Severity != config.SeverityInfo || p.TargetKind != "pool" || p.TargetID != pool.ID {
		t.Errorf("severity/target = %s %s/%s, want info on the pool", p.Severity, p.TargetKind, p.TargetID)
	}
	for _, want := range []string{host.Name, "I/O wait"} {
		if !strings.Contains(p.Detail, want) {
			t.Errorf("detail %q does not mention %q", p.Detail, want)
		}
	}
	// 8192 + 4096 for the folder: the same room for the job, with it on top.
	for _, want := range []string{pool.Name, "--tmpfs-work", "12 GB"} {
		if !strings.Contains(p.Fix, want) {
			t.Errorf("fix %q does not mention %q", p.Fix, want)
		}
	}
	if p.Since == nil {
		t.Error("the problem carries no since, so the list would be rewritten as time passes")
	}
}

// Each condition is there because without it the advice is wrong or unusable.
func TestTheTmpfsAdviceIsSilentUnlessEveryConditionHolds(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *harness, pool *store.Pool, host *store.Host)
	}{
		{"the disk has not been busy long enough", func(h *harness, pool *store.Pool, host *store.Host) {
			h.diskBoundHost(host, 2*time.Minute, 20_000)
			h.ranJobs(pool, host, 4)
		}},
		{"the host has no memory to spare", func(h *harness, pool *store.Pool, host *store.Host) {
			h.diskBoundHost(host, time.Hour, 1500)
			h.ranJobs(pool, host, 4)
		}},
		{"the pool never ran on that host", func(h *harness, pool *store.Pool, host *store.Host) {
			h.diskBoundHost(host, time.Hour, 20_000)
		}},
		{"a single job is not the pool", func(h *harness, pool *store.Pool, host *store.Host) {
			h.diskBoundHost(host, time.Hour, 20_000)
			h.ranJobs(pool, host, 1)
		}},
		{"the work folder is already in memory", func(h *harness, pool *store.Pool, host *store.Host) {
			pool.Tmpfs.Work = store.TmpfsMount{Enabled: true}
			if err := h.st.UpdatePool(h.ctx, pool); err != nil {
				t.Fatal(err)
			}
			h.diskBoundHost(host, time.Hour, 20_000)
			h.ranJobs(pool, host, 4)
		}},
		{"the reading is stale", func(h *harness, pool *store.Pool, host *store.Host) {
			h.diskBoundHost(host, time.Hour, 20_000)
			h.ranJobs(pool, host, 4)
			h.advance(store.HostUsageMaxAge + time.Minute)
		}},
		{"the host is cordoned", func(h *harness, pool *store.Pool, host *store.Host) {
			h.diskBoundHost(host, time.Hour, 20_000)
			h.ranJobs(pool, host, 4)
			host.Cordoned = true
			if err := h.st.UpdateHost(h.ctx, host); err != nil {
				t.Fatal(err)
			}
		}},
		{"the pool is disabled", func(h *harness, pool *store.Pool, host *store.Host) {
			pool.Enabled = false
			if err := h.st.UpdatePool(h.ctx, pool); err != nil {
				t.Fatal(err)
			}
			h.diskBoundHost(host, time.Hour, 20_000)
			h.ranJobs(pool, host, 4)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, pool, host := h.fleet()
			tc.setup(h, pool, host)
			if p := h.problemOrNil("pool.tmpfs_suggested"); p != nil {
				t.Fatalf("advice raised: %+v", *p)
			}
		})
	}
}

// Persistent means the suggestion stands for as long as it is true, unchanged,
// and goes the moment it is not. The list is re-sent when its text changes, so
// a standing suggestion must not change as time passes.
func TestTheTmpfsAdviceStandsWhileTrueAndClearsItself(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	h.diskBoundHost(host, 25*time.Minute, 20_000)
	h.ranJobs(pool, host, 4)

	first := h.problemOrNil("pool.tmpfs_suggested")
	if first == nil {
		t.Fatal("no advice")
	}
	// Refresh the heartbeat each minute, as an agent would, keeping the same
	// stretch of waiting: the clock the controller keeps does not move.
	since := *first.Since
	for i := 0; i < 5; i++ {
		h.advance(time.Minute)
		cpu, wait, mem := 60.0, 35.0, int64(20_000)
		if err := h.st.SetHostUsage(h.ctx, host.ID, store.HostUsage{
			CPUPercent: &cpu, IOWaitPercent: &wait, IOWaitHighSince: &since,
			MemoryAvailableMB: &mem, SampledAt: h.c.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	later := h.problemOrNil("pool.tmpfs_suggested")
	if later == nil {
		t.Fatal("the advice went away while it was still true")
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(later)
	if string(a) != string(b) {
		t.Errorf("a standing suggestion changed as time passed:\n%s\n%s", a, b)
	}

	// Turning the setting on is what it asked for, and clears it.
	pool.Tmpfs.Work = store.TmpfsMount{Enabled: true}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	if p := h.problemOrNil("pool.tmpfs_suggested"); p != nil {
		t.Fatalf("the suggestion stayed after the pool took it: %+v", *p)
	}
}

// A tmpfs is charged to the runner's memory limit. Sizes an operator typed that
// take over half of it are a warning; folders left to size themselves cannot,
// and are only information when the limit is too small to give them their
// defaults. Either way the proposal is the limit plus what they may fill.
func TestAPoolIsProposedAMemoryLimitThatLeavesTheJobItsRoom(t *testing.T) {
	pool := func(limit int64, tmpfs store.TmpfsConfig) *store.Pool {
		return &store.Pool{Name: "zoomies-big", Backend: store.BackendDocker,
			Resources: store.Resources{MemoryMB: limit}, Tmpfs: tmpfs}
	}
	cases := []struct {
		name     string
		pool     *store.Pool
		want     bool
		severity config.Severity
		propose  string
	}{
		{"nothing in memory", pool(8192, store.TmpfsConfig{}), false, "", ""},
		{"no limit to spend", pool(0, store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}), false, "", ""},
		{"a roomy limit takes the default", pool(32768, store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}), false, "", ""},
		{"a typed size over half the limit", pool(8192, store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, SizeMB: 6000}}), true, config.SeverityWarning, "13.9 GB"},
		{"a small limit fits the default down", pool(4096, store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}), true, config.SeverityInfo, "8 GB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, ok := tmpfsMemoryWarning(tc.pool)
			if ok != tc.want {
				t.Fatalf("warned = %v, want %v: %+v", ok, tc.want, w)
			}
			if !ok {
				return
			}
			if w.Severity != tc.severity {
				t.Errorf("severity = %s, want %s", w.Severity, tc.severity)
			}
			if !strings.Contains(w.Fix, tc.propose) {
				t.Errorf("fix %q does not propose %s", w.Fix, tc.propose)
			}
			if w.TargetKind != "pool" || w.Code != "pool.tmpfs_memory_tight" {
				t.Errorf("warning = %+v", w)
			}
		})
	}
}

// A cache source under /dev/shm is the host's memory. The cache is evicted only
// down to its size limit, so without one it grows until the host is out.
func TestACacheKeptInMemoryNeedsASizeLimit(t *testing.T) {
	cases := []struct {
		source string
		limit  int64
		want   bool
	}{
		{"/dev/shm/zoomies-cache", 0, true},
		{"/dev/shm", 0, true},
		{"/dev/shm/../etc", 0, false},
		{"/dev/shm/zoomies-cache", 1 << 30, false},
		{"/var/lib/zoomies/cache", 0, false},
		{"/dev/shmem/cache", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		p := &store.Pool{Name: "zoomies-ci", Cache: store.CacheConfig{Enabled: true, Source: tc.source, SizeLimit: tc.limit}}
		if _, ok := cacheInMemoryWarning(p); ok != tc.want {
			t.Errorf("source %q limit %d: warned = %v, want %v", tc.source, tc.limit, ok, tc.want)
		}
	}
	off := &store.Pool{Name: "zoomies-ci", Cache: store.CacheConfig{Source: "/dev/shm/x"}}
	if _, ok := cacheInMemoryWarning(off); ok {
		t.Error("a disabled cache was warned about")
	}
}

// An agent too old to mount the folders starts the runner on disk, which is
// safe and invisible; the pool is told where it is saved.
func TestAPoolOnAnAgentThatCannotMountInMemoryFoldersIsWarned(t *testing.T) {
	pool := &store.Pool{Name: "zoomies-ci", Tmpfs: store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}}
	room := PoolRoom{Hosts: []PoolHostRoom{
		{Host: "new", Tmpfs: true},
		{Host: "old", Tmpfs: false},
	}}
	w, ok := heldWithoutTmpfs(pool, room)
	if !ok || !strings.Contains(w.Detail, "old") || strings.Contains(w.Detail, "new,") {
		t.Fatalf("warning = %+v, ok = %v; want only the old host named", w, ok)
	}
	if w.Code != "pool.tmpfs_unsupported" || w.TargetKind != "pool" {
		t.Errorf("warning = %+v", w)
	}
	if _, ok := heldWithoutTmpfs(&store.Pool{Name: "zoomies-ci"}, room); ok {
		t.Error("a pool with nothing in memory was warned about its hosts")
	}
	if _, ok := heldWithoutTmpfs(pool, PoolRoom{Hosts: []PoolHostRoom{{Host: "new", Tmpfs: true}}}); ok {
		t.Error("a pool whose hosts all support it was warned")
	}
}

// The seeded demo fleet and a real agent both advertise it, so a current host
// never carries the warning above.
func TestTheEmbeddedAgentAdvertisesTmpfs(t *testing.T) {
	host := &store.Host{Features: store.StringSlice{agent.FeatureTmpfs}}
	if !hostSupportsTmpfs(host) {
		t.Fatal("a host advertising the feature is not taken as supporting it")
	}
	if hostSupportsTmpfs(&store.Host{}) {
		t.Fatal("a host advertising nothing is taken as supporting it")
	}
}
