package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/store"
)

// The failure this was built for, end to end: a heavy job runs on a small
// host, the agent's samples record what it used, a step is killed for memory
// and the runner finishes the job anyway. The job has to come out of it with
// its peaks, marked as the fleet's failure, explained as a memory kill, and
// listed as a problem on the host -- because GitHub will call it a failed
// step like any other.
func TestAJobKilledForMemoryIsRecordedExplainedAndReported(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := h.seedRunner(t, pool, host, store.RunnerBusy)
	started := h.c.Now().Add(-time.Hour)
	job, err := h.st.UpsertJob(h.ctx, &store.Job{
		GitHubJobID: 4242, Repo: "eyupio/zoomies", Workflow: "CI", JobName: "Go (controller)",
		Labels: pool.Labels, State: store.JobInProgress, PoolID: pool.ID, Matched: true,
		RunnerID: r.ID, RunnerName: r.Name, QueuedAt: started, StartedAt: &started, HostID: host.ID,
	})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	at := h.c.Now()
	for _, s := range []backend.Stats{
		{SampledAt: &at, CPUPercent: 390, MemoryBytes: 2900 << 20},
		{SampledAt: &at, CPUPercent: 120, MemoryBytes: 1200 << 20},
	} {
		if err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{RunnerID: r.ID, Stats: s}}); err != nil {
			t.Fatalf("ReportRunners: %v", err)
		}
	}

	done := h.c.Now()
	if _, err := h.st.UpsertJob(h.ctx, &store.Job{GitHubJobID: 4242, State: store.JobCompleted, Conclusion: "failure",
		Repo: job.Repo, StartedAt: &started, CompletedAt: &done}); err != nil {
		t.Fatalf("completing the job: %v", err)
	}
	if err := h.c.ReportRunners(h.ctx, host.ID, []agent.RunnerReport{{
		RunnerID: r.ID, State: store.RunnerRemoved, Fault: store.FaultOutOfMemory,
		Message: "runner exited after its job, but the kernel killed a process in it for its memory limit",
	}}); err != nil {
		t.Fatalf("ReportRunners: %v", err)
	}

	got, err := h.st.GetJob(h.ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OOMKilled || got.FaultKind != store.FaultOutOfMemory || got.PeakMemoryMB != 2900 || got.PeakCPUs != 3.9 {
		t.Fatalf("job = oom %t, fault %q, peaks %v CPU %d MB", got.OOMKilled, got.FaultKind, got.PeakCPUs, got.PeakMemoryMB)
	}

	// The view is the API's shape, and the UI and the MCP tools read these
	// three fields from it.
	raw, err := json.Marshal(NewJobView(got, pool.Name))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"peak_cpus":3.9`, `"peak_memory_mb":2900`, `"oom_killed":true`} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("the job view has no %s: %s", field, raw)
		}
	}

	events, err := h.st.ListJobEvents(h.ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	kills := 0
	for _, e := range events {
		if e.Kind == store.JobEventOOMKilled {
			kills++
		}
	}
	if kills != 1 {
		t.Fatalf("timeline has %d memory kills, want 1", kills)
	}

	ex, err := h.c.ExplainJob(h.ctx, job.ID)
	if err != nil {
		t.Fatalf("ExplainJob: %v", err)
	}
	if !strings.Contains(ex.Summary, "for its memory limit") || !strings.Contains(ex.Summary, "2.8 GB") {
		t.Errorf("the explanation does not lead with the kill: %q", ex.Summary)
	}

	problems, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range problems {
		if p.Code == "jobs.oom_killed" {
			found = true
			if !strings.Contains(p.Title, host.ID) {
				t.Errorf("the problem does not name the host: %q", p.Title)
			}
		}
	}
	if !found {
		t.Error("no jobs.oom_killed problem was raised")
	}
}
