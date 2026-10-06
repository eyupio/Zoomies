package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/eyupio/zoomies/internal/kennel"
	"github.com/eyupio/zoomies/internal/store"
)

// What a person can do with Kennel Club: read it, ask for a repository to be read
// again, and make or end a waiver. The rules for each live here and not in the
// handlers, so they are tested without HTTP and an assistant or a script cannot
// reach a different answer by another route.

var (
	// ErrKennelOff is the answer to anything that needs Kennel Club's rows while
	// it is off. The API turns it into a 409 that says where to turn it on.
	ErrKennelOff = errors.New("kennel club is off")
	// ErrKennelWaiverLimit means a repository already has as many waivers as it
	// may.
	ErrKennelWaiverLimit = errors.New("kennel club: this repository has as many waivers as it may")
)

const (
	// KennelRecheckCooldown is the least time between two requests to read one
	// repository again. A read costs GitHub requests the scheduler shares, and a
	// person pressing the button every few seconds is not asking a different
	// question each time.
	KennelRecheckCooldown = 5 * time.Minute
	// KennelMaxWaiverDays is the longest a waiver may last. A waiver that never
	// ends is a decision nobody is asked to make again.
	KennelMaxWaiverDays = 365
	// KennelMaxWaivers is how many a repository may carry: a waiver is made for a
	// finding that exists, and a repository with fifty of them has a different
	// problem from one more of them.
	KennelMaxWaivers = 50
	// Bounds on a waiver's reason, in characters. Ten is a sentence's worth, so
	// "ok" and "fine" are not reasons; five hundred is a paragraph, which is what
	// an audit row should carry and the page should render.
	KennelMinReason  = 10
	KennelMaxReason  = 500
	kennelMaxSubject = 200
)

// KennelCooldownError is a recheck asked for too soon.
type KennelCooldownError struct{ Until time.Time }

func (e *KennelCooldownError) Error() string {
	return "this repository was asked to be read again a moment ago; it can be asked again at " + e.Until.UTC().Format("15:04:05 MST")
}

// KennelFieldError is one thing wrong with a request, in the words the field it
// belongs to should show.
type KennelFieldError struct{ Field, Message string }

// KennelInvalidError is a request the controller understood and will not act on.
type KennelInvalidError struct{ Fields []KennelFieldError }

func (e *KennelInvalidError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, f.Field+": "+f.Message)
	}
	return strings.Join(parts, "; ")
}

// KennelNeedsAdminError is a waiver of an error finding by somebody who may waive
// only the lesser ones. It says which finding, so the answer is about something.
type KennelNeedsAdminError struct{ Code kennel.Code }

func (e *KennelNeedsAdminError) Error() string {
	return "waiving " + string(e.Code) + ", which is an error, takes a more senior role than waiving a warning does"
}

// KennelListFilter narrows the list of repositories. Every field is validated
// here, because the values reach SQL as parameters and a typo in one should be an
// answer and not an empty page.
type KennelListFilter struct {
	Q, Severity, Code, State, InstallationID string
}

// KennelRepositories lists what Kennel Club has concluded, in the shape of GET
// /kennel/repositories/{id}.
func (c *Controller) KennelRepositories(ctx context.Context, f KennelListFilter, page store.Page) ([]KennelRepositoryView, int, error) {
	if !c.cfg().Kennel.Enabled {
		return nil, 0, ErrKennelOff
	}
	filter := store.KennelFilter{Q: f.Q, InstallationID: f.InstallationID}
	switch f.Severity {
	case "", "error", "warning", "info":
		filter.Severity = f.Severity
	default:
		return nil, 0, &KennelInvalidError{Fields: []KennelFieldError{{"severity", "is not error, warning or info"}}}
	}
	if f.Code != "" {
		if _, ok := kennel.Lookup(kennel.Code(f.Code)); !ok {
			return nil, 0, &KennelInvalidError{Fields: []KennelFieldError{{"code", "is not a check Kennel Club has; GET /kennel/checks lists them"}}}
		}
		filter.Code = f.Code
	}
	if f.State != "" {
		switch kennel.State(f.State) {
		case kennel.StatePending, kennel.StatePartial, kennel.StateAttention, kennel.StateBestInShow:
			filter.States = []string{f.State}
		default:
			return nil, 0, &KennelInvalidError{Fields: []KennelFieldError{{"state", "is not pending, partial, attention or best_in_show"}}}
		}
	}
	rows, total, err := c.st.ListKennelRepositories(ctx, filter, page)
	if err != nil {
		return nil, 0, err
	}
	out := make([]KennelRepositoryView, 0, len(rows))
	for _, r := range rows {
		out = append(out, newKennelRepositoryView(r))
	}
	return out, total, nil
}

