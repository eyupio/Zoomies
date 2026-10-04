package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestListInstallationsReturnsOldestFirst(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })

	for _, target := range []string{"acme", "globex", "initech"} {
		if err := s.CreateInstallation(ctx, &Installation{AppID: 1, InstallationID: int64(len(target)),
			Target: target, TargetType: TargetOrg}); err != nil {
			t.Fatalf("CreateInstallation(%s): %v", target, err)
		}
		now = now.Add(time.Minute)
	}

	got, err := s.ListInstallations(ctx)
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	want := []string{"acme", "globex", "initech"}
	if len(got) != len(want) {
		t.Fatalf("ListInstallations returned %d rows, want %d", len(got), len(want))
	}
	for i, target := range want {
		if got[i].Target != target {
			t.Fatalf("installation %d = %q, want %q", i, got[i].Target, target)
		}
	}
}

func TestUpdateInstallationPersistsCredentialsAndNormalisesTheTarget(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	inst := &Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}

	checked := time.Date(2025, 2, 2, 2, 2, 2, 0, time.UTC)
	inst.Target = "  ACME/Widgets  "
	inst.TargetType = TargetRepo
	inst.APIBaseURL = "https://ghe.example.com/api/v3"
	inst.UploadBaseURL = "https://ghe.example.com/api/uploads"
	inst.PrivateKeyEnc = []byte("sealed-key")
	inst.WebhookSecretEnc = []byte("sealed-secret")
	inst.AppSlug = "zoomies"
	inst.LastCheckedAt = &checked
	inst.LastError = "bad credentials"
	if err := s.UpdateInstallation(ctx, inst); err != nil {
		t.Fatalf("UpdateInstallation: %v", err)
	}

	got, err := s.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if got.Target != NormalizeTarget("ACME/Widgets") {
		t.Fatalf("target = %q, want it normalised", got.Target)
	}
	if got.TargetType != TargetRepo || got.AppSlug != "zoomies" {
		t.Fatalf("installation round-tripped wrong: %+v", got)
	}
	if string(got.PrivateKeyEnc) != "sealed-key" || string(got.WebhookSecretEnc) != "sealed-secret" {
		t.Fatal("sealed credentials did not round-trip")
	}
	if got.LastCheckedAt == nil || !got.LastCheckedAt.Equal(checked) {
		t.Fatalf("last_checked_at = %v, want %v", got.LastCheckedAt, checked)
	}
	if got.Healthy() {
		t.Fatal("an installation with a last error reported itself healthy")
	}

	err = s.UpdateInstallation(ctx, &Installation{ID: "inst_missing", Target: "x", TargetType: TargetOrg})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateInstallation on a missing row = %v, want ErrNotFound", err)
	}
}

// The prober writes the outcome of every credential check, and an empty message
// is how an installation gets well again -- otherwise a transient 401 would mark
// it broken forever.
func TestSetInstallationHealthClearsTheErrorWhenTheProbeSucceeds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	inst := &Installation{AppID: 1, InstallationID: 2, Target: "acme", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}

	if err := s.SetInstallationHealth(ctx, inst.ID, "bad credentials"); err != nil {
		t.Fatalf("SetInstallationHealth: %v", err)
	}
	got, err := s.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if got.LastError != "bad credentials" || got.Healthy() {
		t.Fatalf("installation = %+v, want an unhealthy one", got)
	}
	if got.LastCheckedAt == nil {
		t.Fatal("SetInstallationHealth did not record when the probe ran")
	}

	if err := s.SetInstallationHealth(ctx, inst.ID, ""); err != nil {
		t.Fatalf("SetInstallationHealth: %v", err)
	}
	got, err = s.GetInstallation(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if !got.Healthy() {
		t.Fatalf("a successful probe left the installation unhealthy: %+v", got)
	}
}

func TestGetPoolByNameAndListPools(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	inst, pool, _ := seedPool(t, s)

	got, err := s.GetPoolByName(ctx, pool.Name)
	if err != nil {
		t.Fatalf("GetPoolByName: %v", err)
	}
	if got.ID != pool.ID {
		t.Fatalf("GetPoolByName returned %s, want %s", got.ID, pool.ID)
	}
	if _, err := s.GetPoolByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPoolByName error = %v, want ErrNotFound", err)
	}

	second := &Pool{Name: "aaa-first", InstallationID: inst.ID, Backend: BackendDocker,
		MaxRunners: 1, DockerMode: DockerNone, Enabled: true}
	if err := s.CreatePool(ctx, second); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	pools, err := s.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("ListPools returned %d pools, want 2", len(pools))
	}
	if pools[0].Name >= pools[1].Name {
		t.Fatalf("ListPools is not ordered by name: %q then %q", pools[0].Name, pools[1].Name)
	}
}

