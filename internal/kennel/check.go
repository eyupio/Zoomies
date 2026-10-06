// Package kennel decides what is wrong with a repository, from the facts it is
// handed and from nothing else.
//
// It is pure on purpose, for the reason internal/scheduler is: no clock, no
// database, no network. A check that reads the world cannot be tested by
// describing a world, and a finding whose sentence came from somewhere other
// than this package cannot be trusted not to repeat what a stranger wrote into
// a repository. Both properties are tested: boundary_test.go fails if this
// package imports anything but the standard library or reads a clock, and the
// hostile-input test fails if repository text reaches a sentence.
//
// The question every check answers is "does this affect running CI, or the
// fleet that runs it?". A check that cannot say yes does not belong here.
package kennel

import (
	"slices"
	"strings"
)

// Version is the evaluator's version. A stored evaluation made by an older one
// is re-run, so bump it whenever a check's meaning or wording changes.
const Version = 1

// Code names one check. Codes are stable across releases and form a closed
// set: Evaluate returns no code that is not in the registry below.
type Code string

const (
	CodePublicRepoOnFleet  Code = "exposure.public_repo_on_fleet"
	CodePublicRepoWeakPool Code = "exposure.public_repo_weak_pool"
	CodeForkCodeRan        Code = "exposure.fork_code_ran"
	CodeTargetEventRan     Code = "exposure.target_event_ran"
	CodeUnservedLabel      Code = "capacity.unserved_label"
	CodeJobHitDefaultLimit Code = "capacity.job_hit_default_limit"
)

// Area groups codes for the operator, who can turn a whole area off.
type Area string

const (
	AreaExposure Area = "exposure"
	AreaCapacity Area = "capacity"
)

// Area is the part of a code before its dot.
func (c Code) Area() Area {
	area, _, _ := strings.Cut(string(c), ".")
	return Area(area)
}

// Severity says how much a finding matters. The values are the problem list's
// own, so the UI reuses its severity badge; a test holds them equal to
// config.Severity without this package importing it.
type Severity string

const (
	// SeverityError: a stranger or a fault can already hurt the fleet, or stop
	// CI.
	SeverityError Severity = "error"
	// SeverityWarning: a guard is weaker than it should be, or capacity is
	// being wasted.
	SeverityWarning Severity = "warning"
	// SeverityInfo: worth knowing, cheap to ignore. It never stops a repository
	// being best in show.
	SeverityInfo Severity = "info"
)

// rank orders severities; an unknown one ranks below them all.
func (s Severity) rank() int {
	switch s {
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	}
	return 0
}

// Check is one entry in the registry: what it is called, how severe it usually
// is, what it needs to have been read, and how to judge a snapshot.
type Check struct {
	Code Code `json:"code"`
	Area Area `json:"area"`
	// Severity is the usual one. A finding may be raised above it where a
	// second check makes it worse, and the catalogue's documentation says where.
	Severity Severity `json:"severity"`
	// Detects is one sentence for the catalogue and the documentation. The
	// sentences an operator reads about a particular repository are written
	// per finding, not here.
	Detects string `json:"detects"`
	// Needs are the sources every evaluation of this check must have read to
	// know whether it applies at all.
	Needs []Source `json:"needs"`

	eval func(*Snapshot) result
}

// result is what one check made of one snapshot.
type result struct {
	// applies is whether the repository is one the check has anything to say
	// about: a private repository is not a candidate for the public-exposure
	// checks, which is different from being clear of them.
	applies bool
	// extra are sources needed only once the check applies, such as the run
	// history, which is read for public repositories and no others.
	extra    []Source
	findings []Finding
}

// checks is the registry, in the order findings of equal severity are listed.
var checks = []Check{
	{
		Code: CodePublicRepoOnFleet, Area: AreaExposure, Severity: SeverityWarning,
		Detects: "A public repository ran jobs on this fleet, or has jobs waiting for it.",
		Needs:   []Source{SourceFleet, SourceMetadata},
		eval:    evalPublicRepoOnFleet,
	},
	{
		Code: CodePublicRepoWeakPool, Area: AreaExposure, Severity: SeverityError,
		Detects: "The pool that ran a public repository's jobs is persistent, mounts the host Docker socket, gives its jobs a privileged Docker daemon, runs as root or uses no container.",
		Needs:   []Source{SourceFleet, SourceMetadata},
		eval:    evalPublicRepoWeakPool,
	},
	{
		Code: CodeForkCodeRan, Area: AreaExposure, Severity: SeverityError,
		Detects: "A run from a fork's pull request executed on this fleet.",
		Needs:   []Source{SourceFleet, SourceMetadata},
		eval:    evalForkCodeRan,
	},
	{
		Code: CodeTargetEventRan, Area: AreaExposure, Severity: SeverityWarning,
		Detects: "A workflow that strangers can trigger (pull_request_target, workflow_run, issue_comment, issues) ran on this fleet in a public repository.",
		Needs:   []Source{SourceFleet, SourceMetadata},
		eval:    evalTargetEventRan,
	},
	{
		Code: CodeUnservedLabel, Area: AreaCapacity, Severity: SeverityWarning,
		Detects: "Jobs waited more than ten minutes for a label no pool serves.",
		Needs:   []Source{SourceFleet},
		eval:    evalUnservedLabel,
	},
	{
		Code: CodeJobHitDefaultLimit, Area: AreaCapacity, Severity: SeverityWarning,
		Detects: "A job ran until GitHub stopped it at its six-hour default limit.",
		Needs:   []Source{SourceFleet},
		eval:    evalJobHitDefaultLimit,
	},
}

// Checks returns the registry. The caller may keep and change the slice.
func Checks() []Check {
	out := make([]Check, len(checks))
	for i, c := range checks {
		c.Needs = append([]Source(nil), c.Needs...)
		out[i] = c
	}
	return out
}

// Lookup returns the check with this code.
func Lookup(c Code) (Check, bool) {
	for _, k := range Checks() {
		if k.Code == c {
			return k, true
		}
	}
	return Check{}, false
}

// Names lists every name kennel.disabled_checks accepts -- each area, then each
// code -- in registry order. The validator quotes it back to somebody who
// misspelt one, so the answer comes from the registry and not from a second
// list that could fall behind it.
func Names() []string {
	var out []string
	for _, c := range checks {
		if !slices.Contains(out, string(c.Area)) {
			out = append(out, string(c.Area))
		}
	}
	for _, c := range checks {
		out = append(out, string(c.Code))
	}
	return out
}

// KnownCodeOrArea says whether a name in kennel.disabled_checks means anything,
// so a misspelling is refused when it is saved rather than silently ignored.
func KnownCodeOrArea(name string) bool {
	for _, c := range checks {
		if string(c.Code) == name || string(c.Area) == name {
			return true
		}
	}
	return false
}
