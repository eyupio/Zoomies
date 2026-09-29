package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"
)

var versionsEpoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// versionJob records a completed job with the given timings and stamps it. A
// blank version leaves the job the way one recorded before migration 0060 is:
// never stamped.
func versionJob(t *testing.T, s *Store, n int, version, name string, queued time.Time, wait, run time.Duration, conclusion string) *Job {
	t.Helper()
	ctx := context.Background()
	started, done := queued.Add(wait), queued.Add(wait+run)
	j, _, err := s.ApplyJob(ctx, &Job{
		GitHubJobID: int64(1000 + n), Repo: "acme/widgets", Workflow: "ci", JobName: name,
		Labels: StringSlice{"self-hosted"}, State: JobCompleted, Conclusion: conclusion,
		QueuedAt: queued, StartedAt: &started, CompletedAt: &done, Matched: true, PoolID: "pool_a",
	})
	if err != nil {
		t.Fatalf("recording job %d: %v", n, err)
	}
	if version != "" {
		j, err = s.StampJobVersions(ctx, j.ID, JobVersions{
			ControllerVersion: version, ControllerChannel: "stable", AgentVersion: "agent-" + version, HostID: "host_1",
		})
		if err != nil {
			t.Fatalf("stamping job %d: %v", n, err)
		}
	}
	return j
}

// A job's numbers belong to the build that produced them. If a later delivery
// -- the completion, a re-run, the poller repeating the claim -- could restamp
// the row, a release comparison would move jobs between releases whenever the
// controller was upgraded mid-flight.
func TestJobVersionsAreStampedOnceAndSurviveLaterUpdates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: 1, Repo: "acme/widgets", JobName: "build", State: JobQueued,
		Labels: StringSlice{"self-hosted"}, PoolID: "pool_a", Matched: true})
	if err != nil {
		t.Fatal(err)
	}
	if j.ControllerVersion != "" || j.HostID != "" {
		t.Fatalf("a new job carries versions: %+v", j)
	}

	// The claim knows the controller; the host is not known yet.
	got, err := s.StampJobVersions(ctx, j.ID, JobVersions{ControllerVersion: "v1.3.2", ControllerChannel: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ControllerVersion != "v1.3.2" || got.ControllerChannel != "stable" || got.AgentVersion != "" || got.HostID != "" {
		t.Fatalf("claim stamp = %+v, want the controller's pair only", got)
	}

	// The runner's pickup fills in the host's pair and must not touch the first.
	got, err = s.StampJobVersions(ctx, j.ID, JobVersions{
		ControllerVersion: "v1.3.3", ControllerChannel: "edge", AgentVersion: "v1.3.1", HostID: "host_a"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ControllerVersion != "v1.3.2" || got.ControllerChannel != "stable" || got.AgentVersion != "v1.3.1" || got.HostID != "host_a" {
		t.Fatalf("pickup stamp = %+v, want the controller's pair kept and the host's added", got)
	}

	// Nothing after that moves any of it, not another stamp and not a status update.
	if _, err := s.StampJobVersions(ctx, j.ID, JobVersions{
		ControllerVersion: "v9", ControllerChannel: "x", AgentVersion: "v9", HostID: "host_b"}); err != nil {
		t.Fatal(err)
	}
	started, done := versionsEpoch, versionsEpoch.Add(time.Minute)
	final, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: 1, State: JobCompleted, Conclusion: "success",
		StartedAt: &started, CompletedAt: &done})
	if err != nil {
		t.Fatal(err)
	}
	reread, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, job := range map[string]*Job{"the update's result": final, "a fresh read": reread} {
		if job.ControllerVersion != "v1.3.2" || job.ControllerChannel != "stable" || job.AgentVersion != "v1.3.1" || job.HostID != "host_a" {
			t.Errorf("%s = %+v, want the first stamps", name, job)
		}
	}
}

// Old rows stay NULL rather than being guessed at, and an empty stamp writes
// nothing: "unknown" has to be distinguishable from a release with no name.
func TestAnUnstampedJobIsNullInTheDatabase(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j := versionJob(t, s, 1, "", "build", versionsEpoch, time.Second, time.Second, "success")
	if _, err := s.StampJobVersions(ctx, j.ID, JobVersions{}); err != nil {
		t.Fatal(err)
	}
	var v, c, a, h sql.NullString
	if err := s.read.QueryRowContext(ctx, `SELECT controller_version, controller_channel, agent_version, host_id FROM jobs WHERE id = ?`, j.ID).
		Scan(&v, &c, &a, &h); err != nil {
		t.Fatal(err)
	}
	if v.Valid || c.Valid || a.Valid || h.Valid {
		t.Fatalf("stamped nothing but the row holds %v %v %v %v", v, c, a, h)
	}
}

// The usage ledger is what outlives the job rows, so the release has to be in it.
func TestARunnerSessionCarriesTheReleaseOfItsJob(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "zoomies-1", Ephemeral: true}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatal(err)
	}
	j := versionJob(t, s, 1, "v1.3.3", "build", versionsEpoch, time.Second, time.Minute, "success")
	if _, err := s.exec(ctx, `UPDATE runners SET current_job_id = ?, state = 'removed', finished_at = ? WHERE id = ?`,
		j.ID, ms(versionsEpoch.Add(time.Hour)), r.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.tx(ctx, func(tx *sql.Tx) error { return recordRunnerSession(ctx, tx, r.ID, ms(versionsEpoch.Add(2*time.Hour))) }); err != nil {
		t.Fatal(err)
	}
	// The job row goes at retention; the session keeps what it learned.
	if _, err := s.PruneJobs(ctx, versionsEpoch.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.RunnerSessions(ctx, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %v, %v", sessions, err)
	}
	if got := sessions[0]; got.ControllerVersion != "v1.3.3" || got.ControllerChannel != "stable" || got.AgentVersion != "agent-v1.3.3" || got.HostID != host.ID {
		t.Fatalf("session = %+v, want the job's release and the runner's host", got)
	}
}

func ids(jobs []*Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.ID
	}
	return out
}