// KennelRepository is one repository, in the shape the event stream carries.
func (c *Controller) KennelRepository(ctx context.Context, id string) (*KennelRepositoryView, error) {
	if !c.cfg().Kennel.Enabled {
		return nil, ErrKennelOff
	}
	row, err := c.st.GetKennelRepository(ctx, id)
	if err != nil {
		return nil, err
	}
	v := newKennelRepositoryView(row)
	return &v, nil
}

// KennelNeed is a source a check reads, and the permission it takes.
type KennelNeed struct {
	Source kennel.Source `json:"source"`
	Label  string        `json:"label"`
	// Permission is empty for what this fleet already knows, which needs none.
	Permission string `json:"permission"`
	// Conditional says it is read only for the repositories the check applies to:
	// the run history, for example, is read for public repositories and no others.
	Conditional bool `json:"conditional"`
}

// KennelCheckInfo is one entry in the catalogue of what is checked.
type KennelCheckInfo struct {
	Code     kennel.Code     `json:"code"`
	Area     kennel.Area     `json:"area"`
	Severity kennel.Severity `json:"severity"`
	Detects  string          `json:"detects"`
	Needs    []KennelNeed    `json:"needs"`
	// Disabled says the operator turned it off, by its code or its area.
	Disabled bool `json:"disabled"`
}

// KennelChecks is the catalogue. It is built from the registry the evaluator runs,
// so the page that says what is checked, the documentation and the checks cannot
// disagree.
func (c *Controller) KennelChecks() []KennelCheckInfo {
	disabled := kennelDisabled(c.cfg().Kennel)
	checks := kennel.Checks()
	out := make([]KennelCheckInfo, 0, len(checks))
	for _, ck := range checks {
		info := KennelCheckInfo{
			Code: ck.Code, Area: ck.Area, Severity: ck.Severity, Detects: ck.Detects,
			Needs:    make([]KennelNeed, 0, len(ck.Needs)),
			Disabled: disabled[string(ck.Code)] || disabled[string(ck.Area)],
		}
		for _, src := range ck.Needs {
			info.Needs = append(info.Needs, KennelNeed{Source: src, Label: src.Label(), Permission: src.Permission()})
		}
		for _, src := range ck.Conditional {
			info.Needs = append(info.Needs, KennelNeed{Source: src, Label: src.Label(), Permission: src.Permission(), Conditional: true})
		}
		out = append(out, info)
	}
	return out
}

// RecheckKennelRepository asks for a repository's reads from GitHub to happen
// again. It makes the repository due and wakes the loop; it reads nothing itself,
// so a person pressing the button waits on the same budget and the same hold as
// everything else.
func (c *Controller) RecheckKennelRepository(ctx context.Context, id string) (*KennelRepositoryView, error) {
	if !c.cfg().Kennel.Enabled {
		return nil, ErrKennelOff
	}
	row, err := c.st.GetKennelRepository(ctx, id)
	if err != nil {
		return nil, err
	}
	now := c.Now()
	c.kennel.mu.Lock()
	if last, ok := c.kennel.rechecks[id]; ok && now.Before(last.Add(KennelRecheckCooldown)) {
		c.kennel.mu.Unlock()
		return nil, &KennelCooldownError{Until: last.Add(KennelRecheckCooldown)}
	}
	c.kennel.rechecks[id] = now
	c.kennel.mu.Unlock()

	if err := c.st.RequestKennelRecheck(ctx, row.ID); err != nil {
		return nil, err
	}
	c.KickKennel()
	c.PublishKennelRepository(ctx, row.ID)
	return c.KennelRepository(ctx, row.ID)
}

