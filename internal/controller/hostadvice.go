package controller

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// A host has two numbers that say how many runners it takes: the capacity an
// operator typed, and what its machine holds of the standard runner size it was
// given. The smaller wins, and when it is the second the operator's capacity is a
// promise the machine was never asked to keep -- a host set to three slots that
// holds one, because its standard runner is six of its seven CPUs. Nothing is
// broken: that may be exactly the big runners somebody wanted. It matters when
// jobs are waiting for a runner while the machine has the room, which is the one
// case worth saying something about.

// pressureWindow is how far back a pool's jobs having waited counts. Long enough
// to span a quiet hour in a working day, short enough that a pool that has since
// been left alone stops being advised on.
const pressureWindow = 6 * time.Hour

// Pressure is evidence, not a mood: a pool is under it when the typical job it ran
// in the window waited for a runner, over enough jobs that it is not one slow
// start. The median, so a single job held up by GitHub does not count; and read
// from the jobs themselves rather than from what the scheduler last saw, so a
// restart does not forget it and a test can make it with the jobs it has.
const (
	pressureMedianWait = 30 * time.Second
	pressureMinJobs    = 5
)

// poolsUnderPressure is the pools whose typical job waited at least pressureMedianWait
// within the window.
func (c *Controller) poolsUnderPressure(ctx context.Context) (map[string]bool, error) {
	since := c.Now().Add(-pressureWindow)
	res, err := c.st.JobStats(ctx, store.JobFilter{Since: &since}, []string{store.GroupByPool})
	if err != nil {
		return nil, fmt.Errorf("reading how long jobs waited: %w", err)
	}
	out := map[string]bool{}
	for _, g := range res.Groups {
		id := g.Keys[store.GroupByPool]
		if id == "" || id == store.UnknownVersion {
			continue
		}
		w := g.QueueWait
		if w.Samples >= pressureMinJobs && w.P50MS != nil && time.Duration(*w.P50MS)*time.Millisecond >= pressureMedianWait {
			out[id] = true
		}
	}
	return out, nil
}

// floorTo rounds v down to a multiple of step, which keeps a proposed size a number
// an operator would type and never rounds up into the capacity it is meant to fit.
func floorTo(v, step float64) float64 { return math.Floor(v/step+1e-9) * step }

