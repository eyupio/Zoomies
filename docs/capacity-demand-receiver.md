# Capacity-demand receiver

Zoomies can ask an external autoscaling system for host capacity without owning
or deleting infrastructure itself. Configure `capacity_demand.destination_url`
and a high-entropy `signing_secret`; `pools` optionally limits publication to
pool IDs or names.

The controller posts JSON for `capacity_demand` when queued work is blocked by
full eligible hosts, and `scale_down_opportunity` only after excess idle host
capacity remains continuously visible for the cooldown. Successful events are
deduplicated for the cooldown and delivery state is stored in SQLite, including
across restarts. A scale-down event is advisory: the receiver must apply its
own safety policy before removing a VM.

```json
{
  "event_id": "random-id",
  "type": "capacity_demand",
  "timestamp": "2026-09-05T12:00:00Z",
  "pool_id": "pool_123",
  "host_selector": {"zone": "eu-west-1"},
  "backend": "docker",
  "required_runner_slots": 3,
  "current_capacity": 8,
  "queued_job_count": 3,
  "oldest_queue_age_seconds": 74
}
```

`required_runner_slots` is positive for scale-up demand and negative for a
scale-down opportunity. The request also carries `X-Zoomies-Event-ID`,
`X-Zoomies-Timestamp`, and `X-Zoomies-Signature-256`. The last header is
`sha256=<lowercase hex HMAC-SHA256 of the exact request body>`.

## Minimal receiver mapped to an autoscaler

This Go handler verifies the body before converting the signal into a desired
capacity update. In production, put the event ID in a database with a unique
constraint before calling the provider, clamp changes to fleet limits, and
return a 2xx response only after that durable operation succeeds.

```go
func capacityDemand(secret []byte, autoscaler Autoscaler) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
        if err != nil { http.Error(w, "bad body", 400); return }
        mac := hmac.New(sha256.New, secret)
        mac.Write(body)
        want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
        if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Zoomies-Signature-256"))) {
            http.Error(w, "bad signature", 401); return
        }

        var event struct {
            EventID string `json:"event_id"`
            PoolID string `json:"pool_id"`
            RequiredRunnerSlots int `json:"required_runner_slots"`
        }
        if json.Unmarshal(body, &event) != nil || event.EventID == "" {
            http.Error(w, "bad event", 400); return
        }
        // Autoscaler.ApplyEvent is idempotent by event ID. It can map PoolID
        // to an AWS ASG, GCP MIG, Kubernetes node pool, or another system.
        if err := autoscaler.ApplyEvent(r.Context(), event.EventID, event.PoolID,
            event.RequiredRunnerSlots); err != nil {
            http.Error(w, "autoscaler unavailable", 503); return
        }
        w.WriteHeader(http.StatusNoContent)
    }
}
```

Reject timestamps outside a small clock-skew window to prevent replay, while
retaining event IDs for at least the sender cooldown. Scale-down handlers should
also verify that instances are still idle and respect the provider's own
minimum capacity; Zoomies deliberately never performs infrastructure deletion.