// A pool carried over from a build that did not brand names gains the prefix
// the next time it is edited, so the name a pool is known by stays one thing.
func TestUpdatePoolBrandsTheNameAndNormalisesLabels(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)

	pool.Labels = StringSlice{"  Linux-X64  ", "linux-x64"}
	pool.MaxRunners = 9
	pool.Priority = 3
	cost := 0.25
	pool.CostPerRunnerHour = &cost
	pool.RepositoryScaleUpLimit = 2
	pool.CPUBurst = CPUBurstPolicy{Mode: CPUBurstAutomatic, MaxCPUs: 6}
	if err := s.UpdatePool(ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}

	got, err := s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got.Name != BrandedName(got.Name) {
		t.Fatalf("UpdatePool left the name unbranded: %q", got.Name)
	}
	if got.MaxRunners != 9 || got.Priority != 3 || got.RepositoryScaleUpLimit != 2 {
		t.Fatalf("pool round-tripped wrong: %+v", got)
	}
	if got.CostPerRunnerHour == nil || *got.CostPerRunnerHour != 0.25 {
		t.Fatalf("cost_per_runner_hour = %v, want 0.25", got.CostPerRunnerHour)
	}
	if got.CPUBurst.Mode != CPUBurstAutomatic || got.CPUBurst.MaxCPUs != 6 {
		t.Fatalf("cpu burst policy = %+v, want automatic with a 6 CPU ceiling", got.CPUBurst)
	}
	// A pull policy the caller left empty is filled in rather than stored blank,
	// because an unset policy at pull time is a runner that never starts.
	if got.PullPolicy == "" {
		t.Fatal("UpdatePool stored an empty pull policy")
	}

	err = s.UpdatePool(ctx, &Pool{ID: "pool_missing", Name: "ghost", Backend: BackendDocker})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdatePool on a missing row = %v, want ErrNotFound", err)
	}
}

// A pool that has never heard of tmpfs must read back as nothing in memory, and
// one that opted in must read back exactly what it chose: the settings are a
// JSON column, so a field dropped from the round trip would turn the feature
// off, or change a size, without anybody having touched the pool.
func TestAPoolKeepsItsTmpfsSettings(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, _ := seedPool(t, s)

	got, err := s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got.Tmpfs.Any() {
		t.Fatalf("a new pool keeps %+v in memory, want nothing", got.Tmpfs)
	}

	pool.Tmpfs = TmpfsConfig{
		Work: TmpfsMount{Enabled: true, SizeMB: 6144},
		Tmp:  TmpfsMount{Enabled: true},
	}
	if err := s.UpdatePool(ctx, pool); err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	got, err = s.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if got.Tmpfs != pool.Tmpfs {
		t.Fatalf("tmpfs = %+v, want %+v", got.Tmpfs, pool.Tmpfs)
	}
}

// A prewarm row is per pool and per host, and a second report from the same
// host is the same row moving on -- not a second row that would make the UI
// show one host twice.
func TestSetPoolPrewarmUpsertsPerHost(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	if err := s.SetPoolPrewarm(ctx, pool.ID, host.ID, "img:1", "pending", "", ""); err != nil {
		t.Fatalf("SetPoolPrewarm: %v", err)
	}
	if err := s.SetPoolPrewarm(ctx, pool.ID, host.ID, "img:1", "succeeded", "sha256:abc", ""); err != nil {
		t.Fatalf("SetPoolPrewarm: %v", err)
	}

	got, err := s.ListPoolPrewarms(ctx, pool.ID)
	if err != nil {
		t.Fatalf("ListPoolPrewarms: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListPoolPrewarms returned %d rows, want 1", len(got))
	}
	if got[0].State != "succeeded" || got[0].Digest != "sha256:abc" {
		t.Fatalf("prewarm row = %+v, want the second report", got[0])
	}
	if got[0].HostName != host.Name {
		t.Fatalf("host name = %q, want %q", got[0].HostName, host.Name)
	}
	if got[0].UpdatedAt.IsZero() {
		t.Fatal("prewarm row has no updated_at")
	}

	// A pool nobody has prewarmed lists nothing rather than erroring.
	if rows, err := s.ListPoolPrewarms(ctx, "pool_missing"); err != nil || len(rows) != 0 {
		t.Fatalf("ListPoolPrewarms on an unknown pool = %v, %v", rows, err)
	}
}

