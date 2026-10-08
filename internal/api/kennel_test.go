package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/store"
)

const (
	kennelBase       = "/api/v1/kennel"
	kennelRepository = kennelBase + "/repositories/"

	publicOnFleet = "exposure.public_repo_on_fleet"
	weakPool      = "exposure.public_repo_weak_pool"
	goodReason    = "an open-source project whose runners are rebuilt for every job"
)

// kennelStack is a controller with Kennel Club on and one pass made, against a
// fake GitHub that knows three repositories:
//
//   - acme/exposed, public, whose runs reach a persistent pool: two errors.
//   - acme/rebuilt, public, whose runs reach an ephemeral pool: one warning.
//   - acme/quiet, private: nothing.
//
// The rows are the ones the loop itself writes, so what these tests read is what
// a real fleet would say, and not a document made up to agree with the handler.
type kennelStack struct {
	*harness
	inst                    *store.Installation
	viewer, operator, admin string
	exposed, rebuilt, quiet string
}

func newKennelStack(t *testing.T) *kennelStack {
	t.Helper()
	h := newHarness(t)
	inst := h.installation()
	persistent := h.pool(inst, "linux-x64")
	persistent.Ephemeral = false
	if err := h.st.UpdatePool(h.ctx, persistent); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	ephemeral := h.pool(inst, "linux-arm64")

	seen := int64(1000)
	ran := func(repo string, run int64, pool *store.Pool, visibility, event string) {
		seen++
		now := time.Now()
		if _, err := h.st.UpsertJob(h.ctx, &store.Job{
			GitHubJobID: seen, GitHubRunID: run, Repo: repo, InstallationID: inst.ID, PoolID: pool.ID,
			RunnerID: "run_" + pool.ID, State: store.JobCompleted, Conclusion: "success", Matched: true,
			Labels:   store.NormalizeLabels([]string{"self-hosted", "linux"}),
			QueuedAt: now.Add(-time.Hour), StartedAt: ptr(now.Add(-59 * time.Minute)), CompletedAt: ptr(now.Add(-50 * time.Minute)),
		}); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
		h.gh.AddRepo(repo)
		h.gh.SetVisibility(repo, visibility)
		if event != "" {
			h.gh.SetRunTrigger(repo, run, event, h.gh.RepositoryID(repo))
		}
	}
	ran("acme/exposed", 11, persistent, "public", "push")
	ran("acme/rebuilt", 13, ephemeral, "public", "push")
	ran("acme/quiet", 12, ephemeral, "private", "")

	h.ctrl.UpdateConfig(func(c *config.Config) { c.Kennel.Enabled = true })
	h.ctrl.KennelPass(h.ctx)

	s := &kennelStack{harness: h, inst: inst}
	v, _ := h.user("viewer", store.RoleViewer)
	o, _ := h.user("operator", store.RoleOperator)
	a, _ := h.user("admin", store.RoleAdmin)
	s.viewer, s.operator, s.admin = h.session(v), h.session(o), h.session(a)
	rows := s.list("", s.viewer)
	for _, r := range rows {
		switch r.Name {
		case "acme/exposed":
			s.exposed = r.ID
		case "acme/rebuilt":
			s.rebuilt = r.ID
		case "acme/quiet":
			s.quiet = r.ID
		}
	}
	if s.exposed == "" || s.rebuilt == "" || s.quiet == "" {
		t.Fatalf("one pass did not make a row for each repository: %+v", rows)
	}
	return s
}

// list reads the repositories through the API, with a query string.
func (s *kennelStack) list(query, cookie string) []kennelRepositoryResponse {
	s.t.Helper()
	path := kennelBase + "/repositories"
	if query != "" {
		path += "?" + query
	}
	resp := s.do(request{method: http.MethodGet, path: path, cookie: cookie})
	resp.mustStatus(s.t, http.StatusOK, "list "+path)
	var page struct {
		Items []kennelRepositoryResponse `json:"items"`
	}
	resp.into(s.t, &page)
	return page.Items
}

func (s *kennelStack) get(id string) kennelRepositoryResponse {
	s.t.Helper()
	resp := s.do(request{method: http.MethodGet, path: kennelRepository + id, cookie: s.viewer})
	resp.mustStatus(s.t, http.StatusOK, "get "+id)
	var v kennelRepositoryResponse
	resp.into(s.t, &v)
	return v
}

// waive PUTs a waiver as a session and returns the response.
func (s *kennelStack) waive(id, cookie string, body map[string]any) *response {
	s.t.Helper()
	return s.do(request{method: http.MethodPut, path: kennelRepository + id + "/waivers", cookie: cookie, body: body})
}

func codesOf(v kennelRepositoryResponse) []string {
	var out []string
	for _, f := range v.Findings {
		out = append(out, string(f.Code))
	}
	return out
}

