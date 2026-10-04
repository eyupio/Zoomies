package scheduler

import (
	"math"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// HostAdmissionReason is empty when measured pressure permits new runners.
// Connectivity, configured capacity and manual cordons remain separate facts.
func HostAdmissionReason(h *store.Host, now time.Time) string {
	if !h.Usage.Fresh(now) {
		return ""
	}
	if h.Usage.CPUHeld {
		return "new starts held after sustained CPU pressure; they resume when CPU usage falls below 85%"
	}
	if v := h.Usage.MemoryAvailableMB; v != nil && *v <= h.MemoryReserve() {
		return "new starts held because available memory is at or below the host reserve"
	}
	return ""
}

func hostUnderCPUPressure(h *store.Host, now time.Time) bool {
	// Lent CPU in use is load the elastic plan chose, and the next plan
	// takes it back for a start, so it is not pressure.
	v := h.Usage.UnlentCPUPercent()
	return h.Usage.Fresh(now) && v != nil && *v >= 85
}

func (hs *hostSet) pending(h *store.Host, pools []*store.Pool, runners map[string][]*store.Runner) Reservation {
	var out Reservation
	for _, p := range pools {
		if p == nil {
			continue
		}
		for _, r := range runners[p.ID] {
			if r == nil || r.HostID != h.ID || !r.State.Live() {
				continue
			}
			starting := r.State == store.RunnerProvisioning || r.State == store.RunnerRegistering
			if starting {
				hs.warming[h.ID]++
			}
			if starting || r.CreatedAt.After(h.Usage.SampledAt) ||
				(r.ContainerStartedAt != nil && r.ContainerStartedAt.After(h.Usage.SampledAt)) {
				res := RunnerCharge(p, h, r)
				out.CPUs += res.CPUs
				out.MemoryMB += res.MemoryMB
			}
		}
	}
	return out
}

func (hs *hostSet) score(h *store.Host, p *store.Pool) float64 {
	if h.Slots() <= 0 {
		return -1
	}
	want, left, alloc := Reserve(p, h), hs.left[h.ID], hs.alloc[h.ID]
	score, dimensions := 0.0, 0.0
	if alloc.CPUsKnown && alloc.CPUs > 0 {
		cpu := left.CPUs
		if measured, ok := hs.observedCPU[h.ID]; ok {
			cpu = min(cpu, measured)
		}
		score += max(cpu-want.CPUs, 0) / alloc.CPUs
		dimensions++
	}
	if alloc.MemoryKnown && alloc.MemoryMB > 0 {
		score += float64(max(left.MemoryMB-want.MemoryMB, 0)) / float64(alloc.MemoryMB)
		dimensions++
	}
	if dimensions > 0 {
		return score / dimensions
	}
	// Preserve the old greatest-free-slots choice when there are no resource
	// specifications. Missing telemetry must not make an existing fleet stop.
	return float64(hs.free[h.ID]-1) / float64(h.Slots())
}

func (hs *hostSet) prefer(h, best *store.Host, p *store.Pool) bool {
	if hs.preferReadiness {
		a, b := hs.readyDelay(h, p), hs.readyDelay(best, p)
		// A small timing difference is noise; headroom breaks near ties.
		if a+5*time.Second < b {
			return true
		}
		if b+5*time.Second < a {
			return false
		}
	}
	a, b := hs.alloc[h.ID], hs.alloc[best.ID]
	measured := (a.CPUsKnown || a.MemoryKnown) && (b.CPUsKnown || b.MemoryKnown)
	switch hs.order {
	case HostOrderLargestStandard:
		if c := compareStandard(Reserve(p, h), Reserve(p, best)); c != 0 {
			return c > 0
		}
	case HostOrderBestFit:
		return hs.tighter(h, best, p, measured)
	}
	if !a.CPUsKnown && !a.MemoryKnown && !b.CPUsKnown && !b.MemoryKnown {
		return hs.free[h.ID] > hs.free[best.ID]
	}
	score, bestScore := hs.score(h, p), hs.score(best, p)
	return score > bestScore+cpuEpsilon ||
		(math.Abs(score-bestScore) <= cpuEpsilon && hs.free[h.ID] > hs.free[best.ID])
}

// A serial admission queue costs one estimated service interval per start.
// Unknown hosts use a bounded prior and remain eligible; metrics never relax fit.
func (hs *hostSet) readyDelay(h *store.Host, p *store.Pool) time.Duration {
	d := hs.readiness[p.ID][h.ID]
	if d <= 0 {
		d = time.Minute
	}
	d = min(max(d, 5*time.Second), 10*time.Minute)
	return time.Duration(hs.warming[h.ID]+1) * d
}
