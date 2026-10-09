package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// HostSizeClassView says which size class a host is in and why, in the terms
// the Hosts page shows it.
type HostSizeClassView struct {
	// Class is the class in force: an operator's size tag where there is one, and
	// the class the host's measurements put it in otherwise.
	Class store.SizeClass `json:"class"`
	// Source is "tag" when an operator's tag decided it and "measured" when the
	// host's own CPUs and memory did.
	Source string `json:"source"`
	// Measured is what the host's measurements name right now, set only when it
	// is not the class in force: because a tag replaces it, or because a change
	// is being held back.
	Measured store.SizeClass `json:"measured,omitempty"`
	// Pending is a different class the measurements have named without a break
	// since PendingSince, and PendingUntil is when the host moves if they keep
	// doing so. The times are carried rather than written into Reason, so the
	// page can count down against the viewer's own clock.
	Pending      store.SizeClass `json:"pending,omitempty"`
	PendingSince *time.Time      `json:"pending_since,omitempty"`
	PendingUntil *time.Time      `json:"pending_until,omitempty"`
	// Reason is what put the host in Class, as a sentence.
	Reason string `json:"reason"`
}

// HostAutoPoolView says which automatic pool a host counts towards, or why it
// counts towards none.
type HostAutoPoolView struct {
	Counted bool `json:"counted"`
	// Pool is the pool's name, or under shadow the name it would have. PoolID is
	// empty until there is a pool.
	Pool   string `json:"pool,omitempty"`
	PoolID string `json:"pool_id,omitempty"`
	// Reason is, for a host that counts towards none, why, and ReasonCode the
	// stable form of the same thing.
	Reason     string `json:"reason,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
}

// PoolAutoView is what is particular to a pool the controller keeps: what an
// operator has asked of it, and what the hosts in it give.
type PoolAutoView struct {
	// Key is the architecture and class the pool is kept for.
	Key   string          `json:"key"`
	Arch  string          `json:"arch"`
	Class store.SizeClass `json:"class"`
	// Warm, Cap and Paused are the operator's: runners to keep ready, the most
	// runners to allow however many slots the hosts give, and a pause. Zero is
	// none for the first two.
	Warm   int  `json:"warm"`
	Cap    int  `json:"cap"`
	Paused bool `json:"paused"`
	// Kept says whether the controller is keeping the pool now: automatic pools
	// are on, and it belongs to the installation they are kept for. A pool that is
	// not kept holds the figures it had, takes a pause the moment it is asked for,
	// and applies a cap or a warm count when it is kept again; and deleting it is
	// not undone by the next pass, because nothing will make it again.
	Kept bool `json:"kept"`
	// Hosts are the hosts that count towards the pool and Slots what they hold
	// between them, as of the last pass of the reconciler.
	Hosts   []string `json:"hosts"`
	Slots   int      `json:"slots"`
	Summary string   `json:"summary"`
}

// SizeClassLimits is what one class is: how large a host in it is, and how
// large a runner.
type SizeClassLimits struct {
	Class store.SizeClass `json:"class"`
	// Label is the runs-on label that asks for the class by name.
	Label string `json:"label"`
	// HostMaxCPUs and HostMaxMemoryMB are the most allocatable a host in the
	// class has; both are absent for the largest, which has no limit above.
	HostMaxCPUs     float64 `json:"host_max_cpus,omitempty"`
	HostMaxMemoryMB int64   `json:"host_max_memory_mb,omitempty"`
	RunnerCPUs      float64 `json:"runner_cpus"`
	RunnerMemoryMB  int64   `json:"runner_memory_mb"`
}

// AutoPoolsView is the state of size routing and of the pools the controller
// keeps, as GET /auto-pools returns it.
type AutoPoolsView struct {
	SizeRouting string `json:"size_routing"`
	AutoPools   string `json:"auto_pools"`
	// Installation is the target the pools belong to, and Problem says why there
	// is none. At is when the last pass ran, null before the first.
	Installation string                      `json:"installation,omitempty"`
	Problem      string                      `json:"problem,omitempty"`
	At           *time.Time                  `json:"at"`
	Pools        []AutoPoolSummary           `json:"pools"`
	Findings     []scheduler.AutoPoolFinding `json:"findings"`
	Skipped      []AutoPoolSkip              `json:"skipped"`
	Pending      []AutoPoolPending           `json:"pending"`
	Classes      []SizeClassLimits           `json:"classes"`
	DefaultClass store.SizeClass             `json:"default_class"`
	Hold         store.Duration              `json:"hold"`
	FallbackWait store.Duration              `json:"fallback_wait"`
	HostGrace    store.Duration              `json:"host_grace"`
}

// AutoPools reports the state of both switches and of the pools the controller
// keeps. It reads what the last pass found and does not run one.
func (c *Controller) AutoPools() AutoPoolsView {
	st := c.AutoPoolStatus()
	cfg := c.sizeConfig()
	out := AutoPoolsView{
		SizeRouting:  c.sizeMode(),
		AutoPools:    c.autoMode(),
		Installation: st.Installation,
		Problem:      st.Problem,
		Pools:        emptySlice(st.Pools),
		Findings:     emptySlice(st.Findings),
		Skipped:      emptySlice(st.Skipped),
		Pending:      emptySlice(st.Pending),
		DefaultClass: cfg.DefaultClass,
		Hold:         store.Duration(cfg.Hold),
		FallbackWait: store.Duration(cfg.FallbackWait),
		HostGrace:    store.Duration(c.cfg().Scheduler.AutoPoolsHostGrace),
	}
	if !st.At.IsZero() {
		at := st.At
		out.At = &at
	}
	for _, class := range store.SizeClasses() {
		run := cfg.Runner(class)
		limits := SizeClassLimits{Class: class, Label: class.Label(), RunnerCPUs: run.CPUs, RunnerMemoryMB: run.MemoryMB}
		switch class {
		case store.SizeSmall:
			limits.HostMaxCPUs, limits.HostMaxMemoryMB = cfg.SmallMax.CPUs, cfg.SmallMax.MemoryMB
		case store.SizeMedium:
			limits.HostMaxCPUs, limits.HostMaxMemoryMB = cfg.MediumMax.CPUs, cfg.MediumMax.MemoryMB
		}
		out.Classes = append(out.Classes, limits)
	}
	return out
}

// hostSizeClassView says which class a host is in, nil for one that is in none:
// before the controller has worked it out, with both switches off, or because
// the host's size tag is not a class, which the host's auto pool says.
func (c *Controller) hostSizeClassView(h *store.Host) *HostSizeClassView {
	if !c.tracksClasses() {
		return nil
	}
	class, operator := h.EffectiveSizeClass()
	if !class.Valid() {
		return nil
	}
	cfg := c.sizeConfig()
	measured, known := cfg.HostClass(h)
	alloc := h.Allocatable()
	machine := fmt.Sprintf("%s CPUs and %s of memory allocatable", scheduler.FormatCPUs(alloc.CPUs), scheduler.FormatMB(alloc.MemoryMB))

	out := &HostSizeClassView{Class: class, Source: "measured"}
	if operator {
		out.Source = "tag"
	}
	if known && measured != class {
		out.Measured = measured
	}
	switch {
	case operator && out.Measured != "":
		out.Reason = fmt.Sprintf("the size tag on this host says %s; its machine, at %s, would be %s", class, machine, out.Measured)
	case operator && known:
		out.Reason = fmt.Sprintf("the size tag on this host says %s, which is also what its machine measures (%s)", class, machine)
	case operator:
		out.Reason = fmt.Sprintf("the size tag on this host says %s", class)
	default:
		out.Reason = fmt.Sprintf("%s put it in the %s class (small goes up to %s CPUs and %s, medium to %s CPUs and %s)",
			machine, class, scheduler.FormatCPUs(cfg.SmallMax.CPUs), scheduler.FormatMB(cfg.SmallMax.MemoryMB),
			scheduler.FormatCPUs(cfg.MediumMax.CPUs), scheduler.FormatMB(cfg.MediumMax.MemoryMB))
	}
	if h.SizeClass.Pending.Valid() && h.SizeClass.PendingSince != nil && !operator {
		out.Pending = h.SizeClass.Pending
		since := *h.SizeClass.PendingSince
		until := since.Add(cfg.Hold)
		out.PendingSince, out.PendingUntil = &since, &until
		out.Reason += fmt.Sprintf("; its measurements now name %s, and it moves when they have done so for %s", out.Pending, scheduler.FormatDuration(cfg.Hold))
	}
	return out
}

// hostAutoPoolView says which pool a host counts towards, nil while automatic
// pools are off or before a pass has looked at the host.
func (c *Controller) hostAutoPoolView(h *store.Host) *HostAutoPoolView {
	if c.autoMode() == scheduler.SizeOff {
		return nil
	}
	// Read under the lock and not through AutoPoolStatus, which copies every list
	// it holds: a page of hosts renders one of these for each, and a copy of the
	// whole status for each would grow with the square of the fleet.
	c.autoPools.statusMu.RLock()
	defer c.autoPools.statusMu.RUnlock()
	st := &c.autoPools.status
	if key, ok := st.hostPool[h.ID]; ok {
		for _, p := range st.Pools {
			if p.Key == key {
				return &HostAutoPoolView{Counted: true, Pool: p.Name, PoolID: p.PoolID}
			}
		}
	}
	if skip, ok := st.skipByHost[h.ID]; ok {
		return &HostAutoPoolView{Reason: skip.Message, ReasonCode: skip.Reason}
	}
	return nil
}

// auto renders what is particular to a pool the controller keeps, nil for any
// other.
func (v *PoolRenderer) auto(p *store.Pool) *PoolAutoView {
	if !p.FromHosts() {
		return nil
	}
	arch, class, ok := store.ParseAutoKey(p.AutoKey)
	if !ok {
		return nil
	}
	out := &PoolAutoView{Key: p.AutoKey, Arch: arch, Class: class, Warm: p.AutoMin, Cap: p.AutoCap, Paused: p.AutoPaused, Hosts: []string{}}
	for _, s := range v.autoStatus.Pools {
		if s.PoolID == p.ID {
			out.Hosts, out.Slots = emptySlice(s.Hosts), s.Slots
		}
	}
	var why string
	out.Kept, why = v.autoStatus.keeps(p)
	out.Summary = autoPoolSentence(p, arch, class, out.Hosts, out.Slots, why)
	return out
}

// autoPoolSentence says what a pool the controller keeps is and where its
// maximum comes from. notKept is why the controller is not keeping it at the
// moment, empty when it is: a pool it is not keeping has no hosts counted
// towards it and no maximum being worked out, which is not the same thing as
// having none, and the sentence must not say that it is out of use for want of
// hosts when nobody is looking.
func autoPoolSentence(p *store.Pool, arch string, class store.SizeClass, hosts []string, slots int, notKept string) string {
	which := fmt.Sprintf("%s %s hosts", class, archWord(arch))
	var parts []string
	switch {
	case notKept != "":
		parts = append(parts, fmt.Sprintf("Made by the controller for %s, and not being kept now: %s. It holds the limits it had.", which, notKept))
	case len(hosts) == 0:
		parts = append(parts, fmt.Sprintf("Kept by the controller for %s. No host of that kind counts at the moment, so the pool is out of use.", which))
	default:
		parts = append(parts, fmt.Sprintf("Kept by the controller for %s: %s (%s) hold %s between them.",
			which, plural(len(hosts), "host"), strings.Join(hosts, ", "), plural(slots, "runner")))
	}
	// What an operator asked for is said as it is, but when nothing is working a
	// maximum out the cap and the warm count are not yet doing anything.
	later := ""
	if notKept != "" {
		later = ", which takes effect when the controller keeps it"
	}
	if p.AutoCap > 0 && (notKept != "" || slots > p.AutoCap) {
		parts = append(parts, fmt.Sprintf("You capped it at %s%s.", plural(p.AutoCap, "runner"), later))
	}
	if p.AutoMin > 0 {
		parts = append(parts, fmt.Sprintf("You asked for %s kept warm%s.", plural(p.AutoMin, "runner"), later))
	}
	if p.AutoPaused {
		parts = append(parts, "You paused it, so it takes no new work whatever its hosts do.")
	}
	return strings.Join(parts, " ")
}

// archWord is an architecture as a person says it.
func archWord(arch string) string {
	if arch == "amd64" {
		return "x64"
	}
	return arch
}

// ---------------------------------------------------------------------------
// What a workflow could say better
// ---------------------------------------------------------------------------

// AdviceOptions narrows a request for label advice: a window the figures
// behind each row are read over (zero is scheduler.ClassWindow, the span the
// class itself is decided over) and a repository, compared as GitHub does.
type AdviceOptions struct {
	Window time.Duration
	Repo   string
}

// What bounded the window the figures were read over.
const (
	AdviceBoundAsked     = "asked"
	AdviceBoundRetention = "retention"
)

// AdviceWindow is the span the figures behind a page of advice cover: what
// was asked, what applied, and which of the two bounded it. A window longer
// than the history the fleet keeps would promise figures over runs that were
// pruned, so retention is the ceiling and the answer says when it was hit.
type AdviceWindow struct {
	Asked   store.Duration `json:"asked"`
	Applied store.Duration `json:"applied"`
	Bound   string         `json:"bound"`
}

// adviceWindow applies retention to the window asked for.
func (c *Controller) adviceWindow(asked time.Duration) AdviceWindow {
	if asked <= 0 {
		asked = scheduler.ClassWindow
	}
	w := AdviceWindow{Asked: store.Duration(asked), Applied: store.Duration(asked), Bound: AdviceBoundAsked}
	if keep := c.cfg().Retention.Jobs; keep > 0 && keep < asked {
		w.Applied, w.Bound = store.Duration(keep), AdviceBoundRetention
	}
	return w
}

// LabelAdvice says what to change in the runs-on of the jobs whose measured
// runs call for something other than what they ask for, the costliest first,
// with the figures each row rests on and whether any host carries the class
// it recommends. There is none while size routing is off, because nothing is
// classed to compare with.
func (c *Controller) LabelAdvice(ctx context.Context, opts AdviceOptions) ([]*scheduler.Advice, AdviceWindow, error) {
	window := c.adviceWindow(opts.Window)
	rows, err := c.adviceRows(ctx, opts.Repo)
	if err != nil || len(rows) == 0 {
		return rows, window, err
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return nil, window, fmt.Errorf("listing hosts: %w", err)
	}
	// The fit is whether the fleet has such a host at all; a host that is
	// cordoned or quiet still counts, because the question is about buying a
	// machine, not about this minute's room.
	carried := map[store.SizeClass]bool{}
	for _, h := range hosts {
		if class, _ := h.EffectiveSizeClass(); class.Valid() {
			carried[class] = true
		}
	}
	since := c.Now().Add(-window.Applied.Duration())
	for _, a := range rows {
		runs, err := c.st.JobClassHistory(ctx, a.Repo, a.Workflow, a.JobName, since, store.JobClassHistoryLimit)
		if err != nil {
			return nil, window, fmt.Errorf("reading the runs behind %s/%s/%s: %w", a.Repo, a.Workflow, a.JobName, err)
		}
		obs := scheduler.Observe(runs)
		a.Observed = &obs
		fit := &scheduler.Fit{OK: carried[a.RecommendedClass]}
		if !fit.OK {
			fit.Missing = a.RecommendedClass
		}
		a.Fits = fit
	}
	return rows, window, nil
}

// adviceRows is the advice without its figures: what the problems list
// counts after every pass. It reads the kept classes and the pins and
// nothing per job, so a count on a fleet with hundreds of advised jobs is
// two queries and not hundreds of history scans for a number.
func (c *Controller) adviceRows(ctx context.Context, repoFilter string) ([]*scheduler.Advice, error) {
	if c.sizeMode() == scheduler.SizeOff {
		return nil, nil
	}
	kept, err := c.st.ListJobClassesAsked(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing the classes kept for jobs: %w", err)
	}
	pins, err := c.st.ListSizePins(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing size pins: %w", err)
	}
	repo := strings.ToLower(strings.TrimSpace(repoFilter))
	cfg := c.sizeConfig()
	var out []*scheduler.Advice
	for _, k := range kept {
		if repo != "" && strings.ToLower(k.Repo) != repo {
			continue
		}
		if a := cfg.LabelAdvice(k, pins); a != nil {
			out = append(out, a)
		}
	}
	scheduler.SortAdvice(out)
	return out, nil
}

// labelAdviceMemoFor is how long the problems list reuses the count of label
// advice. It reads every class kept for every job, with the labels of each job's
// latest run, and the list is built after every pass; a hint about a workflow's
// runs-on that is a minute old is as good as a fresh one, and a fleet with tens
// of thousands of jobs does not pay for it once a second.
const labelAdviceMemoFor = time.Minute

// adviceMemo is the last count of label advice by kind, and when it was made.
// The page of advice itself is never taken from it: whoever opens that page is
// asking for the answer now.
type adviceMemo struct {
	mu     sync.Mutex
	at     time.Time
	counts map[string]int
	total  int
}

// labelAdviceCounts is how much advice there is of each kind, and in all. It is
// worked out again once the last answer is a minute old, or when a pin changes
// what a job is advised on.
func (c *Controller) labelAdviceCounts(ctx context.Context) (map[string]int, int, error) {
	m := &c.adviceMemo
	m.mu.Lock()
	defer m.mu.Unlock()
	now := c.Now()
	if age := now.Sub(m.at); m.counts != nil && age >= 0 && age < labelAdviceMemoFor {
		return m.counts, m.total, nil
	}
	advice, err := c.adviceRows(ctx, "")
	if err != nil {
		return nil, 0, err
	}
	// A row with too few runs is counted under its state and not as advice:
	// the problem entry is about workflows that could say something better,
	// and a job the fleet has not seen enough of is not yet one of them.
	counts := map[string]int{}
	total := 0
	for _, a := range advice {
		if a.State != scheduler.AdviceStateOK {
			counts[a.State]++
			continue
		}
		counts[a.Kind]++
		total++
	}
	m.at, m.counts, m.total = now, counts, total
	return counts, total, nil
}

// forgetLabelAdvice drops the memo, for a change that alters what a job is
// advised on at once: a pin, which advice leaves the job it covers alone.
func (c *Controller) forgetLabelAdvice() {
	c.adviceMemo.mu.Lock()
	c.adviceMemo.counts = nil
	c.adviceMemo.mu.Unlock()
}

// labelAdviceProblems raises one entry, at most, however many jobs there is
// advice for: the advice itself is a page of its own, and a drawer with a line
// for every workflow would bury the problems that are about the fleet.
func (c *Controller) labelAdviceProblems(ctx context.Context, out *[]Problem) error {
	if c.sizeMode() == scheduler.SizeOff {
		return nil
	}
	counts, total, err := c.labelAdviceCounts(ctx)
	if err != nil || total == 0 {
		return err
	}
	var parts []string
	if n := counts[scheduler.AdviceTooSmall]; n > 0 {
		parts = append(parts, fmt.Sprintf("%s asks for a class too small for what it uses", plural(n, "job")))
	}
	if n := counts[scheduler.AdviceUnguaranteed]; n > 0 {
		parts = append(parts, fmt.Sprintf("%s needs more than the default class and is only routed there best effort", plural(n, "job")))
	}
	if n := counts[scheduler.AdviceTooLarge]; n > 0 {
		parts = append(parts, fmt.Sprintf("%s asks for more than it uses", plural(n, "job")))
	}
	*out = append(*out, Problem{
		Code:     "jobs.label_advice",
		Severity: config.SeverityInfo,
		Title:    fmt.Sprintf("%s could say something better in runs-on", plural(total, "workflow job")),
		Detail:   capitalise(strings.Join(parts, "; ")) + ".",
		Fix:      "open the label advice on the Jobs page, or run `zoomies jobs advice`, which says what to write instead for each.",
	})
	return nil
}