func waiverBody(code, reason string) map[string]any {
	return map[string]any{
		"code": code, "reason": reason,
		"expires_at": time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}
}

// ---------------------------------------------------------------------------
// Off
// ---------------------------------------------------------------------------

// The Overview answers while Kennel Club is off, because the page needs a
// document to render its explanation from; everything that needs a row says that
// it is off, and where to turn it on, rather than answering an empty list that
// reads as a fleet with nothing wrong.
func TestKennelClubAnswersItsOverviewWhileOffAndRefusesTheRoutesThatNeedRows(t *testing.T) {
	h := newHarness(t)
	v, _ := h.user("viewer", store.RoleViewer)
	o, _ := h.user("operator", store.RoleOperator)
	viewer, operator := h.session(v), h.session(o)

	overview := h.do(request{method: http.MethodGet, path: kennelBase, cookie: viewer})
	overview.mustStatus(t, http.StatusOK, "the overview while off")
	var doc kennelOverviewResponse
	overview.into(t, &doc)
	if doc.Enabled || doc.Repositories != 0 {
		t.Errorf("the overview of a Kennel Club that is off = %+v", doc)
	}

	checks := h.do(request{method: http.MethodGet, path: kennelBase + "/checks", cookie: viewer})
	checks.mustStatus(t, http.StatusOK, "the catalogue while off")
	var catalogue struct {
		Items []kennelCheckResponse `json:"items"`
	}
	checks.into(t, &catalogue)
	if len(catalogue.Items) == 0 {
		t.Error("the catalogue is empty while Kennel Club is off, which is when its page most needs to say what would be checked")
	}

	for _, tc := range []struct {
		name, method, path string
		cookie             string
		body               any
	}{
		{"list", "GET", kennelBase + "/repositories", viewer, nil},
		{"get", "GET", kennelRepository + "kcr_x", viewer, nil},
		{"recheck", "POST", kennelRepository + "kcr_x/recheck", operator, nil},
		{"waive", "PUT", kennelRepository + "kcr_x/waivers", operator, waiverBody(publicOnFleet, goodReason)},
		{"unwaive", "DELETE", kennelRepository + "kcr_x/waivers/kcw_x", operator, nil},
		{"track", "PUT", kennelRepository + "kcr_x/tracking", operator, map[string]any{"tracked": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(request{method: tc.method, path: tc.path, cookie: tc.cookie, body: tc.body})
			resp.mustStatus(t, http.StatusConflict, tc.name+" while off")
			if got := resp.errorCode(t); got != codeConflict {
				t.Errorf("code = %q, want %q", got, codeConflict)
			}
			if got := resp.errorMessage(t); got != kennelOffMessage {
				t.Errorf("message = %q, want the one that says where to turn it on", got)
			}
		})
	}
	for _, action := range []string{"kennel.recheck", "kennel.waive", "kennel.unwaive", "kennel.track", "kennel.untrack"} {
		if row := lastAudit(t, h, action); row != nil {
			t.Errorf("a refused %s was audited as though it had happened: %+v", action, row)
		}
	}
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

func TestTheOverviewCountsWhatThePassFound(t *testing.T) {
	s := newKennelStack(t)
	resp := s.do(request{method: http.MethodGet, path: kennelBase, cookie: s.viewer})
	resp.mustStatus(t, http.StatusOK, "the overview")
	var o kennelOverviewResponse
	resp.into(t, &o)

	if !o.Enabled || o.Repositories != 3 {
		t.Fatalf("overview = enabled %v with %d repositories, want it on with three", o.Enabled, o.Repositories)
	}
	if o.States.Attention != 2 || o.States.BestInShow != 1 || o.States.Pending != 0 || o.States.Partial != 0 {
		t.Errorf("states = %+v, want two needing attention and one best in show", o.States)
	}
	if o.Counts.Error != 2 || o.Counts.Warning != 1 || o.Counts.Waived != 0 {
		t.Errorf("counts = %+v, want two errors and a warning", o.Counts)
	}
	if len(o.Attention) != 2 || o.Attention[0].Name != "acme/exposed" {
		t.Errorf("attention = %+v, want the two that need it with the repository with errors first", o.Attention)
	}
}

func TestTheRepositoryListCanBeNarrowedByEveryFilterItOffers(t *testing.T) {
	s := newKennelStack(t)
	for _, tc := range []struct {
		name, query string
		want        []string
	}{
		{"nothing", "", []string{"acme/exposed", "acme/quiet", "acme/rebuilt"}},
		{"a name fragment", "q=rebu", []string{"acme/rebuilt"}},
		{"errors", "severity=error", []string{"acme/exposed"}},
		{"warnings", "severity=warning", []string{"acme/rebuilt"}},
		{"a check", "code=" + publicOnFleet, []string{"acme/exposed", "acme/rebuilt"}},
		{"the best in show", "state=best_in_show", []string{"acme/quiet"}},
		{"needing attention", "state=attention", []string{"acme/exposed", "acme/rebuilt"}},
		{"a check and a severity", "code=" + publicOnFleet + "&severity=error", []string{"acme/exposed"}},
		{"the installation that reads them", "installation=" + s.inst.ID, []string{"acme/exposed", "acme/quiet", "acme/rebuilt"}},
		{"an installation that reads none", "installation=ins_somewhere_else", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, r := range s.list(tc.query, s.viewer) {
				got = append(got, r.Name)
			}
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("%q listed %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}

// Active is asked of the jobs, the way the loop asks which repositories to look
// at, so it means the same on the list as it does everywhere else. A repository
// with a row and no job is the one the filter exists to tell apart, and under the
// installation scope there are plenty of them.
func TestTheRepositoryListCanBeNarrowedToWhatTheFleetIsServing(t *testing.T) {
	s := newKennelStack(t)
	if _, err := s.st.TouchKennelRepository(s.ctx, store.KennelRepositoryRef{
		GitHubHost: "github.com", RepositoryID: 9001, InstallationID: s.inst.ID,
		FullName: "acme/dormant", Visibility: "private",
	}); err != nil {
		t.Fatalf("TouchKennelRepository: %v", err)
	}
	names := func(query string) []string {
		t.Helper()
		var got []string
		for _, r := range s.list(query, s.viewer) {
			got = append(got, r.Name)
		}
		slices.Sort(got)
		return got
	}
	for _, tc := range []struct {
		name, query string
		want        []string
	}{
		{"asked for nothing", "", []string{"acme/dormant", "acme/exposed", "acme/quiet", "acme/rebuilt"}},
		{"the ones being served", "active=true", []string{"acme/exposed", "acme/quiet", "acme/rebuilt"}},
		{"the ones that are not", "active=false", []string{"acme/dormant"}},
		{"another spelling of true", "active=1", []string{"acme/exposed", "acme/quiet", "acme/rebuilt"}},
		{"served, and with an error", "active=true&severity=error", []string{"acme/exposed"}},
		{"not served, and with an error", "active=false&severity=error", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(tc.query); !slices.Equal(got, tc.want) {
				t.Errorf("%q listed %v, want %v", tc.query, got, tc.want)
			}
		})
	}

	// The total is of what the filter keeps, so a page of one still says three.
	resp := s.do(request{method: http.MethodGet, path: kennelBase + "/repositories?active=true&limit=1", cookie: s.viewer})
	resp.mustStatus(t, http.StatusOK, "a page of the served")
	var page struct {
		Items []json.RawMessage `json:"items"`
		Total int               `json:"total"`
	}
	resp.into(t, &page)
	if len(page.Items) != 1 || page.Total != 3 {
		t.Errorf("a page of one listed %d of %d, want one of three", len(page.Items), page.Total)
	}
}

// The window is Kennel Club's own, thirty days or the fleet's job retention if
// that is shorter, because the jobs table cannot answer about a day it has already
// pruned. A repository whose job is older than what the fleet keeps is not one it
// can say it is serving.
func TestTheWindowForActiveIsTheOneEveryOtherSentenceNames(t *testing.T) {
	s := newKennelStack(t)
	// The stack's jobs were queued an hour ago.
	s.ctrl.UpdateConfig(func(c *config.Config) { c.Retention.Jobs = 30 * time.Minute })
	if got := len(s.list("active=true", s.viewer)); got != 0 {
		t.Errorf("%d repositories active inside a window of half an hour, want none: their jobs are an hour old", got)
	}
	if got := len(s.list("active=false", s.viewer)); got != 3 {
		t.Errorf("%d repositories not active inside a window of half an hour, want all three", got)
	}
	s.ctrl.UpdateConfig(func(c *config.Config) { c.Retention.Jobs = 90 * 24 * time.Hour })
	if got := len(s.list("active=true", s.viewer)); got != 3 {
		t.Errorf("%d repositories active with ninety days of jobs kept, want all three: the window is thirty days at most, not longer", got)
	}
}

func TestTheRepositoryListPagesAndSaysHowManyThereAre(t *testing.T) {
	s := newKennelStack(t)
	resp := s.do(request{method: http.MethodGet, path: kennelBase + "/repositories?limit=2&offset=2", cookie: s.viewer})
	resp.mustStatus(t, http.StatusOK, "the last page")
	var page struct {
		Items  []json.RawMessage `json:"items"`
		Total  int               `json:"total"`
		Limit  int               `json:"limit"`
		Offset int               `json:"offset"`
	}
	resp.into(t, &page)
	if len(page.Items) != 1 || page.Total != 3 || page.Limit != 2 || page.Offset != 2 {
		t.Errorf("page = %d items of %d at %d+%d, want the one left of three", len(page.Items), page.Total, page.Offset, page.Limit)
	}
}

// A filter that is wrong is a 400 on the parameter that is wrong, as it is on
// every other list, and not an empty page that reads as nothing being open.
func TestAFilterTheListDoesNotKnowIsRefusedByName(t *testing.T) {
	s := newKennelStack(t)
	for _, tc := range []struct{ query, field string }{
		{"severity=loud", "severity"},
		{"state=sleeping", "state"},
		{"code=exposure.nonsense", "code"},
		{"active=sometimes", "active"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			resp := s.do(request{method: http.MethodGet, path: kennelBase + "/repositories?" + tc.query, cookie: s.viewer})
			resp.mustStatus(t, http.StatusBadRequest, tc.query)
			var env errorEnvelope
			resp.into(t, &env)
			if env.Error.Code != codeBadRequest || env.Error.Field != tc.field {
				t.Errorf("error = %+v, want a bad request on %q", env.Error, tc.field)
			}
		})
	}
}

func TestOneRepositoryIsReadByItsIDAndAnUnknownOneIsNotFound(t *testing.T) {
	s := newKennelStack(t)
	v := s.get(s.exposed)
	if v.Name != "acme/exposed" || v.Visibility != "public" || v.State != "attention" {
		t.Errorf("repository = %+v", v)
	}
	if got := codesOf(v); !slices.Equal(got, []string{weakPool, publicOnFleet}) && !slices.Equal(got, []string{publicOnFleet, weakPool}) {
		t.Errorf("findings = %v, want the two the repository has", got)
	}
	if v.Counts.Error != 2 {
		t.Errorf("counts = %+v", v.Counts)
	}

	resp := s.do(request{method: http.MethodGet, path: kennelRepository + "kcr_nothing", cookie: s.viewer})
	resp.mustStatus(t, http.StatusNotFound, "an unknown repository")
	if got := resp.errorCode(t); got != codeNotFound {
		t.Errorf("code = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Recheck
// ---------------------------------------------------------------------------

// A recheck is 202: the repository is made due and the loop woken, and the reads
// happen when the budget allows. A second ask inside the cooldown is told how long
// to wait, in the header a client can act on.
func TestAskingForARecheckIsAcceptedOnceAndThenToldHowLongToWait(t *testing.T) {
	s := newKennelStack(t)
	resp := s.do(request{method: http.MethodPost, path: kennelRepository + s.exposed + "/recheck", cookie: s.operator})
	resp.mustStatus(t, http.StatusAccepted, "the first recheck")
	var v kennelRepositoryResponse
	resp.into(t, &v)
	if v.ID != s.exposed || v.NextDueAt != nil {
		t.Errorf("the answer = %+v, want the repository with its reads due now (no due date)", v)
	}
	row := lastAudit(t, s.harness, "kennel.recheck")
	if row == nil || row.TargetID != s.exposed || row.TargetKind != "kennel_repository" {
		t.Fatalf("audit row = %+v, want a recheck of the repository", row)
	}

	again := s.do(request{method: http.MethodPost, path: kennelRepository + s.exposed + "/recheck", cookie: s.operator})
	again.mustStatus(t, http.StatusTooManyRequests, "the second recheck")
	if got := again.errorCode(t); got != codeRateLimited {
		t.Errorf("code = %q, want %q", got, codeRateLimited)
	}
	// The first ask was a moment ago, so what is left of the five minutes is nearly
	// all of it. A constant would pass a looser bound.
	secs, err := strconv.Atoi(again.header.Get("Retry-After"))
	if cooldown := int(controller.KennelRecheckCooldown.Seconds()); err != nil || secs < cooldown-10 || secs > cooldown {
		t.Errorf("Retry-After = %q, want what is left of the %ds cooldown", again.header.Get("Retry-After"), cooldown)
	}

	other := s.do(request{method: http.MethodPost, path: kennelRepository + s.quiet + "/recheck", cookie: s.operator})
	other.mustStatus(t, http.StatusAccepted, "a different repository is not held by the first one's cooldown")

	missing := s.do(request{method: http.MethodPost, path: kennelRepository + "kcr_nothing/recheck", cookie: s.operator})
	missing.mustStatus(t, http.StatusNotFound, "recheck of an unknown repository")
}

// ---------------------------------------------------------------------------
// Waivers
// ---------------------------------------------------------------------------

// An error is a stranger running code on the fleet, and the decision that this is
// acceptable is the senior role's. A warning is the operator's. The refusal names
// the role the caller would need, and nothing is stored or audited for it.
func TestAnOperatorCannotWaiveAnErrorFindingAndAnAdminCan(t *testing.T) {
	s := newKennelStack(t)

	warn := s.waive(s.rebuilt, s.operator, waiverBody(publicOnFleet, goodReason))
	warn.mustStatus(t, http.StatusOK, "an operator waiving a warning")
	var after kennelRepositoryResponse
	warn.into(t, &after)
	if after.Counts.Warning != 0 || after.Counts.Waived != 1 || len(after.Waived) != 1 || len(after.Findings) != 0 {
		t.Errorf("after the waiver = %+v, want the warning waived and the repository answering for it", after.Counts)
	}
	if after.State != "best_in_show" {
		t.Errorf("state = %q: a waived warning is a decision, and nothing is open", after.State)
	}

	refused := s.waive(s.exposed, s.operator, waiverBody(weakPool, goodReason))
	refused.mustStatus(t, http.StatusForbidden, "an operator waiving an error")
	if got := refused.errorCode(t); got != codeForbidden {
		t.Errorf("code = %q, want %q", got, codeForbidden)
	}
	msg := refused.errorMessage(t)
	// The sentence ends with the role it takes and the one the caller has, which is
	// what auth.Explain says and what an operator who got this needs to read.
	for _, want := range []string{weakPool, "needs the admin role", "has operator"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not name %q", msg, want)
		}
	}
	if still := s.get(s.exposed); still.Counts.Error != 2 || still.Counts.Waived != 0 {
		t.Errorf("a refused waiver changed the repository: %+v", still.Counts)
	}

	ok := s.waive(s.exposed, s.admin, waiverBody(weakPool, goodReason))
	ok.mustStatus(t, http.StatusOK, "an admin waiving an error")
	var done kennelRepositoryResponse
	ok.into(t, &done)
	if done.Counts.Error != 1 || done.Counts.Waived != 1 {
		t.Errorf("counts after an admin's waiver = %+v", done.Counts)
	}
	if got := codesOf(done); !slices.Equal(got, []string{publicOnFleet}) {
		t.Errorf("findings = %v, want only the one that is not waived", got)
	}
}

func TestEveryWaiverIsAuditedWithTheReasonItWasGiven(t *testing.T) {
	s := newKennelStack(t)
	s.waive(s.rebuilt, s.operator, waiverBody(publicOnFleet, goodReason)).mustStatus(t, http.StatusOK, "waive")

	row := lastAudit(t, s.harness, "kennel.waive")
	if row == nil {
		t.Fatal("a waiver left no audit row")
	}
	if row.TargetID != s.rebuilt || row.TargetKind != "kennel_repository" || row.ActorName != "operator" {
		t.Errorf("audit row = %+v", row)
	}
	var after kennelWaiverAudit
	if err := json.Unmarshal([]byte(row.After), &after); err != nil {
		t.Fatalf("the audit row's after is not the waiver: %v\n%s", err, row.After)
	}
	if after.Reason != goodReason || after.Code != publicOnFleet || after.Severity != "warning" || after.By != "operator" || !strings.HasPrefix(after.ID, "kcw_") {
		t.Errorf("audited waiver = %+v, want the reason, the code, the severity at the time and who", after)
	}
}

// Making the same decision again renews it, so a person can lengthen a waiver
// without ending one and making another: the waiver keeps its ID.
func TestWaivingTheSameFindingAgainRenewsTheWaiver(t *testing.T) {
	s := newKennelStack(t)
	s.waive(s.rebuilt, s.operator, waiverBody(publicOnFleet, goodReason)).mustStatus(t, http.StatusOK, "waive")
	first := lastAudit(t, s.harness, "kennel.waive")

	renew := waiverBody(publicOnFleet, goodReason+", and so are its hosts")
	s.waive(s.rebuilt, s.operator, renew).mustStatus(t, http.StatusOK, "renew")
	var a, b kennelWaiverAudit
	_ = json.Unmarshal([]byte(first.After), &a)
	_ = json.Unmarshal([]byte(lastAudit(t, s.harness, "kennel.waive").After), &b)
	if a.ID == "" || a.ID != b.ID {
		t.Errorf("a renewal gave waiver %q, want the %q it had", b.ID, a.ID)
	}
	if got := s.get(s.rebuilt); len(got.Waived) != 1 {
		t.Errorf("waived = %d, want the one renewed", len(got.Waived))
	}
}

// Everything wrong with a request is said at once, so a form does not send a
// person round it three times.
func TestAWaiverThatIsWrongIsToldEveryFieldAtOnce(t *testing.T) {
	s := newKennelStack(t)
	resp := s.waive(s.rebuilt, s.operator, map[string]any{
		"code": "exposure.nonsense", "reason": "ok", "expires_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
	})
	resp.mustStatus(t, http.StatusUnprocessableEntity, "a waiver that is wrong three ways")
	var env errorEnvelope
	resp.into(t, &env)
	if env.Error.Code != codeUnprocessable {
		t.Errorf("code = %q", env.Error.Code)
	}
	var fields []string
	for _, f := range env.Errors {
		if f.Message == "" {
			t.Errorf("field %q has no message to show", f.Field)
		}
		fields = append(fields, f.Field)
	}
	slices.Sort(fields)
	if !slices.Equal(fields, []string{"code", "expires_at", "reason"}) {
		t.Errorf("fields = %v, want all three", fields)
	}
	if rows, _ := s.st.ListKennelWaivers(s.ctx, s.rebuilt); len(rows) != 0 {
		t.Errorf("a refused waiver was stored: %+v", rows)
	}
	if lastAudit(t, s.harness, "kennel.waive") != nil {
		t.Error("a refused waiver was audited as though it had happened")
	}
}

func TestAWaiverForAFindingThatDoesNotExistIsRefused(t *testing.T) {
	s := newKennelStack(t)
	// The code is real and the repository has no such finding: a waiver made
	// ahead of one is a standing exception nobody has looked at.
	resp := s.waive(s.quiet, s.admin, waiverBody(weakPool, goodReason))
	resp.mustStatus(t, http.StatusUnprocessableEntity, "a waiver for nothing")
	var env errorEnvelope
	resp.into(t, &env)
	if env.Error.Field != "code" {
		t.Errorf("error = %+v, want it to be about the code", env.Error)
	}
}

func TestABodyThatIsNotAWaiverIsABadRequest(t *testing.T) {
	s := newKennelStack(t)
	for name, raw := range map[string]string{
		"not json":          `{"code":`,
		"an unknown key":    `{"code":"` + publicOnFleet + `","reason":"` + goodReason + `","expires_at":"2099-01-01T00:00:00Z","severity":"info"}`,
		"a wrong type":      `{"code":7}`,
		"nothing at all":    ``,
		"an array":          `[]`,
		"a time that isn't": `{"code":"` + publicOnFleet + `","reason":"` + goodReason + `","expires_at":"tomorrow"}`,
	} {
		t.Run(name, func(t *testing.T) {
			resp := s.do(request{method: http.MethodPut, path: kennelRepository + s.rebuilt + "/waivers", cookie: s.operator, rawBody: raw,
				headers: map[string]string{"Content-Type": "application/json"}})
			resp.mustStatus(t, http.StatusBadRequest, name)
		})
	}
}

// A repository may carry fifty waivers, and the fifty-first is a conflict that
// says what to do about it, not an error nobody can act on.
func TestAWaiverBeyondTheLimitIsAConflictThatSaysWhatToDo(t *testing.T) {
	s := newKennelStack(t)
	for i := 0; i < controller.KennelMaxWaivers; i++ {
		if err := s.st.UpsertKennelWaiver(s.ctx, &store.KennelWaiver{
			RepositoryPK: s.rebuilt, Code: "capacity.unserved_label", Subject: "s" + strconv.Itoa(i), Severity: "warning",
			Reason: goodReason, CreatedBy: "usr_x", CreatedByName: "someone", ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("UpsertKennelWaiver: %v", err)
		}
	}
	resp := s.waive(s.rebuilt, s.operator, waiverBody(publicOnFleet, goodReason))
	resp.mustStatus(t, http.StatusConflict, "the fifty-first waiver")
	if msg := resp.errorMessage(t); !strings.Contains(msg, "50") || !strings.Contains(msg, "end one") {
		t.Errorf("message = %q, want the limit and what to do", msg)
	}
}

// ---------------------------------------------------------------------------
// Ending a waiver
// ---------------------------------------------------------------------------

// Ending a waiver only ever makes Kennel Club stricter -- the finding is open
// again -- so any operator may end any waiver, including an administrator's.
func TestAnOperatorCanEndAnAdministratorsWaiverAndTheFindingOpensAgain(t *testing.T) {
	s := newKennelStack(t)
	s.waive(s.exposed, s.admin, waiverBody(weakPool, goodReason)).mustStatus(t, http.StatusOK, "the admin's waiver")
	id := s.get(s.exposed).Waived[0].Waiver.ID
	if !strings.HasPrefix(id, "kcw_") {
		t.Fatalf("waiver id = %q", id)
	}

	resp := s.do(request{method: http.MethodDelete, path: kennelRepository + s.exposed + "/waivers/" + id, cookie: s.operator})
	resp.mustStatus(t, http.StatusOK, "an operator ending an admin's waiver")
	var v kennelRepositoryResponse
	resp.into(t, &v)
	if v.Counts.Error != 2 || v.Counts.Waived != 0 || len(v.Waived) != 0 {
		t.Errorf("the answer = %+v, want the finding open again, which is what the page repaints from", v.Counts)
	}

	row := lastAudit(t, s.harness, "kennel.unwaive")
	if row == nil || row.TargetID != s.exposed || row.ActorName != "operator" {
		t.Fatalf("audit row = %+v", row)
	}
	var before kennelWaiverAudit
	if err := json.Unmarshal([]byte(row.Before), &before); err != nil || before.ID != id || before.Reason != goodReason || before.By != "admin" {
		t.Errorf("the audit row's before = %s (%v), want the waiver that was ended, with its reason and who made it", row.Before, err)
	}
}

func TestAWaiverIsEndedThroughItsOwnRepositoryOrNotAtAll(t *testing.T) {
	s := newKennelStack(t)
	s.waive(s.exposed, s.admin, waiverBody(weakPool, goodReason)).mustStatus(t, http.StatusOK, "waive")
	id := s.get(s.exposed).Waived[0].Waiver.ID

	// Naming a different repository is a 404 and not a way to end somebody else's
	// decision by guessing at its ID.
	wrong := s.do(request{method: http.MethodDelete, path: kennelRepository + s.quiet + "/waivers/" + id, cookie: s.operator})
	wrong.mustStatus(t, http.StatusNotFound, "ending it through another repository")
	if got := s.get(s.exposed); got.Counts.Waived != 1 {
		t.Errorf("the waiver was ended through the wrong repository: %+v", got.Counts)
	}
	if lastAudit(t, s.harness, "kennel.unwaive") != nil {
		t.Error("a refused unwaive was audited as though it had happened")
	}

	right := s.do(request{method: http.MethodDelete, path: kennelRepository + s.exposed + "/waivers/" + id, cookie: s.operator})
	right.mustStatus(t, http.StatusOK, "ending it through its own")
	again := s.do(request{method: http.MethodDelete, path: kennelRepository + s.exposed + "/waivers/" + id, cookie: s.operator})
	again.mustStatus(t, http.StatusNotFound, "ending it a second time")
}

// ---------------------------------------------------------------------------
// Who may
// ---------------------------------------------------------------------------

func TestAViewerReadsKennelClubAndCannotChangeIt(t *testing.T) {
	s := newKennelStack(t)
	for _, tc := range []struct{ method, path string }{
		{"POST", kennelRepository + s.exposed + "/recheck"},
		{"PUT", kennelRepository + s.rebuilt + "/waivers"},
		{"DELETE", kennelRepository + s.rebuilt + "/waivers/kcw_x"},
	} {
		resp := s.do(request{method: tc.method, path: tc.path, cookie: s.viewer, body: waiverBody(publicOnFleet, goodReason)})
		resp.mustStatus(t, http.StatusForbidden, "a viewer: "+tc.method+" "+tc.path)
	}
	if rows, _ := s.st.ListKennelWaivers(s.ctx, s.rebuilt); len(rows) != 0 {
		t.Errorf("a viewer's refused waiver was stored: %+v", rows)
	}
}

// A token's scopes narrow its role, and never widen it: waiving an error takes
// the scope that names it, and the role to hold it.
func TestAScopedTokenWaivesNoMoreThanItsScopesAndItsRoleAllow(t *testing.T) {
	s := newKennelStack(t)
	waiveOnly := s.token("waive-only", store.RoleAdmin, "kennel:waive")
	waiveErrors := s.token("waive-errors", store.RoleAdmin, "kennel:waive_error")
	operatorWider := s.token("operator-errors", store.RoleOperator, "kennel:waive_error")
	readOnly := s.token("read-only", store.RoleAdmin, "kennel:read")

	put := func(token, id, code string) *response {
		return s.do(request{method: http.MethodPut, path: kennelRepository + id + "/waivers", token: token, body: waiverBody(code, goodReason)})
	}

	// An admin's token that may waive, but not waive errors, can do the first and not the second.
	put(waiveOnly, s.rebuilt, publicOnFleet).mustStatus(t, http.StatusOK, "kennel:waive on a warning")
	refused := put(waiveOnly, s.exposed, weakPool)
	refused.mustStatus(t, http.StatusForbidden, "kennel:waive on an error")
	if msg := refused.errorMessage(t); !strings.Contains(msg, "kennel:waive_error") {
		t.Errorf("message %q does not name the scope the token is missing", msg)
	}

	// The scope that names errors is enough on its own, because the route asks for the lesser one first.
	put(waiveErrors, s.exposed, weakPool).mustStatus(t, http.StatusOK, "kennel:waive_error on an error")

	// Holding the scope does not lend an operator the role.
	put(operatorWider, s.exposed, publicOnFleet).mustStatus(t, http.StatusForbidden, "an operator's token with kennel:waive_error")

	// Reading is not waiving.
	s.do(request{method: http.MethodGet, path: kennelBase, token: readOnly}).mustStatus(t, http.StatusOK, "kennel:read reads")
	put(readOnly, s.rebuilt, publicOnFleet).mustStatus(t, http.StatusForbidden, "kennel:read waiving")
	s.do(request{method: http.MethodPost, path: kennelRepository + s.quiet + "/recheck", token: readOnly}).
		mustStatus(t, http.StatusForbidden, "kennel:read rechecking")
}

// ---------------------------------------------------------------------------
// The contract
// ---------------------------------------------------------------------------

// The UI's client is generated from api/openapi.yaml, so a field the document
// does not describe is one it cannot see; and the same view is what the event
// stream carries, so this is also a check on the frames.
func TestKennelClubsResponsesMatchTheSpecShapes(t *testing.T) {
	doc := loadSpec(t)
	s := newKennelStack(t)
	s.waive(s.rebuilt, s.operator, waiverBody(publicOnFleet, goodReason)).mustStatus(t, http.StatusOK, "waive")
	// One repository nobody tracks, so the decision's words are in the documents
	// checked and not only the empty tracking of the ones that are.
	s.setTracking(s.quiet, request{cookie: s.admin}, stop(sandboxReason)).mustStatus(t, http.StatusOK, "stop")

	each := func(schema string, raws []json.RawMessage) {
		t.Helper()
		if len(raws) == 0 {
			t.Fatalf("%s: none to check, so this would pass on a document without them", schema)
		}
		for _, raw := range raws {
			assertShape(t, doc, schema, raw)
		}
	}
	// nested is the objects under key in each of the given documents.
	nested := func(docs []json.RawMessage, key string) []json.RawMessage {
		var out []json.RawMessage
		for _, d := range docs {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(d, &m); err != nil {
				t.Fatalf("%s is not an object: %v", d, err)
			}
			var items []json.RawMessage
			if err := json.Unmarshal(m[key], &items); err != nil {
				t.Fatalf("%s.%s is not an array: %v", d, key, err)
			}
			out = append(out, items...)
		}
		return out
	}

	t.Run("Repository", func(t *testing.T) {
		resp := s.do(request{method: http.MethodGet, path: kennelBase + "/repositories", cookie: s.viewer})
		resp.mustStatus(t, http.StatusOK, "list")
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		resp.into(t, &page)
		each("KennelRepository", page.Items)
		each("KennelFinding", nested(page.Items, "findings"))
		each("KennelWaived", nested(page.Items, "waived"))
		each("KennelCoverage", nested(page.Items, "coverage"))

		one := s.do(request{method: http.MethodGet, path: kennelRepository + s.exposed, cookie: s.viewer})
		one.mustStatus(t, http.StatusOK, "get")
		assertShape(t, doc, "KennelRepository", one.body)

		re := s.do(request{method: http.MethodPost, path: kennelRepository + s.exposed + "/recheck", cookie: s.operator})
		re.mustStatus(t, http.StatusAccepted, "recheck")
		assertShape(t, doc, "KennelRepository", re.body)
	})
	t.Run("Overview", func(t *testing.T) {
		resp := s.do(request{method: http.MethodGet, path: kennelBase, cookie: s.viewer})
		resp.mustStatus(t, http.StatusOK, "overview")
		assertShape(t, doc, "KennelOverview", resp.body)
		each("KennelCheck", nested([]json.RawMessage{resp.body}, "checks"))
		each("KennelAttention", nested([]json.RawMessage{resp.body}, "attention"))
		each("KennelCoverageSummary", nested([]json.RawMessage{resp.body}, "coverage"))
	})
	t.Run("Catalogue", func(t *testing.T) {
		resp := s.do(request{method: http.MethodGet, path: kennelBase + "/checks", cookie: s.viewer})
		resp.mustStatus(t, http.StatusOK, "checks")
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		resp.into(t, &page)
		each("KennelCatalogueEntry", page.Items)
		each("KennelNeed", nested(page.Items, "needs"))
	})
}

// What the audit row records of a waiver is the waiver as it was decided: why it
// was thought acceptable is the one thing somebody reading the log in a year will
// want, and a field dropped here is one nobody can ask for later.
func TestTheAuditRecordOfAWaiverCarriesEverythingThatWasDecided(t *testing.T) {
	end := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	got := waiverAudit(&store.KennelWaiver{
		ID: "kcw_1", RepositoryPK: "kcr_1", Code: weakPool, Subject: "linux-x64", Severity: "error",
		Reason: goodReason, CreatedBy: "usr_1", CreatedByName: "Ada", ExpiresAt: end,
	})
	want := kennelWaiverAudit{ID: "kcw_1", Code: weakPool, Subject: "linux-x64", Severity: "error", Reason: goodReason, By: "Ada", ExpiresAt: end}
	if got != want {
		t.Errorf("audit = %+v, want %+v", got, want)
	}
}
