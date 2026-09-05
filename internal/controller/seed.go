package controller

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

// Identifiers for the demo fixtures. They are fixed rather than random so that
// seeding twice is a no-op, a Playwright test can navigate straight to
// /pools/pool_demolinux, and a screenshot taken today matches one taken last
// week.
const (
	demoInstallationID = "ins_demoacme"
	demoPoolLinuxID    = "pool_demolinux"
	demoPoolArmID      = "pool_demoarm"
	demoHostPrefix     = "host_demo"
	demoTarget         = "acme"
)

// IsDemoID reports whether an identifier belongs to the seeded demo fixtures.
//
// The fixtures have no GitHub behind them, so the two places that would
// otherwise reach out on their behalf -- the credential prober and the
// registration reaper -- check this and skip. Without it a demo or a UI test
// run fills the problems drawer with "this installation is not usable" and the
// log with parse failures, none of which says anything about the fleet.
func IsDemoID(id string) bool {
	_, rest, ok := strings.Cut(id, "_")
	return ok && strings.HasPrefix(rest, "demo")
}

// demoPoolNames is what "is this instance already seeded?" is decided on, and
// also what stops seeding from touching a real deployment. The first two are
// what seeding writes now; the unbranded pair is what it wrote before pool
// names carried the brand, and is still recognised so that a demo instance
// seeded by an older build is left alone rather than rejected as a real
// fleet.
// demoRepos are the repositories the fixture's jobs come from, and the ones the
// demo installation reports to the migration wizard.
var demoRepos = []string{"acme/widgets", "acme/api", "acme/site"}

var demoPoolNames = []string{
	"zoomies-demo-linux-x64", "zoomies-demo-linux-arm64",
	"demo-linux-x64", "demo-linux-arm64",
}

// SeedDemo writes a deterministic fixture fleet: one installation, two pools, a
// dozen runners spread across the state machine, fifty jobs with plausible
// queue waits and outcomes, three hosts, some scaling history and an audit
// trail. It is what ZOOMIES_SEED_DEMO turns on for the Playwright suite and
// for a demo instance.
//
// It is idempotent -- a second call does nothing -- and it refuses to run at
// all if this instance has any pool that is not one of its own, because a
// fixture fleet appearing in a real deployment would be indistinguishable from
// a compromise.
//
// The installation it creates carries a dummy private key. Nothing ever calls
// GitHub with it: the fixtures are written straight to the database.
func (c *Controller) SeedDemo(ctx context.Context) error {
	pools, err := c.st.ListPools(ctx)
	if err != nil {
		return fmt.Errorf("checking whether this instance is empty: %w", err)
	}
	seeded := false
	for _, p := range pools {
		if slices.Contains(demoPoolNames, p.Name) {
			seeded = true
			continue
		}
		return fmt.Errorf("refusing to seed demo data: this instance already has the pool %q, "+
			"and demo fixtures must never appear in a real fleet; unset %s", p.Name, SeedEnvVar)
	}
	if seeded {
		c.log.Debug("demo fixtures are already present")
		return nil
	}

	// Everything is placed relative to one instant so the fixture reads as a
	// fleet that has been busy this morning, whenever "this morning" is.
	now := c.Now()
	// A fixed seed keeps the job mix, the durations and the outcomes identical
	// between runs, which is what lets a test assert on a count.
	rng := rand.New(rand.NewPCG(20240301, 42))

	if err := c.seedInstallation(ctx); err != nil {
		return err
	}
	hosts, err := c.seedHosts(ctx, now)
	if err != nil {
		return err
	}
	pool1, pool2, err := c.seedPools(ctx)
	if err != nil {
		return err
	}
	runners, err := c.seedRunners(ctx, now, []*store.Pool{pool1, pool2}, hosts)
	if err != nil {
		return err
	}
	if err := c.seedJobs(ctx, now, rng, []*store.Pool{pool1, pool2}, runners); err != nil {
		return err
	}
	if err := c.seedScaling(ctx, now, pool1, pool2); err != nil {
		return err
	}
	if err := c.seedAudit(ctx, now, pool1); err != nil {
		return err
	}
	if err := c.seedSamples(ctx, now, rng); err != nil {
		return err
	}

	c.log.Info("seeded the demo fleet",
		"pools", len(demoPoolNames), "hosts", len(hosts), "runners", len(runners))
	return nil
}

