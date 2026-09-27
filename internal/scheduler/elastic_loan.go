package scheduler

import (
	"fmt"
	"time"
)

// These thresholds decide when CPU lent to a runner comes back because the
// runner is not using it, and how long the controller then waits before
// lending to that runner again. They are deliberately far apart from the
// thresholds that lend in the first place, because the failure being designed
// out is a quota that moves on every heartbeat.
const (
	// LoanInUseShare is how much of a loan a runner must be using, above its
	// guarantee, for the loan to count as used. A quarter, not a half: a
	// build's usage is spiky between link and compile steps, and a loan that
	// is doing a quarter of its job on average is still shortening the job.
	// Below it, a runner lent two cores that uses its guarantee and a sliver
	// is holding CPU a neighbour could use.
	LoanInUseShare = 0.25
	// LoanReclaimSamples is how many fresh samples in a row must show a loan
	// unused before it is withdrawn. One low sample is a link step or a test
	// waiting on a socket; three -- a minute and a half at the default sample
	// interval -- is a job that has settled below what it was given.
	LoanReclaimSamples = 3
	// LoanBackoff is how long a runner whose loan was withdrawn waits before
	// it may be lent again on ordinary demand. Without it, a runner that
	// saturates its guarantee but not a loan is demanding again on the very
	// next sample, and is lent, wastes it and loses it in a loop.
	LoanBackoff = 5 * time.Minute
	// LoanBackoffMax caps the backoff, which doubles each time a loan is
	// wasted again straight after one. A job whose demand really has changed
	// is never kept waiting longer than this.
	LoanBackoffMax = 30 * time.Minute
	// LoanSaturatedShare is the share of its current limit a runner must use
	// to count as pressing against it. Short of the whole limit, because a
	// cgroup at its quota still reads a little under it once the sample's
	// window is averaged.
	LoanSaturatedShare = 0.95
)

// LoanMemory is what the planner remembers about one runner's loan between
// heartbeats. The zero value is a runner nothing is known about.
type LoanMemory struct {
	// LastSampleAt is the sample the memory was last advanced by. A heartbeat
	// that carries the same sample again counts for nothing: the thresholds
	// are counted in samples, not heartbeats.
	LastSampleAt time.Time
	// LowSamples is how many fresh samples in a row showed the current loan
	// unused.
	LowSamples int
	// WastedAt is when a loan was last withdrawn as unused; zero when none
	// has been, or the backoff has been spent by a loan that was used.
	WastedAt time.Time
	// Strikes is how many loans in a row were withdrawn unused. It doubles the
	// backoff, and a loan that is used clears it.
	Strikes int
	// WastedPeakCPUs is the most CPU the runner used while it held the loan
	// that was withdrawn. It is what makes a saturated guarantee a new signal
	// or an old one: see DecideLoan.
	WastedPeakCPUs float64
	// PeakCPUs is the most CPU the runner has used under its current loan.
	PeakCPUs float64
}

// LoanInput is one runner at one heartbeat. Now is passed in rather than
// read, so the decision is a function of its input and can be tested one
// heartbeat at a time.
type LoanInput struct {
	Now time.Time
	// SampledAt is when UsedCPUs was measured; zero is no sample at all.
	SampledAt time.Time
	// BaseCPUs is the runner's guarantee and LimitCPUs the quota it holds
	// now: above BaseCPUs while it is lent CPU, equal to it otherwise.
	BaseCPUs  float64
	LimitCPUs float64
	UsedCPUs  float64
	// HolderBaseCPUs and HolderUsedCPUs are the guarantee and use of the
	// container the loan is actually given to, where that is not the whole
	// runner: a docker-in-docker pair's loan goes to its busier half alone,
	// so the pair's sum -- the idle half's share included -- would read a
	// daemon using two lent cores as a loan barely touched. Zero means the
	// runner is one container, and BaseCPUs and UsedCPUs are the holder's.
	HolderBaseCPUs float64
	HolderUsedCPUs float64
	// Saturated is a signal besides UsedCPUs that the runner is pressing
	// against its limit: the cgroup's throttling counters rose, or the busier
	// half of a docker-in-docker pair is at its own quota.
	Saturated bool
	// Demanding is the ordinary entry test: the runner is using most of its
	// guarantee or being throttled at it.
	Demanding bool
	Memory    LoanMemory
}

// LoanCode is a stable name for a loan decision, for metrics and tests.
type LoanCode string

const (
	LoanNotDemanding    LoanCode = "not_demanding"
	LoanLend            LoanCode = "lend"
	LoanInUse           LoanCode = "in_use"
	LoanWatching        LoanCode = "watching"
	LoanReclaimed       LoanCode = "reclaimed"
	LoanBackingOff      LoanCode = "backing_off"
	LoanBackoffOverride LoanCode = "backoff_overridden"
)

// LoanDecision says whether a runner may take part in this heartbeat's
// water-fill, and why, in words an operator reading a log can act on.
type LoanDecision struct {
	Lend   bool
	Code   LoanCode
	Reason string
	Memory LoanMemory
}

// Backoff is how long the runner waits after its latest wasted loan.
func (m LoanMemory) Backoff() time.Duration {
	d := LoanBackoff
	for i := 1; i < m.Strikes && d < LoanBackoffMax; i++ {
		d *= 2
	}
	return min(d, LoanBackoffMax)
}

