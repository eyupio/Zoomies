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
// safe and invisible; the pool is told where it is saved. Only hosts the pool
// can be placed on count: one its runner profile keeps it off is listed with no
// room, and naming it would be a warning about a machine it never lands on.
func TestAPoolOnAnAgentThatCannotMountInMemoryFoldersIsWarned(t *testing.T) {
	pool := &store.Pool{Name: "zoomies-ci", Tmpfs: store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}}
	hosts := []PoolHostRoom{
		{Host: "new", Tmpfs: true},
		{Host: "old", Tmpfs: false},
	}
	w, ok := heldWithoutTmpfs(pool, hosts)
	if !ok || !strings.Contains(w.Detail, "old") || strings.Contains(w.Detail, "new,") {
		t.Fatalf("warning = %+v, ok = %v; want only the old host named", w, ok)
	}
	if w.Code != "pool.tmpfs_unsupported" || w.TargetKind != "pool" {
		t.Errorf("warning = %+v", w)
	}
	if _, ok := heldWithoutTmpfs(&store.Pool{Name: "zoomies-ci"}, hosts); ok {
		t.Error("a pool with nothing in memory was warned about its hosts")
	}
	if _, ok := heldWithoutTmpfs(pool, []PoolHostRoom{{Host: "new", Tmpfs: true}}); ok {
		t.Error("a pool whose hosts all support it was warned")
	}
	// The room hands the warning only the placeable hosts, so an excluded old
	// agent is never named.
	room := PoolRoom{Hosts: []PoolHostRoom{
		{Host: "new", Tmpfs: true},
		{Host: "excluded-old", Tmpfs: false, ExcludedBy: ExcludedProfile},
	}}
	if _, ok := heldWithoutTmpfs(pool, room.Placeable()); ok {
		t.Error("a host the pool is kept off was named as one it would land on")
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

// The runner's folders and the daemon's image store are charged to different
// containers, each with the typed limit, so each is judged against it on its
// own, and the proposal covers whichever needs more rather than both.
func TestTheImageStoreHasItsOwnShareOfTheMemoryLimit(t *testing.T) {
	pool := func(tmpfs store.TmpfsConfig) *store.Pool {
		return &store.Pool{Name: "zoomies-dind", Backend: store.BackendDocker, DockerMode: store.DockerDinD,
			Resources: store.Resources{MemoryMB: 8192}, Tmpfs: tmpfs}
	}
	// 5 GB of image store in a 8 GB daemon is over half of it.
	tight, ok := tmpfsMemoryWarning(pool(store.TmpfsConfig{Daemon: store.TmpfsMount{Enabled: true, SizeMB: 5000}}))
	if !ok || tight.Severity != config.SeverityWarning || !strings.Contains(tight.Detail, "Docker image store") {
		t.Fatalf("a 5 GB image store in an 8 GB limit was not warned about by name: %+v, ok = %v", tight, ok)
	}
	// 8192 + 5000: the daemon's folder added once.
	if !strings.Contains(tight.Fix, "12.9 GB") {
		t.Errorf("fix %q does not propose 12.9 GB", tight.Fix)
	}
	// Each container fits its own half of the limit: nothing to say, although the
	// two add up to more than the limit, because they are not charged together.
	if w, ok := tmpfsMemoryWarning(pool(store.TmpfsConfig{
		Work:   store.TmpfsMount{Enabled: true, SizeMB: 3000},
		Daemon: store.TmpfsMount{Enabled: true, SizeMB: 4000},
	})); ok {
		t.Errorf("folders in different containers were added together: %+v", w)
	}
}

// On a Docker-in-Docker pool the image store is likely most of the disk
// traffic, so the advice to keep the work folder in memory says it can be kept
// there too -- and why it is a choice of its own.
func TestTheAdviceForADockerInDockerPoolMentionsTheImageStore(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.DockerMode = store.DockerDinD
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	h.diskBoundHost(host, 25*time.Minute, 20_000)
	h.ranJobs(pool, host, 4)

	p := h.problemOrNil("pool.tmpfs_suggested")
	if p == nil {
		t.Fatal("no advice for a pool that ran on a disk-bound host")
	}
	for _, want := range []string{"--tmpfs-docker", "Docker-in-Docker", "fit"} {
		if !strings.Contains(p.Fix, want) {
			t.Errorf("fix %q does not mention %q", p.Fix, want)
		}
	}

	// A pool with no daemon has no image store to suggest.
	pool.DockerMode = store.DockerNone
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	if p := h.problemOrNil("pool.tmpfs_suggested"); p == nil || strings.Contains(p.Fix, "--tmpfs-docker") {
		t.Errorf("a pool with no daemon was told about an image store: %+v", p)
	}
}

// A host that has memory to spare and one that has not cannot both be served by
// one pool setting, so the host has the last word: off keeps its runners on
// disk, a ceiling lowers every folder, and neither changes what the pool says
// for the hosts that say nothing.
func TestAHostHasTheLastWordOnAPoolsInMemoryFolders(t *testing.T) {
	cases := []struct {
		name    string
		profile store.HostTmpfs
		want    store.TmpfsConfig
		wantMax int64
	}{
		{"a host that says nothing leaves the pool's setting alone", store.HostTmpfs{},
			store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}, Tmp: store.TmpfsMount{Enabled: true, SizeMB: 512}}, 0},
		{"a host with a ceiling hands the agent the pool's setting and the ceiling", store.HostTmpfs{MaxMB: 2048},
			store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}, Tmp: store.TmpfsMount{Enabled: true, SizeMB: 512}}, 2048},
		{"a host that turned them off gets no folders and no ceiling", store.HostTmpfs{Disabled: true}, store.TmpfsConfig{}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			inst := h.installation()
			pool := h.pool(inst, "linux-x64")
			pool.MinRunners = 1
			pool.Tmpfs = store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}, Tmp: store.TmpfsMount{Enabled: true, SizeMB: 512}}
			if err := h.st.UpdatePool(h.ctx, pool); err != nil {
				t.Fatalf("UpdatePool: %v", err)
			}
			host := h.measuredHost("measured", 8, 32768, 4, enforcesEverything)
			profile := store.RunnerProfile{Tmpfs: tc.profile}
			if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{RunnerProfile: &profile}); err != nil {
				t.Fatalf("PatchHost: %v", err)
			}

			if err := h.c.Reconcile(h.ctx); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			h.c.lifecycleCalls.Wait()
			task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
			if task.Spec == nil {
				t.Fatal("the create task carries no spec")
			}
			if task.Spec.Tmpfs != tc.want || task.Spec.TmpfsMaxMB != tc.wantMax {
				t.Errorf("spec tmpfs = %+v max %d, want %+v max %d", task.Spec.Tmpfs, task.Spec.TmpfsMaxMB, tc.want, tc.wantMax)
			}
		})
	}
}

