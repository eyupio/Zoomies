package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/machine"
	"github.com/eyupio/zoomies/internal/store"
)

// The memory valve's loop. Everything the controller decides arrives in a
// heartbeat (ElasticMemoryDirective); what this file does is look at each
// runner's memory between heartbeats and, inside those rules, raise a limit
// before the kernel kills a process for it.
//
// It is the one loop in the agent that acts without being told to, and so it is
// held to the standard that matters for that: it only ever raises a limit, it
// re-reads the host's free memory before every raise, and everything it needs
// to be right after a restart -- what each container was created with, what it
// holds now -- is read back from the daemon rather than remembered.
const (
	// memoryGuardTick is how often the loop wakes. A runner close to its limit
	// is looked at on every tick, because a compiler can find the end of its
	// limit in the time a longer interval would leave.
	memoryGuardTick = time.Second
	// memoryIdleLook is how often a runner that is not close is looked at. Most
	// are not, and a look is a call to the daemon per container.
	memoryIdleLook = 5 * time.Second
	// memoryRediscover is how long what is known of a runner's containers is
	// trusted: a docker-in-docker sidecar appears a little after its runner, and
	// a limit someone changed by hand is read back rather than argued with.
	memoryRediscover = 30 * time.Second
	// memoryLookTimeout bounds the calls to the daemon for one runner. A daemon
	// that does not answer must cost one look, not the loop.
	memoryLookTimeout = 3 * time.Second
	// memoryClimbHorizon is how soon a climbing runner must be able to reach its
	// limit, at the pace it has been going, to be looked at every second: two
	// quiet intervals, so that it is never left to the first of them.
	memoryClimbHorizon = 2 * memoryIdleLook
	// memoryNearShare is how much of a limit a runner must have used to have
	// "come near" it, for the count an operator reads before trusting the valve.
	memoryNearShare = 0.90
	// memoryWorkers bounds the looks made at once.
	memoryWorkers = 4
)

// valveContainer is one container of a watched runner: what the daemon says it
// holds, and what an observing agent pretends it holds.
type valveContainer struct {
	backend.MemoryContainer
	// UsageMB is what the container was using at the latest look and PrevUsageMB
	// at the one before: the pace between them is how a runner that is climbing
	// is told from one that is merely tight.
	UsageMB, PrevUsageMB int64
	// VirtualLimitMB and VirtualSwapMB are the limit and swap this container
	// would have if the agent were allowed to act. They start equal to the real
	// ones and move only in observe mode, so that what an observing agent decides
	// builds on what it decided before, as an automatic one's raises do.
	VirtualLimitMB, VirtualSwapMB int64
}

// memoryValve is the guard's record of one runner. It is only ever read or
// written under Agent.mu.
type memoryValve struct {
	// rule is what the controller last said about this runner. Nil when it no
	// longer says anything -- the pool's valve was switched off -- which stops
	// the guard acting on the runner but does not stop it reporting what the
	// runner holds: a raised limit stays raised.
	rule         *ElasticMemoryRunner
	containers   []valveContainer
	discoveredAt time.Time
	lastLook     time.Time
	// hot is whether a container was using enough of its limit at the last look
	// to be worth looking at again at once.
	hot bool
	// code and reason are the latest decision, and the sentence of the latest one
	// that was not "healthy".
	code   MemoryValveCode
	reason string
	// nearLimit is sticky: a runner that came within a tenth of a limit once is
	// one whose pool's jobs use the valve, whatever it did after.
	nearLimit bool
	raises    int
	// warned keeps a failing update to one warning per reason per runner.
	warned map[string]bool
}

// lentMB is what the runner's containers hold beyond what they were created
// with, and spillMB the swap they may use beyond their limits. Unlimited swap is
// not a loan, and is not counted as one.
func (v *memoryValve) lentMB() (lent, spill int64) {
	for _, c := range v.containers {
		lent += max(c.LimitMB-c.GuaranteeMB, 0)
		if c.SwapMB < unlimitedSwapMB {
			spill += c.SwapMB
		}
	}
	return lent, spill
}

