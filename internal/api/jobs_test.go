package api

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// The Jobs page asks for managed=true by default, so the parameter has to reach
// the store: GitHub reports every job in an installed repository, and a fleet
// view that silently includes hosted-runner jobs answers "how is my fleet
// doing?" with somebody else's numbers.
func TestListJobsManagedLeavesOutJobsThisFleetNeverRan(t *testing.T) {
	h := newHarness(t)
	inst := h.installation()
	pool := h.pool(inst, "linux")
	mine := h.job(pool, store.JobCompleted)

	now := time.Now().Add(-time.Minute)
	started := now.Add(time.Second)
	done := now.Add(time.Second * 30)
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 99, GitHubRunID: 99, Repo: "acme/widgets", Workflow: "ci",
		JobName: "hosted", Labels: store.StringSlice{"ubuntu-latest"},
		State: store.JobCompleted, Conclusion: "success",
		QueuedAt: now, StartedAt: &started, CompletedAt: &done,
	}); err != nil {
		t.Fatalf("recording the hosted job: %v", err)
	}

	_, cookie := h.user("viewer", store.RoleViewer)

	var page struct {
		Items []struct {
			ID      string `json:"id"`
			JobName string `json:"job_name"`
		} `json:"items"`
		Total int `json:"total"`
	}
	resp := h.do(request{method: "GET", path: "/api/v1/jobs?managed=true", cookie: cookie})
	resp.mustStatus(t, 200, "listing managed jobs")
	resp.into(t, &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != mine.ID {
		t.Fatalf("managed jobs = %+v (total %d), want only %s", page.Items, page.Total, mine.ID)
	}

	resp = h.do(request{method: "GET", path: "/api/v1/jobs", cookie: cookie})
	resp.mustStatus(t, 200, "listing every job")
	resp.into(t, &page)
	if page.Total != 2 {
		t.Fatalf("unfiltered total = %d, want both jobs -- the toggle has to be able to show them", page.Total)
	}
}
