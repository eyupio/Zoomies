package store

import (
	"context"
	"database/sql"
	"testing"
)

func sampleProfile() RunnerProfile {
	return RunnerProfile{
		Minimum:  RunnerSize{CPUs: 1, MemoryMB: 2048},
		Standard: RunnerStandard{CPUs: 3, MemoryMB: 8192, BurstMaxCPUs: 6},
	}
}

// The profile is read and written whole, so a figure lost in the JSON would be
// a host sized differently after a restart with nothing in the log to say why.
func TestAHostRemembersItsRunnerProfile(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _, host := seedPool(t, s)

	got, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunnerProfile.Set() {
		t.Fatalf("a host nobody has profiled has a profile: %+v", got.RunnerProfile)
	}

	want := sampleProfile()
	if err := s.PatchHost(ctx, host.ID, HostChanges{RunnerProfile: &want}); err != nil {
		t.Fatalf("PatchHost: %v", err)
	}
	got, _ = s.GetHost(ctx, host.ID)
	if got.RunnerProfile != want {
		t.Fatalf("the profile did not survive a round trip:\n got  %+v\n want %+v", got.RunnerProfile, want)
	}
	if hosts, _ := s.ListHosts(ctx); len(hosts) != 1 || hosts[0].RunnerProfile != want {
		t.Fatalf("ListHosts reads the profile differently from GetHost: %+v", hosts)
	}
}

