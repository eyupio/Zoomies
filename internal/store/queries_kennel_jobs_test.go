package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A required check is posted by whichever runner ran the job, so the jobs that
// count are all of them: the fleet's own, a hosted runner's, and one no pool
// claimed. Counting only the fleet's would call every check a hosted job posts
// missing.
func TestJobsSeenCountEveryJobTheFleetWasToldAbout(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	q := *clock
	job(t, s, 1, Job{State: JobCompleted, QueuedAt: q, RunnerID: "run_1", PoolID: "pool_1", Labels: StringSlice{"self-hosted"}, JobName: "test"})
	job(t, s, 2, Job{State: JobCompleted, QueuedAt: q, Labels: StringSlice{"ubuntu-latest"}, JobName: "lint"})
	job(t, s, 3, Job{State: JobQueued, QueuedAt: q, Labels: StringSlice{"typo-linux"}, JobName: "build"})
	job(t, s, 4, Job{State: JobCompleted, QueuedAt: q, Labels: StringSlice{"self-hosted"}, JobName: "test"})

	seen, reported, err := s.KennelJobsSeen(ctx, "acme/api", q.Add(-time.Hour), []string{"test", "lint", "build", "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 4 {
		t.Errorf("seen = %d, want all four jobs, hosted and unmatched included", seen)
	}
	for _, name := range []string{"test", "lint", "build"} {
		if !reported[name] {
			t.Errorf("%q was produced by a job and is not reported: %v", name, reported)
		}
	}
	if reported["deploy"] {
		t.Errorf("a name no job had is reported: %v", reported)
	}
}

// Only the repository asked about, and only the window asked about: another
// repository's job, or one from before the window, is not evidence about this
// one's checks.
func TestJobsSeenStayInTheRepositoryAndTheWindow(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	q := *clock
	since := q.Add(-24 * time.Hour)
	job(t, s, 1, Job{State: JobCompleted, QueuedAt: q, JobName: "test"})
	job(t, s, 2, Job{Repo: "acme/other", State: JobCompleted, QueuedAt: q, JobName: "build"})
	job(t, s, 3, Job{State: JobCompleted, QueuedAt: since.Add(-time.Second), JobName: "old"})
	job(t, s, 4, Job{State: JobCompleted, QueuedAt: since, JobName: "edge"})

	seen, reported, err := s.KennelJobsSeen(ctx, "acme/api", since, []string{"test", "build", "old", "edge"})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Errorf("seen = %d, want the one inside the window and the one on its edge", seen)
	}
	if !reported["test"] || !reported["edge"] || reported["build"] || reported["old"] {
		t.Errorf("reported = %v", reported)
	}
}

// GitHub matches a required check against a job's name exactly. A matrix job is
// named "<job> (<values>)", so a requirement for the bare job name is not met by
// it, which is exactly the breakage this check exists to find, and neither is a
// name that differs in case or only by a prefix.
func TestJobsSeenMatchANameExactly(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	q := *clock
	job(t, s, 1, Job{State: JobCompleted, QueuedAt: q, JobName: "build (ubuntu, 3.12)"})
	job(t, s, 2, Job{State: JobCompleted, QueuedAt: q, JobName: "Test"})
	job(t, s, 3, Job{State: JobCompleted, QueuedAt: q, JobName: "lint-all"})

	_, reported, err := s.KennelJobsSeen(ctx, "acme/api", q.Add(-time.Hour), []string{"build", "test", "lint"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reported) != 0 {
		t.Errorf("reported = %v, want none: each differs from a job's name by a matrix suffix, a case or a suffix", reported)
	}
	_, reported, err = s.KennelJobsSeen(ctx, "acme/api", q.Add(-time.Hour), []string{"Test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || !reported["Test"] {
		t.Errorf("reported = %v, want the exact name", reported)
	}
}

// A name is a repository's own text. One that is shaped like SQL, or like a LIKE
// pattern, is a name and nothing else.
func TestJobsSeenTreatANameAsTextOnly(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	q := *clock
	job(t, s, 1, Job{State: JobCompleted, QueuedAt: q, JobName: "build"})

	for _, name := range []string{"x' OR '1'='1", "build%", "%", "_uild", `build" --`} {
		_, reported, err := s.KennelJobsSeen(ctx, "acme/api", q.Add(-time.Hour), []string{name})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if len(reported) != 0 {
			t.Errorf("%q matched a job: %v", name, reported)
		}
	}
}

// More names than one statement carries are asked in batches, and a name in the
// last batch is found as readily as one in the first.
func TestJobsSeenAskAboutEveryNameWhateverTheirNumber(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	q := *clock
	var names []string
	for i := 0; i < 2*kennelJobNamesPerQuery+7; i++ {
		names = append(names, fmt.Sprintf("check-%03d", i))
	}
	first, middle, last := names[0], names[kennelJobNamesPerQuery+3], names[len(names)-1]
	for i, name := range []string{first, middle, last} {
		job(t, s, int64(i+1), Job{State: JobCompleted, QueuedAt: q, JobName: name})
	}

	seen, reported, err := s.KennelJobsSeen(ctx, "acme/api", q.Add(-time.Hour), names)
	if err != nil {
		t.Fatal(err)
	}
	if seen != 3 || len(reported) != 3 || !reported[first] || !reported[middle] || !reported[last] {
		t.Errorf("seen = %d, reported = %v, want the three that had a job", seen, reported)
	}
}

// With nothing to look for the answer is the count alone, and no name is asked.
func TestJobsSeenWithNoNamesIsJustTheCount(t *testing.T) {
	ctx := context.Background()
	s, _, clock := kennelStore(t)
	q := *clock
	job(t, s, 1, Job{State: JobCompleted, QueuedAt: q, JobName: "build"})

	seen, reported, err := s.KennelJobsSeen(ctx, "acme/api", q.Add(-time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if seen != 1 || len(reported) != 0 {
		t.Errorf("seen = %d, reported = %v", seen, reported)
	}
}
