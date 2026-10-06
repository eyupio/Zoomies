package hosttune

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// res builds a result the way the engine would stamp it: a tier, an outcome and
// whether the check is only a suggestion. The id is what the Findings tests
// read back, so each case can say which rows it expects.
func res(id string, tier Tier, status Status, optional bool) Result {
	return Result{ID: id, Title: id, Tier: tier, Status: status, Optional: optional}
}

func ids(rs []Result) []string {
	out := []string{}
	for _, x := range rs {
		out = append(out, x.ID)
	}
	return out
}

// summaryCases is shared by the Summary and Findings tests so the two are
// judged on the same reports: a problem that names the checks and a count that
// says how many must never be able to disagree.
var summaryCases = []struct {
	name   string
	report Report
	want   Summary
	// findings are the ids Findings must return, in order.
	findings []string
}{
	{
		name:   "a report with no results counts nothing",
		report: Report{Results: []Result{}},
		want:   Summary{},
	},
	{
		// The three tiers and the optional checks in one report. Only the safe,
		// non-optional rows are the host's health; everything else is a choice the
		// operator has not made, and a stock host carries those for ever.
		name: "only safe checks that are not optional count, and the rest are suggestions",
		report: Report{Results: []Result{
			res("inotify.watches", Safe, Warn, false),
			res("inotify.instances", Safe, OK, false),
			res("files.service", Safe, Skip, false),
			res("docker.logs", Safe, Error, false),
			res("disk.space", Safe, Warn, false),
			res("cpu.governor", Aggressive, Warn, false),
			res("memory.swappiness", Aggressive, OK, false),
			res("tmp.tmpfs", Aggressive, Warn, true),
			res("service.snapd", Dedicated, Warn, false),
			res("memory.swap-off", Dedicated, Warn, true),
			res("kernel.hwe-install", Dedicated, Skip, true),
			res("service.fwupd", Dedicated, Error, false),
			// Nothing in the engine is both safe and optional today; the rule is
			// still that optional wins, so a check added tomorrow is a suggestion.
			res("future.safe-optional", Safe, Warn, true),
		}},
		want:     Summary{Counted: 5, Warnings: 2, Errors: 1, Skipped: 1, Suggestions: 5},
		findings: []string{"docker.logs", "inotify.watches", "disk.space"},
	},
	{
		// An uncounted error or skip is in no number: it is still in the results
		// for anyone who opens the host, but it is not the host being unwell.
		name: "an error or skip outside the counted checks is in no number",
		report: Report{Results: []Result{
			res("service.fwupd", Dedicated, Error, false),
			res("kernel.hwe-install", Dedicated, Skip, true),
			res("cpu.governor", Aggressive, Skip, false),
			res("cgroup.version", Safe, OK, false),
		}},
		want: Summary{Counted: 1},
	},
	{
		// TestReportExitCodes builds results with no tier because Counts has no
		// use for one. The summary does, and a row that does not say which tier it
		// belongs to is not evidence that the host is unwell.
		name: "a result with no tier is not counted",
		report: Report{Results: []Result{
			{ID: "inotify.watches", Status: Warn},
			{ID: "docker.logs", Status: Error},
			{ID: "files.service", Status: Skip},
			{ID: "cgroup.version", Status: OK},
		}},
		want: Summary{Suggestions: 1},
	},
	{
		name: "every counted check passing counts them all and finds nothing",
		report: Report{Results: []Result{
			res("inotify.watches", Safe, OK, false),
			res("docker.logs", Safe, OK, false),
		}},
		want: Summary{Counted: 2},
	},
	{
		// Counted is what separates "everything passed" from "nothing could be
		// checked", which the host page words differently.
		name: "every counted check skipped is told apart from every counted check passing",
		report: Report{Results: []Result{
			res("inotify.watches", Safe, Skip, false),
			res("docker.logs", Safe, Skip, false),
		}},
		want: Summary{Counted: 2, Skipped: 2},
	},
	{
		// A reboot is one fact the engine records twice: as the kernel.pending
		// warning and as Report.RebootPending. It is counted once, as the reboot.
		name: "a host whose only finding is a pending reboot has no finding",
		report: Report{RebootPending: true, Results: []Result{
			res("kernel.running", Safe, OK, false),
			res(KernelPending, Safe, Warn, false),
			res("cgroup.version", Safe, OK, false),
		}},
		want: Summary{Counted: 2},
	},
	{
		name: "a pending reboot is left out and the other warnings still count",
		report: Report{RebootPending: true, Results: []Result{
			res("docker.storage", Safe, Warn, false),
			res(KernelPending, Safe, Warn, false),
			res("docker.logs", Safe, Error, false),
		}},
		want:     Summary{Counted: 2, Warnings: 1, Errors: 1},
		findings: []string{"docker.logs", "docker.storage"},
	},
	{
		// The exclusion belongs to the report's own claim. A row that warns with no
		// pending reboot reported beside it is judged as any other row.
		name: "the kernel.pending warning counts when the report does not say a reboot is pending",
		report: Report{RebootPending: false, Results: []Result{
			res(KernelPending, Safe, Warn, false),
			res("cgroup.version", Safe, OK, false),
		}},
		want:     Summary{Counted: 2, Warnings: 1},
		findings: []string{KernelPending},
	},
	{
		// Only the warning is the reboot. A kernel.pending row that passed or that
		// could not run is an ordinary row in a report that happens to contradict
		// itself, and is counted as one.
		name: "only a warning is taken for the reboot",
		report: Report{RebootPending: true, Results: []Result{
			res(KernelPending, Safe, Error, false),
			res("docker.logs", Safe, OK, false),
		}},
		want:     Summary{Counted: 2, Errors: 1},
		findings: []string{KernelPending},
	},
	{
		name: "a reboot says nothing about a result in another tier",
		report: Report{RebootPending: true, Results: []Result{
			res(KernelPending, Safe, Warn, false),
			res("cpu.governor", Aggressive, Warn, false),
		}},
		want: Summary{Suggestions: 1},
	},
}

