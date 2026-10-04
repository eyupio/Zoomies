package controller

import (
	"context"
	"errors"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// The throttle is decided in the scheduler (NextThrottle, pure) and applied
// here, from two places: every heartbeat, which is where a fresh measurement
// arrives, and the housekeeping pass, which is what lifts a throttle whose
// host has stopped sending measurements at all. Both go through settleThrottle
// so that a step is persisted, logged, audited and published in exactly one
// way -- a rung the Hosts page showed and the audit trail did not would be a
// decision nobody could account for afterwards.

// settleThrottle runs the ladder for one host as it stands now and records
// whatever moved. It reports whether the rung changed.
//
// h is updated in place, so a caller that goes on to render or answer with
// the host sees the rung it is actually on. A change that moves no rung -- a
// calm streak starting -- is persisted and published but neither logged nor
// audited: the streak is bookkeeping for the next step down, and a log line
// per heartbeat about a host that is merely recovering would bury the lines
// about the ones that are not.
func (c *Controller) settleThrottle(ctx context.Context, h *store.Host, now time.Time) bool {
	next := scheduler.NextThrottle(h, now)
	if sameThrottle(next, h.Throttle) {
		return false
	}
	if err := c.st.SetHostThrottle(ctx, h.ID, next, h.Throttle.Level); err != nil {
		if errors.Is(err, store.ErrThrottleMoved) {
			// Somebody else decided first: a heartbeat racing the
			// housekeeping tick, or an operator's clear. Their decision
			// stands, and the next sample decides from it.
			c.log.Debug("a host's throttle was decided elsewhere first", "host", h.ID)
			return false
		}
		c.log.Warn("could not record a host's throttle; the next heartbeat will decide again", "host", h.ID, "error", err)
		return false
	}
	was := h.Throttle
	h.Throttle = next
	if next.Level == was.Level {
		c.publishHost(h)
		return false
	}
	c.recordThrottleStep(ctx, h, was, next, true)
	return true
}

// recordThrottleStep is the log line, the audit row, the event and the nudge
// for a rung that moved in either direction. audit is false for an operator's
// clear, whose caller writes the row itself under the operator's own name:
// a lift the ladder decided and a lift somebody asked for are two different
// facts, and an audit trail with both attributed to the system could not
// tell an operator which of their colleagues cleared a throttle early.
func (c *Controller) recordThrottleStep(ctx context.Context, h *store.Host, was, next store.HostThrottle, audit bool) {
	action := "host.throttle"
	switch {
	case next.Level > was.Level:
		c.log.Info("throttled a host after sustained pressure",
			"host", h.ID, "name", h.Name, "level", next.Level, "was", was.Level,
			"effective_capacity", h.EffectiveCapacity(), "capacity", h.Capacity,
			"cpu_factor", next.CPUFactor(), "reason", next.Reason)
	case next.Active():
		action = "host.throttle_lift"
		c.log.Info("lifted a host's throttle one step after a stretch of calm",
			"host", h.ID, "name", h.Name, "level", next.Level, "was", was.Level,
			"effective_capacity", h.EffectiveCapacity(), "capacity", h.Capacity)
	default:
		action = "host.throttle_lift"
		c.log.Info("lifted a host's throttle",
			"host", h.ID, "name", h.Name, "was", was.Level, "capacity", h.Capacity)
	}
	if audit {
		c.authsvc.Auditor().Act(ctx, auth.SystemIdentity(), action, "host", h.ID, map[string]any{
			"level":              next.Level,
			"reason":             firstNonEmpty(next.Reason, was.Reason),
			"effective_capacity": h.EffectiveCapacity(),
		})
	}
	// The slots the host offers just changed, and both the Hosts page and the
	// next pass have to know: a lift is room a queued job may be waiting for.
	c.publishHost(h)
	c.Nudge()
}

// settleThrottles is the housekeeping half: every host on a rung is run
// through the ladder, so that a host whose agent stopped sending measurements
// -- downgraded, or gone -- is lifted after StaleThrottleReset rather than
// staying throttled for as long as nobody heartbeats. A host with no throttle
// needs nothing here; the heartbeat that could put it on a rung runs the
// ladder itself.
//
// Throttling switched off at runtime is also settled here, by lifting every
// throttle that stands: the heartbeat path stops deciding when the setting is
// off, and a rung nothing will ever step down from again is a cordon nobody
// applied.
func (c *Controller) settleThrottles(ctx context.Context, now time.Time) {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		c.log.Warn("could not list hosts to settle their throttles", "error", err)
		return
	}
	enabled := c.cfg().Scheduler.HostThrottling
	for _, h := range hosts {
		if !h.Throttle.Active() {
			continue
		}
		// The demo fleet's hosts are left alone, as they are everywhere else
		// in housekeeping: its throttled host is a fixture, on a rung so the
		// UI can be looked at with one, and it would otherwise lift itself
		// the moment its seeded measurement went stale.
		if IsDemoID(h.ID) {
			continue
		}
		if !enabled {
			c.log.Info("lifting a host's throttle because scheduler.host_throttling is off", "host", h.ID, "name", h.Name)
			if _, err := c.clearThrottle(ctx, h, true); err != nil {
				c.log.Warn("could not lift a host's throttle", "host", h.ID, "error", err)
			}
			continue
		}
		// A host with a fresh sample is being decided by its heartbeats, and
		// housekeeping has nothing to add but a second decider racing the
		// first. This pass is for the host that has stopped sending samples.
		if h.Usage.Fresh(now) {
			continue
		}
		c.settleThrottle(ctx, h, now)
	}
}

