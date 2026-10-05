package scheduler

import (
	"slices"
	"sort"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// The modes of scheduler.size_routing and scheduler.auto_pools.
const (
	// SizeOff does nothing: no host is given a class, no job is classified, no
	// pool is made. It is what every fleet has until an operator says otherwise,
	// and it is why turning the feature on is the only thing that changes it.
	SizeOff = "off"
	// SizeShadow works everything out and records it -- a host's class, a job's
	// class and where it would have gone -- and acts on none of it, so an
	// operator reads what the feature would have done to their own fleet before
	// it does it.
	SizeShadow = "shadow"
	// SizeOn acts.
	SizeOn = "on"
)

// SizeModes is every value either setting accepts, in the order the settings
// page offers them.
var SizeModes = []string{SizeOff, SizeShadow, SizeOn}

// ValidSizeMode reports whether s is a mode. The empty string is off, so a
// setting that was never written is valid.
func ValidSizeMode(s string) bool { return s == "" || slices.Contains(SizeModes, s) }

// HostLimit is the most CPU and memory a host may have allocatable and still
// be in a class.
type HostLimit struct {
	CPUs     float64
	MemoryMB int64
}

// SizeConfig is scheduler.size_* gathered for the pure functions, which cannot
// read settings.
type SizeConfig struct {
	// SmallMax is the largest a small host is, and MediumMax the largest a
	// medium one is; anything above MediumMax is large. A host is in the lower
	// of the classes its CPU and its memory each name, because a machine with
	// the cores of a large host and the memory of a small one runs out of memory
	// at a small host's pace.
	SmallMax, MediumMax HostLimit
	// Small, Medium and Large are the size of one runner in each class, for a
	// host whose profile says nothing about its own. They are what a job is
	// measured against when it is classed -- a job is as big as the smallest
	// runner that holds it -- and what a runner in an automatic pool is given.
	Small, Medium, Large store.RunnerSize
	// Hold is how long a host's measurements must name a different class
	// before the host moves to it.
	Hold time.Duration
	// DefaultClass is the class of a job nothing is known about.
	DefaultClass store.SizeClass
	// FallbackWait is how long a job waits for room in its own class before
	// another is allowed to take it.
	FallbackWait time.Duration
}

// DefaultSizeConfig is the figures the settings default to. They are written
// down once, here, and the config package's defaults are held equal to them by
// a test in the controller, which sees both.
//
// The runner in each class is chosen to tile the hosts in it: a small host is
// at most four cores, so a runner of one holds three on it, and a large one is
// anything above twelve, so a runner of four holds three on a sixteen-core
// machine and seven on a thirty-two-core one. The middle class is the size
// the fleet's own default runner already is, which is why it is where a job
// nothing is known about starts: it gets the runner it would have had before
// there were classes, and moves as its history says.
func DefaultSizeConfig() SizeConfig {
	return SizeConfig{
		SmallMax:     HostLimit{CPUs: 4, MemoryMB: 16 * 1024},
		MediumMax:    HostLimit{CPUs: 12, MemoryMB: 48 * 1024},
		Small:        store.RunnerSize{CPUs: 1, MemoryMB: 2 * 1024},
		Medium:       store.RunnerSize{CPUs: 2, MemoryMB: 4 * 1024},
		Large:        store.RunnerSize{CPUs: 4, MemoryMB: 8 * 1024},
		Hold:         10 * time.Minute,
		DefaultClass: store.SizeMedium,
		FallbackWait: 2 * time.Minute,
	}
}

// Runner is the size of one runner in a class, and zero for something that is
// not one.
func (c SizeConfig) Runner(class store.SizeClass) store.RunnerSize {
	switch class {
	case store.SizeSmall:
		return c.Small
	case store.SizeMedium:
		return c.Medium
	case store.SizeLarge:
		return c.Large
	}
	return store.RunnerSize{}
}

// bracket is the class a figure falls in, given the two limits between them.
func bracket(v, smallMax, mediumMax float64) store.SizeClass {
	switch {
	case v <= smallMax:
		return store.SizeSmall
	case v <= mediumMax:
		return store.SizeMedium
	}
	return store.SizeLarge
}

// HostClass is the class a host's allocatable CPU and memory say it is, and
// false for a host that has measured neither, which has no class to be in.
// Allocatable is what is left after the reserve, so an operator who holds back
// half a machine for something else is describing a smaller host than the one
// the agent measured.
func (c SizeConfig) HostClass(h *store.Host) (store.SizeClass, bool) {
	if h == nil {
		return "", false
	}
	a := h.Allocatable()
	var out store.SizeClass
	lower := func(k store.SizeClass) {
		if out == "" || k.Rank() < out.Rank() {
			out = k
		}
	}
	if a.CPUsKnown {
		lower(bracket(a.CPUs-cpuEpsilon, c.SmallMax.CPUs, c.MediumMax.CPUs))
	}
	if a.MemoryKnown {
		lower(bracket(float64(a.MemoryMB), float64(c.SmallMax.MemoryMB), float64(c.MediumMax.MemoryMB)))
	}
	return out, out != ""
}

// NextHostClass is one look at a host: what its held class should be now, given
// what the measurements say, and whether that differs from what was held.
//
// The first look puts a host in the class its measurements name at once -- a
// host that has just joined should be taking work, not waiting out a hold -- and
// every later change waits for the measurements to have named the new class for
// hold, unbroken. A reading that goes back to the held class clears the wait,
// so a host sitting on a threshold, which measures one side of it and then the
// other, never moves at all. measured is "" for a host that could not be
// measured, and leaves everything as it was.
func NextHostClass(held store.HostSizeClass, measured store.SizeClass, now time.Time, hold time.Duration) (store.HostSizeClass, bool) {
	if !measured.Valid() {
		return held, false
	}
	next := held
	switch {
	case held.Class == "":
		next = store.HostSizeClass{Class: measured, ChangedAt: &now}
	case measured == held.Class:
		next.Pending, next.PendingSince = "", nil
	case hold <= 0:
		next = store.HostSizeClass{Class: measured, ChangedAt: &now}
	case held.Pending != measured || held.PendingSince == nil:
		next.Pending, next.PendingSince = measured, &now
	case now.Sub(*held.PendingSince) >= hold:
		next = store.HostSizeClass{Class: measured, ChangedAt: &now}
	}
	return next, !sameHostClass(held, next)
}

// sameHostClass compares two readings by what they say, not by where their
// times are stored.
func sameHostClass(a, b store.HostSizeClass) bool {
	return a.Class == b.Class && a.Pending == b.Pending &&
		timeEqual(a.PendingSince, b.PendingSince) && timeEqual(a.ChangedAt, b.ChangedAt)
}

func timeEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// Where a host's tag came from.
const (
	// TagOperator is a tag an operator wrote, which is a host label.
	TagOperator = "operator"
	// TagAutomatic is a tag worked out from the host: what the agent reports
	// of the machine, and the class the controller holds it in.
	TagAutomatic = "automatic"
)

// Tag is one fact about a host that a pool's host_selector can ask for.
//
// Tags are not a second set of labels: an operator's tag is a label, and an
// automatic one is what Host.SelectorValue already answers for the same key.
// This is the two of them listed together, with which is which, because the
// Hosts page has to say what an operator can change and what they cannot.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Source is TagOperator or TagAutomatic.
	Source string `json:"source"`
	// Overrides is the value the host would have had automatically, set when
	// an operator's tag replaced it with a different one.
	Overrides string `json:"overrides,omitempty"`
}

// HostTags lists a host's tags, sorted by key: every label an operator put on
// it, and the automatic ones no label has taken the place of.
//
// The class is listed only when size is true -- the controller has been asked
// to work classes out -- so a fleet that has not turned the feature on sees no
// new tag on any host.
func HostTags(h *store.Host, size bool) []Tag {
	auto := map[string]string{}
	if h.OS != "" {
		auto[store.LabelOS] = h.OS
	}
	if h.Arch != "" {
		auto[store.LabelArch] = h.Arch
	}
	if size && h.SizeClass.Set() {
		auto[store.LabelSize] = string(h.SizeClass.Class)
	}
	var out []Tag
	for k, v := range h.Labels {
		t := Tag{Key: k, Value: v, Source: TagOperator}
		if a, ok := auto[k]; ok && a != v {
			t.Overrides = a
		}
		out = append(out, t)
	}
	for k, v := range auto {
		if _, taken := h.Labels[k]; !taken {
			out = append(out, Tag{Key: k, Value: v, Source: TagAutomatic})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
