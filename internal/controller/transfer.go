package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

type TransferProgress = store.TransferProgress

// TransferProgress is where preparation stands, with the sentence that says
// so. The store counts and names what is in the way; the words are written
// here, where the fleet's other reasons are, so the page and the CLI show the
// same ones.
func (c *Controller) TransferProgress(ctx context.Context) (TransferProgress, error) {
	p, err := c.st.TransferProgress(ctx)
	if err != nil {
		return p, err
	}
	c.transferWords(&p)
	return p, nil
}

// publishTransfer sends the preparation to every open page now, for the
// handlers that changed it: a click should repaint before the next pass does.
func (c *Controller) publishTransfer(ctx context.Context) {
	if c.bus == nil || c.bus.Subscribers() == 0 || ctx.Err() != nil {
		return
	}
	c.derivedMu.Lock()
	defer c.derivedMu.Unlock()
	c.sendTransfer(ctx)
}

// sendTransfer publishes a preparation that differs from the one last sent.
// Called with derivedMu held, and with a subscriber on the bus.
//
// Nothing is computed, and nothing sent, while no transfer is being prepared
// and none was when the last frame went out: a controller that is not moving
// anywhere should not read its fleet on every pass to say so.
func (c *Controller) sendTransfer(ctx context.Context) {
	if c.bus == nil {
		return
	}
	if !c.transferDraining.Load() && c.lastTransfer == nil {
		return
	}
	p, err := c.TransferProgress(ctx)
	if err != nil {
		c.log.Warn("could not work out the transfer preparation for the event stream", "error", err)
		return
	}
	if raw, changed := c.derivedChanged(&c.lastTransfer, p); changed {
		c.bus.Publish(events.KindTransfer, "", json.RawMessage(raw))
	}
}

// transferWords writes the sentence for where preparation stands and one for
// each thing it is waiting on. They are written for the operator who has
// clicked the button and is watching: what is still happening, and whether
// any of it needs them.
func (c *Controller) transferWords(p *TransferProgress) {
	now := c.Now()
	for i := range p.Waiting {
		w := &p.Waiting[i]
		// A machine is not on a host; the question does not arise for it.
		w.HostHealthy = w.Kind == store.TransferWaitMachine ||
			(w.HostID != "" && now.Sub(w.HostLastHeartbeat) < store.HeartbeatTimeout)
		w.Detail = transferWaitDetail(*w)
	}
	switch {
	case !p.Draining:
		p.Summary = "Preparation has not started."
		return
	case p.Ready:
		p.Summary = "Ready to export. The instance is fenced."
		return
	case p.Quiescent():
		p.Summary = "The fleet is drained; the recovery fence is being raised."
		return
	}
	var parts []string
	if p.BusyRunners > 0 {
		parts = append(parts, fmt.Sprintf("%s finishing %s", plural(p.BusyRunners, "busy runner"), plural(p.ActiveJobs, "job")))
	}
	if idle := p.LiveRunners - p.BusyRunners; idle > 0 {
		parts = append(parts, plural(idle, "idle runner")+" being withdrawn")
	}
	if p.PendingCleanup > 0 {
		var sides []string
		if p.CleanupAwaitingHost > 0 {
			sides = append(sides, fmt.Sprintf("%d by the host", p.CleanupAwaitingHost))
		}
		if p.CleanupAwaitingGitHub > 0 {
			sides = append(sides, fmt.Sprintf("%d by GitHub", p.CleanupAwaitingGitHub))
		}
		part := plural(p.PendingCleanup, "runner") + " waiting to be confirmed gone"
		if len(sides) > 0 {
			part += " (" + strings.Join(sides, ", ") + ")"
		}
		parts = append(parts, part)
	}
	if p.MachineOperations > 0 {
		parts = append(parts, plural(p.MachineOperations, "machine operation")+" finishing")
	}
	summary := "Preparing: " + strings.Join(parts, "; ") + "."

	// What, if anything, the operator has to do. A silent host is the one
	// thing waiting cannot fix: nobody but its agent can confirm its runners
	// gone, so it is named first; a failed cleanup is the other.
	silent := map[string]bool{}
	var silentNames []string
	for _, w := range p.Waiting {
		if (w.Kind == store.TransferWaitCleanup && !w.HostConfirmed || w.Kind == store.TransferWaitRunner) &&
			w.HostName != "" && !w.HostHealthy && !silent[w.HostName] {
			silent[w.HostName] = true
			silentNames = append(silentNames, w.HostName)
		}
	}
	sort.Strings(silentNames)
	switch {
	case len(silentNames) == 1:
		summary += fmt.Sprintf(" Host %s has stopped sending heartbeats and has runners to confirm gone: start its agent again, or remove the host so its runners are forgotten.", silentNames[0])
	case len(silentNames) > 1:
		summary += fmt.Sprintf(" Hosts %s have stopped sending heartbeats and have runners to confirm gone: start their agents again, or remove the hosts so their runners are forgotten.", strings.Join(silentNames, ", "))
	case p.CleanupFailed > 0:
		summary += fmt.Sprintf(" %s could not be cleaned up; the Runners page says what is left behind and where.", plural(p.CleanupFailed, "runner"))
	default:
		summary += " Nothing needs you yet: the fence is raised on its own when the counts reach zero."
	}
	p.Summary = summary
}

