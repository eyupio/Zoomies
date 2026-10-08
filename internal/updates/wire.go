package updates

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// The two documents the service and the helper exchange through the update
// folder. They are here, with no file in sight, so that both sides parse the same
// bytes the same way: the service refuses to write what the helper would refuse,
// and the helper treats what it reads as hostile.
const (
	// WireVersion is the "v" both documents carry. A release that changes either
	// shape has to refresh the helper's unit, because nothing else will.
	WireVersion = 1
	// MaxRequestBytes bounds a request. The real document is a couple of hundred
	// bytes; the rest is room for a long requested_by and no more.
	MaxRequestBytes = 4096
	// MaxLogTailBytes bounds the tail of the engine's output a result carries, so
	// that a failed upgrade can say why without the result becoming a log store.
	MaxLogTailBytes = 16384

	// maxRequestIDLength bounds an id so that it stays a name and not a payload.
	maxRequestIDLength = 64
	// maxRequestedByLength bounds who asked, which the helper writes into a log
	// root owns: room for any account name, and none for a payload.
	maxRequestedByLength = 128
)

// Request asks the helper to take this host to one release. It names exactly one
// thing, and holds no URL, path, command or flag: the release source and the
// binary come from the helper's own root-owned environment, never from here.
type Request struct {
	V int `json:"v"`
	// ID is the attempt's id, which the result echoes so that the controller can
	// tell its own answer from one left over from before.
	ID  string `json:"id"`
	Tag string `json:"tag"`
	// RequestedBy is who pressed the button, for the helper's log. It is never
	// acted on.
	RequestedBy string    `json:"requested_by"`
	RequestedAt time.Time `json:"requested_at"`
}

// Result is the helper's answer to one request, written whether the upgrade ran,
// failed, or was refused before it started.
type Result struct {
	V  int    `json:"v"`
	ID string `json:"id"`
	OK bool   `json:"ok"`
	// Tag is the release asked for; From and To are the versions before and after
	// as the helper observed them, and To is empty when nothing changed.
	Tag  string `json:"tag"`
	From string `json:"from"`
	To   string `json:"to"`
	// Error is the sentence an operator reads when OK is false.
	Error string `json:"error"`
	// LogTail is the end of the engine's output, at most MaxLogTailBytes.
	LogTail    string    `json:"log_tail"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// requestIDPattern is what store.NewID("upd") makes: the prefix, an underscore
// and lowercase base32. It is spelled out here because this package cannot
// import the store.
var requestIDPattern = regexp.MustCompile(`^upd_[a-z0-9]+$`)

// ParseRequest reads one request and refuses anything but the documented shape.
//
// The checks run cheapest first, and the length before anything is decoded, so a
// large file costs a comparison. Unknown fields are an error and not a shrug: a
// field the helper does not know is something the writer hoped it would act on.
func ParseRequest(body []byte) (Request, error) {
	if len(body) > MaxRequestBytes {
		return Request{}, fmt.Errorf("the request is %d bytes, over the limit of %d", len(body), MaxRequestBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var r Request
	if err := dec.Decode(&r); err != nil {
		return Request{}, fmt.Errorf("the request is not the document the helper reads: %w", err)
	}
	// A second document after the first is not part of the request, and the
	// decoder alone would stop at the end of the first.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Request{}, errors.New("the request has more in it than one document")
	}
	if r.V != WireVersion {
		return Request{}, fmt.Errorf("the request is version %d and this helper reads version %d", r.V, WireVersion)
	}
	if len(r.ID) > maxRequestIDLength || !requestIDPattern.MatchString(r.ID) {
		return Request{}, fmt.Errorf("the request id %q is not an update attempt id: upd_ followed by lowercase letters and digits, at most %d characters", r.ID, maxRequestIDLength)
	}
	if !ValidTag(r.Tag) {
		return Request{}, fmt.Errorf("the tag %q is not a release tag of the form vMAJOR.MINOR.PATCH", r.Tag)
	}
	// requested_by is only ever logged, but it is logged by root: a newline in it
	// would let the less privileged side write a line of root's log, and an escape
	// sequence would repaint the terminal of whoever reads it. The length is
	// checked first so that the refusal below quotes at most this much.
	if len(r.RequestedBy) > maxRequestedByLength {
		return Request{}, fmt.Errorf("requested_by is %d bytes, over the limit of %d", len(r.RequestedBy), maxRequestedByLength)
	}
	// Printable characters only, not merely no control characters: a right-to-left
	// override reorders what the reader sees, and a line or paragraph separator
	// ends a line in some viewers, and neither is a control character.
	if strings.ContainsFunc(r.RequestedBy, func(c rune) bool { return !unicode.IsGraphic(c) }) {
		return Request{}, fmt.Errorf("requested_by %q has a character in it that is not printable, which a log line must not carry", r.RequestedBy)
	}
	// encoding/json leaves a missing or null time as the zero time without an
	// error, and a request that cannot say when it was made cannot be placed in
	// the log against the attempt that made it.
	if r.RequestedAt.IsZero() {
		return Request{}, errors.New("the request has no requested_at, so it cannot say when it was made")
	}
	return r, nil
}
