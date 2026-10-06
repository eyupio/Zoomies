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
}

// The grammars are closed on purpose. They stop shell metacharacters, spaces
// and sentences, which is most of what an injection needs; they do not stop a
// hyphenated sentence, which is why the UI and the MCP tools also treat
// evidence as untrusted rather than relying on this alone. The model is the
// artifactName gate in internal/aicontext.
var (
	poolIDGrammar = regexp.MustCompile(`^pool_[A-Za-z0-9]{1,40}$`)
	nameGrammar   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

// unusualPool replaces a pool name that does not pass the gate.
const unusualPool = "a pool with an unusual name"

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
