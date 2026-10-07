package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/auth"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

// Kennel Club's routes are transport. What a repository is held to, who may waive
// what, and how often a recheck may be asked for are decisions of the controller
// and the policy table, which these handlers ask and render; none of them has an
// opinion about the fleet.

// kennelRepositoryResponse is the shape every repository route returns, rendered
// by the controller so the event stream's kennel.updated frames are the same JSON.
type kennelRepositoryResponse = controller.KennelRepositoryView

// kennelOverviewResponse is GET /kennel, and the payload of kennel.summary.
type kennelOverviewResponse = controller.KennelOverviewView

// kennelCheckResponse is one entry in the catalogue of what is checked.
type kennelCheckResponse = controller.KennelCheckInfo

// kennelOffMessage is what every route that needs Kennel Club's rows answers
// while it is off: where to turn it on, because "conflict" alone does not say.
const kennelOffMessage = "Kennel Club is off. An administrator can turn it on under Settings → Configuration → kennel.enabled."

// failKennel maps the controller's answers about Kennel Club onto status codes,
// and leaves anything else to the common mapping.
func (s *Server) failKennel(w http.ResponseWriter, r *http.Request, doing string, err error) {
	var (
		invalid *controller.KennelInvalidError
		cool    *controller.KennelCooldownError
		needs   *controller.KennelNeedsAdminError
	)
	switch {
	case errors.Is(err, controller.ErrKennelOff):
		conflict(w, kennelOffMessage)
	case errors.As(err, &invalid):
		fields := make([]fieldError, 0, len(invalid.Fields))
		for _, f := range invalid.Fields {
			fields = append(fields, fieldError{Field: f.Field, Message: f.Message})
		}
		unprocessable(w, "", fields)
	case errors.As(err, &cool):
		rateLimited(w, err.Error(), max(cool.Until.Sub(s.ctrl.Now()), time.Second))
	case errors.As(err, &needs):
		// auth.Explain names the role this takes and the one the caller has, which
		// is the sentence a 403 on this route should end with.
		forbidden(w, fmt.Sprintf("%s is an error finding, and waiving one is an administrator's decision: %s",
			needs.Code, auth.Explain(Identity(r.Context()), auth.ActionKennelWaiveError)))
	case errors.Is(err, controller.ErrKennelNotTracked):
		conflict(w, "This repository is not tracked, so Kennel Club has no findings for it and nothing to recheck or waive. Start tracking it first.")
	case errors.Is(err, controller.ErrKennelUntrackNeedsAdmin):
		forbidden(w, "Stopping Kennel Club looking at a repository silences its errors, which is an administrator's decision: "+
			auth.Explain(Identity(r.Context()), auth.ActionKennelUntrack))
	case errors.Is(err, controller.ErrKennelWaiverLimit):
		conflict(w, fmt.Sprintf("this repository already has %d waivers, which is as many as it may; end one that is no longer needed first", controller.KennelMaxWaivers))
	default:
		s.fail(w, r, doing, err)
	}
}