func TestSetRunnerImageDigestRecordsWhatWasActuallyPulled(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerProvisioning}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	if err := s.SetRunnerImageDigest(ctx, r.ID, "sha256:abc"); err != nil {
		t.Fatalf("SetRunnerImageDigest: %v", err)
	}
	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if got.ImageDigest != "sha256:abc" {
		t.Fatalf("image digest = %q, want sha256:abc", got.ImageDigest)
	}
}

// The utilisation bar is busy over live, and a failed runner is not live: it
// holds no slot and is waiting to be tidied away, so counting it would make a
// fleet look busier than it is.
func TestPoolCountsExcludeFailuresFromLive(t *testing.T) {
	c := PoolCounts{Provisioning: 1, Registering: 1, Idle: 2, Busy: 4, Draining: 2, Failed: 3}
	if got := c.Live(); got != 10 {
		t.Fatalf("Live() = %d, want 10", got)
	}
	if got := c.Total(); got != 13 {
		t.Fatalf("Total() = %d, want 13", got)
	}
	if got := c.Utilisation(); got != 0.4 {
		t.Fatalf("Utilisation() = %v, want 0.4", got)
	}
	// An empty pool has no utilisation rather than a division by zero.
	if got := (PoolCounts{Failed: 2}).Utilisation(); got != 0 {
		t.Fatalf("Utilisation() of a pool with nothing live = %v, want 0", got)
	}
}

func TestGetHostByNameCarriesLiveRunnerCounts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	for _, st := range []RunnerState{RunnerIdle, RunnerBusy, RunnerRemoved, RunnerFailed} {
		if err := s.CreateRunner(ctx, &Runner{PoolID: pool.ID, HostID: host.ID,
			Name: "r-" + string(st), State: st}); err != nil {
			t.Fatalf("CreateRunner(%s): %v", st, err)
		}
	}

	got, err := s.GetHostByName(ctx, host.Name)
	if err != nil {
		t.Fatalf("GetHostByName: %v", err)
	}
	if got.ID != host.ID {
		t.Fatalf("GetHostByName returned %s, want %s", got.ID, host.ID)
	}
	// Removed and failed runners hold no slot, so they must not be counted.
	if got.ActiveRunners != 2 {
		t.Fatalf("ActiveRunners = %d, want 2", got.ActiveRunners)
	}

	if _, err := s.GetHostByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetHostByName error = %v, want ErrNotFound", err)
	}
}

// The embedded host is the one the controller runs itself, and an operator
// looking at the hosts page expects to find it first rather than wherever its
// name happens to sort.
func TestListHostsPutsTheEmbeddedHostFirst(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, embedded := seedPool(t, s)

	remote := &Host{Name: "aaa-remote", Capacity: 2, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(ctx, remote); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if err := s.CreateRunner(ctx, &Runner{PoolID: pool.ID, HostID: remote.ID,
		Name: "r-1", State: RunnerIdle}); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	hosts, err := s.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("ListHosts returned %d hosts, want 2", len(hosts))
	}
	if hosts[0].ID != embedded.ID {
		t.Fatalf("ListHosts put %q first, want the embedded host", hosts[0].Name)
	}
	if hosts[1].ActiveRunners != 1 {
		t.Fatalf("the remote host's ActiveRunners = %d, want 1", hosts[1].ActiveRunners)
	}
}

// Agent requests authenticate on a hashed token. A host with no token must not
// be matched by an empty hash, or an unauthenticated request would be answered
// as whichever host happens to have no credential yet.
func TestFindHostByTokenHashIgnoresHostsWithoutOne(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	bare := &Host{Name: "bare", Capacity: 1}
	if err := s.CreateHost(ctx, bare); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	joined := &Host{Name: "joined", Capacity: 1, TokenHash: "hash-1"}
	if err := s.CreateHost(ctx, joined); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	if _, err := s.FindHostByTokenHash(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an empty hash matched a host: %v", err)
	}
	got, err := s.FindHostByTokenHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("FindHostByTokenHash: %v", err)
	}
	if got.ID != joined.ID {
		t.Fatalf("got %s, want %s", got.ID, joined.ID)
	}
	if _, err := s.FindHostByTokenHash(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown hash = %v, want ErrNotFound", err)
	}
}

