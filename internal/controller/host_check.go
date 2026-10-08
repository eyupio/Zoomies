package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
)

const (
	// HostCheckCooldown is how soon after a request the same host may be asked
	// again. It runs from the request, lives in controller memory, and is not
	// cleared by a failure: clearing on failure would let a token in a loop hammer
	// an agent that fails fast. It is a judgement value, not a measurement.
	HostCheckCooldown = 15 * time.Second
	// hostCheckPatience is how long a request may stay "asked" before the view
	// says it failed. It sits above the agent's 45 s bound on one run, so a slow
	// but healthy host is not reported as silent.
	hostCheckPatience = 60 * time.Second
	// hostCheckKeep is how long a finished request stays on the host's view. The
	// page shows it as "Checked just now"; after this the host is idle again and
	// the view carries nothing.
	hostCheckKeep = 30 * time.Second
)

// Reasons a request is refused, as the stable codes HostCheckError carries.
const (
	HostCheckNotConnected = "not_connected"
	HostCheckIncompatible = "incompatible"
	HostCheckUnsupported  = "unsupported"
	HostCheckFenced       = "fenced"
	HostCheckBusy         = "busy"
)

// HostCheckError is a refusal the operator can act on. The API renders it as a
// conflict with Message as the sentence.
type HostCheckError struct{ Code, Message string }

func (e *HostCheckError) Error() string { return e.Message }

// HostCheckCooldownError says the host was asked a moment ago.
type HostCheckCooldownError struct {
	Name  string
	Until time.Time
	now   time.Time
}

func (e *HostCheckCooldownError) Error() string {
	n := max(int((e.Until.Sub(e.now)+time.Second-1)/time.Second), 1)
	return fmt.Sprintf("%s was asked to check itself a moment ago. Ask again in %d s.", e.Name, n)
}

// hostCheck is what the controller remembers about the last request to a host.
// It is memory only, on purpose: a request is a conversation with a live agent,
// and nothing about it survives a restart worth persisting.
type hostCheck struct {
	TaskID, State, Outcome, Message string // State: asked | done | failed
	AskedAt, DoneAt, NextAt         time.Time
}

const (
	hostCheckAsked  = "asked"
	hostCheckDone   = "done"
	hostCheckFailed = "failed"
)

type hostChecks struct {
	mu     sync.Mutex
	byHost map[string]*hostCheck
}

func (hc *hostChecks) get(hostID string) (hostCheck, bool) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if e := hc.byHost[hostID]; e != nil {
		return *e, true
	}
	return hostCheck{}, false
}

func (hc *hostChecks) set(hostID string, e hostCheck) {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if hc.byHost == nil {
		hc.byHost = map[string]*hostCheck{}
	}
	hc.byHost[hostID] = &e
}

func (hc *hostChecks) forget(hostID string) {
	hc.mu.Lock()
	delete(hc.byHost, hostID)
	hc.mu.Unlock()
}

// HostCheckView is the in-flight or just-finished request on a host's view.
type HostCheckView struct {
	State   string     `json:"state"`
	AskedAt time.Time  `json:"asked_at"`
	Outcome string     `json:"outcome,omitempty"`
	Message string     `json:"message,omitempty"`
	NextAt  *time.Time `json:"next_at,omitempty"`
}

// hostCheckView renders the request for the host view, or nil when there is
// nothing to say.
//
// The time-based changes -- asked turning to failed once patience runs out, a
// finished request dropping off -- are worked out here from the clock rather
// than by a timer, so GET and the event render the same thing and
// publishHostChanges re-sends a frame when the bytes move, at most one pass late.
func (c *Controller) hostCheckView(h *store.Host) *HostCheckView {
	e, ok := c.hostChecks.get(h.ID)
	if !ok {
		return nil
	}
	now := c.Now()
	switch e.State {
	case hostCheckAsked:
		if now.Sub(e.AskedAt) > hostCheckPatience {
			e.State = hostCheckFailed
			e.DoneAt = e.AskedAt.Add(hostCheckPatience)
			e.Message = fmt.Sprintf("%s did not answer in time. Check that its agent is running; the last report is still shown.", naming.ForSentence(h.Name))
		}
	}
	if e.State != hostCheckAsked && now.Sub(e.DoneAt) > hostCheckKeep {
		return nil
	}
	v := &HostCheckView{State: e.State, AskedAt: e.AskedAt, Outcome: e.Outcome, Message: e.Message}
	if now.Before(e.NextAt) {
		next := e.NextAt
		v.NextAt = &next
	}
	return v
}

