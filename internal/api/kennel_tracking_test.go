package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

const sandboxReason = "a sandbox nobody keeps up, and its jobs are not ours to judge"

// setTracking PUTs the state of a repository as a session or a token.
func (s *kennelStack) setTracking(id string, as request, body map[string]any) *response {
	s.t.Helper()
	as.method, as.path, as.body = http.MethodPut, kennelRepository+id+"/tracking", body
	return s.do(as)
}

func stop(reason string) map[string]any { return map[string]any{"tracked": false, "reason": reason} }

var start = map[string]any{"tracked": true}

// Stopping silences a repository's errors, so it is the administrator's; starting
// again can only make Kennel Club stricter, so it is the operator's. A viewer does
// neither, and a refusal changes nothing.
func TestOnlyAnAdministratorStopsKennelClubLookingAndAnOperatorStartsItAgain(t *testing.T) {
	s := newKennelStack(t)

	s.setTracking(s.exposed, request{cookie: s.viewer}, start).mustStatus(t, http.StatusForbidden, "a viewer starting")
	s.setTracking(s.exposed, request{cookie: s.viewer}, stop(sandboxReason)).mustStatus(t, http.StatusForbidden, "a viewer stopping")

	refused := s.setTracking(s.exposed, request{cookie: s.operator}, stop(sandboxReason))
	refused.mustStatus(t, http.StatusForbidden, "an operator stopping")
	if got := refused.errorCode(t); got != codeForbidden {
		t.Errorf("code = %q, want %q", got, codeForbidden)
	}
	// It ends with the role the action takes and the one the caller has, which is
	// what somebody who got it needs to read.
	for _, want := range []string{"administrator", "needs the admin role", "has operator"} {
		if msg := refused.errorMessage(t); !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q", msg, want)
		}
	}
	if still := s.get(s.exposed); !still.Tracking.Tracked || still.Counts.Error != 2 {
		t.Errorf("a refused request changed the repository: %+v, %+v", still.Tracking, still.Counts)
	}

	done := s.setTracking(s.exposed, request{cookie: s.admin}, stop("  "+sandboxReason+"  "))
	done.mustStatus(t, http.StatusOK, "an administrator stopping")
	var v kennelRepositoryResponse
	done.into(t, &v)
	if v.Tracking.Tracked || v.Tracking.Reason != sandboxReason || v.Tracking.By != "admin" || v.Tracking.Since == nil {
		t.Errorf("tracking = %+v, want who stopped it, when and why, with the spaces around the reason gone", v.Tracking)
	}
	if v.State != "pending" || len(v.Findings) != 0 || v.Counts.Error != 0 {
		t.Errorf("state %q, %d findings, %d errors: nothing is evaluated for a repository nobody tracks", v.State, len(v.Findings), v.Counts.Error)
	}
	if got := s.get(s.exposed); got.Tracking.Tracked {
		t.Error("the answer said it was stopped and a fetch says it was not")
	}

	back := s.setTracking(s.exposed, request{cookie: s.operator}, start)
	back.mustStatus(t, http.StatusOK, "an operator starting")
	back.into(t, &v)
	if !v.Tracking.Tracked || v.Tracking.Reason != "" || v.Tracking.By != "" || v.Tracking.Since != nil {
		t.Errorf("tracking = %+v, want nothing left of the decision on a repository that is tracked", v.Tracking)
	}
}

func TestARequestToChangeTrackingIsToldEverythingWrongWithIt(t *testing.T) {
	s := newKennelStack(t)

	missing := s.setTracking(s.exposed, request{cookie: s.admin}, map[string]any{"reason": sandboxReason})
	missing.mustStatus(t, http.StatusUnprocessableEntity, "no tracked")
	var env errorEnvelope
	missing.into(t, &env)
	if len(env.Errors) != 1 || env.Errors[0].Field != "tracked" || env.Errors[0].Message == "" {
		t.Errorf("errors = %+v, want the one field that is missing, with a message", env.Errors)
	}

	short := s.setTracking(s.exposed, request{cookie: s.admin}, stop("a sandbox"))
	short.mustStatus(t, http.StatusUnprocessableEntity, "a reason that is too short")
	short.into(t, &env)
	var fields []string
	for _, f := range env.Errors {
		fields = append(fields, f.Field)
	}
	if !slices.Equal(fields, []string{"reason"}) {
		t.Errorf("fields = %v, want the reason", fields)
	}

	// A body that is not this at all is a bad request, as it is for every route.
	s.setTracking(s.exposed, request{cookie: s.admin}, map[string]any{"tracked": "no", "reason": sandboxReason}).
		mustStatus(t, http.StatusBadRequest, "tracked that is not a boolean")
	s.setTracking(s.exposed, request{cookie: s.admin}, map[string]any{"tracked": false, "reason": sandboxReason, "forever": true}).
		mustStatus(t, http.StatusBadRequest, "a field it does not take")
	s.setTracking("kcr_nope", request{cookie: s.admin}, stop(sandboxReason)).mustStatus(t, http.StatusNotFound, "a repository that is not there")

	if !s.get(s.exposed).Tracking.Tracked {
		t.Error("a request that was wrong stopped it")
	}
}

