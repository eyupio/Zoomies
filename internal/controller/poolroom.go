package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// How much machine a pool asks for, against the machines it may land on.
//
// HostFit next door answers "could this pool run anywhere at all", which is a
// yes or a no. This answers the question underneath the two settings an
// operator actually gets wrong: how big one runner is, and how many of them
// there may be. A pool of eight-core runners on three four-core boxes is
// valid, matches its hosts and starts nothing; a pool whose maximum is forty
// on a fleet with room for eleven is valid, healthy, and queues twenty-nine
// jobs behind runners that will never be created. Neither is visible anywhere
// a pool is edited unless something counts it.
//
// It is counted on an empty fleet on purpose: what is running right now
// changes with every job, and a size and a cap chosen against this minute's
// occupancy would be re-chosen the next. The question is what the machines can
// hold, which is a property of the machines.

// PoolHostRoom is one host's answer, in the terms the machine was set up in.
type PoolHostRoom struct {
	HostID string `json:"host_id"`
	Host   string `json:"host"`
	// Slots is the host's runner capacity, less any throttle in force.
	Slots int `json:"slots"`
	// Fits is how many runners of this pool the machine has room for,
	// ignoring the slot count -- so a host whose slots outrun its machine can
	// be told from one sized to match.
	Fits int `json:"fits"`
	// Room is what the pool can actually expect here: the smaller of the two.
	Room int `json:"room"`
	// LimitedBy is what ran out first: slots, cpu, memory or disk. It is empty
	// on a host that has measured nothing, where only its slots bind.
	LimitedBy string `json:"limited_by,omitempty"`
	// ChargeCPUs and ChargeMemoryMB are what one runner of this pool costs
	// here, which is not always what the pool says: a docker-in-docker runner
	// is charged for its sidecar too.
	ChargeCPUs     float64 `json:"charge_cpus"`
	ChargeMemoryMB int64   `json:"charge_memory_mb"`
	// The machine less its reserve, which is what the charge is taken from.
	// Zero with the matching known flag false means the agent has not said.
	CPUs        float64 `json:"cpus"`
	MemoryMB    int64   `json:"memory_mb"`
	DiskMB      int64   `json:"disk_mb"`
	CPUsKnown   bool    `json:"cpus_known"`
	MemoryKnown bool    `json:"memory_known"`
	DiskKnown   bool    `json:"disk_known"`
	// ElasticCPU is whether this host's agent can move a live runner's CPU
	// quota. An elastic pool is honoured only where it is true; a runner of
	// one placed elsewhere is held at its share, and nothing but this says so
	// while the pool is still being edited.
	ElasticCPU bool `json:"elastic_cpu"`
	// Tmpfs is whether this host's agent mounts a pool's in-memory folders. A
	// pool that asks for them is honoured only where it is true.
	Tmpfs bool `json:"tmpfs"`
	// TmpfsOff is whether this host's operator turned in-memory folders off, in
	// its runner profile. A pool that asks for them is not given them here, which
	// is a decision of the host's owner and not a fault, but is not something the
	// pool's own page can show unless somebody says it.
	TmpfsOff bool `json:"tmpfs_off,omitempty"`
	// TmpfsPolicy is this host's whole say over in-memory folders (its standard
	// sizes and ceiling), which the warnings place a pool's folders from. It is
	// not part of the answer an editor shows, so it is not sent.
	TmpfsPolicy store.HostTmpfs `json:"-"`
	// Sizing is how a runner of this pool is sized here -- its standard, its
	// floor and its CPU ceiling, each with where it came from -- and Excluded
	// is the sentence, with ExcludedBy its code, where the host's runner profile
	// keeps the pool off it altogether. Excluded hosts are listed, with no room,
	// rather than dropped: "this host is left out, and this is the limit" is the
	// row an operator came to read.
	Sizing     PoolHostSizing `json:"sizing"`
	Excluded   string         `json:"excluded,omitempty"`
	ExcludedBy string         `json:"excluded_by,omitempty"`
}

// Overcommitted reports whether this host promises more slots than the machine
// can back at this pool's size -- the state where the fleet's own capacity
// figures say there is room and every create for that room is refused.
//
// A host the pool is kept off is not one. It is listed so its row can say why,
// but the pool is never asked to place a runner there, so its slots are not
// promises about this pool, and telling an operator to lower the capacity of a
// host that is working as set would be the wrong fix for a right result.
func (r PoolHostRoom) Overcommitted() bool { return r.ExcludedBy == "" && r.Slots > r.Fits }

// Placeable is the hosts a runner of this pool can be placed on. An excluded
// host is listed with no room rather than dropped, and takes no part in any
// count, charge or warning about where the pool lands.
func (r PoolRoom) Placeable() []PoolHostRoom {
	out := make([]PoolHostRoom, 0, len(r.Hosts))
	for _, h := range r.Hosts {
		if h.ExcludedBy == "" {
			out = append(out, h)
		}
	}
	return out
}

