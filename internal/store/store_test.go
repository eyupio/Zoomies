package store

import (
	"context"
	"errors"
	"fmt"
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

// newTestStoreAt is newTestStore with the clock in the test's hands, for the
// rows whose value is a moment rather than a fact.
func newTestStoreAt(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	s, err := Open(context.Background(), Options{Path: ":memory:", Now: now})
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
	if _, err := s.RedeemJoinToken(ctx, "hash", JoinClaim{HostID: "host_1"}, now); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, err := s.RedeemJoinToken(ctx, "hash", JoinClaim{HostID: "host_2"}, now); err == nil {
		t.Fatal("second redeem succeeded; join tokens must be single use")
	}
}

// A token that travels inside a guest is readable by more people than one an
// operator pastes into a terminal, so it is pinned to the one name it was
// minted for. The check runs inside the transaction that spends the token,
// which is what stops a copied VM image enrolling as somebody else even in a
// race with the machine the token belongs to.
func TestAJoinTokenMintedForOneMachineCannotEnrolAnother(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tok := &JoinToken{TokenHash: "hash", Prefix: "abcd", Capacity: 2, ExpiresAt: now.Add(time.Hour),
		MachineID: "mach_abc", ExpectedName: "zoomies-mach-abc"}
	if err := s.CreateJoinToken(ctx, tok); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}

	_, err := s.RedeemJoinToken(ctx, "hash", JoinClaim{HostID: "host_1", Name: "somebody-elses-laptop"}, now)
	if !errors.Is(err, ErrJoinTokenScope) {
		t.Fatalf("redeeming under another name = %v, want ErrJoinTokenScope", err)
	}
	// And the refusal did not spend it: a token burnt by a claim it refused
	// would let anyone who copied it stop the machine it was minted for from
	// ever enrolling.
	spent, err := s.GetJoinToken(ctx, tok.ID)
	if err != nil {
		t.Fatalf("GetJoinToken: %v", err)
	}
	if spent.UsedAt != nil {
		t.Fatal("a refused claim spent the token")
	}
	if _, err := s.RedeemJoinToken(ctx, "hash", JoinClaim{HostID: "host_1", Name: "zoomies-mach-abc"}, now); err != nil {
		t.Fatalf("the machine the token was minted for could not redeem it: %v", err)
	}
}

