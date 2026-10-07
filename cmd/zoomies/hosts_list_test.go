package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eyupio/zoomies/internal/hosttune"
)

// A throttled host has fewer slots than its operator configured, and the
// list has to show the slots it is actually taking: 2/8 on a host throttled
// to four reads as six slots free, and the scheduler will refuse all six.
func TestHostsListShowsAThrottledHostsEffectiveSlots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
		  {"id":"hst_a","name":"calm","capacity":4,"active_runners":1,"free":3,"effective_capacity":4,
		   "backends":["docker"],"healthy":true,"cpus":8,"memory_mb":16384,"last_heartbeat":"2026-01-01T00:00:00Z"},
		  {"id":"hst_b","name":"pressed","capacity":4,"active_runners":1,"free":1,"effective_capacity":2,
		   "throttle_reason":"throttled to 2 of 4 slots (step 2 of 3) after sustained pressure: the 1-minute load average is 30.0, at least twice the host's 8 CPUs; running jobs continue, and the throttle lifts one step after 5m of calm",
		   "backends":["docker"],"healthy":true,"cpus":8,"memory_mb":16384,"last_heartbeat":"2026-01-01T00:00:00Z"},
		  {"id":"hst_c","name":"old-controller","capacity":4,"active_runners":2,"free":2,
		   "backends":["docker"],"healthy":true,"cpus":8,"memory_mb":16384,"last_heartbeat":"2026-01-01T00:00:00Z"}
		],"total":3}`))
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "list", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String()
	for _, want := range []string{
		"1/4", "1/2 (of 4)", "throttled",
		"pressed is throttled to 2 of 4 slots",
		// A controller older than effective_capacity sends none; that is
		// not a host with no slots.
		"2/4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "2/0") || strings.Contains(got, "1/4 (of") {
		t.Errorf("a host with no throttle was shown as throttled:\n%s", got)
	}
}

// A host with a standard runner size takes what its machine holds of it, which
// is not the capacity beside it, and the list says both what the size is and
// what limits the slots.
func TestHostsListShowsTheSlotsAProfileGives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
		  {"id":"hst_a","name":"big","capacity":8,"slots":3,"slots_limited_by":"cpu","active_runners":1,"free":2,"effective_capacity":3,
		   "runner_profile":{"standard":{"cpus":3,"memory_mb":8192}},
		   "effective_profile":{"standard":{"cpus":3,"memory_mb":8192,"cpus_source":"host","memory_mb_source":"host"}},
		   "backends":["docker"],"healthy":true,"cpus":12,"memory_mb":32768,"last_heartbeat":"2026-01-01T00:00:00Z"},
		  {"id":"hst_b","name":"capped","capacity":2,"slots":2,"slots_limited_by":"capacity","active_runners":0,"free":2,"effective_capacity":2,
		   "runner_profile":{"standard":{"cpus":3}},
		   "effective_profile":{"standard":{"cpus":3,"memory_mb":4096,"cpus_source":"host","memory_mb_source":"global"}},
		   "backends":["docker"],"healthy":true,"cpus":12,"memory_mb":32768,"last_heartbeat":"2026-01-01T00:00:00Z"},
		  {"id":"hst_c","name":"plain","capacity":4,"active_runners":2,"free":2,"effective_capacity":4,
		   "backends":["docker"],"healthy":true,"cpus":8,"memory_mb":16384,"last_heartbeat":"2026-01-01T00:00:00Z"}
		],"total":3}`))
	}))
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "list", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String() + errOut.String()
	for _, want := range []string{
		"1/3",
		"big: runners of 3 CPU and 8 GB (from the host's profile), 3 slots",
		"big: its CPUs set the slots",
		"capped: runners of 3 CPU and 4 GB (CPU from the host's profile, memory from the fleet's default), 2 slots",
		"capped: its capacity of 2 is what limits the slots",
		// An unprofiled host reads exactly as it always did.
		"2/4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "plain: runners of") {
		t.Errorf("an unprofiled host was described as sized by a profile:\n%s", got)
	}
}

// aReport is a connected host whose last heartbeat is fixedNow and whose report
// was taken a minute before it, which is what a healthy collector looks like
// from the controller's side. Staleness is measured against the heartbeat and
// never against the clock of the machine running the CLI, so a static fixture
// reads the same whenever the test runs.
func aReport(d hostDoctor) hostItem {
	return hostItem{Name: "build-04", ID: "host_k3f9qz2mx7ab", Healthy: true, LastHeartbeat: fixedNow, Doctor: &d}
}

