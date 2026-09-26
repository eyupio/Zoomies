package controller

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// Every code the fleet's list can carry has a public sentence, and no code
// that only the platform is shown has one. The second half is the one that
// matters: a sentence for a platform code is a sentence somebody meant to
// show without an account, about a machine that is not the fleet's.
func TestEveryFleetCodeHasAPublicSentence(t *testing.T) {
	for code, audience := range problemAudience {
		_, has := publicSentences[code]
		switch {
		case audience.For(false) && !has:
			t.Errorf("%s reaches the fleet's list and has no public sentence", code)
		case !audience.For(false) && has:
			t.Errorf("%s is the platform's and has a public sentence; the status page can never show it", code)
		}
	}
	for code := range publicSentences {
		if _, ok := problemAudience[code]; !ok {
			t.Errorf("%s has a public sentence and no audience; it is not a code the controller raises", code)
		}
	}
}

// The projection reads the fleet half of the problems split and nothing
// else, so a platform finding -- however bad -- neither shows as a reason
// nor moves the state.
func TestTheStatusCannotCarryAPlatformProblem(t *testing.T) {
	st := ProjectStatus([]Problem{
		{Code: "backup.remote_failed", Severity: config.SeverityError, Title: "remote s3-backups-secret failed", Audience: AudiencePlatform},
		{Code: "controller.lease_lost", Severity: config.SeverityError, Audience: AudiencePlatform},
		{Code: "metrics.public", Severity: config.SeverityWarning, Audience: AudiencePlatform},
		// An unclassified code is withheld, which is audienceFor's default.
		{Code: "something.new", Severity: config.SeverityError},
	}, nil)
	if st.State != FleetHealthy || len(st.Reasons) != 0 || len(st.Explanations) != 0 {
		t.Fatalf("status = %+v, want healthy with no reasons", st)
	}
}

// The state is the worst severity among the fleet's own problems, and a code
// that several pools share is one reason, not one per pool -- a count of
// reasons would be a count of pools.
func TestTheStateIsTheWorstOfTheFleetsProblems(t *testing.T) {
	early := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	cases := []struct {
		name     string
		problems []Problem
		want     FleetState
		reasons  []string
	}{
		{"nothing", nil, FleetHealthy, nil},
		{"info only", []Problem{{Code: "host.limits_unverified", Severity: config.SeverityInfo}}, FleetHealthy, nil},
		{"a warning", []Problem{{Code: "host.throttled", Severity: config.SeverityWarning}}, FleetDegraded, []string{"host.throttled"}},
		{"an error beats a warning", []Problem{
			{Code: "host.throttled", Severity: config.SeverityWarning},
			{Code: "pool.no_capacity", Severity: config.SeverityError, Since: &late},
			{Code: "pool.no_capacity", Severity: config.SeverityWarning, Since: &early},
		}, FleetBlocked, []string{"pool.no_capacity", "host.throttled"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := ProjectStatus(tc.problems, nil)
			if st.State != tc.want {
				t.Fatalf("state = %s, want %s", st.State, tc.want)
			}
			var got []string
			for _, r := range st.Reasons {
				got = append(got, r.Code)
				if st.Explanations[r.Code] == "" {
					t.Errorf("%s has no explanation", r.Code)
				}
			}
			if !slices.Equal(got, tc.reasons) {
				t.Fatalf("reasons = %v, want %v", got, tc.reasons)
			}
			if tc.want == FleetBlocked {
				r := st.Reasons[0]
				if r.Severity != config.SeverityError || r.Since == nil || !r.Since.Equal(early) {
					t.Errorf("merged reason = %+v, want the worst severity and the earliest since", r)
				}
			}
		})
	}
}