// A scoped token is still single-use, and exactly one of two agents racing with
// a copy of it enrols. Two machines sharing one host row is what
// Host.AgentSessionAlternations exists to detect, and this is the half that
// stops it happening in the first place.
func TestAMachineTokenIsSingleUseAcrossARace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tok := &JoinToken{TokenHash: "hash", Prefix: "abcd", Capacity: 2, ExpiresAt: now.Add(time.Hour),
		MachineID: "mach_abc", ExpectedName: "zoomies-mach-abc"}
	if err := s.CreateJoinToken(ctx, tok); err != nil {
		t.Fatalf("CreateJoinToken: %v", err)
	}

	const racers = 8
	results := make(chan error, racers)
	for i := range racers {
		go func(i int) {
			_, err := s.RedeemJoinToken(ctx, "hash",
				JoinClaim{HostID: fmt.Sprintf("host_%d", i), Name: "zoomies-mach-abc"}, now)
			results <- err
		}(i)
	}
	won := 0
	for range racers {
		if err := <-results; err == nil {
			won++
		} else if !errors.Is(err, ErrJoinTokenUsed) {
			t.Errorf("a loser was refused with %v, want ErrJoinTokenUsed", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d racing agents enrolled with one token, want exactly 1", won, racers)
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
	if _, err := s.RedeemJoinToken(ctx, "hash", JoinClaim{HostID: "host_1"}, now); err != nil {
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
		// Held by GitHub for a deployment review, and therefore unclaimed: the
		// claim happens on the approval, so a held job can never be matched
		// while it is held. It is this fleet's to see for exactly the same
		// reason an unclaimed queued job is -- nothing has run it -- and
		// keeping only `queued` hid every one of them from the page by
		// default, so the operator whose deploy was waiting could not find it.
		{GitHubJobID: 5, Repo: "acme/widgets", JobName: "held", State: JobWaiting,
			QueuedAt: now, Labels: StringSlice{"zoomies-linux-x64"}},
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
	if total != 4 || len(got) != 4 {
		t.Fatalf("total = %d, jobs = %d (%v), want the four this fleet has a hand in", total, len(got), names)
	}
	if names["hosted"] {
		t.Error("a job run on a hosted runner is listed as this fleet's")
	}
	for _, want := range []string{"claimed", "ran here", "waiting", "held"} {
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
	if all != 5 {
		t.Fatalf("unfiltered total = %d, want all 5 jobs", all)
	}
}

// The bug this is here for: a repository on a hosted-runner vendor queues a job,
// GitHub tells us about it seconds before the vendor starts it, and it arrived
// on a Jobs page whose switch for other runners was off -- badged, in the same
// row, as hosted elsewhere. Nothing this fleet owns will ever run it and nothing
// here is waiting on it, so it belongs in the same place its finished siblings
// do: behind the switch.
func TestAQueuedJobOnSomebodyElsesRunnersIsNotThisFleetsWork(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	jobs := []*Job{
		{GitHubJobID: 1, Repo: "acme/widgets", JobName: "vendor", State: JobQueued,
			QueuedAt: now, Labels: StringSlice{"blacksmith-4vcpu-ubuntu-2404"}},
		{GitHubJobID: 2, Repo: "acme/widgets", JobName: "github hosted", State: JobQueued,
			QueuedAt: now, Labels: StringSlice{"ubuntu-latest"}},
		// Held for a deployment review, and equally somebody else's to run.
		{GitHubJobID: 3, Repo: "acme/widgets", JobName: "vendor held", State: JobWaiting,
			QueuedAt: now, Labels: StringSlice{"buildjet-4vcpu-ubuntu-2204"}},
		// Asks this fleet for something and gets nothing: the one queued job
		// with no pool that is genuinely ours to show.
		{GitHubJobID: 4, Repo: "acme/widgets", JobName: "ours", State: JobQueued,
			QueuedAt: now, Labels: StringSlice{"self-hosted", "linux", "gpu"}},
		// Half somebody else's labels is not somebody else's job: only a pool
		// here could answer "gpu", so this one is still waiting on us.
		{GitHubJobID: 5, Repo: "acme/widgets", JobName: "mixed", State: JobQueued,
			QueuedAt: now, Labels: StringSlice{"ubuntu-latest", "gpu"}},
	}
	for _, j := range jobs {
		if _, err := s.UpsertJob(ctx, j); err != nil {
			t.Fatalf("UpsertJob %s: %v", j.JobName, err)
		}
	}

	for _, tc := range []struct {
		name   string
		filter JobFilter
		want   []string
	}{
		{"the default view", JobFilter{ManagedOnly: true}, []string{"ours", "mixed"}},
		// The warning is the sharper case: "nothing will run this" about a job
		// a vendor is about to run is false, and it is red.
		{"the unmatched filter", JobFilter{UnmatchedOnly: true}, []string{"ours", "mixed"}},
		{"both switches off", JobFilter{}, []string{"vendor", "github hosted", "vendor held", "ours", "mixed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := s.ListJobs(ctx, tc.filter, Page{})
			if err != nil {
				t.Fatalf("ListJobs: %v", err)
			}
			names := map[string]bool{}
			for _, j := range got {
				names[j.JobName] = true
			}
			if total != len(tc.want) || len(got) != len(tc.want) {
				t.Fatalf("total = %d, jobs = %d (%v), want %v", total, len(got), names, tc.want)
			}
			for _, want := range tc.want {
				if !names[want] {
					t.Errorf("%q is missing", want)
				}
			}
		})
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

	// A database at the schema 0008 left behind: open it normally, then wind
	// every later migration that touches these two tables back off it -- the
	// ledger row and the change itself -- so that reopening migrates a genuine
	// old database forward rather than one that is already up to date.
	//
	// The rows are written with SQL against that old table rather than through
	// UpsertJob, because UpsertJob writes today's columns and the whole point
	// of the fixture is a table that does not have them yet.
	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	done := now.Add(time.Minute)
	for _, stmt := range []string{
		`DELETE FROM schema_migrations WHERE name IN
			('0009_jobs_waiting_state.sql', '0012_job_installation.sql',
			 '0019_job_eligible_at.sql', '0026_provisioning_queue.sql',
			 '0034_job_fault_kind.sql', '0041_job_cancellation_requested.sql',
			 '0043_job_run_number.sql', '0053_job_run_number_backfill.sql',
			 '0054_jobs_run_index.sql', '0060_job_versions.sql', '0061_job_usage.sql',
			 '0062_job_indexes_for_scans.sql', '0071_job_granted_size.sql',
			 '0074_job_size_routing.sql')`,
		`DROP INDEX runners_provisioning_order`,
		`ALTER TABLE runner_sessions DROP COLUMN controller_version`,
		`ALTER TABLE runner_sessions DROP COLUMN controller_channel`,
		`ALTER TABLE runner_sessions DROP COLUMN agent_version`,
		`ALTER TABLE webhook_deliveries DROP COLUMN installation_id`,
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
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, repo, labels, state, conclusion, queued_at,
			completed_at, head_branch, run_attempt, steps)
			VALUES ('job_old1', 501, 'acme/widgets', '["self-hosted"]', 'completed', 'success', %d, %d,
			'main', 2, '[{"number":1,"name":"Checkout","status":"completed","conclusion":"success"}]')`,
			now.UnixMilli(), done.UnixMilli()),
		fmt.Sprintf(`INSERT INTO jobs (id, github_job_id, state, queued_at)
			VALUES ('job_old2', 502, 'queued', %d)`, now.UnixMilli()),
	} {
		if _, err := s.write.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("preparing the old schema: %v: %s", err, stmt)
		}
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
	// Six from the rebuild itself, and one each for the fault category, the
	// cancellation stamp and a run's jobs that later migrations added, and two
	// for the job statistics' window and job-name grouping (0060), and one
	// for a job's usage profile (0061), and two from 0062: the faulted jobs and
	// the listing's order, which replaced the plain queued_at index. A
	// rebuild that replayed and left a later one behind would take the Jobs
	// page's category filter, its Running and Queued views, or every job
	// event's run-number lookup back to a full scan.
	if indexes != 13 {
		t.Fatalf("the rebuilt jobs table has %d indexes, want 13", indexes)
	}
}

// Pools saved before the API swapped the stock image for its Docker variant
// still name an image that cannot use the daemon their docker_mode gives
// them. The migration moves exactly those, and nothing else: not a pool with
// no daemon, not a pinned tag the variant may not exist for, not an image of
// the operator's own, not a digest that names one exact image, and not a pool
// already on the variant.
func TestExistingDaemonPoolsAreMovedOntoTheDockerImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zoomies.db")
	ctx := context.Background()

	// The rows are created on a frozen clock in the past, so that the
	// migration's wall-clock updated_at is later than created_at by months
	// rather than by however long a reopen happens to take on this machine.
	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := Open(ctx, Options{Path: path, Now: func() time.Time { return past }})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	inst := &Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	const digest = "ghcr.io/eyupio/zoomies-runner@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		name  string
		mode  DockerMode
		image string
		want  string
	}{
		{"dind-latest", DockerDinD, "ghcr.io/eyupio/zoomies-runner:latest", "ghcr.io/eyupio/zoomies-runner-docker:latest"},
		{"socket-main", DockerHostSocket, "ghcr.io/eyupio/zoomies-runner:main", "ghcr.io/eyupio/zoomies-runner-docker:main"},
		{"dind-untagged", DockerDinD, "ghcr.io/eyupio/zoomies-runner", "ghcr.io/eyupio/zoomies-runner-docker"},
		{"dind-pinned-sha", DockerDinD, "ghcr.io/eyupio/zoomies-runner:sha-b966fb6", "ghcr.io/eyupio/zoomies-runner:sha-b966fb6"},
		{"dind-pinned-release", DockerDinD, "ghcr.io/eyupio/zoomies-runner:v0.1-alpha", "ghcr.io/eyupio/zoomies-runner:v0.1-alpha"},
		{"none-stock", DockerNone, "ghcr.io/eyupio/zoomies-runner:latest", "ghcr.io/eyupio/zoomies-runner:latest"},
		{"dind-own", DockerDinD, "registry.example.com/ci/runner:latest", "registry.example.com/ci/runner:latest"},
		{"dind-mirror", DockerDinD, "registry.example.com/eyupio/zoomies-runner:latest", "registry.example.com/eyupio/zoomies-runner:latest"},
		{"dind-lookalike", DockerDinD, "ghcr.io/eyupio/zoomies-runner-gpu:latest", "ghcr.io/eyupio/zoomies-runner-gpu:latest"},
		{"dind-digest", DockerDinD, digest, digest},
		{"dind-already", DockerDinD, "ghcr.io/eyupio/zoomies-runner-docker:latest", "ghcr.io/eyupio/zoomies-runner-docker:latest"},
	}
	ids := map[string]string{}
	for _, tc := range cases {
		p := &Pool{Name: tc.name, InstallationID: inst.ID, Labels: StringSlice{tc.name}, Backend: BackendDocker,
			Image: tc.image, MaxRunners: 1, Ephemeral: true, DockerMode: tc.mode, Enabled: true}
		if err := s.CreatePool(ctx, p); err != nil {
			t.Fatalf("CreatePool %s: %v", tc.name, err)
		}
		ids[tc.name] = p.ID
	}
	// The rows are in the shape the migration will find them in: it ran on an
	// empty table when the database was opened, so pretend it has not.
	if _, err := s.write.ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE name = '0010_docker_pools_get_a_client.sql'`); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("Open after the migration: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })
	for _, tc := range cases {
		got, err := migrated.GetPool(ctx, ids[tc.name])
		if err != nil {
			t.Fatalf("GetPool %s: %v", tc.name, err)
		}
		if got.Image != tc.want {
			t.Errorf("%s: image = %q, want %q", tc.name, got.Image, tc.want)
		}
		if moved := got.Image != tc.image; moved != got.UpdatedAt.After(got.CreatedAt) {
			t.Errorf("%s: updated_at moved = %v, want it to move exactly when the image did", tc.name, !moved)
		}
	}
}

// The ledger is the schema version: there is no number, only the file names,
// so the readiness probe and a future backup manifest read the list and take
// the last one. It has to come back in the order the migrations apply.
func TestAppliedMigrationsListsTheLedgerInOrder(t *testing.T) {
	s := newTestStore(t)
	applied, err := s.AppliedMigrations(context.Background())
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	embedded, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(applied) != len(embedded) {
		t.Fatalf("ledger has %d rows, the binary embeds %d migrations", len(applied), len(embedded))
	}
	for i := range applied {
		if applied[i].Name != embedded[i].name {
			t.Errorf("ledger[%d] = %s, want %s", i, applied[i].Name, embedded[i].name)
		}
		if applied[i].AppliedAt.IsZero() {
			t.Errorf("ledger[%d] %s has no applied_at", i, applied[i].Name)
		}
	}
}
func TestPlatformRoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	inst, _, _ := seedPool(t, s)

	pool := &Pool{
		Name: "zoomies-4vcpu-ubuntu-2404-arm64", InstallationID: inst.ID,
		Backend: BackendDocker, MaxRunners: 4, Ephemeral: true, Enabled: true,
		DockerMode: DockerNone,
		// Deliberately in the spellings an operator types rather than the ones
		// the scheduler compares, so that normalisation on write is exercised.
		Platform:  Platform{OS: "Ubuntu", OSVersion: "24.04", Arch: "aarch64"},
		Resources: Resources{CPUs: 4, MemoryMB: 16384},
	}
	if err := s.CreatePool(ctx, pool); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	got, err := s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	want := Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "arm64"}
	if got.Platform != want {
		t.Errorf("platform = %+v, want %+v", got.Platform, want)
	}
	if name := got.CanonicalName(); name != "zoomies-4vcpu-16gb-ubuntu-2404-arm64" {
		t.Errorf("CanonicalName = %q", name)
	}

	got.Platform = Platform{OS: "debian", OSVersion: "12", Arch: "amd64"}
	if err := s.UpdatePool(ctx, got); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	again, err := s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if again.Platform != got.Platform {
		t.Errorf("platform after update = %+v, want %+v", again.Platform, got.Platform)
	}
}

func TestHostReportsWhatMachineItIs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	h := &Host{
		Name: "zoomies-16vcpu-ubuntu-2404-build01", Capacity: 8, Backends: StringSlice{"docker"},
		OS: "linux", Distro: "ubuntu", OSVersion: "24.04", Arch: "amd64",
		CPUs: 16, MemoryMB: 32768,
	}
	if err := s.CreateHost(ctx, h); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	got, err := s.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.CPUs != 16 || got.MemoryMB != 32768 {
		t.Errorf("size = %d vCPU / %d MB, want 16 / 32768", got.CPUs, got.MemoryMB)
	}
	// "linux" cannot pick an image; the distribution is what a pool asks for.
	want := Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "amd64"}
	if got.Platform() != want {
		t.Errorf("Platform() = %+v, want %+v", got.Platform(), want)
	}
	if name := got.Spec().String(); name != "zoomies-16vcpu-32gb-ubuntu-2404-build01" {
		t.Errorf("Spec().String() = %q", name)
	}

	got.OSVersion = "22.04"
	got.CPUs = 32
	if err := s.UpdateHost(ctx, got); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}
	again, err := s.GetHost(ctx, h.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if again.OSVersion != "22.04" || again.CPUs != 32 {
		t.Errorf("host after update = %q / %d vCPU", again.OSVersion, again.CPUs)
	}
}

func TestPlatformMatching(t *testing.T) {
	host := Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "amd64"}
	cases := []struct {
		name string
		pool Platform
		want bool
	}{
		{"a pool that asks nothing goes anywhere", Platform{}, true},
		{"the same machine", host, true},
		{"a different distribution", Platform{OS: "debian"}, false},
		{"a different architecture", Platform{Arch: "arm64"}, false},
		{"a different release", Platform{OS: "ubuntu", OSVersion: "22.04"}, false},
		{"the release written compactly", Platform{OS: "ubuntu", OSVersion: "2404"}, true},
		{"an architecture in GitHub's spelling", Platform{Arch: "x64"}, true},
		{"only part of the machine named", Platform{OS: "ubuntu"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.pool.Matches(host); got != c.want {
				t.Errorf("Matches = %v, want %v", got, c.want)
			}
		})
	}

	// A host that has not reported a field cannot be ruled out by it: adding a
	// platform to a pool must never widen placement, and it must not narrow it
	// against hosts that predate the field either.
	silent := Platform{}
	if !(Platform{OS: "ubuntu", Arch: "arm64"}).Matches(silent) {
		t.Error("a pool with a platform was refused a host that reported none")
	}
}

