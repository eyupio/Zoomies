package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/store"
)

// Recovery is not a startup path. It runs before demand on every pass, so a
// controller that has just come back is simply having the pass where nothing
// was in memory: the rows say what was being done, the provider says what
// actually happened, and the difference between the two is resolved by asking
// rather than by acting. A separate "recover once at start" path would be code
// that runs on the rarest occasion and is therefore never right.
//
// What this file does is the half of recovery that asks nothing of a provider:
// reservations that expired, phases that have run out of time, and the paced
// ownership sweep. The half that needs a provider call is the step function's,
// because a machine whose create was lost is resolved by exactly the same
// Inspect whether the controller restarted or merely blinked.

// recoverMachineOperations walks the machines with unfinished work and settles
// what can be settled from the rows alone.
//
// A keyset cursor over a bounded batch, recoverHostCleanup's shape: a machine
// stuck behind an unreachable node must not starve the rest, and a fleet with a
// month of history must not read it every thirty seconds.
func (c *Controller) recoverMachineOperations(ctx context.Context, env *machineEnv) {
	rows, err := c.st.PendingMachineWork(ctx, c.machines.cursor, machineSweepBatch)
	if err != nil {
		c.log.Warn("could not read the machines with unfinished work", "error", err)
		return
	}
	for _, m := range rows {
		c.machines.cursor = m.ID
		if live := env.machine(m.ID); live != nil {
			// The pass's own copy is the one every later step reads, so the
			// recovery works on that rather than on a second row that would
			// then disagree with it.
			m = live
		}
		c.recoverMachine(ctx, env, m)
	}
	if len(rows) < machineSweepBatch {
		c.machines.cursor = ""
	}
}

// recoverMachine settles one machine's overdue phase.
//
// Every judgement here is made against a stamp on the row rather than against
// anything held in memory, which is what makes a restart indistinguishable from
// a slow pass.
func (c *Controller) recoverMachine(ctx context.Context, env *machineEnv, m *store.Machine) {
	cfg := env.cfg.Provider
	now := env.now
	switch m.State {
	case store.MachinePlanned:
		// A reservation that never became a create. Nothing was issued -- the
		// identity is written before the call and this row has none -- so
		// failing it costs nothing and frees the ceiling it was holding.
		if m.ResourceID == "" && m.ReservationExpiresAt != nil && now.After(*m.ReservationExpiresAt) {
			c.transitionMachine(ctx, env, m, store.MachineFailed,
				"the create for this machine was never issued, so the reservation was released")
		}
	case store.MachineCreating:
		// provider.create_timeout bounds the whole creation, but only a
		// resource the provider cannot find was ever timed (the ambiguity
		// timeout). A create whose task is lost or whose resource stays locked
		// waited for ever, holding a create slot and a ceiling slot while the
		// VM was billed. An unheard outcome stays the ambiguity path's: that
		// one quarantines rather than fails, and asks a person to look.
		if !m.OpOutcomeUnknown && overdue(m.CreateStartedAt, nil, now, cfg.CreateTimeout) {
			c.failMachine(ctx, env, m, store.MachineErrorProvider, fmt.Sprintf(
				"the provider has not finished creating this machine inside %s. The resource may exist and is "+
					"still being paid for: check the provider's console",
				roundDuration(cfg.CreateTimeout)))
		}
	case store.MachineStarting:
		if !m.OpOutcomeUnknown && overdue(m.CreatedOKAt, nil, now, cfg.CreateTimeout) {
			c.failMachine(ctx, env, m, store.MachineErrorProvider, fmt.Sprintf(
				"the machine was created but did not start inside %s. The resource exists and is still being "+
					"paid for: check the provider's console",
				roundDuration(cfg.CreateTimeout)))
		}
	case store.MachineBootstrapping:
		if overdue(m.StartedAt, m.CreatedOKAt, now, cfg.BootstrapTimeout) {
			c.failMachine(ctx, env, m, store.MachineErrorBootstrap, fmt.Sprintf(
				"the agent was not installed inside %s. The machine is running and is still being paid for: "+
					"check that the template has the zoomies agent installed and the guest agent answering",
				roundDuration(cfg.BootstrapTimeout)))
		}
	case store.MachineEnrolling:
		if overdue(m.BootstrappedAt, m.StartedAt, now, cfg.EnrolTimeout) {
			c.failMachine(ctx, env, m, store.MachineErrorBootstrap, fmt.Sprintf(
				"the agent was installed but no host joined within %s. The machine is running and is still being "+
					"paid for: check that it can reach this controller's external URL",
				roundDuration(cfg.EnrolTimeout)))
		}
	case store.MachineDeleting:
		// A delete that has not confirmed is a machine somebody may still be
		// paying for, so it is never quietly forgotten: past the timeout a
		// person is asked rather than the row being stamped on a guess.
		if overdue(m.DeleteStartedAt, nil, now, cfg.DeleteTimeout) && m.ResourceID != "" {
			_ = c.quarantineMachine(ctx, env, m, fmt.Sprintf(
				"this machine's delete was issued %s ago and %s has not confirmed the resource is gone. "+
					"Nothing further has been attempted: check the provider's console, then either release this "+
					"row -- which forgets the resource without touching it -- or delete the resource by hand",
				roundDuration(now.Sub(deref(m.DeleteStartedAt, now))), providerLabelFor(env.rows, m.ProviderID)))
		}
	}
}

