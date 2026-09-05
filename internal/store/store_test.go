package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedPool(t *testing.T, s *Store) (*Installation, *Pool, *Host) {
	t.Helper()
	ctx := context.Background()
	inst := &Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	pool := &Pool{
		Name: "linux-x64", InstallationID: inst.ID, Labels: StringSlice{"Linux-X64", "linux-x64", " docker "},
		Backend: BackendDocker, MinRunners: 0, MaxRunners: 4,
		IdleTimeout: Duration(5 * time.Minute), Ephemeral: true, DockerMode: DockerNone, Enabled: true,
	}
	if err := s.CreatePool(ctx, pool); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	host := &Host{Name: "vm-1", Capacity: 4, Embedded: true, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	return inst, pool, host
}

// Open documents itself as "creating if necessary". A state directory that does
// not exist yet is the normal case on a first run -- a fresh container volume, a
// systemd unit's StateDirectory, a --db-path somewhere new -- and SQLite reports
// only "unable to open database file (14)" when it is missing.
func TestOpenCreatesTheDatabaseDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "nested", "zoomies.db")

	s, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created: %v", err)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	s := newTestStore(t)
	// Running migrate a second time must be a no-op rather than an error.
	if err := s.migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestPoolLabelsAreNormalized(t *testing.T) {
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)
	got, err := s.GetPool(context.Background(), pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	want := []string{"docker", "linux-x64"}
	if len(got.Labels) != len(want) {
		t.Fatalf("labels = %v, want %v", got.Labels, want)
	}
	for i := range want {
		if got.Labels[i] != want[i] {
			t.Fatalf("labels = %v, want %v", got.Labels, want)
		}
	}
	if got.IdleTimeout.Duration() != 5*time.Minute {
		t.Errorf("idle timeout = %s, want 5m", got.IdleTimeout)
	}
}

func TestRunnerTransitionsAreEnforced(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "zoomies-linux-x64-abcd", Ephemeral: true}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	// provisioning -> idle skips registering and must be refused.
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerIdle, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("provisioning -> idle: got %v, want ErrInvalidTransition", err)
	}

	if _, err := s.TransitionRunner(ctx, r.ID, RunnerRegistering, ""); err != nil {
		t.Fatalf("-> registering: %v", err)
	}
	got, err := s.TransitionRunner(ctx, r.ID, RunnerIdle, "")
	if err != nil {
		t.Fatalf("-> idle: %v", err)
	}
	if got.LastIdleAt == nil || got.StartedAt == nil {
		t.Fatal("becoming idle must stamp started_at and last_idle_at")
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerBusy, ""); err != nil {
		t.Fatalf("-> busy: %v", err)
	}
	got, err = s.TransitionRunner(ctx, r.ID, RunnerIdle, "")
	if err != nil {
		t.Fatalf("-> idle again: %v", err)
	}
	if got.JobsHandled != 1 {
		t.Errorf("jobs_handled = %d, want 1 after one busy->idle cycle", got.JobsHandled)
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerRemoved, "done"); err != nil {
		t.Fatalf("-> removed: %v", err)
	}
	// Terminal state: nothing may follow.
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerIdle, ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("removed -> idle: got %v, want ErrInvalidTransition", err)
	}
}