// hostCapacityAdviceProblems is host.slots_below_capacity: a host whose standard
// runner size holds it below its capacity while a pool that reaches it has had jobs
// waiting, with the size that would give it the capacity -- if every pool that
// reaches it keeps the room it has and the host gains some.
func (c *Controller) hostCapacityAdviceProblems(ctx context.Context, out *[]Problem) error {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	pressure, err := c.poolsUnderPressure(ctx)
	if err != nil {
		return err
	}
	if len(pressure) == 0 {
		return nil
	}
	now := c.Now()
	fleet := c.cfg().Runners
	for _, h := range hosts {
		// A host the controller has stepped down is held back by load, not by its
		// runner size, and changing its profile lifts the step-down in the same write:
		// the priced gain would be wrong and the protection gone. host.throttled already
		// says what to do about it.
		if h.Cordoned || h.Capacity <= 0 || h.Throttle.Active() || !scheduler.HostAvailable(h, now) || !h.RunnerProfile.Standard.Sized() {
			continue
		}
		slots := h.Slots()
		if slots >= h.Capacity {
			continue
		}
		var reaching []*store.Pool
		var waiting []string
		for _, p := range pools {
			if !p.Enabled || !reaches(h, p) {
				continue
			}
			reaching = append(reaching, sizingPool(p, fleet))
			if pressure[p.ID] {
				waiting = append(waiting, p.Name)
			}
		}
		if len(waiting) == 0 {
			continue
		}
		sort.Strings(waiting)

		alloc := h.Allocatable()
		std := h.RunnerProfile.Standard
		var limits []string
		next := std
		if std.CPUs > 0 && alloc.CPUsKnown && int((alloc.CPUs+1e-6)/std.CPUs) < h.Capacity {
			limits = append(limits, "CPU")
			next.CPUs = floorTo(alloc.CPUs/float64(h.Capacity), 0.25)
		}
		if std.MemoryMB > 0 && alloc.MemoryKnown && int(alloc.MemoryMB/std.MemoryMB) < h.Capacity {
			limits = append(limits, "memory")
			next.MemoryMB = int64(floorTo(float64(alloc.MemoryMB)/float64(h.Capacity), 256))
		}
		if len(limits) == 0 {
			continue
		}

		remedy, why := c.priceHostSize(h, reaching, next, limits)
		detail := fmt.Sprintf("it is set to %s, and its machine has %s CPU and %s to place runners on, but one runner here is %s CPU and %s, which holds %s. "+
			"Typical jobs waited %s or more for a runner in the last %s: pools %s.",
			plural(h.Capacity, "slot"), scheduler.FormatCPUs(alloc.CPUs), scheduler.FormatMB(alloc.MemoryMB),
			scheduler.FormatCPUs(std.CPUs), scheduler.FormatMB(std.MemoryMB), plural(slots, "runner"),
			pressureMedianWait, pressureWindow, strings.Join(waiting, ", "))
		fix := fmt.Sprintf("give each runner here less (the host's Runner sizes), or lower its capacity to %d if the big runners are what you want. "+
			"A host's slots are limited by whichever of CPU, memory or capacity runs out first.", slots)
		if remedy != nil {
			fix = fmt.Sprintf("set this host's standard runner to %s CPU and %s, or zoomies hosts edit %s --standard-cpus %s --standard-memory-mb %d. ",
				scheduler.FormatCPUs(next.CPUs), scheduler.FormatMB(next.MemoryMB), h.ID, scheduler.FormatCPUs(next.CPUs), next.MemoryMB) +
				"It applies to runners created after the change."
		} else if why != "" {
			detail += " " + capitalise(why) + "."
		}
		*out = append(*out, Problem{
			Code:     "host.slots_below_capacity",
			Severity: config.SeverityInfo,
			Title: fmt.Sprintf("host %s holds %d of its %d slots, because its runners are larger than the machine divides into",
				h.Name, slots, h.Capacity),
			Detail: detail, Fix: fix,
			Remedy:     remedy,
			TargetKind: "host", TargetID: h.ID,
		})
	}
	return nil
}

// priceHostSize prices giving a host the runner size next: every pool that
// reaches it is asked how many runners it would have room for there, before and
// after, and the size is only proposed if none loses any and the host gains.
// Where it cannot be proposed the second answer says what stands in the way, so the
// notice can say it instead of leaving an operator to find out by trying.
func (c *Controller) priceHostSize(h *store.Host, reaching []*store.Pool, next store.RunnerStandard, limits []string) (*Remedy, string) {
	cand := *h
	cand.RunnerProfile.Standard = next
	if m := h.RunnerProfile.Minimum; (m.CPUs > 0 && next.CPUs > 0 && next.CPUs < m.CPUs) || (m.MemoryMB > 0 && next.MemoryMB > 0 && next.MemoryMB < m.MemoryMB) {
		return nil, "the size that would give it its slots is below this host's own smallest runner"
	}
	if next.CPUs > 0 && next.CPUs < store.MinRunnerCPUs || next.MemoryMB > 0 && next.MemoryMB < store.MinRunnerMemoryMB {
		return nil, "the size that would give it its slots is below the least a runner is given"
	}
	var before, after int
	for _, p := range reaching {
		b, a := poolHostRoom(h, p), poolHostRoom(&cand, p)
		if scheduler.ExcludedBySize(&cand, p) != nil && scheduler.ExcludedBySize(h, p) == nil {
			return nil, "a pool that reaches it would no longer be given runners there, because the smaller size is below that pool's smallest runner"
		}
		if a.Room < b.Room {
			return nil, fmt.Sprintf("a pool that reaches it would have room for fewer runners there (%d, from %d)", a.Room, b.Room)
		}
		before += b.Room
		after += a.Room
	}
	if after <= before {
		return nil, "a pool's own minimum charges each runner more than the standard, so a smaller standard would not give the host more slots; lower that pool's smallest runner"
	}
	profile := h.RunnerProfile
	profile.Standard = next
	// The host's own count, not the pools' rooms added up: two pools that reach one
	// machine each have room for what it holds, and their sum is not a number of
	// runners it can run.
	effect := fmt.Sprintf("the host holds %s instead of %s, and no pool that reaches it loses room", plural(cand.Slots(), "runner"), plural(h.Slots(), "runner"))
	return newRemedy(RemedyHostUpdate, h.ID,
		fmt.Sprintf("Give each runner %s CPU and %s", scheduler.FormatCPUs(next.CPUs), scheduler.FormatMB(next.MemoryMB)),
		effect, map[string]any{"runner_profile": profile}, h.RunnerProfile), ""
}