// A pool that keeps nothing in memory is not changed by a host that has a
// ceiling: the ceiling is a limit on folders, not a setting that creates them.
func TestAHostCeilingDoesNotTurnOnFoldersThePoolDidNotAskFor(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	pool.MinRunners = 1
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	host := h.measuredHost("measured", 8, 32768, 4, enforcesEverything)
	profile := store.RunnerProfile{Tmpfs: store.HostTmpfs{MaxMB: 1024}}
	if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{RunnerProfile: &profile}); err != nil {
		t.Fatalf("PatchHost: %v", err)
	}
	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	h.c.lifecycleCalls.Wait()
	task := h.taskOfKind(host.ID, agent.TaskCreateRunner)
	if task.Spec == nil || task.Spec.Tmpfs.Any() {
		t.Errorf("spec tmpfs = %+v, want nothing in memory", task.Spec)
	}
}

// A host's owner turning the folders off is a decision, so it is information on
// the pools that land there and not a warning, and it is not mistaken for an
// agent too old to mount them.
func TestAPoolIsToldWhichHostsKeepItsFoldersOnDiskByChoice(t *testing.T) {
	pool := &store.Pool{Name: "zoomies-ci", Tmpfs: store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}}
	hosts := []PoolHostRoom{
		{Host: "roomy", Tmpfs: true},
		{Host: "tight", Tmpfs: true, TmpfsOff: true},
		{Host: "old-and-off", Tmpfs: false, TmpfsOff: true},
	}
	w, ok := keptOnDiskByHost(pool, hosts)
	if !ok || w.Severity != config.SeverityInfo || w.Code != "pool.tmpfs_host_off" {
		t.Fatalf("warning = %+v, ok = %v; want information naming the hosts that turned it off", w, ok)
	}
	if !strings.Contains(w.Detail, "tight") || !strings.Contains(w.Detail, "old-and-off") || strings.Contains(w.Detail, "roomy") {
		t.Errorf("detail %q should name exactly the hosts that turned it off", w.Detail)
	}
	// The same host is not also reported as an agent that cannot do it.
	if u, ok := heldWithoutTmpfs(pool, hosts); ok {
		t.Errorf("a host that turned it off was also called an old agent: %+v", u)
	}
	if _, ok := keptOnDiskByHost(&store.Pool{Name: "zoomies-ci"}, hosts); ok {
		t.Error("a pool with nothing in memory was told about hosts that keep folders off")
	}
}

