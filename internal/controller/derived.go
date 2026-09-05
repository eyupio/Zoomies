package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/eyupio/zoomies/internal/events"
)

// statsEventWindow is the window a `stats` frame summarises. It is the
// window GET /stats defaults to and the one the Overview asks for, so a frame
// arriving over the stream carries the same numbers a fetch would -- a
// different window here would make the tiles jump on every event.
const statsEventWindow = time.Hour

// publishDerived sends the two payloads that are computed rather than stored:
// the Overview's statistics and the problems list.
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

	if problems, err := c.Problems(ctx); err != nil {
		c.log.Warn("could not gather the current problems for the event stream", "error", err)
	} else if raw, changed := c.derivedChanged(&c.lastProblems, NewProblemsView(problems)); changed {
		c.bus.Publish(events.KindProblems, "", json.RawMessage(raw))
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