// failMachine records which half complained and gives up on the lifecycle,
// leaving the row and whatever resource is behind it exactly where they are.
func (c *Controller) failMachine(ctx context.Context, env *machineEnv, m *store.Machine, src store.MachineErrorSource, detail string) {
	next := c.Now().Add(machineRetryMax)
	if err := c.st.RecordMachineFailure(ctx, m.ID, src, detail, next); err != nil {
		c.log.Warn("could not record why a machine was given up on", "machine", m.ID, "error", err)
	}
	c.transitionMachine(ctx, env, m, store.MachineFailed, detail)
}

// overdue reports whether the phase that began at from (or at fallback, for a
// provider whose create skipped a phase) has run past d.
//
// A phase with no stamp at all is never overdue: a missing timestamp is a
// question, and answering it with "give up" would fail machines for a column
// nobody wrote.
func overdue(from, fallback *time.Time, now time.Time, d time.Duration) bool {
	if d <= 0 {
		return false
	}
	at := from
	if at == nil {
		at = fallback
	}
	if at == nil {
		return false
	}
	return now.Sub(*at) > d
}

func deref(t *time.Time, or time.Time) time.Time {
	if t == nil {
		return or
	}
	return *t
}

// ---------------------------------------------------------------------------
// The ownership sweep
// ---------------------------------------------------------------------------

// sweepProviders asks each provider, at most every sweep interval, for
// everything it believes it is running, and compares that against the rows.
//
// It is paced rather than run every pass because it is one call per provider
// and the answer changes slowly. What it is for is the two things no machine's
// own step can see: a resource that went away outside Zoomies, and a resource
// wearing this fleet's marks that no row accounts for.
func (c *Controller) sweepProviders(ctx context.Context, env *machineEnv) {
	interval := env.cfg.Provider.SweepInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	for _, row := range env.rows {
		if row.LastSweepAt != nil && env.now.Sub(*row.LastSweepAt) < interval {
			continue
		}
		pr, err := c.providerFor(ctx, row)
		if err != nil {
			c.noteProviderTrouble(row.ID, err, "", env.now)
			continue
		}
		if c.machines.sweeps == nil {
			c.machines.sweeps = map[string]bool{}
		}
		if c.machines.sweeps[row.ID] || len(c.machines.sweeps) >= 4 || env.now.Before(c.machines.sweepRetry[row.ID]) {
			continue
		}
		c.machines.sweeps[row.ID] = true
		// Freeze evidence before the pass can advance any machine rows.
		inventory := *env
		inventory.list = make([]*store.Machine, len(env.list))
		for i, m := range env.list {
			copy := *m
			inventory.list[i] = &copy
		}
		c.machines.calls.Add(1)
		go func() {
			defer c.machines.calls.Done()
			defer func() { c.machines.mu.Lock(); delete(c.machines.sweeps, pr.row.ID); c.machines.mu.Unlock() }()
			c.sweepProvider(ctx, &inventory, pr)
		}()
	}
}

// sweepProvider is one provider's six-way comparison.
//
// The rule for what it may act on is deliberately narrow, because this code is
// one call away from destroying somebody else's machine:
//
//   - a resource wearing our marks with a row is verified, and nothing more;
//   - a resource wearing our name grammar with no row at all is an UNTRACKED
//     resource: reported, listed on the orphan page, and never deleted;
//   - a resource whose marks name another machine is a quarantine, not a
//     delete;
//   - a row whose resource is absent is confirmed gone only when the row was
//     already being deleted, and otherwise says so and drains the host.
//
// Anything live and unexplained is left alone. That is reap's rule about GitHub
// registrations, applied where the mistake costs a running machine.
// sweepTimeout is one provider's sweep budget.
//
// A call apiece, because a sweep is a listing plus a read per candidate rather
// than the two requests stepTimeout budgets for -- Proxmox reads one
// configuration per guest in the range to find its marks. Never longer than the
// interval that paces it: a sweep still running when the next one is due has
// already failed at being paced. Derived from provider.call_timeout, which on
// this path was otherwise never consulted at all.
func (c *Controller) sweepTimeout(env *machineEnv, pr *machineProvider) time.Duration {
	d := env.cfg.Provider.CallTimeout
	if d <= 0 {
		d = 30 * time.Second
	}
	if ask := pr.p.Capabilities().Deadlines.Call; ask > d {
		d = ask
	}
	rows := 0
	for _, m := range env.list {
		if m.ProviderID == pr.row.ID {
			rows++
		}
	}
	// Two spare, so a provider with no rows yet still has room for the listing
	// and for the orphan it is about to find.
	budget := d * time.Duration(rows+2)
	if ceiling := env.cfg.Provider.SweepInterval; ceiling > 0 && budget > ceiling {
		budget = ceiling
	}
	return budget
}