// What an audit row keeps of stopping is why, because somebody finding a repository
// quiet in a year will ask; and of starting, what it undid. A request for the state
// a repository is already in decided nothing, so it has no row.
func TestStoppingAndStartingAreAuditedWithWhatWasDecidedAndAnUnchangedRequestIsNot(t *testing.T) {
	s := newKennelStack(t)

	s.setTracking(s.rebuilt, request{cookie: s.admin}, stop(sandboxReason)).mustStatus(t, http.StatusOK, "stop")
	row := lastAudit(t, s.harness, "kennel.untrack")
	if row == nil {
		t.Fatal("stopping left no audit row")
	}
	if row.TargetID != s.rebuilt || row.TargetKind != "kennel_repository" || row.ActorName != "admin" {
		t.Errorf("audit row = %+v", row)
	}
	var after kennelTrackingAudit
	if err := json.Unmarshal([]byte(row.After), &after); err != nil {
		t.Fatalf("the audit row's after is not the decision: %v\n%s", err, row.After)
	}
	if after.Name != "acme/rebuilt" || after.Reason != sandboxReason || after.By != "admin" {
		t.Errorf("audited decision = %+v, want the repository, the reason and who", after)
	}

	// The same request again, from somebody else, decides nothing.
	s.setTracking(s.rebuilt, request{cookie: s.admin}, stop("a different reason entirely")).mustStatus(t, http.StatusOK, "stop again")
	if again := lastAudit(t, s.harness, "kennel.untrack"); again.ID != row.ID {
		t.Errorf("asking for the state it was already in was audited as a second decision: %+v", again)
	}
	if got := s.get(s.rebuilt); got.Tracking.Reason != sandboxReason {
		t.Errorf("the second request replaced the reason: %q", got.Tracking.Reason)
	}

	s.setTracking(s.rebuilt, request{cookie: s.operator}, start).mustStatus(t, http.StatusOK, "start")
	undone := lastAudit(t, s.harness, "kennel.track")
	if undone == nil {
		t.Fatal("starting again left no audit row")
	}
	var was kennelTrackingAudit
	if err := json.Unmarshal([]byte(undone.Before), &was); err != nil {
		t.Fatalf("the audit row's before is not the decision it undid: %v\n%s", err, undone.Before)
	}
	if was.Reason != sandboxReason || was.By != "admin" || was.Since == nil || undone.ActorName != "operator" {
		t.Errorf("before = %+v by %s, want what was undone and who undid it", was, undone.ActorName)
	}

	s.setTracking(s.rebuilt, request{cookie: s.operator}, start).mustStatus(t, http.StatusOK, "start again")
	if again := lastAudit(t, s.harness, "kennel.track"); again.ID != undone.ID {
		t.Errorf("starting a repository that was tracked was audited: %+v", again)
	}
}

// A token's scopes narrow its role and never widen it, and the scope that names
// stopping covers starting, because the route asks for the lesser first.
func TestAScopedTokenChangesTrackingNoMoreThanItsScopesAndItsRoleAllow(t *testing.T) {
	s := newKennelStack(t)
	startOnly := s.token("track-only", store.RoleAdmin, "kennel:track")
	stopToo := s.token("untrack", store.RoleAdmin, "kennel:untrack")
	operatorWider := s.token("operator-untrack", store.RoleOperator, "kennel:untrack")
	readOnly := s.token("read-only", store.RoleAdmin, "kennel:read")

	refused := s.setTracking(s.exposed, request{token: startOnly}, stop(sandboxReason))
	refused.mustStatus(t, http.StatusForbidden, "kennel:track stopping")
	if msg := refused.errorMessage(t); !strings.Contains(msg, "kennel:untrack") {
		t.Errorf("message %q does not name the scope the token is missing", msg)
	}
	s.setTracking(s.exposed, request{token: startOnly}, start).mustStatus(t, http.StatusOK, "kennel:track starting")

	s.setTracking(s.exposed, request{token: stopToo}, stop(sandboxReason)).mustStatus(t, http.StatusOK, "kennel:untrack stopping")
	s.setTracking(s.exposed, request{token: stopToo}, start).mustStatus(t, http.StatusOK, "kennel:untrack starting")

	// Holding the scope does not lend an operator the role.
	s.setTracking(s.exposed, request{token: operatorWider}, stop(sandboxReason)).mustStatus(t, http.StatusForbidden, "an operator's token with kennel:untrack")
	// Reading is not changing.
	s.setTracking(s.exposed, request{token: readOnly}, start).mustStatus(t, http.StatusForbidden, "kennel:read starting")
}