// The suggestion to turn the work folder on is for a pool on a host that could
// use it, so a host that turned it off is skipped, and one with a ceiling is
// judged against the most a folder could take there.
func TestTheAdviceRespectsAHostsOwnPolicy(t *testing.T) {
	cases := []struct {
		name    string
		profile store.HostTmpfs
		spareMB int64 // memory free beyond the host's own reserve
		want    bool
	}{
		{"a host that turned the folders off is not advised to use them", store.HostTmpfs{Disabled: true}, 20_000, false},
		{"a host with a small ceiling needs only that much to spare", store.HostTmpfs{MaxMB: 512}, 600, true},
		{"a host with a ceiling still needs it to spare", store.HostTmpfs{MaxMB: 512}, 100, false},
		{"a host that says nothing is judged as before", store.HostTmpfs{}, 20_000, true},
		{"and needs the whole default to spare", store.HostTmpfs{}, 600, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			_, pool, host := h.fleet()
			profile := store.RunnerProfile{Tmpfs: tc.profile}
			if err := h.st.PatchHost(h.ctx, host.ID, store.HostChanges{RunnerProfile: &profile}); err != nil {
				t.Fatal(err)
			}
			h.diskBoundHost(host, 25*time.Minute, host.MemoryReserve()+tc.spareMB)
			h.ranJobs(pool, host, 4)
			if got := h.problemOrNil("pool.tmpfs_suggested") != nil; got != tc.want {
				t.Errorf("advised = %v, want %v", got, tc.want)
			}
		})
	}
}

// A pool made or edited without a dry run must still be told that some of its
// hosts keep it on disk: the standing problems carry the sentence, not only the
// validation response.
func TestAnInMemoryPoolOnAnOldAgentIsWarnedInTheProblemsList(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	pool.Tmpfs = store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	host.Features = nil
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	if p := h.problemOrNil("pool.tmpfs_unsupported"); p == nil {
		t.Fatal("no pool.tmpfs_unsupported problem for a host whose agent cannot mount the folders")
	}
}