// aMinuteOld is a fresh report carrying this count.
func aMinuteOld(s hostDoctorSummary) hostDoctor {
	return hostDoctor{CheckedAt: fixedNow.Add(-time.Minute), Summary: &s}
}

// olderThanTheHeartbeatBy is a report taken this long before the host's last
// heartbeat, which is negative for one the host dated in the future.
func olderThanTheHeartbeatBy(age time.Duration, s hostDoctorSummary) hostItem {
	return aReport(hostDoctor{CheckedAt: fixedNow.Add(-age), Summary: &s})
}

// The os health column is where an operator sees at a glance which hosts need
// attention, and it has to agree with the controller's own count rather than
// make up a verdict. The first line that matches wins, so each state is pinned
// on its own and the pairs that could be confused are pinned in the order they
// must be read in.
func TestHostOSHealthReadsTheControllersCountOfTheReport(t *testing.T) {
	unreachable := aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Warnings: 2}))
	unreachable.Healthy = false
	partial := aMinuteOld(hostDoctorSummary{Counted: 12, Skipped: 9})
	partial.Container = true
	staleAndPartial := olderThanTheHeartbeatBy(time.Hour, hostDoctorSummary{Counted: 12})
	staleAndPartial.Doctor.Container = true
	reboot := func(s hostDoctorSummary) hostItem {
		d := aMinuteOld(s)
		d.RebootPending = true
		return aReport(d)
	}
	undated := aReport(aMinuteOld(hostDoctorSummary{Counted: 12}))
	undated.Doctor.CheckedAt = time.Time{}

	cases := []struct {
		name string
		host hostItem
		want string
	}{
		// Nothing to say.
		{"a host that has never reported", hostItem{Name: "new", Healthy: true, LastHeartbeat: fixedNow}, "-"},
		{"a controller older than the summary", aReport(hostDoctor{CheckedAt: fixedNow}), "-"},
		{"an unreachable host, whose last report describes a machine nobody can see", unreachable, "-"},

		// A report that is not the host's.
		{"the container's partial report", aReport(partial), "partial"},
		{"a partial report is not judged on its age", staleAndPartial, "partial"},

		// A report that has stopped coming.
		{"a report older than ten minutes by the host's last heartbeat", olderThanTheHeartbeatBy(14*time.Minute, hostDoctorSummary{Counted: 12}), "stale 14m"},
		{"a report with no date at all", undated, "stale"},
		{"exactly ten minutes is not yet stale", olderThanTheHeartbeatBy(hosttune.ReportStaleAfter, hostDoctorSummary{Counted: 12}), "ok"},
		{"a second past ten minutes is", olderThanTheHeartbeatBy(hosttune.ReportStaleAfter+time.Second, hostDoctorSummary{Counted: 12}), "stale 10m"},
		{"a report from a host clock running ahead is not old", olderThanTheHeartbeatBy(-time.Minute, hostDoctorSummary{Counted: 12}), "ok"},
		{"stale outranks what the old report found", olderThanTheHeartbeatBy(20*time.Minute, hostDoctorSummary{Counted: 12, Errors: 1}), "stale 20m"},

		// What it found.
		{"one error", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Errors: 1})), "1 error"},
		{"one warning", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Warnings: 1})), "1 warning"},
		{"several warnings", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Warnings: 2})), "2 warnings"},
		{"errors then warnings", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Errors: 1, Warnings: 2})), "1 error, 2 warnings"},
		{"several errors and warnings with a reboot", reboot(hostDoctorSummary{Counted: 12, Errors: 2, Warnings: 3}), "2 errors, 3 warnings, reboot pending"},
		{"warnings with a reboot", reboot(hostDoctorSummary{Counted: 12, Warnings: 1}), "1 warning, reboot pending"},
		// The controller counts a pending reboot once: its own kernel check is
		// left out of the warnings, so a host whose only finding is a reboot is
		// not also a host with a warning.
		{"a reboot and nothing else", reboot(hostDoctorSummary{Counted: 11}), "reboot pending"},
		{"a reboot outranks every other check being skipped", reboot(hostDoctorSummary{Counted: 3, Skipped: 3}), "reboot pending"},

		// What it could not find out.
		{"every counted check skipped", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Skipped: 12})), "unavailable"},
		{"no counted check at all", aReport(aMinuteOld(hostDoctorSummary{})), "unavailable"},
		{"some checks skipped and the rest fine", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Skipped: 2})), "ok"},

		{"every counted check passing", aReport(aMinuteOld(hostDoctorSummary{Counted: 12})), "ok"},

		// Silence has to show: an accepted warning is in no other number.
		{"a host whose only warning was accepted", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Accepted: 1})), "ok, 1 accepted"},
		{"accepted beside a warning", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Warnings: 1, Accepted: 2})), "1 warning, 2 accepted"},
	}
	for _, tc := range cases {
		p, _ := testPrinter(outputTable)
		if got := hostOSHealth(p, tc.host); got != tc.want {
			t.Errorf("%s: os health = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Colour is how the eye finds the host that needs attention in a long list, so
// each state has the one it means: red for an error, yellow for a warning or a
// report that has stopped, green for ok, dim for what says nothing about the host.
// A pending reboot is unpainted, because the problem it raises is only a note.
func TestHostOSHealthPaintsEachStateInTheColourItMeans(t *testing.T) {
	rebootWith := func(s hostDoctorSummary) hostItem {
		d := aMinuteOld(s)
		d.RebootPending = true
		return aReport(d)
	}
	partial := aMinuteOld(hostDoctorSummary{Counted: 12})
	partial.Container = true

	cases := []struct {
		name string
		host hostItem
		want string
	}{
		{"errors and warnings each in their own colour", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Errors: 1, Warnings: 2})),
			colourise(colourRed, "1 error") + ", " + colourise(colourYellow, "2 warnings")},
		{"a reboot is not painted", rebootWith(hostDoctorSummary{Counted: 12, Warnings: 1}),
			colourise(colourYellow, "1 warning") + ", reboot pending"},
		{"stale is yellow", olderThanTheHeartbeatBy(14*time.Minute, hostDoctorSummary{Counted: 12}), colourise(colourYellow, "stale 14m")},
		{"partial is dim", aReport(partial), colourise(colourDim, "partial")},
		{"unavailable is dim", aReport(aMinuteOld(hostDoctorSummary{Counted: 12, Skipped: 12})), colourise(colourDim, "unavailable")},
		{"ok is green", aReport(aMinuteOld(hostDoctorSummary{Counted: 12})), colourise(colourGreen, "ok")},
		{"nothing to say is a plain dash", hostItem{Healthy: true}, "-"},
	}
	for _, tc := range cases {
		p, _ := testPrinter(outputTable)
		p.colour = true
		if got := hostOSHealth(p, tc.host); got != tc.want {
			t.Errorf("%s: os health = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// osHealthFleet is what a controller sends for a fleet in every state the
// column has a word for. last_heartbeat is long before today on purpose: a CLI
// that measured staleness against its own clock would read every one of these
// reports as ancient.
const osHealthFleet = `{"items":[
  {"id":"hst_ok","name":"fine","capacity":4,"active_runners":1,"free":3,"effective_capacity":4,"backends":["docker"],"healthy":true,
   "last_heartbeat":"2026-01-01T00:00:00Z",
   "doctor":{"checked_at":"2025-12-31T23:59:00Z","container":false,"reboot_pending":false,"os":"linux","distro":"ubuntu","results":[],
             "summary":{"counted":12,"warnings":0,"errors":0,"skipped":0,"suggestions":9}}},
  {"id":"hst_warn","name":"watched","capacity":4,"active_runners":1,"free":3,"effective_capacity":4,"backends":["docker"],"healthy":true,
   "last_heartbeat":"2026-01-01T00:00:00Z",
   "doctor":{"checked_at":"2025-12-31T23:59:00Z","container":false,"reboot_pending":false,"results":[],
             "summary":{"counted":12,"warnings":2,"errors":1,"skipped":0,"suggestions":0}}},
  {"id":"hst_boot","name":"rebooting","capacity":4,"active_runners":0,"free":4,"effective_capacity":4,"backends":["docker"],"healthy":true,
   "last_heartbeat":"2026-01-01T00:00:00Z",
   "doctor":{"checked_at":"2025-12-31T23:59:00Z","container":false,"reboot_pending":true,"results":[],
             "summary":{"counted":11,"warnings":0,"errors":0,"skipped":0,"suggestions":0}}},
  {"id":"hst_part","name":"boxed","capacity":4,"active_runners":0,"free":4,"effective_capacity":4,"backends":["docker"],"healthy":true,
   "last_heartbeat":"2026-01-01T00:00:00Z",
   "doctor":{"checked_at":"2025-12-31T23:59:00Z","container":true,"reboot_pending":false,"results":[],
             "summary":{"counted":12,"warnings":1,"errors":0,"skipped":9,"suggestions":0}}},
  {"id":"hst_old","name":"silent","capacity":4,"active_runners":0,"free":4,"effective_capacity":4,"backends":["docker"],"healthy":true,
   "last_heartbeat":"2026-01-01T00:00:00Z",
   "doctor":{"checked_at":"2025-12-31T23:40:00Z","container":false,"reboot_pending":false,"results":[],
             "summary":{"counted":12,"warnings":0,"errors":0,"skipped":0,"suggestions":0}}},
  {"id":"hst_down","name":"offline","capacity":4,"active_runners":0,"free":4,"effective_capacity":4,"backends":["docker"],"healthy":false,
   "last_heartbeat":"2025-12-31T00:00:00Z",
   "doctor":{"checked_at":"2025-12-30T23:59:00Z","container":false,"reboot_pending":false,"results":[],
             "summary":{"counted":12,"warnings":3,"errors":0,"skipped":0,"suggestions":0}}},
  {"id":"hst_new","name":"unreported","capacity":4,"active_runners":0,"free":4,"effective_capacity":4,"backends":["docker"],"healthy":true,
   "last_heartbeat":"2026-01-01T00:00:00Z"},
  {"id":"hst_old_controller","name":"pre-summary","capacity":4,"active_runners":0,"free":4,"effective_capacity":4,"backends":["docker"],"healthy":true,
   "last_heartbeat":"2026-01-01T00:00:00Z",
   "doctor":{"checked_at":"2025-12-31T23:59:00Z","container":false,"reboot_pending":false,"results":[]}}
],"total":8}`

// hostsListCells is a listing's row for one host, split on the two or more
// spaces that separate columns, so a cell with a single space in it ("1 error,
// 2 warnings") stays whole.
func hostsListCells(t *testing.T, out, name string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, name+" ") {
			return regexp.MustCompile(` {2,}`).Split(line, -1)
		}
	}
	t.Fatalf("no row for %s in:\n%s", name, out)
	return nil
}

// The column sits between the state and the runners, so an operator reads "is it
// up, is its system well, is it busy" left to right, and every state the
// controller can send reads as the word the table above pins.
func TestHostsListPutsOSHealthBetweenStateAndRunners(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/hosts": osHealthFleet})
	out, _ := runCLI(t, "hosts", "list", "--url", srv.URL)

	header := strings.Fields(strings.SplitN(out, "\n", 2)[0])
	if got, want := strings.Join(header[:5], " "), "NAME ID STATE OS HEALTH"; got != want {
		t.Errorf("the table starts %q, want %q", got, want)
	}
	if got, want := strings.Join(header[5:], " "), "RUNNERS USED BACKENDS PLATFORM SIZE LAST SEEN"; got != want {
		t.Errorf("the table ends %q, want %q", got, want)
	}

	for _, tc := range []struct{ name, state, osHealth, runners string }{
		{"fine", "healthy", "ok", "1/4"},
		{"watched", "healthy", "1 error, 2 warnings", "1/4"},
		{"rebooting", "healthy", "reboot pending", "0/4"},
		{"boxed", "healthy", "partial", "0/4"},
		{"silent", "healthy", "stale 20m", "0/4"},
		{"offline", "unreachable", "-", "0/4"},
		{"unreported", "healthy", "-", "0/4"},
		{"pre-summary", "healthy", "-", "0/4"},
	} {
		cells := hostsListCells(t, out, tc.name)
		if cells[2] != tc.state || cells[3] != tc.osHealth || cells[4] != tc.runners {
			t.Errorf("%s: state, os health, runners = %q, %q, %q; want %q, %q, %q",
				tc.name, cells[2], cells[3], cells[4], tc.state, tc.osHealth, tc.runners)
		}
	}
}

// The structured outputs are the server's own bytes, not a re-marshalling of a
// struct this CLI knows, so the column cannot change what a script reads: the
// report is there whole, the controller's count is there beside it, and a
// controller that sends no doctor does not have one invented for it.
func TestHostsListStructuredOutputIsTheServersOwnWithItsSummary(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/hosts": osHealthFleet})
	var sent any
	if err := json.Unmarshal([]byte(osHealthFleet), &sent); err != nil {
		t.Fatal(err)
	}

	t.Run("json", func(t *testing.T) {
		out, _ := runCLI(t, "hosts", "list", "--url", srv.URL, "--output", "json")
		var got any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("--output json did not produce JSON: %v\n%s", err, out)
		}
		if !reflect.DeepEqual(got, sent) {
			t.Errorf("--output json is not what the controller sent:\n%s", out)
		}
		if !strings.Contains(out, `"summary"`) || !strings.Contains(out, `"suggestions": 9`) {
			t.Errorf("--output json lost doctor.summary:\n%s", out)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		out, _ := runCLI(t, "hosts", "list", "--url", srv.URL, "--output", "yaml")
		var viaYAML any
		if err := yaml.Unmarshal([]byte(out), &viaYAML); err != nil {
			t.Fatalf("--output yaml did not produce YAML: %v\n%s", err, out)
		}
		// YAML reads an integer as an int where JSON reads a float, so both sides
		// go through JSON before they are compared.
		round, err := json.Marshal(viaYAML)
		if err != nil {
			t.Fatal(err)
		}
		var got any
		if err := json.Unmarshal(round, &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, sent) {
			t.Errorf("--output yaml is not what the controller sent:\n%s", out)
		}
		if !strings.Contains(out, "summary:") || !strings.Contains(out, "suggestions: 9") {
			t.Errorf("--output yaml lost doctor.summary:\n%s", out)
		}
	})

	t.Run("a controller older than the field", func(t *testing.T) {
		old := jsonRoutes(t, map[string]string{"/api/v1/hosts": `{"items":[{"id":"hst_a","name":"calm","capacity":4,"healthy":true,
			"last_heartbeat":"2026-01-01T00:00:00Z"}],"total":1}`})
		out, _ := runCLI(t, "hosts", "list", "--url", old.URL, "--output", "json")
		if strings.Contains(out, "doctor") {
			t.Errorf("a host the controller sent no doctor for was given one:\n%s", out)
		}
		table, _ := runCLI(t, "hosts", "list", "--url", old.URL)
		if cells := hostsListCells(t, table, "calm"); cells[3] != "-" {
			t.Errorf("a controller older than the field read %q, want a dash:\n%s", cells[3], table)
		}
	})
}

// A host's report is written by the host, and the controller carries every word
// of it to the CLI in the very response the list is read from. The os health
// cell is built from integers and words this binary chose, so none of it can
// reach the terminal: a newline in a check's title that forged a row, or an
// escape sequence that rewrote the window title, would be a hostile host
// talking to an operator in the middle of an incident.
func TestHostsListPrintsHostileReportTextWithoutControlCharacters(t *testing.T) {
	srv := jsonRoutes(t, map[string]string{"/api/v1/hosts": `{"items":[
	  {"id":"hst_a","name":"calm","capacity":4,"active_runners":1,"free":3,"effective_capacity":4,"backends":["docker"],"healthy":true,
	   "last_heartbeat":"2026-01-01T00:00:00Z",
	   "doctor":{"checked_at":"2025-12-31T23:59:00Z","container":false,"reboot_pending":true,
	     "os":"` + hostileJSON + `","distro":"` + hostileJSON + `","work_dir":"` + hostileJSON + `",
	     "results":[{"id":"` + shortHostileJSON + `","title":"` + hostileJSON + `","status":"warn","tier":"safe",
	       "current":"` + hostileJSON + `","recommended":"` + hostileJSON + `","detail":"` + hostileJSON + `"}],
	     "summary":{"counted":12,"warnings":1,"errors":1,"skipped":0,"suggestions":0}}}
	],"total":1}`})
	out, _ := runCLI(t, "hosts", "list", "--url", srv.URL)
	// A header and one row, and nothing the report said.
	assertNoTerminalControl(t, "hosts list", out, 2)
	if !strings.Contains(out, "1 error, 1 warning, reboot pending") {
		t.Errorf("the os health cell is not what the count says:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "acme/widgets") {
			t.Errorf("a newline in the report started a forged line: %q", line)
		}
	}
}

// The group's help is the first place an operator learns the list now says
// something about the host's operating system.
func TestHostsHelpSaysTheListCarriesOSHealth(t *testing.T) {
	out, _ := runCLI(t, "hosts", "help")
	if !strings.Contains(out, "Every host, with its state, OS health and free capacity") {
		t.Errorf("the group's help does not describe the list's OS health:\n%s", out)
	}
}
