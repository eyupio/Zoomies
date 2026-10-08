package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// whyServer is a controller that answers by path and remembers what it was asked,
// because for this command the question matters as much as the answer: which job
// the CLI chose to explain is the behaviour.
type whyServer struct {
	*httptest.Server
	mu      sync.Mutex
	queries map[string][]url.Values
}

func newWhyServer(t *testing.T, routes map[string]string) *whyServer {
	t.Helper()
	ws := &whyServer{queries: map[string][]url.Values{}}
	ws.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws.mu.Lock()
		ws.queries[r.URL.Path] = append(ws.queries[r.URL.Path], r.URL.Query())
		ws.mu.Unlock()
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such thing"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ws.Close)
	return ws
}

// last is the newest query made to a path.
func (ws *whyServer) last(path string) url.Values {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	q := ws.queries[path]
	if len(q) == 0 {
		return nil
	}
	return q[len(q)-1]
}

// runWhyCLI runs the CLI and returns what it wrote and the exit status, which the
// shared helper cannot: it treats every nonzero status as a failure of the test,
// and for this command a nonzero status is an answer.
func runWhyCLI(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	e, o, eo := newTestEnv(t)
	code = dispatch(context.Background(), e, args)
	return o.String(), eo.String(), code
}

const oomExplanation = `{
	"job_id":"job_1","state":"completed","summary":"The kernel killed this job's runner, or one of its steps, for its memory limit.",
	"detail":"The runner this job was on stopped.","fix":"raise the pool's memory limit.","waiting":false,"blocked":false,
	"computed_at":"2026-10-08T12:00:00Z","class":"oom","confidence":"high",
	"evidence":[
		{"kind":"memory_peak","label":"Most memory it was measured using","value":"2900","unit":"MB"},
		{"kind":"memory_limit","label":"Memory it was given","value":"2048","unit":"MB"},
		{"kind":"duration","label":"Ran for","value":"300","unit":"s"},
		{"kind":"pool","label":"Pool","value":"zoomies-4vcpu","ref":"/pools/pool_1"}],
	"problem_code":"jobs.oom_killed",
	"next_steps":[
		{"text":"Raise the pool's memory limit.","kind":"change"},
		{"text":"Re-run the job once the limit is raised.","kind":"rerun","link":"https://github.com/acme/widgets/actions/runs/1/job/2"}]}`

const jobOne = `{"id":"job_1","repo":"acme/widgets","workflow":"CI","job_name":"build","state":"completed","conclusion":"failure",
	"queued_at":"2026-10-08T11:00:00Z"}`

const smallCatalog = `{"version":1,"entries":[{"id":"jobs.oom_killed","kind":"problem","docs_html":"https://zoomies.sh/problem-codes/#runtime-pools-jobs-and-runners"}]}`

// The page somebody reads when a job has failed: what is the matter, how sure the
// controller is, the facts it rests on in terms a person recognises, where to read
// more, and what to do, in order.
func TestWhyTellsAPersonWhatIsTheMatterAndWhatToDo(t *testing.T) {
	srv := newWhyServer(t, map[string]string{
		"/api/v1/jobs/job_1/explanation": oomExplanation,
		"/api/v1/jobs/job_1":             jobOne,
		"/api/v1/catalog":                smallCatalog,
	})

	out, errOut, code := runWhyCLI(t, "why", "job_1", "--url", srv.URL)

	if code != exitOK {
		t.Fatalf("exit status = %d, want 0 for a diagnosed job\n%s", code, errOut)
	}
	for _, want := range []string{
		"The kernel killed this job's runner",
		"acme/widgets, CI, build (job_1)",
		"oom, high confidence",
		// Numbers are said the way the rest of the CLI says them, not as 2900.
		"2.8 GB", "2.0 GB", "5m",
		"jobs.oom_killed", "https://zoomies.sh/problem-codes/#runtime-pools-jobs-and-runners",
		"1. [change] Raise the pool's memory limit.",
		"2. [rerun] Re-run the job once the limit is raised.  https://github.com/acme/widgets/actions/runs/1/job/2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the page does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Values in quotes") {
		t.Errorf("the page says some values are outside text when none are:\n%s", out)
	}
}