// The scheduler's drain timeout counts from here, so this stamp is the whole
// mechanism: a drain whose start is not recorded, or is pushed forward every
// time somebody says "still draining", is a drain no timeout can ever reach.
func TestEnteringDrainingIsStampedOnceAndNotMovedAfterwards(t *testing.T) {
	clock := time.Date(2025, 3, 4, 12, 0, 0, 0, time.UTC)
	s := newTestStore(t)
	s.now = func() time.Time { return clock }
	ctx := context.Background()
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "zoomies-linux-x64-abcd", Ephemeral: true}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	if r.DrainingSince != nil {
		t.Fatal("a runner is not draining when it is created")
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerRegistering, ""); err != nil {
		t.Fatalf("-> registering: %v", err)
	}

	got, err := s.TransitionRunner(ctx, r.ID, RunnerDraining, "operator asked")
	if err != nil {
		t.Fatalf("-> draining: %v", err)
	}
	if got.DrainingSince == nil || !got.DrainingSince.Equal(clock) {
		t.Fatalf("draining_since = %v, want %v", got.DrainingSince, clock)
	}
	first := *got.DrainingSince

	// An agent reporting the state it is already in is legal, and must not
	// restart the clock -- that is how the runner would become immortal.
	clock = clock.Add(time.Hour)
	got, err = s.TransitionRunner(ctx, r.ID, RunnerDraining, "still draining")
	if err != nil {
		t.Fatalf("-> draining again: %v", err)
	}
	if got.DrainingSince == nil || !got.DrainingSince.Equal(first) {
		t.Fatalf("draining_since moved to %v; it must stay at %v", got.DrainingSince, first)
	}

	// And it survives a reload, because a controller restart is the case the
	// whole column exists for.
	reloaded, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if reloaded.DrainingSince == nil || !reloaded.DrainingSince.Equal(first) {
		t.Fatalf("draining_since did not survive a reload: %v", reloaded.DrainingSince)
	}
}

