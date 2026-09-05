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
