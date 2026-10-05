package controller

import (
	"context"
	"math"
	"sort"

	"github.com/eyupio/zoomies/internal/store"
)

// An automatic folder decides per runner, which is only as useful as the
// operator's picture of what it decided. A pool that asks for memory and gets
// disk on every host is working as designed and doing nothing for anyone, and the
// words "a runner is too small" do not say what to change. So the controller
// works the answer out: what each host gives each folder now, and the smallest
// change that would give more -- one setting on the pool, or one size on a host --
// with what that costs in runners, because the cost is what decides whether it is
// worth doing. It informs; every change is the operator's.

// TmpfsFolderPlan is one folder on one host.
type TmpfsFolderPlan struct {
	Name string `json:"name"`
	// AskMB is what the folder is asked for there and MB what a runner is given;
	// zero is disk.
	AskMB int64 `json:"ask_mb"`
	MB    int64 `json:"mb"`
	Auto  bool  `json:"auto"`
}

// TmpfsHostPlan is what a runner of the pool is given on one host.
type TmpfsHostPlan struct {
	Host     string            `json:"host"`
	RunnerMB int64             `json:"runner_mb"`
	DaemonMB int64             `json:"daemon_mb,omitempty"`
	Folders  []TmpfsFolderPlan `json:"folders"`
}

// TmpfsShareOption is a daemon share that would put more folders in memory.
type TmpfsShareOption struct {
	Percent int `json:"percent"`
	// InMemory is how many folder placements are in memory across the hosts at
	// this share, against Now at the pool's. Runners is how many the hosts could
	// hold at it, against RunnersNow: moving a share can raise what a runner is
	// charged, which is how it costs capacity.
	InMemory   int `json:"in_memory"`
	Now        int `json:"now"`
	Runners    int `json:"runners"`
	RunnersNow int `json:"runners_now"`
}

// TmpfsSizeOption is the runner size on one host at which the work folder would
// be in memory there.
type TmpfsSizeOption struct {
	Host     string `json:"host"`
	MemoryMB int64  `json:"memory_mb"`
	Slots    int    `json:"slots"`
	SlotsNow int    `json:"slots_now"`
	// Lever is what changes the runner's size on this pool: "standard", the host's
	// standard runner size in its runner profile, for a pool that takes its size
	// from its hosts; or "capacity", the host's slot count, for a pool sized by a
	// slot's share of the machine, where fewer slots are bigger ones.
	Lever string `json:"lever"`
}

// TmpfsPlan is what a pool's in-memory folders come to, host by host, and what
// would change it.
type TmpfsPlan struct {
	Hosts []TmpfsHostPlan `json:"hosts"`
	// InMemory and Total count folder placements across the hosts: how many of the
	// folders asked for are in memory.
	InMemory int `json:"in_memory"`
	Total    int `json:"total"`
	// Share is the daemon share that would put the most folders in memory, when
	// moving it would put more than now. Sizes are, per host, the runner size
	// that would put the work folder there. Either may be empty: the plan says
	// only what would help.
	Share *TmpfsShareOption `json:"share,omitempty"`
	Sizes []TmpfsSizeOption `json:"sizes,omitempty"`
}

// planHosts places the pool's folders on each host of room and counts them.
func planHosts(p *store.Pool, room PoolRoom) (hosts []TmpfsHostPlan, inMemory, total int) {
	for _, h := range room.Placeable() {
		if !h.Tmpfs || h.TmpfsOff {
			continue
		}
		runnerMB, daemonMB, ok := runnerLimitsOn(p, h)
		if !ok {
			continue
		}
		hp := TmpfsHostPlan{Host: h.Host, RunnerMB: runnerMB, DaemonMB: daemonMB}
		for _, o := range outcomes(p.Tmpfs, runnerMB, daemonMB, h.TmpfsPolicy) {
			hp.Folders = append(hp.Folders, TmpfsFolderPlan{Name: o.Name, AskMB: o.Ask, MB: o.Got, Auto: o.Auto})
			total++
			if o.Got > 0 {
				inMemory++
			}
		}
		hosts = append(hosts, hp)
	}
	return hosts, inMemory, total
}

