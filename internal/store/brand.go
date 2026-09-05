package store

import "strings"

// The brand, as it appears on GitHub.
//
// Two names cross the boundary into somebody else's account: the name of every
// runner Zoomies registers, and the labels a workflow's `runs-on` has to spell
// out. Both are read far more often than they are written -- in the runner list
// on GitHub, in a job's "Set up job" step, in every workflow file in the
// organisation -- so both are short, both start with the product name, and
// neither carries anything a reader would have to decode.
const (
	// Brand is the product name, lowercased, as it appears in a label.
	Brand = "zoomies"

	// BrandLabel is the one label every Zoomies pool answers to. It is what
	// makes `runs-on: zoomies` mean "any runner this fleet has", which is the
	// line a repository can adopt before anyone has decided which pool it
	// belongs in.
	BrandLabel = Brand

	// BrandPrefix is what every name this fleet puts in front of somebody
	// else's eyes starts with: a pool's name, and the runner names derived
	// from it.
	BrandPrefix = Brand + "-"

	// RunnerNamePrefix is what every name NewRunnerName mints starts with. The
	// reaper uses it to tell a registration Zoomies created from one somebody
	// else registered by hand, so it must not appear in front of anything else.
	RunnerNamePrefix = BrandPrefix

	// runnerNameEntropy is how many random bytes go into a runner name. Five
	// encodes to eight base32 characters, which is a name a person can read
	// back over a call and still leaves a collision within one target
	// vanishingly unlikely.
	runnerNameEntropy = 5

	// maxLabelSegment bounds the part of a label derived from something an
	// operator typed. GitHub allows longer, but a label nobody can read at a
	// glance defeats the point of branding it.
	maxLabelSegment = 40
)

// NewRunnerName mints the name one runner registers under: "zoomies-" and
// eight random characters.
//
// It deliberately says nothing else. GitHub shows this name in the runner list
// and in every job's log header, where the useful facts -- which pool, which
// host, which job -- are either already on screen or one click away in Zoomies.
// Encoding them in the name made it long enough that the brand was the part
// that got truncated.
func NewRunnerName() string { return RunnerNamePrefix + NewSecret(runnerNameEntropy) }

// IsRunnerName reports whether name looks like one NewRunnerName minted, which
// is as much as anything outside this package can know about a registration it
// finds on GitHub.
func IsRunnerName(name string) bool {
	return strings.HasPrefix(NormalizeLabel(name), RunnerNamePrefix)
}

// BrandLabels returns labels with BrandLabel guaranteed present, normalised.
//
// Adding it is not cosmetic: it is what lets a workflow ask for this fleet
// without naming one of its pools, and what the migration wizard rewrites
// `runs-on: ubuntu-latest` to when the operator has not chosen a pool. A pool
// that did not answer to it would be invisible to both.
func BrandLabels(in []string) []string {
	return NormalizeLabels(append([]string{BrandLabel}, in...))
}

// BrandedLabel turns a pool name into the label a workflow writes: the brand,
// then the name with everything GitHub dislikes replaced by a hyphen.
//
// A pool already named for the brand is returned as it is, so that naming a
// pool "zoomies-gpu" does not produce "zoomies-zoomies-gpu".
func BrandedLabel(name string) string {
	s := SanitizeLabel(name)
	switch {
	case s == "":
		return BrandLabel
	case s == Brand || strings.HasPrefix(s, RunnerNamePrefix):
		return s
	default:
		return RunnerNamePrefix + s
	}
}

// BrandedName returns a pool name carrying the brand, which every pool name
// has to.
//
// A pool's name is not private to Zoomies. It is what BrandedLabel turns into
// the label a workflow's `runs-on` spells out, it is the word in an audit line
// somebody reads months later, and it is how the pool is named in the runner
// list of an account that also has runners nobody here registered. A pool
// called "gpu" makes all of those say something that could have come from
// anywhere, and renaming it afterwards does not un-write the workflows already
// pointing at it -- so a name arriving without the brand gains it here rather
// than being refused, which is the treatment BrandLabels already gives a label
// list, for the same reason.
//
// An empty name is returned unchanged: "a pool needs a name" is a better thing
// for an operator to be told than a pool called "zoomies-".
func BrandedName(name string) string {
	s := strings.TrimSpace(name)
	if s == "" || IsBrandedName(s) {
		return s
	}
	return BrandPrefix + strings.TrimLeft(s, "-")
}

// IsBrandedName reports whether name already carries the brand, comparing the
// way GitHub compares labels so that "Zoomies-GPU" is not given a second one.
func IsBrandedName(name string) bool {
	s := NormalizeLabel(name)
	return s == Brand || strings.HasPrefix(s, BrandPrefix)
}

// SanitizeLabel reduces an arbitrary string to the characters GitHub accepts in
// a runner label: lowercase letters, digits and hyphens, with no hyphen at
// either end.
func SanitizeLabel(s string) string {
	s = NormalizeLabel(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(collapseHyphens(b.String()), "-")
	if len(out) > maxLabelSegment {
		out = strings.Trim(out[:maxLabelSegment], "-")
	}
	return out
}

// collapseHyphens turns runs of hyphens into one, so that "Ubuntu 24.04"
// sanitises to "ubuntu-24-04" rather than "ubuntu-24-04" with a gap where the
// space and the dot both became hyphens.
func collapseHyphens(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prev := rune(0)
	for _, r := range s {
		if r == '-' && prev == '-' {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// RunsOn renders the shortest `runs-on` value that reaches a pool advertising
// these labels.
//
// A pool with one label of its own is reached by that label alone, which is the
// whole reason the labels are branded: "runs-on: zoomies-linux-x64" says where
// the job runs, and a reviewer of the pull request that introduced it does not
// have to know what "self-hosted, linux, x64" resolves to in this
// organisation. Where a pool really does need several labels to be identified,
// the list form is rendered instead, because dropping one would send jobs
// somewhere else.
func RunsOn(labels []string) string {
	var specific []string
	for _, l := range NormalizeLabels(labels) {
		if ImplicitLabels[l] || l == BrandLabel {
			continue
		}
		specific = append(specific, l)
	}
	switch len(specific) {
	case 1:
		return specific[0]
	case 0:
		// Nothing but the brand and the labels every self-hosted runner
		// already has. The brand still names this fleet; "self-hosted" is the
		// last resort and matches any of them.
		for _, l := range NormalizeLabels(labels) {
			if l == BrandLabel {
				return BrandLabel
			}
		}
		return "self-hosted"
	default:
		return "[" + strings.Join(specific, ", ") + "]"
	}
}