// A protocol change should write only the protocol. Folding it into UpdateHost
// would make it carry every other figure the heartbeat brought along, including
// the free-disk drift the tolerance exists to ignore.
func TestSetHostProtocolWritesOnlyTheProtocol(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _, host := seedPool(t, s)

	if err := s.SetHostProtocol(ctx, host.ID, 3, true); err != nil {
		t.Fatalf("SetHostProtocol: %v", err)
	}
	got, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if got.ProtocolVersion != 3 || !got.Incompatible {
		t.Fatalf("host = %+v, want protocol 3 and incompatible", got)
	}
	if got.Capacity != host.Capacity || got.Name != host.Name {
		t.Fatalf("SetHostProtocol disturbed the rest of the row: %+v", got)
	}

	if err := s.SetHostProtocol(ctx, "hst_missing", 1, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetHostProtocol on a missing row = %v, want ErrNotFound", err)
	}
}

func TestHeartbeatAndCordonReportAMissingHost(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2025, 4, 4, 4, 4, 4, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	_, _, host := seedPool(t, s)

	if err := s.Heartbeat(ctx, host.ID, now); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got, err := s.GetHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !got.LastHeartbeat.Equal(now) {
		t.Fatalf("last_heartbeat = %v, want %v", got.LastHeartbeat, now)
	}

	if err := s.SetHostCordoned(ctx, host.ID, true); err != nil {
		t.Fatalf("SetHostCordoned: %v", err)
	}
	if got, err := s.GetHost(ctx, host.ID); err != nil || !got.Cordoned {
		t.Fatalf("host not cordoned: %v, %v", got, err)
	}
	if err := s.SetHostCordoned(ctx, host.ID, false); err != nil {
		t.Fatalf("SetHostCordoned: %v", err)
	}
	if got, err := s.GetHost(ctx, host.ID); err != nil || got.Cordoned {
		t.Fatalf("host still cordoned: %v, %v", got, err)
	}

	if err := s.Heartbeat(ctx, "hst_missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Heartbeat on a missing row = %v, want ErrNotFound", err)
	}
	if err := s.SetHostCordoned(ctx, "hst_missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetHostCordoned on a missing row = %v, want ErrNotFound", err)
	}
}

// The transport a host actually reached the controller on is a fixed vocabulary,
// so a typo is refused rather than written and rendered as an unknown badge.
func TestSetHostConnectionRefusesAnUnknownTransport(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _, host := seedPool(t, s)

	if err := s.SetHostConnection(ctx, host.ID, "carrier-pigeon"); !errors.Is(err, ErrConflict) {
		t.Fatalf("SetHostConnection error = %v, want ErrConflict", err)
	}
	for _, conn := range []string{"direct", "tailcat"} {
		if err := s.SetHostConnection(ctx, host.ID, conn); err != nil {
			t.Fatalf("SetHostConnection(%s): %v", conn, err)
		}
		got, err := s.GetHost(ctx, host.ID)
		if err != nil {
			t.Fatalf("GetHost: %v", err)
		}
		if got.Connection != conn {
			t.Fatalf("connection = %q, want %q", got.Connection, conn)
		}
	}
	if err := s.SetHostConnection(ctx, "hst_missing", "direct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetHostConnection on a missing row = %v, want ErrNotFound", err)
	}
}

func TestGetRunnerByNameFindsTheRunnerGitHubKnows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "zoomies-abc123", State: RunnerIdle}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	got, err := s.GetRunnerByName(ctx, "zoomies-abc123")
	if err != nil {
		t.Fatalf("GetRunnerByName: %v", err)
	}
	if got.ID != r.ID {
		t.Fatalf("got %s, want %s", got.ID, r.ID)
	}
	if _, err := s.GetRunnerByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRunnerByName error = %v, want ErrNotFound", err)
	}
}

