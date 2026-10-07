package scheduler

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

// The modes of scheduler.history_sizing.
const (
	// HistoryOff places runners on the pool's size and the host's room alone,
	// as the fleet always did.
	HistoryOff = "off"
	// HistoryShadow works out what the jobs waiting are known to need and
	// says where it would have placed differently, without doing so. It is
	// the default for the reason placement_mode's shadow is: an operator sees
	// what the policy would do on their own fleet before it does it.
	HistoryShadow = "shadow"
	// HistoryOn places and sizes the runner for the largest known need among
	// the jobs waiting on its pool.
	HistoryOn = "on"
)

const (
	// ProfilePercentile is the share of a job's recent runs its requirement
	// covers. The ninetieth, not the peak of peaks: one run that pulled a
	// cold cache should not size every run after it, and one that did less
	// than usual should not shrink them.
	ProfilePercentile = 0.9
	// ProfileMemoryMargin is added on top of that percentile for memory. A
	// job that used 5 GB on its heaviest ordinary run is killed at exactly 5
	// GB the next time its test data grows, so it is given a fifth more.
	//
	// CPU gets no margin on purpose. A build uses every core it is given, so
	// its peak is its limit; a margin on it would ask for a fifth more CPU
	// every run and ratchet the requirement up to the largest host. Memory
	// does not ratchet the same way: a job does not use memory because it is
	// there.
	ProfileMemoryMargin = 1.2
	// ProfileOOMGrowth is how much more than its peak a run the kernel killed
	// for memory is taken to have needed. Its peak is the limit it hit, which
	// is by definition not enough; half as much again is a guess, but a
	// guess that moves the next run off a host that has already failed it.
	ProfileOOMGrowth = 1.5
	// profileMemoryStep rounds a memory requirement up, so the figure in a
	// reason reads as a size rather than a measurement.
	profileMemoryStep = 64
)

// Requirement is what one queued job is expected to need, from the runs of it
// the fleet has measured. The zero value is "nothing known", which constrains
// nothing: a job with no history is placed exactly as it was before history
// sizing existed.
type Requirement struct {
	CPUs     float64 `json:"cpus,omitempty"`
	MemoryMB int64   `json:"memory_mb,omitempty"`
	// Samples is how many measured runs it was taken from, and OOMKilled how
	// many of them the kernel killed for memory.
	Samples   int `json:"samples,omitempty"`
	OOMKilled int `json:"oom_killed,omitempty"`
}

// Known reports whether the requirement says anything at all.
func (r Requirement) Known() bool { return r.CPUs > 0 || r.MemoryMB > 0 }

// String says the requirement the way a reason does: "~6 GB and 4 CPU".
func (r Requirement) String() string {
	parts := []string{}
	if r.MemoryMB > 0 {
		parts = append(parts, "~"+formatMB(r.MemoryMB))
	}
	if r.CPUs > 0 {
		parts = append(parts, "~"+formatCPUs(r.CPUs)+" CPU")
	}
	return strings.Join(parts, " and ")
}

// Profile turns a job's recent measured runs into its requirement: the
// ninetieth percentile of their peaks, with a margin on memory, and a run the
// kernel killed for memory counted as having needed more than it had.
func Profile(peaks []store.JobPeak) Requirement {
	var cpus []float64
	var mem []int64
	out := Requirement{}
	for _, p := range peaks {
		out.Samples++
		if p.OOMKilled {
			out.OOMKilled++
		}
		if p.CPUs > 0 {
			cpus = append(cpus, p.CPUs)
		}
		// A run the kernel killed needed more than the limit it hit, and that limit is
		// the one figure known to have been too little: the sampled peak can read under
		// it, because a spike between two 30-second samples is not seen. Taken as the
		// higher of the two, as the size classes already do.
		m := p.MemoryMB
		if p.OOMKilled {
			m = max(m, p.GrantedMemoryMB)
		}
		if m > 0 {
			if p.OOMKilled {
				m = int64(math.Ceil(float64(m) * ProfileOOMGrowth))
			}
			mem = append(mem, m)
		}
	}
	if len(cpus) > 0 {
		slices.Sort(cpus)
		out.CPUs = math.Ceil(cpus[percentileIndex(len(cpus))]*100) / 100
	}
	if len(mem) > 0 {
		slices.Sort(mem)
		m := math.Ceil(float64(mem[percentileIndex(len(mem))]) * ProfileMemoryMargin)
		out.MemoryMB = int64(math.Ceil(m/profileMemoryStep)) * profileMemoryStep
	}
	return out
}

