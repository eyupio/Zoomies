package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// autoPoolInterval is how often the pools the controller keeps are recomputed
// when nothing has said to. It is the safety net, and the thing correctness
// rests on: every figure is worked out from the hosts as they are, so a missed
// event costs at most this long, and the events -- a host joining, a cordon, an
// edit -- are only there so that the common case does not wait for it.
const autoPoolInterval = time.Minute

// autoPoolState is the reconciler's own memory: the wake-up flag, the lock that
// keeps two passes from overlapping, what counted towards each pool last time
// so that a pool's cause can say who joined and who left, and what the last pass
// found, for the pages that explain it.
type autoPoolState struct {
	kick    chan struct{}
	passMu  sync.Mutex
	members map[string][]string

	statusMu sync.RWMutex
	status   AutoPoolStatus
}

func newAutoPoolState() autoPoolState {
	return autoPoolState{kick: make(chan struct{}, 1), members: map[string][]string{}}
}

// AutoPoolStatus is what the last pass of the automatic pool reconciler found.
type AutoPoolStatus struct {
	// Mode is scheduler.auto_pools as it was when the pass ran.
	Mode string `json:"mode"`
	// At is when the pass ran, zero for a controller that has not run one.
	At time.Time `json:"at"`
	// Installation is the target of the installation the pools belong to, and
	// Problem says why there is none when the mode is not off.
	Installation string `json:"installation,omitempty"`
	Problem      string `json:"problem,omitempty"`
	// Findings are what the controller could not do, and what to change.
	Findings []scheduler.AutoPoolFinding `json:"findings,omitempty"`
	// Pools is every pool the controller keeps, or would, with the hosts that
	// count towards it and what they give it between them.
	Pools []AutoPoolSummary `json:"pools"`
	// Skipped is every host that counted towards no pool, and why.
	Skipped []AutoPoolSkip `json:"skipped,omitempty"`
	// Pending is, under shadow, what the controller would have done.
	Pending []AutoPoolPending `json:"pending,omitempty"`

	// installationID is the installation the pools belong to, by ID, empty
	// where none could be chosen. It is what says whether a given pool is one
	// the controller is keeping now: see keeps.
	installationID string
	// hostPool says which pool each host counted towards, by host ID, and
	// skipByHost why each of the others did not. They are what a host's own view
	// reads, so that a card does not have to search the lists above.
	hostPool   map[string]string
	skipByHost map[string]AutoPoolSkip
}

// keeps says whether the controller is keeping a pool as of the last pass --
// automatic pools are on, and the pool belongs to the installation they are kept
// for -- and, when it is not, says why in the words a page can show beside the
// pool. A pool that is not kept keeps whatever figures it had: nothing works out
// its maximum, and nothing would make it again if it were deleted.
func (s AutoPoolStatus) keeps(p *store.Pool) (bool, string) {
	switch {
	case s.Mode == scheduler.SizeOff:
		return false, "automatic pools are off, so the controller keeps no pool"
	case s.Mode == scheduler.SizeShadow:
		return false, "automatic pools are only reporting what they would do, so the controller changes no pool"
	case s.installationID == "":
		return false, "no GitHub App installation could be chosen for automatic pools"
	case p.InstallationID != s.installationID:
		return false, "it belongs to another GitHub App installation than the one automatic pools are kept for, so it is out of use"
	}
	return true, ""
}

// AutoPoolKept says whether the controller is keeping this pool now, which is
// what decides whether deleting it would only have it made again.
func (c *Controller) AutoPoolKept(p *store.Pool) bool {
	c.autoPools.statusMu.RLock()
	defer c.autoPools.statusMu.RUnlock()
	// The mode is read from the settings and not from the last pass, so that a
	// switch turned off is believed before the pass that follows it has run.
	if c.autoMode() != scheduler.SizeOn {
		return false
	}
	kept, _ := c.autoPools.status.keeps(p)
	return kept
}

// AutoPoolSummary is a pool the controller keeps, or would, with the hosts that
// count towards it.
type AutoPoolSummary struct {
	Key string `json:"key"`
	// Name is the pool's, or what it would be called under shadow. PoolID is
	// empty until there is a pool.
	Name   string   `json:"name"`
	PoolID string   `json:"pool_id,omitempty"`
	Hosts  []string `json:"hosts"`
	// Slots is the runners those hosts hold between them, before any cap.
	Slots int `json:"slots"`
}

