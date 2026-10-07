package controller

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/store"
)

// osHealthCodes are the three problems a host's OS report can raise.
var osHealthCodes = []string{"host.os_health", "host.health_stale", "host.reboot_pending"}

// check builds a result the way the engine stamps one: an id the controller
// knows, a tier, an outcome and whether it is only a suggestion. Title,
// Current and Rationale are filled with text that must never reach a problem,
// because they are the agent's.
func check(id string, tier hosttune.Tier, status hosttune.Status, optional bool) hosttune.Result {
	return hosttune.Result{
		ID: id, Tier: tier, Status: status, Optional: optional,
		Title: "AGENT TITLE " + id, Current: "AGENT CURRENT " + id,
		Recommended: "AGENT RECOMMENDED " + id, Rationale: "AGENT RATIONALE " + id, Reason: "AGENT REASON " + id,
	}
}

// report is a full report from a native agent, taken now.
func report(now time.Time, rs ...hosttune.Result) *hosttune.Report {
	return &hosttune.Report{CheckedAt: now, OS: "linux", Distro: "ubuntu 24.04", Results: rs}
}

// reports stores a host's latest OS report the way a heartbeat leaves it. The
// host row is read again by every problems pass, so this is the whole setup.
func (h *harness) reports(t *testing.T, host *store.Host, r *hosttune.Report) {
	t.Helper()
	if err := h.st.SetHostDoctor(h.ctx, host.ID, r); err != nil {
		t.Fatalf("SetHostDoctor: %v", err)
	}
}

// silence makes a host miss its heartbeats, which is all "offline" means.
func (h *harness) silence(t *testing.T, host *store.Host) {
	t.Helper()
	host.LastHeartbeat = time.Now().Add(-2 * store.HeartbeatTimeout)
	if err := h.st.UpdateHost(h.ctx, host); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
}

// problemsOf is every problem in the list with a code, for the tests that care
// how many there are as much as what they say.
func (h *harness) problemsOf(t *testing.T, code string) []Problem {
	t.Helper()
	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatalf("Problems: %v", err)
	}
	var out []Problem
	for _, p := range ps {
		if p.Code == code {
			out = append(out, p)
		}
	}
	return out
}

// raised is which of the three OS health codes the list carries.
func (h *harness) raised(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, code := range osHealthCodes {
		if len(h.problemsOf(t, code)) > 0 {
			out = append(out, code)
		}
	}
	return out
}