// An empty profile is how an operator clears one, so it has to be a write and
// not "leave it alone"; and every other edit has to leave the profile where it
// was, or saving a capacity would silently resize the host's runners.
func TestPatchingAHostChangesTheRunnerProfileOnlyWhenAskedTo(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _, host := seedPool(t, s)
	profile := sampleProfile()
	if err := s.PatchHost(ctx, host.ID, HostChanges{RunnerProfile: &profile}); err != nil {
		t.Fatal(err)
	}

	capacity := 9
	labels := StringMap{"rack": "b"}
	if err := s.PatchHost(ctx, host.ID, HostChanges{Capacity: &capacity, Labels: &labels}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetHost(ctx, host.ID)
	if got.RunnerProfile != profile {
		t.Fatalf("a capacity and labels edit changed the profile: %+v", got.RunnerProfile)
	}
	if got.Capacity != 9 || got.Labels["rack"] != "b" {
		t.Fatalf("the edit itself was lost: capacity %d, labels %v", got.Capacity, got.Labels)
	}

	narrower := RunnerProfile{Standard: RunnerStandard{CPUs: 2}}
	if err := s.PatchHost(ctx, host.ID, HostChanges{RunnerProfile: &narrower}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetHost(ctx, host.ID)
	if got.RunnerProfile != narrower {
		t.Fatalf("a profile edit merged with the old one instead of replacing it: %+v", got.RunnerProfile)
	}
	if got.Capacity != 9 {
		t.Fatalf("a profile edit changed the capacity to %d", got.Capacity)
	}

	if err := s.PatchHost(ctx, host.ID, HostChanges{RunnerProfile: &RunnerProfile{}}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetHost(ctx, host.ID)
	if got.RunnerProfile.Set() {
		t.Fatalf("an empty profile did not clear the profile: %+v", got.RunnerProfile)
	}
}

// A heartbeat carries a copy of the host read at the top of the request. If
// either of the writes it reaches wrote the profile, an operator who saved one
// in that interval would have the old value put back with nothing to say so.
func TestAnAgentsWritesNeverReachTheRunnerProfile(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _, host := seedPool(t, s)

	stale, _ := s.GetHost(ctx, host.ID)
	profile := sampleProfile()
	if err := s.PatchHost(ctx, host.ID, HostChanges{RunnerProfile: &profile}); err != nil {
		t.Fatal(err)
	}

	stale.CPUs, stale.MemoryMB = 12, 32768
	if err := s.UpdateHost(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostReported(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetHost(ctx, host.ID)
	if got.RunnerProfile != profile {
		t.Fatalf("a heartbeat's write replaced the operator's profile with %+v", got.RunnerProfile)
	}
	if got.CPUs != 12 {
		t.Fatalf("the heartbeat's own facts were not written: %d CPUs", got.CPUs)
	}
}

// A host created already carrying a profile, as a re-registration that keeps
// the operator's answer would, has to store it with the row.
func TestAHostCanBeCreatedWithARunnerProfile(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	host := &Host{Name: "big", Capacity: 8, RunnerProfile: sampleProfile()}
	if err := s.CreateHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunnerProfile != sampleProfile() {
		t.Fatalf("CreateHost dropped the profile: %+v", got.RunnerProfile)
	}
}

func TestAnEmptyRunnerProfileIsStoredAsNothing(t *testing.T) {
	v, err := RunnerProfile{}.Value()
	if err != nil {
		t.Fatal(err)
	}
	if v != "{}" {
		t.Fatalf("an empty profile is stored as %v, want {} so that unprofiled hosts stay recognisable", v)
	}
	var p RunnerProfile
	for _, raw := range []any{nil, "", "{}", []byte("{}")} {
		if err := p.Scan(raw); err != nil || p.Set() {
			t.Fatalf("Scan(%#v) = %v, %+v; want no profile and no error", raw, err, p)
		}
	}
	if err := p.Scan(42); err == nil {
		t.Fatal("a column of the wrong type was accepted as a profile")
	}
}

func TestAStandardWithOnlyABurstCeilingNamesNoSize(t *testing.T) {
	if (RunnerStandard{BurstMaxCPUs: 8}).Sized() {
		t.Fatal("a burst ceiling alone gives the slot count nothing to divide the machine by")
	}
	if !(RunnerStandard{MemoryMB: 4096}).Sized() || !(RunnerStandard{CPUs: 2}).Sized() {
		t.Fatal("either dimension is enough to name a size")
	}
}

// The mode that takes its size from the host is a stored choice: a pool with no
// typed figures reads the same either way, and only the flag says which it is.
func TestAPoolRemembersThatItTakesItsSizeFromTheHost(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)
	if got, _ := s.GetPool(ctx, pool.ID); got.SizeFromProfile {
		t.Fatal("a pool created without the setting takes its size from the host")
	}

	pool.SizeFromProfile = true
	if err := s.UpdatePool(ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	got, err := s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.SizeFromProfile {
		t.Fatal("size_from_profile did not survive a round trip")
	}
	if pools, _ := s.ListPools(ctx); len(pools) != 1 || !pools[0].SizeFromProfile {
		t.Fatalf("ListPools reads the flag differently from GetPool: %+v", pools)
	}

	created := &Pool{
		Name: "from-host", InstallationID: pool.InstallationID, Labels: StringSlice{"from-host"},
		Backend: BackendDocker, MaxRunners: 2, DockerMode: DockerNone, Enabled: true, Ephemeral: true,
		SizeFromProfile: true,
	}
	if err := s.CreatePool(ctx, created); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if got, _ := s.GetPool(ctx, created.ID); !got.SizeFromProfile {
		t.Fatal("CreatePool dropped size_from_profile")
	}
}

// 0070 must change nothing for a fleet that has not asked for it: every
// existing host reads as unprofiled and every existing pool as sized the way
// it always was.
func TestAHostAndPoolFromBeforeRunnerProfilesUpgradeUnchanged(t *testing.T) {
	ctx := context.Background()
	path := atSchemaBefore(t, "0070_runner_profiles.sql")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO hosts (id, name, created_at) VALUES ('host_old', 'old', 1)`); err != nil {
		t.Fatalf("seeding a host at the old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("upgrading: %v", err)
	}
	defer s.Close()

	h, err := s.GetHost(ctx, "host_old")
	if err != nil {
		t.Fatalf("reading an upgraded host: %v", err)
	}
	if h.RunnerProfile.Set() {
		t.Fatalf("an upgraded host has a profile: %+v", h.RunnerProfile)
	}
	for _, col := range []struct{ table, name string }{{"hosts", "runner_profile"}, {"pools", "size_from_profile"}} {
		var notNull, hasDefault int
		if err := s.read.QueryRowContext(ctx,
			`SELECT "notnull", dflt_value IS NOT NULL FROM pragma_table_info(?) WHERE name = ?`,
			col.table, col.name).Scan(&notNull, &hasDefault); err != nil {
			t.Fatalf("reading %s.%s: %v", col.table, col.name, err)
		}
		if notNull != 1 || hasDefault != 1 {
			t.Fatalf("%s.%s arrived without a NOT NULL default; existing rows would read as unset", col.table, col.name)
		}
	}
	_, pool, _ := seedPool(t, s)
	if got, _ := s.GetPool(ctx, pool.ID); got.SizeFromProfile {
		t.Fatal("a pool on an upgraded database takes its size from the host")
	}
}

func TestAJobIsStampedWithTheSizeItWasGrantedOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	job := seedJob(t, s, 1, JobInProgress, "run_a")
	if job.GrantedSource != "" || job.GrantedCPUs != 0 || job.GrantedMemoryMB != 0 {
		t.Fatalf("a new job claims a granted size: %+v", job)
	}

	got, err := s.StampJobGranted(ctx, job.ID, 3, 8192, AllocationFromProfile)
	if err != nil {
		t.Fatal(err)
	}
	if got.GrantedCPUs != 3 || got.GrantedMemoryMB != 8192 || got.GrantedSource != AllocationFromProfile {
		t.Fatalf("the stamp did not land: %+v", got)
	}

	// A second observer of the same assignment, or a runner resized since,
	// must not rewrite what this run was given.
	got, err = s.StampJobGranted(ctx, job.ID, 1.5, 4096, AllocationFromHost)
	if err != nil {
		t.Fatal(err)
	}
	if got.GrantedCPUs != 3 || got.GrantedMemoryMB != 8192 || got.GrantedSource != AllocationFromProfile {
		t.Fatalf("a second stamp rewrote the first: %+v", got)
	}

	// Delivery from GitHub moves the job on; the stamp is the fleet's own note
	// and has to be there afterwards.
	again, err := s.UpsertJob(ctx, &Job{GitHubJobID: 1, Repo: "acme/api", JobName: "build", State: JobCompleted, Conclusion: "success"})
	if err != nil {
		t.Fatal(err)
	}
	if again.GrantedSource != AllocationFromProfile || again.GrantedCPUs != 3 {
		t.Fatalf("a webhook delivery lost the granted size: %+v", again)
	}

	unlimited := seedJob(t, s, 2, JobInProgress, "run_b")
	got, err = s.StampJobGranted(ctx, unlimited.ID, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.GrantedSource != "" {
		t.Fatalf("a runner with no recorded source stamped %q", got.GrantedSource)
	}
	if _, err := s.StampJobGranted(ctx, "job_missing", 1, 1, AllocationFromHost); err == nil {
		t.Fatal("stamping a job that does not exist reported success")
	}
}

// 0071 backfills nothing: what a pool, a host or a profile said at the time is
// not recoverable, and a guess would put a job in a size it never had.
func TestAJobFromBeforeGrantedSizesUpgradesWithNoneRecorded(t *testing.T) {
	ctx := context.Background()
	path := atSchemaBefore(t, "0071_job_granted_size.sql")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO jobs (id, github_job_id, state, queued_at) VALUES ('job_old', 7, 'completed', 1)`); err != nil {
		t.Fatalf("seeding a job at the old schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("upgrading: %v", err)
	}
	defer s.Close()
	j, err := s.GetJob(ctx, "job_old")
	if err != nil {
		t.Fatalf("reading an upgraded job: %v", err)
	}
	if j.GrantedCPUs != 0 || j.GrantedMemoryMB != 0 || j.GrantedSource != "" {
		t.Fatalf("an upgraded job claims a granted size: %+v", j)
	}
	got, err := s.StampJobGranted(ctx, "job_old", 2, 4096, AllocationFromPool)
	if err != nil || got.GrantedSource != AllocationFromPool {
		t.Fatalf("an upgraded job could not be stamped: %+v, %v", got, err)
	}
}

// A host's slots are what its machine holds of the standard size, never more
// than the capacity the operator set. The figures are the real reserve floors:
// 12 CPUs less 5% leaves 11.4, 16 GB less 5% leaves 15565 MB, and the 4-CPU
// box keeps the half-core floor.
func TestSlotsAreWhatTheMachineHoldsOfTheStandardCappedByTheCapacity(t *testing.T) {
	big := func() Host { return Host{CPUs: 12, MemoryMB: 32768, Capacity: 8} }
	small := func() Host { return Host{CPUs: 4, MemoryMB: 16384, Capacity: 8} }
	with := func(h Host, capacity int, std RunnerStandard) *Host {
		h.Capacity, h.RunnerProfile.Standard = capacity, std
		return &h
	}
	cases := []struct {
		name string
		host *Host
		want int
	}{
		{"no profile leaves the capacity alone", with(big(), 8, RunnerStandard{}), 8},
		{"a burst ceiling alone names no size to divide by", with(big(), 8, RunnerStandard{BurstMaxCPUs: 8}), 8},
		{"CPU and memory both bind, the smaller wins", with(big(), 8, RunnerStandard{CPUs: 3, MemoryMB: 8192}), 3},
		{"the capacity caps what the machine could hold", with(big(), 2, RunnerStandard{CPUs: 3, MemoryMB: 8192}), 2},
		{"a capacity of zero still pauses the host", with(big(), 0, RunnerStandard{CPUs: 3, MemoryMB: 8192}), 0},
		{"CPU alone, memory unconstrained", with(big(), 8, RunnerStandard{CPUs: 2}), 5},
		{"memory alone, CPU unconstrained", with(big(), 8, RunnerStandard{MemoryMB: 16384}), 1},
		{"the small host in the worked example", with(small(), 8, RunnerStandard{CPUs: 1.5, MemoryMB: 4096}), 2},
		{"a standard the machine cannot hold once gives none", with(big(), 8, RunnerStandard{CPUs: 20}), 0},
		{"fractional CPUs divide without a rounding shortfall", with(big(), 8, RunnerStandard{CPUs: 3.8}), 3},
		{"an unmeasured host places by its capacity alone", with(Host{}, 6, RunnerStandard{CPUs: 3, MemoryMB: 8192}), 6},
		{"a host that measured only memory ignores a CPU standard", with(Host{MemoryMB: 32768}, 6, RunnerStandard{CPUs: 3}), 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.host.Slots(); got != tc.want {
				t.Fatalf("Slots() = %d, want %d (allocatable %+v)", got, tc.want, tc.host.Allocatable())
			}
		})
	}
}

// The throttle steps the slots down, so it has to be measured against the
// count the profile gives and not against the capacity beside it, or a
// throttled host would show room it does not have.
func TestAThrottleStepsDownTheDerivedSlotsNotTheCapacity(t *testing.T) {
	h := &Host{CPUs: 12, MemoryMB: 32768, Capacity: 8,
		RunnerProfile: RunnerProfile{Standard: RunnerStandard{CPUs: 3, MemoryMB: 8192}}}
	if got := h.EffectiveCapacity(); got != 3 {
		t.Fatalf("EffectiveCapacity() = %d, want the 3 slots the profile gives", got)
	}
	h.Throttle = HostThrottle{Level: 1}
	if got := h.EffectiveCapacity(); got != 2 {
		t.Fatalf("EffectiveCapacity() under one rung = %d, want 2 of the 3 slots", got)
	}
	h.Throttle = HostThrottle{Level: MaxThrottleLevel}
	if got := h.EffectiveCapacity(); got != 1 {
		t.Fatalf("EffectiveCapacity() on the top rung = %d, want the one-slot floor a throttle never goes below", got)
	}
	h.Capacity = 0
	if got := h.EffectiveCapacity(); got != 0 {
		t.Fatalf("a paused host takes %d runners under a throttle, want none", got)
	}
}