func TestUpsertJobNeverMovesBackwards(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	started := now.Add(10 * time.Second)
	done := now.Add(time.Minute)

	if _, err := s.UpsertJob(ctx, &Job{
		GitHubJobID: 42, Repo: "acme/widgets", State: JobQueued, QueuedAt: now,
		Labels: StringSlice{"self-hosted", "Linux-X64"},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.UpsertJob(ctx, &Job{
		GitHubJobID: 42, State: JobCompleted, Conclusion: "success",
		StartedAt: &started, CompletedAt: &done,
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// A duplicate "queued" delivery arriving late must not resurrect the job.
	got, err := s.UpsertJob(ctx, &Job{GitHubJobID: 42, State: JobQueued})
	if err != nil {
		t.Fatalf("late queued: %v", err)
	}
	if got.State != JobCompleted {
		t.Errorf("state = %s, want completed (a stale webhook must not rewind a job)", got.State)
	}
	if got.Conclusion != "success" {
		t.Errorf("conclusion = %q, want success", got.Conclusion)
	}
	if got.QueueWait() != 10*time.Second {
		t.Errorf("queue wait = %s, want 10s", got.QueueWait())
	}
	if got.Duration() != 50*time.Second {
		t.Errorf("duration = %s, want 50s", got.Duration())
	}
}

func TestJoinTokenIsSingleUse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tok := &JoinToken{TokenHash: "hash", Prefix: "abcd", Capacity: 2, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateJoinToken(ctx, tok); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	if _, err := s.RedeemJoinToken(ctx, "hash", "host_1", now); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := s.RedeemJoinToken(ctx, "hash", "host_2", now); err == nil {
		t.Fatal("second redeem succeeded; join tokens must be single use")
	}
}

// TestJoinTokenRemembersWhichHostUsedIt is what lets a page that handed out a
// token find the machine that arrived with it.
func TestJoinTokenRemembersWhichHostUsedIt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tok := &JoinToken{TokenHash: "hash", Prefix: "abcd", Capacity: 2, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateJoinToken(ctx, tok); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}
	fresh, err := s.GetJoinToken(ctx, tok.ID)
	if err != nil {
		t.Fatalf("GetJoinToken before redeem: %v", err)
	}
	if !fresh.Usable(now) || fresh.UsedByID != "" {
		t.Fatalf("an unredeemed token reads as %+v", fresh)
	}
	if _, err := s.RedeemJoinToken(ctx, "hash", "host_1", now); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	spent, err := s.GetJoinToken(ctx, tok.ID)
	if err != nil {
		t.Fatalf("GetJoinToken after redeem: %v", err)
	}
	if spent.UsedAt == nil || spent.UsedByID != "host_1" {
		t.Errorf("after redeem the token reads %+v, want used by host_1", spent)
	}
	if _, err := s.GetJoinToken(ctx, "join_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetJoinToken of a missing id = %v, want ErrNotFound", err)
	}
}

func TestSecretSettingsAreNotListed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, "webhook_secret", "hunter2", true); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	list, err := s.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	for _, st := range list {
		if st.Key == "webhook_secret" && st.Value != "" {
			t.Fatal("ListSettings returned a secret value; it must be blanked")
		}
	}
	// The direct getter still works for internal use.
	v, err := s.GetSetting(ctx, "webhook_secret")
	if err != nil || v != "hunter2" {
		t.Fatalf("GetSetting = %q, %v", v, err)
	}
}

func TestListRunnersFiltersAndPaginates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, pool, host := seedPool(t, s)
	for i := 0; i < 5; i++ {
		r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "runner-" + string(rune('a'+i))}
		if err := s.CreateRunner(ctx, r); err != nil {
			t.Fatalf("CreateRunner: %v", err)
		}
	}
	got, total, err := s.ListRunners(ctx, RunnerFilter{PoolIDs: []string{pool.ID}}, Page{Limit: 2})
	if err != nil {
		t.Fatalf("ListRunners: %v", err)
	}
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(got) != 2 {
		t.Errorf("page size = %d, want 2", len(got))
	}
	counts, err := s.CountRunnersByPool(ctx)
	if err != nil {
		t.Fatalf("CountRunnersByPool: %v", err)
	}
	if counts[pool.ID].Provisioning != 5 {
		t.Errorf("provisioning = %d, want 5", counts[pool.ID].Provisioning)
	}
}