// A counted warning is a warning and only a counted error is an error. The
// owner's rule is that a stock host carries some warnings for ever, so a page
// for one would train people to ignore the page; what is worth waking for is a
// check that could not run, because then nobody knows what the host is doing.
func TestACountedWarningIsAWarningAndOnlyACountedErrorIsAnError(t *testing.T) {
	cases := []struct {
		name    string
		results []hosttune.Result
		want    config.Severity
		title   string
		isError bool
	}{
		{"one counted warning", []hosttune.Result{
			check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
		}, config.SeverityWarning, "host build-04 has an OS health check that needs attention", false},
		{"three counted warnings", []hosttune.Result{
			check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
			check("disk.space", hosttune.Safe, hosttune.Warn, false),
			check("docker.logs", hosttune.Safe, hosttune.Warn, false),
		}, config.SeverityWarning, "host build-04 has 3 OS health checks that need attention", false},
		{"one counted error", []hosttune.Result{
			check("docker.logs", hosttune.Safe, hosttune.Error, false),
		}, config.SeverityError, "host build-04 has an OS health check that needs attention", true},
		{"an error among warnings", []hosttune.Result{
			check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
			check("docker.logs", hosttune.Safe, hosttune.Error, false),
		}, config.SeverityError, "host build-04 has 2 OS health checks that need attention", true},
		{"an error that is only a suggestion does not make it one", []hosttune.Result{
			check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
			check("service.snapd", hosttune.Dedicated, hosttune.Error, false),
		}, config.SeverityWarning, "host build-04 has an OS health check that needs attention", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			host := h.host("build-04")
			h.reports(t, host, report(h.c.Now(), tc.results...))

			ps := h.problemsOf(t, "host.os_health")
			if len(ps) != 1 {
				t.Fatalf("host.os_health entries = %d, want 1: %v", len(ps), h.problemCodes())
			}
			p := ps[0]
			if p.Severity != tc.want || p.Title != tc.title {
				t.Errorf("severity %s, title %q; want %s, %q", p.Severity, p.Title, tc.want, tc.title)
			}
			if p.TargetKind != "host" || p.TargetID != host.ID || p.Audience != AudienceFleet {
				t.Errorf("target %s/%s, audience %q; want this host, for the fleet", p.TargetKind, p.TargetID, p.Audience)
			}
			// The controller never changes the operating system, and the autopilot
			// acts only on a problem that carries a remedy, so none may.
			if p.Remedy != nil || p.Setting != "" || p.Since != nil {
				t.Errorf("remedy %v, setting %q, since %v; want none of them", p.Remedy, p.Setting, p.Since)
			}
			if got := strings.Contains(p.Detail, "could not run failed"); got != tc.isError {
				t.Errorf("detail explains a check that could not run = %v, want %v: %q", got, tc.isError, p.Detail)
			}
			// The command that works where the person is: on the host, which needs no
			// address and no token. `--host` reads through the controller and needs
			// both, so it is not offered as text to be typed -- the host's page has
			// it written out, with a token made on the spot -- and an advice that
			// led with it was how somebody ended up running it on the host, with
			// neither, and being told the controller was unreachable.
			if !strings.Contains(p.Fix, "on "+host.Name+", run `sudo zoomies doctor --verbose`") || !strings.Contains(p.Fix, "`sudo zoomies doctor --interactive`") {
				t.Errorf("fix %q should name the two commands to run on the host, and the host", p.Fix)
			}
			if !strings.Contains(p.Fix, "the host's page has the command") || !strings.Contains(p.Fix, "short-lived token") {
				t.Errorf("fix %q should send a person on another machine to the page that has the command", p.Fix)
			}
			if strings.Contains(p.Fix, "--host") {
				t.Errorf("fix %q hands over a command that cannot work as written", p.Fix)
			}
			if strings.Contains(strings.ToLower(p.Fix), "controller log") {
				t.Errorf("fix %q sends the fleet to a log it cannot read", p.Fix)
			}
		})
	}
}

// The aggressive and dedicated tiers and the optional checks are choices an
// operator has not made, not faults, and a stock host carries some for ever.
// Raising a problem for them would put a permanent entry on every fleet.
func TestSuggestionsNeverRaiseAProblem(t *testing.T) {
	h := newHarness(t)
	host := h.host("stock")
	h.reports(t, host, report(h.c.Now(),
		check("cgroup.version", hosttune.Safe, hosttune.OK, false),
		check("cpu.governor", hosttune.Aggressive, hosttune.Warn, false),
		check("memory.swappiness", hosttune.Aggressive, hosttune.Error, false),
		check("tmp.tmpfs", hosttune.Aggressive, hosttune.Warn, true),
		check("service.snapd", hosttune.Dedicated, hosttune.Warn, false),
		check("memory.swap-off", hosttune.Dedicated, hosttune.Warn, true),
		check("kernel.hwe-install", hosttune.Dedicated, hosttune.Skip, true),
		// A skipped counted check is not a finding either: it is a check that
		// could not be made, which the host's page says and a problem does not.
		check("files.service", hosttune.Safe, hosttune.Skip, false),
	))
	if got := h.raised(t); len(got) != 0 {
		t.Fatalf("a host with only suggestions raised %v", got)
	}
}

