package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// The closed set has to stay closed at every edge it can be crossed at: an
// agent newer than this controller, a row written by a newer build, a query
// parameter somebody typed. Every one of those reaches Normalise, and the safe
// direction is "we could not narrow it" -- which sends somebody to look --
// rather than a category nothing can render or, worse, silence.
func TestAnUnknownFaultKindReadsAsUnclassifiedRatherThanAbsent(t *testing.T) {
	for _, tc := range []struct {
		in   FaultKind
		want FaultKind
	}{
		{"", ""},
		{FaultOutOfMemory, FaultOutOfMemory},
		{"quantum_decoherence", FaultRunnerExited},
		{"OUT_OF_MEMORY", FaultRunnerExited},
	} {
		if got := tc.in.Normalise(); got != tc.want {
			t.Errorf("FaultKind(%q).Normalise() = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, k := range FaultKinds() {
		if !k.Valid() {
			t.Errorf("%q is in the closed set and does not pass Valid", k)
		}
		// Every kind exists because something different is done about it, and
		// the remedy is where that is written down. A kind with no fix is a
		// category that earns nobody anything.
		if k.Fix() == "" {
			t.Errorf("%q has no fix; a category nothing acts on is prose", k)
		}
	}
}

// Whose a failure is has to be answered the same way by the Go predicate and by
// the SQL behind the counts, or the Overview's tile and the list it links to
// disagree about somebody's afternoon.
func TestTheFaultDomainAgreesWithTheFilters(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)

	mk := func(ghID int64, conclusion string, fault FaultKind, message string) *Job {
		t.Helper()
		j, err := s.UpsertJob(ctx, &Job{
			GitHubJobID: ghID, Repo: "acme/widgets", State: JobCompleted,
			Conclusion: conclusion, QueuedAt: now, CompletedAt: &now,
		})
		if err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
		if fault != "" {
			if _, _, err := s.SetJobRunnerFault(ctx, j.ID, message, fault); err != nil {
				t.Fatalf("SetJobRunnerFault: %v", err)
			}
			j, err = s.GetJob(ctx, j.ID)
			if err != nil {
				t.Fatalf("GetJob: %v", err)
			}
		}
		return j
	}

	ours := mk(1, "failure", FaultOutOfMemory, "runner zoomies-a stopped: out of memory")
	theirs := mk(2, "failure", "", "")
	fine := mk(3, "success", "", "")

	if ours.FaultDomain() != FaultDomainFleet || !ours.FleetFailed() || ours.WorkflowFailed() {
		t.Fatalf("a job the fleet broke reads as %+v", ours)
	}
	if theirs.FaultDomain() != FaultDomainWorkflow || theirs.FleetFailed() || !theirs.WorkflowFailed() {
		t.Fatalf("a test failure reads as %+v", theirs)
	}
	if fine.FaultDomain() != "" || fine.Failed() {
		t.Fatalf("a success reads as %+v", fine)
	}

	// The three filters, which are the SQL spelling of the three predicates.
	for _, tc := range []struct {
		name   string
		filter JobFilter
		want   []string
	}{
		{"failed", JobFilter{FailedOnly: true}, []string{ours.ID, theirs.ID}},
		{"ours", JobFilter{FaultedOnly: true}, []string{ours.ID}},
		{"theirs", JobFilter{WorkflowFailedOnly: true}, []string{theirs.ID}},
		{"by category", JobFilter{FaultKinds: []FaultKind{FaultOutOfMemory}}, []string{ours.ID}},
		// A category nothing matched is an empty list, never the whole table:
		// a filter that silently widened would read as a fleet in better shape
		// than it is.
		{"a category with nothing in it", JobFilter{FaultKinds: []FaultKind{FaultOutOfDisk}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := s.ListJobs(ctx, tc.filter, Page{Limit: 10})
			if err != nil {
				t.Fatalf("ListJobs: %v", err)
			}
			if total != len(tc.want) {
				t.Fatalf("matched %d jobs, want %d", total, len(tc.want))
			}
			ids := map[string]bool{}
			for _, j := range got {
				ids[j.ID] = true
			}
			for _, want := range tc.want {
				if !ids[want] {
					t.Fatalf("job %s is missing from the %s list", want, tc.name)
				}
			}
		})
	}

	// And the count behind the Overview's split, which has to be the same
	// judgement as the filters rather than a second one.
	st, err := s.StatsSince(ctx, now.Add(-time.Hour), false)
	if err != nil {
		t.Fatalf("StatsSince: %v", err)
	}
	if st.Failed != 2 || st.FleetFailed != 1 {
		t.Fatalf("failed = %d, of them ours = %d; want 2 and 1", st.Failed, st.FleetFailed)
	}
	counts, err := s.JobFaultCountsSince(ctx, now.Add(-time.Hour), false)
	if err != nil {
		t.Fatalf("JobFaultCountsSince: %v", err)
	}
	if counts[FaultOutOfMemory] != 1 || len(counts) != 1 {
		t.Fatalf("fault counts = %v, want one out_of_memory", counts)
	}
}

// Every job that already carried a fault is a fleet failure, and the migration
// has to say so. Leaving them empty would hand a year of the fleet's own
// failures back to the workflows that did nothing wrong, the moment the split
// started being counted -- and the number on the tile would be the first thing
// anybody saw of the feature.
func TestTheMigrationClaimsFaultsWrittenBeforeTheCategoryExisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.db")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Wind the category off a database that has the prose, which is what every
	// deployment upgrading to this build looks like.
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE name IN ('0034_job_fault_kind.sql', '0062_job_indexes_for_scans.sql')`,
		`DROP INDEX idx_jobs_fault_kind`,
		`DROP INDEX idx_jobs_faulted`,
		`ALTER TABLE jobs DROP COLUMN fault_kind`,
	} {
		if _, err := s.write.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("winding the category off: %v: %s", err, stmt)
		}
	}
	for _, stmt := range []string{
		`INSERT INTO jobs (id, github_job_id, state, conclusion, queued_at, completed_at, runner_fault)
			VALUES ('job_lost', 8801, 'completed', 'failure', ?, ?, 'runner zoomies-old stopped while this job was running: exited with code 137')`,
		`INSERT INTO jobs (id, github_job_id, state, conclusion, queued_at, completed_at, runner_fault)
			VALUES ('job_theirs', 8802, 'completed', 'failure', ?, ?, '')`,
	} {
		if _, err := s.write.ExecContext(ctx, stmt, now.UnixMilli(), now.UnixMilli()); err != nil {
			t.Fatalf("writing an old row: %v", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open after the migration: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })

	lost, err := migrated.GetJob(ctx, "job_lost")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	// Unclassified, which is the truth: the message was never categorised, and
	// reading a category back out of the prose would be a guess.
	if lost.FaultKind != FaultRunnerExited {
		t.Fatalf("an existing fault got the category %q, want runner_exited", lost.FaultKind)
	}
	if lost.FaultDomain() != FaultDomainFleet {
		t.Fatalf("an existing fault reads as %q, want fleet", lost.FaultDomain())
	}
	theirs, err := migrated.GetJob(ctx, "job_theirs")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if theirs.FaultKind != "" || theirs.FaultDomain() != FaultDomainWorkflow {
		t.Fatalf("a test failure was claimed by the fleet: %q/%q", theirs.FaultKind, theirs.FaultDomain())
	}
}

// The counts behind the Overview's split and the runner half that reaches no
// job at all. Both windows fall back to a creation stamp on purpose: a failure
// with no completion is the one an operator most wants to see, and a window
// that dropped it would hide exactly the fleet that is in trouble.
func TestTheFaultCountsSeeFailuresThatNeverFinished(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	since := now.Add(-time.Hour)

	// A job whose runner died before GitHub closed it: no completion stamp.
	open, err := s.UpsertJob(ctx, &Job{
		GitHubJobID: 41, Repo: "acme/widgets", State: JobInProgress, QueuedAt: now,
	})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if _, _, err := s.SetJobRunnerFault(ctx, open.ID, "runner zoomies-a stopped", FaultHostLost); err != nil {
		t.Fatalf("SetJobRunnerFault: %v", err)
	}
	counts, err := s.JobFaultCountsSince(ctx, since, false)
	if err != nil {
		t.Fatalf("JobFaultCountsSince: %v", err)
	}
	if counts[FaultHostLost] != 1 {
		t.Fatalf("fault counts = %v, want the unfinished job counted under host_lost", counts)
	}

	// And a window that ends before it was queued does not.
	if counts, err := s.JobFaultCountsSince(ctx, now.Add(time.Hour), false); err != nil {
		t.Fatalf("JobFaultCountsSince: %v", err)
	} else if len(counts) != 0 {
		t.Fatalf("counts from a window after the failure = %v, want none", counts)
	}
}

// The runner side is the half no job ever sees: a runner that fails before it
// registers leaves its job queued, so nothing is marked failed and every other
// count reads as a fleet that is merely busy.
func TestFailedRunnerCountsAndTheListBehindThem(t *testing.T) {
	ctx := context.Background()
	// The clock is the test's, so "newest first" is tested rather than a tie
	// SQLite happened to break the right way.
	now := time.Now().UTC().Truncate(time.Millisecond)
	clock := now
	s := newTestStoreAt(t, func() time.Time { return clock })
	since := now.Add(-time.Hour)

	_, pool, host := seedPool(t, s)
	mk := func(id string, state RunnerState, fault FaultKind, message string) {
		t.Helper()
		r := &Runner{ID: id, PoolID: pool.ID, HostID: host.ID, Name: id, State: RunnerProvisioning}
		if err := s.CreateRunner(ctx, r); err != nil {
			t.Fatalf("CreateRunner: %v", err)
		}
		if state == RunnerFailed {
			clock = clock.Add(time.Second)
			if _, err := s.FailRunner(ctx, id, message, fault); err != nil {
				t.Fatalf("FailRunner: %v", err)
			}
		}
	}
	mk("run_image", RunnerFailed, FaultImage, "pulling ghcr.io/acme/runner:v9: manifest unknown")
	mk("run_backend", RunnerFailed, FaultBackend, "dial unix /var/run/docker.sock: no such file")
	mk("run_fine", RunnerProvisioning, "", "")

	counts, err := s.RunnerFaultCountsSince(ctx, since)
	if err != nil {
		t.Fatalf("RunnerFaultCountsSince: %v", err)
	}
	if counts[FaultImage] != 1 || counts[FaultBackend] != 1 || len(counts) != 2 {
		t.Fatalf("runner fault counts = %v, want one image and one backend", counts)
	}

	// Newest first, because the most recent failure is the one whose message
	// the problems drawer and the job's explanation both quote.
	failed, err := s.FailedRunnersForPoolSince(ctx, pool.ID, since, 10)
	if err != nil {
		t.Fatalf("FailedRunnersForPoolSince: %v", err)
	}
	if len(failed) != 2 {
		t.Fatalf("failed runners = %d, want the two that failed and not the one still starting", len(failed))
	}
	if failed[0].ID != "run_backend" || failed[0].FaultKind != FaultBackend {
		t.Fatalf("newest failed runner = %+v, want run_backend carrying its category", failed[0])
	}
	// The limit is a cap on the read, not a filter on the meaning.
	if one, err := s.FailedRunnersForPoolSince(ctx, pool.ID, since, 1); err != nil {
		t.Fatalf("FailedRunnersForPoolSince: %v", err)
	} else if len(one) != 1 || one[0].ID != "run_backend" {
		t.Fatalf("limited to one = %+v, want the newest", one)
	}
	if none, err := s.FailedRunnersForPoolSince(ctx, "pool_other", since, 10); err != nil {
		t.Fatalf("FailedRunnersForPoolSince: %v", err)
	} else if len(none) != 0 {
		t.Fatalf("another pool's failures leaked in: %+v", none)
	}
}

// The guard on a repeating timeline entry. A pool stuck in a start loop tries
// again every pass, and one entry per attempt on every waiting job turns a
// timeline into the log file it exists to save somebody reading.
func TestTheLastTimelineEntryIsWhatTheDedupeReads(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j, err := s.UpsertJob(ctx, &Job{GitHubJobID: 51, State: JobQueued, QueuedAt: time.Now()})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	// A job with no history answers empty rather than failing: it is the
	// ordinary state of a job nothing has happened to yet.
	if kind, err := s.LastJobEventKind(ctx, j.ID); err != nil || kind != "" {
		t.Fatalf("LastJobEventKind on a fresh job = %q (%v), want empty", kind, err)
	}
	now := time.Now().UTC()
	for i, kind := range []JobEventKind{JobEventQueued, JobEventClaimed, JobEventRunnerStartFailed} {
		if err := s.AppendJobEvent(ctx, &JobEvent{
			JobID: j.ID, Kind: kind, Source: "controller", At: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("AppendJobEvent: %v", err)
		}
	}
	if kind, err := s.LastJobEventKind(ctx, j.ID); err != nil || kind != JobEventRunnerStartFailed {
		t.Fatalf("LastJobEventKind = %q (%v), want the newest entry", kind, err)
	}
	if kind, err := s.LastJobEventKind(ctx, "job_missing"); err != nil || kind != "" {
		t.Fatalf("LastJobEventKind for a job that does not exist = %q (%v), want empty and no error", kind, err)
	}
}