// transferWaitDetail is the sentence for one wait. How long it has waited is
// left to the reader: the page counts from `since` on its own clock, and a
// sentence that named the minutes would change every minute and be sent
// every minute for a page that could already say it.
func transferWaitDetail(w store.TransferWait) string {
	host := func() string {
		if w.HostName == "" {
			return "its host"
		}
		if w.HostHealthy {
			return w.HostName
		}
		return w.HostName + ", which has stopped sending heartbeats"
	}
	switch w.Kind {
	case store.TransferWaitRunner:
		if w.State == string(store.RunnerBusy) {
			return "running a job on " + host() + "; it is left to finish"
		}
		return w.State + " on " + host() + "; being withdrawn"
	case store.TransferWaitCleanup:
		if w.Error != "" {
			return "cleanup failed on " + host() + ": " + w.Error
		}
		switch {
		case !w.HostConfirmed && !w.RegistrationConfirmed:
			return "waiting for " + host() + " to confirm it is gone, and for GitHub to confirm its registration is"
		case !w.HostConfirmed:
			return "waiting for " + host() + " to confirm it is gone"
		default:
			return "waiting for GitHub to confirm its registration is gone"
		}
	case store.TransferWaitJob:
		return "running on " + host() + "; it is left to finish"
	case store.TransferWaitMachine:
		return w.State + "; the provider operation is left to finish"
	}
	return ""
}

// Preparing changes the scheduler's view, never the saved pool configuration.
// Existing jobs finish naturally; the ordinary idle-runner withdrawal handshake
// keeps the last job that raced with the button from being killed.
func (c *Controller) PrepareTransfer(ctx context.Context) (TransferProgress, error) {
	p, err := c.prepareTransfer(ctx)
	if err != nil {
		return p, err
	}
	c.publishTransfer(ctx)
	return p, nil
}

func (c *Controller) prepareTransfer(ctx context.Context) (TransferProgress, error) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	c.machines.mu.Lock()
	if c.Fenced().Fenced && !c.transferDraining.Load() {
		c.machines.mu.Unlock()
		return TransferProgress{}, errors.New("resolve the existing recovery fence before preparing a transfer")
	}
	err := c.st.SetSetting(ctx, store.SettingTransferDraining, "true", false)
	if err == nil {
		c.transferDraining.Store(true)
		// A reservation that has never issued a provider call is still only intent.
		// Cancelling it here avoids leaving preparation waiting on its own pause.
		var cancelled []*store.Machine
		cancelled, err = c.st.CancelUnstartedTransferMachines(ctx)
		if err == nil {
			for _, m := range cancelled {
				c.publishMachine(ctx, m)
			}
		}
	}
	c.machines.mu.Unlock()
	if err != nil {
		return TransferProgress{}, err
	}
	c.Nudge()
	c.NudgeMachines()
	if err = c.finishTransferDrain(ctx); err != nil {
		return TransferProgress{}, err
	}
	return c.TransferProgress(ctx)
}

func (c *Controller) CancelTransfer(ctx context.Context) error {
	if err := c.cancelTransfer(ctx); err != nil {
		return err
	}
	c.publishTransfer(ctx)
	return nil
}

func (c *Controller) cancelTransfer(ctx context.Context) error {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	c.machines.mu.Lock()
	defer c.machines.mu.Unlock()
	if c.Fenced().Fenced {
		return errors.New("this instance is already fenced for cutover; verify that no destination is running before lifting its recovery fence")
	}
	if err := c.st.DeleteSetting(ctx, store.SettingTransferDraining); err != nil {
		return err
	}
	c.transferDraining.Store(false)
	c.Nudge()
	c.NudgeMachines()
	return nil
}

// reconcileMu is held by each caller. Serialising with the machine pass keeps
// a plan from starting another provider operation between the last count and
// the fence. Operations already launched leave nonterminal rows until finished.
func (c *Controller) finishTransferDrain(ctx context.Context) error {
	if !c.transferDraining.Load() || c.Fenced().Fenced {
		return nil
	}
	c.machines.mu.Lock()
	defer c.machines.mu.Unlock()
	p, err := c.st.TransferProgress(ctx)
	if err != nil {
		return err
	}
	if !p.Quiescent() {
		return nil
	}
	if err = c.st.SetRecoveryFence(ctx, true, "instance drained for transfer; keep the source stopped during cutover"); err != nil {
		return err
	}
	return c.LoadFence(ctx)
}
