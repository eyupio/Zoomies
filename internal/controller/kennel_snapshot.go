package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

// The constants Kennel Club's loop is built from. They are constants with a
// documented value and not settings: the measures in the plan say when one earns
// a knob, and a setting nobody can reason about is worse than a number somebody
// can.
const (
	// kennelRetention is how long a repository's row is kept after the fleet last
	// served it, so a quiet repository keeps its waivers. A repository somebody
	// told Kennel Club not to look at is kept however long it has been quiet.
	kennelRetention = 90 * 24 * time.Hour
	// kennelLocalInterval is the least time between two evaluations of what the
	// fleet itself observed. They cost no GitHub request, but a busy fleet changes
	// the facts every second and a finding that flickered would be worse than one
	// ten minutes behind.
	kennelLocalInterval = 10 * time.Minute
	// kennelMaxWindow is the longest the evidence reaches back.
	kennelMaxWindow = 30 * 24 * time.Hour
	// kennelUnservedWindow is how far back a job that waited for a label no pool
	// serves still counts: a label somebody fixed last month is not news.
	kennelUnservedWindow = 7 * 24 * time.Hour
	// kennelRunsPerRefresh caps the runs read for one repository in one refresh.
	kennelRunsPerRefresh = 100
	// kennelRunsKept caps the runs remembered for one repository. Only the ones a
	// check can use are kept -- fork pull requests and the four events a stranger
	// can trigger -- so this is a long way above what a repository produces.
	kennelRunsKept = 500
	// kennelListingLimit bounds a repository listing, as the discovery of AI
	// Context does.
	kennelListingLimit = 500
)

// kennelWindow is how far back the evidence reaches: thirty days, or the fleet's
// job retention if that is shorter, because the jobs table cannot answer about
// a day it has already pruned. A sentence that says "in the last N days" says
// this one.
func kennelWindow(retentionJobs time.Duration) time.Duration {
	if retentionJobs > 0 && retentionJobs < kennelMaxWindow {
		return retentionJobs
	}
	return kennelMaxWindow
}

// kennelWatermark is what the controller keeps about the facts it has read for a
// repository, in the row's own document. The store keeps it whole and has no
// opinion about what is in it.
//
// Only runs a check can use are remembered. A run's trigger and head repository
// never change, so each is read once; the watermark is what stops a restart
// reading the same hundred again.
type kennelWatermark struct {
	// After is the highest run ID read in an unbroken run from the lowest one
	// taken. The next read asks only for runs above it.
	After         int64                 `json:"after"`
	Workflows     *kennel.WorkflowFacts `json:"workflows,omitempty"`
	WorkflowState kennel.CoverageState  `json:"workflow_state,omitempty"`
	// WorkflowFiles is the inventory the workflows were read from, by SHA, with
	// each path as the gate let it through or "" where it did not. It is what
	// the page resolves a finding's file to; it is never in the snapshot.
	WorkflowFiles []kennelWorkflowRef `json:"workflow_files,omitempty"`
	// WorkflowFormat says which shape Workflows holds. A watermark from before
	// the files were kept by SHA holds counts the evaluator can no longer read,
	// so one below kennelWorkflowFormat is read again once.
	WorkflowFormat int                   `json:"workflow_format,omitempty"`
	Setup          *kennel.SetupFacts    `json:"setup,omitempty"`
	SetupState     kennel.CoverageState  `json:"setup_state,omitempty"`
	Guidance       *kennel.GuidanceFacts `json:"guidance,omitempty"`
	GuidanceState  kennel.CoverageState  `json:"guidance_state,omitempty"`
	GuidanceFiles  []kennelWorkflowRef   `json:"guidance_files,omitempty"`
	// Runs are the fork pull requests and stranger-triggered runs found, newest
	// first.
	Runs []kennelSeenRun `json:"runs"`
	// GapQueuedAt is, in milliseconds, when the newest run was queued that a read
	// had to leave unread because there were more than the cap. While that is
	// still inside the window the runs are a sample. Zero means nothing was
	// skipped.
	GapQueuedAt int64 `json:"gap_queued_at,omitempty"`
	// ReadAt is when a read of the runs last got anywhere, in milliseconds. Zero
	// means never.
	ReadAt int64 `json:"read_at,omitempty"`
	// State is how the last attempt to read the runs ended, when it did not end
	// well. Empty means it did.
	State kennel.CoverageState `json:"state,omitempty"`
}

// kennelWorkflowFormat is the shape of Workflows this release writes.
const kennelWorkflowFormat = 2

// kennelWorkflowRef is one workflow file in the inventory: its blob SHA and
// its path after the gate.
type kennelWorkflowRef struct {
	SHA  string `json:"sha"`
	Path string `json:"path,omitempty"`
}

// kennelSeenRun is one run worth remembering.
type kennelSeenRun struct {
	ID    int64  `json:"id"`
	Event string `json:"event"`
	Fork  bool   `json:"fork,omitempty"`
	// QueuedAt is in milliseconds; a run ages out of the window by it.
	QueuedAt int64 `json:"queued_at"`
}

// parseKennelWatermark reads the stored document. One that does not parse is a
// repository nothing has been read for, which costs one re-read and not a
// refusal to evaluate.
func parseKennelWatermark(raw []byte) kennelWatermark {
	var w kennelWatermark
	if err := json.Unmarshal(raw, &w); err != nil {
		return kennelWatermark{}
	}
	return w
}