// RequestHostCheck asks a host's agent to run its OS checks once. queued is true
// only when a task was actually put on the host's queue, which is what the
// caller audits: a repeat press and every refusal queue nothing.
//
// Nothing is dialled. The task goes out on the agent's own poll.
func (c *Controller) RequestHostCheck(ctx context.Context, hostID string) (h store.Host, queued bool, err error) {
	hp, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		return store.Host{}, false, err
	}
	h = *hp
	now := c.Now()
	switch {
	case !c.mayAct():
		return h, false, &HostCheckError{HostCheckFenced, "This controller is recovering and is not handing out work yet. Try again in a minute."}
	case !h.Healthy(now):
		return h, false, &HostCheckError{HostCheckNotConnected, fmt.Sprintf("%s is not connected, so it cannot be asked. Its agent last reported %s. Check that the agent is running.", naming.ForSentence(h.Name), heartbeatWhen(h.LastHeartbeat, now))}
	case h.Incompatible:
		return h, false, &HostCheckError{HostCheckIncompatible, incompatibleReason(&h)}
	case !h.Supports(agent.FeatureHostCheck):
		return h, false, &HostCheckError{HostCheckUnsupported, fmt.Sprintf("The agent on %s cannot check on request. Agents running in a container, and agents older than this release, send their report on their own schedule instead. The last report is still shown.", naming.ForSentence(h.Name))}
	}
	if e, ok := c.hostChecks.get(h.ID); ok {
		if e.State == hostCheckAsked && now.Sub(e.AskedAt) <= hostCheckPatience {
			return h, false, nil
		}
		if now.Before(e.NextAt) {
			return h, false, &HostCheckCooldownError{Name: naming.ForSentence(h.Name), Until: e.NextAt, now: now}
		}
	}
	task := agent.Task{Kind: agent.TaskCheckHost, ID: "task_" + store.NewSecret(8), IssuedAt: now}
	if !c.enqueue(h.ID, task) {
		return h, false, &HostCheckError{HostCheckBusy, fmt.Sprintf("The previous request to %s is still being cleaned up. Try again in a few seconds.", naming.ForSentence(h.Name))}
	}
	c.hostChecks.set(h.ID, hostCheck{TaskID: task.ID, State: hostCheckAsked, AskedAt: now, NextAt: now.Add(HostCheckCooldown)})
	c.publishHost(&h)
	return h, true, nil
}

// heartbeatWhen says how long ago a host last reported, for a sentence an
// operator reads.
func heartbeatWhen(last, now time.Time) string {
	if last.IsZero() {
		return "never"
	}
	d := now.Sub(last).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	default:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
}

// failHostCheck records a failure for the request taskID and announces it. A
// result for an older request must not overwrite a newer one, so an entry for a
// different task is left alone; an empty taskID matches whatever is there.
func (c *Controller) failHostCheck(ctx context.Context, hostID, taskID, message string) {
	e, ok := c.hostChecks.get(hostID)
	if ok && taskID != "" && e.TaskID != taskID {
		return
	}
	if !ok {
		e = hostCheck{TaskID: taskID, AskedAt: c.Now()}
	}
	e.State, e.Message, e.Outcome, e.DoneAt = hostCheckFailed, message, "", c.Now()
	c.hostChecks.set(hostID, e)
	if h, err := c.st.GetHost(ctx, hostID); err == nil {
		c.publishHost(h)
	}
}