// Counts are bands, and a queue longer than every slot the fleet has is the
// one band a developer most needs: it means waiting is not going to be short.
func TestCountsAreBandsAndWaitsAreWholeMinutes(t *testing.T) {
	cases := []struct {
		queued, capacity, running int
		wantQueued, wantRunning   Band
	}{
		{0, 8, 0, BandNone, BandNone},
		{3, 8, 5, BandFew, BandFew},
		{7, 8, 6, BandMany, BandMany},
		{9, 8, 40, BandBackedUp, BandMany},
		// A handful is a handful even on a fleet with one slot.
		{4, 1, 1, BandFew, BandFew},
	}
	for _, tc := range cases {
		stats := &Stats{Hosts: HostStats{Capacity: tc.capacity}}
		stats.Fleet.QueuedJobs, stats.Fleet.RunningJobs = tc.queued, tc.running
		stats.Fleet.MedianWaitMS, stats.Fleet.P95WaitMS = 89_000, 29_000
		st := ProjectStatus(nil, stats)
		if st.Queued != tc.wantQueued || st.Running != tc.wantRunning {
			t.Errorf("queued %d of %d, running %d: got %s/%s, want %s/%s",
				tc.queued, tc.capacity, tc.running, st.Queued, st.Running, tc.wantQueued, tc.wantRunning)
		}
		if st.MedianWaitMinutes != 1 || st.P95WaitMinutes != 0 {
			t.Errorf("waits = %d/%d minutes, want 1/0", st.MedianWaitMinutes, st.P95WaitMinutes)
		}
	}
}

// The fixture names every test below searches for. Distinctive on purpose: a
// name that is also an ordinary word would pass the search by accident the
// day a sentence used the word.
const (
	fixtureTarget = "quokkaorg"
	fixturePool   = "narwhalpool"
	fixtureHost   = "axolotlhost"
	fixtureRepo   = fixtureTarget + "/pangolinrepo"
	fixtureJob    = "capybarajob"
	fixtureFlow   = "okapiworkflow"
)

// statusFixture is one of the four reasons a job waits, built for real on a
// fleet whose every name is distinctive.
type statusFixture struct {
	name string
	code string
	make func(t *testing.T, h *harness)
}

func (h *harness) namedFleet() (*store.Installation, *store.Pool) {
	inst := h.installationOn(fixtureTarget, store.TargetOrg)
	return inst, h.pool(inst, fixturePool)
}

func (h *harness) queueNamedJob(id int64, labels []string, queued time.Time) {
	h.deliverJob(jobEvent{
		Action: "queued", JobID: id, RunID: id, Repo: fixtureRepo,
		Name: fixtureJob, Workflow: fixtureFlow, Labels: labels, QueuedAt: queued,
	})
}

