package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// A problem tells an operator what is wrong and what to change. Most of the time
// the change is one the controller has already worked out in full -- it priced it
// against the hosts before it said so -- and an operator who has to retype it into
// a form, or paste a command, is doing the part of the job a machine did better.
//
// A Remedy is that change as data: the update, in the exact shape the route that
// makes it takes, and the sentence that says what it costs. Applying one is not a
// second way of changing a pool or a host. It is the pool's or the host's own
// update route, called as the person who asked, with everything that route does --
// its validation, its refusal of a change that leaves a pool with nowhere to run,
// its audit row -- and never with the override that lifts the refusal.

// RemedyKind says which update a remedy is, and so which role may apply it.
type RemedyKind string

const (
	// RemedyPoolUpdate is a PATCH of a pool; Body is its request.
	RemedyPoolUpdate RemedyKind = "pool.update"
	// RemedyHostUpdate is a PATCH of a host; Body is its request.
	RemedyHostUpdate RemedyKind = "host.update"
)

// Remedy is one change a problem proposes.
type Remedy struct {
	// ID names this exact change, so that an apply is for what the person was
	// shown: if the controller has since worked out a different one, the ID is
	// different and the apply is refused as out of date rather than made.
	ID string `json:"id"`
	// Label is the button: what the change is, in a few words.
	Label string `json:"label"`
	// Effect is what the controller priced it at -- what it keeps and what it
	// gains -- said before it is applied, because a change whose cost is found
	// out afterwards is not a suggestion.
	Effect   string          `json:"effect,omitempty"`
	Kind     RemedyKind      `json:"kind"`
	TargetID string          `json:"target_id"`
	Body     json.RawMessage `json:"body"`
	// Base is what the body was built from -- the pool's resources or the host's
	// runner profile as the controller read them -- as RemedyBase hashes it. The body
	// replaces that whole object, so applying it to one that has been edited since
	// would put back whatever the edit changed; the apply compares the two and
	// refuses. It is the controller's own check and never leaves it.
	Base string `json:"-"`
}

// RemedyBase is the fingerprint a remedy's Base and the object it will replace are
// compared by.
func RemedyBase(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// newRemedy builds a remedy for a pool or host from the request that makes it, and
// the object the request was built from.
func newRemedy(kind RemedyKind, targetID, label, effect string, body, base any) *Remedy {
	raw, err := json.Marshal(body)
	if err != nil {
		// A request built from the controller's own types does not fail to
		// encode; if it somehow did, there is no remedy to offer, which is the
		// safe answer.
		return nil
	}
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + targetID + "\x00" + string(raw)))
	return &Remedy{ID: "rem_" + hex.EncodeToString(sum[:6]), Label: label, Effect: effect, Kind: kind, TargetID: targetID, Body: raw, Base: RemedyBase(base)}
}
