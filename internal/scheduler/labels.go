package scheduler

import (
	"fmt"
	"slices"

	"github.com/eyupio/zoomies/internal/store"
)

// osLabels and archLabels split store.ImplicitLabels into the two dimensions a
// pool can genuinely contradict. "self-hosted" belongs to neither: every pool
// in Zoomies is self-hosted, so a job asking for it constrains nothing.
var (
	osLabels   = map[string]bool{"linux": true, "windows": true, "macos": true}
	archLabels = map[string]bool{"x64": true, "arm": true, "arm64": true}
)

const (
	// noMatch is the score of a pool that cannot run a job at all. It is
	// negative so that callers can sort scores and reject anything below zero.
	noMatch = -1
	// exactScore is what a pool scores when it advertises precisely the labels
	// the job asked for. Surplus labels count down from here, which keeps every
	// matching score non-negative for any sane label set.
	exactScore = 1 << 16
)

// Matches reports whether a pool advertising poolLabels may run a job whose
// runs-on listed jobLabels.
//
// The rule mirrors what GitHub itself does. Labels every actions/runner binary
// advertises (store.ImplicitLabels) do not constrain pool selection, because
// "runs-on: [self-hosted, linux, x64]" is how nearly every workflow is written
// and it is not a request for a particular pool. The one exception is a pool
// that declares its own os or arch: that is a promise about the machine, so a
// job asking for a different one must not land there.
func Matches(poolLabels, jobLabels []string) bool {
	return matches(store.NormalizeLabels(poolLabels), store.NormalizeLabels(jobLabels), true)
}

// PoolMatches is Matches for a whole pool, which knows whether its runners
// advertise the implicit labels at all. A pool registered with
// --no-default-labels carries exactly the labels it lists, so a job asking
// for self-hosted or linux must find those among them: GitHub would never
// offer that job to such a runner, and a runner started for it would sit
// idle while the job waited.
func PoolMatches(p *store.Pool, jobLabels []string) bool {
	return matches(store.NormalizeLabels(p.Labels), store.NormalizeLabels(jobLabels), !p.NoDefaultLabels)
}

// matches is Matches over already-normalised label sets. implicit says
// whether the pool's runners advertise store.ImplicitLabels.
func matches(pool, job []string, implicit bool) bool {
	for _, l := range job {
		if implicit && store.ImplicitLabels[l] {
			continue
		}
		if !slices.Contains(pool, l) {
			return false
		}
	}
	return !contradicts(pool, job, osLabels) && !contradicts(pool, job, archLabels)
}

// contradicts reports whether the job asked for a label from dim that the pool
// lacks while declaring a different label from the same dimension. A pool that
// declares nothing in a dimension makes no promise and contradicts nothing.
func contradicts(pool, job []string, dim map[string]bool) bool {
	declares := slices.ContainsFunc(pool, func(l string) bool { return dim[l] })
	if !declares {
		return false
	}
	return slices.ContainsFunc(job, func(l string) bool {
		return dim[l] && !slices.Contains(pool, l)
	})
}

// Score ranks how well a pool fits a job. Higher is better; a negative score
// means the pool cannot run the job at all.
//
// Every matching pool satisfies all of the job's explicit labels by
// construction, so the only thing left to rank on is surplus: capability the
// job never asked for. Preferring the smallest surplus keeps a specialised
// pool (say gpu + cuda12) free for the jobs that actually need it.
func Score(poolLabels, jobLabels []string) int {
	return score(poolLabels, jobLabels, true)
}

// score is Score for a pool that may not advertise the implicit labels.
func score(poolLabels, jobLabels []string, implicit bool) int {
	pool := store.NormalizeLabels(poolLabels)
	job := store.NormalizeLabels(jobLabels)
	if !matches(pool, job, implicit) {
		return noMatch
	}
	surplus := 0
	for _, l := range pool {
		if !slices.Contains(job, l) {
			surplus++
		}
	}
	return max(exactScore-surplus, 0)
}

// Eligible reports whether a pool may run a job at all, and when it may not,
// says why in the operator's words.
//
// The three tests are asked in the order an operator would ask them. A
// disabled pool was disabled on purpose. A pool belonging to another GitHub
// App installation cannot run the job whatever its labels say: Zoomies would
// mint the runner's registration in the wrong GitHub target, and the job would
// sit queued while a runner waited for it somewhere it can never be offered.
// Labels come last, because they are the only one of the three an operator
// changes by editing a workflow.
func Eligible(p *store.Pool, j *store.Job) (bool, string) {
	switch {
	case p == nil || j == nil:
		return false, ""
	case !p.Enabled:
		return false, "the pool is disabled"
	case j.InstallationID == "":
		return false, "no GitHub App installation here covers that repository"
	case p.InstallationID != j.InstallationID:
		return false, "the pool belongs to another GitHub App installation"
	case !PoolMatches(p, j.Labels):
		return false, "the pool does not advertise those labels"
	}
	return true, ""
}