func (c *Controller) sweepProvider(ctx context.Context, env *machineEnv, pr *machineProvider) {
	owner := provider.Owner{ControllerID: c.controllerID(), ProviderID: pr.row.ID}
	// Bounded, because this call is made from inside the pass and everything
	// after it -- every reservation, every drain and every delete -- waits on
	// it. A sweep cut short is retried by the next pass with nothing stamped
	// and nothing acted on, which is much the cheaper of the two failures.
	// Only the call: the store writes below must not inherit a deadline a slow
	// List has already spent.
	listCtx, cancel := context.WithTimeout(ctx, c.sweepTimeout(env, pr))
	seen, err := pr.p.List(listCtx, owner)
	cancel()
	if err != nil {
		c.noteProviderTrouble(pr.row.ID, err, "", env.now)
		c.machines.mu.Lock()
		if c.machines.sweepRetry == nil {
			c.machines.sweepRetry = map[string]time.Time{}
		}
		c.machines.sweepRetry[pr.row.ID] = c.Now().Add(30 * time.Second)
		c.machines.mu.Unlock()
		c.log.Warn("could not list a provider's resources", "provider", pr.row.Name, "error", err)
		return
	}
	// The remote listing must not hold the machine pass lock. Apply it to a
	// fresh local snapshot, since creates and operator actions continued meanwhile.
	c.machines.mu.Lock()
	defer c.machines.mu.Unlock()
	if !c.mayAct() {
		return
	}
	rows, readErr := c.st.ListProviders(ctx)
	if readErr != nil {
		return
	}
	fresh, readErr := c.machineSnapshot(ctx, rows)
	if readErr != nil {
		return
	}
	current := fresh.provider(pr.row.ID)
	if current == nil || !current.UpdatedAt.Equal(pr.row.UpdatedAt) {
		return
	}
	baseline := make(map[string]*store.Machine, len(env.list))
	for _, m := range env.list {
		baseline[m.ID] = m
	}
	unchanged := func(m *store.Machine) bool {
		old := baseline[m.ID]
		return old != nil && old.State == m.State && old.ResourceID == m.ResourceID &&
			old.ResourceZone == m.ResourceZone && old.UpdatedAt.Equal(m.UpdatedAt)
	}
	env = fresh
	// Recorded even when the sweep found nothing: the interval is paced by
	// this stamp, and a sweep that found nothing still happened.
	if err := c.st.SetProviderSwept(ctx, pr.row.ID, env.now); err != nil {
		c.log.Warn("could not record a provider's sweep", "provider", pr.row.ID, "error", err)
	}

	byResource := map[string]*store.Machine{}
	for _, m := range env.list {
		if m.ProviderID == pr.row.ID && m.ResourceID != "" {
			byResource[m.ResourceZone+"/"+m.ResourceID] = m
		}
	}

	var orphans []string
	found := map[string]bool{}
	for _, got := range seen {
		key := got.Ref.Zone + "/" + got.Ref.ID
		m := byResource[key]
		if m == nil && got.Ref.ID == "" {
			m = byResource[got.Ref.Zone+"/"+got.Ref.Name]
			key = got.Ref.Zone + "/" + got.Ref.Name
		}
		if m == nil {
			// No row at all. If it wears our naming grammar it is ours and
			// something lost track of it; if it wears somebody else's marks it
			// is another fleet's. Neither is ever deleted from here.
			if store.IsMachineName(got.Ref.Name) {
				orphans = append(orphans, got.Ref.Name)
			}
			continue
		}
		found[key] = true
		if !unchanged(m) {
			continue
		}
		if why := c.ownershipComplaint(m, got); why != "" {
			if m.State != store.MachineQuarantined {
				_ = c.quarantineMachine(ctx, env, m, why)
			}
			continue
		}
		if err := c.st.SetMachineOwnershipVerified(ctx, m.ID, env.now); err != nil {
			c.log.Warn("could not record a machine's ownership", "machine", m.ID, "error", err)
		}
	}
	slices.Sort(orphans)
	c.noteOrphans(pr.row.ID, orphans)

	for key, m := range byResource {
		if found[key] || !unchanged(m) {
			continue
		}
		c.sweepAbsent(ctx, env, pr, m)
	}
}