// handleKennelOverview answers GET /api/v1/kennel.
//
// It answers 200 with enabled:false while Kennel Club is off, and not a 409: the
// page needs a document to render its explanation, and "off" is a state it
// describes and not a failure it reports.
func (s *Server) handleKennelOverview(w http.ResponseWriter, r *http.Request) {
	o, err := s.ctrl.KennelOverview(r.Context())
	if err != nil {
		s.internal(w, r, "working out the Kennel Club overview", err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// handleKennelChecks answers GET /api/v1/kennel/checks: what is checked, and the
// permission each check takes. It is the registry the evaluator runs, so the page
// that says what is checked and the documentation cannot disagree with it.
func (s *Server) handleKennelChecks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, newList(s.ctrl.KennelChecks()))
}

// handleListKennelRepositories answers GET /api/v1/kennel/repositories.
func (s *Server) handleListKennelRepositories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := parsePage(r)
	filter := controller.KennelListFilter{
		Q: q.Get("q"), Severity: q.Get("severity"), Code: q.Get("code"),
		State: q.Get("state"), InstallationID: q.Get("installation"),
	}
	if raw := strings.TrimSpace(q.Get("active")); raw != "" {
		active, err := strconv.ParseBool(raw)
		if err != nil {
			// A filter that is wrong is a 400 on the parameter, as it is on every
			// other: ignoring it would show the whole fleet to somebody who asked
			// for the busy part of it.
			badRequestField(w, "active", "is not true or false")
			return
		}
		filter.Active = &active
	}
	if raw := strings.TrimSpace(q.Get("tracked")); raw != "" {
		tracked, err := strconv.ParseBool(raw)
		if err != nil {
			badRequestField(w, "tracked", "is not true or false")
			return
		}
		filter.Tracked = &tracked
	}
	rows, total, err := s.ctrl.KennelRepositories(r.Context(), filter, p)
	if err != nil {
		var invalid *controller.KennelInvalidError
		if errors.As(err, &invalid) && len(invalid.Fields) > 0 {
			// A filter that is wrong is a 400 on the parameter, as it is on every
			// other list, and not a 422: there is no body to correct.
			badRequestField(w, invalid.Fields[0].Field, invalid.Fields[0].Message)
			return
		}
		s.failKennel(w, r, "listing Kennel Club's repositories", err)
		return
	}
	writeJSON(w, http.StatusOK, newPage(rows, total, p))
}