// A pool whose runners are sized by their host stores no memory limit, so the
// typed-limit warning had nothing to judge and was silent on exactly the pools
// that get small runners. A 5 GB slot with 60% to the daemon leaves the runner
// 2 GB, half of which the folders may take: /tmp came out near 200 MB and every
// Go build died with "no space left on device".
func TestAHostSizedPoolWhoseFoldersAreFittedToSmallRunnersIsWarned(t *testing.T) {
	pool := &store.Pool{Name: "zoomies-ci", DockerMode: store.DockerDinD,
		Resources: store.Resources{DaemonSharePercent: 60},
		Tmpfs: store.TmpfsConfig{
			Work: store.TmpfsMount{Enabled: true}, Tmp: store.TmpfsMount{Enabled: true}, Daemon: store.TmpfsMount{Enabled: true},
		}}
	small := PoolHostRoom{Host: "twelve-core", Tmpfs: true, ChargeMemoryMB: 5120}
	w, ok := hostSizedTmpfsWarning(pool, []PoolHostRoom{small})
	if !ok || w.Code != "pool.tmpfs_memory_tight" || w.Severity != config.SeverityWarning {
		t.Fatalf("warning = %+v, ok = %v; want a warning on the small slot", w, ok)
	}
	for _, want := range []string{"twelve-core", "2 GB", "no space left on device"} {
		if !strings.Contains(w.Detail, want) {
			t.Errorf("detail %q does not say %q", w.Detail, want)
		}
	}
	if !strings.Contains(w.Fix, "--tmpfs-auto") || !strings.Contains(w.Fix, "--tmpfs-tmp=false") || !strings.Contains(w.Fix, "zoomies-ci") {
		t.Errorf("fix %q names no way out", w.Fix)
	}

	roomy := PoolHostRoom{Host: "big", Tmpfs: true, ChargeMemoryMB: 65536}
	if _, ok := hostSizedTmpfsWarning(pool, []PoolHostRoom{roomy}); ok {
		t.Error("a runner with room for the folders unreduced was warned")
	}
	// A host that keeps the folders off, or whose agent cannot mount them, is
	// said elsewhere in its own words, and a slot there is not a size they get.
	off := small
	off.TmpfsOff = true
	old := small
	old.Tmpfs = false
	if _, ok := hostSizedTmpfsWarning(pool, []PoolHostRoom{off, old}); ok {
		t.Error("hosts that do not mount the folders were judged")
	}
	// A typed limit is the other warning's: this one is for what the host decides.
	typed := *pool
	typed.Resources.MemoryMB = 4096
	if _, ok := hostSizedTmpfsWarning(&typed, []PoolHostRoom{small}); ok {
		t.Error("a pool with its own memory limit was judged by its hosts")
	}
	if _, ok := hostSizedTmpfsWarning(&store.Pool{Name: "p"}, []PoolHostRoom{small}); ok {
		t.Error("a pool with nothing in memory was warned")
	}
}

// With no sidecar the runner has the whole slot, so the same slot that starves a
// pair is enough here: the folders are fitted, not cut to a fraction.
func TestAHostSizedPoolWithoutASidecarGetsTheWholeSlot(t *testing.T) {
	pool := &store.Pool{Name: "plain", Tmpfs: store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}}
	if _, ok := hostSizedTmpfsWarning(pool, []PoolHostRoom{{Host: "h", Tmpfs: true, ChargeMemoryMB: 16384}}); ok {
		t.Error("a 16 GB runner with a default work folder was warned")
	}
	w, ok := hostSizedTmpfsWarning(pool, []PoolHostRoom{{Host: "h", Tmpfs: true, ChargeMemoryMB: 4096}})
	if !ok || w.Severity != config.SeverityInfo {
		t.Fatalf("warning = %+v, ok = %v; a 4 GB runner fits half of 4 GB, which is small but not under half of the ask", w, ok)
	}
}

// An automatic folder on disk is the setting working, and is said as such, once,
// with the hosts and what their runners have -- not as a fault.
func TestAutomaticFoldersOnDiskAreSaidAsInformation(t *testing.T) {
	pool := &store.Pool{Name: "zoomies-ci", DockerMode: store.DockerDinD, Resources: store.Resources{DaemonSharePercent: 60},
		Tmpfs: store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, Auto: true}, Daemon: store.TmpfsMount{Enabled: true, Auto: true}}}
	small := PoolHostRoom{Host: "twelve-core", Tmpfs: true, ChargeMemoryMB: 5120}
	roomy := PoolHostRoom{Host: "big", Tmpfs: true, ChargeMemoryMB: 65536}
	w, ok := autoKeptOnDisk(pool, []PoolHostRoom{small, roomy}, nil)
	if !ok || w.Code != "pool.tmpfs_auto_on_disk" || w.Severity != config.SeverityInfo {
		t.Fatalf("warning = %+v, ok = %v", w, ok)
	}
	if !strings.Contains(w.Detail, "twelve-core") || strings.Contains(w.Detail, "big") {
		t.Errorf("detail %q should name only the small host", w.Detail)
	}
	// The tight-fit warning is not raised for what auto put on disk.
	if _, ok := hostSizedTmpfsWarning(pool, []PoolHostRoom{small}); ok {
		t.Error("a folder auto kept on disk was also reported as fitted too small")
	}
	// A pool that is not automatic has nothing to say here.
	manual := *pool
	manual.Tmpfs = store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}
	if _, ok := autoKeptOnDisk(&manual, []PoolHostRoom{small}, nil); ok {
		t.Error("a manual pool was told its folders were put on disk")
	}
	// A host's own standard changes what is asked, so what counts as small.
	big := small
	big.ChargeMemoryMB = 40960
	big.TmpfsPolicy = store.HostTmpfs{WorkMB: 4096}
	if _, ok := autoKeptOnDisk(pool, []PoolHostRoom{big}, nil); ok {
		t.Error("a 40 GB slot with a 4 GB host standard should have room for the folders")
	}
}