// since is inclusive and until exclusive, so that the last 30 days and the 30
// before them, cut at the same instant, share no job and lose none.
func TestSinceIsInclusiveAndUntilExclusive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := func(m int) time.Time { return versionsEpoch.Add(time.Duration(m) * time.Minute) }
	var made []*Job
	for m := 0; m < 4; m++ {
		made = append(made, versionJob(t, s, m, "v1", "build", at(m), time.Second, time.Second, "success"))
	}
	since, until := at(1), at(3)
	got, total, err := s.ListJobs(ctx, JobFilter{Since: &since, Until: &until}, Page{Limit: 10, Sort: "queued_at", Desc: false})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{made[1].ID, made[2].ID}
	if !slices.Equal(ids(got), want) || total != 2 {
		t.Fatalf("[minute 1, minute 3) = %v (total %d), want minutes 1 and 2 %v", ids(got), total, want)
	}
}

// Offset paging repeats or drops a row whenever a job is queued between two
// pages. A keyset cursor must not, because that is why it exists.
func TestCursorPagingReturnsEveryJobOnceWhileNewJobsArrive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var original []string
	// Ten of the 31 share a timestamp, so the tie-break is exercised too.
	for n := 0; n < 31; n++ {
		queued := versionsEpoch.Add(time.Duration(n/3) * time.Minute)
		original = append(original, versionJob(t, s, n, "v1", "build", queued, time.Second, time.Second, "success").ID)
	}
	var seen []string
	var cursor *JobCursor
	arrivals := 0
	for pages := 0; ; pages++ {
		if pages > 50 {
			t.Fatal("the cursor never ran out")
		}
		got, _, next, err := s.ListJobsAfter(ctx, JobFilter{}, cursor, 4)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, ids(got)...)
		// A job arrives between every two pages, newer than everything read.
		arrivals++
		versionJob(t, s, 1000+arrivals, "v1", "arrival", versionsEpoch.Add(time.Duration(100+arrivals)*time.Hour), time.Second, time.Second, "success")
		if next == nil {
			break
		}
		token, err := ParseJobCursor(next.Encode())
		if err != nil || token.ID != next.ID || !token.QueuedAt.Equal(next.QueuedAt) {
			t.Fatalf("the cursor did not survive its own encoding: %+v -> %+v (%v)", next, token, err)
		}
		cursor = &token
	}
	slices.Sort(seen)
	slices.Sort(original)
	if !slices.Equal(seen, original) {
		t.Fatalf("paging returned %d jobs, want the %d that existed when it began, each once\n got %v\nwant %v",
			len(seen), len(original), seen, original)
	}
}

func TestAJunkCursorIsRefused(t *testing.T) {
	for _, token := range []string{"", "!!!", "bm90LWEtY3Vyc29y"} {
		if _, err := ParseJobCursor(token); err == nil {
			t.Errorf("ParseJobCursor(%q) accepted it", token)
		}
	}
}

