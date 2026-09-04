package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