// A pending reboot is one fact that a report carries twice, as the
// kernel.pending warning and as the reboot flag. It is counted once, as the
// reboot: a host whose only finding is a reboot is not unwell, and an entry
// saying "an OS health check needs attention" over a reboot would put the same
// thing on the page twice.
func TestAPendingRebootIsCountedOnceAsTheReboot(t *testing.T) {
	t.Run("a reboot alone raises the reboot and not a health problem", func(t *testing.T) {
		h := newHarness(t)
		host := h.host("reboot-only")
		r := report(h.c.Now(), check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false))
		r.RebootPending = true
		h.reports(t, host, r)

		if got := h.raised(t); !slices.Equal(got, []string{"host.reboot_pending"}) {
			t.Fatalf("raised %v, want only host.reboot_pending", got)
		}
		p := h.problem(t, "host.reboot_pending")
		if p.Severity != config.SeverityInfo || p.Title != "host reboot-only is waiting for a reboot" {
			t.Errorf("reboot problem = %s %q", p.Severity, p.Title)
		}
		if p.Remedy != nil || p.Setting != "" || p.Since != nil || p.TargetID != host.ID {
			t.Errorf("remedy %v, setting %q, since %v, target %q", p.Remedy, p.Setting, p.Since, p.TargetID)
		}
	})
	t.Run("a reboot beside a real finding raises both, and the health one does not name it", func(t *testing.T) {
		h := newHarness(t)
		host := h.host("both")
		r := report(h.c.Now(),
			check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
			check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false),
		)
		r.RebootPending = true
		h.reports(t, host, r)

		if got := h.raised(t); !slices.Equal(got, []string{"host.os_health", "host.reboot_pending"}) {
			t.Fatalf("raised %v, want the health problem and the reboot", got)
		}
		p := h.problem(t, "host.os_health")
		if !strings.Contains(p.Title, "has an OS health check that needs") {
			t.Errorf("title %q counts the reboot as a finding", p.Title)
		}
		if strings.Contains(p.Detail, "Installed kernel awaiting reboot") {
			t.Errorf("detail %q names the reboot as a finding", p.Detail)
		}
	})
	t.Run("a kernel warning with no reboot flag is a finding like any other", func(t *testing.T) {
		// A report that does not say a reboot is pending is judged by its rows.
		h := newHarness(t)
		host := h.host("unflagged")
		h.reports(t, host, report(h.c.Now(), check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false)))
		if got := h.raised(t); !slices.Equal(got, []string{"host.os_health"}) {
			t.Fatalf("raised %v, want host.os_health", got)
		}
	})
}