// sweepAbsent decides what a resource's absence means for the row that names
// it.
//
// Absence is only ever acted on for a machine that was already on its way out.
// For one the fleet believes is working it is reported and its host drained,
// because a machine that vanished under us has taken whatever it was running
// with it -- and there is nothing left to delete.
func (c *Controller) sweepAbsent(ctx context.Context, env *machineEnv, pr *machineProvider, m *store.Machine) {
	switch m.State {
	case store.MachineDeleting, store.MachineFailed:
		if _, err := c.st.ConfirmMachineDeleted(ctx, m.ID, env.now); err != nil {
			c.log.Warn("could not confirm a machine's resource is gone", "machine", m.ID, "error", err)
			return
		}
		c.transitionMachine(ctx, env, m, store.MachineDeleted,
			fmt.Sprintf("%s no longer has this machine's resource", pr.row.Name))
		if m.HostID != "" {
			if err := c.DeleteHost(ctx, m.HostID); err != nil && !errors.Is(err, store.ErrNotFound) {
				c.log.Warn("a machine's resource is gone but its host row could not be removed",
					"machine", m.ID, "host", m.HostID, "error", err)
			}
		}
	case store.MachineCreating, store.MachinePlanned:
		// The create's own ambiguity path owns this: a machine that is being
		// built and cannot be seen yet is not a machine that has gone.
	case store.MachineQuarantined, store.MachineDeleted:
	default:
		// ready, enrolling, starting, bootstrapping, draining: the resource
		// went away outside Zoomies.
		detail := fmt.Sprintf("%s no longer has this machine's resource, so it was destroyed outside Zoomies. "+
			"There is nothing left to delete; anything it was running has been lost", pr.row.Name)
		if m.HostID != "" {
			if err := c.st.SetHostCordoned(ctx, m.HostID, true); err != nil {
				c.log.Warn("could not cordon a vanished machine's host", "machine", m.ID, "error", err)
			}
		}
		c.failMachine(ctx, env, m, store.MachineErrorProvider, detail)
		c.log.Warn("a machine's resource vanished outside Zoomies", "machine", m.ID,
			"name", m.Name, "provider", pr.row.Name)
	}
}

// ---------------------------------------------------------------------------
// Enrolment, which is an observation of our own rows
// ---------------------------------------------------------------------------

// stepEnrolling links a machine to the host that redeemed its token.
//
// The link is the only thing that grants this controller deletion authority
// over a host, and the only writer of it is a token this machine minted. There
// is deliberately no route, verb or label that attaches a machine to a host an
// operator already had: a host that could be labelled into deletion authority
// is a host anyone with the operator role can have destroyed.
func (c *Controller) stepEnrolling(ctx context.Context, env *machineEnv, m *store.Machine) {
	if m.HostID != "" {
		c.transitionMachine(ctx, env, m, store.MachineReady, "the agent joined and this machine is serving runners")
		return
	}
	if m.JoinTokenID == "" {
		// Nothing was minted, so nothing can have joined. Back to bootstrapping
		// is not a transition the state machine allows, so this waits for the
		// enrolment timeout to say so in words.
		return
	}
	tok, err := c.st.GetJoinToken(ctx, m.JoinTokenID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			c.log.Warn("could not read a machine's join token", "machine", m.ID, "error", err)
		}
		return
	}
	if tok.UsedAt == nil || tok.UsedByID == "" {
		return
	}
	if tok.MachineID != "" && tok.MachineID != m.ID {
		_ = c.quarantineMachine(ctx, env, m, fmt.Sprintf(
			"this machine's join token was spent by machine %s rather than by this one. Nothing has been deleted: "+
				"a credential that enrolled something else is a copied guest, and which machine owns host %s has to be "+
				"decided by a person", tok.MachineID, tok.UsedByID))
		return
	}
	if err := c.st.LinkMachineHost(ctx, m.ID, tok.UsedByID, tok.ID, env.now); err != nil {
		if errors.Is(err, store.ErrConflict) {
			_ = c.quarantineMachine(ctx, env, m, fmt.Sprintf(
				"this machine's token enrolled host %s, which another machine already claims. Nothing has been "+
					"deleted; a person has to decide which machine that host belongs to", tok.UsedByID))
			return
		}
		c.log.Warn("could not link a machine to the host it enrolled as", "machine", m.ID, "error", err)
		return
	}
	m.HostID = tok.UsedByID
	c.transitionMachine(ctx, env, m, store.MachineReady, "the agent joined and this machine is serving runners")
	c.log.Info("a machine became a host", "machine", m.ID, "name", m.Name, "host", m.HostID)
	// A new host changes where runners can be placed, and the scheduling pass
	// is the half that places them.
	c.Nudge()
}
