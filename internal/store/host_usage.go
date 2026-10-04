package store

import (
	"context"
	"database/sql/driver"
	"fmt"
	"math"
	"time"
)

// HostUsage is a recent whole-host measurement, separate from reservations.
// Pointers distinguish an idle/full host from a host that could not measure.
// The controller owns the timestamps and the sustained-CPU hold.
type HostUsage struct {
	CPUPercent        *float64 `json:"cpu_percent,omitempty"`
	MemoryAvailableMB *int64   `json:"memory_available_mb,omitempty"`
	// LoadAverage1 is the kernel's one-minute load average for the whole
	// machine. CPU occupancy stops at 100%; this keeps counting the tasks
	// queued behind the cores, so it is what says how far past its size a
	// host has been pushed rather than merely that it is busy.
	LoadAverage1 *float64   `json:"load_average_1m,omitempty"`
	SampledAt    time.Time  `json:"sampled_at,omitempty"`
	CPUHighSince *time.Time `json:"cpu_high_since,omitempty"`
	CPUHeld      bool       `json:"cpu_held,omitempty"`
	// LentCPUPercent is the part of CPUPercent that runners were using out of
	// CPU elastic CPU lent them, as a share of the whole host. The controller
	// works it out from the same heartbeat's runner samples; whatever an agent
	// put here is replaced. A boost doing its job is load the plan chose, and
	// judging admission and the throttle on it would have a boost trip the
	// very hold that then withdraws it.
	LentCPUPercent float64 `json:"lent_cpu_percent,omitempty"`
	// IOWaitPercent is the share of CPU time the machine spent idle with
	// something waiting on disk, as the agent measured it between heartbeats.
	// CPUPercent counts that time as busy, so a build stalled on a saturated disk
	// reads as a host hard at work; this is the figure that says otherwise.
	IOWaitPercent *float64 `json:"io_wait_percent,omitempty"`
	// IOWaitHighSince is when I/O wait rose to IOWaitHighPercent and has not
	// fallen below IOWaitCalmPercent since. The controller owns it, for the reason
	// it owns CPUHighSince: an agent that sent its own could keep a host looking
	// disk-bound, or never.
	IOWaitHighSince *time.Time `json:"io_wait_high_since,omitempty"`
}

// UnlentCPUPercent is the host's CPU less what runners were using of CPU lent
// to them: the load the plan did not account for. Nil where CPU was not
// measured.
func (u HostUsage) UnlentCPUPercent() *float64 {
	if u.CPUPercent == nil {
		return nil
	}
	v := max(*u.CPUPercent-max(u.LentCPUPercent, 0), 0)
	return &v
}

const HostUsageMaxAge = 90 * time.Second

// CPUHoldWindow is how long CPU has to sit at or above 95% before new starts
// are held. It is a constant rather than a literal because the throttle
// ladder is paced against it: a rung may not be climbed faster than the hold
// that feeds it can trip, and internal/controller pins the two together.
const CPUHoldWindow = 30 * time.Second

// I/O wait is judged the way CPU is: one threshold to start the clock and a
// lower one to stop it, so a host hovering around the line is one sustained
// condition rather than a dozen short ones. The advice that rests on it -- that
// a pool's scratch folders could live in memory -- is persistent, and a clock
// that reset on every dip would never reach IOWaitSustained on a busy disk,
// which dips constantly.
const (
	IOWaitHighPercent = 20.0
	IOWaitCalmPercent = 10.0
	// IOWaitSustained is how long a host has to have been waiting on disk before
	// anything is said about it. A build's checkout is a burst of I/O, and a
	// notification for each one would teach an operator to ignore the list.
	IOWaitSustained = 10 * time.Minute
)

// DiskBound reports whether this host has been waiting on its disk, steadily,
// for long enough to say so.
func (u HostUsage) DiskBound(now time.Time) bool {
	return u.Fresh(now) && u.IOWaitHighSince != nil && now.Sub(*u.IOWaitHighSince) >= IOWaitSustained
}

func (u HostUsage) Fresh(now time.Time) bool {
	return !u.SampledAt.IsZero() && !now.Before(u.SampledAt) && now.Sub(u.SampledAt) < HostUsageMaxAge
}

// ObserveHostUsage validates just the measurements an agent may supply. A
// forged timestamp cannot keep a reading fresh, or shorten a pressure hold.
// Sustained CPU pressure pauses admission after 30 seconds above 95%; a
// reading below 85% releases it. Busy runners are never changed by this state.
//
// lentPercent is the controller's own figure for the share of the host that
// runners were using out of lent CPU, and both thresholds are judged without
// it. The recorded CPUPercent stays the raw measurement.
func ObserveHostUsage(previous, measured HostUsage, lentPercent float64, memoryMB int64, now time.Time) HostUsage {
	u := HostUsage{SampledAt: now}
	if v := measured.CPUPercent; v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 100 {
		raw := *v
		u.CPUPercent = &raw
		if !math.IsNaN(lentPercent) && lentPercent > 0 {
			u.LentCPUPercent = min(lentPercent, raw)
		}
		value := *u.UnlentCPUPercent()
		if previous.Fresh(now) && previous.CPUPercent != nil {
			u.CPUHeld = previous.CPUHeld && value >= 85
		}
		if value >= 95 {
			since := now
			if previous.Fresh(now) && previous.CPUHighSince != nil {
				since = *previous.CPUHighSince
			}
			u.CPUHighSince = &since
			u.CPUHeld = u.CPUHeld || now.Sub(since) >= CPUHoldWindow
		}
	}
	if v := measured.MemoryAvailableMB; v != nil && *v >= 0 && memoryMB > 0 && *v <= memoryMB {
		value := *v
		u.MemoryAvailableMB = &value
	}
	if v := measured.IOWaitPercent; v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 100 {
		wait := *v
		u.IOWaitPercent = &wait
		held := previous.Fresh(now) && previous.IOWaitHighSince != nil
		switch {
		case wait >= IOWaitHighPercent && held:
			since := *previous.IOWaitHighSince
			u.IOWaitHighSince = &since
		case wait >= IOWaitHighPercent:
			since := now
			u.IOWaitHighSince = &since
		case wait >= IOWaitCalmPercent && held:
			since := *previous.IOWaitHighSince
			u.IOWaitHighSince = &since
		}
	}
	// A load average has no ceiling -- that is the point of it -- but it is
	// never negative, and a NaN or an infinity is an agent that read
	// something other than /proc/loadavg. A figure that is only a load
	// average, with no CPU and no memory, is still a measurement.
	if v := measured.LoadAverage1; v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 {
		value := *v
		u.LoadAverage1 = &value
	}
	if u.CPUPercent == nil && u.MemoryAvailableMB == nil && u.LoadAverage1 == nil {
		return HostUsage{}
	}
	return u
}

func (u HostUsage) Value() (driver.Value, error) { return marshalJSON(u) }

func (u *HostUsage) Scan(value any) error {
	*u = HostUsage{}
	switch v := value.(type) {
	case string:
		return unmarshalJSON(v, u)
	case []byte:
		return unmarshalJSON(string(v), u)
	case nil:
		return nil
	default:
		return fmt.Errorf("host usage: unexpected database value %T", value)
	}
}

// SetHostUsage cannot write an operator's capacity, cordon or reserves.
func (s *Store) SetHostUsage(ctx context.Context, id string, usage HostUsage) error {
	res, err := s.exec(ctx, `UPDATE hosts SET usage=? WHERE id=?`, usage, id)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "host", id)
}
