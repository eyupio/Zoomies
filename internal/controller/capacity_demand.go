package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

const (
	capacityDemandEvent = "capacity_demand"
	scaleDownEvent      = "scale_down_opportunity"

	// capacityDemandSchemaVersion is what a receiver checks before it reads
	// the rest. Adding a field never raises it; a change a receiver written
	// for the current shape would misread does.
	capacityDemandSchemaVersion = 1
)

// CapacityDemandEvent is the stable wire contract consumed by provisioners.
// RequiredRunnerSlots is positive for demand and negative for removable idle
// capacity. It is a reading, not an increment: the pool should have
// CurrentCapacity+RequiredRunnerSlots slots, and a receiver that adds
// RequiredRunnerSlots to whatever it already asked for adds the same shortfall
// again on every re-delivery, because each attempt after the cooldown carries
// a fresh EventID for what may be the same unmet demand.
type CapacityDemandEvent struct {
	SchemaVersion         int               `json:"schema_version"`
	EventID               string            `json:"event_id"`
	Type                  string            `json:"type"`
	Timestamp             time.Time         `json:"timestamp"`
	PoolID                string            `json:"pool_id"`
	HostSelector          store.StringMap   `json:"host_selector"`
	Backend               store.BackendKind `json:"backend"`
	RequiredRunnerSlots   int               `json:"required_runner_slots"`
	CurrentCapacity       int               `json:"current_capacity"`
	QueuedJobCount        int               `json:"queued_job_count"`
	OldestQueueAgeSeconds int64             `json:"oldest_queue_age_seconds"`
}

func (c *Controller) publishCapacitySignals(ctx context.Context, snap scheduler.Snapshot, plan scheduler.Plan) {
	if c.cfg().CapacityDemand.DestinationURL == "" {
		return
	}
	pools := make(map[string]*store.Pool, len(snap.Pools))
	for _, p := range snap.Pools {
		pools[p.ID] = p
	}
	// Computed once for every pool in this pass rather than once per pool in
	// the loop below: oldestPoolQueue used to re-walk every job against every
	// pool for each pool it was asked about, which made this whole function
	// quadratic in the pool count on top of its jobs. oldestQueueAges walks
	// the jobs once, calling scheduler.BestPool exactly once per job.
	oldestQueue := oldestQueueAges(snap.Pools, snap.Jobs, snap.Now)
	for _, pp := range plan.Pools {
		p := pools[pp.PoolID]
		if p == nil || !c.capacityPoolAllowed(p) {
			continue
		}
		capacity := eligibleCapacity(p, snap.Hosts, snap.Now)
		oldest := queueAgeSeconds(oldestQueue[p.ID])
		// Both blockages are demand, and the second one is the one a receiver
		// exists for: a pool whose jobs have nowhere to go at all is asking to
		// be scaled from zero, and until this said so the only shortfall
		// Zoomies ever announced was one a finished job would have cleared by
		// itself. A pool blocked for a reason a machine cannot fix -- the wrong
		// platform, a selector nothing matches -- still reaches the receiver,
		// which is correct: the event says what is missing, and deciding
		// whether it can supply it has always been the receiver's half.
		if (pp.BlockedAtCapacity || pp.BlockedNoEligibleHost) && pp.QueuedMatched > 0 {
			slots := max(pp.Desired-pp.Current, 1)
			c.deliverCapacityEvent(ctx, p, capacity, pp.QueuedMatched, oldest, slots, capacityDemandEvent, false)
		} else {
			_ = c.st.DeleteCapacityDemandObservation(ctx, p.ID, capacityDemandEvent)
		}

		// A host provisioner can remove only capacity which is both unused and
		// above this pool's desired runner count. The observation must remain
		// continuously true for one cooldown before it is announced.
		excess := capacity - pp.Desired
		if pp.QueuedMatched == 0 && excess > 0 {
			c.deliverCapacityEvent(ctx, p, capacity, 0, 0, -excess, scaleDownEvent, true)
		} else {
			_ = c.st.DeleteCapacityDemandObservation(ctx, p.ID, scaleDownEvent)
		}
	}
}

func (c *Controller) capacityPoolAllowed(p *store.Pool) bool {
	a := c.cfg().CapacityDemand.Pools
	return len(a) == 0 || slices.Contains(a, p.ID) || slices.Contains(a, p.Name)
}

// eligibleCapacity is the slots the pool can be placed into right now, which
// is a throttled host's effective capacity rather than its configured one: a
// provisioner told the fleet has eight slots while the throttle leaves it four
// would never be asked for the capacity the queue is actually short of.
func eligibleCapacity(p *store.Pool, hosts []*store.Host, now time.Time) int {
	n := 0
	for _, h := range hosts {
		if scheduler.HostCanRun(h, p, now) {
			n += h.EffectiveCapacity()
		}
	}
	return n
}

// oldestQueueAges maps every pool to the age of its oldest queued job, by
// asking scheduler.BestPool which pool claims each job exactly once. A pool
// with no queued job of its own is simply absent, which queueAgeSeconds reads
// the same way oldestPoolQueue's zero-valued accumulator used to.
func oldestQueueAges(pools []*store.Pool, jobs []*store.Job, now time.Time) map[string]time.Duration {
	oldest := make(map[string]time.Duration, len(pools))
	for _, j := range jobs {
		p := scheduler.BestPool(pools, j)
		if p == nil {
			continue
		}
		if a := now.Sub(j.QueuedAt); a > oldest[p.ID] {
			oldest[p.ID] = a
		}
	}
	return oldest
}

