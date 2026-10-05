package controller

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/scheduler"
	"github.com/eyupio/zoomies/internal/store"
)

// Memory states a runner's memory_resource can be in. They are stable names for
// clients and alerts; Reason is the sentence for a person.
const (
	// MemoryWatching: the valve is on and has done nothing for this runner.
	MemoryWatching = "watching"
	// MemoryObserving: observe mode, which decides and records and changes
	// nothing. The runner would have been lent memory, or came near a limit.
	MemoryObserving = "observing"
	// MemoryLent: the runner holds memory beyond what it was created with.
	MemoryLent = "lent"
	// MemorySpilled: it may use swap beyond its limit, which is the last resort.
	MemorySpilled = "spilled"
)

// MemoryResourceView is what the memory valve has done for one runner, in
// exact numbers and a stable state. It is the memory counterpart of
// CPUResourceView, and differs in the one way the two resources do: what is
// lent here stays lent, so the figures only ever go up while the runner lives.
type MemoryResourceView struct {
	// State is MemoryWatching, MemoryObserving, MemoryLent or MemorySpilled;
	// Mode is observe, automatic, or off for a runner whose pool has since
	// turned the valve off while a loan it made stands.
	State string `json:"state"`
	Mode  string `json:"mode"`
	// Blocked is the agent's latest word when it wanted to lend more and could
	// not -- at_ceiling, pool_empty, host_floor -- or could not lend at all --
	// unmeasured, unsupported, failed. Empty when nothing is in the way. It is
	// beside State and not instead of it: a runner can hold a loan and be at its
	// ceiling.
	Blocked string `json:"blocked,omitempty"`
	// Reason is the sentence for the last thing that was not "healthy".
	Reason string `json:"reason,omitempty"`
	// GuaranteedMB is what the runner was created with, its containers together;
	// CurrentMB what they hold now; CeilingMB the most they may.
	GuaranteedMB int64 `json:"guaranteed_mb"`
	CurrentMB    int64 `json:"current_mb"`
	CeilingMB    int64 `json:"ceiling_mb"`
	// LentMB is CurrentMB less GuaranteedMB. SpillMB is the swap the runner may
	// use beyond its limits, and SpillAllowedMB what its pool allows each
	// container.
	LentMB         int64 `json:"lent_mb"`
	SpillMB        int64 `json:"spill_mb,omitempty"`
	SpillAllowedMB int64 `json:"spill_allowed_mb,omitempty"`
	// WouldLendMB and WouldSpillMB are what an observing valve would hold if it
	// were allowed to: zero outside observe mode.
	WouldLendMB  int64 `json:"would_lend_mb,omitempty"`
	WouldSpillMB int64 `json:"would_spill_mb,omitempty"`
	// NearLimit is whether the runner has come within a tenth of a limit, and
	// Raises how many times a limit was raised for it.
	NearLimit bool `json:"near_limit,omitempty"`
	Raises    int  `json:"raises,omitempty"`
}

// valveOutcome is a decision code as a metric label. The code is an agent's word,
// and a label is a series for every value it takes, so a value that is not one
// of the codes the agent defines is "unknown" rather than a series of its own.
func valveOutcome(code string) string {
	switch agent.MemoryValveCode(code) {
	case agent.MemoryHealthy, agent.MemoryRaised, agent.MemorySpilled, agent.MemoryAtCeiling, agent.MemoryPoolEmpty,
		agent.MemoryHostFloor, agent.MemoryUnmeasured, agent.MemoryUnsupported, agent.MemoryFailed:
		return code
	}
	return "unknown"
}

// blockedCodes are the decisions that say the valve could not give a runner
// what it wanted.
func blockedCode(code string) bool {
	switch agent.MemoryValveCode(code) {
	case agent.MemoryAtCeiling, agent.MemoryPoolEmpty, agent.MemoryHostFloor,
		agent.MemoryUnmeasured, agent.MemoryUnsupported, agent.MemoryFailed:
		return true
	}
	return false
}