func TestJobFiltersByReleaseHostExactNameAndHosted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a := versionJob(t, s, 1, "v1.0.0", "build", versionsEpoch, time.Second, time.Second, "success")
	b := versionJob(t, s, 2, "v1.1.0", "build-docs", versionsEpoch.Add(time.Minute), time.Second, time.Second, "success")
	c := versionJob(t, s, 3, "", "build", versionsEpoch.Add(2*time.Minute), time.Second, time.Second, "success")
	hosted, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: 99, Repo: "acme/widgets", JobName: "build", State: JobCompleted,
		Labels: StringSlice{"ubuntu-latest"}, QueuedAt: versionsEpoch.Add(3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(ctx, `UPDATE jobs SET host_id = 'host_2' WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	for name, tc := range map[string]struct {
		f    JobFilter
		want []string
	}{
		"one release":           {JobFilter{ControllerVersions: []string{"v1.0.0"}}, []string{a.ID}},
		"the unknown release":   {JobFilter{ControllerVersions: []string{UnknownVersion}}, []string{c.ID, hosted.ID}},
		"a release or unknown":  {JobFilter{ControllerVersions: []string{"v1.1.0", UnknownVersion}}, []string{b.ID, c.ID, hosted.ID}},
		"a host":                {JobFilter{HostIDs: []string{"host_2"}}, []string{b.ID}},
		"an exact name":         {JobFilter{JobNames: []string{"build"}}, []string{a.ID, c.ID, hosted.ID}},
		"a substring is not it": {JobFilter{Search: "build"}, []string{a.ID, b.ID, c.ID, hosted.ID}},
		"hosted":                {JobFilter{Hosted: &yes}, []string{hosted.ID}},
		"not hosted":            {JobFilter{Hosted: &no}, []string{a.ID, b.ID, c.ID}},
	} {
		got, _, err := s.ListJobs(ctx, tc.f, Page{Limit: 50, Sort: "queued_at"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		g := ids(got)
		slices.Sort(g)
		w := slices.Clone(tc.want)
		slices.Sort(w)
		if !slices.Equal(g, w) {
			t.Errorf("%s: got %v, want %v", name, g, w)
		}
	}
}

func p(v *int64) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprint(*v)
}

// Twenty-one jobs of 1..21 seconds make the positions unambiguous: with n=21
// the p50 is the 11th sample and the p95 the 20th.
func TestJobStatsPercentilesOnAKnownFixture(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for n := 1; n <= 21; n++ {
		versionJob(t, s, n, "v1.0.0", "build", versionsEpoch.Add(time.Duration(n)*time.Minute),
			time.Duration(n*10)*time.Second, time.Duration(n)*time.Second, "success")
	}
	res, err := s.JobStats(ctx, JobFilter{}, []string{GroupByControllerVersion})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 {
		t.Fatalf("groups = %+v", res.Groups)
	}
	g := res.Groups[0]
	if g.Keys[GroupByControllerVersion] != "v1.0.0" || g.Count != 21 || g.Succeeded != 21 || g.Failed != 0 {
		t.Fatalf("counts = %+v", g)
	}
	if g.Duration.Samples != 21 || p(g.Duration.P50MS) != "11000" || p(g.Duration.P95MS) != "20000" {
		t.Errorf("duration = %d samples p50 %s p95 %s, want 21, 11000, 20000", g.Duration.Samples, p(g.Duration.P50MS), p(g.Duration.P95MS))
	}
	if p(g.QueueWait.P50MS) != "110000" || p(g.QueueWait.P95MS) != "200000" {
		t.Errorf("queue wait p50 %s p95 %s, want 110000, 200000", p(g.QueueWait.P50MS), p(g.QueueWait.P95MS))
	}
	if g.Startup.Samples != 0 || g.Startup.P50MS != nil || g.Startup.P95MS != nil {
		t.Errorf("startup with no runner rows = %+v, want no samples and no figures", g.Startup)
	}
}

func TestJobStatsLeaveCancelledJobsOutOfDurationsOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for n := 1; n <= 3; n++ {
		versionJob(t, s, n, "v1", "build", versionsEpoch.Add(time.Duration(n)*time.Minute), time.Second, 10*time.Second, "success")
	}
	versionJob(t, s, 4, "v1", "build", versionsEpoch.Add(4*time.Minute), 7*time.Second, time.Hour, "cancelled")
	versionJob(t, s, 5, "v1", "build", versionsEpoch.Add(5*time.Minute), 7*time.Second, time.Hour, "skipped")
	res, err := s.JobStats(ctx, JobFilter{}, []string{GroupByControllerVersion})
	if err != nil {
		t.Fatal(err)
	}
	g := res.Groups[0]
	if g.Count != 5 || g.Succeeded != 3 || g.Cancelled != 2 {
		t.Errorf("counts = %+v, want 5 jobs, 3 succeeded, 2 cancelled", g)
	}
	if g.Duration.Samples != 3 || p(g.Duration.P95MS) != "10000" {
		t.Errorf("duration = %d samples, p95 %s: the hour-long cancelled jobs leaked in", g.Duration.Samples, p(g.Duration.P95MS))
	}
	if g.QueueWait.Samples != 5 {
		t.Errorf("queue wait has %d samples, want all 5: a cancelled job still waited", g.QueueWait.Samples)
	}
}

func TestJobStatsGroupUnstampedJobsAsUnknownAndSplitFleetFailuresByKind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	versionJob(t, s, 1, "v1.0.0", "build", versionsEpoch, time.Second, time.Second, "success")
	old := versionJob(t, s, 2, "", "build", versionsEpoch.Add(time.Minute), time.Second, time.Second, "success")
	lost := []*Job{
		versionJob(t, s, 3, "v1.1.0", "build", versionsEpoch.Add(2*time.Minute), time.Second, time.Second, "failure"),
		versionJob(t, s, 4, "v1.1.0", "build", versionsEpoch.Add(3*time.Minute), time.Second, time.Second, "failure"),
		versionJob(t, s, 5, "v1.1.0", "build", versionsEpoch.Add(4*time.Minute), time.Second, time.Second, "failure"),
	}
	versionJob(t, s, 6, "v1.1.0", "build", versionsEpoch.Add(5*time.Minute), time.Second, time.Second, "failure") // the workflow's own
	for i, kind := range []FaultKind{FaultRunnerExited, FaultRunnerExited, FaultOutOfMemory} {
		if _, _, err := s.SetJobRunnerFault(ctx, lost[i].ID, "runner stopped", kind); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.JobStats(ctx, JobFilter{}, []string{GroupByControllerVersion})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	by := map[string]JobStatsGroup{}
	for _, g := range res.Groups {
		names = append(names, g.Keys[GroupByControllerVersion])
		by[g.Keys[GroupByControllerVersion]] = g
	}
	if !slices.Equal(names, []string{"v1.0.0", UnknownVersion, "v1.1.0"}) {
		t.Fatalf("groups %v, want releases in the order they were first seen", names)
	}
	if by[UnknownVersion].Count != 1 || by[UnknownVersion].Succeeded != 1 || old.ID == "" {
		t.Errorf("unknown group = %+v", by[UnknownVersion])
	}
	g := by["v1.1.0"]
	if g.Count != 4 || g.Failed != 4 || g.FleetFailed != 3 || g.FleetFailureRate != 0.75 {
		t.Errorf("v1.1.0 = %+v, want 4 failed of which 3 were the fleet's (rate 0.75)", g)
	}
	if g.FleetFailedByKind[string(FaultRunnerExited)] != 2 || g.FleetFailedByKind[string(FaultOutOfMemory)] != 1 || len(g.FleetFailedByKind) != 2 {
		t.Errorf("by kind = %v, want runner_exited 2 and out_of_memory 1", g.FleetFailedByKind)
	}
}

func TestJobStatsGroupByTwoKeysAndStartup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	for n := 1; n <= 3; n++ {
		j := versionJob(t, s, n, "v1", "build", versionsEpoch.Add(time.Duration(n)*24*time.Hour), time.Second, time.Second, "success")
		r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: fmt.Sprintf("zoomies-%d", n), Ephemeral: true}
		if err := s.CreateRunner(ctx, r); err != nil {
			t.Fatal(err)
		}
		created := versionsEpoch.Add(time.Duration(n) * 24 * time.Hour)
		if _, err := s.exec(ctx, `UPDATE runners SET created_at = ?, container_started_at = ? WHERE id = ?`,
			ms(created), ms(created.Add(time.Duration(n)*time.Second)), r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.exec(ctx, `UPDATE jobs SET runner_id = ? WHERE id = ?`, r.ID, j.ID); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.JobStats(ctx, JobFilter{}, []string{GroupByControllerVersion, GroupByDay})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 3 {
		t.Fatalf("release x day = %d groups, want one per day", len(res.Groups))
	}
	if g := res.Groups[1]; g.Keys[GroupByDay] != "2026-03-03" || p(g.Startup.P50MS) != "2000" || g.Startup.Samples != 1 {
		t.Errorf("second day = %+v, want a 2s startup from its runner", g)
	}
	if _, err := s.JobStats(ctx, JobFilter{}, []string{"a", "b", "c"}); err == nil {
		t.Error("three group_by keys were accepted")
	}
	if _, err := s.JobStats(ctx, JobFilter{}, []string{"repo; DROP TABLE jobs"}); err == nil {
		t.Error("an arbitrary group_by key was accepted")
	}
}
