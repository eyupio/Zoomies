package scheduler

import (
	"fmt"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// AtQueue is the class a job joins the queue in, before anyone has looked at
// what it did last time: a size label in its runs-on, an operator's pin, the
// class kept from its earlier runs, or the default class.
//
// It does not look at the runs. That is done when a job finishes, and the
// result is kept; reading the kept class when the next one arrives is what makes
// classifying a job at the door a lookup and not a query over its history.
func (c SizeConfig) AtQueue(labels []string, pin *store.SizePin, kept *store.JobClass) Classification {
	if out, ok := c.authority(labels, pin); ok {
		return out
	}
	if kept != nil && kept.Class.Valid() {
		basis := kept.Basis
		if basis == "" {
			basis = store.SizeBasisHistory
		}
		return Classification{Class: kept.Class, Basis: basis, Reason: kept.Reason, FloorMB: kept.FloorMB, Runs: kept.Runs}
	}
	def := c.defaultClass()
	return Classification{Class: def, Basis: store.SizeBasisDefault,
		Reason: fmt.Sprintf("it has no measured runs yet, so it starts in the default class, %s", def)}
}

// Reroute is a queued job sent to a different class from the one it was routed
// to, and why.
type Reroute struct {
	Job      *store.Job
	From, To store.SizeClass
	// Note is the sentence kept on the job, which the Jobs page shows beside
	// where it ran.
	Note string
}

// RoutingInput is what Fallbacks decides from: the same fleet Decide was given
// and the plan it made of it.
type RoutingInput struct {
	Now   time.Time
	Pools []*store.Pool
	Hosts []*store.Host
	// Jobs is every job, of which only the queued ones matter.
	Jobs   []*store.Job
	Plan   Plan
	Config SizeConfig
	// Size is the same hook PlanAutoPools takes: a pool as the scheduler sizes
	// it, where the settings decide part of that.
	Size func(*store.Pool) *store.Pool
}

// classOrder is the order a job of class c is offered the classes in: its own,
// then each larger one, then each smaller one, nearest first. Larger comes
// before smaller because a larger host always has what the job needs, and a
// smaller one only sometimes does.
func classOrder(c store.SizeClass) []store.SizeClass {
	all := store.SizeClasses()
	out := []store.SizeClass{c}
	for _, k := range all {
		if k.Rank() > c.Rank() {
			out = append(out, k)
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Rank() < c.Rank() {
			out = append(out, all[i])
		}
	}
	return out
}

// Fallbacks decides which queued jobs should be sent to a class other than the
// one they are routed to, and where.
//
// A job whose class has no pool at all -- no host of that class is enrolled --
// is moved at once. A job whose class has a pool with no room is moved once it
// has waited the configured time and is among the jobs that pool has no runner
// for. It goes to the first class in its order that shows room for it, so it is
// not sent to a pool that cannot start it either, and only a job that has no
// runner coming is ever moved: one that has been sent somewhere and is being
// served stays, so two classes that are both short of room do not pass a job
// between them.
//
// A job that asked for a class by name is never moved: that is the guaranteed
// path, and it is the author's to change. A job nobody has classed has nothing
// to be moved from. A class smaller than the job's is used only when its runners
// hold what the job is known to need, except where the job's class has no pool
// and nothing larger does either, when the nearest smaller class is used anyway
// and the note says it may be too small: a job waiting for a host that does not
// exist waits for ever, and one that runs and is killed for memory says why.
func Fallbacks(in RoutingInput) []Reroute {
	pools := sortedPools(in.Pools)
	size := in.Size
	if size == nil {
		size = func(p *store.Pool) *store.Pool { return p }
	}
	demand, _ := assign(pools, in.Jobs, nil)

	plans := make(map[string]*PoolPlan, len(in.Plan.Pools))
	for i := range in.Plan.Pools {
		plans[in.Plan.Pools[i].PoolID] = &in.Plan.Pools[i]
	}
	moved := map[string]int{}
	var out []Reroute

	// spare is the runners a pool can still start for jobs sent to it.
	spare := func(p *store.Pool) int {
		pp := plans[p.ID]
		if pp == nil || pp.Blocked != "" || pp.BlockedNoEligibleHost || pp.Failing != "" || pp.Held != "" {
			return 0
		}
		return max(0, p.MaxRunners-(pp.Current+creates(pp.Actions))) - pp.Unserved - moved[p.ID]
	}

	// poolsFor are the enabled automatic pools of a class that could run the job.
	poolsFor := func(class store.SizeClass, j *store.Job) []*store.Pool {
		var found []*store.Pool
		for _, p := range pools {
			if servesClass(p, class) {
				if ok, _ := Eligible(p, j); ok {
					found = append(found, p)
				}
			}
		}
		return found
	}

	// landsIn is the pool a job would be given if it were routed to class: the
	// one the claim itself picks among the pools that serve the class and could
	// run the job. Any other choice -- the first by name, say -- checks the room
	// of one pool and then sends the job to another, which a fleet with a pool for
	// each architecture of a class has.
	landsIn := func(class store.SizeClass, j *store.Job) *store.Pool {
		routed := *j
		routed.RoutedClass = class
		return BestPool(poolsFor(class, j), &routed)
	}

	// holds reports whether a runner of the pool the job would land in has at
	// least mb of memory.
	holds := func(class store.SizeClass, j *store.Job, mb int64) bool {
		p := landsIn(class, j)
		if p == nil {
			return false
		}
		sized := size(p)
		for _, h := range in.Hosts {
			if k, _ := h.EffectiveSizeClass(); k != class || !HostAvailable(h, in.Now) || !HostOffers(h, p) {
				continue
			}
			if slotSize(sized, h).MemoryMB >= mb {
				return true
			}
		}
		return false
	}

	move := func(j *store.Job, from, to store.SizeClass, note string) {
		out = append(out, Reroute{Job: j, From: from, To: to, Note: note})
	}

	for _, j := range sortedJobs(in.Jobs) {
		if j.State != store.JobQueued || j.Provisioning != "" || !j.SizeClass.Valid() || j.SizeBasis == store.SizeBasisExplicit {
			continue
		}
		need, routed := j.SizeClass, j.RoutedClass
		if !routed.Valid() {
			// Not routed: the controller is only watching, or has not got to
			// this job yet. There is nothing to move it from.
			continue
		}
		if len(poolsFor(routed, j)) > 0 {
			continue
		}
		// The class it is routed to has no pool, so no host will ever take it
		// there. Nothing is waited for.
		order := classOrder(need)
		var pick store.SizeClass
		var smallerNote bool
		for _, k := range order {
			if k == routed || len(poolsFor(k, j)) == 0 {
				continue
			}
			if k.Rank() < need.Rank() && !(j.SizeFloorMB > 0 && holds(k, j, j.SizeFloorMB)) {
				continue
			}
			pick = k
			break
		}
		if pick == "" {
			// Nothing larger, nothing smaller that is known to hold it: the
			// nearest smaller class with a pool, because the alternative is a
			// job that never starts.
			for _, k := range order {
				if k.Rank() < need.Rank() && len(poolsFor(k, j)) > 0 {
					pick, smallerNote = k, true
					break
				}
			}
		}
		if pick == "" {
			continue
		}
		move(j, routed, pick, noPoolNote(routed, pick, j, smallerNote))
		moved[landsIn(pick, j).ID]++
	}

	for _, p := range pools {
		pp := plans[p.ID]
		if !servesAnyClass(p) || pp == nil || pp.Unserved <= 0 {
			continue
		}
		queued := demand[p.ID]
		// The runners that exist or are being made go to the oldest jobs. The
		// jobs left over are the youngest of those that drove the pool's demand.
		served := max(0, pp.eligible-pp.Unserved)
		if served >= len(queued) {
			continue
		}
		for _, j := range queued[served:min(pp.eligible, len(queued))] {
			if !j.SizeClass.Valid() || !j.RoutedClass.Valid() || j.SizeBasis == store.SizeBasisExplicit ||
				slices.ContainsFunc(out, func(r Reroute) bool { return r.Job == j }) {
				continue
			}
			waited := in.Now.Sub(waitingSince(j))
			if waited < in.Config.FallbackWait {
				continue
			}
			for _, k := range classOrder(j.SizeClass) {
				if k == j.RoutedClass || (k.Rank() < j.SizeClass.Rank() && !(j.SizeFloorMB > 0 && holds(k, j, j.SizeFloorMB))) {
					continue
				}
				// Room is asked of the pool the job would really land in, not of
				// whichever pool of the class has some: the claim does not look
				// at room when it chooses.
				target := landsIn(k, j)
				if target == nil || spare(target) <= 0 {
					continue
				}
				moved[target.ID]++
				move(j, j.RoutedClass, k, noRoomNote(j, k, waited))
				break
			}
		}
	}
	return out
}

// waitingSince is when a job became something the fleet could act on.
func waitingSince(j *store.Job) time.Time {
	if j.EligibleAt != nil {
		return *j.EligibleAt
	}
	return j.QueuedAt
}

// noPoolNote is the sentence for a job sent on because its class has no pool.
func noPoolNote(from, to store.SizeClass, j *store.Job, below bool) string {
	switch {
	case below:
		return fmt.Sprintf("there is no %s pool and nothing larger, so it was sent to %s, which is smaller than it is known to need%s; add a %s host",
			from, to, needPhrase(j.SizeFloorMB), j.SizeClass)
	case to.Rank() < j.SizeClass.Rank():
		return fmt.Sprintf("there is no %s pool, so it was sent to %s: the memory it needs%s fits there", from, to, needPhrase(j.SizeFloorMB))
	}
	return fmt.Sprintf("there is no %s pool (no %s host is enrolled), so it was sent to %s, the next size up", from, from, to)
}

// noRoomNote is the sentence for a job sent on because the class it was in had
// no room for it for as long as the wait.
func noRoomNote(j *store.Job, to store.SizeClass, waited time.Duration) string {
	head := fmt.Sprintf("no %s host had room for it for %s, so it ", j.RoutedClass, formatDuration(waited.Round(time.Second)))
	switch {
	case to == j.SizeClass:
		return head + fmt.Sprintf("went back to %s, its own class, which has room", to)
	case to.Rank() < j.SizeClass.Rank():
		return head + fmt.Sprintf("was allowed on %s, which still holds the %s it needs", to, formatMB(j.SizeFloorMB))
	}
	return head + fmt.Sprintf("was allowed on %s", to)
}

func needPhrase(floorMB int64) string {
	if floorMB <= 0 {
		return ""
	}
	return " (about " + formatMB(floorMB) + ")"
}

// servesClass reports whether an enabled pool answers the jobs routed to a class:
// the pool the controller keeps for it, or a pool an operator made that carries
// the class's label, which is the operator saying that pool is for that size.
//
// The second half matters because the controller makes no pool for a class an
// operator's pool already answers to -- it raises auto_pool.label_taken and
// leaves the class to them -- and a job routed there would otherwise be taken to
// have no pool and sent to a larger class, past the pool its own operator made
// for it, with a note saying no pool exists.
func servesClass(p *store.Pool, class store.SizeClass) bool {
	if p == nil || !p.Enabled {
		return false
	}
	if p.FromHosts() {
		_, k, ok := store.ParseAutoKey(p.AutoKey)
		return ok && k == class
	}
	return slices.ContainsFunc(p.Labels, func(l string) bool {
		k, ok := store.ClassOfLabel(l)
		return ok && k == class
	})
}

// servesAnyClass reports whether servesClass is true of a pool for some class.
func servesAnyClass(p *store.Pool) bool {
	for _, k := range store.SizeClasses() {
		if servesClass(p, k) {
			return true
		}
	}
	return false
}