// The notice has to say what to do, not only that a folder is on disk: on the
// fleet that raised this, 6 GB runners split evenly leave the runner 3 GB, and
// the work folder needs the runner to have 4. The plan works that out per host
// and the problem says it, with what it costs in slots.
func TestAnAutomaticFolderOnDiskSaysWhatWouldPutItInMemory(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := &store.Host{
		Name: "vm-1", Capacity: 4, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", CPUs: 12, MemoryMB: 32048, DiskTotalMB: 500 * 1024, DiskFreeMB: 400 * 1024,
		LastHeartbeat: time.Now(), Features: store.StringSlice{agent.FeatureTmpfs},
		RunnerProfile: store.RunnerProfile{
			Minimum:  store.RunnerSize{CPUs: 1, MemoryMB: 2048},
			Standard: store.RunnerStandard{CPUs: 2, MemoryMB: 6144},
		},
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	pool.DockerMode = store.DockerDinD
	pool.SizeFromProfile = true
	pool.Resources = store.Resources{MinCPUs: 1, MinMemoryMB: 2048}
	pool.Tmpfs = store.TmpfsConfig{
		Work: store.TmpfsMount{Enabled: true, Auto: true},
		Tmp:  store.TmpfsMount{Enabled: true, Auto: true},
	}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}

	room, err := h.c.PoolRoom(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	plan := room.TmpfsPlan
	if plan == nil || len(plan.Hosts) != 1 || plan.InMemory != 0 || plan.Total != 2 {
		t.Fatalf("plan = %+v; want one host with both folders on disk", plan)
	}
	if len(plan.Sizes) != 1 || plan.Sizes[0].MemoryMB != 8192 || plan.Sizes[0].Host != host.Name {
		t.Fatalf("sizes = %+v; want 8 GB on %s, the runner at which the work folder fits", plan.Sizes, host.Name)
	}
	if plan.Sizes[0].Lever != "standard" {
		t.Errorf("lever = %q; a pool that takes its size from the host profile is changed there", plan.Sizes[0].Lever)
	}
	if plan.Sizes[0].Slots < 1 || plan.Sizes[0].Slots > plan.Sizes[0].SlotsNow {
		t.Errorf("slots = %+v; an 8 GB runner cannot leave more slots than 6 GB did", plan.Sizes[0])
	}
	if plan.Share != nil && plan.Share.InMemory <= plan.Share.Now {
		t.Errorf("share = %+v; a share is only offered if it puts more in memory", plan.Share)
	}

	p := h.problemOrNil("pool.tmpfs_auto_on_disk")
	if p == nil {
		t.Fatal("no pool.tmpfs_auto_on_disk problem")
	}
	// A share that would cost runners is not offered: at 6 GB runners every share
	// that puts the folder in memory raises the slot and loses most of the fleet.
	if plan.Share != nil && plan.Share.Runners < plan.Share.RunnersNow {
		t.Errorf("share = %+v; a share that loses runners must not be offered", plan.Share)
	}
	for _, want := range []string{"standard runner memory", "--standard-memory-mb", host.Name, "8 GB"} {
		if !strings.Contains(p.Fix, want) {
			t.Errorf("fix %q does not say %q", p.Fix, want)
		}
	}
}

// A pool sized by a slot's share of the machine has no standard to raise: its
// runners are bigger when the host has fewer slots, so that is the lever named.
func TestAPoolSizedBySlotShareIsToldToLowerCapacity(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux-x64")
	host := &store.Host{
		Name: "vm-8", Capacity: 8, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
		OS: "linux", Arch: "amd64", CPUs: 16, MemoryMB: 32048, DiskTotalMB: 500 * 1024, DiskFreeMB: 400 * 1024,
		LastHeartbeat: time.Now(), Features: store.StringSlice{agent.FeatureTmpfs},
	}
	if err := h.st.CreateHost(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	pool.DockerMode = store.DockerDinD
	pool.Tmpfs = store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, Auto: true}}
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatal(err)
	}
	room, err := h.c.PoolRoom(h.ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if room.TmpfsPlan == nil || len(room.TmpfsPlan.Sizes) != 1 {
		t.Fatalf("plan = %+v; want one size option", room.TmpfsPlan)
	}
	o := room.TmpfsPlan.Sizes[0]
	if o.Lever != "capacity" || o.Slots >= o.SlotsNow {
		t.Fatalf("option = %+v; want fewer slots as the lever", o)
	}
	p := h.problemOrNil("pool.tmpfs_auto_on_disk")
	if p == nil || !strings.Contains(p.Fix, "--capacity") || strings.Contains(p.Fix, "--standard-memory-mb") {
		t.Fatalf("problem = %+v; want capacity named and no standard size", p)
	}
}

