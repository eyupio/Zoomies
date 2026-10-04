package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// A pool that keeps its work folder or /tmp in memory spends its memory limit
// on disk speed. Three things can go wrong with that without anything failing
// loudly, and each is said where the pool is edited rather than found out from
// a job that ran out of memory.

// tmpfsMemoryWarning proposes the memory limit a pool's in-memory folders need.
//
// A tmpfs is charged to the runner's own cgroup, so the room it may fill comes
// out of the limit the pool was sized with for the job. Switching it on without
// raising the limit leaves the job less than it had, which shows up much later
// as an out-of-memory kill in a build that always passed. The proposal is the
// limit plus what the folders may fill: the same room for the job, with the
// folders on top. It is a proposal and never an edit, because the limit is also
// what the scheduler charges the host for, and raising it changes how many
// runners fit.
//
// Two cases speak. Sizes an operator typed that take more than half the limit
// are a warning, because a job that fills them is out of memory. Folders left
// to size themselves are fitted into half the limit, so they cannot do that;
// when the limit is too small to give them their defaults the fitting is only
// information -- they work, and are smaller than they would be.
func tmpfsMemoryWarning(p *store.Pool) (Problem, bool) {
	limit := p.Resources.MemoryMB
	if limit <= 0 || !p.Tmpfs.Any() {
		return Problem{}, false
	}
	// A typed limit is given to the runner and to the daemon alike, and each is
	// charged for its own folders, so each is judged against it separately: the
	// work folder and /tmp are the runner's, the image store is the daemon's.
	work, tmp := p.Tmpfs.Sizes(limit)
	runner := work + tmp
	daemon := p.Tmpfs.DaemonSize(limit)
	severity := config.SeverityInfo
	var title, detail string
	switch {
	case runner*2 > limit || daemon*2 > limit:
		severity = config.SeverityWarning
		title = fmt.Sprintf("pool %s: its in-memory folders may take more than half of its memory limit", p.Name)
		detail = fmt.Sprintf("a tmpfs is charged to the memory limit of the container it is in, so the job has what the folders leave. "+
			"This pool's limit is %s and %s, so a job that fills them is killed for want of memory.",
			formatRoomMB(limit), tightFolders(runner, daemon, limit))
	case runner < p.Tmpfs.ReserveMB() || daemon < p.Tmpfs.DaemonReserveMB():
		title = fmt.Sprintf("pool %s: its in-memory folders were fitted to a small memory limit", p.Name)
		detail = fmt.Sprintf("a tmpfs is charged to the memory limit of the container it is in, so folders left to size themselves are fitted into half of it. "+
			"This pool's limit is %s, which is not enough to give them the size they would be given with room.",
			formatRoomMB(limit))
	default:
		return Problem{}, false
	}
	return Problem{
		Code:     "pool.tmpfs_memory_tight",
		Severity: severity,
		Title:    title,
		Detail:   detail,
		Fix: fmt.Sprintf("raise the pool's memory limit to %s, which leaves the job the %s it has now with the folders on top, or shrink the folders.",
			formatRoomMB(p.Tmpfs.RecommendedMemoryMB(limit)), formatRoomMB(limit)),
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// tightFolders says which container's folders take more than half the limit.
func tightFolders(runner, daemon, limit int64) string {
	switch {
	case runner*2 > limit && daemon*2 > limit:
		return fmt.Sprintf("the runner's folders may fill %s and the Docker image store %s", formatRoomMB(runner), formatRoomMB(daemon))
	case daemon*2 > limit:
		return fmt.Sprintf("the Docker image store may fill %s", formatRoomMB(daemon))
	default:
		return fmt.Sprintf("its folders may fill %s", formatRoomMB(runner))
	}
}

// cacheInMemoryWarning is a pool cache kept in a memory-backed directory with
// nothing to stop it growing.
//
// The pool cache is shared by one runner after another, which is why it cannot
// be a tmpfs of each container's own -- every runner would start with it cold.
// The form that works is a directory the host mounts in memory, such as
// /dev/shm, given as the cache source. There the host's RAM is the disk, and
// the only thing evicting from it is the cache's size limit: without one the
// cache grows until the host is out of memory, which takes every runner and the
// agent with it. The limit is refused on a named volume because it cannot be
// measured there, and is accepted on this path because it can.
func cacheInMemoryWarning(p *store.Pool) (Problem, bool) {
	if !p.Cache.Enabled || p.Cache.SizeLimit > 0 {
		return Problem{}, false
	}
	source := filepath.Clean(strings.TrimSpace(p.Cache.Source))
	if source != "/dev/shm" && !strings.HasPrefix(source, "/dev/shm/") {
		return Problem{}, false
	}
	return Problem{
		Code:     "pool.cache_memory_unbounded",
		Severity: config.SeverityWarning,
		Title:    fmt.Sprintf("pool %s: its cache is kept in memory and has no size limit", p.Name),
		Detail: "the cache source is under /dev/shm, which is the host's memory. The cache is evicted only down to its size limit, " +
			"so with none it grows until the host has no memory left, and that takes the runners and the agent with it.",
		Fix:        "set the cache's size limit, keeping it well inside what the smallest host can spare in memory, or move the cache source to a directory on disk.",
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// heldWithoutTmpfs names the hosts on which an in-memory pool is not in memory.
//
// Mounting the folders is the agent's work, and an agent too old to advertise
// that it can ignores the setting and starts the runner on disk: a safe
// outcome, and an invisible one, for the same reason heldByOldAgents gives for
// elastic CPU. The warning is where the pool is saved, with the hosts named.
//
// Only the hosts the pool can be placed on count: one its runner profile keeps
// the pool off is listed in the room with no room, and is not a host the pool
// will ever land on, so naming it would be a warning about nothing.
func heldWithoutTmpfs(p *store.Pool, hosts []PoolHostRoom) (Problem, bool) {
	if !p.Tmpfs.Any() {
		return Problem{}, false
	}
	var names []string
	for _, h := range hosts {
		// A host whose operator turned the folders off is not running an old agent
		// as far as the pool is concerned: that is its own, deliberate, answer.
		if !h.Tmpfs && !h.TmpfsOff {
			names = append(names, h.Host)
		}
	}
	if len(names) == 0 {
		return Problem{}, false
	}
	runs := "run"
	if len(names) == 1 {
		runs = "runs"
	}
	return Problem{
		Code:     "pool.tmpfs_unsupported",
		Severity: config.SeverityWarning,
		Title: fmt.Sprintf("pool %s: %d of its %s %s an agent that cannot keep folders in memory",
			p.Name, len(names), plural(len(hosts), "host"), runs),
		Detail: "the agent on a host mounts the in-memory folders, and these agents are too old to say they can: " +
			strings.Join(names, ", ") + ". A runner placed there starts with its folders on disk, exactly as with the setting off, " +
			"and nothing on the pool says which of its runners that happened to.",
		Fix: "upgrade the agent on those hosts -- the command is on each host's card under Hosts -- " +
			"or point the pool at other hosts with its host selector until they are.",
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// keptOnDiskByHost names the hosts where an operator has turned in-memory
// folders off.
//
// It is information and not a warning: a machine's owner knows how much memory
// it has and the pool's does not, so the host having the last word is the design,
// not a mistake. It is said anyway, where the pool is saved, because a pool that
// asks for folders in memory and is placed on such a host runs there exactly as
// it always did, and nothing on the pool says which of its runners that was.
func keptOnDiskByHost(p *store.Pool, hosts []PoolHostRoom) (Problem, bool) {
	if !p.Tmpfs.Any() {
		return Problem{}, false
	}
	var names []string
	for _, h := range hosts {
		if h.TmpfsOff {
			names = append(names, h.Host)
		}
	}
	if len(names) == 0 {
		return Problem{}, false
	}
	return Problem{
		Code:     "pool.tmpfs_host_off",
		Severity: config.SeverityInfo,
		Title: fmt.Sprintf("pool %s: %s keep in-memory folders off", p.Name,
			plural(len(names), "host")),
		Detail: "an operator turned in-memory folders off in the runner profile of " + strings.Join(names, ", ") +
			", so a runner of this pool placed there has its folders on disk, as it would with the setting off.",
		Fix: "nothing, if that is what the host's owner meant. To use memory there, clear \"Keep in-memory folders off this machine\" under Runner sizes on the host's card " +
			"-- or run zoomies hosts edit with --tmpfs-off=false -- or point this pool at other hosts with its host selector.",
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// hostSupportsTmpfs is whether a host's agent mounts in-memory folders.
func hostSupportsTmpfs(h *store.Host) bool { return h.Supports(agent.FeatureTmpfs) }

const (
	// tmpfsAdviceWindow is how far back a pool must have run jobs on a host for
	// that host to say anything about the pool. I/O wait is the whole machine's,
	// and a host can be waiting on a disk that none of this pool's jobs touch.
	tmpfsAdviceWindow = 6 * time.Hour
	// tmpfsAdviceMinJobs keeps a single job that happened to land there from
	// speaking for the pool.
	tmpfsAdviceMinJobs = 3
)

// tmpfsAdviceProblems suggests keeping the work folder in memory, to the pool
// that would gain from it.
//
// It speaks only when three things are all true of one host: the pool has run
// jobs there recently, the host has been waiting on its disk for a sustained
// stretch (HostUsage.DiskBound -- the figure CPU occupancy hides, since it
// counts that wait as busy), and it has the memory to spare for the folder
// beyond its own reserve. Each is there because without it the advice is wrong
// or unusable: a pool whose jobs never touched that disk is not helped by
// moving its folder, and a host with no memory left cannot take one.
//
// Nothing is stored. The list is recomputed every pass, so the suggestion stays
// for exactly as long as the three are true -- continued, as an operator asked,
// rather than raised once and forgotten -- and goes the moment the pool turns
// the setting on, the disk calms, or the memory is spent. Since is when the
// earliest of those hosts began waiting, which a standing condition does not
// move, so the list is not rewritten while it stands.
//
// It is information, never a warning: nothing is failing, only slower than it
// needs to be, and the setting trades memory for it.
func (c *Controller) tmpfsAdviceProblems(ctx context.Context, out *[]Problem) error {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	var candidates []*store.Pool
	for _, p := range pools {
		if !p.Enabled || p.Tmpfs.Work.Enabled {
			continue
		}
		if p.Backend != store.BackendDocker && p.Backend != store.BackendPodman {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return nil
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	now := c.Now()
	var bound []*store.Host
	for _, h := range hosts {
		// A host whose operator turned the folders off cannot be helped by a
		// suggestion to use them.
		if h.Cordoned || h.Incompatible || h.RunnerProfile.Tmpfs.Disabled || !h.Usage.DiskBound(now) {
			continue
		}
		bound = append(bound, h)
	}
	if len(bound) == 0 {
		return nil
	}
	since := now.Add(-tmpfsAdviceWindow)
	stats, err := c.st.JobStats(ctx, store.JobFilter{Since: &since}, []string{store.GroupByPool, store.GroupByHost})
	if err != nil {
		return fmt.Errorf("counting recent jobs: %w", err)
	}
	ran := make(map[[2]string]int, len(stats.Groups))
	for _, g := range stats.Groups {
		ran[[2]string{g.Keys[store.GroupByPool], g.Keys[store.GroupByHost]}] += g.Count
	}

	// What the folder would be given on an empty runner is what the host must
	// have to spare: the default, since the pool has not chosen a size.
	room := store.TmpfsConfig{Work: store.TmpfsMount{Enabled: true}}
	for _, p := range candidates {
		var names []string
		var earliest *time.Time
		for _, h := range bound {
			if ran[[2]string{p.ID, h.ID}] < tmpfsAdviceMinJobs {
				continue
			}
			// What the folder needs from this host is what it would be given here, which
			// the host's own ceiling may lower.
			need := room.ReserveMB()
			if ceiling := h.RunnerProfile.Tmpfs.MaxMB; ceiling > 0 {
				need = min(need, ceiling)
			}
			avail := h.Usage.MemoryAvailableMB
			if avail == nil || *avail-h.MemoryReserve() < need {
				continue
			}
			names = append(names, h.Name)
			if at := h.Usage.IOWaitHighSince; at != nil && (earliest == nil || at.Before(*earliest)) {
				earliest = at
			}
		}
		if len(names) == 0 {
			continue
		}
		sort.Strings(names)
		fix := fmt.Sprintf("turn on the in-memory work folder for pool %s -- Keep the work folder in memory in the pool editor, or zoomies pools edit with --tmpfs-work -- and compare a few runs.", p.Name)
		if p.DockerMode == store.DockerDinD {
			// The sidecar's image store is where a dind job's pulls and builds are
			// written, so on a pool like this it is likely the larger share of the
			// traffic; it is its own choice because an image bigger than the mount
			// does not pull.
			fix += " This pool builds inside Docker-in-Docker, where the daemon writes every pulled image and built layer: its image store can be kept in memory too (--tmpfs-docker), if the images it pulls fit."
		}
		if rec := room.RecommendedMemoryMB(p.Resources.MemoryMB); rec > 0 {
			fix += fmt.Sprintf(" The folder is charged to the runner memory limit, so raise that limit to %s to leave the job the room it has now.", formatRoomMB(rec))
		}
		*out = append(*out, Problem{
			Code:     "pool.tmpfs_suggested",
			Severity: config.SeverityInfo,
			Title:    fmt.Sprintf("pool %s: its jobs have been waiting on disk while its machines have memory to spare", p.Name),
			Detail: fmt.Sprintf("%s: the disk has been the bottleneck for at least ten minutes, with I/O wait above %.0f%%, and this pool has run jobs there since. "+
				"A checkout, an install or a build writes a great many small files, and on a disk that cannot keep up that is where a job spends its time. "+
				"Each of these machines has memory free beyond its reserve -- at least %s -- which is room for the work folder to live in memory instead.",
				strings.Join(names, ", "), store.IOWaitHighPercent, formatRoomMB(room.ReserveMB())),
			Fix:        fix,
			TargetKind: "pool",
			TargetID:   p.ID,
			Since:      earliest,
		})
	}
	return nil
}