func (w kennelWatermark) marshal() json.RawMessage {
	if w.Runs == nil {
		w.Runs = []kennelSeenRun{}
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// remember adds runs, newest first, keeping only those inside the window and no
// more than kennelRunsKept of them. A run already remembered is kept once.
func (w *kennelWatermark) remember(found []kennelSeenRun, now time.Time, window time.Duration) {
	cutoff := now.Add(-window).UnixMilli()
	all := append(slices.Clone(found), w.Runs...)
	slices.SortStableFunc(all, func(a, b kennelSeenRun) int {
		switch {
		case a.ID > b.ID:
			return -1
		case a.ID < b.ID:
			return 1
		}
		return 0
	})
	out := make([]kennelSeenRun, 0, len(all))
	for _, r := range all {
		if r.QueuedAt < cutoff || (len(out) > 0 && out[len(out)-1].ID == r.ID) {
			continue
		}
		if len(out) == kennelRunsKept {
			break
		}
		out = append(out, r)
	}
	w.Runs = out
}

// facts is what the evaluator is given: the runs remembered that are still inside
// the window, with their events put through the allow-list. nil means nothing
// has been read, which is different from none having been found.
func (w kennelWatermark) facts(now time.Time, window time.Duration) *kennel.RunFacts {
	if w.ReadAt == 0 {
		return nil
	}
	cutoff := now.Add(-window).UnixMilli()
	out := &kennel.RunFacts{Window: window}
	for _, r := range w.Runs {
		if r.QueuedAt < cutoff {
			continue
		}
		out.Runs = append(out.Runs, kennel.Run{ID: r.ID, Event: kennel.NormalizeEvent(r.Event), FromFork: r.Fork})
	}
	return out
}

// coverage is how far the runs could be read.
//
// A failure after a read that did get somewhere is partial and not the failure's
// own state: what was found stands, and what was not found is not an all-clear.
// A repository that was never read has nothing to stand, so it says why, which
// is the sentence that names the permission to grant.
func (w kennelWatermark) coverage(now time.Time, window time.Duration) kennel.CoverageState {
	if w.ReadAt == 0 {
		if w.State == "" {
			return kennel.CoverageNotRead
		}
		return w.State
	}
	if w.State != "" {
		return kennel.CoveragePartial
	}
	if w.GapQueuedAt > now.Add(-window).UnixMilli() {
		return kennel.CoveragePartial
	}
	return kennel.CoverageOK
}

// kennelDangers reduces a pool to the words the evaluator knows. The evaluator
// never sees a pool: it sees what is weak about it, so a pool's name, labels and
// image cannot reach a sentence by the way.
//
// The first four are exactly what Pool.Dangerous says, which is held by a test,
// so a new way a pool can be dangerous is a failing test and not a gap in what
// Kennel Club notices. The last has no sentence there: a process backend runs a
// job directly on the host, which Dangerous does not call dangerous because it
// is what that backend is, and which is the strongest thing there is to say
// about a pool that runs a stranger's code.
func kennelDangers(p *store.Pool) []kennel.PoolDanger {
	var out []kennel.PoolDanger
	if !p.Ephemeral {
		out = append(out, kennel.DangerPersistent)
	}
	if p.DockerMode == store.DockerHostSocket {
		out = append(out, kennel.DangerHostSocket)
	}
	if p.DockerMode == store.DockerDinD {
		out = append(out, kennel.DangerPrivileged)
	}
	if p.RunAsRoot {
		out = append(out, kennel.DangerRoot)
	}
	if p.Backend == store.BackendProcess {
		out = append(out, kennel.DangerNoContainer)
	}
	return out
}

// kennelDigests is what the loop compares to know whether anything it would
// evaluate has changed, in two halves that are throttled differently.
//
// Facts is what GitHub and the fleet said, and a busy fleet changes it every
// second, so it is re-evaluated at most every ten minutes. Policy is what the
// operator decided -- a check turned off, a waiver made or ended -- and an
// operator who has just changed it is looking at the page, so it is
// re-evaluated at once: a setting that is "saved" while the page still shows the
// old answer for ten minutes is a setting that does not seem to work.
type kennelDigests struct {
	Facts  string
	Policy string
}

// String is the form kept in the row's inputs_digest column.
func (d kennelDigests) String() string { return d.Facts + "/" + d.Policy }

// parseKennelDigests reads what String wrote. One that does not parse is a
// digest that matches nothing, which costs one evaluation.
func parseKennelDigests(s string) kennelDigests {
	facts, policy, ok := strings.Cut(s, "/")
	if !ok {
		return kennelDigests{}
	}
	return kennelDigests{Facts: facts, Policy: policy}
}

// kennelDigest covers the snapshot, and separately the policy and the
// evaluator's version, with a waiver counted by whether it still covers
// anything so that one ending is a change and its finding comes back within one
// pass.
func kennelDigest(s kennel.Snapshot, p kennel.Policy, now time.Time) kennelDigests {
	s.At = time.Time{}
	s.Fleet.Pools = slices.Clone(s.Fleet.Pools)
	slices.SortFunc(s.Fleet.Pools, func(a, b kennel.PoolFact) int { return strings.Compare(a.ID, b.ID) })
	type waiver struct {
		ID       string
		Code     kennel.Code
		Subject  string
		Severity kennel.Severity
		Ends     int64
		Active   bool
	}
	var waivers []waiver
	for _, w := range p.Waivers {
		waivers = append(waivers, waiver{w.ID, w.Code, w.Subject, w.Severity, w.ExpiresAt.UnixMilli(), now.Before(w.ExpiresAt)})
	}
	slices.SortFunc(waivers, func(a, b waiver) int { return strings.Compare(a.ID, b.ID) })
	var disabled []string
	for name, off := range p.Disabled {
		if off {
			disabled = append(disabled, name)
		}
	}
	slices.Sort(disabled)
	return kennelDigests{
		Facts: hashOf(s),
		Policy: hashOf(struct {
			Version  int
			Disabled []string
			Waivers  []waiver
		}{kennel.Version, disabled, waivers}),
	}
}

func hashOf(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}