// The presets are priced on the hosts, and the one preselected is one that costs
// nothing: a CPU-skewed split raises a thin half to its minimum and grows the slot,
// which on a small slot loses runners and on a roomy one loses none.
func TestSplitPresetsArePricedOnTheHostsAndTheCheapestSuitableIsPreselected(t *testing.T) {
	plan := func(hostCPUs float64, memoryMB int64, std store.RunnerStandard) *SplitPlan {
		h := newHarness(t)
		inst := h.installation()
		pool := h.pool(inst, "linux-x64")
		host := &store.Host{
			Name: "vm", Capacity: 4, Backends: store.StringSlice{"docker"}, Labels: store.StringMap{},
			OS: "linux", Arch: "amd64", CPUs: int(hostCPUs), MemoryMB: memoryMB, DiskTotalMB: 500 * 1024, DiskFreeMB: 400 * 1024,
			LastHeartbeat: time.Now(), Features: store.StringSlice{agent.FeatureTmpfs},
			RunnerProfile: store.RunnerProfile{Minimum: store.RunnerSize{CPUs: 1, MemoryMB: 2048}, Standard: std},
		}
		if err := h.st.CreateHost(h.ctx, host); err != nil {
			t.Fatal(err)
		}
		pool.DockerMode, pool.SizeFromProfile = store.DockerDinD, true
		pool.Resources = store.Resources{MinCPUs: 1, MinMemoryMB: 2048}
		if err := h.st.UpdatePool(h.ctx, pool); err != nil {
			t.Fatal(err)
		}
		room, err := h.c.PoolRoom(h.ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		return room.SplitPlan
	}

	roomy := plan(32, 128*1024, store.RunnerStandard{CPUs: 4, MemoryMB: 16384})
	if roomy == nil || len(roomy.Options) != 3 {
		t.Fatalf("plan = %+v; want three priced presets", roomy)
	}
	if roomy.Options[0].ID != "even" || roomy.Options[0].Loses {
		t.Errorf("even = %+v; the pool's own division can never lose against itself", roomy.Options[0])
	}
	if roomy.Recommended != "build" {
		t.Errorf("recommended = %q on a roomy fleet, want build: %+v", roomy.Recommended, roomy.Options)
	}

	// A 2-CPU slot cannot give a 70% daemon its comfortable CPU without growing, and on
	// a 12-core host the slots that grow no longer all fit.
	tight := plan(12, 32*1024, store.RunnerStandard{CPUs: 2, MemoryMB: 6144})
	var build *SplitOption
	for i := range tight.Options {
		if tight.Options[i].ID == "build" {
			build = &tight.Options[i]
		}
	}
	if build == nil || !build.Loses {
		t.Fatalf("build = %+v on a small slot; want it priced as losing runners", build)
	}
	if tight.Recommended != "even" {
		t.Errorf("recommended = %q; a preset that loses runners must not be preselected", tight.Recommended)
	}

	// A typed size has no division to choose.
	h := newHarness(t)
	inst := h.installation()
	typed := h.pool(inst, "typed")
	typed.DockerMode, typed.Resources = store.DockerDinD, store.Resources{CPUs: 2, MemoryMB: 4096}
	if err := h.st.UpdatePool(h.ctx, typed); err != nil {
		t.Fatal(err)
	}
	h.host("vm-1")
	if room, err := h.c.PoolRoom(h.ctx, typed); err != nil || room.SplitPlan != nil {
		t.Errorf("a typed pool has a split plan: %+v (err %v)", room.SplitPlan, err)
	}
}

// The share the plan found is offered as a change that can be applied, with what it
// costs, but only on a dind pool somebody made, since one the controller keeps
// refuses edits to its resources. The plan only ever offers a share that loses no runners,
// so the remedy never needs a warning about capacity.
func TestAnAutomaticFolderOnDiskOffersTheDaemonShareAsARemedy(t *testing.T) {
	pool := &store.Pool{ID: "pool_x", Name: "zoomies-ci", DockerMode: store.DockerDinD, Resources: store.Resources{DaemonMemorySharePercent: 50, MinCPUs: 0.5},
		Tmpfs: store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true, Auto: true}}}
	small := PoolHostRoom{Host: "twelve-core", Tmpfs: true, ChargeMemoryMB: 5120}
	plan := &TmpfsPlan{Total: 8, Share: &TmpfsShareOption{Percent: 15, InMemory: 7, Now: 4, Runners: 6, RunnersNow: 6}}

	w, ok := autoKeptOnDisk(pool, []PoolHostRoom{small}, plan)
	if !ok || w.Remedy == nil {
		t.Fatalf("problem = %+v, ok = %v; want a remedy", w, ok)
	}
	if w.Remedy.Kind != RemedyPoolUpdate || w.Remedy.TargetID != "pool_x" || !strings.Contains(w.Remedy.Effect, "7 of 8 folders") {
		t.Errorf("remedy = %+v", w.Remedy)
	}
	var body struct {
		Resources store.Resources `json:"resources"`
	}
	if err := json.Unmarshal(w.Remedy.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Resources.DaemonMemorySharePercent != 15 || body.Resources.MinCPUs != 0.5 {
		t.Errorf("the request must be the pool's own resources with only the share changed: %+v", body.Resources)
	}

	if w, _ := autoKeptOnDisk(pool, []PoolHostRoom{small}, nil); w.Remedy != nil {
		t.Error("without a plan there is nothing priced to offer")
	}
	kept := *pool
	kept.AutoKey = "amd64/large"
	if w, _ := autoKeptOnDisk(&kept, []PoolHostRoom{small}, plan); w.Remedy != nil {
		t.Error("a pool the controller keeps refuses edits to its resources, so none is offered")
	}
}