// Nothing is raised where there is nothing fair to say, and the three cases are
// the ones where saying something would be wrong rather than merely unhelpful.
func TestAHostWithNothingFairToJudgeRaisesNothing(t *testing.T) {
	findings := func() []hosttune.Result {
		return []hosttune.Result{
			check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
			check("docker.logs", hosttune.Safe, hosttune.Error, false),
			check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false),
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, h *harness, host *store.Host)
	}{
		{"a host that has sent no report", func(t *testing.T, h *harness, host *store.Host) {}},
		{"an offline host, whatever its last report said", func(t *testing.T, h *harness, host *store.Host) {
			h.silence(t, host)
			// An old report as well: an offline host is silent about all three,
			// because host.unhealthy already speaks and a report from before it
			// went quiet describes a machine nobody can see.
			r := report(h.c.Now().Add(-time.Hour), findings()...)
			r.RebootPending = true
			h.reports(t, host, r)
		}},
		{"a container's partial report", func(t *testing.T, h *harness, host *store.Host) {
			// The container's own view skips most checks and warns about the
			// image's distribution for ever, and the monitor falls back to it
			// when the host's own file goes stale, so it is also fresh.
			r := report(h.c.Now(), findings()...)
			r.Container, r.RebootPending = true, true
			h.reports(t, host, r)
		}},
		{"a stale container report", func(t *testing.T, h *harness, host *store.Host) {
			r := report(h.c.Now().Add(-time.Hour), findings()...)
			r.Container, r.RebootPending = true, true
			h.reports(t, host, r)
		}},
		{"a full report with nothing to say", func(t *testing.T, h *harness, host *store.Host) {
			h.reports(t, host, report(h.c.Now(), check("cgroup.version", hosttune.Safe, hosttune.OK, false)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			host := h.host("quiet")
			tc.setup(t, h, host)
			if got := h.raised(t); len(got) != 0 {
				t.Fatalf("raised %v, want none", got)
			}
		})
	}
}

// The age is judged against the controller's clock, and only a report older than
// ReportStaleAfter is stale. A report a minute in the future is accepted on
// arrival, so its age is negative and it must not read as stale: a host whose
// clock runs a little fast is not a host whose collector has stopped.
func TestAReportIsStaleOnlyOnceItIsOlderThanTheThreshold(t *testing.T) {
	now := time.Date(2026, 10, 6, 14, 30, 0, 0, time.UTC)
	host := &store.Host{ID: "host_k3f9qz2mx7ab", Name: "build-04"}
	cases := []struct {
		name  string
		age   time.Duration
		stale bool
	}{
		{"a report taken now", 0, false},
		{"a report a minute in the future", -time.Minute, false},
		{"a report a nanosecond short of the threshold", hosttune.ReportStaleAfter - time.Nanosecond, false},
		{"a report exactly as old as the threshold", hosttune.ReportStaleAfter, false},
		{"a report a nanosecond past the threshold", hosttune.ReportStaleAfter + time.Nanosecond, true},
		{"a report an hour old", time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := report(now.Add(-tc.age))
			p, ok := healthStaleProblem(host, r, now)
			if ok != tc.stale {
				t.Fatalf("stale = %v, want %v", ok, tc.stale)
			}
			if !ok {
				return
			}
			if p.Severity != config.SeverityWarning || p.Code != "host.health_stale" {
				t.Errorf("problem = %s %s, want a warning", p.Code, p.Severity)
			}
			if p.Since == nil || !p.Since.Equal(r.CheckedAt) {
				t.Errorf("since = %v, want the report's own time %v", p.Since, r.CheckedAt)
			}
		})
	}

	// The words, once: an absolute time and never an age, because the list is sent
	// only when it changes and an age is a new problem every minute.
	p, _ := healthStaleProblem(host, report(now.Add(-time.Hour)), now)
	if p.Title != "host build-04 has sent no OS health report for over 10 minutes" {
		t.Errorf("title = %q", p.Title)
	}
	if !strings.Contains(p.Detail, "is dated 2026-10-06T13:30:00Z by the host's own clock") {
		t.Errorf("detail = %q, want the report's absolute time", p.Detail)
	}
	if !strings.Contains(p.Fix, "`sudo systemctl restart zoomies-agent`") || !strings.Contains(p.Fix, "`timedatectl`") {
		t.Errorf("fix = %q", p.Fix)
	}
	if p.Remedy != nil || p.Setting != "" {
		t.Errorf("remedy %v, setting %q", p.Remedy, p.Setting)
	}
}

// Through the whole controller: a connected host whose newest report is old
// raises the stale problem, and the findings in that old report are still
// standing. A collector that has stopped does not fix an OS setting, and hiding
// them would turn "I can no longer see" into "all clear".
func TestAStaleReportOnAConnectedHostRaisesTheStaleProblemAndKeepsTheRest(t *testing.T) {
	h := newHarness(t)
	host := h.host("quiet-collector")
	r := report(h.c.Now().Add(-hosttune.ReportStaleAfter-time.Minute),
		check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
		check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false))
	r.RebootPending = true
	h.reports(t, host, r)

	if got := h.raised(t); !slices.Equal(got, osHealthCodes) {
		t.Fatalf("raised %v, want all three", got)
	}
	p := h.problem(t, "host.health_stale")
	if p.Since == nil || !p.Since.Equal(r.CheckedAt) || p.TargetID != host.ID {
		t.Errorf("stale problem = %+v", p)
	}
}

// One entry per host per code. A fleet of hosts that all need attention is a
// list with one line for each, not a line for every check on every host.
func TestEachHostRaisesAtMostOneEntryPerCode(t *testing.T) {
	h := newHarness(t)
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		host := h.host(name)
		ids = append(ids, host.ID)
		r := report(h.c.Now().Add(-time.Hour),
			check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
			check("docker.logs", hosttune.Safe, hosttune.Error, false),
			check("disk.space", hosttune.Safe, hosttune.Warn, false),
			check("disk.inodes", hosttune.Safe, hosttune.Warn, false),
			check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false))
		r.RebootPending = true
		h.reports(t, host, r)
	}
	for _, code := range osHealthCodes {
		ps := h.problemsOf(t, code)
		var targets []string
		for _, p := range ps {
			targets = append(targets, p.TargetID)
		}
		slices.Sort(targets)
		want := slices.Clone(ids)
		slices.Sort(want)
		if !slices.Equal(targets, want) {
			t.Errorf("%s targets = %v, want one for each of %v", code, targets, want)
		}
	}
}