func TestNotFoundIsReported(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetPool(context.Background(), "pool_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// The probe an agent sends is the only thing that can explain a host that is
// connected and still running nothing, so it has to survive a round trip
// through the database intact -- including the backends that did not answer.
func TestHostBackendProbeRoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	host := &Host{
		Name:     "vm-1",
		Capacity: 2,
		Backends: StringSlice{"process"},
		BackendInfo: HostBackends{
			{Kind: BackendDocker, Detail: "cannot connect to /var/run/docker.sock: permission denied"},
			{Kind: BackendProcess, Available: true, Version: "fake", Endpoint: "memory"},
		},
	}
	if err := s.CreateHost(ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	got, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if len(got.BackendInfo) != 2 {
		t.Fatalf("backend info = %+v, want both backends", got.BackendInfo)
	}
	docker, ok := got.BackendInfo.Find(BackendDocker)
	if !ok || docker.Available || !strings.Contains(docker.Detail, "permission denied") {
		t.Fatalf("docker = %+v, want it recorded as unavailable with its reason", docker)
	}
	if kinds := got.BackendInfo.Kinds(); len(kinds) != 1 || kinds[0] != "process" {
		t.Fatalf("kinds = %v, want only the backend that answered", kinds)
	}

	got.BackendInfo = HostBackends{{Kind: BackendDocker, Available: true, SupportsDinD: true}}
	got.Backends = got.BackendInfo.Kinds()
	if err := s.UpdateHost(ctx, got); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	again, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if len(again.BackendInfo) != 1 || !again.BackendInfo[0].SupportsDinD {
		t.Fatalf("backend info = %+v, want the update to have replaced it", again.BackendInfo)
	}
}

// A host row written before this column existed still reads back, with no
// probe rather than a scan error.
func TestHostWithNoStoredProbeReadsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	host := &Host{Name: "vm-1", Capacity: 1, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(ctx, host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if _, err := s.exec(ctx, `UPDATE hosts SET backend_info = '' WHERE id = ?`, host.ID); err != nil {
		t.Fatalf("clearing backend_info: %v", err)
	}
	got, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if len(got.BackendInfo) != 0 || len(got.Backends) != 1 {
		t.Fatalf("host = %+v, want its backends and no probe", got)
	}
}

// TestSetInstallationAppSlugRecordsWhatTheProbeLearned covers the field an
// installation added by hand never carries. Every link to the App on GitHub is
// built from its slug -- including the settings page where its avatar is
// uploaded, the one setup step an App manifest cannot do -- so an installation
// whose slug is never learned has no way of offering that link at all.
func TestSetInstallationAppSlugRecordsWhatTheProbeLearned(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	inst := &Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	if inst.AppSlug != "" {
		t.Fatalf("a hand-added installation started with the slug %q", inst.AppSlug)
	}

	if err := s.SetInstallationAppSlug(ctx, inst.ID, "zoomies-acme"); err != nil {
		t.Fatalf("SetInstallationAppSlug: %v", err)
	}
	got, err := s.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if got.AppSlug != "zoomies-acme" {
		t.Errorf("app slug = %q, want zoomies-acme", got.AppSlug)
	}

	// Writing the same slug again must not touch the row: the probe runs on a
	// timer, and a row whose updated_at moves every minute makes the change
	// feed useless for telling what actually changed.
	before := got.UpdatedAt
	if err := s.SetInstallationAppSlug(ctx, inst.ID, "zoomies-acme"); err != nil {
		t.Fatalf("SetInstallationAppSlug again: %v", err)
	}
	again, err := s.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if !again.UpdatedAt.Equal(before) {
		t.Errorf("updated_at moved on an unchanged slug: %v -> %v", before, again.UpdatedAt)
	}
}

// The unmatched filter is what the Jobs page's "these will never run" banner is
// counted from, so it must not include a job that demonstrably already ran. A
// repository left on a hosted-runner vendor produces exactly that: labels no
// pool here claims, on jobs GitHub ran without this controller's help.
func TestUnmatchedOnlyLeavesOutJobsThatAlreadyRan(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	started := now.Add(time.Second)
	done := now.Add(time.Minute)

	if _, err := s.UpsertJob(ctx, &Job{
		GitHubJobID: 1, Repo: "acme/widgets", JobName: "waiting", State: JobQueued,
		QueuedAt: now, Labels: StringSlice{"typo-linux"},
	}); err != nil {
		t.Fatalf("queued job: %v", err)
	}
	if _, err := s.UpsertJob(ctx, &Job{
		GitHubJobID: 2, Repo: "acme/widgets", JobName: "ran elsewhere", State: JobCompleted,
		Conclusion: "success", QueuedAt: now, StartedAt: &started, CompletedAt: &done,
		Labels: StringSlice{"blacksmith-4vcpu-ubuntu-2404"},
	}); err != nil {
		t.Fatalf("completed job: %v", err)
	}

	got, total, err := s.ListJobs(ctx, JobFilter{UnmatchedOnly: true}, Page{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("total = %d, jobs = %d, want only the job still waiting", total, len(got))
	}
	if got[0].JobName != "waiting" {
		t.Fatalf("unmatched job = %q, want the queued one", got[0].JobName)
	}
}

// The brand is put on here rather than only in the handler because the API is
// not the only writer: the installer and the demo seeder create pools too, and
// a pool whose name says nothing about this fleet is one an operator meets
// again in GitHub's runner list next to registrations nobody here made.
func TestPoolNamesAreBrandedWhoeverWritesThem(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	inst := &Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}

	pool := &Pool{
		Name: "gpu", InstallationID: inst.ID, Labels: StringSlice{"gpu"},
		Backend: BackendDocker, MaxRunners: 4, DockerMode: DockerNone, Enabled: true,
	}
	if err := s.CreatePool(ctx, pool); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if pool.Name != "zoomies-gpu" {
		t.Fatalf("created name = %q, want it branded", pool.Name)
	}

	// A pool carried over from a build that did not brand names gains the
	// prefix the next time it is written, rather than keeping a name no new
	// pool could have.
	pool.Name = "builders"
	if err := s.UpdatePool(ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	got, err := s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got.Name != "zoomies-builders" {
		t.Fatalf("updated name = %q, want it branded", got.Name)
	}
}

// The Jobs page defaults to this fleet's own work, because GitHub reports every
// job in an installed repository and most of them belong to somebody else's
// runners. What counts as ours is deliberately generous: a pool claimed it, a
// runner here ran it, or nothing has run it yet -- an unclaimed queued job is a
// fault this fleet has to be able to see.
func TestManagedOnlyKeepsThisFleetsWorkAndItsUnclaimedQueue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	started := now.Add(time.Second)
	done := now.Add(time.Minute)

	jobs := []*Job{
		{GitHubJobID: 1, Repo: "acme/widgets", JobName: "claimed", State: JobCompleted,
			Conclusion: "success", PoolID: "pool_1", Matched: true,
			QueuedAt: now, StartedAt: &started, CompletedAt: &done},
		{GitHubJobID: 2, Repo: "acme/widgets", JobName: "ran here", State: JobCompleted,
			Conclusion: "success", RunnerID: "run_1",
			QueuedAt: now, StartedAt: &started, CompletedAt: &done},
		{GitHubJobID: 3, Repo: "acme/widgets", JobName: "waiting", State: JobQueued,
			QueuedAt: now, Labels: StringSlice{"typo-linux"}},
		{GitHubJobID: 4, Repo: "acme/widgets", JobName: "hosted", State: JobCompleted,
			Conclusion: "success", QueuedAt: now, StartedAt: &started, CompletedAt: &done,
			Labels: StringSlice{"ubuntu-latest"}},
	}
	for _, j := range jobs {
		if _, err := s.UpsertJob(ctx, j); err != nil {
			t.Fatalf("UpsertJob %s: %v", j.JobName, err)
		}
	}

	got, total, err := s.ListJobs(ctx, JobFilter{ManagedOnly: true}, Page{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	names := map[string]bool{}
	for _, j := range got {
		names[j.JobName] = true
	}
	if total != 3 || len(got) != 3 {
		t.Fatalf("total = %d, jobs = %d (%v), want the three this fleet has a hand in", total, len(got), names)
	}
	if names["hosted"] {
		t.Error("a job run on a hosted runner is listed as this fleet's")
	}
	for _, want := range []string{"claimed", "ran here", "waiting"} {
		if !names[want] {
			t.Errorf("%q is missing from the managed list", want)
		}
	}

	// Without the flag the page still shows everything, which is what the
	// "include other runners" toggle asks for.
	_, all, err := s.ListJobs(ctx, JobFilter{}, Page{})
	if err != nil {
		t.Fatalf("ListJobs unfiltered: %v", err)
	}
	if all != 4 {
		t.Fatalf("unfiltered total = %d, want all 4 jobs", all)
	}
}

// Both flags together must not cancel each other out: the unmatched view is a
// narrower question about the same fleet, and answering it with an empty page
// would send an operator looking for a bug that is not there.
func TestUnmatchedOnlyStillAnswersWhenManagedOnlyIsAlsoSet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if _, err := s.UpsertJob(ctx, &Job{
		GitHubJobID: 1, Repo: "acme/widgets", JobName: "waiting", State: JobQueued,
		QueuedAt: now, Labels: StringSlice{"typo-linux"},
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	got, total, err := s.ListJobs(ctx, JobFilter{UnmatchedOnly: true, ManagedOnly: true}, Page{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("total = %d, jobs = %d, want the unmatched job", total, len(got))
	}
}

// last_idle_at is when a runner became idle, which is what the idle timeout
// counts from. A repeated transition to idle used to move it forward, so a
// second caller reporting "still idle" made the runner immortal.
func TestASelfTransitionDoesNotRestartTheIdleClock(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	s, err := Open(context.Background(), Options{Path: ":memory:", Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "zoomies-abcd", Ephemeral: true}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerRegistering, ""); err != nil {
		t.Fatal(err)
	}
	first, err := s.TransitionRunner(ctx, r.ID, RunnerIdle, "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	again, err := s.TransitionRunner(ctx, r.ID, RunnerIdle, "still here")
	if err != nil {
		t.Fatalf("idle -> idle: %v", err)
	}
	if !again.LastIdleAt.Equal(*first.LastIdleAt) {
		t.Fatalf("last_idle_at moved from %s to %s on a self-transition", first.LastIdleAt, again.LastIdleAt)
	}
	if again.Message != "still here" {
		t.Fatalf("message = %q; a self-transition may still carry a message", again.Message)
	}

	failed, err := s.TransitionRunner(ctx, r.ID, RunnerFailed, "boom")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	failedAgain, err := s.TransitionRunner(ctx, r.ID, RunnerFailed, "boom again")
	if err != nil {
		t.Fatalf("failed -> failed: %v", err)
	}
	if !failedAgain.FinishedAt.Equal(*failed.FinishedAt) {
		t.Fatalf("finished_at moved from %s to %s on a self-transition", failed.FinishedAt, failedAgain.FinishedAt)
	}
}

// The jobs table was rebuilt to admit the waiting state. A rebuild that lost a
// row, a column or the unique index the upsert keys on would be a quiet
// disaster on every existing database, so an existing database is what this
// migrates.
func TestTheJobsRebuildKeepsEveryRowAndItsIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.db")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// A database at the schema before the rebuild: open it normally, then
	// pretend the rebuild has not happened by removing its ledger row and
	// putting the old table back the way 0008 left it.
	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE name = '0009_jobs_waiting_state.sql'`,
		`DROP TABLE jobs`,
		`CREATE TABLE jobs (
			id TEXT PRIMARY KEY, github_job_id INTEGER NOT NULL, github_run_id INTEGER NOT NULL DEFAULT 0,
			repo TEXT NOT NULL DEFAULT '', workflow TEXT NOT NULL DEFAULT '', job_name TEXT NOT NULL DEFAULT '',
			labels TEXT NOT NULL DEFAULT '[]', state TEXT NOT NULL CHECK (state IN ('queued','in_progress','completed')),
			conclusion TEXT NOT NULL DEFAULT '', pool_id TEXT NOT NULL DEFAULT '', runner_id TEXT NOT NULL DEFAULT '',
			runner_name TEXT NOT NULL DEFAULT '', html_url TEXT NOT NULL DEFAULT '', queued_at INTEGER NOT NULL,
			started_at INTEGER, completed_at INTEGER, matched INTEGER NOT NULL DEFAULT 0,
			head_branch TEXT NOT NULL DEFAULT '', head_sha TEXT NOT NULL DEFAULT '', run_attempt INTEGER NOT NULL DEFAULT 0,
			steps TEXT NOT NULL DEFAULT '[]', runner_fault TEXT NOT NULL DEFAULT '')`,
		`CREATE UNIQUE INDEX idx_jobs_github ON jobs(github_job_id)`,
	} {
		if _, err := s.write.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("preparing the old schema: %v: %s", err, stmt)
		}
	}
	done := now.Add(time.Minute)
	if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: 501, Repo: "acme/widgets", State: JobCompleted, Conclusion: "success",
		Labels: StringSlice{"self-hosted"}, QueuedAt: now, CompletedAt: &done, HeadBranch: "main", RunAttempt: 2,
		Steps: JobSteps{{Number: 1, Name: "Checkout", Status: "completed", Conclusion: "success"}}}); err != nil {
		t.Fatalf("writing a row into the old schema: %v", err)
	}
	if _, err := s.UpsertJob(ctx, &Job{GitHubJobID: 502, State: JobQueued, QueuedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open after the rebuild: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })

	got, err := migrated.GetJobByGitHubID(ctx, 501)
	if err != nil {
		t.Fatalf("the completed row did not survive the rebuild: %v", err)
	}
	if got.Repo != "acme/widgets" || got.Conclusion != "success" || got.HeadBranch != "main" || got.RunAttempt != 2 ||
		len(got.Steps) != 1 || !got.CompletedAt.Equal(done) {
		t.Fatalf("the rebuilt row lost a column: %+v", got)
	}
	if _, err := migrated.GetJobByGitHubID(ctx, 502); err != nil {
		t.Fatalf("the queued row did not survive the rebuild: %v", err)
	}
	// The unique index is what the upsert keys on: a second delivery for a
	// job must find the first row, not add another.
	if _, err := migrated.UpsertJob(ctx, &Job{GitHubJobID: 502, State: JobInProgress}); err != nil {
		t.Fatal(err)
	}
	if _, total, err := migrated.ListJobs(ctx, JobFilter{}, Page{Limit: 10}); err != nil || total != 2 {
		t.Fatalf("jobs after an upsert = %d (%v), want 2", total, err)
	}
	// And the state the rebuild was for is admitted.
	if _, err := migrated.UpsertJob(ctx, &Job{GitHubJobID: 503, State: JobWaiting, QueuedAt: now}); err != nil {
		t.Fatalf("a waiting job was refused after the rebuild: %v", err)
	}
	var indexes int
	if err := migrated.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'jobs' AND name LIKE 'idx_jobs_%'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if indexes != 6 {
		t.Fatalf("the rebuilt jobs table has %d indexes, want 6", indexes)
	}
}