// AutoPoolSkip is a host that counts towards no automatic pool. Reason is the
// stable code and Message the sentence that says what it means for the host.
type AutoPoolSkip struct {
	Host    string `json:"host"`
	HostID  string `json:"host_id"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// AutoPoolPending is a change the controller would make and has not, because it
// is being watched rather than trusted.
type AutoPoolPending struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Pool  string `json:"pool"`
	Cause string `json:"cause"`
}

// AutoPoolStatus returns what the last pass found.
func (c *Controller) AutoPoolStatus() AutoPoolStatus {
	c.autoPools.statusMu.RLock()
	defer c.autoPools.statusMu.RUnlock()
	out := c.autoPools.status
	out.Pools = append([]AutoPoolSummary(nil), out.Pools...)
	out.Findings = append([]scheduler.AutoPoolFinding(nil), out.Findings...)
	out.Skipped = append([]AutoPoolSkip(nil), out.Skipped...)
	out.Pending = append([]AutoPoolPending(nil), out.Pending...)
	out.hostPool = maps.Clone(out.hostPool)
	out.skipByHost = maps.Clone(out.skipByHost)
	return out
}

func (c *Controller) setAutoPoolStatus(s AutoPoolStatus) {
	c.autoPools.statusMu.Lock()
	c.autoPools.status = s
	c.autoPools.statusMu.Unlock()
}

// KickAutoPools asks for a pass of the reconciler now. It never blocks and never
// queues, like Nudge: fifty hosts joining at once cost one pass.
func (c *Controller) KickAutoPools() {
	select {
	case c.autoPools.kick <- struct{}{}:
	default:
	}
}

// autoPoolLoop recomputes the pools on a timer and on every host event, with one
// pass at a time.
func (c *Controller) autoPoolLoop(ctx context.Context) {
	ticker := time.NewTicker(autoPoolInterval)
	defer ticker.Stop()
	c.runAutoPools(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.autoPools.kick:
		}
		c.runAutoPools(ctx)
	}
}

func (c *Controller) runAutoPools(ctx context.Context) {
	if err := c.ReconcileAutoPools(ctx); err != nil && ctx.Err() == nil {
		c.log.Error("automatic pools could not be recomputed; the next pass will try again", "error", err)
	}
}

// ReconcileAutoPools runs one pass: give every host its class, work out the
// pools the hosts call for, and make them so. It is exported so that tests and
// the API can force a pass and know it has finished.
//
// It holds no lock the scheduling pass takes. Everything it writes is a narrow
// statement on columns the scheduler only reads -- a pool's derived figures, a
// host's class -- so a pass that is placing runners while this one lowers a
// maximum sees the old figure or the new one, and either is a state the next
// pass puts right; and every figure here is recomputed from the hosts as they
// are, so there is nothing to lose by being run twice or late.
func (c *Controller) ReconcileAutoPools(ctx context.Context) error {
	c.autoPools.passMu.Lock()
	defer c.autoPools.passMu.Unlock()

	mode := c.autoMode()
	if !c.tracksClasses() {
		c.setAutoPoolStatus(AutoPoolStatus{Mode: mode})
		return nil
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("listing hosts: %w", err)
	}
	// Whether the controller is the one allowed to change anything is asked
	// once, and a fenced or lease-less controller still works out classes and
	// plans so that an operator can see what it would do: it reads the classes
	// into the copies it holds and writes none of them.
	act := c.mayAct()
	c.maintainHostClasses(ctx, hosts, act)
	status := AutoPoolStatus{Mode: mode, At: c.Now()}
	if mode == scheduler.SizeOff {
		c.setAutoPoolStatus(status)
		return nil
	}

	installations, err := c.st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("listing installations: %w", err)
	}
	inst, problem := c.autoPoolInstallation(installations)
	if inst == nil {
		status.Problem = problem
		c.setAutoPoolStatus(status)
		return nil
	}
	status.Installation, status.installationID = inst.Target, inst.ID
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("listing pools: %w", err)
	}

	fleet := c.cfg().Runners
	plan := scheduler.PlanAutoPools(scheduler.AutoPoolInput{
		Now: c.Now(), Hosts: hosts, Pools: pools, InstallationID: inst.ID, Since: c.startedAt, RunnerGroup: autoPoolRunnerGroup(inst),
		Grace:      c.cfg().Scheduler.AutoPoolsHostGrace,
		DockerMode: store.DockerMode(c.cfg().Scheduler.AutoPoolsDockerMode),
		Size:       func(p *store.Pool) *store.Pool { return sizingPool(p, fleet) },
		Previous:   c.autoPools.members,
	})
	status.Findings = plan.Findings
	status.skipByHost = map[string]AutoPoolSkip{}
	for _, s := range plan.Skipped {
		skip := AutoPoolSkip{Host: s.Host.Name, HostID: s.Host.ID, Reason: s.Reason, Message: hostSkipSentence(s.Reason)}
		status.Skipped = append(status.Skipped, skip)
		status.skipByHost[s.Host.ID] = skip
	}
	ours := map[string]*store.Pool{}
	for _, p := range pools {
		if p.FromHosts() && p.InstallationID == inst.ID {
			ours[p.AutoKey] = p
		}
	}

	// What counted this time is what the next pass says has changed from, but
	// only once the changes made for it have been. A write that failed leaves the
	// old membership, so the next pass words the cause again.
	settled := true
	if mode == scheduler.SizeShadow || !act {
		for _, ch := range plan.Changes {
			status.Pending = append(status.Pending, AutoPoolPending{Kind: string(ch.Kind), Key: ch.Key, Pool: ch.After.Name, Cause: ch.Cause})
		}
	} else {
		applied := 0
		for _, ch := range plan.Changes {
			if ctx.Err() != nil {
				break
			}
			if c.applyAutoPoolChange(ctx, ch) {
				applied++
				if ch.Kind == scheduler.AutoPoolCreate {
					ours[ch.Key] = ch.After
				}
			}
		}
		settled = applied == len(plan.Changes)
		if applied > 0 {
			// Queued jobs should start using a new host straight away.
			c.Nudge()
		}
	}
	if settled {
		c.autoPools.members = plan.Members
	}
	status.Pools, status.hostPool = autoPoolSummaries(plan, ours, hosts)
	c.setAutoPoolStatus(status)
	return nil
}

// autoPoolSummaries says, for each pool the plan keeps, which hosts count towards
// it and what they give it, and for each host which pool it counted towards.
func autoPoolSummaries(plan scheduler.AutoPoolPlan, ours map[string]*store.Pool, hosts []*store.Host) ([]AutoPoolSummary, map[string]string) {
	idByName := make(map[string]string, len(hosts))
	for _, h := range hosts {
		idByName[h.Name] = h.ID
	}
	keys := make([]string, 0, len(plan.Members))
	for k := range plan.Members {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		aa, ac, _ := store.ParseAutoKey(a)
		ba, bc, _ := store.ParseAutoKey(b)
		if ac.Rank() != bc.Rank() {
			return ac.Rank() - bc.Rank()
		}
		return strings.Compare(aa, ba)
	})
	out := make([]AutoPoolSummary, 0, len(keys))
	hostPool := map[string]string{}
	for _, key := range keys {
		arch, class, _ := store.ParseAutoKey(key)
		sum := AutoPoolSummary{Key: key, Name: scheduler.AutoPoolName(arch, class), Hosts: append([]string{}, plan.Members[key]...), Slots: plan.Slots[key]}
		if p := ours[key]; p != nil {
			sum.PoolID, sum.Name = p.ID, p.Name
		}
		for _, name := range sum.Hosts {
			if id, ok := idByName[name]; ok {
				hostPool[id] = key
			}
		}
		out = append(out, sum)
	}
	return out, hostPool
}

// autoPoolRunnerGroup is the runner group a pool made for an installation goes in,
// which is the group a pool created by hand there gets when it names none.
func autoPoolRunnerGroup(inst *store.Installation) string {
	if inst.TargetType == store.TargetOrg {
		return ManagedRunnerGroupName
	}
	return ""
}

// hostSkipSentence is what it means for a host that it counts towards no
// automatic pool, in the words its card uses.
func hostSkipSentence(code string) string {
	switch code {
	case scheduler.SkipCordoned:
		return "It is cordoned, so its slots do not count towards an automatic pool until it is uncordoned."
	case scheduler.SkipIncompatible:
		return "Its agent speaks a protocol this controller cannot place work on, so it counts towards no automatic pool."
	case scheduler.SkipSilent:
		return "It has not been heard from for longer than scheduler.auto_pools_host_grace, so its slots no longer count towards an automatic pool; they return when it reports again."
	case scheduler.SkipNoClass:
		return "Its size class has not been worked out yet, which needs its agent to report its CPUs and memory."
	case scheduler.SkipBadSize:
		return "Its size tag is not exactly small, medium or large, so it counts towards no automatic pool. Write one of the three as it is here, or remove the tag to let the controller work the class out from the machine."
	case scheduler.SkipArch:
		return "Its architecture is neither amd64 nor arm64, so no automatic pool is kept for it."
	case scheduler.SkipBackend:
		return "It offers neither Docker nor Podman, which an automatic pool needs, so it counts towards no pool."
	}
	return "It counts towards no automatic pool."
}

// autoPoolInstallation is the installation the automatic pools belong to, or
// the reason there is none. A pool belongs to exactly one, so a fleet with
// several has to say which, and the controller does not guess whose runners a
// host's capacity is for.
func (c *Controller) autoPoolInstallation(insts []*store.Installation) (*store.Installation, string) {
	want := strings.TrimSpace(c.cfg().Scheduler.AutoPoolsInstallation)
	switch {
	case want != "":
		for _, i := range insts {
			if strings.EqualFold(i.Target, want) || i.ID == want {
				return i, ""
			}
		}
		return nil, fmt.Sprintf("scheduler.auto_pools_installation names %q, which no GitHub App installation manages", want)
	case len(insts) == 1:
		return insts[0], ""
	case len(insts) == 0:
		return nil, "no GitHub App installation is configured, so there is nothing for automatic pools to belong to"
	}
	return nil, "several GitHub App installations are configured; set scheduler.auto_pools_installation to the one automatic pools belong to"
}

// maintainHostClasses gives each host the class its measurements put it in,
// holding a change for the configured time, and leaves the slice reading what
// is now stored. With write false nothing is stored and the slice reads what
// would be, which is all a controller that may not act can offer.
func (c *Controller) maintainHostClasses(ctx context.Context, hosts []*store.Host, write bool) {
	cfg := c.sizeConfig()
	now := c.Now()
	for _, h := range hosts {
		measured, ok := cfg.HostClass(h)
		if !ok {
			continue
		}
		next, changed := scheduler.NextHostClass(h.SizeClass, measured, now, cfg.Hold)
		if !changed {
			continue
		}
		if !write {
			h.SizeClass = next
			continue
		}
		previous := h.SizeClass.Class
		if err := c.st.SetHostSizeClass(ctx, h.ID, next, previous); err != nil {
			// Another pass decided first, or the host is gone: not ours to
			// write over, and the next look reads the truth.
			if !errors.Is(err, store.ErrSizeClassMoved) && !errors.Is(err, store.ErrNotFound) {
				c.log.Warn("could not record a host's size class", "host", h.ID, "error", err)
			}
			continue
		}
		h.SizeClass = next
		if next.Class != previous {
			alloc := h.Allocatable()
			cause := fmt.Sprintf("%s CPUs and %s of memory allocatable put it in the %s class", scheduler.FormatCPUs(alloc.CPUs), scheduler.FormatMB(alloc.MemoryMB), next.Class)
			if previous != "" {
				cause = fmt.Sprintf("its allocatable resources (%s CPUs and %s of memory) have named the %s class for %s without a break, so it moved up from %s",
					scheduler.FormatCPUs(alloc.CPUs), scheduler.FormatMB(alloc.MemoryMB), next.Class, scheduler.FormatDuration(cfg.Hold), previous)
				if next.Class.Rank() < previous.Rank() {
					cause = strings.Replace(cause, "moved up from", "moved down from", 1)
				}
			}
			c.systemAudit(ctx, "host.size_class", "host", h.ID,
				map[string]any{"class": string(previous)},
				map[string]any{"class": string(next.Class), "cause": cause, "host": h.Name})
		}
		c.publishHost(h)
	}
}

// applyAutoPoolChange makes one change to a pool the controller keeps, records
// it with its cause, and reports whether it was made.
//
// Everything but a create is one narrow write of the columns the controller
// owns, guarded by the operator's warm count, cap and pause as this pass read
// them: see store.ApplyAutoPool. A pool that needed its limits and its fixed
// settings corrected is therefore corrected whole, in one statement, and a
// change an operator saved while the pass was working is never put back.
func (c *Controller) applyAutoPoolChange(ctx context.Context, ch scheduler.AutoPoolChange) bool {
	after := ch.After
	if ch.Kind == scheduler.AutoPoolCreate {
		if err := c.st.CreatePool(ctx, after); err != nil {
			// The unique name or key: an operator made the pool between the read
			// and the write, or another pass did. Either way the next pass sees it
			// and, where the name is held by a pool it cannot take over, says so as
			// a finding; here it is only a line for whoever is reading the log.
			if errors.Is(err, store.ErrConflict) {
				c.log.Debug("an automatic pool was not made because its name or key is already in use", "pool", after.Name, "key", after.AutoKey)
			} else {
				c.log.Warn("could not create an automatic pool", "pool", after.Name, "error", err)
			}
			return false
		}
		c.systemAudit(ctx, "pool.auto_create", "pool", after.ID, nil, autoPoolDoc(after, ch))
		c.publishPool(ctx, events.KindPoolCreated, after)
		c.log.Info("an automatic pool was created", "pool", after.Name, "cause", ch.Cause)
		return true
	}
	written, err := c.st.ApplyAutoPool(ctx, ch.Before, after)
	if err != nil {
		c.log.Warn("could not update an automatic pool", "pool", after.Name, "kind", ch.Kind, "error", err)
		return false
	}
	if !written {
		// The pool is already as it should be, or an operator changed its warm
		// count, cap or pause after this pass read it, which the figures were
		// worked out from. Either way the next pass starts from the pool as it
		// now is, so this one is not counted as made.
		return false
	}
	action := map[scheduler.AutoPoolChangeKind]string{
		scheduler.AutoPoolResize: "pool.auto_resize", scheduler.AutoPoolEnable: "pool.auto_enable",
		scheduler.AutoPoolDisable: "pool.auto_disable", scheduler.AutoPoolReshape: "pool.auto_reshape",
	}[ch.Kind]
	c.systemAudit(ctx, action, "pool", after.ID, autoPoolDoc(ch.Before, ch), autoPoolDoc(after, ch))
	if fresh, err := c.st.GetPool(ctx, after.ID); err == nil {
		c.publishPool(ctx, events.KindPoolUpdated, fresh)
	}
	c.log.Info("an automatic pool changed", "pool", after.Name, "kind", ch.Kind, "cause", ch.Cause)
	return true
}

// autoPoolDoc is the picture of a pool the audit row keeps: the figures that
// move, the fixed settings when those were brought back in line, and, on the
// after side, the sentence that says why.
func autoPoolDoc(p *store.Pool, ch scheduler.AutoPoolChange) map[string]any {
	doc := map[string]any{
		"name": p.Name, "key": p.AutoKey,
		"min_runners": p.MinRunners, "max_runners": p.MaxRunners, "enabled": p.Enabled,
	}
	if ch.Reshaped {
		doc["labels"] = []string(p.Labels)
		doc["host_selector"] = map[string]string(p.HostSelector)
		doc["docker_mode"] = string(p.DockerMode)
	}
	if p == ch.After {
		doc["cause"] = ch.Cause
		doc["hosts"] = ch.Hosts
		doc["slots"] = ch.Slots
	}
	return doc
}

// autoPoolProblems turns what the last pass could not do into the entries the
// problems drawer shows: a pool that would collide with one of the operator's,
// an installation that could not be chosen, a host left out for a reason the
// operator can change.
func (c *Controller) autoPoolProblems(_ context.Context, out *[]Problem) error {
	st := c.AutoPoolStatus()
	if st.Mode == scheduler.SizeOff {
		return nil
	}
	if st.Problem != "" {
		*out = append(*out, Problem{
			Code: "pool.auto_blocked", Severity: config.SeverityWarning, Setting: "scheduler.auto_pools_installation",
			Title:  "automatic pools cannot be kept because no installation could be chosen",
			Detail: st.Problem + ".",
			Fix:    "set scheduler.auto_pools_installation to the organisation or repository whose GitHub App installation the pools belong to.",
		})
	}
	for _, f := range st.Findings {
		if f.Code != scheduler.FindingHostSkipped {
			*out = append(*out, Problem{Code: "pool.auto_blocked", Severity: config.SeverityWarning, Title: f.Message, Fix: f.Fix})
			continue
		}
		hostID := ""
		for _, s := range st.Skipped {
			if s.Host == f.Subject {
				hostID = s.HostID
			}
		}
		*out = append(*out, Problem{Code: "host.auto_pool_skipped", Severity: config.SeverityWarning, Title: f.Message, Fix: f.Fix,
			TargetKind: "host", TargetID: hostID})
	}
	return nil
}