// PoolRoom is the fleet's answer, and the per-host detail behind it.
type PoolRoom struct {
	// Runners is how many runners of this pool the matching hosts could hold
	// between them. It is the number a maximum is worth comparing against.
	Runners int `json:"runners"`
	// Slots is what those hosts' capacities add up to, whatever the size.
	Slots int            `json:"slots"`
	Hosts []PoolHostRoom `json:"hosts"`
	// SmallestDiskMB is the least free disk on any matching host, which is
	// what a cache size limit has to live inside. Known is false when no
	// matching host has measured its disk.
	SmallestDiskMB   int64  `json:"smallest_disk_mb"`
	SmallestDiskHost string `json:"smallest_disk_host,omitempty"`
	DiskKnown        bool   `json:"disk_known"`
}

// Overcommitted is every host promising more slots than it can back.
func (r PoolRoom) Overcommitted() []PoolHostRoom {
	var out []PoolHostRoom
	for _, h := range r.Hosts {
		if h.Overcommitted() {
			out = append(out, h)
		}
	}
	return out
}

// PoolRoom counts the room a pool has across the hosts that can run it.
//
// Only the hosts that could run it at all are counted: a host turned down for
// its backend or its platform has no room for this pool whatever its size, and
// HostFit already says so in its own words. Counting it here would put machine
// the pool can never reach into the number an operator sets a maximum from.
func (c *Controller) PoolRoom(ctx context.Context, p *store.Pool) (PoolRoom, error) {
	raw, fleet := p, c.cfg().Runners
	p = sizingPool(p, fleet)
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return PoolRoom{}, err
	}
	now := c.Now()
	out := PoolRoom{Hosts: []PoolHostRoom{}}
	for _, h := range hosts {
		if !scheduler.HostSelects(h, p) || !scheduler.HostAvailable(h, now) {
			continue
		}
		code, reason := HostRefusal(h, p)
		if code != "" && code != ExcludedSize && code != ExcludedProfile {
			continue
		}
		entry := poolHostRoom(h, p)
		entry.Sizing = poolHostSizing(raw, p, h, fleet)
		if code == ExcludedProfile {
			entry.Excluded, entry.ExcludedBy = reason, code
		}
		alloc := h.Allocatable()
		out.Hosts = append(out.Hosts, entry)
		out.Runners += entry.Room
		out.Slots += entry.Slots
		if alloc.DiskKnown && (!out.DiskKnown || alloc.DiskMB < out.SmallestDiskMB) {
			out.SmallestDiskMB, out.SmallestDiskHost, out.DiskKnown = alloc.DiskMB, h.Name, true
		}
	}
	return out, nil
}

// poolHostRoom is one host's line in PoolRoom.
//
// The charge is what a runner there is really given. On a machine too small
// for the pool's standard size that is the reduced size the pass places --
// all the host can spare, never below the minimum -- and quoting the standard
// there would tell an operator the pool is charged a size no runner on that
// host is ever created at.
func poolHostRoom(h *store.Host, p *store.Pool) PoolHostRoom {
	room := scheduler.HostRoomFor(h, p)
	alloc := h.Allocatable()
	charge := scheduler.Reserve(p, h)
	whole := scheduler.Reservation{CPUs: alloc.CPUs, MemoryMB: alloc.MemoryMB, DiskMB: alloc.DiskMB}
	if reduced, _, ok := scheduler.ReducedSize(p, h, whole, alloc); ok {
		charge = reduced
	}
	return PoolHostRoom{
		HostID:         h.ID,
		Host:           h.Name,
		Slots:          room.Slots,
		Fits:           room.Fits,
		Room:           room.Room,
		LimitedBy:      room.LimitedBy,
		ChargeCPUs:     charge.CPUs,
		ChargeMemoryMB: charge.MemoryMB,
		CPUs:           alloc.CPUs,
		MemoryMB:       alloc.MemoryMB,
		DiskMB:         alloc.DiskMB,
		CPUsKnown:      alloc.CPUsKnown,
		MemoryKnown:    alloc.MemoryKnown,
		DiskKnown:      alloc.DiskKnown,
		ElasticCPU:     h.Supports(agent.FeatureElasticCPU),
		Tmpfs:          hostSupportsTmpfs(h),
		TmpfsOff:       h.RunnerProfile.Tmpfs.Disabled,
		TmpfsPolicy:    h.RunnerProfile.Tmpfs,
	}
}