func TestSummaryCountsOnlyWhatAHostIsJudgedOn(t *testing.T) {
	for _, tc := range summaryCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.report.Summary(); got != tc.want {
				t.Errorf("Summary() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A problem names the checks Findings returns and says how many with Summary. If
// the two were counted by separate rules, a host would raise "3 checks need
// attention" and then name two.
func TestFindingsAreExactlyWhatSummaryCountsAsWarningsAndErrors(t *testing.T) {
	for _, tc := range summaryCases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.report.Findings()
			s := tc.report.Summary()
			if len(got) != s.Warnings+s.Errors {
				t.Errorf("len(Findings()) = %d, but Summary() counts %d warnings and %d errors", len(got), s.Warnings, s.Errors)
			}
			if !slices.Equal(ids(got), append([]string{}, tc.findings...)) {
				t.Errorf("Findings() = %v, want %v", ids(got), tc.findings)
			}
		})
	}
}

// Errors come first because a check that could not run is what an operator
// should look at before a setting that is merely below the recommendation, and
// the engine's own order inside each rank keeps the text of a problem stable
// from one report to the next, which is what keeps problems.updated quiet.
func TestFindingsPutErrorsFirstAndKeepTheEnginesOrderWithinARank(t *testing.T) {
	r := Report{Results: []Result{
		res("w1", Safe, Warn, false),
		res("e1", Safe, Error, false),
		res("ok", Safe, OK, false),
		res("w2", Safe, Warn, false),
		res("skipped", Safe, Skip, false),
		res("e2", Safe, Error, false),
		res("suggestion", Aggressive, Warn, false),
		res("optional", Safe, Warn, true),
		res("w3", Safe, Warn, false),
	}}
	want := []string{"e1", "e2", "w1", "w2", "w3"}
	if got := ids(r.Findings()); !slices.Equal(got, want) {
		t.Errorf("Findings() = %v, want %v", got, want)
	}
	// The slice is the caller's own: a problem builder that appends to it must
	// not be writing into the report a host's view is rendered from.
	first := r.Findings()
	_ = append(first[:1], res("intruder", Safe, Warn, false))
	if got := ids(r.Findings()); !slices.Equal(got, want) {
		t.Errorf("Findings() after a caller appended = %v, want %v", got, want)
	}
	if r.Results[1].ID != "e1" {
		t.Errorf("Findings() wrote into the report's results: %v", ids(r.Results))
	}
}

func TestOnlyASafeCheckThatIsNotOptionalIsCounted(t *testing.T) {
	for _, tc := range []struct {
		tier     Tier
		optional bool
		want     bool
	}{
		{Safe, false, true},
		{Safe, true, false},
		{Aggressive, false, false},
		{Aggressive, true, false},
		{Dedicated, false, false},
		{Dedicated, true, false},
		{"", false, false},
		{"", true, false},
	} {
		if got := res("x", tc.tier, Warn, tc.optional).Counted(); got != tc.want {
			t.Errorf("Counted() for tier %q, optional %v = %v, want %v", tc.tier, tc.optional, got, tc.want)
		}
	}
}