func (c *Controller) seedInstallation(ctx context.Context) error {
	if _, err := c.st.GetInstallation(ctx, demoInstallationID); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	// A syntactically plausible but useless key. It is sealed like any other so
	// that the Installations page renders the same code path as a real one.
	key, err := c.key.SealString("-----BEGIN RSA PRIVATE KEY-----\nDEMO FIXTURE, NOT A KEY\n-----END RSA PRIVATE KEY-----\n")
	if err != nil {
		return fmt.Errorf("sealing the demo private key: %w", err)
	}
	secret, err := c.key.SealString("demo-webhook-secret")
	if err != nil {
		return fmt.Errorf("sealing the demo webhook secret: %w", err)
	}
	inst := &store.Installation{
		ID:               demoInstallationID,
		AppID:            123456,
		InstallationID:   7654321,
		Target:           demoTarget,
		TargetType:       store.TargetOrg,
		APIBaseURL:       "https://api.github.com",
		PrivateKeyEnc:    key,
		WebhookSecretEnc: secret,
		AppSlug:          "zoomies-demo",
	}
	return c.st.CreateInstallation(ctx, inst)
}

func (c *Controller) seedHosts(ctx context.Context, now time.Time) ([]*store.Host, error) {
	specs := []struct {
		id, name, arch string
		capacity       int
		embedded       bool
		cordoned       bool
		silentFor      time.Duration
	}{
		{demoHostPrefix + "a", "demo-builder-1", "amd64", 6, true, false, 0},
		{demoHostPrefix + "b", "demo-builder-2", "amd64", 4, false, false, 0},
		// One cordoned host, so the Hosts page and the problems drawer both
		// have something real to render.
		{demoHostPrefix + "c", "demo-arm-1", "arm64", 2, false, true, 0},
	}
	out := make([]*store.Host, 0, len(specs))
	for _, s := range specs {
		h := &store.Host{
			ID:       s.id,
			Name:     s.name,
			Address:  "10.0.0." + s.id[len(s.id)-1:],
			Embedded: s.embedded,
			Capacity: s.capacity,
			Backends: store.StringSlice{"docker"},
			BackendInfo: store.HostBackends{
				{Kind: store.BackendDocker, Available: true, Version: "27.1.1",
					Rootless: true, Endpoint: "unix:///run/user/1000/docker.sock", SupportsDinD: true},
				// The real probe's sentence, commands and all: the demo fleet is
				// what the UI is looked at with, so it has to show what an
				// operator actually gets when a backend is missing.
				{Kind: store.BackendPodman, Detail: "no socket at /run/user/1000/podman/podman.sock; " +
					"if Podman is installed, its API socket is off by default -- enable it with `systemctl --user enable --now podman.socket`"},
			},
			Labels:        store.StringMap{"arch": s.arch, "zone": "demo"},
			OS:            "linux",
			Arch:          s.arch,
			Version:       "demo",
			Cordoned:      s.cordoned,
			LastHeartbeat: now.Add(-s.silentFor),
		}
		if err := c.st.CreateHost(ctx, h); err != nil {
			return nil, fmt.Errorf("seeding host %s: %w", s.name, err)
		}
		out = append(out, h)
	}
	return out, nil
}