// queueAgeSeconds turns a queue age into the whole seconds the wire event
// carries, treating a negative reading (a job queued in the future, as a
// clock skew or a test fixture might produce) the same as no wait at all.
func queueAgeSeconds(d time.Duration) int64 {
	if d < 0 {
		return 0
	}
	return int64(d / time.Second)
}

func (c *Controller) deliverCapacityEvent(ctx context.Context, p *store.Pool, capacity, queued int, oldest int64, slots int, eventType string, sustained bool) {
	now := c.Now()
	previous, err := c.st.GetCapacityDemandDelivery(ctx, p.ID, eventType)
	if err == nil {
		// Attempt time, rather than only success time, is the durable circuit
		// breaker which prevents an unavailable receiver (and controller
		// restarts) from turning every reconciliation tick into a request storm.
		if previous.AttemptedAt != nil && now.Sub(*previous.AttemptedAt) < c.cfg().CapacityDemand.Cooldown {
			return
		}
		if sustained && previous.AttemptedAt == nil && now.Sub(previous.ObservedSince) < c.cfg().CapacityDemand.Cooldown {
			return
		}
	} else if err == store.ErrNotFound {
		if sustained {
			_ = c.st.PutCapacityDemandDelivery(ctx, &store.CapacityDemandDelivery{PoolID: p.ID, EventType: eventType, EventID: store.NewSecret(12), Payload: "", ObservedSince: now})
			return
		}
	} else {
		c.log.Error("could not read capacity-demand delivery state", "error", err)
		return
	}

	e := CapacityDemandEvent{SchemaVersion: capacityDemandSchemaVersion, EventID: store.NewSecret(12), Type: eventType, Timestamp: now, PoolID: p.ID, HostSelector: p.HostSelector, Backend: p.Backend, RequiredRunnerSlots: slots, CurrentCapacity: capacity, QueuedJobCount: queued, OldestQueueAgeSeconds: oldest}
	body, _ := json.Marshal(e)
	d := &store.CapacityDemandDelivery{PoolID: p.ID, EventType: eventType, EventID: e.EventID, Payload: string(body), ObservedSince: now, AttemptedAt: &now}
	// The attempt is recorded before the request is made, not after: it is
	// the circuit breaker the cooldown check above reads, and the next pass
	// may well arrive before the receiver has answered.
	if err := c.st.PutCapacityDemandDelivery(ctx, d); err != nil {
		c.log.Error("could not persist capacity-demand delivery", "error", err)
		return
	}

	// The request itself leaves the reconcile pass. This function runs under
	// the reconcile lock, and a receiver that accepts the connection and never
	// answers held every pass for three attempts of the timeout, per pool, per
	// event type -- tens of seconds in which nothing scaled. The delivery gets
	// a context of its own, bounded by the attempts it may make, so a pass
	// ending or the controller stopping does not cut a request off half-way.
	timeout := c.cfg().CapacityDemand.Timeout
	c.deliveries.Add(1)
	go func() {
		defer c.deliveries.Done()
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*timeout+time.Second)
		defer cancel()
		c.finishCapacityDelivery(dctx, p, e, body, d)
	}()
}

// finishCapacityDelivery makes the request and records how it went.
func (c *Controller) finishCapacityDelivery(ctx context.Context, p *store.Pool, e CapacityDemandEvent, body []byte, d *store.CapacityDemandDelivery) {
	status, attempts, sendErr := c.postCapacityEvent(ctx, body, e)
	d.StatusCode = status
	d.Attempts = attempts
	if sendErr != nil {
		d.LastError = sendErr.Error()
	} else {
		delivered := c.Now()
		d.DeliveredAt = &delivered
	}
	if err := c.st.PutCapacityDemandDelivery(ctx, d); err != nil {
		c.log.Error("could not persist capacity-demand delivery", "error", err)
	}
	action := "capacity_demand.delivered"
	if sendErr != nil {
		action = "capacity_demand.failed"
	}
	c.authsvc.Auditor().Act(ctx, auth.SystemIdentity(), action, "pool", p.ID, map[string]any{"event_id": e.EventID, "type": d.EventType, "status_code": status, "attempts": attempts, "error": d.LastError})
}

func (c *Controller) postCapacityEvent(ctx context.Context, body []byte, e CapacityDemandEvent) (int, int, error) {
	mac := hmac.New(sha256.New, []byte(c.cfg().CapacityDemand.SigningSecret))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	var last error
	status := 0
	for attempt := 1; attempt <= 3; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, c.cfg().CapacityDemand.Timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.cfg().CapacityDemand.DestinationURL, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Zoomies-Event-ID", e.EventID)
			req.Header.Set("X-Zoomies-Timestamp", e.Timestamp.Format(time.RFC3339Nano))
			req.Header.Set("X-Zoomies-Signature-256", signature)
			var resp *http.Response
			resp, err = c.httpClient.Do(req)
			if resp != nil {
				status = resp.StatusCode
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if status >= 200 && status < 300 {
					cancel()
					return status, attempt, nil
				}
				err = fmt.Errorf("receiver returned HTTP %d", status)
			}
		}
		cancel()
		last = err
		if ctx.Err() != nil {
			return status, attempt, ctx.Err()
		}
		if attempt < 3 {
			timer := time.NewTimer(time.Duration(1<<(attempt-1)) * 100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return status, attempt, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return status, 3, last
}
