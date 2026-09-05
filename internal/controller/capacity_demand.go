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
)

// CapacityDemandEvent is the stable wire contract consumed by provisioners.
// RequiredRunnerSlots is positive for demand and negative for removable idle
// capacity, so receivers can also treat it as a signed capacity delta.
type CapacityDemandEvent struct {
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
	if c.cfg.CapacityDemand.DestinationURL == "" {
		return
	}
	pools := make(map[string]*store.Pool, len(snap.Pools))
	for _, p := range snap.Pools {
		pools[p.ID] = p
	}
	for _, pp := range plan.Pools {
		p := pools[pp.PoolID]
		if p == nil || !c.capacityPoolAllowed(p) {
			continue
		}
		capacity := eligibleCapacity(p, snap.Hosts, snap.Now)
		oldest := oldestPoolQueue(p, snap.Pools, snap.Jobs, snap.Now)
		if pp.BlockedAtCapacity && pp.QueuedMatched > 0 {
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
	a := c.cfg.CapacityDemand.Pools
	return len(a) == 0 || slices.Contains(a, p.ID) || slices.Contains(a, p.Name)
}

func eligibleCapacity(p *store.Pool, hosts []*store.Host, now time.Time) int {
	n := 0
	for _, h := range hosts {
		if h.Healthy(now) && !h.Cordoned && slices.Contains(h.Backends, string(p.Backend)) && selectorMatches(p.HostSelector, h.Labels) {
			n += h.Capacity
		}
	}
	return n
}
func selectorMatches(want, got store.StringMap) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
func oldestPoolQueue(p *store.Pool, pools []*store.Pool, jobs []*store.Job, now time.Time) int64 {
	var oldest time.Duration
	for _, j := range jobs {
		if scheduler.BestPool(pools, j.Labels) == p {
			if a := now.Sub(j.QueuedAt); a > oldest {
				oldest = a
			}
		}
	}
	if oldest < 0 {
		return 0
	}
	return int64(oldest / time.Second)
}

func (c *Controller) deliverCapacityEvent(ctx context.Context, p *store.Pool, capacity, queued int, oldest int64, slots int, eventType string, sustained bool) {
	now := c.Now()
	previous, err := c.st.GetCapacityDemandDelivery(ctx, p.ID, eventType)
	if err == nil {
		// Attempt time, rather than only success time, is the durable circuit
		// breaker which prevents an unavailable receiver (and controller
		// restarts) from turning every reconciliation tick into a request storm.
		if previous.AttemptedAt != nil && now.Sub(*previous.AttemptedAt) < c.cfg.CapacityDemand.Cooldown {
			return
		}
		if sustained && previous.AttemptedAt == nil && now.Sub(previous.ObservedSince) < c.cfg.CapacityDemand.Cooldown {
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

	e := CapacityDemandEvent{EventID: store.NewSecret(12), Type: eventType, Timestamp: now, PoolID: p.ID, HostSelector: p.HostSelector, Backend: p.Backend, RequiredRunnerSlots: slots, CurrentCapacity: capacity, QueuedJobCount: queued, OldestQueueAgeSeconds: oldest}
	body, _ := json.Marshal(e)
	d := &store.CapacityDemandDelivery{PoolID: p.ID, EventType: eventType, EventID: e.EventID, Payload: string(body), ObservedSince: now}
	status, attempts, sendErr := c.postCapacityEvent(ctx, body, e)
	d.AttemptedAt = &now
	d.StatusCode = status
	d.Attempts = attempts
	if sendErr != nil {
		d.LastError = sendErr.Error()
	} else {
		d.DeliveredAt = &now
	}
	if err := c.st.PutCapacityDemandDelivery(ctx, d); err != nil {
		c.log.Error("could not persist capacity-demand delivery", "error", err)
	}
	action := "capacity_demand.delivered"
	if sendErr != nil {
		action = "capacity_demand.failed"
	}
	c.authsvc.Auditor().Act(ctx, auth.SystemIdentity(), action, "pool", p.ID, map[string]any{"event_id": e.EventID, "type": eventType, "status_code": status, "attempts": attempts, "error": d.LastError})
}

func (c *Controller) postCapacityEvent(ctx context.Context, body []byte, e CapacityDemandEvent) (int, int, error) {
	mac := hmac.New(sha256.New, []byte(c.cfg.CapacityDemand.SigningSecret))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	var last error
	status := 0
	for attempt := 1; attempt <= 3; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, c.cfg.CapacityDemand.Timeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.cfg.CapacityDemand.DestinationURL, bytes.NewReader(body))
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
