package scheduler

import (
	"math"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// Figures are three points of one dimension of what a job's runs used: the
// median, the figure all but the heaviest twentieth stayed under, and the most.
type Figures struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	Max float64 `json:"max"`
}

// Observed is what a job's measured runs did, as figures a person can check
// the class against. It is the measurement and nothing else: a run the kernel
// killed appears at the peak it recorded, not at the figure the class rule
// treats it as having needed, because a reader who sees a max above every
// recorded peak would rightly stop trusting the page.
type Observed struct {
	// Runs is how many runs the figures were taken from, which is bounded by
	// the window and the history limit and so can differ from the count the
	// class was kept with.
	Runs int `json:"runs"`
	// Window is the span the runs were read from. The controller renders it
	// once on the page rather than on every row.
	Window   time.Duration `json:"-"`
	CPU      Figures       `json:"cpu"`
	MemoryMB Figures       `json:"memory_mb"`
}

// Observe turns a job's measured runs into the figures behind its advice: the
// nearest-rank percentiles of their peaks, the same rank rule the class is
// decided by, so the p95 here and the p90 in the class's reason agree on what
// a percentile means. A run that measured nothing in a dimension contributes
// nothing to that dimension; a zero would pull the median down to a figure no
// run showed. The zero value is "no runs".
func Observe(runs []store.JobRun) Observed {
	out := Observed{Runs: len(runs)}
	var cpus, mem []float64
	for _, r := range runs {
		if r.PeakCPUs > 0 {
			cpus = append(cpus, r.PeakCPUs)
		}
		if r.PeakMemoryMB > 0 {
			mem = append(mem, float64(r.PeakMemoryMB))
		}
	}
	out.CPU = figuresOf(cpus)
	out.MemoryMB = figuresOf(mem)
	return out
}

// figuresOf is the nearest-rank p50, p95 and max of samples, zero for none.
func figuresOf(samples []float64) Figures {
	if len(samples) == 0 {
		return Figures{}
	}
	slices.Sort(samples)
	n := len(samples)
	return Figures{
		P50: samples[nearestRank(0.5, n)],
		P95: samples[nearestRank(0.95, n)],
		Max: samples[n-1],
	}
}

// nearestRank is the index of percentile p in n sorted samples by the
// nearest-rank method: the smallest sample that at least p of them are at or
// under. It is percentileIndex with the percentile as an argument.
func nearestRank(p float64, n int) int {
	return clamp(int(math.Ceil(p*float64(n)))-1, 0, n-1)
}