// BestPool returns the pool that best fits a job, or nil when no pool is
// eligible for it -- which the caller surfaces as a configuration problem
// rather than silently dropping the job.
//
// Ties break on pool name and then pool ID, so the same job always lands in
// the same pool. Anything else would make the controller disagree with itself
// across restarts, and make a scaling decision impossible to explain.
func BestPool(pools []*store.Pool, j *store.Job) *store.Pool {
	best, _ := bestPool(pools, j, nil)
	return best
}

// bestPool is BestPool that also explains an empty answer, given the target
// each installation manages so that the explanation can name one.
//
// The explanation names the nearest miss rather than every pool's objection.
// A job whose labels match a pool in another installation is a different
// problem from one whose labels match nothing here -- the first is a pool on
// the wrong installation, the second is a workflow asking for a machine this
// fleet does not offer -- and only the first is worth a sentence, because the
// second is what the Jobs page already says.
func bestPool(pools []*store.Pool, j *store.Job, targets map[string]string) (*store.Pool, string) {
	var best *store.Pool
	bestScore := noMatch
	var elsewhere *store.Pool
	for _, p := range pools {
		if p == nil {
			continue
		}
		if ok, _ := Eligible(p, j); !ok {
			// A pool that would take the job but for its installation is the
			// near miss worth reporting. Ties break the same way matches do,
			// so the sentence does not change between passes.
			if p.Enabled && PoolMatches(p, j.Labels) &&
				(elsewhere == nil || lessPool(p, elsewhere)) {
				elsewhere = p
			}
			continue
		}
		s := score(p.Labels, j.Labels, !p.NoDefaultLabels)
		if s < 0 {
			continue
		}
		if best == nil || s > bestScore || (s == bestScore && lessPoolFor(j, p, best)) {
			best, bestScore = p, s
		}
	}
	if best != nil || elsewhere == nil {
		return best, ""
	}
	if j.InstallationID == "" {
		return nil, fmt.Sprintf("its labels match pool %s, but no GitHub App installation here covers %s", elsewhere.Name, j.Repo)
	}
	return nil, fmt.Sprintf("its labels match pool %s, which belongs to installation %s",
		elsewhere.Name, installationName(elsewhere.InstallationID, targets))
}

// installationName prefers the target an installation manages -- the
// organisation or repository an operator recognises -- and falls back to the
// identifier when the caller did not supply the targets.
func installationName(id string, targets map[string]string) string {
	if t := targets[id]; t != "" {
		return t
	}
	return id
}

// lessPoolFor is lessPool for a job that has been routed to a size class: of
// two pools that fit it equally, the one in the class it is routed to comes
// first, then a larger class and then a smaller one, nearest first, and among
// the pools of one class the architecture that is the default.
//
// A pool an operator made has no class, and sits between the pool in the right
// class and every other: where it fits a job exactly as well as the pools the
// controller keeps, it was not made for a class and has no reason to lose to
// one made for the wrong one. A job that has not been routed, which is every job
// while size routing is off, is ordered exactly as it always was.
func lessPoolFor(j *store.Job, a, b *store.Pool) bool {
	if !j.RoutedClass.Valid() {
		return lessPool(a, b)
	}
	if ra, rb := classPreference(j.RoutedClass, a), classPreference(j.RoutedClass, b); ra != rb {
		return ra < rb
	}
	if aa, ab := a.FromHosts() && a.Platform.Arch != "amd64", b.FromHosts() && b.Platform.Arch != "amd64"; aa != ab {
		return ab
	}
	return lessPool(a, b)
}

// classPreference ranks a pool for a job routed to want: 0 for the pool in that
// class, 1 for a pool no class was made for, then the larger classes in order
// and then the smaller ones.
func classPreference(want store.SizeClass, p *store.Pool) int {
	if !p.FromHosts() {
		return 1
	}
	_, have, ok := store.ParseAutoKey(p.AutoKey)
	if !ok {
		return 1
	}
	switch d := have.Rank() - want.Rank(); {
	case d == 0:
		return 0
	case d > 0:
		return 1 + d
	default:
		return 10 - d
	}
}

// lessPool is the total order used wherever two pools would otherwise tie.
func lessPool(a, b *store.Pool) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}
