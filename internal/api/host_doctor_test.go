package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

func TestHostAPIIncludesTheLatestDoctorResult(t *testing.T) {
	h := newHarness(t)
	host := h.host("health-host")
	r := &hosttune.Report{CheckedAt: h.ctrl.Now(), OS: "linux", Distro: "ubuntu 24.04", Results: []hosttune.Result{{ID: "disk.space", Title: "Disk space", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "2%", Recommended: "10%", Rationale: "Leave room for builds."}}}
	if _, err := h.ctrl.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
		t.Fatal(err)
	}
	u, _ := h.user("reader", store.RoleViewer)
	res := h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, cookie: h.session(u)})
	res.mustStatus(t, http.StatusOK, "doctor report")
	var v hostResponse
	res.into(t, &v)
	if v.Doctor == nil || len(v.Doctor.Results) != 1 {
		t.Fatalf("missing report: %+v", v.Doctor)
	}
}

// The count beside the results is the controller's, and the document says so:
// the doctor a client reads has every key HostDoctorView describes, and none it
// does not, on the single-host route and in the list. The generated client types
// doctor from that schema, so a key it lacks is one the UI cannot read.
func TestHostAPIRendersTheCountBesideTheResultsAsTheSpecDescribesIt(t *testing.T) {
	doc := loadSpec(t)
	h := newHarness(t)
	host := h.host("counted-host")
	r := &hosttune.Report{
		CheckedAt: h.ctrl.Now(), OS: "linux", Distro: "ubuntu 24.04", WorkDir: "/var/lib/zoomies",
		Results: []hosttune.Result{
			{ID: "disk.space", Title: "Disk space", Tier: hosttune.Safe, Status: hosttune.Warn, Current: "2%", Recommended: "10%", Rationale: "Leave room for builds."},
			{ID: "files.watches", Title: "File watches", Tier: hosttune.Safe, Status: hosttune.OK},
			{ID: "tmp.tmpfs", Title: "Temporary files", Tier: hosttune.Aggressive, Status: hosttune.Warn},
		},
	}
	if _, err := h.ctrl.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
		t.Fatal(err)
	}
	u, _ := h.user("reader", store.RoleViewer)
	cookie := h.session(u)

	one := h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, cookie: cookie})
	one.mustStatus(t, http.StatusOK, "one host")
	list := h.do(request{method: http.MethodGet, path: "/api/v1/hosts", cookie: cookie})
	list.mustStatus(t, http.StatusOK, "list hosts")
	var listed struct {
		Items []json.RawMessage `json:"items"`
	}
	list.into(t, &listed)
	var inList json.RawMessage
	for _, item := range listed.Items {
		var id struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(item, &id); err == nil && id.ID == host.ID {
			inList = item
		}
	}
	if inList == nil {
		t.Fatalf("the host is not in the list: %s", truncate(list.body))
	}

	for name, body := range map[string]json.RawMessage{"GET /hosts/{id}": one.body, "GET /hosts": inList} {
		var payload struct {
			Doctor json.RawMessage `json:"doctor"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Doctor == nil {
			t.Fatalf("%s: no doctor in the host: %s", name, truncate(body))
		}
		assertShape(t, doc, "HostDoctorView", payload.Doctor)

		var doctor struct {
			Results []hosttune.Result          `json:"results"`
			Summary map[string]json.RawMessage `json:"summary"`
		}
		if err := json.Unmarshal(payload.Doctor, &doctor); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(doctor.Results, r.Results) {
			t.Errorf("%s: results = %+v, want the report's own, in full, so the count adds to them", name, doctor.Results)
		}
		summary, err := json.Marshal(doctor.Summary)
		if err != nil {
			t.Fatal(err)
		}
		assertShape(t, doc, "HostDoctorSummary", summary)
		var got hosttune.Summary
		if err := json.Unmarshal(summary, &got); err != nil {
			t.Fatal(err)
		}
		// One counted warning, one counted pass, and the aggressive warning
		// a suggestion that is in no other number.
		if want := (hosttune.Summary{Counted: 2, Warnings: 1, Suggestions: 1}); got != want {
			t.Errorf("%s: summary = %+v, want %+v", name, got, want)
		}
	}
}

// A host that has sent no report has no doctor key: an object of zeros would be
// read as a host with nothing to fail.
func TestHostAPIOmitsDoctorForAHostThatHasSentNoReport(t *testing.T) {
	h := newHarness(t)
	host := h.host("silent-host")
	u, _ := h.user("reader", store.RoleViewer)
	res := h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + host.ID, cookie: h.session(u)})
	res.mustStatus(t, http.StatusOK, "host with no report")
	if v, ok := res.json(t)["doctor"]; ok {
		t.Errorf("a host with no report has doctor = %v, want the key absent", v)
	}
}

// An agent writes the report and the controller counts it. The agent protocol
// tolerates unknown fields, so a heartbeat that sends a summary of its own is
// not refused -- it is simply not believed: nothing reads it, nothing stores it,
// and the host's page shows the count of the results it sent. A host that could
// claim "nothing is wrong" in its own words would defeat the point of counting
// it in one place.
func TestAHeartbeatThatSendsItsOwnSummaryHasItIgnoredAndNeverStoresIt(t *testing.T) {
	h := newHarness(t)
	hostID, token := h.agentToken("vm-claims-health")
	checked := h.ctrl.Now()
	resp := h.do(request{method: http.MethodPost, path: agent.PathHeartbeat, token: token, body: map[string]any{
		"protocol_version": agent.ProtocolVersion,
		"capacity":         2,
		"version":          "test",
		"doctor": map[string]any{
			"checked_at":     checked,
			"os":             "linux",
			"distro":         "ubuntu 24.04",
			"container":      false,
			"reboot_pending": false,
			"results": []map[string]any{{
				"id": "disk.space", "title": "Disk space", "tier": "safe", "status": "warn",
				"current": "2%", "recommended": "10%", "rationale": "Leave room for builds.", "actionable": false,
			}},
			// The claim: fifty checks, every one of them fine.
			"summary": map[string]any{"counted": 50, "warnings": 0, "errors": 0, "skipped": 0, "suggestions": 0},
		},
	}})
	resp.mustStatus(t, http.StatusOK, "heartbeat that claims its own summary")

	u, _ := h.user("reader", store.RoleViewer)
	got := h.do(request{method: http.MethodGet, path: "/api/v1/hosts/" + hostID, cookie: h.session(u)})
	got.mustStatus(t, http.StatusOK, "host")
	var v hostResponse
	got.into(t, &v)
	if v.Doctor == nil {
		t.Fatal("the report was not stored")
	}
	if want := (hosttune.Summary{Counted: 1, Warnings: 1}); v.Doctor.Summary != want {
		t.Errorf("summary = %+v, want the controller's own count %+v and not the host's claim", v.Doctor.Summary, want)
	}

	// What is on disk is the report as the protocol defines it.
	stored, err := h.st.GetHost(h.ctx, hostID)
	if err != nil {
		t.Fatal(err)
	}
	column, err := stored.Doctor.Value()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(column.(string)), &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["summary"]; ok {
		t.Errorf("the host's summary was stored: %s", column)
	}
}

// docs/host-health.md says which scopes reach a host's check detail, and a token
// that must not see it is minted from that list. A route the page leaves out, or
// a scope it names that does not open one, is how an operator ends up holding a
// token that reads every host's kernel version and filesystem paths while
// believing it cannot. So the list is held to the routes, and the problems --
// which name a failing check and never carry a value -- are held to that too.
func TestTheCheckDetailReachesTheScopesTheDocsNameAndNoOther(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	host := h.host("detail-host")
	run := h.runner(h.pool(inst, "linux-x64"), host, store.RunnerIdle)
	const value = "9% free of /srv/private/builds"
	r := &hosttune.Report{
		CheckedAt: h.ctrl.Now(), OS: "linux", Distro: "ubuntu 24.04",
		Results: []hosttune.Result{{
			ID: "disk.space", Title: "Disk space", Tier: hosttune.Safe, Status: hosttune.Warn,
			Current: value, Recommended: "10%", Rationale: "Leave room for builds.",
		}},
	}
	if _, err := h.ctrl.Heartbeat(h.ctx, host.ID, agent.HeartbeatRequest{ProtocolVersion: agent.ProtocolVersion, Doctor: r}); err != nil {
		t.Fatal(err)
	}
	title, ok := hosttune.Title("disk.space")
	if !ok {
		t.Fatal("the controller does not know disk.space, so no problem could name it")
	}

	const (
		hostPath    = "/api/v1/hosts/"
		runnerPath  = "/api/v1/runners/"
		problemPath = "/api/v1/problems"
	)
	cases := []struct {
		scope string
		// Which of the three routes the scope opens, and so whether the value
		// is in the body: the first two carry the host's results, the problems
		// carry only the name of the check.
		host, runner, problems bool
	}{
		{scope: "hosts:read", host: true},
		// Any scope on a resource includes reading it, so a token that may only
		// cordon hosts reads them too, and the docs say so.
		{scope: "hosts:cordon", host: true},
		// A runner's detail carries its host, whole.
		{scope: "runners:read", runner: true},
		{scope: "stats:read", problems: true},
		{scope: "pools:read"},
	}
	for _, tc := range cases {
		t.Run(tc.scope, func(t *testing.T) {
			token := h.token("reads "+tc.scope, store.RoleViewer, tc.scope)
			for _, route := range []struct {
				path string
				open bool
			}{
				{hostPath + host.ID, tc.host},
				{runnerPath + run.ID, tc.runner},
				{problemPath, tc.problems},
			} {
				res := h.do(request{method: http.MethodGet, path: route.path, token: token})
				if !route.open {
					res.mustStatus(t, http.StatusForbidden, tc.scope+" on "+route.path)
					continue
				}
				res.mustStatus(t, http.StatusOK, tc.scope+" on "+route.path)
				carriesValue := strings.Contains(string(res.body), value)
				if isProblems := route.path == problemPath; carriesValue == isProblems {
					t.Errorf("%s on %s: the check's value is in the body = %v; the host routes carry it and the problems never do",
						tc.scope, route.path, carriesValue)
				}
				if route.path != problemPath {
					continue
				}
				var got struct {
					Items []struct {
						Code   string `json:"code"`
						Detail string `json:"detail"`
					} `json:"items"`
				}
				res.into(t, &got)
				found := false
				for _, p := range got.Items {
					if p.Code == "host.os_health" {
						found = strings.Contains(p.Detail, title)
					}
				}
				if !found {
					t.Errorf("%s on the problems: no host.os_health entry naming %q: %s", tc.scope, title, truncate(res.body))
				}
			}
		})
	}
}