// PoolRoomWarnings is what the room says about the pool's own figures.
//
// All three are the same class of failure: a pool that is valid, matches its
// hosts, shows green on every page, and cannot do what its settings promise.
// Nothing else catches them, because each is a disagreement between two
// settings that are correct on their own -- a maximum and a fleet, a host's
// slots and a runner's size, a cache limit and a disk.
func PoolRoomWarnings(p *store.Pool, room PoolRoom) []Problem {
	var out []Problem
	placeable := room.Placeable()
	if len(placeable) == 0 {
		// A pool no host can run is already said in the fleet's own words,
		// and saying it again as arithmetic helps nobody.
		return out
	}

	if p.MaxRunners > room.Runners {
		out = append(out, Problem{
			Code:     "pool.max_above_room",
			Severity: config.SeverityWarning,
			Title: fmt.Sprintf("pool %s: its maximum is %d runners and its hosts have room for %d",
				p.Name, p.MaxRunners, room.Runners),
			Detail: fmt.Sprintf("one runner of this pool is charged %s CPU and %s, and the %s it can land on %s room for %s at that size. "+
				"The maximum is a backstop rather than a target, so this is not wrong -- but %s above the room are runners the scheduler will never create, "+
				"and the jobs that ask for them wait with nothing on any page saying why.",
				scheduler.FormatCPUs(chargeCPUs(placeable)), formatRoomMB(chargeMemoryMB(placeable)),
				plural(len(placeable), "host"), verb(len(placeable)), plural(room.Runners, "runner"),
				plural(p.MaxRunners-room.Runners, "runner")),
			Fix:        fmt.Sprintf("lower the maximum to %d, ask for less per runner, or give the pool more hosts.", room.Runners),
			TargetKind: "pool",
			TargetID:   p.ID,
		})
	}

	if over := room.Overcommitted(); len(over) > 0 {
		names := make([]string, 0, len(over))
		for _, h := range over {
			names = append(names, fmt.Sprintf("%s (%d slots, room for %d)", h.Host, h.Slots, h.Fits))
		}
		out = append(out, Problem{
			Code:     "pool.host_overcommitted",
			Severity: config.SeverityWarning,
			Title: fmt.Sprintf("pool %s: %s promises more slots than it can back at this size",
				p.Name, plural(len(over), "host")),
			Detail: "the slots above what the machine has room for read as free capacity on every page that counts them, " +
				"and every create for one of them is refused for want of CPU or memory: " + strings.Join(names, ", ") + ".",
			Fix: "adjust those hosts to the slots their machines can back, give this pool's runners less, " +
				"or clear its CPU and memory so each runner is given one slot's share of whatever host it lands on -- " +
				"which is the one size that cannot outrun a slot.",
			TargetKind: "pool",
			TargetID:   p.ID,
		})
	}

	if w, ok := strandedByFixedSize(p, room); ok {
		out = append(out, w)
	}

	if w, ok := heldByOldAgents(p, placeable); ok {
		out = append(out, w)
	}

	if w, ok := heldWithoutTmpfs(p, placeable); ok {
		out = append(out, w)
	}

	if w, ok := keptOnDiskByHost(p, placeable); ok {
		out = append(out, w)
	}

	if w, ok := hostSizedTmpfsWarning(p, placeable); ok {
		out = append(out, w)
	}

	if w, ok := autoKeptOnDisk(p, placeable); ok {
		out = append(out, w)
	}

	if p.Cache.Enabled && p.Cache.SizeLimit > 0 && room.DiskKnown {
		limitMB := p.Cache.SizeLimit / (1024 * 1024)
		if limitMB > room.SmallestDiskMB {
			out = append(out, Problem{
				Code:     "pool.cache_above_disk",
				Severity: config.SeverityWarning,
				Title: fmt.Sprintf("pool %s: its cache may grow to %s and %s has %s free",
					p.Name, formatRoomMB(limitMB), room.SmallestDiskHost, formatRoomMB(room.SmallestDiskMB)),
				Detail: "the cache is evicted down to its limit between one runner and the next, so a limit above the free space is not a limit at all: " +
					"the disk fills first, and a host at or below its disk reserve takes no runner of any pool.",
				Fix:        fmt.Sprintf("keep the limit inside what the smallest host can spare -- %s or less -- or free space there.", formatRoomMB(room.SmallestDiskMB)),
				TargetKind: "pool",
				TargetID:   p.ID,
			})
		}
	}
	return out
}