// wouldLendMB is lentMB for the limits an observing agent decided on.
func (v *memoryValve) wouldLendMB() (lent, spill int64) {
	for _, c := range v.containers {
		lent += max(c.VirtualLimitMB-c.GuaranteeMB, 0)
		if c.VirtualSwapMB < unlimitedSwapMB {
			spill += c.VirtualSwapMB
		}
	}
	return lent, spill
}

// unlimitedSwapMB is what the backend reports for a container whose swap has no
// limit; see backend.swapUnlimited.
const unlimitedSwapMB = int64(1) << 40

// sample is what the heartbeat carries for the runner, nil when the valve has
// nothing to say about it.
func (v *memoryValve) sample() *backend.MemoryValveSample {
	if v == nil {
		return nil
	}
	lent, spill := v.lentMB()
	if v.rule == nil && lent == 0 && spill == 0 {
		return nil
	}
	out := &backend.MemoryValveSample{
		Mode:       string(store.MemoryBurstOff),
		Code:       string(v.code),
		Reason:     v.reason,
		LentBytes:  lent << 20,
		SpillBytes: spill << 20,
		NearLimit:  v.nearLimit,
		Raises:     v.raises,
	}
	if out.Code == "" {
		out.Code = string(MemoryHealthy)
	}
	if v.rule != nil {
		out.Mode = string(v.rule.Mode)
		if v.rule.Mode == store.MemoryBurstObserve {
			would, wouldSpill := v.wouldLendMB()
			out.WouldLendBytes, out.WouldSpillBytes = would<<20, wouldSpill<<20
		}
	}
	return out
}

// memoryRules is the last ElasticMemoryDirective, indexed for the loop.
type memoryRules struct {
	capacityMB, floorMB int64
	runners             map[string]ElasticMemoryRunner
}

// features is what this agent says it can do, which the heartbeat and the join
// both carry. The memory valve is offered wherever the host's memory can be read
// the way it has to be, which is Linux's procfs; elsewhere there is nothing to
// check a loan against, and a loan that cannot be checked is not made.
func (a *Agent) features() []string {
	out := []string{FeatureElasticCPU, FeatureToolCacheFill, FeatureTmpfs}
	if a.opts.ReadMemory != nil || machine.MemoryReadable() {
		out = append(out, FeatureElasticMemory)
	}
	return out
}

// applyMemoryDirective replaces the valve's rules with the heartbeat's. A
// controller that sends none is a controller with nothing for the valve to do,
// and clears them; a heartbeat that did not arrive leaves them alone, which is
// what keeps a controller restart from being the thing that kills a job.
func (a *Agent) applyMemoryDirective(d *ElasticMemoryDirective) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rules := memoryRules{}
	if d != nil {
		rules.capacityMB, rules.floorMB = d.CapacityMB, d.FloorMB
		rules.runners = make(map[string]ElasticMemoryRunner, len(d.Runners))
		for _, r := range d.Runners {
			if r.RunnerID != "" && (r.Mode == store.MemoryBurstObserve || r.Mode == store.MemoryBurstAutomatic) && r.CeilingMB > 0 {
				rules.runners[r.RunnerID] = r
			}
		}
	}
	a.memory = rules
	for id, t := range a.runners {
		rule, ok := rules.runners[id]
		switch {
		case ok && t.valve == nil:
			t.valve = &memoryValve{rule: &rule}
		case ok:
			t.valve.rule = &rule
		case t.valve != nil:
			t.valve.rule = nil
		}
	}
}

