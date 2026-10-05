package scheduler

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/store"
)

// The three things label advice says about a job's runs-on. They are ordered by
// how much it costs to leave them: a job asking for too little is killed, a job
// that is only routed best effort may land on a host too small for it, and a job
// asking for too much only occupies a bigger machine than it needs.
const (
	// AdviceTooSmall: the job asks for a class by name and its runs call for a
	// larger one. The label is a guarantee, so the job can only ever run on hosts
	// that are too small for it, and is killed when it needs more than they have.
	AdviceTooSmall = "too_small"
	// AdviceUnguaranteed: the job asks only for the base label and its runs call
	// for a class above the default one. It is routed there while there is room,
	// which is best effort -- GitHub decides which waiting job a runner takes --
	// so it can land on a smaller host; naming the class makes it a guarantee.
	AdviceUnguaranteed = "unguaranteed"
	// AdviceTooLarge: the job asks for a class by name and its runs would fit a
	// smaller one, so it occupies a host another job needs.
	AdviceTooLarge = "too_large"
)

// AdviceMinRuns is how many measured runs a job needs before anything is said
// about its labels. A class worked out from one run is a guess, and advice to
// rewrite a workflow is not something to put on the strength of one.
const AdviceMinRuns = 5

// Advice is one thing to change in one job's runs-on, and why.
type Advice struct {
	Repo     string `json:"repo"`
	Workflow string `json:"workflow"`
	JobName  string `json:"job_name"`
	Kind     string `json:"kind"`
	// Asked is the class the job names in its runs-on, empty when it names none,
	// and Class the class its runs call for.
	Asked store.SizeClass `json:"asked,omitempty"`
	Class store.SizeClass `json:"class"`
	// Runs is how many measured runs the class was worked out from.
	Runs int `json:"runs"`
	// Labels is the runs-on the job had on its latest measured run.
	Labels []string `json:"labels"`
	// Message says what is wrong, and Fix what to write instead.
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// LabelAdvice says what, if anything, a job's workflow should change in its
// runs-on, given the class kept from the job's runs and what its latest measured
// run asked for.
//
// It says nothing about a job an operator has pinned, because the pin is a
// decision that takes the place of the measurements; about a job that asks for
// something this fleet's automatic pools do not answer, because advice to add a
// class label to it would send it away from the pool that carries its own; or
// about a job with too few runs to be worked out from.
func (c SizeConfig) LabelAdvice(k *store.JobClassAsked, pins []*store.SizePin) *Advice {
	if k == nil || !k.Class.Valid() || k.Basis != store.SizeBasisHistory || k.Runs < AdviceMinRuns || len(k.Labels) == 0 {
		return nil
	}
	for _, p := range pins {
		if p.Repo == lowerRepo(k.Repo) && (p.ForRepository() || (p.Workflow == k.Workflow && p.JobName == k.JobName)) {
			return nil
		}
	}
	out := &Advice{Repo: k.Repo, Workflow: k.Workflow, JobName: k.JobName, Class: k.Class, Runs: k.Runs, Labels: append([]string(nil), k.Labels...)}
	asked, named := RequestedClass(k.Labels)
	switch {
	case named && k.Class.Rank() > asked.Rank():
		out.Kind, out.Asked = AdviceTooSmall, asked
		out.Message = fmt.Sprintf("its runs-on asks for %s, which only a %s host answers, and its runs call for %s: %s.",
			asked.Label(), asked, k.Class, k.Reason)
		out.Fix = fmt.Sprintf("write %s in runs-on in place of %s. Left as it is, the job can only run on hosts smaller than it needs, and is killed when it uses more than they have.",
			k.Class.Label(), asked.Label())
	case named && k.Class.Rank() < asked.Rank():
		out.Kind, out.Asked = AdviceTooLarge, asked
		out.Message = fmt.Sprintf("its runs-on asks for %s, and its runs would fit a %s runner: %s.",
			asked.Label(), k.Class, k.Reason)
		out.Fix = fmt.Sprintf("write %s in runs-on in place of %s to leave the larger hosts to jobs that need them, unless the job needs the host for something that is not measured, such as its disk or its network.",
			k.Class.Label(), asked.Label())
	case !named && baseLabelsOnly(k.Labels) && k.Class.Rank() > c.defaultClass().Rank():
		out.Kind = AdviceUnguaranteed
		out.Message = fmt.Sprintf("its runs-on asks only for %s, so it is sent to %s while there is room, which is best effort and not a promise: GitHub decides which waiting job a runner takes, so it can land on a smaller host. %s.",
			store.BrandLabel, k.Class, capitalise(k.Reason))
		out.Fix = fmt.Sprintf("add %s to runs-on, beside %s, to make it a guarantee.", k.Class.Label(), store.BrandLabel)
	default:
		return nil
	}
	return out
}

// baseLabelsOnly reports whether a job asks for nothing but what every runner
// carries and the brand label. A job that names another label asks for a pool of
// somebody's own, which the automatic pools do not answer.
func baseLabelsOnly(labels []string) bool {
	for _, l := range store.NormalizeLabels(labels) {
		if !store.ImplicitLabels[l] && l != store.BrandLabel {
			return false
		}
	}
	return true
}

// lowerRepo is a repository as a pin stores it, which is how GitHub compares one.
func lowerRepo(r string) string { return strings.ToLower(strings.TrimSpace(r)) }

// capitalise starts a clause that was written to follow "because" as a sentence.
func capitalise(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// SortAdvice puts the advice that costs most to leave first, then orders it by
// repository, workflow and job, so a page of it is stable between reads.
func SortAdvice(in []*Advice) {
	rank := map[string]int{AdviceTooSmall: 0, AdviceUnguaranteed: 1, AdviceTooLarge: 2}
	sort.SliceStable(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if rank[a.Kind] != rank[b.Kind] {
			return rank[a.Kind] < rank[b.Kind]
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		if a.Workflow != b.Workflow {
			return a.Workflow < b.Workflow
		}
		return a.JobName < b.JobName
	})
}