// killedNeed is the memory the largest of a job's killed runs needed: the limit it
// hit, or its sampled peak if that is higher, with the growth a kill implies and the
// margin every requirement carries. Zero when no run was killed.
//
// The percentile ignores the heaviest tenth, which is exactly where a single kill sits
// once a job has about ten runs, so the placement path takes the larger of the two: a
// kill is not an outlier of the ordinary kind but a size known to have failed. The
// size classes do not use it, because they decide for themselves which kills still
// count -- one from before the class last moved is what moved it.
func killedNeed(peaks []store.JobPeak) int64 {
	var need int64
	for _, p := range peaks {
		if !p.OOMKilled {
			continue
		}
		limit := max(p.MemoryMB, p.GrantedMemoryMB)
		if limit <= 0 {
			continue
		}
		m := math.Ceil(float64(limit) * ProfileOOMGrowth * ProfileMemoryMargin)
		need = max(need, int64(math.Ceil(m/profileMemoryStep))*profileMemoryStep)
	}
	return need
}

// percentileIndex is the nearest-rank position of ProfilePercentile in n
// sorted samples.
func percentileIndex(n int) int {
	i := int(math.Ceil(ProfilePercentile*float64(n))) - 1
	return clamp(i, 0, n-1)
}

// requirementFor is one queued job's requirement on p: its profile, never
// below the pool's minimum where it says anything at all.
func (t *tick) requirementFor(p *store.Pool, j *store.Job) Requirement {
	peaks := t.history[store.JobUsageKey{Repo: j.Repo, Workflow: j.Workflow, JobName: j.JobName, PoolID: p.ID}]
	r := Profile(peaks)
	if !r.Known() {
		return Requirement{}
	}
	r.MemoryMB = max(r.MemoryMB, killedNeed(peaks))
	if r.CPUs > 0 {
		r.CPUs = max(r.CPUs, p.Resources.MinCPUs)
	}
	if r.MemoryMB > 0 {
		r.MemoryMB = max(r.MemoryMB, p.Resources.MinMemoryMB)
	}
	return r
}

// poolNeed is the requirement a runner created for p's queue is placed for.
//
// GitHub, not Zoomies, decides which of the jobs waiting an idle runner takes,
// so a runner created for this queue may be handed any of them. It is placed
// for the largest of them: the heavy job the runner might be given is the one
// a small host kills.
//
// A job no host that could run this pool has the allocatable room for is left
// out, and said so in the second value: holding every runner back for it
// would keep the light jobs beside it waiting for ever, and a larger host is
// the only thing that helps it.
func (t *tick) poolNeed(p *store.Pool, queued []*store.Job) (Requirement, string) {
	var fitting []Requirement
	var unfit Requirement
	for _, j := range queued {
		r := t.requirementFor(p, j)
		if !r.Known() {
			continue
		}
		if t.hosts.anyCouldHold(p, r) {
			fitting = append(fitting, r)
			continue
		}
		if r.MemoryMB > unfit.MemoryMB || (r.MemoryMB == unfit.MemoryMB && r.CPUs > unfit.CPUs) {
			unfit = r
		}
	}
	note := ""
	if unfit.Known() {
		note = fmt.Sprintf("jobs waiting on pool %s are known to need %s, and no host that can run the pool has that much to give", p.Name, unfit)
	}
	if len(fitting) == 0 {
		return Requirement{}, note
	}
	need := Requirement{}
	for _, r := range fitting {
		need.CPUs, need.MemoryMB = max(need.CPUs, r.CPUs), max(need.MemoryMB, r.MemoryMB)
		need.Samples += r.Samples
		need.OOMKilled += r.OOMKilled
	}
	if t.hosts.anyCouldHold(p, need) {
		return need, note
	}
	// The heaviest by memory and the heaviest by CPU are different jobs, and
	// no one host has both. The one that is killed for want of it is memory.
	slices.SortFunc(fitting, func(a, b Requirement) int {
		if a.MemoryMB != b.MemoryMB {
			return int(b.MemoryMB - a.MemoryMB)
		}
		switch {
		case a.CPUs > b.CPUs:
			return -1
		case a.CPUs < b.CPUs:
			return 1
		}
		return 0
	})
	return fitting[0], note
}

