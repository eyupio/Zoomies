package github

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/aicontext"
)

const (
	commitA = "a8ce5e396d2cea8ce0584eb0997fd2b722da9322"
	commitB = "bc2aeae318b6abc62c31a047c52d63903a73b8ff"
)

func runReader(t *testing.T) (*FakeGitHub, ContextRunReader) {
	t.Helper()
	fake, client := migrationFake(t)
	reader, ok := client.(ContextRunReader)
	if !ok {
		t.Fatal("the App client cannot read context runs")
	}
	return fake, reader
}

func generateJob(conclusion string, failedAt string, log string) FakeContextJob {
	steps := []FakeContextStep{{"Set up job", "success"}, {"Generate bounded source context", "success"}}
	if failedAt != "" {
		steps = append(steps, FakeContextStep{failedAt, "failure"})
	}
	return FakeContextJob{Name: "generate", Conclusion: conclusion, Steps: steps, Log: log}
}

// The tail of the log of eyupio/rea run 37425650458, which is what a full
// artifact quota looks like from the outside: the generator passed, and Actions
// printed the reason as its own annotation.
const quotaLog = `2026-10-06T06:47:47.7997758Z omitted: .tools/cics-abend-catalogue-seed.json (972.9 KiB, dropped to fit the size and file-count limits)
2026-10-06T06:47:47.8499426Z ##[group]Run actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a
2026-10-06T06:47:47.8500120Z with:
2026-10-06T06:47:48.0799042Z With the provided path, there will be 3 files uploaded
2026-10-06T06:47:48.2010869Z ##[error]Failed to CreateArtifact: Artifact storage quota has been hit. Unable to upload any new artifacts. Usage is recalculated every 6-12 hours.
2026-10-06T06:47:48.2466382Z Post job cleanup.
`

// The tail of run 37139058368: the generator's own refusal is an ordinary line
// printed just before Actions' annotation of the exit code.
const refusalLog = `2026-10-03T17:02:46.0487778Z ##[endgroup]
2026-10-03T17:02:49.4790859Z Context generation refused: A source file exceeds the context size limit; add an exclusion
2026-10-03T17:02:49.4899378Z ##[error]Process completed with exit code 1.
2026-10-03T17:02:49.5047280Z Post job cleanup.
`

func TestLatestContextRunReadsTheRunAndItsJobs(t *testing.T) {
	fake, reader := runReader(t)
	id := fake.AddContextRun("acme/widgets", FakeContextRun{Commit: commitA, Conclusion: "failure", Jobs: []FakeContextJob{
		generateJob("failure", "Run actions/upload-artifact@043fb46", quotaLog),
		{Name: "publish", Conclusion: "skipped"},
	}})

	run, err := reader.LatestContextRun(t.Context(), "acme/widgets", commitA)
	if err != nil || run == nil {
		t.Fatalf("LatestContextRun = %v, %v", run, err)
	}
	if run.ID != id || !strings.Contains(run.URL, "/actions/runs/") {
		t.Errorf("run = %d %q, want id %d with a link", run.ID, run.URL, id)
	}
	if run.Facts.Status != "completed" || run.Facts.Conclusion != "failure" || len(run.Facts.Jobs) != 2 || len(run.JobIDs) != 2 {
		t.Fatalf("facts = %+v ids %v", run.Facts, run.JobIDs)
	}
	job := run.Facts.Jobs[0]
	if job.Name != "generate" || job.Conclusion != "failure" || len(job.Steps) != 3 || job.Steps[2].Conclusion != "failure" {
		t.Errorf("generate job = %+v", job)
	}
	if run.JobIDs[0] != fake.ContextRunJobID(id, 0) {
		t.Errorf("job ids %v do not follow the jobs", run.JobIDs)
	}

	// Another commit's runs say nothing about this one.
	if other, err := reader.LatestContextRun(t.Context(), "acme/widgets", commitB); err != nil || other != nil {
		t.Errorf("a commit with no run got %v, %v", other, err)
	}
}