// The scheduler reads a pool's runners on every reconcile and must see
// everything that still holds a slot -- but a removed row holds none, and
// carrying thousands of them would make each pass slower for no decision.
func TestListRunnersForPoolSkipsRemovedRows(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	for _, st := range []RunnerState{RunnerIdle, RunnerBusy, RunnerFailed, RunnerRemoved} {
		if err := s.CreateRunner(ctx, &Runner{PoolID: pool.ID, HostID: host.ID,
			Name: "r-" + string(st), State: st}); err != nil {
			t.Fatalf("CreateRunner(%s): %v", st, err)
		}
	}

	got, err := s.ListRunnersForPool(ctx, pool.ID)
	if err != nil {
		t.Fatalf("ListRunnersForPool: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListRunnersForPool returned %d runners, want 3", len(got))
	}
	for _, r := range got {
		if r.State == RunnerRemoved {
			t.Fatalf("a removed runner reached the scheduler: %+v", r)
		}
	}
}

func TestListRunnersForHostFiltersByStateWhenAsked(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	other := &Host{Name: "vm-2", Capacity: 1, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(ctx, other); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if err := s.CreateRunner(ctx, &Runner{PoolID: pool.ID, HostID: other.ID,
		Name: "elsewhere", State: RunnerIdle}); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	for _, st := range []RunnerState{RunnerIdle, RunnerBusy, RunnerRemoved} {
		if err := s.CreateRunner(ctx, &Runner{PoolID: pool.ID, HostID: host.ID,
			Name: "r-" + string(st), State: st}); err != nil {
			t.Fatalf("CreateRunner(%s): %v", st, err)
		}
	}

	// With no states named, the agent gets everything it is still responsible
	// for -- which is everything that has not been removed.
	all, err := s.ListRunnersForHost(ctx, host.ID)
	if err != nil {
		t.Fatalf("ListRunnersForHost: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListRunnersForHost returned %d runners, want 2", len(all))
	}

	busy, err := s.ListRunnersForHost(ctx, host.ID, RunnerBusy)
	if err != nil {
		t.Fatalf("ListRunnersForHost: %v", err)
	}
	if len(busy) != 1 || busy[0].State != RunnerBusy {
		t.Fatalf("state filter returned %+v", busy)
	}

	// Naming a terminal state explicitly is how a caller asks for one.
	removed, err := s.ListRunnersForHost(ctx, host.ID, RunnerRemoved)
	if err != nil {
		t.Fatalf("ListRunnersForHost: %v", err)
	}
	if len(removed) != 1 {
		t.Fatalf("ListRunnersForHost(removed) returned %d runners, want 1", len(removed))
	}
}

func TestSetRunnerGitHubIDContainerAndUsage(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerRegistering}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	if err := s.SetRunnerGitHubID(ctx, r.ID, 4242); err != nil {
		t.Fatalf("SetRunnerGitHubID: %v", err)
	}
	if err := s.SetRunnerContainer(ctx, r.ID, "ctr-abc"); err != nil {
		t.Fatalf("SetRunnerContainer: %v", err)
	}
	if err := s.SetRunnerResourceUsage(ctx, r.ID, 42.5, 1<<30); err != nil {
		t.Fatalf("SetRunnerResourceUsage: %v", err)
	}

	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if got.GitHubRunnerID != 4242 || got.ContainerID != "ctr-abc" {
		t.Fatalf("runner = %+v", got)
	}
	if got.CPUPercent != 42.5 || got.MemoryBytes != 1<<30 {
		t.Fatalf("resource sample = %v, %v", got.CPUPercent, got.MemoryBytes)
	}
}

func TestSetRunnerStartupRecordsTimingsWithoutInventingAPull(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerProvisioning}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	// A backend that cannot report a pull passes nil, and the column stays
	// empty rather than claiming the pull took no time at all.
	// The column is milliseconds, so the fixture is a whole one.
	started := r.CreatedAt.Truncate(time.Millisecond).Add(2 * time.Second)
	if err := s.SetRunnerStartup(ctx, r.ID, nil, &started); err != nil {
		t.Fatalf("SetRunnerStartup: %v", err)
	}
	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if got.ImagePullDuration != nil {
		t.Fatalf("image pull = %v, want nil", got.ImagePullDuration)
	}
	if got.ContainerStartedAt == nil || !got.ContainerStartedAt.Equal(started) {
		t.Fatalf("container_started_at = %v, want %v", got.ContainerStartedAt, started)
	}

	pull := 3 * time.Second
	if err := s.SetRunnerStartup(ctx, r.ID, &pull, &started); err != nil {
		t.Fatalf("SetRunnerStartup: %v", err)
	}
	got, err = s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if got.ImagePullDuration == nil || *got.ImagePullDuration != pull {
		t.Fatalf("image pull = %v, want %v", got.ImagePullDuration, pull)
	}
}