// The summary is a contract with the UI, the CLI and the metrics, and a missing
// key reads as "unknown" where a zero reads as "none". A host with nothing wrong
// must still send all five numbers.
func TestSummaryAlwaysCarriesAllFiveNumbers(t *testing.T) {
	b, err := json.Marshal(Report{}.Summary())
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"counted":0,"warnings":0,"errors":0,"skipped":0,"suggestions":0}`; string(b) != want {
		t.Errorf("an empty summary marshals as %s, want %s", b, want)
	}
}

// The docs and the problem text say ten minutes in words. Moving the threshold
// is a decision about those, not a tidy-up of a constant.
func TestAReportIsStaleAfterTenMinutes(t *testing.T) {
	if ReportStaleAfter != 10*time.Minute {
		t.Errorf("ReportStaleAfter = %s; docs/host-health.md and docs/problem-codes.md say ten minutes, so change them with it", ReportStaleAfter)
	}
}

// mixedHost is a supported host with one of each outcome in its safe checks: two
// warnings, an error from a daemon.json that is not JSON, and the rest skipped
// because the fake host has nothing to read.
func mixedHost(osRelease string) *Engine {
	f := fake()
	f.put("/etc/os-release", osRelease)
	f.put("/sys/fs/cgroup/cgroup.controllers", "")
	f.put("/proc/sys/fs/inotify/max_user_watches", "1024")
	f.put("/proc/sys/vm/swappiness", "60")
	f.commands["docker info --format {{.Driver}}"] = "vfs"
	f.commands["docker info --format {{.LoggingDriver}}"] = "json-file"
	f.commands["docker info --format {{json .SecurityOptions}}"] = `["name=seccomp"]`
	f.put("/etc/docker/daemon.json", "{")
	return New(Options{System: f, OS: "linux", UID: 0, Now: func() time.Time { return time.Unix(1234, 0) }, WorkDir: "/work"})
}

// Counts is what zoomies doctor prints and exits on, and Summary is what every
// other surface shows. They are two functions only because doctor must count the
// tier it was asked to run. On the default run they have to agree, and they do
// today for one reason: no safe check is optional. This pins that reason. If a
// safe check is ever made optional the two numbers will part, and that wants a
// decision about what doctor prints -- not a surprise on a dashboard.
func TestDoctorsOwnCountsAgreeWithTheSummaryOnTheDefaultRun(t *testing.T) {
	for _, c := range append(append(baseChecks(), kernelChecks()...), dedicatedChecks()...) {
		if c.Tier == Safe && c.Optional {
			t.Errorf("%s is a safe check marked optional: Counts() and Summary() now disagree on the default doctor run, so decide which is right before adding it", c.ID)
		}
	}
	for _, tc := range []struct {
		name      string
		osRelease string
	}{
		{"a supported distribution", "ID=ubuntu\nVERSION_ID=24.04\n"},
		// The distribution warning is a safe, non-optional row the engine adds by
		// hand, so it has to be counted by both.
		{"an unsupported distribution", "ID=fedora\nVERSION_ID=42\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := mixedHost(tc.osRelease).Run(context.Background(), Safe)
			w, e, s := r.Counts()
			if w == 0 || e == 0 || s == 0 {
				t.Fatalf("the fixture must produce a warning, an error and a skip or this guard proves nothing: %d, %d, %d", w, e, s)
			}
			got := r.Summary()
			if got.Warnings != w || got.Errors != e || got.Skipped != s {
				t.Errorf("Counts() = %d warnings, %d errors, %d skipped; Summary() = %+v", w, e, s, got)
			}
			if got.Suggestions != 0 {
				t.Errorf("a safe run has no suggestions, got %d", got.Suggestions)
			}
		})
	}
}

// doctor --tier aggressive and --dedicated count what they ran, exit code
// included, so Counts must keep counting every tier. The summary, by contrast,
// must not let a stock host's aggressive warnings read as an unwell host.
func TestACountedTierIsDoctorsToCountAndASuggestionToTheSummary(t *testing.T) {
	r := mixedHost("ID=ubuntu\nVERSION_ID=24.04\n").Run(context.Background(), Aggressive)
	w, _, _ := r.Counts()
	got := r.Summary()
	if w <= got.Warnings {
		t.Fatalf("Counts() = %d warnings, Summary() = %d: the aggressive swappiness warning should be counted by one and not the other", w, got.Warnings)
	}
	if got.Suggestions == 0 {
		t.Errorf("the aggressive warning should be a suggestion: %+v", got)
	}
	if r.ExitCode() == 0 {
		t.Error("doctor must still exit non-zero on what it was asked to check")
	}
}

// rebootHost is a host that has a newer kernel installed than the one running
// and nothing else wrong, which is the common state of a patched but unrebooted
// machine.
func rebootHost() *Engine {
	e := mixedHost("ID=ubuntu\nVERSION_ID=24.04\n")
	f := e.System.(*fakeSystem)
	f.commands["uname -r"] = "6.9.0-1-generic"
	f.put("/lib/modules/6.10.0-2-generic/modules.dep", "")
	// Put right everything mixedHost got wrong, leaving only the reboot.
	f.put("/proc/sys/fs/inotify/max_user_watches", "524288")
	f.commands["docker info --format {{.Driver}}"] = "overlay2"
	f.put("/etc/docker/daemon.json", `{"log-opts":{"max-size":"10m","max-file":"3"}}`)
	return e
}

// This is the one place doctor and the summary are meant to differ on the
// default run. A reboot is a fact about the host rather than a setting to fix,
// so the summary counts it once, as the reboot, and a host that is only waiting
// for one is not flagged as unwell as well. doctor still lists the row.
func TestAPendingRebootIsCountedOnceByTheSummaryAndTwiceByDoctor(t *testing.T) {
	ctx := context.Background()

	r := rebootHost().Run(ctx, Safe)
	if !r.RebootPending {
		t.Fatalf("the fixture must have a reboot pending: %+v", r.Results)
	}
	if w, e, _ := r.Counts(); w != 1 || e != 0 {
		t.Fatalf("doctor counts %d warnings and %d errors, want just the kernel.pending warning", w, e)
	}
	got := r.Summary()
	if got.Warnings != 0 || got.Errors != 0 || len(r.Findings()) != 0 {
		t.Errorf("a host whose only finding is a reboot should have none: %+v %v", got, ids(r.Findings()))
	}
	if safe := len(r.Results); got.Counted != safe-1 {
		t.Errorf("Counted = %d, want every safe row but the reboot, %d", got.Counted, safe-1)
	}

	// Another warning beside it is still a finding, and the reboot is still not.
	e := rebootHost()
	e.System.(*fakeSystem).commands["docker info --format {{.Driver}}"] = "vfs"
	r = e.Run(ctx, Safe)
	got = r.Summary()
	if got.Warnings != 1 || !slices.Equal(ids(r.Findings()), []string{"docker.storage"}) {
		t.Errorf("want only the storage driver flagged, got %+v %v", got, ids(r.Findings()))
	}
	if w, _, _ := r.Counts(); w != got.Warnings+1 {
		t.Errorf("doctor counts %d warnings, want one more than the summary's %d", w, got.Warnings)
	}
}

// Run sets RebootPending from the constant, and the exclusion above is only
// right while the constant names the check that sets it.
func TestTheRebootFlagIsSetByTheCheckTheConstantNames(t *testing.T) {
	found := false
	for _, c := range kernelChecks() {
		if c.ID == KernelPending {
			found = true
		}
	}
	if !found {
		t.Fatalf("no kernel check has the id %q that Run reads the reboot flag from", KernelPending)
	}
}

// A problem names checks from this catalogue and never from the report, whose
// every field an agent wrote and which any signed-in viewer can read back. So
// every check the engine can emit must be named here, or a real finding would
// be reported as one the controller has never heard of.
func TestEveryCheckTheEngineCanEmitHasATitle(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		e    *Engine
	}{
		{"a supported host", mixedHost("ID=ubuntu\nVERSION_ID=24.04\n")},
		{"an unsupported distribution", mixedHost("ID=fedora\nVERSION_ID=42\n")},
		{"another operating system", New(Options{System: fake(), OS: "darwin", UID: 0, Now: time.Now})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, c := range tc.e.Checks {
				got, ok := Title(c.ID)
				if !ok || got != c.Title {
					t.Errorf("Title(%q) = %q, %v; the engine calls it %q", c.ID, got, ok, c.Title)
				}
			}
			for _, x := range tc.e.Run(ctx, Dedicated).Results {
				if _, ok := Title(x.ID); !ok {
					t.Errorf("the engine emitted %q, which has no title", x.ID)
				}
			}
		})
	}
	if got, ok := Title("environment"); !ok || got != "Distribution" {
		t.Errorf(`Title("environment") = %q, %v`, got, ok)
	}
}

func TestACheckTheControllerHasNotHeardOfHasNoTitle(t *testing.T) {
	for _, id := range []string{"", "made.up", "Kernel.Pending", " kernel.pending", "service."} {
		if got, ok := Title(id); ok || got != "" {
			t.Errorf("Title(%q) = %q, %v, want nothing", id, got, ok)
		}
	}
}