// expireHostCheck is the sweep's word that a request was never picked up.
func (c *Controller) expireHostCheck(ctx context.Context, hostID, taskID string) {
	c.failHostCheck(ctx, hostID, taskID, fmt.Sprintf("%s did not pick the request up. Check that its agent is running; the last report is still shown.", c.hostName(ctx, hostID)))
}

// applyHostCheck takes the agent's answer to a check_host task.
//
// A result reaches the controller through ReportResult, which has no host row
// and is not a heartbeat, so this does its own fresh read, validates, ingests,
// and records freshness with a narrow write -- never HeartbeatWithReport, which
// would let a task result pose as a heartbeat.
func (c *Controller) applyHostCheck(ctx context.Context, hostID string, res agent.TaskResult, issuedAt time.Time) {
	h, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		// The host was deleted while the agent was working; its entry went with it.
		return
	}
	switch {
	case !res.OK && res.NotStarted:
		c.failHostCheck(ctx, hostID, res.TaskID, fmt.Sprintf("The agent on %s was restarting and gave the request back. Ask again in a moment.", naming.ForSentence(h.Name)))
		return
	case !res.OK && strings.Contains(res.Error, "unknown task kind"):
		c.failHostCheck(ctx, hostID, res.TaskID, fmt.Sprintf("The agent on %s is older than this controller and does not know how to check on request. Update it.", naming.ForSentence(h.Name)))
		return
	case !res.OK:
		c.failHostCheck(ctx, hostID, res.TaskID, fmt.Sprintf("%s could not run its checks: %s", naming.ForSentence(h.Name), res.Error))
		return
	case res.Doctor == nil:
		c.failHostCheck(ctx, hostID, res.TaskID, fmt.Sprintf("%s answered without a report.", naming.ForSentence(h.Name)))
		return
	}
	now := c.Now()
	in := res.Doctor
	// This also clears the acceptance fields, so an agent cannot smuggle an
	// acceptance in through the result path.
	if err := validateDoctor(in, now); err != nil {
		c.failHostCheck(ctx, hostID, res.TaskID, fmt.Sprintf("%s sent a report this controller refused: %v. Check the clock on that host.", naming.ForSentence(h.Name), err))
		return
	}
	outcome := "changed"
	switch {
	case h.Doctor.Report == nil:
		outcome = "first"
	case !in.CheckedAt.After(h.Doctor.CheckedAt):
		outcome = "not_newer"
	case h.Doctor.SameFindings(*in):
		outcome = "unchanged"
	}
	// Recorded before ingestDoctor, so the frame it publishes on a write already
	// carries the answer. A late result after the page gave up still lands: it
	// is fresh, read-only data.
	e, ok := c.hostChecks.get(hostID)
	switch {
	case ok && e.TaskID != res.TaskID:
		// A newer request owns the entry; this report is ingested all the same.
	default:
		if !ok {
			e = hostCheck{TaskID: res.TaskID, AskedAt: issuedAt}
			if e.AskedAt.IsZero() {
				e.AskedAt = now
			}
		}
		e.State, e.Outcome, e.Message, e.DoneAt = hostCheckDone, outcome, "", now
		c.hostChecks.set(hostID, e)
	}
	freshAt, err := c.ingestDoctor(ctx, h, in)
	if err != nil {
		c.log.Warn("could not store a host's on-request report", "host", hostID, "error", err)
		c.failHostCheck(ctx, hostID, res.TaskID, fmt.Sprintf("%s answered, but the controller could not store its report: %v", naming.ForSentence(h.Name), err))
		return
	}
	switch {
	case !freshAt.IsZero():
		if err := c.st.SetHostDoctorChecked(ctx, h.ID, freshAt); err != nil && !errors.Is(err, store.ErrNotFound) {
			c.log.Warn("could not record when a host was last checked", "host", hostID, "error", err)
		}
		c.publishHost(h)
	case outcome == "not_newer":
		c.publishHost(h)
	}
}
