package kennel

// Source is somewhere a check's facts come from. A check whose source cannot be
// read is skipped and says why; it is never judged against partial data.
type Source string

const (
	// SourceFleet is what this controller already knows: jobs, pools and
	// runner groups. It costs no GitHub request and is always readable.
	SourceFleet Source = "fleet"
	// SourceMetadata is the repository's own record: visibility above all.
	SourceMetadata Source = "metadata"
	// SourceRuns is the trigger and head repository of runs the fleet ran, read
	// for public repositories only.
	SourceRuns Source = "runs"
)

// CoverageState is how far a source could be read.
type CoverageState string

const (
	// CoverageOK: read in full.
	CoverageOK CoverageState = "ok"
	// CoveragePartial: a page cap was reached or the read was a sample. A
	// finding found in a partial read stands; the absence of one is not an
	// all-clear.
	CoveragePartial CoverageState = "partial"
	// CoverageDenied: GitHub refused for want of a permission the installation
	// has not been given.
	CoverageDenied CoverageState = "denied"
	// CoverageUnavailable: this GitHub does not offer it, or the repository
	// does not have it. Never a finding.
	CoverageUnavailable CoverageState = "unavailable"
	// CoverageHeld: reads are being held back for this installation, either
	// because GitHub rate-limited it or because Kennel Club has spent what its
	// budget allows this hour.
	CoverageHeld CoverageState = "held"
	// CoverageError: a transient failure, retried at the next refresh.
	CoverageError CoverageState = "error"
	// CoverageNotRead: nothing has tried yet.
	CoverageNotRead CoverageState = "not_read"
)

// readable is whether a state gives facts to judge. A partial read does.
func (s CoverageState) readable() bool { return s == CoverageOK || s == CoveragePartial }

// SourceState is the state of one source.
type SourceState struct {
	State CoverageState `json:"state"`
}

// Coverage is the state of every source a snapshot was assembled from. A source
// missing from it is NotRead.
type Coverage map[Source]SourceState

func (c Coverage) state(s Source) CoverageState {
	if st, ok := c[s]; ok && st.State != "" {
		return st.State
	}
	return CoverageNotRead
}

// Permission is the GitHub permission a source needs, in the words the App's
// settings page uses, or "" for a source that needs none. It is here, beside
// the sources, so the Kennel Club page, the grant flow and the documentation
// read one answer.
func (s Source) Permission() string {
	switch s {
	case SourceMetadata:
		return "Repository permissions: Metadata: Read-only"
	case SourceRuns:
		return "Repository permissions: Actions: Read-only"
	}
	return ""
}

// Label is the source in the words an operator reads on the page.
func (s Source) Label() string {
	switch s {
	case SourceFleet:
		return "What this fleet observed"
	case SourceMetadata:
		return "Repository details"
	case SourceRuns:
		return "Workflow runs"
	}
	return "Something else"
}

// Explain says in one sentence why a source is in a state, and is empty for one
// that was read in full. Like Skipped.Reason it names a permission or a state
// and never anything read from a repository, so it is safe to put on a page
// beside a repository's name.
func Explain(src Source, st CoverageState) string {
	switch st {
	case CoverageOK:
		return ""
	case CoveragePartial:
		return "Only part of this could be read. What was found stands, and nothing found is not an all-clear."
	case CoverageDenied:
		if need := src.Permission(); need != "" {
			return "Zoomies needs " + need + ", which this installation has not been given."
		}
		return "GitHub refused the read."
	case CoverageUnavailable:
		return "This GitHub does not offer it."
	case CoverageHeld:
		return "Requests to GitHub for this installation are being held back to stay within its rate limit; it is read again when they can resume."
	case CoverageError:
		return "The last attempt to read it failed, and it will be tried again."
	}
	return "It has not been read yet."
}

// Skipped is a check that did not run, and why.
type Skipped struct {
	Code   Code          `json:"code"`
	Source Source        `json:"source"`
	State  CoverageState `json:"state"`
}

// Reason is one sentence an operator can act on. It names a permission or a
// state, never anything read from the repository.
func (s Skipped) Reason() string {
	need := s.Source.Permission()
	switch s.State {
	case CoverageDenied:
		if need != "" {
			return "This check needs " + need + ", which this installation has not been given."
		}
		return "GitHub refused this check's read."
	case CoverageUnavailable:
		return "This GitHub does not offer what this check reads."
	case CoverageHeld:
		return "Requests to GitHub for this installation are being held back to stay within its rate limit; the check runs again when they can resume."
	case CoverageError:
		return "The last attempt to read what this check needs failed; it will be tried again."
	}
	return "What this check needs has not been read yet."
}
