package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const oomExplanation = `{"job_id":"job_1","state":"completed","summary":"The kernel killed this job's runner for its memory limit.",
	"detail":"","fix":"give the pool more memory.","waiting":false,"blocked":false,"computed_at":"2026-10-08T12:00:00Z",
	"class":"oom","confidence":"high","confidence_reason":"",
	"evidence":[{"kind":"conclusion","label":"GitHub's conclusion","value":"failure"},
		{"kind":"exit_code","label":"exit code","value":"137"},
		{"kind":"memory_peak","label":"memory peak","value":"7900","unit":"MB"},
		{"kind":"memory_limit","label":"memory limit","value":"8192","unit":"MB"}],
	"log_excerpt":{"lines":[{"n":30,"text":"step output 30"},{"n":31,"text":"Killed process 2231 (node)","decisive":true}]},
	"problem_code":"jobs.oom_killed",
	"next_steps":[{"text":"Raise the pool's memory per runner.","kind":"change","link":"/pools/pool_1"},
		{"text":"Re-run the job once the cause is addressed.","kind":"rerun"},
		{"text":"Read what jobs.oom_killed means.","kind":"read","link":"https://zoomies.sh/problem-codes/#jobs-oom_killed"}]}`

// why is the answer to one question, laid out the way a person reads it: the
// verdict, how sure, the facts, the lines that decided it, and what to do.
func TestWhyPrintsTheClassTheEvidenceAndTheSteps(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/jobs/job_1/explanation": oomExplanation})
	out, _ := runCLI(t, "why", "job_1", "--url", srv.URL)
	for _, want := range []string{
		"The kernel killed this job's runner for its memory limit.",
		"Class: oom (high)",
		"memory peak: 7900 MB",
		"> 31",
		"Killed process 2231 (node)",
		"1. Raise the pool's memory per runner.",
		"2. Re-run the job once the cause is addressed.",
		"Read: https://zoomies.sh/problem-codes/#jobs-oom_killed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A GitHub URL is what a person has in their clipboard when they ask. The
// job is found through the jobs list by repository and run, and the job ID
// in the URL picks it out of the run's jobs.
func TestWhyResolvesAGitHubJobURL(t *testing.T) {
	var listed url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/jobs":
			listed = r.URL.Query()
			_, _ = w.Write([]byte(`{"items":[{"id":"job_8","github_job_id":8},{"id":"job_9","github_job_id":9}],"total":2}`))
		case "/api/v1/jobs/job_9/explanation":
			_, _ = w.Write([]byte(oomExplanation))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	out, _ := runCLI(t, "why", "https://github.com/acme/widgets/actions/runs/1234/job/9", "--url", srv.URL)
	if listed.Get("repo") != "acme/widgets" || listed.Get("run_id") != "1234" || listed.Get("include_steps") != "false" {
		t.Fatalf("the jobs list was asked %v; want repo, run_id and no steps", listed)
	}
	if !strings.Contains(out, "Class: oom") {
		t.Fatalf("the job named in the URL was not explained:\n%s", out)
	}
}

// A URL that names a job this fleet never saw is a job not found, which is
// exit 2 with the command that shows what the fleet did see: an operator
// pasting the wrong run should not read it as the controller failing.
func TestWhyExitsTwoForAJobNobodySaw(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/jobs": `{"items":[],"total":0}`})
	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"why", "https://github.com/acme/widgets/actions/runs/1234/job/9", "--url", srv.URL})
	if code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "zoomies jobs list --repo acme/widgets") {
		t.Fatalf("the error does not say where to look:\n%s", errOut.String())
	}
}

// Not enough data is its own outcome: the explanation is printed, and the
// exit code lets a script tell "diagnosed" from "the fleet could not say".
func TestWhyExitsThreeWhenTheClassIsUnknown(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/jobs/job_1/explanation": `{"job_id":"job_1","state":"completed",
		"summary":"This job ran and action_required.","waiting":false,"blocked":false,"computed_at":"2026-10-08T12:00:00Z",
		"class":"unknown","confidence":"low","confidence_reason":"GitHub concluded it action_required, which the fleet does not class",
		"evidence":[],"log_excerpt":null,"next_steps":[]}`})
	e, out, _ := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"why", "job_1", "--url", srv.URL})
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if !strings.Contains(out.String(), "Class: unknown (low)") || !strings.Contains(out.String(), "which the fleet does not class") {
		t.Fatalf("an unknown class is still explained:\n%s", out.String())
	}
}

// --latest-failed is the question "what broke last", narrowed by repository
// or pool, and the answer is the first job the failed list gives.
func TestWhyLatestFailedAsksTheListForTheNewestFailure(t *testing.T) {
	var listed url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/jobs":
			listed = r.URL.Query()
			_, _ = w.Write([]byte(`{"items":[{"id":"job_1","github_job_id":1}],"total":1}`))
		case "/api/v1/jobs/job_1/explanation":
			_, _ = w.Write([]byte(oomExplanation))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	out, _ := runCLI(t, "why", "--latest-failed", "--repo", "acme/widgets", "--pool", "pool_1", "--url", srv.URL)
	if listed.Get("failed") != "true" || listed.Get("repo") != "acme/widgets" || listed.Get("pool_id") != "pool_1" {
		t.Fatalf("the jobs list was asked %v", listed)
	}
	if !strings.Contains(out, "Class: oom") {
		t.Fatalf("the newest failure was not explained:\n%s", out)
	}
}

// --output json is the explanation as the controller sent it, so a script
// reads the same document the drawer does.
func TestWhyOutputsJSONVerbatim(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/jobs/job_1/explanation": oomExplanation})
	out, _ := runCLI(t, "why", "job_1", "--output", "json", "--url", srv.URL)
	if !strings.Contains(out, `"class": "oom"`) || !strings.Contains(out, `"problem_code": "jobs.oom_killed"`) {
		t.Fatalf("JSON output is not the explanation:\n%s", out)
	}
}

// jobs why is the same command found where jobs live.
func TestJobsWhyIsTheSameCommand(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/jobs/job_1/explanation": oomExplanation})
	viaJobs, _ := runCLI(t, "jobs", "why", "job_1", "--url", srv.URL)
	direct, _ := runCLI(t, "why", "job_1", "--url", srv.URL)
	if viaJobs != direct {
		t.Fatalf("jobs why and why differ:\n%s\n---\n%s", viaJobs, direct)
	}
}

// --no-logs asks the controller for no excerpt at all, which is logs=0 on
// the wire, and prints none.
func TestWhyNoLogsAsksForNone(t *testing.T) {
	var asked url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Replace(oomExplanation, `"log_excerpt":{"lines":[{"n":30,"text":"step output 30"},{"n":31,"text":"Killed process 2231 (node)","decisive":true}]}`, `"log_excerpt":null`, 1)))
	}))
	t.Cleanup(srv.Close)
	out, _ := runCLI(t, "why", "job_1", "--no-logs", "--url", srv.URL)
	if asked.Get("logs") != "0" {
		t.Fatalf("asked logs=%q, want 0", asked.Get("logs"))
	}
	if strings.Contains(out, "Killed process") {
		t.Fatalf("an excerpt was printed without one being asked for:\n%s", out)
	}
}