// The list is sent to every open tab whenever its JSON differs from the last one
// sent. A report arrives about once a minute with a new time on it and often with
// new numbers in the agent's own text -- disk.space's current value is "9% free
// (47244816)" and moves with every file written -- so a sentence that carried
// either would re-send the whole list every minute for as long as the host had a
// finding.
func TestTheHealthProblemDoesNotMoveWhenOnlyTheReportDoes(t *testing.T) {
	host := &store.Host{ID: "host_k3f9qz2mx7ab", Name: "build-04"}
	at := time.Date(2026, 10, 6, 14, 0, 0, 0, time.UTC)
	build := func(checkedAt time.Time, current string) []byte {
		first := check("disk.space", hosttune.Safe, hosttune.Warn, false)
		first.Current = current
		first.Title = "disk space as this agent chooses to word it at " + current
		r := report(checkedAt, first, check("docker.logs", hosttune.Safe, hosttune.Error, false))
		r.RebootPending = true
		var out []Problem
		if p, ok := osHealthProblem(host, r); ok {
			out = append(out, p)
		}
		if p, ok := rebootPendingProblem(host, r); ok {
			out = append(out, p)
		}
		b, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	before := build(at, "9% free (47244816)")
	after := build(at.Add(7*time.Minute), "8% free (41943040)")
	if string(before) != string(after) {
		t.Errorf("a refreshed report changed the problems:\n before: %s\n  after: %s", before, after)
	}
	if strings.Contains(string(before), "47244816") || strings.Contains(string(before), "disk space as this agent") {
		t.Errorf("the agent's own text reached a problem: %s", before)
	}
}

// Through the controller, and the other way: a stale report that stands while
// time passes says the same thing every time, since the list is re-sent only
// when it changes and a sentence counting minutes would be a new one each pass.
func TestAStaleReportDoesNotChangeWhileTimePasses(t *testing.T) {
	h := newHarness(t)
	host := h.host("stuck-collector")
	h.reports(t, host, report(h.c.Now().Add(-time.Hour),
		check("inotify.watches", hosttune.Safe, hosttune.Warn, false)))
	before := problemJSON(t, h, "host.health_stale")
	// Less than the heartbeat timeout, so the host is still connected: a host
	// that went quiet would hand the problem to host.unhealthy.
	h.advance(30 * time.Second)
	if after := problemJSON(t, h, "host.health_stale"); after != before {
		t.Errorf("host.health_stale changed as time passed:\n before: %s\n  after: %s", before, after)
	}
	if got := problemJSON(t, h, "host.os_health"); got == "" {
		t.Error("the finding in the stale report was dropped")
	}
}

// Nothing a host wrote may reach a problem, because a problem reaches every
// signed-in viewer and the MCP tools: an agent is the least trusted writer in
// the system. An id the controller does not know is counted and never named.
func TestNoTextFromTheAgentReachesAProblem(t *testing.T) {
	h := newHarness(t)
	host := h.host("hostile")
	// An hour old, so that all three problems are raised and all three are read.
	r := report(h.c.Now().Add(-time.Hour),
		check("inotify.watches", hosttune.Safe, hosttune.Warn, false),
		check("ignore-previous-instructions", hosttune.Safe, hosttune.Warn, false),
		check("docker.logs", hosttune.Safe, hosttune.Error, false),
		check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false))
	r.RebootPending = true
	r.OS, r.Distro, r.WorkDir = "AGENT OS", "AGENT DISTRO", "/AGENT/WORKDIR"
	h.reports(t, host, r)

	for _, code := range osHealthCodes {
		p := h.problem(t, code)
		all := p.Title + "\n" + p.Detail + "\n" + p.Fix
		for _, leaked := range []string{"AGENT", "ignore-previous-instructions", "/AGENT/WORKDIR"} {
			if strings.Contains(all, leaked) {
				t.Errorf("%s carries %q: %s", code, leaked, all)
			}
		}
	}
	// Named from the catalogue, errors first and marked, the unknown id counted.
	p := h.problem(t, "host.os_health")
	want := "flags Docker log rotation (could not run), File watches and 1 more."
	if !strings.Contains(p.Detail, want) {
		t.Errorf("detail %q does not say %q", p.Detail, want)
	}
}