// KennelWaiverInput is a request to waive a finding.
type KennelWaiverInput struct {
	Code      string
	Subject   string
	Reason    string
	ExpiresAt time.Time
}

// KennelActor is who is waiving, as far as the rules need to know.
type KennelActor struct {
	ID, Name string
	// CanWaiveErrors is whether the caller holds kennel.waive_error. The policy
	// table says who does; the controller only asks.
	CanWaiveErrors bool
}

// WaiveKennelFinding records a decision that a finding is acceptable here, and
// works the repository out again so the answer already says so.
//
// It is for a finding that exists. A waiver made ahead of one is a standing
// exception nobody has looked at, so a code and subject that match no open or
// waived finding are refused; and one for an error needs the senior role, because
// an error is a stranger running code on the fleet and the decision that it is
// acceptable is not an operator's to take alone.
func (c *Controller) WaiveKennelFinding(ctx context.Context, repositoryID string, in KennelWaiverInput, by KennelActor) (*KennelRepositoryView, *store.KennelWaiver, error) {
	if !c.cfg().Kennel.Enabled {
		return nil, nil, ErrKennelOff
	}
	row, err := c.st.GetKennelRepository(ctx, repositoryID)
	if err != nil {
		return nil, nil, err
	}
	now := c.Now()
	in.Subject = strings.TrimSpace(in.Subject)
	in.Reason = strings.TrimSpace(in.Reason)
	if bad := checkKennelWaiverInput(in, now); len(bad) > 0 {
		return nil, nil, &KennelInvalidError{Fields: bad}
	}
	code := kennel.Code(in.Code)

	var ev kennel.Evaluation
	_ = json.Unmarshal(row.Evaluation, &ev)
	severity, found := kennelFindingSeverity(ev, code, in.Subject)
	if !found {
		return nil, nil, &KennelInvalidError{Fields: []KennelFieldError{{"code", "matches no open finding on this repository, so there is nothing to waive; a waiver is a decision about a finding that exists"}}}
	}
	if severity == kennel.SeverityError && !by.CanWaiveErrors {
		return nil, nil, &KennelNeedsAdminError{Code: code}
	}

	existing, err := c.st.ListKennelWaivers(ctx, row.ID)
	if err != nil {
		return nil, nil, err
	}
	renewing := false
	for _, w := range existing {
		if w.Code == in.Code && w.Subject == in.Subject {
			renewing = true
		}
	}
	if !renewing && len(existing) >= KennelMaxWaivers {
		return nil, nil, ErrKennelWaiverLimit
	}

	w := &store.KennelWaiver{
		RepositoryPK: row.ID, Code: in.Code, Subject: in.Subject, Severity: string(severity),
		Reason: in.Reason, CreatedBy: by.ID, CreatedByName: by.Name, ExpiresAt: in.ExpiresAt,
	}
	if err := c.st.UpsertKennelWaiver(ctx, w); err != nil {
		return nil, nil, err
	}
	v, err := c.kennelReevaluated(ctx, row.ID)
	return v, w, err
}