// tmpfsPlan is the plan for a pool whose room is already counted, or nil when
// there is nothing to say: no host places its folders, or nothing is on disk.
func (c *Controller) tmpfsPlan(ctx context.Context, p *store.Pool, room PoolRoom) *TmpfsPlan {
	hosts, inMemory, total := planHosts(p, room)
	if len(hosts) == 0 {
		return nil
	}
	plan := &TmpfsPlan{Hosts: hosts, InMemory: inMemory, Total: total}
	onDisk := false
	for _, h := range hosts {
		for _, f := range h.Folders {
			onDisk = onDisk || f.Auto && f.MB == 0
		}
	}
	if !onDisk {
		return plan
	}

	// The share. A pool sized by its hosts splits one slot between the runner and
	// its daemon, so moving the split moves memory between the two; whether that
	// is affordable is what the room says, since a thinner half is raised to its
	// minimum and the slot grows with it. Each candidate is judged by asking the
	// room again under it.
	if p.DockerMode == store.DockerDinD && p.Resources.MemoryMB <= 0 {
		now := p.Resources.DaemonPercent()
		best := TmpfsShareOption{Percent: now, InMemory: inMemory, Now: inMemory, Runners: room.Runners, RunnersNow: room.Runners}
		for s := store.MinDaemonSharePercent; s <= store.MaxDaemonSharePercent; s += 5 {
			if s == now {
				continue
			}
			cand := *p
			cand.Resources.DaemonSharePercent = s
			r2, err := c.poolRoom(ctx, &cand)
			// A share that costs runners is not offered: a pool that gains a folder in
			// memory and loses most of its fleet has not been helped. It happens
			// because a thinner half is raised to its minimum and the slot grows
			// with it; the size option below says its cost in slots instead.
			if err != nil || r2.Runners < room.Runners {
				continue
			}
			_, in2, _ := planHosts(&cand, r2)
			better := in2 > best.InMemory ||
				in2 == best.InMemory && best.Percent != now && r2.Runners > best.Runners ||
				in2 == best.InMemory && best.Percent != now && r2.Runners == best.Runners && abs(s-now) < abs(best.Percent-now)
			if better {
				best = TmpfsShareOption{Percent: s, InMemory: in2, Now: inMemory, Runners: r2.Runners, RunnersNow: room.Runners}
			}
		}
		if best.Percent != now {
			plan.Share = &best
		}
	}

	// The runner size, per host: the smallest at which the work folder is in
	// memory there, in half-gigabyte steps, and the slots that leaves.
	if p.Tmpfs.Work.Enabled && p.Tmpfs.Work.Auto {
		for _, h := range room.Placeable() {
			if !h.Tmpfs || h.TmpfsOff || h.MemoryMB <= 0 || h.ChargeMemoryMB <= 0 {
				continue
			}
			if _, _, ok := runnerLimitsOn(p, h); !ok {
				continue
			}
			if onDiskHere(plan.Hosts, h.Host, "work folder") {
				for charge := (h.ChargeMemoryMB/512 + 1) * 512; charge <= 65536; charge += 512 {
					probe := h
					probe.ChargeMemoryMB = charge
					r, d, _ := runnerLimitsOn(p, probe)
					if w, _ := p.Tmpfs.PlaceRunner(r, h.TmpfsPolicy); w > 0 {
						_ = d
						if slots := int(h.MemoryMB / charge); slots >= 1 {
							lever := "capacity"
							if p.SizeFromProfile {
								lever = "standard"
							}
							plan.Sizes = append(plan.Sizes, TmpfsSizeOption{Host: h.Host, MemoryMB: charge, Slots: min(slots, h.Slots), SlotsNow: h.Slots, Lever: lever})
						}
						break
					}
				}
			}
		}
		sort.Slice(plan.Sizes, func(i, j int) bool { return plan.Sizes[i].Host < plan.Sizes[j].Host })
	}
	return plan
}

func onDiskHere(hosts []TmpfsHostPlan, host, folder string) bool {
	for _, h := range hosts {
		if h.Host != host {
			continue
		}
		for _, f := range h.Folders {
			if f.Name == folder && f.Auto && f.MB == 0 {
				return true
			}
		}
	}
	return false
}

func abs(n int) int { return int(math.Abs(float64(n))) }