func (c *Controller) seedPools(ctx context.Context) (*store.Pool, *store.Pool, error) {
	linux := &store.Pool{
		ID:             demoPoolLinuxID,
		Name:           demoPoolNames[0],
		InstallationID: demoInstallationID,
		Labels:         store.StringSlice(store.BrandLabels([]string{"linux", "x64", "zoomies-demo-linux-x64"})),
		Backend:        store.BackendDocker,
		Image:          c.cfg.GitHub.RunnerImage,
		MinRunners:     1,
		MaxRunners:     8,
		IdleTimeout:    store.Duration(5 * time.Minute),
		Ephemeral:      true,
		DockerMode:     store.DockerNone,
		Resources:      store.Resources{CPUs: 2, MemoryMB: 4096},
		HostSelector:   store.StringMap{"arch": "amd64"},
		Enabled:        true,
	}
	arm := &store.Pool{
		ID:             demoPoolArmID,
		Name:           demoPoolNames[1],
		InstallationID: demoInstallationID,
		Labels:         store.StringSlice(store.BrandLabels([]string{"linux", "arm64", "zoomies-demo-linux-arm64"})),
		Backend:        store.BackendDocker,
		Image:          c.cfg.GitHub.RunnerImage,
		MinRunners:     0,
		MaxRunners:     4,
		IdleTimeout:    store.Duration(10 * time.Minute),
		// Persistent runners, so the problems drawer has a dangerous setting to
		// show and the UI's warning styling is exercised.
		Ephemeral:    false,
		DockerMode:   store.DockerDinD,
		HostSelector: store.StringMap{"arch": "arm64"},
		Enabled:      true,
	}
	if err := c.st.CreatePool(ctx, linux); err != nil {
		return nil, nil, fmt.Errorf("seeding pool %s: %w", linux.Name, err)
	}
	if err := c.st.CreatePool(ctx, arm); err != nil {
		return nil, nil, fmt.Errorf("seeding pool %s: %w", arm.Name, err)
	}
	return linux, arm, nil
}

// seedRunners writes a dozen runners spread over every state the UI renders
// differently, so each badge, each empty field and the failure message all
// have a fixture behind them.
func (c *Controller) seedRunners(ctx context.Context, now time.Time, pools []*store.Pool, hosts []*store.Host) ([]*store.Runner, error) {
	type spec struct {
		state   store.RunnerState
		pool    int
		host    int
		ageMin  int
		message string
		jobs    int
	}
	specs := []spec{
		{store.RunnerBusy, 0, 0, 12, "", 3},
		{store.RunnerBusy, 0, 0, 9, "", 1},
		{store.RunnerBusy, 0, 1, 7, "", 2},
		{store.RunnerIdle, 0, 0, 30, "", 5},
		{store.RunnerIdle, 0, 1, 24, "", 4},
		{store.RunnerIdle, 1, 2, 45, "", 9},
		{store.RunnerRegistering, 0, 1, 1, "", 0},
		{store.RunnerProvisioning, 0, 0, 0, "3 jobs queued > 30s", 0},
		{store.RunnerDraining, 0, 1, 60, "idle for 6m, over the 5m idle timeout", 6},
		{store.RunnerFailed, 0, 0, 20, "GitHub would not register zoomies-demo-linux-x64-f4k3: github: create jit config: 403 Forbidden", 0},
		{store.RunnerRemoved, 0, 0, 90, "ephemeral runner exited cleanly after its job", 1},
		{store.RunnerRemoved, 1, 2, 120, "runner exited cleanly", 2},
	}

	out := make([]*store.Runner, 0, len(specs))
	for i, s := range specs {
		pool := pools[s.pool]
		host := hosts[s.host]
		created := now.Add(-time.Duration(s.ageMin) * time.Minute)
		r := &store.Runner{
			ID:             fmt.Sprintf("run_demo%02d", i),
			PoolID:         pool.ID,
			HostID:         host.ID,
			Name:           fmt.Sprintf("%sdemo%04d", store.RunnerNamePrefix, i),
			State:          s.state,
			Ephemeral:      pool.Ephemeral,
			Labels:         pool.Labels,
			Image:          pool.Image,
			ContainerID:    fmt.Sprintf("demo%032d", i),
			Message:        s.message,
			JobsHandled:    s.jobs,
			CPUPercent:     float64((i*17)%90) + 1,
			MemoryBytes:    int64(256+i*64) << 20,
			GitHubRunnerID: int64(9000 + i),
		}
		if s.state != store.RunnerProvisioning {
			started := created.Add(20 * time.Second)
			r.StartedAt = &started
		}
		if s.state == store.RunnerIdle {
			idle := now.Add(-time.Duration(s.ageMin/2) * time.Minute)
			r.LastIdleAt = &idle
		}
		if s.state.Terminal() {
			finished := now.Add(-time.Duration(s.ageMin/3) * time.Minute)
			r.FinishedAt = &finished
		}
		if err := c.st.CreateRunner(ctx, r); err != nil {
			return nil, fmt.Errorf("seeding runner %s: %w", r.Name, err)
		}
		// The store stamps created_at itself, so every fixture runner shares
		// one creation instant. The spread the UI actually renders -- started,
		// last idle, finished -- is set above and does vary.
		out = append(out, r)
	}
	return out, nil
}