// guardMemory is one tick of the loop: look at the runners that are due, then
// decide for each, most pressed first, and raise what has to be raised.
func (a *Agent) guardMemory(ctx context.Context) {
	now := a.now()
	jobs := a.dueMemoryLooks(now)
	if len(jobs) == 0 {
		return
	}
	looks := a.takeMemoryLooks(ctx, jobs, now)
	if len(looks) == 0 {
		return
	}
	host, measured := a.hostMemory()
	a.mu.Lock()
	rules := a.memory
	a.mu.Unlock()

	// The most pressed runner is decided first: when the pool is short, what is
	// left should go to the runner nearest its kill.
	slices.SortStableFunc(looks, func(x, y memoryLook) int {
		switch px, py := x.pressure(), y.pressure(); {
		case px > py:
			return -1
		case px < py:
			return 1
		}
		return 0
	})
	for _, look := range looks {
		// The containers are stored before anything is decided about them, so
		// that a raise made now is recorded on a container that is there.
		a.adoptMemoryLook(look, now)
		taken := a.decideMemory(ctx, look, rules, host, measured, now)
		if measured {
			// What a raise took is no longer free for the next decision of this
			// same tick, whose reading of the host was made before it.
			host.AvailableMB = max(host.AvailableMB-taken, 0)
		}
	}
}

// memoryJob is a runner that is due to be looked at.
type memoryJob struct {
	runnerID string
	kind     store.BackendKind
	handle   backend.Handle
	rule     ElasticMemoryRunner
	// known is what is stored of its containers, and rediscov says they are to
	// be found again rather than trusted: the loop has not found them yet, or has
	// not looked for a while.
	known    []valveContainer
	rediscov bool
	// lastLook is when the runner was last looked at, so that a climb can be
	// measured over the time it took.
	lastLook time.Time
}

// memoryLook is what one look found: the runner's containers as the daemon
// holds them now, with how much each is using.
type memoryLook struct {
	memoryJob
	containers []valveContainer
	usageMB    map[string]int64
}

// pressure is how close the runner's most pressed container is to its limit, as
// a share of it.
func (l memoryLook) pressure() float64 {
	worst := 0.0
	for _, c := range l.containers {
		if c.LimitMB > 0 {
			worst = max(worst, float64(l.usageMB[c.ID])/float64(c.LimitMB))
		}
	}
	return worst
}

// dueMemoryLooks picks the runners to look at this tick: those the controller
// has a rule for, that are running, and that are either close to a limit or
// have not been looked at for a few seconds.
func (a *Agent) dueMemoryLooks(now time.Time) []memoryJob {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.memory.runners) == 0 {
		return nil
	}
	var out []memoryJob
	for id, rule := range a.memory.runners {
		t := a.runners[id]
		if t == nil || t.valve == nil || t.terminal || t.hostRemoved || t.handle == "" || !t.phase.Live() {
			continue
		}
		v := t.valve
		if why, refused := a.memoryUnsupported[t.kind]; refused {
			// A runtime that cannot change a live limit is not asked again, and
			// the runners on it say so rather than reading as healthy.
			v.code, v.reason = MemoryUnsupported, why
			continue
		}
		if !v.hot && !v.lastLook.IsZero() && now.Sub(v.lastLook) < memoryIdleLook {
			continue
		}
		out = append(out, memoryJob{
			runnerID: id, kind: t.kind, handle: t.handle, rule: rule,
			known:    slices.Clone(v.containers),
			rediscov: len(v.containers) == 0 || now.Sub(v.discoveredAt) >= memoryRediscover,
			lastLook: v.lastLook,
		})
	}
	return out
}