// A step's name is written by whoever can open a pull request, and this page is
// read during an incident. An escape sequence must not retitle the terminal, and a
// newline must not forge a line the fleet never wrote.
func TestWhyPrintsTextTheFleetDidNotWriteAsQuotedData(t *testing.T) {
	forged := "Run tests\n\x1b]0;pwned\x07Class: succeeded, high confidence"
	srv := newWhyServer(t, map[string]string{
		"/api/v1/jobs/job_1/explanation": `{"job_id":"job_1","state":"completed","summary":"This job ran and failure.","waiting":false,"blocked":false,
			"computed_at":"2026-10-08T12:00:00Z","class":"workflow-failure","confidence":"high",
			"evidence":[{"kind":"step","label":"Step it stopped at","value":` + jsonString(forged) + `,"untrusted":true}],
			"next_steps":[]}`,
		"/api/v1/jobs/job_1": jobOne,
	})

	out, _, code := runWhyCLI(t, "why", "job_1", "--url", srv.URL)

	if code != exitOK {
		t.Fatalf("exit status = %d, want 0", code)
	}
	if strings.ContainsRune(out, '\x1b') || strings.ContainsRune(out, '\x07') {
		t.Errorf("a control character reached the terminal:\n%q", out)
	}
	if strings.Contains(out, "\nClass: succeeded") {
		t.Errorf("a step's name forged a line of the page:\n%s", out)
	}
	if !strings.Contains(out, `"Run tests`) || !strings.Contains(out, "Values in quotes were written by the workflow or the runner") {
		t.Errorf("outside text was not quoted and labelled as data:\n%s", out)
	}
}

