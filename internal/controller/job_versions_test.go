package controller

import (
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// A job is stamped where the fleet first has a claim on it -- the controller's
// build when a pool matches it, the host and its agent when a runner takes it --
// and an upgrade half way through must not move it to another release.
func TestAJobIsStampedAtClaimAndAtPickupAndNeverAgain(t *testing.T) {
	h := newHarness(t)
	_, _, host := h.fleet()
	host.Version = "v1.3.1"
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatal(err)
	}
	labels := []string{"self-hosted", "linux", "x64", "demo"}

	h.deliverJob(jobEvent{Action: "queued", JobID: 9301, Labels: labels})
	job, err := h.st.GetJobByGitHubID(h.ctx, 9301)
	if err != nil {
		t.Fatal(err)
	}
	if job.ControllerVersion != version.Version || job.ControllerChannel != version.Channel(version.Version) {
		t.Fatalf("claimed job carries %q/%q, want this build's %q", job.ControllerVersion, job.ControllerChannel, version.Version)
	}
	if job.HostID != "" || job.AgentVersion != "" {
		t.Fatalf("a job with no runner yet has a host: %+v", job)
	}

	// The controller is upgraded while the job runs.
	defer func(v string) { version.Version = v }(version.Version)
	version.Version = "v9.9.9"

	if err := h.c.Reconcile(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.advance(5 * time.Second)
	if _, err := h.c.PollTasks(h.ctx, host.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	r := h.onlyRunner()
	h.deliverJob(jobEvent{Action: "in_progress", JobID: 9301, RunnerName: r.Name, Labels: labels})
	h.deliverJob(jobEvent{Action: "completed", JobID: 9301, RunnerName: r.Name, Labels: labels, Conclusion: "success"})

	got, err := h.st.GetJobByGitHubID(h.ctx, 9301)
	if err != nil {
		t.Fatal(err)
	}
	if got.HostID != host.ID || got.AgentVersion != "v1.3.1" {
		t.Errorf("picked-up job is on %q at agent %q, want %q at v1.3.1", got.HostID, got.AgentVersion, host.ID)
	}
	if got.ControllerVersion == "v9.9.9" || got.ControllerVersion != job.ControllerVersion {
		t.Errorf("the controller's release moved from %q to %q on later deliveries", job.ControllerVersion, got.ControllerVersion)
	}
	if got.State != store.JobCompleted {
		t.Errorf("state = %s", got.State)
	}
}

// Hosted work belongs to somebody else's runners, so crediting it to a release
// of this controller would put numbers it never produced into that release.
func TestAJobNoPoolClaimsIsNotStamped(t *testing.T) {
	h := newHarness(t)
	h.fleet()
	h.deliverJob(jobEvent{Action: "queued", JobID: 9302, Labels: []string{"ubuntu-latest"}})
	job, err := h.st.GetJobByGitHubID(h.ctx, 9302)
	if err != nil {
		t.Fatal(err)
	}
	if job.ControllerVersion != "" || job.HostID != "" {
		t.Fatalf("an unclaimed hosted job was stamped: %+v", job)
	}
}