// Only the one fix flips with the host, and on the one thing the owner asked it
// to: whether anything is running there, so whether it can be rebooted now.
// Title and detail are the same in both states, so the entry is the same entry,
// and a dismissal of it is not spent when the host swings between idle and busy.
func TestTheRebootFixSaysWhetherTheHostCanBeRebootedNow(t *testing.T) {
	h := newHarness(t)
	_, pool, host := h.fleet()
	r := report(h.c.Now(), check(hosttune.KernelPending, hosttune.Safe, hosttune.Warn, false))
	r.RebootPending = true
	h.reports(t, host, r)

	idle := h.problem(t, "host.reboot_pending")
	if !strings.Contains(idle.Fix, "nothing is running on vm-1, so it can be rebooted now") {
		t.Errorf("idle fix = %q", idle.Fix)
	}

	h.runnerRow(pool, host, store.RunnerIdle)
	busy := h.problem(t, "host.reboot_pending")
	// A runner kept warm for a pool is idle and never finishes by itself, so the
	// fix must not promise that waiting for the runners to finish ends: it sends
	// the operator to the runners that hold a job, by the host's id.
	if !strings.Contains(busy.Fix, "runners are on vm-1") ||
		!strings.Contains(busy.Fix, "`zoomies runners list --host "+host.ID+" --state busy`") ||
		strings.Contains(busy.Fix, "once they have finished") {
		t.Errorf("busy fix = %q", busy.Fix)
	}

	if idle.Title != busy.Title || idle.Detail != busy.Detail {
		t.Errorf("title or detail moved with the host's load:\n idle: %q %q\n busy: %q %q", idle.Title, idle.Detail, busy.Title, busy.Detail)
	}
	for _, fix := range []string{idle.Fix, busy.Fix} {
		// Cordon, not drain: drain cordons and then stops a busy runner after
		// five minutes, which is the wrong advice for waiting for jobs.
		if !strings.Contains(fix, "`zoomies hosts cordon "+host.ID+"`") || !strings.Contains(fix, "`zoomies hosts uncordon "+host.ID+"`") {
			t.Errorf("fix %q should name cordon and uncordon with the host's id", fix)
		}
		if strings.Contains(fix, "drain") {
			t.Errorf("fix %q advises drain", fix)
		}
		// A count would change with every runner that came and went, and the
		// entry would be re-sent each time; the host's own name and id are the
		// only digits there may be.
		if bare := strings.NewReplacer(host.ID, "", host.Name, "").Replace(fix); strings.ContainsAny(bare, "0123456789") {
			t.Errorf("fix %q carries a number", fix)
		}
	}
	if busy.Severity != config.SeverityInfo {
		t.Errorf("severity = %s, want info", busy.Severity)
	}
}

// A cordoned host is exactly where a reboot belongs, so it is not skipped.
func TestACordonedHostStillRaisesTheRebootAndItsFindings(t *testing.T) {
	h := newHarness(t)
	host := h.host("cordoned")
	if err := h.st.SetHostCordoned(h.ctx, host.ID, true); err != nil {
		t.Fatal(err)
	}
	r := report(h.c.Now(), check("inotify.watches", hosttune.Safe, hosttune.Warn, false))
	r.RebootPending = true
	h.reports(t, host, r)
	if got := h.raised(t); !slices.Equal(got, []string{"host.os_health", "host.reboot_pending"}) {
		t.Fatalf("raised %v", got)
	}
}