// A name the operator chose is the fleet's own words, and is not quoted, but a
// control character in it is no safer in a terminal than one a workflow wrote.
func TestWhyMakesControlCharactersInAnyValueHarmless(t *testing.T) {
	srv := newWhyServer(t, map[string]string{
		"/api/v1/jobs/job_1/explanation": `{"job_id":"job_1","state":"completed","summary":"This job ran and success.","waiting":false,"blocked":false,
			"computed_at":"2026-10-08T12:00:00Z","class":"succeeded","confidence":"high",
			"evidence":[{"kind":"pool","label":"Pool","value":"zoomies\u001b[2Jpool","ref":"/pools/pool_1"}],
			"next_steps":[{"text":"Open the pool\u001b[2J.","kind":"read","link":"/pools/pool_1"}]}`,
		"/api/v1/jobs/job_1": jobOne,
	})
	out, _, code := runWhyCLI(t, "why", "job_1", "--url", srv.URL)
	if code != exitOK || strings.ContainsRune(out, '\x1b') {
		t.Errorf("exit %d; a control character reached the terminal:\n%q", code, out)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteString(string("0123456789abcdef"[r>>4]))
			b.WriteString(string("0123456789abcdef"[r&0xf]))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// The exit status is part of the command's contract, and a script reads it before
// anything else: it is how it tells a diagnosed job from one the controller could
// not narrow, and both from one that is not there.
func TestWhyExitsWithTheStatusItDocuments(t *testing.T) {
	unknown := `{"job_id":"job_1","state":"completed","summary":"This job ran and stale.","waiting":false,"blocked":false,
		"computed_at":"2026-10-08T12:00:00Z","class":"unknown","confidence":"low",
		"confidence_reason":"GitHub's conclusion for this job, \"stale\", is not one the explainer has a rule for","evidence":[],"next_steps":[]}`
	srv := newWhyServer(t, map[string]string{
		"/api/v1/jobs/job_1/explanation": unknown,
		"/api/v1/jobs/job_1":             jobOne,
	})

	t.Run("a job the controller could not narrow exits 3 and still says why", func(t *testing.T) {
		out, _, code := runWhyCLI(t, "why", "job_1", "--url", srv.URL)
		if code != int(whyNotEnoughData) {
			t.Errorf("exit status = %d, want %d", code, whyNotEnoughData)
		}
		if !strings.Contains(out, "why not sure") || !strings.Contains(out, "is not one the explainer has a rule for") {
			t.Errorf("an unknown class has to say what was missing:\n%s", out)
		}
	})
	t.Run("a job that is not there exits 2 and names it", func(t *testing.T) {
		out, errOut, code := runWhyCLI(t, "why", "job_missing", "--url", srv.URL)
		if code != int(whyNotFound) {
			t.Errorf("exit status = %d, want %d", code, whyNotFound)
		}
		if out != "" || !strings.Contains(errOut, `there is no job "job_missing"`) {
			t.Errorf("stdout %q, stderr %q: the miss belongs on stderr and names what was looked for", out, errOut)
		}
	})
	t.Run("as json it prints the answer alone, and still exits with the status", func(t *testing.T) {
		out, errOut, code := runWhyCLI(t, "why", "job_1", "--url", srv.URL, "--output", "json")
		if code != int(whyNotEnoughData) {
			t.Errorf("exit status = %d, want %d", code, whyNotEnoughData)
		}
		if !strings.Contains(out, `"class": "unknown"`) && !strings.Contains(out, `"class":"unknown"`) {
			t.Errorf("the json answer is not the explanation:\n%s", out)
		}
		if errOut != "" {
			t.Errorf("json mode wrote to stderr: %q", errOut)
		}
		_, errOut, code = runWhyCLI(t, "why", "job_missing", "--url", srv.URL, "--output", "json")
		if code != int(whyNotFound) || errOut != "" {
			t.Errorf("a miss in json mode exits %d with stderr %q; it should exit 2 and say nothing", code, errOut)
		}
	})
	t.Run("an answer without a class from an older controller is not claimed as unknown", func(t *testing.T) {
		old := newWhyServer(t, map[string]string{
			"/api/v1/jobs/job_1/explanation": `{"job_id":"job_1","state":"queued","summary":"A runner is on its way for this job.","waiting":true,"blocked":false,"computed_at":"2026-10-08T12:00:00Z"}`,
			"/api/v1/jobs/job_1":             jobOne,
		})
		out, _, code := runWhyCLI(t, "why", "job_1", "--url", old.URL)
		if code != exitOK || !strings.Contains(out, "A runner is on its way") {
			t.Errorf("exit %d, output %q: a controller with no class still has a sentence to show", code, out)
		}
	})
}

// --latest-failed is the form somebody types in the middle of an incident, so it
// has to ask for exactly the newest failure, narrowed only as they said.
func TestWhyLatestFailedAsksForTheNewestFailureOnly(t *testing.T) {
	srv := newWhyServer(t, map[string]string{
		"/api/v1/jobs":                   `{"items":[{"id":"job_7","repo":"acme/widgets","state":"completed","conclusion":"failure"}],"total":1}`,
		"/api/v1/jobs/job_7/explanation": strings.ReplaceAll(oomExplanation, "job_1", "job_7"),
		"/api/v1/jobs/job_7":             jobOne,
		"/api/v1/catalog":                smallCatalog,
	})

	_, _, code := runWhyCLI(t, "why", "--latest-failed", "--repo", "acme/widgets", "--pool", "pool_1", "--url", srv.URL)
	if code != exitOK {
		t.Fatalf("exit status = %d", code)
	}
	q := srv.last("/api/v1/jobs")
	for key, want := range map[string]string{"failed": "true", "limit": "1", "repo": "acme/widgets", "pool_id": "pool_1"} {
		if got := q.Get(key); got != want {
			t.Errorf("the list was asked with %s=%q, want %q (full query %v)", key, got, want, q)
		}
	}

	t.Run("no failure to explain exits 2 and says what was narrowed", func(t *testing.T) {
		empty := newWhyServer(t, map[string]string{"/api/v1/jobs": `{"items":[],"total":0}`})
		_, errOut, code := runWhyCLI(t, "why", "--latest-failed", "--repo", "acme/widgets", "--url", empty.URL)
		if code != int(whyNotFound) || !strings.Contains(errOut, "no job has gone wrong in acme/widgets") {
			t.Errorf("exit %d, stderr %q", code, errOut)
		}
	})
}

// A GitHub address is what is in the clipboard when a check goes red. A job's own
// address names the job; a run's names several, and the one that went wrong is the
// one somebody opened the run to find.
func TestWhyResolvesGitHubAddresses(t *testing.T) {
	list := `{"items":[
		{"id":"job_a","github_job_id":111,"github_run_id":900,"repo":"acme/widgets","job_name":"lint","state":"completed","conclusion":"success"},
		{"id":"job_b","github_job_id":222,"github_run_id":900,"repo":"acme/widgets","job_name":"test","state":"completed","conclusion":"failure"},
		{"id":"job_c","github_job_id":333,"github_run_id":900,"repo":"acme/widgets","job_name":"build","state":"completed","conclusion":"success"}],"total":3}`
	routes := map[string]string{"/api/v1/jobs": list}
	for _, id := range []string{"job_a", "job_b", "job_c"} {
		routes["/api/v1/jobs/"+id+"/explanation"] = strings.ReplaceAll(oomExplanation, "job_1", id)
		routes["/api/v1/jobs/"+id] = strings.ReplaceAll(jobOne, "job_1", id)
	}
	routes["/api/v1/catalog"] = smallCatalog
	srv := newWhyServer(t, routes)

	t.Run("a job address names the job", func(t *testing.T) {
		out, _, code := runWhyCLI(t, "why", "https://github.com/acme/widgets/actions/runs/900/job/333", "--url", srv.URL)
		if code != exitOK || !strings.Contains(out, "(job_c)") {
			t.Errorf("exit %d; the job explained was not job_c:\n%s", code, out)
		}
		q := srv.last("/api/v1/jobs")
		if q.Get("repo") != "acme/widgets" || q.Get("run_id") != "900" {
			t.Errorf("the run was looked up with %v", q)
		}
	})
	t.Run("a run address picks the job that went wrong and says there were others", func(t *testing.T) {
		out, _, code := runWhyCLI(t, "why", "https://github.com/acme/widgets/actions/runs/900", "--url", srv.URL)
		if code != exitOK || !strings.Contains(out, "(job_b)") {
			t.Errorf("exit %d; the failed job was not the one explained:\n%s", code, out)
		}
		if !strings.Contains(out, `Run 900 has 3 jobs on this fleet; this is "test".`) {
			t.Errorf("the page does not say which of the run's jobs it chose:\n%s", out)
		}
	})
	t.Run("a job that is not on this fleet exits 2", func(t *testing.T) {
		_, errOut, code := runWhyCLI(t, "why", "https://github.com/acme/widgets/actions/runs/900/job/999", "--url", srv.URL)
		if code != int(whyNotFound) || !strings.Contains(errOut, "run 900 of acme/widgets has no job 999 on this fleet") {
			t.Errorf("exit %d, stderr %q", code, errOut)
		}
	})
	t.Run("a run the fleet never saw exits 2 and says it may be somebody else's", func(t *testing.T) {
		empty := newWhyServer(t, map[string]string{"/api/v1/jobs": `{"items":[],"total":0}`})
		_, errOut, code := runWhyCLI(t, "why", "https://github.com/acme/widgets/actions/runs/5", "--url", empty.URL)
		if code != int(whyNotFound) || !strings.Contains(errOut, "somebody else's runners") {
			t.Errorf("exit %d, stderr %q", code, errOut)
		}
	})
}

// The same command is found where jobs live, and does exactly the same thing.
func TestJobsWhyIsTheSameCommand(t *testing.T) {
	srv := newWhyServer(t, map[string]string{
		"/api/v1/jobs/job_1/explanation": oomExplanation,
		"/api/v1/jobs/job_1":             jobOne,
		"/api/v1/catalog":                smallCatalog,
	})
	a, _, codeA := runWhyCLI(t, "why", "job_1", "--url", srv.URL)
	b, _, codeB := runWhyCLI(t, "jobs", "why", "job_1", "--url", srv.URL)
	if codeA != exitOK || codeB != exitOK || a != b {
		t.Errorf("why and jobs why differ (exit %d and %d):\n%s\n----\n%s", codeA, codeB, a, b)
	}
}

// The narrowing flags only mean something with --latest-failed. Silently
// ignoring --repo on a named job would explain a job in a repository the person
// did not mean, and say nothing.
func TestWhyRefusesNarrowingWithoutLatestFailed(t *testing.T) {
	srv := newWhyServer(t, map[string]string{})
	_, errOut, code := runWhyCLI(t, "why", "job_1", "--repo", "acme/widgets", "--url", srv.URL)
	if code != exitUsage || !strings.Contains(errOut, "only narrow --latest-failed") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	_, errOut, code = runWhyCLI(t, "why", "--url", srv.URL)
	if code != exitUsage || !strings.Contains(errOut, "needs a job ID") {
		t.Errorf("exit %d, stderr %q: naming nothing is a usage mistake", code, errOut)
	}
}