// seedJobs writes fifty jobs with queue waits and outcomes that look like a
// real morning: mostly quick and successful, a long tail that makes the p95
// worth showing, and a couple nothing claims.
func (c *Controller) seedJobs(ctx context.Context, now time.Time, rng *rand.Rand, pools []*store.Pool, runners []*store.Runner) error {
	repos := demoRepos
	workflows := []string{"CI", "Release", "Nightly"}
	jobNames := []string{"build", "test", "lint", "package"}
	conclusions := []string{"success", "success", "success", "success", "failure", "cancelled"}

	busy := make([]*store.Runner, 0, 4)
	for _, r := range runners {
		if r.State == store.RunnerBusy {
			busy = append(busy, r)
		}
	}

	for i := range 50 {
		pool := pools[i%len(pools)]
		queued := now.Add(-time.Duration(6*60-i*7) * time.Minute)
		j := &store.Job{
			ID:          fmt.Sprintf("job_demo%03d", i),
			GitHubJobID: int64(80000 + i),
			GitHubRunID: int64(40000 + i/2),
			Repo:        repos[i%len(repos)],
			Workflow:    workflows[i%len(workflows)],
			JobName:     jobNames[i%len(jobNames)],
			Labels:      pool.Labels,
			PoolID:      pool.ID,
			Matched:     true,
			QueuedAt:    queued,
			HTMLURL:     fmt.Sprintf("https://github.com/%s/actions/runs/%d", repos[i%len(repos)], 40000+i/2),
		}

		switch {
		case i < 44:
			// Finished. A tenth of them waited a long time, which is what the
			// p95 on the Overview is there to surface.
			wait := time.Duration(5+rng.IntN(40)) * time.Second
			if i%10 == 0 {
				wait = time.Duration(3+rng.IntN(6)) * time.Minute
			}
			started := queued.Add(wait)
			completed := started.Add(time.Duration(40+rng.IntN(600)) * time.Second)
			j.State = store.JobCompleted
			j.Conclusion = conclusions[i%len(conclusions)]
			j.StartedAt, j.CompletedAt = &started, &completed
			j.RunnerName = fmt.Sprintf("%sdemo%04d", store.RunnerNamePrefix, i%12)
		case i < 47 && len(busy) > 0:
			// Running right now, on one of the busy runners.
			r := busy[i%len(busy)]
			started := now.Add(-time.Duration(2+i%5) * time.Minute)
			j.State = store.JobInProgress
			j.StartedAt = &started
			j.RunnerID, j.RunnerName = r.ID, r.Name
		case i < 49:
			j.State = store.JobQueued
			j.QueuedAt = now.Add(-time.Duration(20+i) * time.Second)
		default:
			// One job nothing claims, so the problems drawer has its
			// "no pool wants this" entry.
			j.State = store.JobQueued
			j.QueuedAt = now.Add(-4 * time.Minute)
			j.Labels = store.StringSlice{"self-hosted", "linux", "gpu", "cuda12"}
			j.PoolID, j.Matched = "", false
		}

		if _, err := c.st.UpsertJob(ctx, j); err != nil {
			return fmt.Errorf("seeding job %d: %w", i, err)
		}
	}

	// Link the busy runners to the jobs they are running, so the Runners page
	// can show what each one is doing.
	for i, r := range busy {
		jobID := fmt.Sprintf("job_demo%03d", 44+i)
		if _, err := c.st.GetJob(ctx, jobID); err != nil {
			continue
		}
		if err := c.st.AssignRunnerJob(ctx, r.ID, jobID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) seedScaling(ctx context.Context, now time.Time, linux, arm *store.Pool) error {
	// The reason quotes the pool by name, exactly as the scheduler writes it,
	// so the fixture cannot drift from the pool it describes when the pools are
	// renamed.
	events := []struct {
		pool     *store.Pool
		from, to int
		why      string
		agoMin   int
	}{
		{linux, 1, 4, "3 jobs queued > 30s", 95},
		{linux, 4, 6, "2 jobs queued > 30s", 70},
		{linux, 6, 4, "2 runners idle > 5m", 40},
		{arm, 0, 1, "1 job queued > 30s", 30},
		{linux, 4, 5, "1 job queued > 30s", 8},
	}
	for i, e := range events {
		ev := &store.ScalingEvent{
			ID:        fmt.Sprintf("scl_demo%02d", i),
			PoolID:    e.pool.ID,
			PoolName:  e.pool.Name,
			From:      e.from,
			To:        e.to,
			Reason:    fmt.Sprintf("scaled %s %d -> %d: %s", e.pool.Name, e.from, e.to, e.why),
			CreatedAt: now.Add(-time.Duration(e.agoMin) * time.Minute),
		}
		if err := c.st.AppendScalingEvent(ctx, ev); err != nil {
			return fmt.Errorf("seeding scaling event %d: %w", i, err)
		}
	}
	return nil
}

func (c *Controller) seedAudit(ctx context.Context, now time.Time, pool *store.Pool) error {
	entries := []struct {
		actor, kind, action, targetKind, target string
		agoMin                                  int
	}{
		{"alice", "user", "pool.create", "pool", pool.ID, 240},
		{"alice", "user", "installation.create", "installation", demoInstallationID, 245},
		{"bob", "user", "runner.drain", "runner", "run_demo08", 60},
		{"ci-bot", "token", "pool.update", "pool", pool.ID, 35},
		{"zoomies", "system", "host.join", "host", demoHostPrefix + "b", 200},
	}
	for i, e := range entries {
		ev := &store.AuditEvent{
			ID:         fmt.Sprintf("aud_demo%02d", i),
			ActorID:    "usr_demo_" + e.actor,
			ActorName:  e.actor,
			ActorKind:  e.kind,
			Action:     e.action,
			TargetKind: e.targetKind,
			TargetID:   e.target,
			IP:         "10.0.0.9",
			CreatedAt:  now.Add(-time.Duration(e.agoMin) * time.Minute),
		}
		if err := c.st.AppendAudit(ctx, ev); err != nil {
			return fmt.Errorf("seeding audit event %d: %w", i, err)
		}
	}
	return nil
}

// seedSamples writes an hour of per-minute fleet history.
//
// Without it the Overview's sparklines have a single point until the instance
// has been up for a while, so the one thing that makes that page worth leaving
// open -- the shape of the last hour -- is exactly what a demo or a screenshot
// cannot show. The shape is deliberate rather than noise: a quiet start, a
// burst of queued work that the fleet scales into, and a wind-down, which is
// what a real morning looks like.
func (c *Controller) seedSamples(ctx context.Context, now time.Time, rng *rand.Rand) error {
	const minutes = 60
	start := now.Add(-minutes * time.Minute).Truncate(time.Minute)

	for i := 0; i <= minutes; i++ {
		at := start.Add(time.Duration(i) * time.Minute)

		// A burst arriving around minute 20 and clearing by minute 50.
		var queued, running, total int
		switch {
		case i < 15:
			queued = jitter(rng, 0, 1)
			running = jitter(rng, 1, 2)
			total = 2 + running
		case i < 25:
			queued = jitter(rng, 4, 9)
			running = jitter(rng, 2, 4)
			total = 4 + running
		case i < 45:
			// The scheduler has caught up: the queue drains as runners appear.
			queued = jitter(rng, 1, 4)
			running = jitter(rng, 4, 7)
			total = 2 + running + queued/2
		default:
			queued = jitter(rng, 0, 2)
			running = jitter(rng, 2, 4)
			total = 3 + running
		}
		busy := running
		if busy > total {
			busy = total
		}
		idle := total - busy
		if idle < 0 {
			idle = 0
		}

		if err := c.st.RecordSample(ctx, store.FleetSample{
			At:           at,
			QueuedJobs:   queued,
			RunningJobs:  running,
			IdleRunners:  idle,
			BusyRunners:  busy,
			TotalRunners: total,
		}); err != nil {
			return fmt.Errorf("seeding the fleet sample for %s: %w", at.Format(time.RFC3339), err)
		}
	}
	return nil
}

// jitter returns a value in [lo, hi]. The fixtures use a seeded source, so the
// shape is the same on every run and a screenshot taken today matches one taken
// last week.
func jitter(rng *rand.Rand, lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + rng.IntN(hi-lo+1)
}