// An agent too old to divide a slot by the pool's sidecar shares splits it evenly,
// which is safe and invisible: the pool reads 80% and the daemon is given half. The
// pool is told, but only if it asked for something other than the even split, and only
// for hosts it can land on.
func TestADindPoolWithAShareOnAnAgentThatIgnoresItIsWarned(t *testing.T) {
	shared := &store.Pool{Name: "zoomies-ci", DockerMode: store.DockerDinD, Resources: store.Resources{DaemonSharePercent: 80}}
	hosts := []PoolHostRoom{{Host: "new", DaemonShare: true}, {Host: "old"}}
	w, ok := heldWithoutDaemonShare(shared, hosts)
	if !ok || w.Code != "pool.daemon_share_unsupported" || !strings.Contains(w.Detail, "old") || strings.Contains(w.Detail, "new,") {
		t.Fatalf("warning = %+v, ok = %v; want only the old host named", w, ok)
	}
	even := &store.Pool{Name: "zoomies-ci", DockerMode: store.DockerDinD}
	if _, ok := heldWithoutDaemonShare(even, hosts); ok {
		t.Error("a pool on the even split was warned about a split every agent already makes")
	}
	if _, ok := heldWithoutDaemonShare(shared, []PoolHostRoom{{Host: "new", DaemonShare: true}}); ok {
		t.Error("a pool whose hosts all follow the share was warned")
	}
	plain := &store.Pool{Name: "zoomies-ci", Resources: store.Resources{DaemonSharePercent: 80}}
	if _, ok := heldWithoutDaemonShare(plain, hosts); ok {
		t.Error("a pool with no sidecar was warned about its share")
	}
	room := PoolRoom{Hosts: []PoolHostRoom{{Host: "new", DaemonShare: true}, {Host: "excluded-old", ExcludedBy: ExcludedProfile}}}
	if _, ok := heldWithoutDaemonShare(shared, room.Placeable()); ok {
		t.Error("a host the pool is kept off was named as one it would land on")
	}
}
