package kennel

import (
	"regexp"
	"strconv"
)

// EvidenceKind says what an Evidence ref refers to.
type EvidenceKind string

const (
	EvidencePool EvidenceKind = "pool"
	EvidenceRun  EvidenceKind = "run"
	// EvidenceFile is a place in a workflow file: the file by its blob SHA,
	// never its path, and a job index and a line inside it. The page resolves
	// the SHA to a path from the inventory the controller keeps beside the
	// evaluation, so a path a stranger wrote never enters a stored finding.
	EvidenceFile EvidenceKind = "file"
)

// Evidence is the only place free text can appear in a finding, and it is
// typed. A finding's Title, Detail and Fix are written from fixed templates
// with only integers and enumerated words in them; anything an operator or a
// stranger wrote reaches a person only here, after the gate below, and the UI
// renders it as text.
type Evidence struct {
	Kind EvidenceKind `json:"kind"`
	// Ref is an identifier the gate has accepted, or "" if it refused.
	Ref string `json:"ref"`
	// Label is a name the gate has accepted, or a plain sentence saying the name
	// was unusual.
	Label string `json:"label,omitempty"`
	// JobIndex and Line say where in a file, for file evidence: the job by
	// its position in the file's jobs mapping, -1 for the workflow itself,
	// and the 1-based line of the thing the finding is about.
	JobIndex *int `json:"job_index,omitempty"`
	Line     int  `json:"line,omitempty"`
}

// Location is a place in a workflow file, as the parser reports one.
type Location struct {
	JobIndex int `json:"job_index"`
	Line     int `json:"line"`
}

// The grammars are closed on purpose. They stop shell metacharacters, spaces
// and sentences, which is most of what an injection needs; they do not stop a
// hyphenated sentence, which is why the UI and the MCP tools also treat
// evidence as untrusted rather than relying on this alone. The model is the
// artifactName gate in internal/aicontext.
var (
	poolIDGrammar  = regexp.MustCompile(`^pool_[A-Za-z0-9]{1,40}$`)
	nameGrammar    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	fileSHAGrammar = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

// unusualPool replaces a pool name that does not pass the gate, and
// unusualFile a file identity that does not.
const (
	unusualPool = "a pool with an unusual name"
	unusualFile = "a workflow whose identity was unusual"
)

// fileEvidence gates a blob SHA. One GitHub made always passes; one that does
// not is something else wearing its name, and is dropped whole.
func fileEvidence(sha string, loc Location) Evidence {
	if !fileSHAGrammar.MatchString(sha) {
		return Evidence{Kind: EvidenceFile, Label: unusualFile}
	}
	job := loc.JobIndex
	return Evidence{Kind: EvidenceFile, Ref: sha, JobIndex: &job, Line: loc.Line}
}

// subjectOf is what a workflow finding is about: the file, by the first
// twelve characters of its SHA, so a waiver covers one file and lapses when
// the file changes. Empty for a SHA that fails the gate.
func subjectOf(sha string) string {
	if !fileSHAGrammar.MatchString(sha) {
		return ""
	}
	return sha[:12]
}

// poolEvidence gates a pool's identifier and name.
func poolEvidence(id, name string) Evidence {
	if !poolIDGrammar.MatchString(id) {
		// An identifier the controller made should always pass. One that does
		// not is dropped whole rather than shown.
		return Evidence{Kind: EvidencePool, Label: unusualPool}
	}
	if !nameGrammar.MatchString(name) {
		return Evidence{Kind: EvidencePool, Ref: id, Label: unusualPool}
	}
	return Evidence{Kind: EvidencePool, Ref: id, Label: name}
}

// runEvidence is a run's numeric ID, which is all that is ever shown of a run:
// the page on GitHub is where the rest can be read.
func runEvidence(id int64) Evidence {
	if id <= 0 {
		return Evidence{Kind: EvidenceRun}
	}
	return Evidence{Kind: EvidenceRun, Ref: strconv.FormatInt(id, 10)}
}