// takeMemoryLooks reads the memory of every container of the due runners, a few
// at a time. A runner whose look fails is left for the next tick.
func (a *Agent) takeMemoryLooks(ctx context.Context, jobs []memoryJob, now time.Time) []memoryLook {
	var (
		mu    sync.Mutex
		out   []memoryLook
		wg    sync.WaitGroup
		queue = make(chan memoryJob)
	)
	for range min(memoryWorkers, len(jobs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				if ctx.Err() != nil {
					continue
				}
				if look, ok := a.lookAtMemory(ctx, job, now); ok {
					mu.Lock()
					out = append(out, look)
					mu.Unlock()
				}
			}
		}()
	}
	for _, job := range jobs {
		select {
		case queue <- job:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
	return out
}

// lookAtMemory finds a runner's containers, if they are not known, and reads
// each one's memory.
func (a *Agent) lookAtMemory(ctx context.Context, job memoryJob, now time.Time) (memoryLook, bool) {
	b, err := a.opts.Backends.Get(job.kind)
	if err != nil {
		return memoryLook{}, false
	}
	u, ok := b.(backend.MemoryUpdater)
	if !ok {
		return memoryLook{}, false
	}
	lctx, cancel := context.WithTimeout(ctx, memoryLookTimeout)
	defer cancel()

	containers := job.known
	if job.rediscov {
		found, err := u.MemoryContainers(lctx, job.handle)
		if err != nil {
			if !errors.Is(err, backend.ErrNotFound) {
				a.log.Debug("could not find a runner's containers for the memory valve", "runner", job.runnerID, "error", err)
			}
			return memoryLook{}, false
		}
		containers = make([]valveContainer, 0, len(found))
		for _, c := range found {
			vc := valveContainer{MemoryContainer: c, VirtualLimitMB: c.LimitMB, VirtualSwapMB: c.SwapMB}
			// Observing is a standing pretence, so what was decided before is
			// kept across a rediscovery rather than forgotten with it.
			for _, old := range job.known {
				if old.ID == c.ID {
					vc.VirtualLimitMB, vc.VirtualSwapMB = max(old.VirtualLimitMB, c.LimitMB), max(old.VirtualSwapMB, c.SwapMB)
					vc.UsageMB = old.UsageMB
				}
			}
			containers = append(containers, vc)
		}
	}
	look := memoryLook{memoryJob: job, usageMB: make(map[string]int64, len(containers))}
	for _, c := range containers {
		reading, err := u.MemoryUsage(lctx, c.ID)
		if err != nil {
			if !errors.Is(err, backend.ErrNotFound) {
				a.log.Debug("could not read a container's memory for the memory valve", "runner", job.runnerID, "error", err)
			}
			continue
		}
		look.usageMB[c.ID] = reading.UsageBytes >> 20
		c.PrevUsageMB, c.UsageMB = c.UsageMB, look.usageMB[c.ID]
		// The limit is what the daemon says it is: a limit somebody else changed
		// is read back, not fought.
		if limit := reading.LimitBytes >> 20; limit > 0 && limit != c.LimitMB {
			c.LimitMB = limit
			c.VirtualLimitMB = max(c.VirtualLimitMB, limit)
		}
		look.containers = append(look.containers, c)
	}
	if len(look.containers) == 0 {
		return memoryLook{}, false
	}
	look.rediscov = job.rediscov
	return look, true
}

// hostMemory is the host's free memory and swap as it is now, and whether it
// could be read. A host that cannot be read is one nothing is lent from.
func (a *Agent) hostMemory() (machine.Memory, bool) {
	a.mu.Lock()
	infos := a.backendInfo
	a.mu.Unlock()
	_, memoryMB := hostSize(infos, a.machine())
	if a.opts.ReadMemory != nil {
		return a.opts.ReadMemory(memoryMB)
	}
	if remoteDaemon(infos) {
		// A remote daemon's memory cannot be read through this process's procfs.
		return machine.Memory{}, false
	}
	return a.usageSampler.Memory(memoryMB)
}

// decideMemory runs the guard over one runner's containers and applies, or in
// observe mode records, what it decides.
// It returns how much memory it raised limits by.
func (a *Agent) decideMemory(ctx context.Context, look memoryLook, rules memoryRules, host machine.Memory, measured bool, now time.Time) (taken int64) {
	rule := look.rule
	observing := rule.Mode == store.MemoryBurstObserve

	// Most pressed container first, so a pair's daemon, which is doing the
	// building, is not made to wait on the runner beside it.
	order := slices.Clone(look.containers)
	slices.SortStableFunc(order, func(x, y valveContainer) int {
		px, py := float64(look.usageMB[x.ID])/float64(max(x.LimitMB, 1)), float64(look.usageMB[y.ID])/float64(max(y.LimitMB, 1))
		switch {
		case px > py:
			return -1
		case px < py:
			return 1
		}
		return 0
	})

	code, reason := MemoryHealthy, ""
	hot, near := false, false
	for i := range order {
		c := &order[i]
		usage := look.usageMB[c.ID]
		limit, swap := c.LimitMB, c.SwapMB
		if observing {
			limit, swap = c.VirtualLimitMB, c.VirtualSwapMB
		}
		if float64(usage) >= memoryNearShare*float64(c.LimitMB) {
			near = true
		}
		if MemoryHot(usage, limit) || MemoryClimbing(c.PrevUsageMB, usage, limit, now.Sub(look.lastLook)) {
			hot = true
		}

		held := int64(0)
		for _, other := range order {
			if observing {
				held += other.VirtualLimitMB
			} else {
				held += other.LimitMB
			}
		}
		in := MemoryGuardInput{
			UsageMB: usage, LimitMB: limit, SwapMB: swap,
			HeldMB: held, CeilingMB: rule.CeilingMB,
			PoolMB:      a.memoryPoolMB(rules, observing),
			AvailableMB: host.AvailableMB, FloorMB: rules.floorMB, AvailableKnown: measured,
			SpillMB: rule.SpillMB, SwapFreeMB: host.SwapFreeMB, SwapKnown: measured,
			Observing: observing,
		}
		d := GuardMemory(in)
		if d.Code != MemoryHealthy {
			code, reason = d.Code, d.Reason
		}
		if !d.Changed(in) {
			continue
		}
		if observing {
			c.VirtualLimitMB, c.VirtualSwapMB = d.LimitMB, d.SwapMB
			a.setVirtual(look.runnerID, c.ID, d)
			continue
		}
		if err := a.raiseMemory(ctx, look, *c, d); err != nil {
			if errors.Is(err, backend.ErrNotFound) {
				// The runner finished between the look and the raise.
				continue
			}
			code, reason = a.memoryFailure(look, err), err.Error()
			continue
		}
		taken += d.LimitMB - c.LimitMB
		c.LimitMB, c.SwapMB = d.LimitMB, d.SwapMB
		a.setReal(look.runnerID, c.ID, d)
	}
	a.finishMemoryLook(look, code, reason, hot, near, now)
	return taken
}

// memoryPoolMB is what the host may still lend: its capacity less what its
// runners hold. An observing runner draws on the same pool as one that acts, so
// what it would have taken is counted for observers and not for the runners
// that are really taking it.
func (a *Agent) memoryPoolMB(rules memoryRules, observing bool) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	var lent int64
	for _, t := range a.runners {
		if t.valve == nil {
			continue
		}
		real, _ := t.valve.lentMB()
		lent += real
		if observing && t.valve.rule != nil && t.valve.rule.Mode == store.MemoryBurstObserve {
			would, _ := t.valve.wouldLendMB()
			lent += max(would-real, 0)
		}
	}
	return max(rules.capacityMB-lent, 0)
}