func TestLatestContextRunLooksPastARunCancelledByANewerOne(t *testing.T) {
	fake, reader := runReader(t)
	failed := fake.AddContextRun("acme/widgets", FakeContextRun{Commit: commitA, Conclusion: "failure", Jobs: []FakeContextJob{generateJob("failure", "Run actions/upload-artifact@043fb46", "")}})
	// The workflow's concurrency group cancels the previous run on purpose, so
	// the newest run being a cancelled one says nothing about the one before it.
	fake.AddContextRun("acme/widgets", FakeContextRun{Commit: commitA, Conclusion: "cancelled"})

	run, err := reader.LatestContextRun(t.Context(), "acme/widgets", commitA)
	if err != nil || run == nil || run.ID != failed {
		t.Fatalf("picked %v (%v), want the failed run %d", run, err, failed)
	}

	// A run in progress is the newest thing that matters, whatever came before.
	running := fake.AddContextRun("acme/widgets", FakeContextRun{Commit: commitA, Status: "in_progress"})
	if run, err = reader.LatestContextRun(t.Context(), "acme/widgets", commitA); err != nil || run == nil || run.ID != running || run.Facts.Status != "in_progress" {
		t.Fatalf("picked %v (%v), want the run in progress %d", run, err, running)
	}
	if len(run.Facts.Jobs) != 0 {
		t.Error("jobs were fetched for a run that has not finished")
	}

	// With nothing but cancelled runs the newest is returned, so a caller can
	// tell that something was started and then stopped.
	only := fake.AddContextRun("acme/widgets", FakeContextRun{Commit: commitB, Conclusion: "cancelled"})
	if run, err = reader.LatestContextRun(t.Context(), "acme/widgets", commitB); err != nil || run == nil || run.ID != only {
		t.Fatalf("picked %v (%v), want the cancelled run %d", run, err, only)
	}
}

func TestLatestContextRunNeedsOnlyActionsRead(t *testing.T) {
	fake, reader := runReader(t)
	fake.AddContextRun("acme/widgets", FakeContextRun{Commit: commitA, Conclusion: "failure", Jobs: []FakeContextJob{generateJob("failure", "", "")}})
	// Reading a run must not require the write permission that starting one does.
	fake.SetPermissions(map[string]string{"actions": "read", "metadata": "read", "contents": "write"})
	if run, err := reader.LatestContextRun(t.Context(), "acme/widgets", commitA); err != nil || run == nil {
		t.Fatalf("with Actions read: %v, %v", run, err)
	}
	fake.SetPermissions(map[string]string{"metadata": "read", "contents": "write"})
	if _, err := reader.LatestContextRun(t.Context(), "acme/widgets", commitA); !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "Actions") {
		t.Fatalf("without Actions: %v, want a forbidden error that names Actions", err)
	}
}

