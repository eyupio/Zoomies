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
		Image:          c.cfg().GitHub.RunnerImage,
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
		Image:          c.cfg().GitHub.RunnerImage,
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
			// The store stamps created_at itself, so the backend timings are
			// placed relative to the seeding instant rather than to `created`:
			// a few seconds to a running container, a few more to a registered
			// runner, varying by runner so the p50 and p95 differ. Without them
			// the Overview's startup and registration tiles both read 0ms, and
			// a demo that says the fleet starts runners in no time at all is
			// lying about the one number an operator sizing a pool asks for.
			containerStarted := now.Add(time.Duration(3+i%5) * time.Second)
			r.ContainerStartedAt = &containerStarted
			if s.state != store.RunnerRegistering {
				registered := containerStarted.Add(time.Duration(6+(i*3)%9) * time.Second)
				r.RegisteredAt = &registered
			}
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

	branches := []string{"main", "main", "main", "feature/faster-builds", "release/2.4", "renovate/deps"}

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
			HeadBranch:  branches[i%len(branches)],
			HeadSHA:     fmt.Sprintf("%040x", 0xC0FFEE+i*7919),
			RunAttempt:  1 + i%7/6,
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
			// One failure the fleet owns: the runner died under the job, which
			// is what the "runner lost" badge, the timeline entry and the
			// problems drawer entry all have as their fixture. It is the most
			// recent finished job, so that it falls inside the hour the
			// problems drawer looks back over.
			lostRunner := i == 43
			if lostRunner {
				j.Conclusion = "failure"
			}
			j.StartedAt, j.CompletedAt = &started, &completed
			r := runners[i%12]
			j.RunnerID, j.RunnerName = r.ID, r.Name
			j.Steps = demoSteps(j.JobName, j.Conclusion, started, completed)
			if lostRunner {
				j.RunnerFault = fmt.Sprintf("runner %s stopped while this job was running: runner exited with code 137: the container was killed for exceeding its memory limit", r.Name)
			}
		case i < 47 && len(busy) > 0:
			// Running right now, on one of the busy runners.
			r := busy[i%len(busy)]
			started := now.Add(-time.Duration(2+i%5) * time.Minute)
			// Queued moments before it started, as on a fleet that is keeping
			// up. Left in its historical slot the job would carry a forty-minute
			// wait, and the three running jobs are most of what the Overview's
			// one-hour median sees -- so the headline number would say the
			// fleet is drowning while every other panel says it is fine.
			j.QueuedAt = started.Add(-time.Duration(12+7*(i%3)) * time.Second)
			j.State = store.JobInProgress
			j.StartedAt = &started
			j.RunnerID, j.RunnerName = r.ID, r.Name
			j.Steps = demoSteps(j.JobName, "", started, time.Time{})
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

		// One repository still on a hosted-runner vendor, which is what a fleet
		// looks like part-way through a migration. Its jobs carry labels no pool
		// here claims and they run anyway, so they are the case that must never
		// be reported as "nothing will run this".
		if i == 12 {
			j.Labels = store.StringSlice{"blacksmith-4vcpu-ubuntu-2404"}
			j.PoolID, j.Matched = "", false
			j.RunnerID, j.RunnerName = "", "blacksmith-4vcpu-ubuntu-2404-9f2c"
		}

		saved, change, err := c.st.ApplyJob(ctx, j)
		if err != nil {
			return fmt.Errorf("seeding job %d: %w", i, err)
		}
		if err := c.seedJobTimeline(ctx, saved, change); err != nil {
			return err
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

// demoSteps renders the steps a job of this name would have, concluded the way
// the job was: a failure fails on the step that does the work and skips the
// rest, a cancellation stops there, and a job still running is part-way through
// it.
func demoSteps(jobName, conclusion string, started, completed time.Time) store.JobSteps {
	work := map[string]string{"build": "Build", "test": "Run tests", "lint": "Lint", "package": "Package artefacts"}[jobName]
	if work == "" {
		work = "Run " + jobName
	}
	names := []string{"Set up job", "Checkout", "Set up toolchain", work, "Post checkout", "Complete job"}
	steps := make(store.JobSteps, 0, len(names))
	span := completed.Sub(started)
	if completed.IsZero() {
		span = 4 * time.Minute
	}
	// The working step takes most of the time; the rest are seconds each.
	cuts := []float64{0, 0.02, 0.05, 0.12, 0.96, 0.98, 1}
	for i, name := range names {
		at := started.Add(time.Duration(cuts[i] * float64(span)))
		end := started.Add(time.Duration(cuts[i+1] * float64(span)))
		step := store.JobStep{Number: i + 1, Name: name, Status: "completed", Conclusion: "success", StartedAt: &at, CompletedAt: &end}
		switch {
		case conclusion == "" && i == 3:
			step.Status, step.Conclusion, step.CompletedAt = "in_progress", "", nil
		case conclusion == "" && i > 3:
			step.Status, step.Conclusion, step.StartedAt, step.CompletedAt = "queued", "", nil, nil
		case (conclusion == "failure" || conclusion == "cancelled") && i == 3:
			step.Conclusion = conclusion
		case (conclusion == "failure" || conclusion == "cancelled") && i == 4:
			step.Conclusion = "skipped"
		}
		steps = append(steps, step)
	}
	return steps
}

// seedJobTimeline writes the entries a seeded job would have earned had its
// deliveries really arrived, stamped at the times the job's own timestamps say
// they happened rather than at seeding time.
func (c *Controller) seedJobTimeline(ctx context.Context, j *store.Job, change store.JobChange) error {
	if !change.Created {
		return nil
	}
	add := func(kind store.JobEventKind, source, message string, at time.Time, runner bool) error {
		e := &store.JobEvent{JobID: j.ID, Kind: kind, Source: source, Message: message, At: at}
		if runner {
			e.RunnerID, e.RunnerName = j.RunnerID, j.RunnerName
		}
		return c.st.AppendJobEvent(ctx, e)
	}
	if err := add(store.JobEventQueued, sourceWebhook, fmt.Sprintf("GitHub queued %s in %s, asking for [%s]",
		jobTitle(j), j.Repo, strings.Join(j.Labels, ", ")), j.QueuedAt, false); err != nil {
		return err
	}
	if err := add(c.claimKind(j), sourceWebhook, c.claimMessage(ctx, j), j.QueuedAt.Add(time.Second), false); err != nil {
		return err
	}
	if j.StartedAt != nil {
		if err := add(store.JobEventStarted, sourceWebhook, c.startMessage(ctx, j, nil), *j.StartedAt, true); err != nil {
			return err
		}
	}
	if j.RunnerFault != "" && j.CompletedAt != nil {
		if err := add(store.JobEventRunnerLost, sourceAgent,
			j.RunnerFault+"; GitHub will report the job failed once the runner's absence is noticed",
			j.CompletedAt.Add(-20*time.Second), true); err != nil {
			return err
		}
	}
	if j.CompletedAt != nil {
		if err := add(store.JobEventCompleted, sourceWebhook, completionMessage(j), *j.CompletedAt, true); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) seedScaling(ctx context.Context, now time.Time, linux, arm *store.Pool) error {
	// The reason quotes the pool by name, exactly as the scheduler writes it,
	// so the fixture cannot drift from the pool it describes when the pools are
	// renamed.
	//
	// Ten of them, on purpose: that is as many as the Overview shows, and more
	// than fit beside a fleet of two pools, so the fixture exercises the feed
	// being cut to its column rather than stretching the page. The scheduler
	// keeps deciding over this fleet once it is seeded, and each decision it
	// records pushes the oldest line here off the Overview -- so the lines the
	// UI tests quote are kept well clear of the old end, and the wind-down
	// before them is what gets displaced.
	events := []struct {
		pool     *store.Pool
		from, to int
		why      string
		agoMin   int
	}{
		{linux, 6, 4, "2 runners idle > 5m", 165},
		{linux, 4, 2, "2 runners idle > 5m", 150},
		{linux, 2, 1, "1 runner idle > 5m", 140},
		{linux, 1, 4, "3 jobs queued > 30s", 95},
		{linux, 4, 6, "2 jobs queued > 30s", 70},
		{arm, 0, 1, "1 job queued > 30s", 62},
		{arm, 1, 0, "1 runner idle > 10m", 48},
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
