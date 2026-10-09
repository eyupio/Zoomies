package controller

import (
	"context"
	"errors"

	"github.com/eyupio/zoomies/internal/store"
)

type TransferProgress = store.TransferProgress

func (c *Controller) TransferProgress(ctx context.Context) (TransferProgress, error) {
	return c.st.TransferProgress(ctx)
}

// Preparing changes the scheduler's view, never the saved pool configuration.
// Existing jobs finish naturally; the ordinary idle-runner withdrawal handshake
// keeps the last job that raced with the button from being killed.
func (c *Controller) PrepareTransfer(ctx context.Context) (TransferProgress, error) {
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
