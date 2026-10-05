package main

import (
	"strings"
	"testing"
	"unicode"
)

// A job, workflow, step or label name is written by whoever can open a pull
// request against a repository this fleet serves, and the CLI is what an
// operator runs during an incident on exactly those failing jobs. A raw escape
// sequence can rewrite the window title or overwrite earlier lines, and a
// newline forges a whole extra row -- for instance a "success" line for a job
// that failed. The MCP layer and the UI already treat these as hostile.
// hostileJSON is hostileName escaped the way the API would carry it.
const hostileJSON = `build\u001b]0;pwned\u0007\u001b[2K\u001b[1A\u009b31m\nacme/widgets  ci  deploy  success\u202eevil`

// shortHostileJSON is early enough to survive a listing's column truncation,
// which would otherwise cut the name before the hostile part.
const shortHostileJSON = `x\nfake  success\u001b[2K\u009b\u202e`

func assertNoTerminalControl(t *testing.T, what, out string, wantLines int) {
	t.Helper()
	for _, r := range out {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) || r == '\u202e' {
			t.Errorf("%s printed the control character %U to the terminal:\n%q", what, r, out)
			break
		}
	}
	if got := strings.Count(out, "\n"); got != wantLines {
		t.Errorf("%s printed %d lines, want %d -- a newline in a name forged a row:\n%q", what, got, wantLines, out)
	}
}

func TestJobsListPrintsHostileNamesWithoutControlCharacters(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/jobs": `{"items":[{"id":"job_1","repo":"acme/widgets","workflow":"` + shortHostileJSON + `",
			"job_name":"` + shortHostileJSON + `","state":"completed","conclusion":"failure","matched":true,
			"failed_step":{"number":2,"name":"` + shortHostileJSON + `"},
			"queued_at":"2025-01-01T00:00:00Z"}],"total":1}`,
	})
	out, _ := runCLI(t, "jobs", "list", "--url", srv.URL)
	// A header and one row.
	assertNoTerminalControl(t, "jobs list", out, 2)
}

func TestJobsGetPrintsHostileNamesWithoutControlCharacters(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/jobs/job_1": `{"id":"job_1","repo":"acme/widgets","workflow":"` + hostileJSON + `",
			"job_name":"` + hostileJSON + `","head_branch":"` + hostileJSON + `",
			"labels":["` + hostileJSON + `"],"state":"completed","conclusion":"failure","matched":true,
			"failed_step":{"number":2,"name":"` + hostileJSON + `"},
			"steps":[{"number":2,"name":"` + hostileJSON + `","status":"completed","conclusion":"failure"}],
			"queued_at":"2025-01-01T00:00:00Z"}`,
		"/api/v1/jobs/job_1/events": `{"items":[{"at":"2025-01-01T00:00:00Z","source":"webhook",
			"message":"` + hostileJSON + `"}],"total":1}`,
		"/api/v1/jobs/job_1/explanation": `{"summary":"` + hostileJSON + `","detail":"` + hostileJSON + `","fix":"` + hostileJSON + `"}`,
	})
	out, _ := runCLI(t, "jobs", "get", "job_1", "--url", srv.URL)
	assertNoTerminalControl(t, "jobs get", out, strings.Count(out, "\n"))

	// Line count cannot be fixed in advance for the detail view, so what
	// matters is that the forged row never starts a line of its own.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "acme/widgets  ci  deploy") {
			t.Errorf("a newline in a name started a forged line: %q", line)
		}
	}
}

func TestRunnersGetPrintsHostileNamesWithoutControlCharacters(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/runners/run_1": `{"id":"run_1","name":"zoomies-abc","state":"busy",
			"current_job":{"repo":"acme/widgets","workflow":"` + hostileJSON + `","job_name":"` + hostileJSON + `"},
			"message":"` + hostileJSON + `",
			"timeline":[{"state":"busy","at":"2025-01-01T00:00:00Z","message":"` + hostileJSON + `"}],
			"created_at":"2025-01-01T00:00:00Z"}`,
	})
	out, _ := runCLI(t, "runners", "get", "run_1", "--url", srv.URL)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "acme/widgets  ci  deploy") {
			t.Errorf("a newline in a name started a forged line: %q", line)
		}
	}
	assertNoTerminalControl(t, "runners get", out, strings.Count(out, "\n"))
}

func TestRunnersListPrintsHostileJobNamesWithoutControlCharacters(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/runners": `{"items":[{"id":"run_1","name":"zoomies-abc","state":"busy",
			"current_job":{"repo":"acme/widgets","job_name":"` + shortHostileJSON + `"},
			"created_at":"2025-01-01T00:00:00Z"}],"total":1}`,
	})
	out, _ := runCLI(t, "runners", "list", "--url", srv.URL)
	assertNoTerminalControl(t, "runners list", out, 2)
}

func TestPlainKeepsOrdinaryNamesAndReplacesWhatCouldMoveTheCursor(t *testing.T) {
	cases := []struct{ in, want string }{
		{"go test ./...", "go test ./..."},
		{"build (ubuntu, 1.27) ✓ 日本語", "build (ubuntu, 1.27) ✓ 日本語"},
		{"a\nb", "a b"},
		{"a\tb\rc", "a b c"},
		{"a\x1b[2Kb", "a�[2Kb"},
		{"a\u009bb", "a�b"},
		{"a\u202eb", "a�b"},
		{"a b", "a b"},
	}
	for _, tc := range cases {
		if got := plain(tc.in); got != tc.want {
			t.Errorf("plain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A problem can quote a job's name and its labels, which a workflow author wrote,
// and `zoomies status` is what an operator reads in the middle of an incident.
func TestStatusPrintsHostileNamesInProblemsWithoutControlCharacters(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{
		"/api/v1/meta":  `{"version":"1.0.0 (abc1234)","bootstrap_required":false}`,
		"/api/v1/stats": `{"window":"1h0m0s","runners":{},"hosts":{},"pools":[]}`,
		"/api/v1/problems": `{"ok":false,"items":[{"code":"jobs.unmatched","severity":"warning",
			"title":"a job is waiting for a runner that matches ` + shortHostileJSON + `",
			"detail":"The job ` + hostileJSON + ` asked for [` + hostileJSON + `].","fix":"` + hostileJSON + `"}]}`,
		"/api/v1/scaling-events": `{"items":[]}`,
	})
	out, _ := runCLI(t, "status", "--url", srv.URL)
	for _, line := range strings.Split(out, "\n") {
		for _, r := range line {
			if unicode.IsControl(r) || r == '\u202e' {
				t.Fatalf("status printed the control character %U:\n%q", r, line)
			}
		}
	}
	// A header block, a pools-free summary, the count line and the one problem's three
	// lines: a newline in a name must not add a row of its own.
	if strings.Contains(out, "fake  success") && strings.Contains(out, "\nfake") {
		t.Errorf("a newline in a name forged a row:\n%q", out)
	}
}

// A bidirectional mark is not a control character, and each can reorder what a terminal
// shows of a name a workflow author wrote.
func TestPlainReplacesEveryBidirectionalMark(t *testing.T) {
	for _, r := range []rune{'؜', '‎', '‏', '‪', '‮', '⁦', '⁩', ' ', ' '} {
		if got := plain("a" + string(r) + "b"); strings.ContainsRune(got, r) {
			t.Errorf("%U passed through plain: %q", r, got)
		}
	}
	if got := plain("build ci"); got != "build ci" {
		t.Errorf("an ordinary name changed: %q", got)
	}
}