var statusFixtures = []statusFixture{
	{"capacity", "pool.no_capacity", func(t *testing.T, h *harness) {
		_, p := h.namedFleet()
		h.queueNamedJob(1, p.Labels, time.Now().Add(-time.Minute))
		if err := h.c.Reconcile(h.ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}},
	{"labels", "jobs.unmatched", func(t *testing.T, h *harness) {
		h.namedFleet()
		h.host(fixtureHost)
		h.queueNamedJob(2, []string{"self-hosted", "gpu"}, time.Now().Add(-unmatchedGrace-time.Minute))
	}},
	{"permission", "installation.unhealthy", func(t *testing.T, h *harness) {
		inst, p := h.namedFleet()
		h.host(fixtureHost)
		h.queueNamedJob(3, p.Labels, time.Now().Add(-time.Minute))
		if err := h.st.SetInstallationHealth(h.ctx, inst.ID, "the App lost access to "+fixtureRepo); err != nil {
			t.Fatalf("SetInstallationHealth: %v", err)
		}
	}},
	{"webhook secret", "webhook.rejected", func(t *testing.T, h *harness) {
		_, p := h.namedFleet()
		h.host(fixtureHost)
		h.deliver("workflow_job", jobEvent{Action: "queued", JobID: 4, Repo: fixtureRepo, Name: fixtureJob, Labels: p.Labels}.body(), "not-the-secret")
	}},
	// Not a reason a job waits: a fleet doing its job, so that runners -- and
	// the names they are given -- are in the fixture too.
	{"busy", "", func(t *testing.T, h *harness) {
		_, p := h.namedFleet()
		h.host(fixtureHost)
		h.queueNamedJob(5, p.Labels, time.Now().Add(-time.Minute))
		if err := h.c.Reconcile(h.ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(h.runners()) == 0 {
			t.Fatal("no runner was created, so the fixture has no runner names to search for")
		}
	}},
}

// fixtureNames is every name the fixture's rows carry, read back from the
// store rather than listed, so a name the controller makes up -- a runner's
// -- is searched for too.
func fixtureNames(t *testing.T, h *harness) []string {
	t.Helper()
	names := []string{fixtureTarget, fixturePool, fixtureHost, fixtureRepo, "pangolinrepo", fixtureJob, fixtureFlow}
	pools, _ := h.st.ListPools(h.ctx)
	for _, p := range pools {
		names = append(names, p.Name, p.ID)
	}
	hosts, _ := h.st.ListHosts(h.ctx)
	for _, x := range hosts {
		names = append(names, x.Name, x.ID)
	}
	insts, _ := h.st.ListInstallations(h.ctx)
	for _, i := range insts {
		names = append(names, i.Target, i.ID)
	}
	for _, r := range h.runners() {
		names = append(names, r.Name, r.ID)
	}
	jobs, _, _ := h.st.ListJobs(h.ctx, store.JobFilter{}, store.Page{Limit: 100})
	for _, j := range jobs {
		names = append(names, j.Repo, j.JobName, j.Workflow, j.ID)
	}
	out := names[:0]
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// The acceptance test for the disclosure boundary: a fleet whose every pool,
// host, repository, runner and job is named something distinctive produces a
// status body containing none of those names, and each of the four reasons a
// job waits -- capacity, labels, a lost permission, a webhook secret that
// stopped verifying -- produces a state a reader can tell from the other
// three.
func TestTheStatusNamesNothingAndTellsTheFourWaitsApart(t *testing.T) {
	causes := map[string]bool{}
	for _, f := range statusFixtures {
		if f.code != "" {
			causes[f.code] = true
		}
	}
	for _, f := range statusFixtures {
		t.Run(f.name, func(t *testing.T) {
			h := newHarness(t)
			f.make(t, h)
			st, err := h.c.Status(h.ctx)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			body, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			names := fixtureNames(t, h)
			for _, n := range names {
				if strings.Contains(string(body), n) {
					t.Errorf("the status body contains %q:\n%s", n, body)
				}
			}
			if len(names) < 8 {
				t.Fatalf("only %d names were collected, so the search proves little: %v", len(names), names)
			}

			var got []string
			for _, r := range st.Reasons {
				got = append(got, r.Code)
			}
			if f.code != "" && !slices.Contains(got, f.code) {
				t.Fatalf("reasons = %v, want %s", got, f.code)
			}
			// Distinguishable: this cause's code is present and no other
			// cause's is, so the page says a different thing for each.
			for code := range causes {
				if code != f.code && slices.Contains(got, code) {
					t.Errorf("reasons = %v carry %s as well as %s; the four waits must read differently", got, code, f.code)
				}
			}
			if f.code != "" && st.State == FleetHealthy {
				t.Errorf("a fleet whose jobs wait on %s reads as healthy", f.name)
			}
		})
	}
}

// Since is when the state last changed, not when somebody asked: a page that
// refreshes every thirty seconds would otherwise always say "just now".
func TestSinceHoldsUntilTheStateChanges(t *testing.T) {
	h := newHarness(t)
	first, err := h.c.Status(h.ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	h.advance(10 * time.Minute)
	second, _ := h.c.Status(h.ctx)
	if !second.Since.Equal(first.Since) {
		t.Fatalf("since moved from %s to %s with the state unchanged", first.Since, second.Since)
	}
	inst := h.installation()
	if err := h.st.SetInstallationHealth(h.ctx, inst.ID, "gone"); err != nil {
		t.Fatal(err)
	}
	third, _ := h.c.Status(h.ctx)
	if third.State == second.State || !third.Since.After(second.Since) {
		t.Fatalf("state %s since %s, want a new state with a later since", third.State, third.Since)
	}
}