// What a problem says about which checks, from the catalogue and never the
// report, in the order Findings gives them.
func TestNamingTheFindings(t *testing.T) {
	warn := func(id string) hosttune.Result { return check(id, hosttune.Safe, hosttune.Warn, false) }
	fail := func(id string) hosttune.Result { return check(id, hosttune.Safe, hosttune.Error, false) }
	cases := []struct {
		name string
		in   []hosttune.Result
		want string
	}{
		{"one", []hosttune.Result{warn("inotify.watches")}, "File watches"},
		{"a check that could not run says so", []hosttune.Result{fail("docker.logs")}, "Docker log rotation (could not run)"},
		{"two", []hosttune.Result{warn("inotify.watches"), warn("disk.space")}, "File watches and Work directory free space"},
		{"three are all named", []hosttune.Result{warn("inotify.watches"), warn("disk.space"), warn("docker.logs")},
			"File watches, Work directory free space and Docker log rotation"},
		{"the rest are counted", []hosttune.Result{warn("inotify.watches"), warn("disk.space"), warn("docker.logs"), warn("disk.inodes")},
			"File watches, Work directory free space, Docker log rotation and 1 more"},
		{"an id nothing here knows is counted and never named", []hosttune.Result{warn("made.up"), warn("inotify.watches")},
			"File watches and 1 more"},
		{"unknown ids are skipped when naming, so they do not use up the three", []hosttune.Result{
			warn("a.b"), warn("c.d"), warn("inotify.watches"), warn("disk.space"), warn("docker.logs")},
			"File watches, Work directory free space, Docker log rotation and 2 more"},
		{"none known", []hosttune.Result{warn("a.b")}, "1 check this controller does not recognise"},
		{"none known, several", []hosttune.Result{warn("a.b"), fail("c.d")}, "2 checks this controller does not recognise"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nameFindings(tc.in); got != tc.want {
				t.Errorf("nameFindings = %q, want %q", got, tc.want)
			}
		})
	}
}

// The three are the fleet's, and they never reach the public status page: they
// say whether a machine's settings match a recommendation, not whether a job
// will run, and a stock fleet carries some of them for ever. Were they on it, the
// page would read "degraded" for people with no account over a sysctl, and
// "blocked" over a check that could not read a file.
func TestTheOSHealthProblemsNeverMoveThePublicStatus(t *testing.T) {
	for _, code := range osHealthCodes {
		if audienceFor(code) != AudienceFleet {
			t.Errorf("%s is %q, want the fleet's", code, audienceFor(code))
		}
		if !statusExempt(code) {
			t.Errorf("%s is not exempt from the status page", code)
		}
		if _, has := publicSentences[code]; has {
			t.Errorf("%s has a public sentence it can never show", code)
		}
	}

	h := newHarness(t)
	host := h.host("noisy")
	r := report(h.c.Now().Add(-time.Hour), check("docker.logs", hosttune.Safe, hosttune.Error, false))
	r.RebootPending = true
	h.reports(t, host, r)

	ps, err := h.c.Problems(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := h.raised(t); !slices.Equal(got, osHealthCodes) {
		t.Fatalf("raised %v, want all three, or the rest of this proves nothing", got)
	}
	var sawError bool
	for _, p := range ps {
		sawError = sawError || (p.Code == "host.os_health" && p.Severity == config.SeverityError)
	}
	if !sawError {
		t.Fatal("no error-severity host.os_health, so this does not exercise the case that would turn the page to blocked")
	}
	st := ProjectStatus(ps, nil)
	if st.State != FleetHealthy || len(st.Reasons) != 0 || len(st.Explanations) != 0 {
		t.Fatalf("status = %+v, want healthy with no reasons", st)
	}
	body, _ := json.Marshal(st)
	if strings.Contains(string(body), "host.os_health") || strings.Contains(string(body), "noisy") {
		t.Errorf("the status carries an OS health problem: %s", body)
	}

	// And they do not hide a real problem beside them.
	st = ProjectStatus([]Problem{
		{Code: "host.os_health", Severity: config.SeverityError, Audience: AudienceFleet},
		{Code: "host.unhealthy", Severity: config.SeverityWarning, Audience: AudienceFleet},
	}, nil)
	if st.State != FleetDegraded || len(st.Reasons) != 1 || st.Reasons[0].Code != "host.unhealthy" {
		t.Errorf("status = %+v, want degraded by the host alone", st)
	}
}