// checkKennelWaiverInput is everything wrong with a request to waive that can be
// said without looking at the repository, all of it at once so a form does not
// send a person round it three times. It is a function of the request and the
// moment, and nothing else, so the edges of each rule -- the shortest reason, the
// last day a waiver may run to -- are tested at the exact instant.
func checkKennelWaiverInput(in KennelWaiverInput, now time.Time) []KennelFieldError {
	var bad []KennelFieldError
	if _, ok := kennel.Lookup(kennel.Code(in.Code)); !ok {
		bad = append(bad, KennelFieldError{"code", "is not a check Kennel Club has; GET /kennel/checks lists them"})
	}
	if len(in.Subject) > kennelMaxSubject {
		bad = append(bad, KennelFieldError{"subject", fmt.Sprintf("is longer than %d bytes", kennelMaxSubject)})
	}
	if n := utf8.RuneCountInString(in.Reason); n < KennelMinReason || n > KennelMaxReason {
		bad = append(bad, KennelFieldError{"reason", fmt.Sprintf("must be %d to %d characters: say why this is acceptable here, for whoever reads the audit log in a year", KennelMinReason, KennelMaxReason)})
	} else if badReasonCharacter(in.Reason) {
		bad = append(bad, KennelFieldError{"reason", "may not contain control or direction-changing characters"})
	}
	switch {
	case in.ExpiresAt.IsZero():
		bad = append(bad, KennelFieldError{"expires_at", "is required: a waiver that never ends is a decision nobody is asked to make again"})
	case !in.ExpiresAt.After(now):
		bad = append(bad, KennelFieldError{"expires_at", "must be in the future"})
	case in.ExpiresAt.After(now.AddDate(0, 0, KennelMaxWaiverDays)):
		bad = append(bad, KennelFieldError{"expires_at", fmt.Sprintf("is more than %d days away; decide again before then", KennelMaxWaiverDays)})
	}
	return bad
}

// UnwaiveKennelFinding ends a waiver. It returns the waiver it ended, which is
// what the audit row records as what was.
func (c *Controller) UnwaiveKennelFinding(ctx context.Context, repositoryID, waiverID string) (*KennelRepositoryView, *store.KennelWaiver, error) {
	if !c.cfg().Kennel.Enabled {
		return nil, nil, ErrKennelOff
	}
	w, err := c.st.GetKennelWaiver(ctx, waiverID)
	if err != nil {
		return nil, nil, err
	}
	// A waiver is found by its own ID, and it has to be this repository's: a
	// request that names the wrong repository is a 404, and not a way to end
	// somebody else's decision by guessing at one.
	if w.RepositoryPK != repositoryID {
		return nil, nil, fmt.Errorf("kennel waiver %s: %w", waiverID, store.ErrNotFound)
	}
	if err := c.st.DeleteKennelWaiver(ctx, waiverID); err != nil {
		return nil, nil, err
	}
	v, err := c.kennelReevaluated(ctx, repositoryID)
	return v, w, err
}

// kennelReevaluated works one repository out again from what is already known --
// no request to GitHub -- and returns it as it now stands. A change the operator
// made is looked at by the operator at once, so it cannot wait for the loop.
func (c *Controller) kennelReevaluated(ctx context.Context, id string) (*KennelRepositoryView, error) {
	row, err := c.st.GetKennelRepository(ctx, id)
	if err != nil {
		return nil, err
	}
	in, err := c.kennelInput(ctx)
	if err != nil {
		return nil, err
	}
	inst, err := c.st.GetInstallation(ctx, row.InstallationID)
	if err != nil {
		return nil, err
	}
	if err := c.kennelEvaluate(ctx, inst, row, parseKennelWatermark(row.Watermark), nil, false, 0, in); err != nil {
		return nil, err
	}
	return c.KennelRepository(ctx, id)
}

// kennelFindingSeverity is the severity of the finding a waiver would be about,
// whether it is open or already waived (a renewal).
func kennelFindingSeverity(ev kennel.Evaluation, code kennel.Code, subject string) (kennel.Severity, bool) {
	for _, f := range ev.Findings {
		if f.Code == code && f.Subject == subject {
			return f.Severity, true
		}
	}
	for _, w := range ev.Waived {
		if w.Finding.Code == code && w.Finding.Subject == subject {
			return w.Finding.Severity, true
		}
	}
	return "", false
}

// badReasonCharacter is whether a reason has a character that does not belong in
// a sentence a person reads: a control character other than a line break, or one
// of the characters that change which way text runs, which is how a reason can be
// made to look like something it is not.
func badReasonCharacter(s string) bool {
	for _, r := range s {
		if (unicode.IsControl(r) && r != '\n') || isBidiControl(r) {
			return true
		}
	}
	return false
}
