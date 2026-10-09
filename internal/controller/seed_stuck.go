package controller

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// StuckSeedEnvVar names the environment variable that adds the diagnostics
// fixture on top of the demo fleet.
//
// It is separate from ZOOMIES_SEED_DEMO on purpose, and it is the opposite of
// what a demo wants. The demo deliberately keeps its two starting runners young
// (see freshenDemoRunners) so an instance left open never reports a fault in a
// fleet that has no agent to have one. Every diagnostic this project has for a
// fleet in trouble -- the problems drawer, the stuck-runner shapes, the blocked
// pool, the held job -- is therefore unreachable from the demo, which is why
// they went untested until this existed.
//
// Nothing here is for an operator. It exists so a test can open the pages an
// operator opens on their worst day.
const StuckSeedEnvVar = "ZOOMIES_SEED_STUCK"

// The diagnostics fixture's own identifiers. They begin "demo" after the
// prefix, so IsDemoID covers them and every guard that keeps a fixture out of a
// real fleet applies to them unchanged.
const (
	stuckPoolID    = "pool_demostuckblocked"
	stuckPoolName  = "zoomies-demo-stuck-blocked"
	stuckHeldJobID = "job_demostuckheldjob"
	// stuckBlockedJobID is the job waiting on the pool nothing can place: the
	// fixture for an explanation whose class is the scheduler's own reason.
	stuckBlockedJobID = "job_demostuckblocked"
	// StuckThrottledHostName is the fixture's throttled host, named here so
	// a browser test can find its card.
	StuckThrottledHostName = "demo-throttled-1"
	stuckThrottledHostID   = demoHostPrefix + "stuckthrottled"
)