// raiseMemory asks the daemon to raise one container.
func (a *Agent) raiseMemory(ctx context.Context, look memoryLook, c valveContainer, d MemoryGuardDecision) error {
	b, err := a.opts.Backends.Get(look.kind)
	if err != nil {
		return err
	}
	u, ok := b.(backend.MemoryUpdater)
	if !ok {
		return backend.ErrMemoryUpdateUnsupported
	}
	rctx, cancel := context.WithTimeout(ctx, memoryLookTimeout)
	defer cancel()
	if err := u.RaiseMemory(rctx, c.ID, d.LimitMB, d.SwapMB); err != nil {
		return err
	}
	a.log.Info("raised a runner's memory limit",
		"runner", look.runnerID, "container", shortContainer(c.ID),
		"from_mb", c.LimitMB, "to_mb", d.LimitMB, "swap_mb", d.SwapMB,
		"code", string(d.Code), "reason", d.Reason)
	return nil
}

// memoryFailure files a refused raise: a runtime with no way to do it is not
// asked again; any other refusal is logged once per reason and tried again at
// the next look. It returns the code the failure is reported under.
func (a *Agent) memoryFailure(look memoryLook, err error) MemoryValveCode {
	switch {
	case errors.Is(err, backend.ErrMemoryUpdateUnsupported):
		a.mu.Lock()
		if a.memoryUnsupported == nil {
			a.memoryUnsupported = map[store.BackendKind]string{}
		}
		_, known := a.memoryUnsupported[look.kind]
		a.memoryUnsupported[look.kind] = err.Error()
		a.mu.Unlock()
		if !known {
			a.log.Warn("this host's container runtime cannot change a live container's memory limit, so the memory valve cannot lend runners memory on it; it will not be asked again until the agent restarts",
				"backend", string(look.kind), "error", err)
		}
		return MemoryUnsupported
	}
	a.mu.Lock()
	t := a.runners[look.runnerID]
	first := false
	if t != nil && t.valve != nil {
		if t.valve.warned == nil {
			t.valve.warned = map[string]bool{}
		}
		first = !t.valve.warned[err.Error()]
		t.valve.warned[err.Error()] = true
	}
	a.mu.Unlock()
	if first {
		a.log.Warn("could not raise a runner's memory limit; it will be tried again", "runner", look.runnerID, "error", err)
	}
	return MemoryFailed
}

