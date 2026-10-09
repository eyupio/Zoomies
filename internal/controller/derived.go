package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// statsEventWindow is the window a `stats` frame summarises. It is the
// window GET /stats defaults to and the one the Overview asks for, so a frame
// arriving over the stream carries the same numbers a fetch would -- a
// different window here would make the tiles jump on every event.
const statsEventWindow = time.Hour

// publishDerived sends what is computed rather than stored: the Overview's
// statistics, the problems list, and any host whose card has moved.
//
// Nothing writes a row when a job ages out of the wait percentiles or a host
// stops sending heartbeats, so nothing in the store can announce those. They
// are worked out here after every reconcile pass and every housekeeping tick,
// and published only when the JSON actually differs from what was last sent.
// Without this the Overview's four numbers and the problems bell were the
// only parts of the product that stayed as they were until the browser was
// reloaded.
//
// It costs a handful of SQLite queries per pass, so it is skipped when nobody
// is connected to the stream: a controller nobody is watching should not do
// the watchers' work.
func (c *Controller) publishDerived(ctx context.Context) {
	if c.bus == nil || c.bus.Subscribers() == 0 || ctx.Err() != nil {
		return
	}

	c.derivedMu.Lock()
	defer c.derivedMu.Unlock()

	if stats, err := c.Stats(ctx, statsEventWindow); err != nil {
		c.log.Warn("could not compute fleet statistics for the event stream", "error", err)
	} else if raw, changed := c.derivedChanged(&c.lastStats, stats); changed {
		c.bus.Publish(events.KindStats, "", json.RawMessage(raw))
	}

	if problems, err := c.SharedProblems(ctx); err != nil {
		c.log.Warn("could not gather the current problems for the event stream", "error", err)
	} else if raw, changed := c.derivedChanged(&c.lastProblems, NewProblemsView(problems)); changed {
		c.bus.Publish(events.KindProblems, "", json.RawMessage(raw))
	}

	// The Overview of Kennel Club is computed, not stored: nothing writes a row
	// when an evaluation grows older, so nothing could announce that it did. With
	// the feature off this costs no query -- the document is the same "off" one
	// each time -- and is sent once when it flips.
	if overview, err := c.KennelOverview(ctx); err != nil {
		c.log.Warn("could not compute the Kennel Club overview for the event stream", "error", err)
	} else if raw, changed := c.derivedChanged(&c.lastKennel, overview); changed {
		c.bus.Publish(events.KindKennelSummary, "", json.RawMessage(raw))
	}

	// What an update would take is computed too: a soak ends when the clock passes
	// it and nothing is written, so no row could announce that auto may now act.
	// The sentence moves as time does, by the hour and then by the minute near the
	// end of a wait, and each move is sent once.
	//
	// A request and a closed attempt send their own frame through the same
	// memory, so this one says nothing a button has just said.
	if status, err := c.UpdatesView(ctx); err != nil {
		c.log.Warn("could not work out the update status for the event stream", "error", err)
	} else {
		c.sendUpdates(status)
	}

	c.publishHostChanges(ctx)
	c.publishMachineChanges(ctx)
}

// publishMachineChanges announces every machine whose rendered view has moved
// since it was last sent. Called with derivedMu held.
//
// It exists for the same reason publishHostChanges does, and for one more. A
// machine spends most of its life waiting -- for a clone, for a guest to boot,
// for an agent to join -- and what moves on the page while it waits is elapsed
// time and the counts around it, neither of which writes a row. Without this
// the one screen an operator watches while a machine is being built would be
// the one screen that did not move.
//
// Machines are in the tens, like hosts, so diffing the rendered view is cheap;
// this is deliberately not how the runners are done.
func (c *Controller) publishMachineChanges(ctx context.Context) {
	if !c.cfg().Provider.Enabled {
		return
	}
	view, err := c.MachineRenderer(ctx)
	if err != nil {
		c.log.Warn("could not read the providers for the event stream", "error", err)
		return
	}
	machines, _, err := c.st.ListMachines(ctx, store.MachineFilter{}, store.Page{Limit: 500})
	if err != nil {
		c.log.Warn("could not read the machines for the event stream", "error", err)
		return
	}
	if c.lastMachines == nil {
		c.lastMachines = make(map[string][]byte, len(machines))
	}
	live := make(map[string]struct{}, len(machines))
	for _, m := range machines {
		live[m.ID] = struct{}{}
		last := c.lastMachines[m.ID]
		raw, changed := c.derivedChanged(&last, view.View(m))
		if !changed {
			continue
		}
		c.lastMachines[m.ID] = last
		c.bus.Publish(events.KindMachineUpdated, "machine:"+m.ID, json.RawMessage(raw))
	}
	for id := range c.lastMachines {
		if _, ok := live[id]; !ok {
			delete(c.lastMachines, id)
		}
	}
}

// publishHostChanges announces every host whose rendered view has moved since
// it was last sent. Called with derivedMu held.
//
// The two numbers an operator actually reads off the Hosts page are the two
// nothing announces. `active_runners` is counted from the runners table when a
// host is read, so a runner starting moves the "4 of 6 slots in use" bar with
// no host row written and nothing to publish; and the heartbeat behind "last
// heartbeat 12s ago" is a bare UPDATE of one column that no publisher watches.
// Both sat still until the browser reconciled, which is why the page felt dead
// while the fleet moved underneath it -- the operator was looking at the one
// screen in the product that was not live.
//
// Diffing the rendered view is what stops that becoming a frame per host per
// pass: a host nobody has touched marshals to the same bytes and says nothing,
// and a host that is merely alive says something once per heartbeat rather
// than once per pass. The cost is bounded by how many hosts a fleet has, which
// is tens -- this is deliberately not how the runners are done, because there
// are thousands of those and the same idea would be too expensive.
func (c *Controller) publishHostChanges(ctx context.Context) {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		c.log.Warn("could not read the hosts for the event stream", "error", err)
		return
	}
	if c.lastHosts == nil {
		c.lastHosts = make(map[string][]byte, len(hosts))
	}
	live := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		live[h.ID] = struct{}{}
		last := c.lastHosts[h.ID]
		raw, changed := c.derivedChanged(&last, c.HostView(h))
		if !changed {
			continue
		}
		c.lastHosts[h.ID] = last
		c.bus.Publish(events.KindHostUpdated, "host:"+h.ID, json.RawMessage(raw))
	}
	// A host that has gone announced itself on the way out. Forgetting it here
	// only keeps the map from growing for the life of the process.
	for id := range c.lastHosts {
		if _, ok := live[id]; !ok {
			delete(c.lastHosts, id)
		}
	}
}

// derivedChanged marshals v, records it as the last published form, and says
// whether it differs from the one before. Comparing the bytes rather than the
// values means a new field is compared the day it is added.
func (c *Controller) derivedChanged(last *[]byte, v any) ([]byte, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		c.log.Error("could not marshal a payload for the event stream", "error", err)
		return nil, false
	}
	if bytes.Equal(raw, *last) {
		return raw, false
	}
	*last = raw
	return raw, true
}