// handleGetKennelRepository answers GET /api/v1/kennel/repositories/{id}.
func (s *Server) handleGetKennelRepository(w http.ResponseWriter, r *http.Request) {
	v, err := s.ctrl.KennelRepository(r.Context(), chiURLParam(r, "id"))
	if err != nil {
		s.failKennel(w, r, "reading a Kennel Club repository", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// handleRecheckKennelRepository answers POST /api/v1/kennel/repositories/{id}/recheck.
//
// It is a 202: the repository is made due and the loop is woken, and the reads
// happen when the loop's budget and the installation's hold allow. The answer is
// the repository as it now stands, with its reads due.
func (s *Server) handleRecheckKennelRepository(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	v, err := s.ctrl.RecheckKennelRepository(r.Context(), id)
	if err != nil {
		s.failKennel(w, r, "asking for a repository to be read again", err)
		return
	}
	_ = s.auth.Auditor().Record(r.Context(), Identity(r.Context()), "kennel.recheck", "kennel_repository", id, nil,
		map[string]any{"name": v.Name})
	writeJSON(w, http.StatusAccepted, v)
}

// kennelWaiverRequest is the body of PUT /kennel/repositories/{id}/waivers.
type kennelWaiverRequest struct {
	Code      string    `json:"code"`
	Subject   string    `json:"subject"`
	Reason    string    `json:"reason"`
	ExpiresAt time.Time `json:"expires_at"`
}

// kennelWaiverAudit is the part of a waiver an audit row records. The reason is in
// it, because why a finding was thought acceptable is the one thing somebody
// reading the log in a year will want.
type kennelWaiverAudit struct {
	ID        string    `json:"id"`
	Code      string    `json:"code"`
	Subject   string    `json:"subject,omitempty"`
	Severity  string    `json:"severity"`
	Reason    string    `json:"reason"`
	By        string    `json:"by"`
	ExpiresAt time.Time `json:"expires_at"`
}

func waiverAudit(w *store.KennelWaiver) kennelWaiverAudit {
	return kennelWaiverAudit{ID: w.ID, Code: w.Code, Subject: w.Subject, Severity: w.Severity,
		Reason: w.Reason, By: w.CreatedByName, ExpiresAt: w.ExpiresAt}
}

// handleWaiveKennelFinding answers PUT /api/v1/kennel/repositories/{id}/waivers.
//
// A PUT because making the same decision again renews it: the waiver keeps its ID,
// and the reason, owner and end are replaced.
func (s *Server) handleWaiveKennelFinding(w http.ResponseWriter, r *http.Request) {
	var in kennelWaiverRequest
	if !decode(w, r, &in) {
		return
	}
	ident := Identity(r.Context())
	id := chiURLParam(r, "id")
	v, waiver, err := s.ctrl.WaiveKennelFinding(r.Context(), id, controller.KennelWaiverInput{
		Code: in.Code, Subject: in.Subject, Reason: in.Reason, ExpiresAt: in.ExpiresAt,
	}, controller.KennelActor{
		ID: ident.ID, Name: ident.Name,
		CanWaiveErrors: auth.Allowed(ident, auth.ActionKennelWaiveError),
	})
	if err != nil {
		s.failKennel(w, r, "recording a Kennel Club waiver", err)
		return
	}
	_ = s.auth.Auditor().Record(r.Context(), ident, "kennel.waive", "kennel_repository", id, nil, waiverAudit(waiver))
	writeJSON(w, http.StatusOK, v)
}

// kennelTrackingRequest is the body of PUT /kennel/repositories/{id}/tracking.
// Tracked is a pointer because false is an answer and absent is not.
type kennelTrackingRequest struct {
	Tracked *bool  `json:"tracked"`
	Reason  string `json:"reason"`
}

// kennelTrackingAudit is the decision an audit row records, which is what stopping
// was and not just that it happened: the reason is the part somebody reading the
// log in a year will want.
type kennelTrackingAudit struct {
	Name   string     `json:"name,omitempty"`
	Reason string     `json:"reason,omitempty"`
	By     string     `json:"by,omitempty"`
	Since  *time.Time `json:"since,omitempty"`
}

// handleSetKennelTracking answers PUT /api/v1/kennel/repositories/{id}/tracking.
//
// A PUT because it sets a state: asking for the one a repository is already in is
// answered with it and changes, and audits, nothing. Stopping needs the
// administrator role and a reason; starting again needs an operator. The answer is
// the repository as it now stands.
func (s *Server) handleSetKennelTracking(w http.ResponseWriter, r *http.Request) {
	var in kennelTrackingRequest
	if !decode(w, r, &in) {
		return
	}
	if in.Tracked == nil {
		unprocessable(w, "", []fieldError{{Field: "tracked", Message: "is required: true to start looking at this repository, false to stop"}})
		return
	}
	ident := Identity(r.Context())
	id := chiURLParam(r, "id")
	v, was, changed, err := s.ctrl.SetKennelTracking(r.Context(), id, controller.KennelTrackingInput{
		Tracked: *in.Tracked, Reason: in.Reason,
	}, controller.KennelActor{
		ID: ident.ID, Name: ident.Name,
		CanUntrack: auth.Allowed(ident, auth.ActionKennelUntrack),
	})
	if err != nil {
		s.failKennel(w, r, "changing whether Kennel Club tracks a repository", err)
		return
	}
	if changed {
		if *in.Tracked {
			// What was undone: who stopped it, when, and why.
			_ = s.auth.Auditor().Record(r.Context(), ident, "kennel.track", "kennel_repository", id,
				kennelTrackingAudit{Reason: was.Reason, By: was.ByName, Since: &was.At}, kennelTrackingAudit{Name: v.Name})
		} else {
			_ = s.auth.Auditor().Record(r.Context(), ident, "kennel.untrack", "kennel_repository", id, nil,
				kennelTrackingAudit{Name: v.Name, Reason: v.Tracking.Reason, By: v.Tracking.By})
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// handleUnwaiveKennelFinding answers DELETE /api/v1/kennel/repositories/{id}/waivers/{waiver_id}.
//
// Any operator may end any waiver, including an administrator's. Ending one only
// ever makes Kennel Club stricter -- the finding is open again -- so it is not a
// decision that needs the senior role the making of one for an error does. The
// answer is the repository as it now stands, which is what the page repaints from.
func (s *Server) handleUnwaiveKennelFinding(w http.ResponseWriter, r *http.Request) {
	id := chiURLParam(r, "id")
	v, ended, err := s.ctrl.UnwaiveKennelFinding(r.Context(), id, chiURLParam(r, "waiver_id"))
	if err != nil {
		s.failKennel(w, r, "ending a Kennel Club waiver", err)
		return
	}
	_ = s.auth.Auditor().Record(r.Context(), Identity(r.Context()), "kennel.unwaive", "kennel_repository", id, waiverAudit(ended), nil)
	writeJSON(w, http.StatusOK, v)
}