// The reserve is what an operator holds back from placement for a machine's
// own sake, and it is theirs the way capacity is. Its own statement is what
// keeps it that way: UpdateHost is the path an agent's heartbeat takes, and a
// host that could write this could talk its way out of the room kept for it.
func TestAHostsReserveIsSetOnItsOwnAndSurvivesWhatAnAgentReports(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, host := seedPool(t, s)

	if err := s.SetHostReserve(ctx, host.ID, 2, 4096, 50_000); err != nil {
		t.Fatalf("SetHostReserve: %v", err)
	}
	got, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.ReserveCPUs != 2 || got.ReserveMemoryMB != 4096 || got.ReserveDiskMB != 50_000 {
		t.Fatalf("reserve = %d cpus, %d MB, %d MB disk", got.ReserveCPUs, got.ReserveMemoryMB, got.ReserveDiskMB)
	}

	// What an agent reports goes through UpdateHost, which carries the
	// observations and leaves the reserve alone.
	got.DiskTotalMB, got.DiskFreeMB = 500_000, 200_000
	got.ReserveCPUs, got.ReserveMemoryMB, got.ReserveDiskMB = 0, 0, 0
	if err := s.UpdateHost(ctx, got); err != nil {
		t.Fatalf("UpdateHost: %v", err)
	}

	after, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if after.ReserveCPUs != 2 || after.ReserveMemoryMB != 4096 || after.ReserveDiskMB != 50_000 {
		t.Fatalf("reserve = %d cpus, %d MB, %d MB disk after an agent's update; it must be untouched",
			after.ReserveCPUs, after.ReserveMemoryMB, after.ReserveDiskMB)
	}
	if after.DiskTotalMB != 500_000 || after.DiskFreeMB != 200_000 {
		t.Fatalf("disk = %d total, %d free; the observation did not land", after.DiskTotalMB, after.DiskFreeMB)
	}
	// A host that does not exist is a caller's mistake, not a silent no-op.
	if err := s.SetHostReserve(ctx, "host_nope", 1, 1, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetHostReserve on a missing host = %v, want ErrNotFound", err)
	}
}