// ClearHostThrottle lifts a host's throttle by hand, whatever rung it is on.
//
// It is the operator's way out once the cause is fixed -- a pool given the
// limits it lacked, a capacity lowered to what the machine can carry --
// without waiting out the recovery the ladder would otherwise ask for. The
// caller audits, because it knows who asked; this writes nothing to the audit
// trail itself. It logs the lift, publishes the host and nudges a pass, since
// the slots it gives back are room a queued job may be waiting on. A host
// that is not throttled is returned unchanged.
//
// Nothing pins a throttle the other way: if the pressure is still there, the
// next heartbeat puts the host back on the first rung, which is the honest
// answer to a clear that was premature.
func (c *Controller) ClearHostThrottle(ctx context.Context, id string) (*store.Host, error) {
	h, err := c.st.GetHost(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.clearThrottle(ctx, h, false)
}

func (c *Controller) clearThrottle(ctx context.Context, h *store.Host, audit bool) (*store.Host, error) {
	if !h.Throttle.Active() {
		return h, nil
	}
	if err := c.st.SetHostThrottle(ctx, h.ID, store.HostThrottle{}, h.Throttle.Level); err != nil {
		if errors.Is(err, store.ErrThrottleMoved) {
			// The rung moved between the read and the clear. Read it again
			// and clear from where it is now: a clear is an instruction
			// about the host, not about the rung it happened to be on.
			fresh, rerr := c.st.GetHost(ctx, h.ID)
			if rerr != nil {
				return nil, rerr
			}
			return c.clearThrottle(ctx, fresh, audit)
		}
		return nil, err
	}
	was := h.Throttle
	h.Throttle = store.HostThrottle{}
	c.recordThrottleStep(ctx, h, was, h.Throttle, audit)
	return h, nil
}

// throttleDirective is what a heartbeat answers about the host's rung. It is
// always sent, level 0 and factor 1 included: an agent that was told to run
// its runners at half their quota has to be told when to stop, and "no field"
// from a controller too old to send one already means "no throttle".
//
// The CPU factor is the ladder's, raised where the host's profile sets a
// minimum runner size so that no runner is throttled below it (see
// scheduler.ThrottleFloorFactor). That is decided here and not on the agent:
// the agent already scales each runner by whatever one factor it is sent, so a
// controller-side floor needs no new field, no feature flag and no protocol
// bump, and an agent that has never heard of profiles obeys it unchanged.
func (c *Controller) throttleDirective(ctx context.Context, h *store.Host) *agent.ThrottleDirective {
	factor := h.Throttle.CPUFactor()
	if floor := h.RunnerProfile.Minimum.CPUs; floor > 0 && factor < 1 {
		factor = max(factor, scheduler.ThrottleFloorFactor(floor, c.limitedContainerCPUs(ctx, h)))
	}
	return &agent.ThrottleDirective{Level: h.Throttle.Level, CPUFactor: factor}
}

// limitedContainerCPUs is the CPU quota each limited container on h was created
// with: one per runner, or two for a docker-in-docker pair -- a typed figure is
// given to both halves, a share is split between them. A runner created with no
// limit has nothing to scale and is left out. A read that fails answers with
// nothing, which leaves the ladder's own factor standing: a throttle that
// cannot find its floor is still a throttle.
func (c *Controller) limitedContainerCPUs(ctx context.Context, h *store.Host) []float64 {
	runners, err := c.st.ListRunnersForHost(ctx, h.ID)
	if err != nil {
		c.log.Warn("could not read a host's runners to find its throttle floor", "host", h.ID, "error", err)
		return nil
	}
	pools := map[string]*store.Pool{}
	var out []float64
	for _, r := range runners {
		if !r.State.Live() || r.AllocatedCPUs <= 0 {
			continue
		}
		p, seen := pools[r.PoolID]
		if !seen {
			p, _ = c.st.GetPool(ctx, r.PoolID)
			pools[r.PoolID] = p
		}
		perHalf := r.AllocatedCPUs
		if p != nil && p.DockerMode == store.DockerDinD && !typedAllocation(r, p.Resources.CPUs > 0) {
			// One slot split between the runner and its daemon.
			perHalf /= 2
		}
		out = append(out, perHalf)
	}
	return out
}

// sameThrottle reports whether two rungs are the same row: the ladder returns
// a fresh value on every call, and only a value that differs is worth a write.
func sameThrottle(a, b store.HostThrottle) bool {
	return a.Level == b.Level && a.Reason == b.Reason &&
		sameInstant(a.Since, b.Since) && sameInstant(a.ChangedAt, b.ChangedAt) && sameInstant(a.CalmSince, b.CalmSince)
}

func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