// Creation time is otherwise immutable, because a row whose age can be edited
// is a row whose age cannot be reasoned about. The demo seeder is the one
// caller, and its fixtures would otherwise age into a fleet reporting problems
// it does not have.
func TestSetRunnerCreatedAtMovesAFixturesAge(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerIdle}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	fresh := time.Date(2025, 7, 7, 7, 7, 7, 0, time.UTC)
	if err := s.SetRunnerCreatedAt(ctx, r.ID, fresh); err != nil {
		t.Fatalf("SetRunnerCreatedAt: %v", err)
	}
	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if !got.CreatedAt.Equal(fresh) {
		t.Fatalf("created_at = %v, want %v", got.CreatedAt, fresh)
	}
}

// The first delivery is what the queue-wait figures are measured from, so a
// redelivery must not move it -- otherwise a task retried twice looks like one
// issued moments ago.
func TestSetRunnerCreateTaskIssuedKeepsTheFirstDelivery(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerProvisioning}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	first := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.SetRunnerCreateTaskIssued(ctx, r.ID, first); err != nil {
		t.Fatalf("SetRunnerCreateTaskIssued: %v", err)
	}
	if err := s.SetRunnerCreateTaskIssued(ctx, r.ID, first.Add(time.Hour)); err != nil {
		t.Fatalf("SetRunnerCreateTaskIssued: %v", err)
	}
	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if got.CreateTaskIssuedAt == nil || !got.CreateTaskIssuedAt.Equal(first) {
		t.Fatalf("create_task_issued_at = %v, want %v", got.CreateTaskIssuedAt, first)
	}

	// The lifecycle task's own stamp is the opposite: it records every
	// redelivery, because it is what a restarted controller reasons from.
	later := first.Add(time.Hour)
	if err := s.SetRunnerTaskIssued(ctx, r.ID, first); err != nil {
		t.Fatalf("SetRunnerTaskIssued: %v", err)
	}
	if err := s.SetRunnerTaskIssued(ctx, r.ID, later); err != nil {
		t.Fatalf("SetRunnerTaskIssued: %v", err)
	}
	got, err = s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if got.TaskIssuedAt == nil || !got.TaskIssuedAt.Equal(later) {
		t.Fatalf("task_issued_at = %v, want %v", got.TaskIssuedAt, later)
	}
}

// The problems pass names the rows still complaining without reading the whole
// table, and it wants the newest failure first so the freshest trouble is at
// the top of the list.
func TestRunnersWithFailedCleanupReturnsNewestFailureFirst(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	_, pool, host := seedPool(t, s)

	clean := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "clean", State: RunnerRemoved}
	early := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "early", State: RunnerRemoved}
	late := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "late", State: RunnerRemoved}
	for _, r := range []*Runner{clean, early, late} {
		if err := s.CreateRunner(ctx, r); err != nil {
			t.Fatalf("CreateRunner(%s): %v", r.Name, err)
		}
	}

	if err := s.RecordCleanupFailure(ctx, early.ID, "container would not die"); err != nil {
		t.Fatalf("RecordCleanupFailure: %v", err)
	}
	now = now.Add(time.Minute)
	if err := s.RecordCleanupFailure(ctx, late.ID, ""); err != nil {
		t.Fatalf("RecordCleanupFailure: %v", err)
	}

	got, err := s.RunnersWithFailedCleanup(ctx)
	if err != nil {
		t.Fatalf("RunnersWithFailedCleanup: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("RunnersWithFailedCleanup returned %d rows, want 2", len(got))
	}
	if got[0].Name != "late" {
		t.Fatalf("newest failure is %q, want late", got[0].Name)
	}
	// A failure with no reason still says something, because "cleanup failed"
	// with an empty message is not something an operator can act on.
	if got[0].CleanupError == "" {
		t.Fatal("a cleanup failure was recorded without a reason")
	}
	if got[0].CleanupAttempts != 1 {
		t.Fatalf("cleanup attempts = %d, want 1", got[0].CleanupAttempts)
	}
}

