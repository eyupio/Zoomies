---
icon: material/gauge
description: >-
  The advisory contract that tells another system a Zoomies pool is short of
  capacity, and what a receiver must do to be safe.
---

# Capacity-demand receiver

Zoomies can ask an external autoscaling system for host capacity without owning
or deleting infrastructure itself. Configure `capacity_demand.destination_url`
and a high-entropy `signing_secret`; `pools` optionally limits publication to
pool IDs or names.

The controller posts JSON for `capacity_demand` when queued work has nowhere to
run — either because every eligible host is full, or because no host in the
fleet could run that pool at all — and `scale_down_opportunity` only after
excess idle host capacity remains continuously visible for the cooldown.
Delivery state is stored in SQLite, so it survives a restart. A scale-down event
is advisory: the receiver must apply its own safety policy before removing a VM.

The second case is the one worth knowing about, because it is the one a receiver
is usually written for and the one Zoomies used to keep to itself. A pool with
queued jobs and an empty fleet — or a fleet whose every host runs the wrong
backend, the wrong platform, or does not match the pool's host selector — is
asking to be scaled from zero, and no job finishing anywhere will clear it. The
event says what is missing; whether your infrastructure can supply it is still
yours to decide, and a pool blocked on something a new machine would not fix
(an architecture you do not have) is a signal to refuse, not to buy.

Three behaviours matter to whoever writes the receiver, and none of them is
what you would guess:

* **The cooldown keys on the attempt, not the outcome.** Once Zoomies has tried
  to deliver an event for a pool and event type, it will not try again until the
  cooldown has passed — whether the first attempt was accepted, refused or
  never answered. This is deliberate: it is the circuit breaker that stops an
  unavailable receiver turning every reconcile pass into a request storm. It
  also means a receiver that returns 500 does not get an immediate retry from
  the next pass.
* **Each delivery retries up to three times on its own**, with backoff, inside
  that one attempt, and every try carries the same `event_id`. Deduplicate on
  it: the second arrival of one event is not a new demand. When all three
  fail, the failure is recorded, raised as the
  `capacity_demand.delivery_failed` problem, and left until the cooldown
  expires.
* **The event is a reading, not an increment.** After the cooldown, a shortfall
  that is still unmet — because the hosts you asked for have not joined yet —
  is delivered again under a **new** `event_id`. A receiver that adds
  `required_runner_slots` to whatever it already asked for, once per event ID,
  therefore adds the same shortfall again every cooldown until the first hosts
  arrive. Read the pair instead: `current_capacity` is the eligible runner
  capacity Zoomies counted when it looked, `required_runner_slots` is what was
  missing, and `current_capacity + required_runner_slots` is the capacity the
  pool should have. Set your target to that. It is safe to apply once per event
  ID and safe to apply again when the next event names the same shortfall,
  because the same reading sets the same target. Both numbers are runner slots,
  not machines; how many slots a host of yours provides is for the receiver to
  know.

`schema_version` is the first thing to check. It is `1`; adding a field never
raises it, and a change that a receiver written for `1` would misread does.
Refuse a version you do not know rather than guess at the fields.

```json
{
  "schema_version": 1,
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
scale-down opportunity, and the target reading holds for both: eight slots
minus two removable ones is a target of six. The request also carries
`X-Zoomies-Event-ID`, `X-Zoomies-Timestamp`, and `X-Zoomies-Signature-256`.
The last header is
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
            SchemaVersion int `json:"schema_version"`
            EventID string `json:"event_id"`
            PoolID string `json:"pool_id"`
            RequiredRunnerSlots int `json:"required_runner_slots"`
            CurrentCapacity int `json:"current_capacity"`
        }
        if json.Unmarshal(body, &event) != nil || event.EventID == "" {
            http.Error(w, "bad event", 400); return
        }
        if event.SchemaVersion != 1 {
            http.Error(w, "unknown schema version", 400); return
        }
        // Autoscaler.SetTarget is idempotent by event ID and, because the
        // event is a reading, harmless to repeat under the next one: the pool
        // should have current_capacity + required_runner_slots slots. It can
        // map PoolID to an AWS ASG, GCP MIG, Kubernetes node pool, or another
        // system, and the slots to however many machines that takes.
        if err := autoscaler.SetTarget(r.Context(), event.EventID, event.PoolID,
            event.CurrentCapacity+event.RequiredRunnerSlots); err != nil {
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