func TestContextJobErrorsKeepsTheReasonAndNotTheRestOfTheLog(t *testing.T) {
	fake, reader := runReader(t)
	id := fake.AddContextRun("acme/widgets", FakeContextRun{Commit: commitA, Conclusion: "failure", Jobs: []FakeContextJob{
		generateJob("failure", "Run actions/upload-artifact@043fb46", quotaLog),
		generateJob("failure", "Generate bounded source context", refusalLog),
		{Name: "generate", Conclusion: "cancelled"},
	}})

	got, err := reader.ContextJobErrors(t.Context(), "acme/widgets", fake.ContextRunJobID(id, 0))
	if err != nil {
		t.Fatal(err)
	}
	// Each annotation comes with the line before it, because the scripts end by
	// raising SystemExit with their reason, which Actions prints just above its
	// own note of the exit code. Nothing else of the log is kept: not the
	// generator's list of omitted files, and not the cleanup after the failure.
	want := []string{
		"With the provided path, there will be 3 files uploaded",
		"Failed to CreateArtifact: Artifact storage quota has been hit. Unable to upload any new artifacts. Usage is recalculated every 6-12 hours.",
	}
	if !slices.Equal(got, want) {
		t.Errorf("quota errors = %q, want %q", got, want)
	}

	got, err = reader.ContextJobErrors(t.Context(), "acme/widgets", fake.ContextRunJobID(id, 1))
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"Context generation refused: A source file exceeds the context size limit; add an exclusion", "Process completed with exit code 1."}
	if !slices.Equal(got, want) {
		t.Errorf("refusal errors = %q, want %q", got, want)
	}

	// The log's own address is signed and on another host: the installation's
	// token must not go with the request.
	for _, auth := range fake.ContextLogAuthorizations() {
		if auth != "" {
			t.Errorf("the log was fetched with credentials %q", auth)
		}
	}
	if len(fake.ContextLogAuthorizations()) != 2 {
		t.Errorf("%d log downloads, want 2", len(fake.ContextLogAuthorizations()))
	}

	// A job that never started has no log, and that is an answer.
	if got, err = reader.ContextJobErrors(t.Context(), "acme/widgets", fake.ContextRunJobID(id, 2)); err != nil || got != nil {
		t.Errorf("a job with no log gave %q, %v", got, err)
	}
}

func TestErrorLinesAreBounded(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&log, "2026-10-06T06:47:48.%07dZ reason %d\n2026-10-06T06:47:48.0000000Z ##[error]%s\n", i, i, strings.Repeat("x", 1000))
	}
	got := errorLines(log.String())
	if len(got) != maxContextErrorLines {
		t.Errorf("%d lines kept, want %d", len(got), maxContextErrorLines)
	}
	for _, line := range got {
		if len(line) > maxContextErrorLine {
			t.Errorf("a %d-byte line was kept", len(line))
		}
	}
	// The lines that ended the job are the ones kept.
	if !strings.HasPrefix(got[len(got)-2], "reason 99") {
		t.Errorf("kept %q, want the last reason", got[len(got)-2])
	}
	if got := errorLines("no annotations here\r\nonly output\r\n"); len(got) != 0 {
		t.Errorf("a clean log produced %q", got)
	}
}