// strandedByFixedSize names the capacity a pool's fixed size is leaving on the
// floor, and the one edit that would take it back.
//
// A pool sized by its host gets one slot's share of each machine, so every
// host fills every slot it has: that is what dividing the machine by the slot
// count means. A fixed size can only match that on the hosts that happen to be
// the size it was chosen for -- and a fleet acquires unequal machines as a
// matter of course, one 8-core box and then a 64-core one. The 8-core figure
// then fits four runners on a machine with room for thirty-one, and nothing on
// any page says the pool is the reason.
//
// It is not the same as pool.host_overcommitted, which is about a host
// promising slots its machine cannot back; this is about a pool not using
// slots its hosts can. Both can be true at once, on different hosts.
//
// Only real stranding counts. A host is stranded when its machine could hold
// more runners of this pool than its slot count allows -- Fits above Slots --
// because that is the state where the size is not what binds, and the
// automatic share would have used every slot instead.
func strandedByFixedSize(p *store.Pool, room PoolRoom) (Problem, bool) {
	if p.Automatic() {
		return Problem{}, false
	}
	var names []string
	stranded := 0
	for _, h := range room.Hosts {
		// Slots above Fits is the overcommitted case, which has its own
		// problem and its own fix. This is the other direction: machine left
		// over that the slot count forbids using.
		if !h.CPUsKnown && !h.MemoryKnown {
			continue
		}
		if h.Fits <= h.Slots {
			continue
		}
		stranded += h.Fits - h.Slots
		names = append(names, fmt.Sprintf("%s (%d slots, room for %d at this size)", h.Host, h.Slots, h.Fits))
	}
	if stranded == 0 {
		return Problem{}, false
	}
	return Problem{
		Code:     "pool.size_strands_hosts",
		Severity: config.SeverityInfo,
		Title: fmt.Sprintf("pool %s: its fixed size is smaller than its hosts' share",
			p.Name),
		Detail: fmt.Sprintf("this pool asks for %s CPU and %s on every host, and %s could each hold more runners of it than their slot count allows: %s. "+
			"The slot count is what binds, so the machine above it goes unused -- %s worth across the fleet.",
			scheduler.FormatCPUs(p.Resources.CPUs), formatRoomMB(p.Resources.MemoryMB),
			plural(len(names), "host"), strings.Join(names, ", "), plural(stranded, "runner")),
		Fix:        "raise those hosts' capacity to the runners they can hold, or clear this pool's CPU and memory so each runner is given one slot's share of the host it lands on -- which fills every slot on every machine, whatever size it is.",
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// heldByOldAgents names the hosts on which an elastic pool is not elastic.
//
// Lending CPU means moving a live runner's cgroup quota, and the agent is the
// only thing on the host that can. An agent too old to advertise that it can
// is sent no directive and the runner stays at its guaranteed share -- which
// is a correct, safe outcome, and an invisible one: the pool says automatic,
// the runner says nothing was lent, and the metric that counts the decision as
// unsupported_agent is not a page anybody edits a pool from. This is the
// warning in the place the pool is saved, with the hosts named, because the
// only thing the controller cannot do about it is upgrade the agent itself:
// agents connect outbound, and a binary is replaced on the host.
//
// Observe mode raises nothing. It measures and never moves a quota, so the
// agent's part is never asked of it.
func heldByOldAgents(p *store.Pool, hosts []PoolHostRoom) (Problem, bool) {
	if !p.CPUBurst.Enforces() {
		return Problem{}, false
	}
	var names []string
	for _, h := range hosts {
		if !h.ElasticCPU {
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
		Code:     "pool.elastic_cpu_unsupported",
		Severity: config.SeverityWarning,
		Title: fmt.Sprintf("pool %s: %d of its %s %s an agent that cannot lend CPU",
			p.Name, len(names), plural(len(hosts), "host"), runs),
		Detail: "elastic CPU moves a live runner's quota through the agent on its host, and these agents are too old to say they can: " +
			strings.Join(names, ", ") + ". A runner placed there is held at its guaranteed share, exactly as with elastic CPU off, " +
			"and nothing on the pool says which of its runners that happened to.",
		Fix: "upgrade the agent on those hosts -- the command is on each host's card under Hosts -- " +
			"or keep this pool on observe, which needs nothing of the agent, until they are.",
		TargetKind: "pool",
		TargetID:   p.ID,
	}, true
}

// verb keeps "the host it can land on has" from reading as "hosts ... has".
func verb(n int) string {
	if n == 1 {
		return "has"
	}
	return "have"
}

// The charge is the same on every host that reports its machine, and differs
// only where a host's own share is standing in for a figure the pool did not
// give -- which, now that every pool has a size, is a pool made before it was
// mandatory. The first host that can run it is the one quoted.
func chargeCPUs(hosts []PoolHostRoom) float64 {
	if len(hosts) == 0 {
		return 0
	}
	return hosts[0].ChargeCPUs
}

func chargeMemoryMB(hosts []PoolHostRoom) int64 {
	if len(hosts) == 0 {
		return 0
	}
	return hosts[0].ChargeMemoryMB
}

// formatRoomMB writes a size the way the scheduler's own explanations do.
func formatRoomMB(mb int64) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}
