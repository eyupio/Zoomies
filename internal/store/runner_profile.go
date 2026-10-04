package store

import (
	"database/sql/driver"
	"fmt"
)

// RunnerProfile is an operator's answer, for one host, to "how big is a runner
// here?". It has two tiers: a minimum, below which no runner is placed on the
// host, and a standard, which is the size a runner is given when its pool
// leaves that to the host. The standard carries the most CPU one runner may be
// lent on top.
//
// Every field is optional and zero is "not said", never "none": a field left
// out follows the fleet's runners.* setting, and the controller resolves that
// when it sizes a runner (controller.EffectiveProfile) rather than copying the
// fleet's figure in here. That is what keeps a host that says nothing following
// the fleet when an operator changes it, instead of freezing the figure it read
// the day the host joined.
//
// The profile is the operator's alone, like the capacity and the reserves. An
// agent reports what it measures about its machine and never writes this, so
// the same host cannot decide how big its own runners are.
type RunnerProfile struct {
	// Minimum is the least a runner on this host is ever given. It joins the
	// pool's own minimum -- the larger of the two applies -- and a pool whose
	// size is stated below it is not placed here at all.
	Minimum RunnerSize `json:"minimum,omitzero"`
	// Standard is the size of one runner here for a pool that takes its size
	// from the host, and what the slot count is derived from.
	Standard RunnerStandard `json:"standard,omitzero"`
	// Tmpfs is whether, and how far, a pool may keep a runner's folders in
	// memory on this host. It is the same kind of answer as the two above -- how
	// a runner here is built -- and is just as much the operator's.
	Tmpfs HostTmpfs `json:"tmpfs,omitzero"`
}

// RunnerSize is a size of runner: how much CPU and memory it has. It is the
// minimum of a RunnerProfile, and the fleet's default size wherever the
// controller carries that beside a pool (Pool.FleetStandard).
type RunnerSize struct {
	CPUs     float64 `json:"cpus,omitempty"`
	MemoryMB int64   `json:"memory_mb,omitempty"`
}

// RunnerStandard is the usual size of a runner on a host.
type RunnerStandard struct {
	CPUs     float64 `json:"cpus,omitempty"`
	MemoryMB int64   `json:"memory_mb,omitempty"`
	// BurstMaxCPUs is the most CPU one runner here may use, guaranteed share
	// and lent CPU together. A pool's own cpu_burst.max_cpus can lower it and
	// never raise it, so a host's owner has the last word on how much of the
	// machine one job may take. Zero is "no host ceiling": the pool's ceiling,
	// or the host's allocatable CPU when the pool has none either.
	BurstMaxCPUs float64 `json:"burst_max_cpus,omitempty"`
}

// Set reports whether the operator has said anything about this host's
// runners. It is what the UI asks before it draws a profile as present and
// what the scheduler asks before it stops using the slot count alone.
func (p RunnerProfile) Set() bool { return p != RunnerProfile{} }

// Sized reports whether the standard names a size at all, in either dimension.
// A standard with only a burst ceiling does not: there is nothing to divide the
// machine by, so the slot count stays the operator's.
func (s RunnerStandard) Sized() bool { return s.CPUs > 0 || s.MemoryMB > 0 }

// Value stores the profile as JSON; "{}" is no profile.
func (p RunnerProfile) Value() (driver.Value, error) { return marshalJSON(p) }

// Scan reads the column back. NULL, which a hand-edited database can leave, and
// an empty string are no profile, so a host is never unreadable for lack of one.
func (p *RunnerProfile) Scan(value any) error {
	*p = RunnerProfile{}
	switch v := value.(type) {
	case string:
		return unmarshalJSON(v, p)
	case []byte:
		return unmarshalJSON(string(v), p)
	case nil:
		return nil
	default:
		return fmt.Errorf("runner profile: unexpected database value %T", value)
	}
}