// A host that has been throttled is a host that was given more than it could do.
// The scheduler's default order, headroom, prefers whichever host would have the
// most left afterwards -- so the largest machine is the first choice every time,
// and goes on being the first choice while it is throttled, because even a
// throttled large host has more left than a small idle one. The result is the
// pattern worth saying aloud: the biggest host, which is often the one that also
// runs the controller, pinned, while three smaller ones sit idle.
//
// There is no remedy. What would help is a decision -- the placement order, or how
// many slots the large host should have -- and both depend on what the large host
// is for, which only its operator knows.

// idleCPUPercent is how little CPU a host is using to count as idle for this.
const idleCPUPercent = 30.0

// hostConcentrationProblems is host.work_concentrated.
func (c *Controller) hostConcentrationProblems(ctx context.Context, out *[]Problem) error {
	order := c.cfg().Scheduler.HostOrder
	if order != "" && order != "headroom" {
		return nil
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	now := c.Now()
	var pinned, idle []*store.Host
	for _, h := range hosts {
		if h.Cordoned || !scheduler.HostAvailable(h, now) || !h.Usage.Fresh(now) {
			continue
		}
		switch {
		case h.Throttle.Active() && h.ActiveRunners > 0:
			pinned = append(pinned, h)
		case h.ActiveRunners == 0 && h.Capacity > 0 && h.Usage.CPUPercent != nil && *h.Usage.CPUPercent < idleCPUPercent:
			idle = append(idle, h)
		}
	}
	if len(pinned) == 0 || len(idle) == 0 {
		return nil
	}
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].Name < idle[j].Name })
	for _, h := range pinned {
		// An idle host only counts if work that runs on this one could run there: a
		// Windows or arm64 machine beside a throttled amd64 one is idle for a reason.
		var reaching []*store.Pool
		for _, p := range pools {
			if p.Enabled && reaches(h, p) {
				reaching = append(reaching, p)
			}
		}
		var names []string
		for _, other := range idle {
			if slices.ContainsFunc(reaching, func(p *store.Pool) bool { return reaches(other, p) }) {
				names = append(names, other.Name)
			}
		}
		if len(names) == 0 {
			continue
		}
		*out = append(*out, Problem{
			Code:     "host.work_concentrated",
			Severity: config.SeverityInfo,
			Title:    fmt.Sprintf("host %s is throttled while %s idle", h.Name, plural(len(names), "other host")+verbIs(len(names))),
			Detail: fmt.Sprintf("%s is running %s and has been stepped down after sustained pressure, while %s with no runners and under %.0f%% CPU, and work that runs on %s could run there. "+
				"The placement order is headroom, which prefers the host with the most left afterwards, so the largest machine is the first choice again as soon as a job needs a runner, throttled or not.",
				h.Name, plural(h.ActiveRunners, "runner"), strings.Join(names, ", ")+verbIs(len(names)), idleCPUPercent, h.Name),
			Fix: fmt.Sprintf("lower %s's capacity so it takes fewer runners at once. Changing scheduler.host_order is not the answer on its own: best_fit packs the fullest host first "+
				"and largest_standard prefers the host where a pool's runner is biggest, and both keep choosing a large host that headroom would have left. "+
				"Which capacity is right depends on what %s is for, so none is proposed for you.", h.Name, h.Name),
			TargetKind: "host", TargetID: h.ID,
		})
	}
	return nil
}

// reaches is whether a pool's runners could be placed on a host at all: the
// host's selector, its backend and its platform, and none of its sizes or load.
func reaches(h *store.Host, p *store.Pool) bool {
	return scheduler.HostSelects(h, p) && scheduler.HostOffers(h, p) && scheduler.HostIsPlatform(h, p)
}

// verbIs is " is" or " are", for a sentence that counts hosts.
func verbIs(n int) string {
	if n == 1 {
		return " is"
	}
	return " are"
}