// Neither half of teardown can settle the other: the host's complaint and the
// GitHub deletion's are separate columns joined into one message, so an
// operator sees both rather than whichever failed last.
func TestCleanupFailuresFromBothSidesAreKeptTogether(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerRemoved}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}

	if err := s.RecordCleanupFailure(ctx, r.ID, "container would not die"); err != nil {
		t.Fatalf("RecordCleanupFailure: %v", err)
	}
	if err := s.RecordRegistrationCleanupFailure(ctx, r.ID, ""); err != nil {
		t.Fatalf("RecordRegistrationCleanupFailure: %v", err)
	}

	got, err := s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if got.CleanupAttempts != 2 {
		t.Fatalf("cleanup attempts = %d, want 2", got.CleanupAttempts)
	}
	if got.CleanupError == "container would not die" {
		t.Fatal("the GitHub deletion's complaint replaced the host's")
	}
	if got.CleanupError == "" {
		t.Fatal("no complaint survived")
	}

	// Settling the host's side leaves the GitHub deletion visible until its own
	// retry succeeds, and keeps the attempt count as evidence.
	if err := s.ClearCleanupFailure(ctx, r.ID); err != nil {
		t.Fatalf("ClearCleanupFailure: %v", err)
	}
	got, err = s.GetRunner(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetRunner: %v", err)
	}
	if got.CleanupError == "" {
		t.Fatal("clearing the host's complaint also cleared GitHub's")
	}
	if got.CleanupAttempts != 2 {
		t.Fatalf("cleanup attempts = %d after clearing, want 2", got.CleanupAttempts)
	}
}

// GitHub can deliver a completion twice, and for a job the runner is not on.
// The job ID guard is what makes both harmless.
func TestCompleteRunnerJobIgnoresACompletionForAnotherJob(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerIdle, Ephemeral: false}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerBusy, ""); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}
	if err := s.AssignRunnerJob(ctx, r.ID, "job_1"); err != nil {
		t.Fatalf("AssignRunnerJob: %v", err)
	}

	got, changed, err := s.CompleteRunnerJob(ctx, r.ID, "job_other", "done")
	if err != nil {
		t.Fatalf("CompleteRunnerJob: %v", err)
	}
	if changed {
		t.Fatal("a completion for another job released the runner")
	}
	if got.State != RunnerBusy || got.CurrentJobID != "job_1" {
		t.Fatalf("runner = %+v, want it still busy on job_1", got)
	}

	// The real completion returns a persistent runner to idle and counts the job.
	got, changed, err = s.CompleteRunnerJob(ctx, r.ID, "job_1", "finished")
	if err != nil {
		t.Fatalf("CompleteRunnerJob: %v", err)
	}
	if !changed || got.State != RunnerIdle || got.JobsHandled != 1 || got.CurrentJobID != "" {
		t.Fatalf("runner = %+v, want an idle runner with one job handled", got)
	}
	if got.LastIdleAt == nil {
		t.Fatal("returning to idle did not record when")
	}

	// A duplicate delivery of the same completion changes nothing.
	_, changed, err = s.CompleteRunnerJob(ctx, r.ID, "job_1", "finished")
	if err != nil {
		t.Fatalf("CompleteRunnerJob: %v", err)
	}
	if changed {
		t.Fatal("a duplicate completion released the runner a second time")
	}

	if _, _, err := s.CompleteRunnerJob(ctx, "run_missing", "job_1", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CompleteRunnerJob on a missing runner = %v, want ErrNotFound", err)
	}
}

// An ephemeral runner is finished by its job rather than returned to the pool,
// so a missed workload-exit report cannot leave one busy forever.
func TestCompleteRunnerJobFinishesAnEphemeralRunner(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerIdle, Ephemeral: true}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	if _, err := s.TransitionRunner(ctx, r.ID, RunnerBusy, ""); err != nil {
		t.Fatalf("TransitionRunner: %v", err)
	}
	if err := s.AssignRunnerJob(ctx, r.ID, "job_1"); err != nil {
		t.Fatalf("AssignRunnerJob: %v", err)
	}

	got, changed, err := s.CompleteRunnerJob(ctx, r.ID, "job_1", "finished")
	if err != nil {
		t.Fatalf("CompleteRunnerJob: %v", err)
	}
	if !changed || got.State != RunnerRemoved {
		t.Fatalf("runner = %+v, want a removed ephemeral runner", got)
	}
	if got.FinishedAt == nil {
		t.Fatal("an ephemeral runner was finished without recording when")
	}
}

