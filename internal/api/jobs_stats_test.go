package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// releaseJobs records n completed jobs under one release, each with a set of
// steps like a real workflow's, so that the size of a listing means something.
func (h *harness) releaseJobs(pool *store.Pool, version string, n int, firstID int64, queuedAt time.Time) []*store.Job {
	h.t.Helper()
	var out []*store.Job
	for i := 0; i < n; i++ {
		queued := queuedAt.Add(time.Duration(i) * time.Second)
		started, done := queued.Add(5*time.Second), queued.Add(65*time.Second)
		steps := store.JobSteps{}
		for s := 1; s <= 12; s++ {
			steps = append(steps, store.JobStep{Number: s, Name: fmt.Sprintf("Step %d of the build", s),
				Status: "completed", Conclusion: "success", StartedAt: &started, CompletedAt: &done})
		}
		j, _, err := h.st.ApplyJob(h.ctx, &store.Job{
			GitHubJobID: firstID + int64(i), GitHubRunID: 1, Repo: "acme/widgets", Workflow: "ci", JobName: "build",
			Labels: store.StringSlice{"self-hosted", "linux", "x64"}, State: store.JobCompleted, Conclusion: "success",
			InstallationID: pool.InstallationID, PoolID: pool.ID, Matched: true, RunnerName: "zoomies-abc",
			QueuedAt: queued, StartedAt: &started, CompletedAt: &done, Steps: steps,
			HeadBranch: "main", HeadSHA: "0123456789abcdef0123456789abcdef01234567",
		})
		if err != nil {
			h.t.Fatal(err)
		}
		if version != "" {
			if j, err = h.st.StampJobVersions(h.ctx, j.ID, store.JobVersions{
				ControllerVersion: version, ControllerChannel: "stable", AgentVersion: version, HostID: "host_1"}); err != nil {
				h.t.Fatal(err)
			}
		}
		out = append(out, j)
	}
	return out
}