// stuckSeedRequested reports whether the diagnostics fixture was asked for.
func stuckSeedRequested() bool {
	v, ok := os.LookupEnv(StuckSeedEnvVar)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// SeedStuck ages the demo's starting runners into the two stuck shapes and adds
// the two fixtures the demo has no room for: a pool nothing can place, and a
// job GitHub is holding.
//
// It runs after SeedDemo and depends on it, because a fleet in trouble is only
// legible against one that is working: the point of the problems drawer is that
// it names the three things wrong among the twenty that are right.
func (c *Controller) SeedStuck(ctx context.Context) error {
	now := c.Now()

	// The demo's two starting runners are the two shapes exactly: one with no
	// container yet, one whose container came up and never registered. They are
	// already the right shape and only ever the wrong age, so this back-dates
	// them rather than writing more rows -- a fixture that duplicated them
	// would drift from the demo's the first time either was edited.
	starting, _, err := c.st.ListRunners(ctx, store.RunnerFilter{
		States: []store.RunnerState{store.RunnerProvisioning, store.RunnerRegistering},
	}, store.Page{Limit: 100})
	if err != nil {
		return fmt.Errorf("listing the runners to age: %w", err)
	}
	// Three quarters of the provision timeout: past half, which is when
	// runners.not_progressing starts saying so, and short of the whole, which
	// is when the reconcile loop gives up and fails the runner. Ageing them to
	// the full timeout produced a fixture with one stuck runner rather than
	// two, because the next pass failed the one with no container -- which is
	// the correct behaviour and the wrong fixture.
	stuckSince := now.Add(-c.cfg().Scheduler.ProvisionTimeout * 3 / 4)
	aged := 0
	for _, r := range starting {
		if !IsDemoID(r.ID) {
			continue
		}
		if r.ContainerStartedAt != nil {
			err = c.st.SetRunnerStartup(ctx, r.ID, r.ImagePullDuration, &stuckSince)
		} else {
			err = c.st.SetRunnerCreatedAt(ctx, r.ID, stuckSince)
		}
		if err != nil {
			return fmt.Errorf("ageing runner %s into a stuck one: %w", r.ID, err)
		}
		aged++
	}
	if aged == 0 {
		return fmt.Errorf("no demo runner was starting up, so there is nothing to make stuck; is %s set too?", SeedEnvVar)
	}

	if err := c.seedBlockedPool(ctx); err != nil {
		return err
	}
	if err := c.seedHeldJob(ctx, now); err != nil {
		return err
	}
	if err := c.seedThrottledHost(ctx, now); err != nil {
		return err
	}
	c.log.Info("seeded the diagnostics fixture", "env", StuckSeedEnvVar, "stuck_runners", aged)
	return nil
}

// seedBlockedPool writes a pool no host can take, so the scheduler's own
// sentence for that -- and the pool warning the drawer renders from it -- has
// something to say.
//
// The selector is what makes it unplaceable, rather than a disabled pool or a
// ceiling of zero: those are choices an operator made, and the page says so
// differently. A selector nothing answers is the misconfiguration, and it is
// the one the reason string exists to explain.
func (c *Controller) seedBlockedPool(ctx context.Context) error {
	if _, err := c.st.GetPool(ctx, stuckPoolID); err == nil {
		return nil
	}
	p := &store.Pool{
		ID:             stuckPoolID,
		Name:           stuckPoolName,
		InstallationID: demoInstallationID,
		Labels:         store.StringSlice(store.BrandLabels([]string{"linux", "x64", stuckPoolName})),
		Backend:        store.BackendDocker,
		MinRunners:     1,
		MaxRunners:     2,
		IdleTimeout:    store.Duration(5 * time.Minute),
		Ephemeral:      true,
		DockerMode:     store.DockerNone,
		// No host answers this, and none is meant to.
		HostSelector: store.StringMap{"zone": "nowhere"},
		Enabled:      true,
	}
	if err := c.st.CreatePool(ctx, p); err != nil {
		return fmt.Errorf("seeding the blocked pool: %w", err)
	}
	return c.seedBlockedJob(ctx, p)
}

// seedBlockedJob queues one job the blocked pool claims, so that the pool's
// reason for placing nothing has a job to be the explanation of.
func (c *Controller) seedBlockedJob(ctx context.Context, p *store.Pool) error {
	if _, err := c.st.GetJob(ctx, stuckBlockedJobID); err == nil {
		return nil
	}
	queued := c.Now().Add(-10 * time.Minute)
	job := &store.Job{
		ID: stuckBlockedJobID, GitHubJobID: 80095, GitHubRunID: 40050, RunNumber: 350,
		Repo: demoRepos[0], Workflow: "CI", JobName: "build",
		Labels: p.Labels, InstallationID: p.InstallationID, PoolID: p.ID, Matched: true,
		State: store.JobQueued, QueuedAt: queued, EligibleAt: &queued,
		HTMLURL:    fmt.Sprintf("https://github.com/%s/actions/runs/%d", demoRepos[0], 40050),
		HeadBranch: "main", HeadSHA: fmt.Sprintf("%040x", 0xC0FFEE+95*7919), RunAttempt: 1,
	}
	saved, change, err := c.st.ApplyJob(ctx, job)
	if err != nil {
		return fmt.Errorf("seeding the blocked job: %w", err)
	}
	return c.seedJobTimeline(ctx, saved, change)
}

// seedHeldJob writes a job GitHub is holding for a deployment review.
//
// It is the one job state the demo has never carried, and the one that reads
// as a fleet fault when it is not: every panel that explains a wait keys on
// `queued`, so a held job used to sit in the drawer with nothing said about it.
func (c *Controller) seedHeldJob(ctx context.Context, now time.Time) error {
	held := &store.Job{
		ID:             stuckHeldJobID,
		GitHubJobID:    80099,
		GitHubRunID:    40099,
		RunNumber:      399,
		Repo:           demoRepos[0],
		Workflow:       "Deploy",
		JobName:        "deploy-production",
		Labels:         store.StringSlice(store.BrandLabels([]string{"linux", "x64", "zoomies-demo-linux-x64"})),
		InstallationID: demoInstallationID,
		State:          store.JobWaiting,
		QueuedAt:       now.Add(-18 * time.Minute),
		HTMLURL:        fmt.Sprintf("https://github.com/%s/actions/runs/%d", demoRepos[0], 40099),
		HeadBranch:     "main",
		HeadSHA:        fmt.Sprintf("%040x", 0xDEC1DE),
		RunAttempt:     1,
	}
	saved, change, err := c.st.ApplyJob(ctx, held)
	if err != nil {
		return fmt.Errorf("seeding the held job: %w", err)
	}
	return c.seedJobTimeline(ctx, saved, change)
}

// seedThrottledHost writes a host the controller has stepped down after
// sustained pressure, with a live runner on it that was given one slot's
// share of the machine, so the host card's throttle notice, the runner
// detail's allocation and the problems drawer's host.throttled entry all
// have something to render.
//
// It is throttled for its load average rather than for a CPU hold, because
// that is the signal the ladder was added for: a machine whose CPU pinned at
// 100% and stopped saying anything, while the runnable queue kept growing.
// The measurements are the shape of that machine -- CPU at 60% because the
// throttle has already halved every runner's quota, load still at thirty on
// eight cores because the work is still there -- and the demo heartbeat keeps
// the sample fresh so the card can keep explaining itself. Housekeeping leaves
// the demo's hosts alone, so the rung does not climb or lift on its own.
func (c *Controller) seedThrottledHost(ctx context.Context, now time.Time) error {
	if _, err := c.st.GetHost(ctx, stuckThrottledHostID); err == nil {
		return nil
	}
	cpu, load, memory := 60.0, 30.0, int64(4096)
	since := now.Add(-7 * time.Minute)
	changed := now.Add(-3 * time.Minute)
	h := &store.Host{
		ID:       stuckThrottledHostID,
		Name:     StuckThrottledHostName,
		Address:  "10.0.0.14",
		Capacity: 4,
		Backends: store.StringSlice{"docker"},
		BackendInfo: store.HostBackends{
			{Kind: store.BackendDocker, Available: true, Version: "27.1.1",
				Rootless: true, Endpoint: "unix:///run/user/1000/docker.sock", SupportsDinD: true,
				Limits: store.LimitSupport{Known: true, CPU: true, Memory: true, Pids: true}},
		},
		Labels:      store.StringMap{"arch": "amd64", "zone": "demo"},
		OS:          "linux",
		Distro:      "ubuntu",
		OSVersion:   "24.04",
		Arch:        "amd64",
		CPUs:        8,
		MemoryMB:    16384,
		DiskTotalMB: 524_288,
		DiskFreeMB:  301_989,
		Usage: store.HostUsage{
			CPUPercent:        &cpu,
			LoadAverage1:      &load,
			MemoryAvailableMB: &memory,
			SampledAt:         now,
		},
		Throttle: store.HostThrottle{
			Level:     2,
			Since:     &since,
			ChangedAt: &changed,
			Reason:    "the 1-minute load average is 30.0, at least twice the host's 8 CPUs",
		},
		Version:         version.Version,
		ProtocolVersion: agent.ProtocolVersion,
		LastHeartbeat:   now,
	}
	if err := c.st.CreateHost(ctx, h); err != nil {
		return fmt.Errorf("seeding the throttled host: %w", err)
	}
	// One of the demo's busy runners moves here and is given the share a
	// four-slot host of this size hands out: (8 - 0.5) / 4 CPUs, floored to
	// the hundredth, and (16384 - 512) / 4 MB. Moved rather than written
	// afresh so the fixture's runner count stays what the demo's tests pin.
	runners, _, err := c.st.ListRunners(ctx, store.RunnerFilter{
		States: []store.RunnerState{store.RunnerBusy},
	}, store.Page{Limit: 100})
	if err != nil {
		return fmt.Errorf("listing the runners to place on the throttled host: %w", err)
	}
	slices.SortFunc(runners, func(a, b *store.Runner) int { return strings.Compare(a.ID, b.ID) })
	for _, r := range runners {
		if !IsDemoID(r.ID) {
			continue
		}
		r.HostID = h.ID
		r.AllocatedCPUs = 1.87
		r.AllocatedMemoryMB = 3968
		r.AllocationSource = store.AllocationFromHost
		if err := c.st.UpdateRunner(ctx, r); err != nil {
			return fmt.Errorf("placing runner %s on the throttled host: %w", r.ID, err)
		}
		return nil
	}
	return fmt.Errorf("no demo runner was busy, so none can be placed on the throttled host; is %s set too?", SeedEnvVar)
}