func TestContextArtifactUsageNamesTheBiggestGroups(t *testing.T) {
	fake, reader := runReader(t)
	var artifacts []FakeArtifact
	for i := 0; i < 130; i++ {
		artifacts = append(artifacts, FakeArtifact{Name: "rea-graph-studio-windows-amd64-alpha", Bytes: 30 << 20})
	}
	for i := 0; i < 120; i++ {
		artifacts = append(artifacts, FakeArtifact{Name: fmt.Sprintf("build-%d.dockerbuild", i), Bytes: 56 << 10})
	}
	// An expired artifact holds no storage and must not be counted against it.
	artifacts = append(artifacts, FakeArtifact{Name: "gone", Bytes: 1 << 40, Expired: true})
	fake.AddArtifacts("acme/widgets", artifacts...)

	usage, err := reader.ContextArtifactUsage(t.Context(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Count != 250 || usage.Partial {
		t.Errorf("count = %d partial = %v, want 250 across three pages", usage.Count, usage.Partial)
	}
	if len(usage.Largest) != 3 || usage.Largest[0].Name != "rea-graph-studio-windows-amd64-alpha" || usage.Largest[0].Count != 130 || usage.Largest[0].Bytes != 130*(30<<20) {
		t.Errorf("largest = %+v", usage.Largest)
	}
	for _, g := range usage.Largest {
		if g.Name == "gone" {
			t.Error("an expired artifact was counted")
		}
	}
}

func TestContextArtifactUsageSaysWhenItStoppedReading(t *testing.T) {
	fake, reader := runReader(t)
	many := make([]FakeArtifact, 1050)
	for i := range many {
		many[i] = FakeArtifact{Name: "bulk", Bytes: 1 << 20}
	}
	fake.AddArtifacts("acme/widgets", many...)
	usage, err := reader.ContextArtifactUsage(t.Context(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !usage.Partial || usage.Count != maxArtifactPages*artifactPageSize {
		t.Errorf("count = %d partial = %v, want %d and partial", usage.Count, usage.Partial, maxArtifactPages*artifactPageSize)
	}
}

// What the client returns is what the classifier reads, end to end: the real
// run 14 of eyupio/rea comes out as a full quota, with what holds it.
func TestAFullQuotaRunIsDiagnosedFromWhatTheClientReads(t *testing.T) {
	fake, reader := runReader(t)
	id := fake.AddContextRun("acme/widgets", FakeContextRun{Commit: commitA, Conclusion: "failure", Jobs: []FakeContextJob{
		generateJob("failure", "Run actions/upload-artifact@043fb46", quotaLog), {Name: "publish", Conclusion: "skipped"}}})
	fake.AddArtifacts("acme/widgets", FakeArtifact{Name: "alpha", Bytes: 3 << 30}, FakeArtifact{Name: "alpha", Bytes: 1 << 20})

	run, err := reader.LatestContextRun(t.Context(), "acme/widgets", commitA)
	if err != nil || run == nil {
		t.Fatal(run, err)
	}
	i := slices.Index(run.JobIDs, fake.ContextRunJobID(id, 0))
	if run.Facts.Jobs[i].Errors, err = reader.ContextJobErrors(t.Context(), "acme/widgets", run.JobIDs[i]); err != nil {
		t.Fatal(err)
	}
	outcome, d := aicontext.Diagnose(run.Facts)
	if outcome != aicontext.OutcomeFailed || d.Cause != aicontext.CauseArtifactQuota {
		t.Fatalf("diagnosed %q %+v, want a full quota", outcome, d)
	}
	usage, err := reader.ContextArtifactUsage(t.Context(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if detail := d.WithArtifactUsage(*usage).Detail; !strings.Contains(detail, "2 unexpired artifacts hold 3.0 GiB") {
		t.Errorf("detail = %s", detail)
	}
}

// The log's address comes back in a response header. It may be on GitHub's blob
// storage and on nothing else the controller can reach.
func TestOnlyAnAddressGitHubCouldSensiblyNameIsFetched(t *testing.T) {
	tests := []struct {
		name string
		api  string
		log  string
		want bool
	}{
		{"GitHub's own blob storage", "https://api.github.com", "https://productionresultssa1.blob.core.windows.net/actions-results/x?sig=abc", true},
		{"an Enterprise Server's log host", "https://ghe.example.com/api/v3", "https://ghe.example.com/_logs/1?token=x", true},
		{"the cloud metadata service", "https://api.github.com", "https://169.254.169.254/latest/meta-data/", false},
		{"a private network", "https://api.github.com", "https://10.0.0.5/logs", false},
		{"this machine", "https://api.github.com", "https://localhost/logs", false},
		{"loopback written as a number", "https://api.github.com", "https://2130706433/logs", false},
		{"a downgrade to http", "https://api.github.com", "http://blob.example.com/logs", false},
		{"credentials in the address", "https://api.github.com", "https://user:secret@blob.example.com/logs", false},
		{"no host", "https://api.github.com", "https:///logs", false},
		// A server on a LAN, or the test's own, is entitled to keep its logs
		// beside itself: private is only refused when the API is not.
		{"a private API with its own private log host", "http://127.0.0.1:8080", "http://127.0.0.1:8080/_logs/1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.log)
			if err != nil {
				t.Fatal(err)
			}
			if got := fetchableLogAddress(tt.api, u); got != tt.want {
				t.Errorf("fetchableLogAddress(%q, %q) = %v, want %v", tt.api, tt.log, got, tt.want)
			}
		})
	}
	if fetchableLogAddress("https://api.github.com", nil) {
		t.Error("a missing address was fetched")
	}
}