// The question this exists for: did builds get faster and more stable on later
// releases, answered in one call.
func TestJobStatsGroupsReleasesInOneCall(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "linux")
	_, cookie := h.user("viewer", store.RoleViewer)
	now := time.Now()
	h.releaseJobs(pool, "v1.2.0", 4, 1_000, now.Add(-20*24*time.Hour))
	h.releaseJobs(pool, "v1.3.0", 4, 2_000, now.Add(-5*24*time.Hour))
	h.releaseJobs(pool, "", 2, 3_000, now.Add(-30*24*time.Hour))

	resp := h.do(request{method: http.MethodGet, path: "/api/v1/jobs/stats?group_by=controller_version&since=" +
		url.QueryEscape(now.Add(-40*24*time.Hour).Format(time.RFC3339)), cookie: cookie})
	resp.mustStatus(t, http.StatusOK, "job statistics for a viewer")
	var got struct {
		GroupBy          []string `json:"group_by"`
		DurationExcludes []string `json:"duration_excludes"`
		Notes            []string `json:"notes"`
		Groups           []struct {
			Keys     map[string]string `json:"keys"`
			Count    int               `json:"count"`
			Duration struct {
				P50 *int64 `json:"p50_ms"`
				P95 *int64 `json:"p95_ms"`
			} `json:"duration"`
			QueueWait struct {
				P50 *int64 `json:"p50_ms"`
			} `json:"queue_wait"`
			Startup struct {
				Samples int    `json:"samples"`
				P50     *int64 `json:"p50_ms"`
			} `json:"startup"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("%v: %s", err, resp.body)
	}
	var order []string
	for _, g := range got.Groups {
		order = append(order, g.Keys["controller_version"])
	}
	if strings.Join(order, ",") != "unknown,v1.2.0,v1.3.0" {
		t.Fatalf("groups = %v, want unknown, v1.2.0 and v1.3.0 in the order they were first seen", order)
	}
	for _, g := range got.Groups {
		if g.Duration.P50 == nil || *g.Duration.P50 != 60_000 || *g.Duration.P95 != 60_000 || *g.QueueWait.P50 != 5_000 {
			t.Errorf("%s: duration %v/%v, wait %v; want 60s and 5s", g.Keys["controller_version"], g.Duration.P50, g.Duration.P95, g.QueueWait.P50)
		}
		if g.Startup.Samples != 0 || g.Startup.P50 != nil {
			t.Errorf("%s: startup %+v, want no figures with no runner rows", g.Keys["controller_version"], g.Startup)
		}
	}
	if len(got.DurationExcludes) != 2 || !strings.Contains(strings.Join(got.Notes, " "), "cancelled and skipped") {
		t.Errorf("the response must say what durations leave out: %v / %v", got.DurationExcludes, got.Notes)
	}
}

func TestJobStatsRefusesWhatItCannotAnswer(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Limits.JobStatsWindow = 48 * time.Hour })
	_, cookie := h.user("viewer", store.RoleViewer)
	now := time.Now()
	ago := func(d time.Duration) string { return url.QueryEscape(now.Add(-d).Format(time.RFC3339)) }
	for name, tc := range map[string]struct{ query, want string }{
		"a window over the limit":  {"since=" + ago(72*time.Hour), "limits.job_stats_window"},
		"three keys":               {"group_by=day,host,pool", "at most two"},
		"an unknown key":           {"group_by=repo", "controller_version, day, host, pool, job_name"},
		"the same key twice":       {"group_by=day&group_by=day", "named twice"},
		"an end before the start":  {"since=" + ago(time.Hour) + "&until=" + ago(2*time.Hour), "start must be before end"},
		"a hosted that is no bool": {"hosted=flase", "true or false"},
	} {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/jobs/stats?" + tc.query, cookie: cookie})
		resp.mustStatus(t, http.StatusBadRequest, name)
		if !strings.Contains(string(resp.body), tc.want) {
			t.Errorf("%s: %s, want it to mention %q", name, resp.body, tc.want)
		}
	}
	h.do(request{method: http.MethodGet, path: "/api/v1/jobs/stats?since=" + ago(47*time.Hour), cookie: cookie}).
		mustStatus(t, http.StatusOK, "a window inside the limit")
}

// Listing a job's history must not change for a caller that sends none of the
// new parameters: full jobs, steps and all, and the same envelope.
func TestJobListingKeepsItsShapeAndOffersACursor(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "linux")
	_, cookie := h.user("viewer", store.RoleViewer)
	h.releaseJobs(pool, "v1.3.0", 5, 1_000, time.Now().Add(-time.Hour))

	list := func(query string) (items []map[string]any, next string, status int) {
		resp := h.do(request{method: http.MethodGet, path: "/api/v1/jobs?" + query, cookie: cookie})
		var body struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
			Next  string           `json:"next"`
		}
		_ = json.Unmarshal(resp.body, &body)
		return body.Items, body.Next, resp.status
	}

	items, next, status := list("limit=2")
	if status != http.StatusOK || len(items) != 2 {
		t.Fatalf("status %d, %d items", status, len(items))
	}
	if steps, ok := items[0]["steps"].([]any); !ok || len(steps) != 12 {
		t.Errorf("the default listing lost its steps: %v", items[0]["steps"])
	}
	if items[0]["controller_version"] != "v1.3.0" || items[0]["host_id"] != "host_1" {
		t.Errorf("a job must show its stamps: %v", items[0])
	}
	if next == "" {
		t.Fatal("a page with more behind it must carry a cursor")
	}

	seen := map[string]bool{}
	for _, it := range items {
		seen[it["id"].(string)] = true
	}
	for next != "" {
		var page []map[string]any
		page, next, status = list("limit=2&include_steps=false&before=" + url.QueryEscape(next))
		if status != http.StatusOK {
			t.Fatalf("cursor page: status %d", status)
		}
		for _, it := range page {
			if _, has := it["steps"]; has {
				t.Errorf("a summary carries steps: %v", it)
			}
			id := it["id"].(string)
			if seen[id] {
				t.Errorf("%s came twice", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 5 {
		t.Errorf("paging reached %d of the 5 jobs", len(seen))
	}

	for name, query := range map[string]string{
		"a made-up cursor":        "before=nonsense",
		"a cursor with an offset": "before=" + url.QueryEscape(store.JobCursor{QueuedAt: time.Now(), ID: "job_x"}.Encode()) + "&offset=5",
		"include_steps as a word": "include_steps=maybe",
	} {
		if _, _, status := list(query); status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, status)
		}
	}
}

// A matrix job's name has a comma in it, so a filter that split on commas
// would match nothing while looking as though it worked.
func TestJobNameFilterIsExactAndKeepsItsCommas(t *testing.T) {
	h := newHarness(t)
	pool := h.pool(h.installation(), "linux")
	_, cookie := h.user("viewer", store.RoleViewer)
	name := "test (ubuntu-latest, 3.12)"
	for i, n := range []string{name, "test", "test (macos, 3.12)"} {
		if _, err := h.st.UpsertJob(h.ctx, &store.Job{GitHubJobID: int64(500 + i), JobName: n, State: store.JobQueued,
			Labels: store.StringSlice{"self-hosted"}, PoolID: pool.ID, Matched: true, QueuedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	resp := h.do(request{method: http.MethodGet, path: "/api/v1/jobs?job_name=" + url.QueryEscape(name), cookie: cookie})
	var body struct {
		Items []struct {
			JobName string `json:"job_name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(resp.body, &body); err != nil || len(body.Items) != 1 || body.Items[0].JobName != name {
		t.Fatalf("job_name=%q returned %s", name, resp.body)
	}
}