// memoryResourceView renders what the valve has done for a runner. Nil for a
// runner it has nothing to say about: its pool does not have the valve on and it
// holds no loan.
func memoryResourceView(r *store.Runner, p *store.Pool, h *store.Host) *MemoryResourceView {
	if r == nil || p == nil || r.AllocatedMemoryMB <= 0 {
		return nil
	}
	var sample backend.Stats
	if len(r.ResourceSample) > 0 {
		_ = json.Unmarshal(r.ResourceSample, &sample)
	}
	v := sample.MemoryValve
	lent := r.LentMemoryMB
	if v != nil {
		lent = max(lent, v.LentBytes>>20)
	}
	if !p.MemoryBurst.Observes() && lent == 0 && (v == nil || v.SpillBytes == 0) {
		return nil
	}

	// What it was launched with, from its row: the pool's current charge for a
	// runner is not that once the pool or the host has been edited, and is not
	// computable at all from the raw pool a runner's view is rendered with.
	guaranteed, ok := scheduler.LaunchedMemoryMB(p, r)
	if !ok {
		return nil
	}
	out := &MemoryResourceView{
		State: MemoryWatching, Mode: string(p.MemoryBurst.Mode),
		GuaranteedMB: guaranteed, LentMB: lent, CurrentMB: guaranteed + lent,
		SpillAllowedMB: p.MemoryBurst.SpillMB,
	}
	if out.Mode == "" {
		out.Mode = string(store.MemoryBurstOff)
	}
	if p.MemoryBurst.Observes() {
		out.CeilingMB = scheduler.MemoryCeiling(p, h, guaranteed)
	}
	out.CeilingMB = max(out.CeilingMB, out.CurrentMB)
	if v != nil {
		out.Mode = v.Mode
		out.Reason = v.Reason
		out.SpillMB = v.SpillBytes >> 20
		out.NearLimit, out.Raises = v.NearLimit, v.Raises
		if v.Mode == string(store.MemoryBurstObserve) {
			out.WouldLendMB, out.WouldSpillMB = v.WouldLendBytes>>20, v.WouldSpillBytes>>20
		}
		if blockedCode(v.Code) {
			out.Blocked = v.Code
		}
	}
	switch {
	case out.SpillMB > 0:
		out.State = MemorySpilled
	case out.LentMB > 0:
		out.State = MemoryLent
	case out.Mode == string(store.MemoryBurstObserve):
		out.State = MemoryObserving
	}
	return out
}

// ScratchView is which of a runner's folders its pool keeps in memory, as they
// were worked out when it was created. Nil for a runner whose pool keeps none.
type ScratchView struct {
	// InMemory counts the folders the runner was given in memory.
	InMemory int                 `json:"in_memory"`
	Folders  []ScratchFolderView `json:"folders"`
}

// ScratchFolderView is one folder: what it is, where it lives in the runner,
// what was asked for and what it was given. A folder the pool wanted in memory
// that the runner was not given there says why, in Note.
type ScratchFolderView struct {
	Kind     store.ScratchKind `json:"kind"`
	Label    string            `json:"label"`
	Path     string            `json:"path"`
	InMemory bool              `json:"in_memory"`
	SizeMB   int64             `json:"size_mb,omitempty"`
	AskedMB  int64             `json:"asked_mb,omitempty"`
	Auto     bool              `json:"auto,omitempty"`
	Why      store.ScratchWhy  `json:"why,omitempty"`
	Note     string            `json:"note,omitempty"`
}

func scratchView(r *store.Runner) *ScratchView {
	if r == nil || !r.Scratch.Any() {
		return nil
	}
	out := &ScratchView{InMemory: r.Scratch.InMemory(), Folders: make([]ScratchFolderView, 0, len(r.Scratch.Folders))}
	for _, f := range r.Scratch.Folders {
		label, path, floor := scratchFolder(f.Kind)
		out.Folders = append(out.Folders, ScratchFolderView{
			Kind: f.Kind, Label: label, Path: path, InMemory: f.InMemory(),
			SizeMB: f.SizeMB, AskedMB: f.AskedMB, Auto: f.Auto, Why: f.Why, Note: scratchNote(f.Why, label, floor),
		})
	}
	return out
}

