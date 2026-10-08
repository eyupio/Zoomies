package controller

import (
	"context"
	"time"

	"github.com/eyupio/zoomies/internal/config"
)

// PrivateConnectionFault is why the private-connection listener cannot be
// reached by the hosts that enrolled through it, and since when.
//
// The listener belongs to the API server, which owns the socket-shaped half of
// the process; the controller only keeps the fact, because the problems list is
// built here and a listener that fails with nothing but a log line is the
// failure this exists to design out.
type PrivateConnectionFault struct {
	Since  time.Time
	Reason string
}

// SetPrivateConnectionFault records the private-connection listener's state:
// a fault while no relay answers, nil once one does. It is safe to call from
// any goroutine.
func (c *Controller) SetPrivateConnectionFault(f *PrivateConnectionFault) {
	if f == nil {
		c.privateFault.Store(nil)
		return
	}
	// Keep the first moment it went wrong: a retry that fails again is the
	// same outage, not a new one, and "since" should say how long hosts have
	// been cut off rather than when somebody last looked.
	if prev := c.privateFault.Load(); prev != nil {
		f = &PrivateConnectionFault{Since: prev.Since, Reason: f.Reason}
	}
	c.privateFault.Store(f)
}

// PrivateConnectionFault is the listener's current fault, or nil.
func (c *Controller) PrivateConnectionFault() *PrivateConnectionFault {
	return c.privateFault.Load()
}

// privateConnectionProblems says the private connection is down.
//
// It is a warning rather than an error because nothing about the direct half of
// the fleet depends on it, and a host that enrolled privately keeps its jobs
// running through an interruption; what stops is its heartbeats and its next
// task, which host.unhealthy reports on its own. This entry is the cause those
// symptoms otherwise leave an operator to guess.
func (c *Controller) privateConnectionProblems(_ context.Context, out *[]Problem) error {
	f := c.privateFault.Load()
	if f == nil {
		return nil
	}
	since := f.Since
	*out = append(*out, Problem{
		Code:     "tailcat.unavailable",
		Severity: config.SeverityWarning,
		Title:    "private hosts cannot reach this controller",
		Detail: "the private-connection listener has no Tailcat relay it can reach, so hosts enrolled with a " +
			"private connection cannot heartbeat or take work. " +
			"The last attempt said: " + f.Reason,
		Fix: "check that this controller has outbound HTTPS to Tailcat's relays, a firewall, proxy or DNS change is " +
			"the usual cause. The controller keeps retrying, trying the relay its identity was sealed with first, " +
			"and this clears on its own once one answers; no restart is needed.",
		Since: &since,
	})
	return nil
}