// Hard deletion is for the UI's explicit action and for pruning history; normal
// teardown transitions to "removed" instead.
func TestDeleteRunnerRemovesTheRow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, pool, host := seedPool(t, s)

	r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-1", State: RunnerRemoved}
	if err := s.CreateRunner(ctx, r); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	if err := s.DeleteRunner(ctx, r.ID); err != nil {
		t.Fatalf("DeleteRunner: %v", err)
	}
	if _, err := s.GetRunner(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted runner still resolves: %v", err)
	}
	if err := s.DeleteRunner(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteRunner on a missing row = %v, want ErrNotFound", err)
	}
}

// A stale bookmarked URL should not break the page, so an unknown sort column
// falls back rather than erroring.
func TestPageOrderByFallsBackForAnUnknownColumn(t *testing.T) {
	allowed := map[string]string{"name": "name", "state": "state"}

	if got := (Page{Sort: "nonsense"}).orderBy(allowed, "created_at DESC"); got != "created_at DESC" {
		t.Fatalf("orderBy = %q, want the fallback", got)
	}
	if got := (Page{Sort: "name"}).orderBy(allowed, "created_at DESC"); got != "name ASC" {
		t.Fatalf("orderBy = %q, want name ASC", got)
	}
	if got := (Page{Sort: "state", Desc: true}).orderBy(allowed, "created_at DESC"); got != "state DESC" {
		t.Fatalf("orderBy = %q, want state DESC", got)
	}
}

func TestPageLimitClampsToTheMaximum(t *testing.T) {
	if got := (Page{}).limit(50, 500); got != 50 {
		t.Fatalf("limit = %d, want the default 50", got)
	}
	if got := (Page{Limit: 10}).limit(50, 500); got != 10 {
		t.Fatalf("limit = %d, want 10", got)
	}
	if got := (Page{Limit: 10000}).limit(50, 500); got != 500 {
		t.Fatalf("limit = %d, want the cap 500", got)
	}
}

// The scheduler plans against a snapshot and applies against live rows, so the
// check that a runner is still not working has to be part of the write itself:
// a caller's earlier read can be stale by the time it writes. The conditional
// transitions allow nothing the state machine does not, and refuse, as an
// invalid transition, a runner that has taken a job in the meantime.
func TestConditionalRunnerTransitionsRefuseARunnerThatIsNowWorking(t *testing.T) {
	ctx := context.Background()

	transitions := map[string]func(*Store, string) (*Runner, error){
		"drain": func(s *Store, id string) (*Runner, error) {
			return s.TransitionRunnerUnlessBusy(ctx, id, RunnerDraining, "planned")
		},
		"fail": func(s *Store, id string) (*Runner, error) {
			return s.FailRunnerUnlessBusy(ctx, id, "planned", FaultRunnerExited)
		},
	}
	want := map[string]RunnerState{"drain": RunnerDraining, "fail": RunnerFailed}

	for name, transition := range transitions {
		t.Run(name+" goes through for a runner with no job", func(t *testing.T) {
			s := newTestStore(t)
			_, pool, host := seedPool(t, s)
			r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-idle", State: RunnerIdle}
			if err := s.CreateRunner(ctx, r); err != nil {
				t.Fatalf("CreateRunner: %v", err)
			}
			got, err := transition(s, r.ID)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if got.State != want[name] {
				t.Errorf("state = %s, want %s", got.State, want[name])
			}
		})

		t.Run(name+" is refused for a runner that has taken a job", func(t *testing.T) {
			s := newTestStore(t)
			_, pool, host := seedPool(t, s)
			r := &Runner{PoolID: pool.ID, HostID: host.ID, Name: "r-busy", State: RunnerRegistering}
			if err := s.CreateRunner(ctx, r); err != nil {
				t.Fatalf("CreateRunner: %v", err)
			}
			if _, _, err := s.StartRunnerJob(ctx, r.ID, "job_1", ""); err != nil {
				t.Fatalf("StartRunnerJob: %v", err)
			}
			if _, err := transition(s, r.ID); !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("%s on a busy runner = %v, want ErrInvalidTransition", name, err)
			}
			after, err := s.GetRunner(ctx, r.ID)
			if err != nil {
				t.Fatalf("GetRunner: %v", err)
			}
			if after.State != RunnerBusy || after.CurrentJobID != "job_1" {
				t.Errorf("runner = %s with job %q, want it left busy on job_1", after.State, after.CurrentJobID)
			}
		})
	}
}
