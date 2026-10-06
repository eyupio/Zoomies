package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// seedJob completes a job of the given name at an offset into the past, with the
// steps it ran, which is all the trend problems read.
func seedJob(t *testing.T, h *harness, inst string, id int64, name, conclusion string, ago, took time.Duration, failedStep string, labels ...string) {
	t.Helper()
	if len(labels) == 0 {
		labels = []string{"self-hosted", "linux", "x64"}
	}
	queued := h.c.Now().Add(-ago)
	started, done := queued.Add(2*time.Second), queued.Add(2*time.Second+took)
	job := &store.Job{
		GitHubJobID: id, GitHubRunID: id, Repo: "acme/widgets", Workflow: "ci", JobName: name, Labels: store.StringSlice(labels),
		State: store.JobCompleted, Conclusion: conclusion, Matched: true, InstallationID: inst,
		QueuedAt: queued, StartedAt: &started, CompletedAt: &done,
	}
	if failedStep != "" {
		job.Steps = []store.JobStep{{Number: 1, Name: "Set up job", Conclusion: "success"}, {Number: 2, Name: failedStep, Conclusion: "failure"}}
	}
	if _, err := h.st.UpsertJob(h.ctx, job); err != nil {
		t.Fatal(err)
	}
}

// Three failures at the same step with nothing passing is the pattern worth
// saying; the same job passing once, or three different steps, is an ordinary bad day.
func TestAJobFailingAtTheSameStepRepeatedlyIsSaidOnceAsInformation(t *testing.T) {
	h := newHarness(t)
	inst := h.installation().ID
	for i := 0; i < 3; i++ {
		seedJob(t, h, inst, int64(100+i), "generate", "failure", time.Duration(i+1)*20*time.Minute, 20*time.Second, "Run actions/upload-artifact@v4")
	}
	p := h.problemOrNil("jobs.workflow_failing")
	if p == nil {
		t.Fatal("no jobs.workflow_failing problem")
	}
	if p.Remedy != nil || p.TargetKind != "job" {
		t.Errorf("problem = %+v; want information about a job with no remedy", p)
	}
	for _, want := range []string{"upload-artifact", "3 times"} {
		if !strings.Contains(p.Title+p.Detail, want) {
			t.Errorf("the problem should say %q: %s / %s", want, p.Title, p.Detail)
		}
	}
}

func TestAJobThatPassedSinceIsNotSaidToBeFailing(t *testing.T) {
	h := newHarness(t)
	inst := h.installation().ID
	for i := 0; i < 3; i++ {
		seedJob(t, h, inst, int64(200+i), "generate", "failure", time.Duration(i+2)*20*time.Minute, 20*time.Second, "Run actions/upload-artifact@v4")
	}
	seedJob(t, h, inst, 299, "generate", "success", 5*time.Minute, 30*time.Second, "")
	if p := h.problemOrNil("jobs.workflow_failing"); p != nil {
		t.Errorf("a job that has since passed was reported: %+v", p)
	}
}

func TestDifferentStepsAreNotOnePattern(t *testing.T) {
	h := newHarness(t)
	inst := h.installation().ID
	for i, step := range []string{"Build", "Test", "Lint"} {
		seedJob(t, h, inst, int64(300+i), "generate", "failure", time.Duration(i+1)*20*time.Minute, 20*time.Second, step)
	}
	if p := h.problemOrNil("jobs.workflow_failing"); p != nil {
		t.Errorf("three different failures were reported as one pattern: %+v", p)
	}
}

// A job that took twice as long as the week before is said; one that crept up a
// little, or has too few runs to tell, is not.
func TestAJobThatGotMuchSlowerIsSaidAsInformation(t *testing.T) {
	h := newHarness(t)
	inst := h.installation().ID
	n := int64(1000)
	for i := 0; i < 12; i++ {
		n++
		seedJob(t, h, inst, n, "Go (api)", "success", 2*24*time.Hour+time.Duration(i)*time.Hour, 5*time.Minute, "")
		n++
		seedJob(t, h, inst, n, "Go (api)", "success", time.Duration(i+1)*time.Hour, 11*time.Minute, "")
	}
	p := h.problemOrNil("jobs.duration_regressed")
	if p == nil {
		t.Fatal("no jobs.duration_regressed problem")
	}
	if p.Remedy != nil || !strings.Contains(p.Title, "Go (api)") || !strings.Contains(p.Title, "11m0s") || !strings.Contains(p.Title, "5m0s") {
		t.Errorf("problem = %+v", p)
	}
}

func TestAJobWithTooFewRunsOrTooSmallAChangeIsNotSaidToBeSlower(t *testing.T) {
	h := newHarness(t)
	inst := h.installation().ID
	n := int64(2000)
	// Few runs: a median of three says nothing.
	for i := 0; i < 3; i++ {
		n++
		seedJob(t, h, inst, n, "rare", "success", 2*24*time.Hour+time.Duration(i)*time.Hour, 5*time.Minute, "")
		n++
		seedJob(t, h, inst, n, "rare", "success", time.Duration(i+1)*time.Hour, 15*time.Minute, "")
	}
	// A big ratio on a tiny job: nobody waits for ten seconds going to twenty.
	for i := 0; i < 12; i++ {
		n++
		seedJob(t, h, inst, n, "tiny", "success", 2*24*time.Hour+time.Duration(i)*time.Hour, 10*time.Second, "")
		n++
		seedJob(t, h, inst, n, "tiny", "success", time.Duration(i+1)*time.Hour, 20*time.Second, "")
	}
	if p := h.problemOrNil("jobs.duration_regressed"); p != nil {
		t.Errorf("reported %+v", p)
	}
}