// setReal and setVirtual record a decision on the tracked runner's container, so
// that the next pool figure and the next report see it before the look that made
// it has finished.
func (a *Agent) setReal(runnerID, containerID string, d MemoryGuardDecision) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.runners[runnerID]
	if t == nil || t.valve == nil {
		return
	}
	for i := range t.valve.containers {
		if c := &t.valve.containers[i]; c.ID == containerID {
			if d.LimitMB > c.LimitMB {
				t.valve.raises++
			}
			c.LimitMB, c.SwapMB = d.LimitMB, d.SwapMB
			c.VirtualLimitMB, c.VirtualSwapMB = max(c.VirtualLimitMB, d.LimitMB), max(c.VirtualSwapMB, d.SwapMB)
		}
	}
}

func (a *Agent) setVirtual(runnerID, containerID string, d MemoryGuardDecision) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.runners[runnerID]
	if t == nil || t.valve == nil {
		return
	}
	for i := range t.valve.containers {
		if c := &t.valve.containers[i]; c.ID == containerID {
			c.VirtualLimitMB, c.VirtualSwapMB = d.LimitMB, d.SwapMB
		}
	}
}

// adoptMemoryLook stores the containers a look found. The guard loop is the one
// writer of them, so what it found is what is stored, with what an observing
// agent has decided carried on each container's own virtual limits.
func (a *Agent) adoptMemoryLook(look memoryLook, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.runners[look.runnerID]
	if t == nil || t.valve == nil || t.handle != look.handle {
		return
	}
	t.valve.containers = slices.Clone(look.containers)
	if look.rediscov {
		t.valve.discoveredAt = now
	}
}

// finishMemoryLook stores what a look decided: when it happened, how close the
// runner is to a limit, and the decision's code and reason.
func (a *Agent) finishMemoryLook(look memoryLook, code MemoryValveCode, reason string, hot, near bool, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.runners[look.runnerID]
	if t == nil || t.valve == nil || t.handle != look.handle {
		return
	}
	v := t.valve
	v.lastLook = now
	v.hot = hot
	v.nearLimit = v.nearLimit || near
	v.code = code
	if reason != "" {
		v.reason = reason
	}
}

// remoteDaemon reports whether any container backend answers over the network.
// Its host's memory cannot be read through this process's procfs, even when the
// two machines happen to be the same size.
func remoteDaemon(infos []backend.Info) bool {
	for _, info := range infos {
		if info.Available && info.Kind != store.BackendProcess && !isLocalEndpoint(info.Endpoint) {
			return true
		}
	}
	return false
}

func isLocalEndpoint(endpoint string) bool {
	return strings.HasPrefix(endpoint, "unix://") || strings.HasPrefix(endpoint, "/")
}

func shortContainer(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// memoryGuardLoop wakes every tick and runs the guard.
func (a *Agent) memoryGuardLoop(ctx context.Context) error {
	t := time.NewTicker(memoryGuardTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		a.guardMemory(ctx)
	}
}