// DecideLoan decides whether a runner should be lent CPU at this heartbeat.
//
// A runner that holds a loan keeps it while it uses at least a quarter of it;
// after LoanReclaimSamples fresh samples below that, the loan is withdrawn and
// the runner backs off. During the backoff it is lent again only when it
// presses against its guarantee and did not while it held the wasted loan --
// that is a job whose demand changed, not the same job asking again. A job
// that used a sliver more than its guarantee under the loan is expected to
// saturate its guarantee without it, and re-lending on that would be the
// flap this exists to stop.
func DecideLoan(in LoanInput) LoanDecision {
	m := in.Memory
	fresh := !in.SampledAt.IsZero() && in.SampledAt.After(m.LastSampleAt)
	if fresh {
		m.LastSampleAt = in.SampledAt
	}
	lent := in.LimitCPUs > in.BaseCPUs+cpuEpsilon
	loan := in.LimitCPUs - in.BaseCPUs

	if !m.WastedAt.IsZero() {
		since := in.Now.Sub(m.WastedAt)
		if since < m.Backoff() {
			// A sample can still show the loan for a heartbeat after it was
			// withdrawn, because the agent moves the quota only once it has
			// the plan. That is not the runner holding a loan, and judging it
			// as one would lend the CPU straight back.
			pressing := !lent && (in.UsedCPUs >= LoanSaturatedShare*in.LimitCPUs || in.Saturated)
			// The wasted loan's peak is what separates a new demand from the
			// old one: a job that stayed under its guarantee while it was lent
			// more, and now fills it, has started doing something else.
			if pressing && m.WastedPeakCPUs < LoanSaturatedShare*in.BaseCPUs {
				m.WastedAt, m.WastedPeakCPUs, m.LowSamples, m.PeakCPUs = time.Time{}, 0, 0, 0
				return LoanDecision{Lend: true, Code: LoanBackoffOverride, Memory: m, Reason: fmt.Sprintf(
					"lent again before its backoff ended: now at its %.2f CPU limit, where it used only %.2f while it last held a loan",
					in.LimitCPUs, in.Memory.WastedPeakCPUs)}
			}
			m.LowSamples, m.PeakCPUs = 0, 0
			return LoanDecision{Code: LoanBackingOff, Memory: m, Reason: fmt.Sprintf(
				"not lent: its last loan went unused, and it waits another %s before it is lent again",
				(m.Backoff() - since).Round(time.Second))}
		}
		if since >= m.Backoff()+LoanBackoffMax {
			// Long enough ago that the next waste is a new story, not a
			// repeat of the last one.
			m.WastedAt, m.WastedPeakCPUs, m.Strikes = time.Time{}, 0, 0
		}
	}

	if lent {
		if fresh {
			m.PeakCPUs = max(m.PeakCPUs, in.UsedCPUs)
		}
		holderBase, holderUsed := in.BaseCPUs, in.UsedCPUs
		if in.HolderBaseCPUs > 0 {
			holderBase, holderUsed = in.HolderBaseCPUs, in.HolderUsedCPUs
		}
		floor := holderBase + LoanInUseShare*loan
		if holderUsed >= floor {
			m.LowSamples, m.Strikes, m.WastedAt, m.WastedPeakCPUs = 0, 0, time.Time{}, 0
			return LoanDecision{Lend: true, Code: LoanInUse, Memory: m, Reason: fmt.Sprintf(
				"keeps its loan: using %.2f CPUs where the loan is, at least a quarter of the %.2f lent above a %.2f share",
				holderUsed, loan, holderBase)}
		}
		if fresh {
			m.LowSamples++
		}
		if m.LowSamples < LoanReclaimSamples {
			return LoanDecision{Lend: true, Code: LoanWatching, Memory: m, Reason: fmt.Sprintf(
				"keeps its loan for now: using %.2f CPUs where the loan is, under %.2f, for %d of %d samples before it is taken back",
				holderUsed, floor, m.LowSamples, LoanReclaimSamples)}
		}
		m.Strikes++
		m.WastedAt, m.WastedPeakCPUs = in.Now, m.PeakCPUs
		m.LowSamples, m.PeakCPUs = 0, 0
		return LoanDecision{Code: LoanReclaimed, Memory: m, Reason: fmt.Sprintf(
			"loan taken back: used at most %.2f CPUs of %.2f for %d samples in a row; not lent again for %s unless its demand changes",
			m.WastedPeakCPUs, in.LimitCPUs, LoanReclaimSamples, m.Backoff())}
	}

	m.LowSamples, m.PeakCPUs = 0, 0
	if in.Demanding {
		return LoanDecision{Lend: true, Code: LoanLend, Memory: m, Reason: fmt.Sprintf(
			"may be lent CPU: using %.2f CPUs of its %.2f guarantee, or throttled at it", in.UsedCPUs, in.BaseCPUs)}
	}
	return LoanDecision{Code: LoanNotDemanding, Memory: m, Reason: fmt.Sprintf(
		"not lent: using %.2f CPUs of its %.2f guarantee, short of what would count as demand", in.UsedCPUs, in.BaseCPUs)}
}