// scratchFolder names a folder, says where it is in the runner, and gives the
// least an automatic one is worth.
func scratchFolder(kind store.ScratchKind) (label, path string, floorMB int64) {
	switch kind {
	case store.ScratchWork:
		return "Work folder", backend.RunnerWorkMount, store.AutoMinWorkMB
	case store.ScratchTmp:
		return "Temporary folder", backend.RunnerTmpMount, store.AutoMinTmpMB
	default:
		return "Docker image store", backend.DaemonStoreMount, store.AutoMinDaemonMB
	}
}

func scratchNote(why store.ScratchWhy, label string, floorMB int64) string {
	switch why {
	case store.ScratchAutoTooSmall:
		return fmt.Sprintf("Kept on disk: this runner's memory limit left the %s less than the %s it is worth having in memory.",
			lowerFirst(label), humanMB(floorMB))
	case store.ScratchHostOff:
		return "Kept on disk: this host's owner has turned in-memory folders off."
	case store.ScratchUnsupported:
		return "Kept on disk: the agent on this host is too old to keep folders in memory."
	case store.ScratchBound:
		return "Kept on disk: the work folder is a directory bound from the host."
	}
	return ""
}

// humanMB writes a size the way an operator says it: 2 GB, 1.5 GB, 512 MB. A
// figure that is not a whole half of a gigabyte stays in megabytes, because
// 1.3 GB would be a rounding of a number somebody may want exactly.
func humanMB(mb int64) string {
	switch {
	case mb >= 1024 && mb%1024 == 0:
		return fmt.Sprintf("%d GB", mb/1024)
	case mb >= 1024 && mb%512 == 0:
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]|0x20) + s[1:]
}

// MemoryPoolView is a host's pool of memory to lend, as the controller last
// worked it out: what is left of the machine once every promise is kept and the
// floor is left alone, and how much of it has been lent already.
type MemoryPoolView struct {
	// Supported is whether the host's agent carries the valve's rules out.
	Supported bool `json:"supported"`
	// CapacityMB is the most that may be lent on the host in all, LentMB what is
	// lent now and PoolMB what is left. FloorMB is the least free memory a loan
	// leaves the host.
	CapacityMB int64 `json:"capacity_mb"`
	LentMB     int64 `json:"lent_mb"`
	PoolMB     int64 `json:"pool_mb"`
	FloorMB    int64 `json:"floor_mb"`
	// CommittedMB, IdleReserveMB and StartReserveMB are the promises the capacity
	// was taken after, so a pool of nothing can say whose guarantees it went to.
	CommittedMB    int64 `json:"committed_mb"`
	IdleReserveMB  int64 `json:"idle_reserve_mb"`
	StartReserveMB int64 `json:"start_reserve_mb"`
	// Binding says what limited the capacity: "ledger" for the promises already
	// made, "measured" for the memory the host reported as free.
	Binding string `json:"binding"`
	// Observing and Enforcing count the runners here with a rule, by mode.
	Observing int `json:"observing"`
	Enforcing int `json:"enforcing"`
	// ShortAt is when a runner here was last refused memory because the host had
	// none to give, and ShortCode which of pool_empty or host_floor it was.
	ShortAt   *time.Time `json:"short_at,omitempty"`
	ShortCode string     `json:"short_code,omitempty"`
	// At is when the pool was last worked out.
	At time.Time `json:"at"`
}

// memoryPoolView renders what the controller last worked out for a host; nil for
// a host with no runner the valve applies to.
func (c *Controller) memoryPoolView(h *store.Host) *MemoryPoolView {
	s := c.memoryState(h.ID)
	if s.At.IsZero() {
		return nil
	}
	out := &MemoryPoolView{
		Supported: s.Supported, CapacityMB: s.Pool.CapacityMB, LentMB: s.Pool.LentMB, PoolMB: s.Pool.PoolMB,
		FloorMB: s.FloorMB, CommittedMB: s.Pool.CommittedMB, IdleReserveMB: s.Pool.IdleReserveMB,
		StartReserveMB: s.Pool.StartReserveMB, Binding: string(s.Pool.Binding),
		Observing: s.Observing, Enforcing: s.Enforcing, At: s.At,
	}
	if !s.ShortAt.IsZero() && c.Now().Sub(s.ShortAt) < memoryBlockedFor {
		at := s.ShortAt
		out.ShortAt, out.ShortCode = &at, string(s.ShortCode)
	}
	return out
}
