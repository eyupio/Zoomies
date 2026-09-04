package scheduler

import (
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
	return matches(store.NormalizeLabels(poolLabels), store.NormalizeLabels(jobLabels))
}

// matches is Matches over already-normalised label sets.
func matches(pool, job []string) bool {
	for _, l := range job {
		if store.ImplicitLabels[l] {
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
	pool := store.NormalizeLabels(poolLabels)
	job := store.NormalizeLabels(jobLabels)
	if !matches(pool, job) {
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

// BestPool returns the enabled pool that best fits jobLabels, or nil when no
// pool claims the job -- which the caller surfaces as a configuration problem
// rather than silently dropping the job. Disabled pools are skipped, because
// an operator disables a pool precisely so that it stops taking work.
//
// Ties break on pool name and then pool ID, so the same job always lands in
// the same pool. Anything else would make the controller disagree with itself
// across restarts, and make a scaling decision impossible to explain.
func BestPool(pools []*store.Pool, jobLabels []string) *store.Pool {
	job := store.NormalizeLabels(jobLabels)
	var best *store.Pool
	bestScore := noMatch
	for _, p := range pools {
		if p == nil || !p.Enabled {
			continue
		}
		s := Score(p.Labels, job)
		if s < 0 {
			continue
		}
		if best == nil || s > bestScore || (s == bestScore && lessPool(p, best)) {
			best, bestScore = p, s
		}
	}
	return best
}

// lessPool is the total order used wherever two pools would otherwise tie.
func lessPool(a, b *store.Pool) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}