// anyCouldHold reports whether any host that could run p has the allocatable
// room for r at all, busy or not.
func (hs *hostSet) anyCouldHold(p *store.Pool, r Requirement) bool {
	for _, h := range hs.hosts {
		if !HostCouldRun(h, p) {
			continue
		}
		a := hs.alloc[h.ID]
		if a.CPUsKnown && a.CPUs+cpuEpsilon < r.CPUs {
			continue
		}
		if a.MemoryKnown && a.MemoryMB < r.MemoryMB {
			continue
		}
		return true
	}
	return false
}

// shortOf says what h cannot give a runner of p that needs r, or "" when it
// can. Memory is asked first: it is what a job is killed for.
func (hs *hostSet) shortOf(h *store.Host, p *store.Pool, r Requirement) string {
	left, a := hs.leftFor(h, p), hs.alloc[h.ID]
	if a.MemoryKnown && r.MemoryMB > 0 && left.MemoryMB < r.MemoryMB {
		return fmt.Sprintf("held off %s: jobs waiting need ~%s, it has %s", hostName(h), formatMB(r.MemoryMB), formatMB(max(left.MemoryMB, 0)))
	}
	if a.CPUsKnown && r.CPUs > 0 && left.CPUs+cpuEpsilon < r.CPUs {
		return fmt.Sprintf("held off %s: jobs waiting need ~%s CPU, it has %s", hostName(h), formatCPUs(r.CPUs), formatCPUs(max(left.CPUs, 0)))
	}
	return ""
}

// heldOff is every host that would take a runner of p now but is short of r,
// with the sentence saying by how much, in host order.
func (hs *hostSet) heldOff(p *store.Pool, r Requirement) map[string]string {
	out := map[string]string{}
	for _, h := range hs.hosts {
		if !hs.eligibleAsSized(h, p) {
			continue
		}
		if s := hs.shortOf(h, p, r); s != "" {
			out[h.ID] = s
		}
	}
	return out
}

// sortedPhrases joins the held-off sentences in host order.
func (hs *hostSet) sortedPhrases(m map[string]string) string {
	var out []string
	for _, h := range hs.hosts {
		if s, ok := m[h.ID]; ok {
			out = append(out, s)
		}
	}
	return strings.Join(out, "; ")
}

// sizeForNeed raises the charge of a runner placed for a known need, on each
// field the pool leaves to the host, and returns the size to create it at, or
// the size it already had when nothing moved. A field the pool states is left
// alone: the operator wrote that limit, and a runner is never given more than
// it -- the need then only decides the host.
func sizeForNeed(p *store.Pool, res Reservation, size *store.Resources, need Requirement) (Reservation, *store.Resources) {
	raised := false
	if p.Resources.CPUs <= 0 && need.CPUs > res.CPUs+cpuEpsilon {
		res.CPUs, raised = need.CPUs, true
	}
	if p.Resources.MemoryMB <= 0 && need.MemoryMB > res.MemoryMB {
		res.MemoryMB, raised = need.MemoryMB, true
	}
	if !raised {
		return res, size
	}
	grant := p.Resources
	grant.MinCPUs, grant.MinMemoryMB = 0, 0
	grant.CPUs = res.CPUs / fieldFactor(p, p.Resources.CPUs > 0)
	grant.MemoryMB = res.MemoryMB / int64(fieldFactor(p, p.Resources.MemoryMB > 0))
	return res, &grant
}

func hostName(h *store.Host) string {
	if h.Name != "" {
		return naming.ForSentence(h.Name)
	}
	return h.ID
}
