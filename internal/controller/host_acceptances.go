package controller

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"time"

	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

const (
	// Bounds on an acceptance's reason, in characters, are the Kennel waiver's:
	// ten is a sentence's worth, so "ok" is not a reason, and five hundred is a
	// paragraph, which is what an audit row should carry and a row can render.
	HostAcceptMinReason = KennelMinReason
	HostAcceptMaxReason = KennelMaxReason
)

// HostCheckChangedError is an acceptance asked for a value the check no longer
// has. The operator decided about what they saw, and an agent has reported since:
// accepting what they did not see would be a decision about nothing.
type HostCheckChangedError struct{ Now string }

func (e *HostCheckChangedError) Error() string {
	return fmt.Sprintf("That check changed while you were deciding. It now reads %q. Review it and accept again.", e.Now)
}

// HostAcceptInvalidError is a request to accept that is wrong in a way the
// person can fix, each in the words of the field it belongs to.
type HostAcceptInvalidError struct{ Fields []KennelFieldError }

func (e *HostAcceptInvalidError) Error() string {
	if len(e.Fields) == 0 {
		return "that is not a valid request"
	}
	return e.Fields[0].Field + " " + e.Fields[0].Message
}

// ErrHostAcceptLimit is a host that already holds as many acceptances as it may.
var ErrHostAcceptLimit = errors.New("this host already has as many accepted checks as it may; revoke one that is no longer needed first")

// HostActor is who is making a decision about a host: the name is kept with the
// acceptance, so it still reads right after the person's account is gone.
type HostActor struct{ ID, Name string }

// AcceptInput is a request to accept one host's check as deliberate.
type AcceptInput struct {
	CheckID string
	// SeenCurrent is what the person read when they decided. The acceptance is
	// recorded against the stored report's text, never this, and only when the
	// two agree.
	SeenCurrent string
	Reason      string
	ExpiresAt   time.Time
}

// checkHostAcceptInput is everything wrong with a request that can be said
// without looking at the host, all of it at once so a form does not send a
// person round it three times.
func checkHostAcceptInput(in AcceptInput, now time.Time) []KennelFieldError {
	var bad []KennelFieldError
	if in.CheckID == "" {
		bad = append(bad, KennelFieldError{"check_id", "is required"})
	}
	if n := utf8.RuneCountInString(in.Reason); n < HostAcceptMinReason || n > HostAcceptMaxReason {
		bad = append(bad, KennelFieldError{"reason", fmt.Sprintf("must be %d to %d characters: say why this is deliberate, for whoever reads it on this host's page", HostAcceptMinReason, HostAcceptMaxReason)})
	} else if badReasonCharacter(in.Reason) {
		bad = append(bad, KennelFieldError{"reason", "may not contain control or direction-changing characters"})
	}
	switch {
	case in.ExpiresAt.IsZero():
		bad = append(bad, KennelFieldError{"expires_at", "is required: an acceptance that never ends is a decision nobody is asked to make again"})
	case !in.ExpiresAt.After(now):
		bad = append(bad, KennelFieldError{"expires_at", "must be in the future"})
	case in.ExpiresAt.After(now.Add(hosttune.MaxAcceptance)):
		bad = append(bad, KennelFieldError{"expires_at", "is more than 365 days away; decide again before then"})
	}
	return bad
}

// AcceptHostCheck records that a counted warning on one host is deliberate. It
// changes nothing on the host -- the controller never runs a command there -- it
// only stops Zoomies counting the check until the value changes or the term ends.
//
// The value stored is the stored report's, not the request's, so an acceptance
// cannot be made to cover text the host never reported.
func (c *Controller) AcceptHostCheck(ctx context.Context, hostID string, in AcceptInput, by HostActor) (HostView, *store.HostCheckAcceptance, error) {
	if bad := checkHostAcceptInput(in, c.Now()); len(bad) > 0 {
		return HostView{}, nil, &HostAcceptInvalidError{Fields: bad}
	}
	h, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		return HostView{}, nil, err
	}
	rep := h.Doctor.Report
	if rep == nil {
		return HostView{}, nil, &HostAcceptInvalidError{Fields: []KennelFieldError{{"check_id", "cannot be accepted yet: this host has not sent a health report"}}}
	}
	if rep.Container {
		return HostView{}, nil, &HostAcceptInvalidError{Fields: []KennelFieldError{{"check_id", "cannot be accepted here: a container agent's report is partial, so there is nothing here to accept"}}}
	}
	var stored *hosttune.Result
	for i := range rep.Results {
		if rep.Results[i].ID == in.CheckID {
			stored = &rep.Results[i]
		}
	}
	if ok, why := hosttune.Acceptable(in.CheckID); !ok {
		return HostView{}, nil, &HostAcceptInvalidError{Fields: []KennelFieldError{{"check_id", why}}}
	}
	if stored == nil {
		return HostView{}, nil, &HostAcceptInvalidError{Fields: []KennelFieldError{{"check_id", "That check is not a warning on this host right now."}}}
	}
	if why := hosttune.AcceptRefusal(*stored); why != "" {
		return HostView{}, nil, &HostAcceptInvalidError{Fields: []KennelFieldError{{"check_id", why}}}
	}
	if stored.Current != in.SeenCurrent {
		return HostView{}, nil, &HostCheckChangedError{Now: stored.Current}
	}
	a := &store.HostCheckAcceptance{
		HostID: h.ID, CheckID: in.CheckID, Current: stored.Current, Reason: in.Reason,
		By: by.ID, ByName: by.Name, ExpiresAt: in.ExpiresAt.UTC(),
	}
	if err := c.st.UpsertHostCheckAcceptance(ctx, a); err != nil {
		if errors.Is(err, store.ErrTooManyHostAcceptances) {
			return HostView{}, nil, ErrHostAcceptLimit
		}
		return HostView{}, nil, err
	}
	v, err := c.hostAfterAcceptance(ctx, h.ID)
	return v, a, err
}

// RevokeHostCheck ends an acceptance and returns the one it ended, which is what
// the audit row records as what was. Revoking one that is not there is not an
// error: two operators pressing the button agree on the outcome, and the second
// is told the host as it stands. The returned acceptance is nil then.
func (c *Controller) RevokeHostCheck(ctx context.Context, hostID, checkID string) (HostView, *store.HostCheckAcceptance, error) {
	h, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		return HostView{}, nil, err
	}
	var ended *store.HostCheckAcceptance
	for i := range h.Acceptances {
		if h.Acceptances[i].CheckID == checkID {
			ended = &h.Acceptances[i]
		}
	}
	if err := c.st.DeleteHostCheckAcceptance(ctx, hostID, checkID); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return HostView{}, nil, err
		}
		ended = nil
	}
	v, err := c.hostAfterAcceptance(ctx, hostID)
	return v, ended, err
}

// hostAfterAcceptance re-reads the host so the frame and the answer carry the
// acceptances as they now stand, and announces it: every open page repaints from
// the same payload the person who decided gets.
func (c *Controller) hostAfterAcceptance(ctx context.Context, hostID string) (HostView, error) {
	h, err := c.st.GetHost(ctx, hostID)
	if err != nil {
		return HostView{}, err
	}
	c.publishHost(h)
	return c.HostView(h), nil
}
