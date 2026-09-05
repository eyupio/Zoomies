package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// UpdateRunner writes every column of the row, and the argument list has to be
// exactly as long as the placeholder list: the driver binds positionally and
// ignores a surplus argument, so one stray value shifts every later column onto
// its neighbour's and binds WHERE id to a number -- which updates nothing and
// reports the runner as not found. Round-tripping every field is what catches
// the two lists drifting apart when the row grows a column.
func TestUpdateRunnerRoundTripsEveryColumn(t *testing.T) {
	s := newTestStore(t)
	_, p, h := seedPool(t, s)
	ctx := context.Background()

	r := &Runner{PoolID: p.ID, HostID: h.ID, Name: "zoomies-roundtrip", Labels: StringSlice{"zoomies"}}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	at := func(m int) *time.Time {
		v := time.Date(2026, 3, 1, 0, m, 0, 0, time.UTC)
		return &v
	}
	pull := 42 * time.Second
	r.State = RunnerBusy
	r.GitHubRunnerID = 77
	r.ContainerID = "abc123"
	r.Ephemeral = false
	r.Labels = StringSlice{"zoomies", "gpu"}
	r.Image = "ghcr.io/eyupio/zoomies-runner:pinned"
	r.ImageDigest = "sha256:feedface"
	r.RunnerVersion = "2.337.0"
	r.CurrentJobID = "job_current"
	r.ImagePullDuration = &pull
	r.ContainerStartedAt = at(1)
	r.RegisteredAt = at(2)
	r.StartedAt = at(3)
	r.LastIdleAt = at(4)
	r.FinishedAt = at(5)
	r.Message = "updated by the test"
	r.JobsHandled = 3
	r.CPUPercent = 12.5
	r.MemoryBytes = 1 << 30
	if err := s.UpdateRunner(ctx, r); err != nil {
		t.Fatalf("UpdateRunner: %v", err)
	}

	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	check := func(field string, got, want any) {
		if got != want {
			t.Errorf("%s = %v after UpdateRunner, want %v", field, got, want)
		}
	}
	check("State", got.State, RunnerBusy)
	check("GitHubRunnerID", got.GitHubRunnerID, int64(77))
	check("ContainerID", got.ContainerID, "abc123")
	check("Ephemeral", got.Ephemeral, false)
	check("Labels", strings.Join(got.Labels, ","), strings.Join(r.Labels, ","))
	check("Image", got.Image, r.Image)
	check("ImageDigest", got.ImageDigest, r.ImageDigest)
	check("RunnerVersion", got.RunnerVersion, r.RunnerVersion)
	check("CurrentJobID", got.CurrentJobID, r.CurrentJobID)
	check("Message", got.Message, r.Message)
	check("JobsHandled", got.JobsHandled, 3)
	check("CPUPercent", got.CPUPercent, 12.5)
	check("MemoryBytes", got.MemoryBytes, int64(1<<30))
	if got.ImagePullDuration == nil || *got.ImagePullDuration != pull {
		t.Errorf("ImagePullDuration = %v, want %v", got.ImagePullDuration, pull)
	}
	for _, ts := range []struct {
		field     string
		got, want *time.Time
	}{
		{"ContainerStartedAt", got.ContainerStartedAt, r.ContainerStartedAt},
		{"RegisteredAt", got.RegisteredAt, r.RegisteredAt},
		{"StartedAt", got.StartedAt, r.StartedAt},
		{"LastIdleAt", got.LastIdleAt, r.LastIdleAt},
		{"FinishedAt", got.FinishedAt, r.FinishedAt},
	} {
		if ts.got == nil || !ts.got.Equal(*ts.want) {
			t.Errorf("%s = %v, want %v", ts.field, ts.got, ts.want)
		}
	}
}