// An untracked repository is listed, and the list can be asked for the ones that
// are or are not. A value that is not a boolean is a 400 on the parameter, and not
// the whole fleet shown to somebody who asked for part of it.
func TestTheListCanBeNarrowedToWhatIsTrackedAndAnUntrackedRepositoryIsAlwaysListed(t *testing.T) {
	s := newKennelStack(t)
	s.setTracking(s.quiet, request{cookie: s.admin}, stop(sandboxReason)).mustStatus(t, http.StatusOK, "stop")

	names := func(query string) []string {
		var out []string
		for _, r := range s.list(query, s.viewer) {
			out = append(out, r.Name)
		}
		slices.Sort(out)
		return out
	}
	if got := names(""); !slices.Equal(got, []string{"acme/exposed", "acme/quiet", "acme/rebuilt"}) {
		t.Errorf("left alone = %v, want every repository, the one nobody tracks included", got)
	}
	if got := names("tracked=true"); !slices.Equal(got, []string{"acme/exposed", "acme/rebuilt"}) {
		t.Errorf("tracked = %v", got)
	}
	if got := names("tracked=false"); !slices.Equal(got, []string{"acme/quiet"}) {
		t.Errorf("not tracked = %v", got)
	}

	resp := s.do(request{method: http.MethodGet, path: kennelBase + "/repositories?tracked=maybe", cookie: s.viewer})
	resp.mustStatus(t, http.StatusBadRequest, "a tracked that is not a boolean")
	var env errorEnvelope
	resp.into(t, &env)
	if env.Error.Field != "tracked" {
		t.Errorf("the 400 names %q, want the parameter", env.Error.Field)
	}

	var o kennelOverviewResponse
	ov := s.do(request{method: http.MethodGet, path: kennelBase, cookie: s.viewer})
	ov.mustStatus(t, http.StatusOK, "overview")
	ov.into(t, &o)
	if o.Repositories != 2 || o.NotTracked != 1 {
		t.Errorf("overview = %d repositories and %d not tracked, want 2 and 1", o.Repositories, o.NotTracked)
	}
}

// There are no findings for a repository nobody is tracking, so there is nothing to
// read again or to call acceptable. The answer says how to start, because "conflict"
// alone does not.
func TestAnUntrackedRepositoryCannotBeRecheckedOrWaivedAndTheAnswerSaysHowToStart(t *testing.T) {
	s := newKennelStack(t)
	s.setTracking(s.exposed, request{cookie: s.admin}, stop(sandboxReason)).mustStatus(t, http.StatusOK, "stop")

	for name, resp := range map[string]*response{
		"recheck": s.do(request{method: http.MethodPost, path: kennelRepository + s.exposed + "/recheck", cookie: s.operator}),
		"waive":   s.waive(s.exposed, s.admin, waiverBody(weakPool, goodReason)),
		"unwaive": s.do(request{method: http.MethodDelete, path: kennelRepository + s.exposed + "/waivers/kcw_x", cookie: s.operator}),
	} {
		resp.mustStatus(t, http.StatusConflict, name+" on a repository nobody tracks")
		if got := resp.errorCode(t); got != codeConflict {
			t.Errorf("%s: code = %q, want %q", name, got, codeConflict)
		}
		if msg := resp.errorMessage(t); !strings.Contains(msg, "not tracked") || !strings.Contains(msg, "Start tracking") {
			t.Errorf("%s: message %q does not say why or what to do", name, msg)
		}
	}
	for _, action := range []string{"kennel.recheck", "kennel.waive", "kennel.unwaive"} {
		if row := lastAudit(t, s.harness, action); row != nil {
			t.Errorf("a refused %s was audited as though it had happened: %+v", action, row)
		}
	}
}
