package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/naming"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
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
	demoProviderID     = "prv_demoproxmox"
	// The two machines: one that finished and one still on its way, which is
	// the whole of the Machines page's story in two rows.
	demoMachineReadyID    = "mach_demo01"
	demoMachineBuildingID = "mach_demo02"
	demoJoinTokenID       = "join_demo01"
)

// IsDemoID reports whether an identifier belongs to the seeded demo fixtures.
//
// The fixtures have no GitHub behind them, so the two places that would
// otherwise reach out on their behalf -- the credential prober and the
// registration reaper -- check this and skip. Without it a demo or a UI test
// run fills the problems drawer with "this installation is not usable" and the
// log with parse failures, none of which says anything about the fleet.
//
// A fixture's identifier begins "demo" after its prefix and is never the shape
// store.NewID produces. Both halves matter: the prefix alone also matched a
// real identifier whose random part happened to start with those four
// letters, which one row in a million does, and such an installation was then
// skipped by the prober, the poller and the reap, and such a host was never
// reclaimed. A test in this package holds every fixture literal to the rule.
func IsDemoID(id string) bool {
	_, rest, ok := strings.Cut(id, "_")
	return ok && strings.HasPrefix(rest, "demo") && !store.LooksGenerated(id)
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

// demoQuietRepos have no workflows at all. They exist only for the migration
// wizard, which has to show that a repository was looked at and had nothing to
// move -- and has to be able to hide it again.
var demoQuietRepos = []string{"acme/docs"}

// demoArchivedRepos are archived on GitHub, so no pull request can be opened
// against them however much their workflows would change. They are here
// because that is the one row the wizard used to tick and then refuse at the
// end, and a demo should show it being refused up front.
var demoArchivedRepos = []string{"acme/legacy-api"}

// demoMigratedRepos already run on this fleet. They have nothing to move for
// the opposite reason demoQuietRepos do, and the wizard has to say which.
var demoMigratedRepos = []string{"acme/infra"}

var demoPoolNames = []string{
	"zoomies-demo-linux-x64", "zoomies-demo-linux-arm64",
	"demo-linux-x64", "demo-linux-arm64",
}

// refuseSeedOnRealState is the second half of SeedDemo's guard: anything in the
// database that is not a demo fixture means this is somebody's fleet.
func (c *Controller) refuseSeedOnRealState(ctx context.Context) error {
	refuse := func(what string) error {
		return fmt.Errorf("refusing to seed demo data: this instance already has %s, and demo fixtures must never appear in a real fleet; unset %s", what, SeedEnvVar)
	}
	insts, err := c.st.ListInstallations(ctx)
	if err != nil {
		return fmt.Errorf("checking whether this instance is empty: %w", err)
	}
	for _, inst := range insts {
		if !IsDemoID(inst.ID) {
			return refuse(fmt.Sprintf("the installation %q", inst.Target))
		}
	}
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		return fmt.Errorf("checking whether this instance is empty: %w", err)
	}
	for _, h := range hosts {
		if !IsDemoID(h.ID) {
			return refuse(fmt.Sprintf("the host %q", h.Name))
		}
	}
	n, err := c.st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("checking whether this instance is empty: %w", err)
	}
	if n > 0 {
		return refuse(plural(n, "account"))
	}
	return nil
}

// SeedDemo writes a deterministic fixture fleet: one installation, two pools, a
// dozen runners spread across the state machine, fifty jobs with plausible
// queue waits and outcomes, three hosts, one provider with the two machines it
// has rented, some scaling history and an audit trail. It is what
// ZOOMIES_SEED_DEMO turns on for the Playwright suite and for a demo instance.
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
	// A pool is what seeding writes, but it is not the only sign of a real
	// deployment. A controller with an installation, a host or an account and
	// no pool yet is the state every fresh production install passes through,
	// and a compose file that kept ZOOMIES_SEED_DEMO from a trial run must not
	// be able to drop a fixture fleet into it.
	if err := c.refuseSeedOnRealState(ctx); err != nil {
		return err
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
	prov, err := c.seedProvider(ctx, now)
	if err != nil {
		return err
	}
	machines, err := c.seedMachines(ctx, now, prov, pool1, hosts)
	if err != nil {
		return err
	}
	runners, err := c.seedRunners(ctx, now, []*store.Pool{pool1, pool2}, hosts)
	if err != nil {
		return err
	}
	if err := c.seedMemory(ctx, now, []*store.Pool{pool1, pool2}, hosts, runners); err != nil {
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
	if err := c.seedHostSamples(ctx, now, hosts, runners); err != nil {
		return err
	}

	c.log.Info("seeded the demo fleet",
		"pools", len(demoPoolNames), "hosts", len(hosts), "runners", len(runners),
		"machines", len(machines))
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
		distro, osVer  string
		cpus           int
		memoryMB       int64
		diskMB, freeMB int64
		capacity       int
		embedded       bool
		cordoned       bool
		silentFor      time.Duration
	}{
		{demoHostPrefix + "a", "demo-builder-1", "amd64", "ubuntu", "24.04", 16, 32768, 1_048_576, 734_003, 6, true, false, 0},
		// A second distribution, so the Hosts page shows the platform column
		// doing something and a pool's platform has a host it must not land on.
		{demoHostPrefix + "b", "demo-builder-2", "amd64", "debian", "12", 8, 16384, 524_288, 31_457, 4, false, false, 0},
		// One cordoned host, so the Hosts page and the problems drawer both
		// have something real to render. The second builder is nearly out of
		// disk, which is the state that stops jobs while every slot still
		// reads as free -- the Hosts page has to show it.
		{demoHostPrefix + "c", "demo-arm-1", "arm64", "ubuntu", "24.04", 8, 16384, 262_144, 190_054, 2, false, true, 0},
	}
	out := make([]*store.Host, 0, len(specs))
	for i, s := range specs {
		h := &store.Host{
			ID:   s.id,
			Name: s.name,
			// An address that reads as one; the last character of the ID
			// gave the demo fleet a host at 10.0.0.a.
			Address:  fmt.Sprintf("10.0.0.%d", 10+i),
			Embedded: s.embedded,
			Capacity: s.capacity,
			Backends: store.StringSlice{"docker"},
			BackendInfo: store.HostBackends{
				// A daemon that can apply every limit, which is what a rootless
				// daemon on a delegated cgroup v2 slice reports: the demo fleet
				// exists to look like a fleet with nothing wrong, and a probe
				// that said nothing would raise host.limits_unverified on every
				// host in it.
				{Kind: store.BackendDocker, Available: true, Version: "27.1.1",
					Rootless: true, Endpoint: "unix:///run/user/1000/docker.sock", SupportsDinD: true,
					Limits: store.LimitSupport{Known: true, CPU: true, Memory: true, Pids: true}},
				// The real probe's sentence, commands and all: the demo fleet is
				// what the UI is looked at with, so it has to show what an
				// operator actually gets when a backend is missing.
				{Kind: store.BackendPodman, Detail: "no socket at /run/user/1000/podman/podman.sock; " +
					"install Podman, start it (`systemctl --user enable --now podman.socket`), " +
					"or point agent.docker_host at the right socket"},
			},
			Labels:      store.StringMap{"arch": s.arch, "zone": "demo"},
			OS:          "linux",
			Distro:      s.distro,
			OSVersion:   s.osVer,
			Arch:        s.arch,
			CPUs:        s.cpus,
			MemoryMB:    s.memoryMB,
			DiskTotalMB: s.diskMB,
			DiskFreeMB:  s.freeMB,
			// This build's own version, not a literal: a demo host on some
			// other string is a host on another release, and the fleet would
			// correctly report skew on every one of them. The demo exists to
			// look like a fleet with nothing wrong.
			Version:         version.Version,
			ProtocolVersion: agent.ProtocolVersion,
			// This build's agent advertises it, so a demo host on this build
			// does too; a fleet of hosts that could not lend CPU would carry a
			// badge on every card saying so.
			Features:      store.StringSlice{agent.FeatureElasticCPU, agent.FeatureElasticMemory, agent.FeatureTmpfs, agent.FeatureDaemonShare},
			Cordoned:      s.cordoned,
			LastHeartbeat: now.Add(-s.silentFor),
		}
		if s.arch == "arm64" {
			h.Connection = "tailcat"
		}
		// A measurement on every host, kept fresh by the demo heartbeat: the
		// capacity map draws measured CPU and memory, and a fixture fleet that
		// had only committed figures would show the chart's headline series
		// as "not measured" in every screenshot.
		usage := demoHostUsage(s.cpus, s.memoryMB, i, now, now)
		h.Usage = usage
		if err := c.st.CreateHost(ctx, h); err != nil {
			return nil, fmt.Errorf("seeding host %s: %w", s.name, err)
		}
		out = append(out, h)
	}
	return out, nil
}

// demoHeartbeatInterval keeps the seeded hosts inside store.HeartbeatTimeout
// with room to spare.
const demoHeartbeatInterval = 30 * time.Second

// demoStartingAge is how far into starting up the demo's two unfinished runners
// are held: long enough that the grid shows a plausible age rather than zero,
// and far short of the point at which a fleet would call them stuck.
const demoStartingAge = 20 * time.Second

// demoHeartbeatLoop keeps the demo fleet's hosts alive.
//
// A seeded host has no agent behind it, so its heartbeat is a timestamp written
// once and never touched again -- and ninety seconds later every host in the
// demo fleet is unhealthy, every pool "has nowhere to run", and the fleet an
// operator opened the UI to look at has gone dark while they were reading it.
// The same ninety seconds is why a long Playwright run saw a different Pools
// page from a short one.
//
// This runs only when the demo seed was requested, and only for hosts the seed
// created, so nothing it does can reach a real fleet.
func (c *Controller) demoHeartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(demoHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.beatDemoHosts(ctx)
			c.freshenDemoRunners(ctx)
			c.freshenDemoMachines(ctx)
		}
	}
}

// freshenDemoRunners keeps the seeded runners that are still starting up from
// ageing into runners that are stuck.
//
// The fixture holds one runner in `provisioning` and one in `registering` so
// that both states appear in the grid and in the screenshots. On a real fleet
// those states last seconds, and `runners.not_progressing` says so once one has
// lasted half the provision timeout -- so a demo instance left open would start
// reporting a fault in a fleet that has no agent to have one. It is the same
// lie as a heartbeat written once: correct at the instant it was seeded and
// wrong a few minutes later.
//
// Only the seed's own rows are touched, and only while they are still starting.
func (c *Controller) freshenDemoRunners(ctx context.Context) {
	// The diagnostics fixture exists to have runners that are stuck, so
	// keeping them young would undo the one thing it does. It is opt-in and
	// never on in a demo.
	if stuckSeedRequested() {
		return
	}
	runners, _, err := c.st.ListRunners(ctx, store.RunnerFilter{
		States: []store.RunnerState{store.RunnerProvisioning, store.RunnerRegistering},
	}, store.Page{Limit: 100})
	if err != nil {
		c.log.Warn("demo refresh could not list runners that are starting up", "error", err)
		return
	}
	now := c.Now()
	for _, r := range runners {
		if !IsDemoID(r.ID) {
			continue
		}
		// A registering runner's clock runs from its container; a provisioning
		// one has no container yet, so its age is all it has.
		fresh := now.Add(-demoStartingAge)
		var err error
		if r.ContainerStartedAt != nil {
			err = c.st.SetRunnerStartup(ctx, r.ID, r.ImagePullDuration, &fresh)
			r.ContainerStartedAt = &fresh
		} else {
			err = c.st.SetRunnerCreatedAt(ctx, r.ID, fresh)
			r.CreatedAt = fresh
		}
		if err != nil {
			c.log.Warn("demo refresh failed", "runner", r.ID, "error", err)
			continue
		}
		// A page already open holds the row it was sent, so without this the
		// age on screen keeps climbing and then jumps back on a reload. The
		// host beat publishes for the same reason.
		c.publishRunner(ctx, events.KindRunnerUpdated, r)
	}
}

// freshenDemoMachines keeps the seeded machines' proof of ownership, and the
// sweep that would have written it, from going stale.
//
// A machine may only be deleted on an observation younger than
// observationMaxAge, and a demo has no hypervisor behind it to make a second
// one. A minute after seeding every machine on the page answers "the last time
// anything confirmed this resource is ours is too old to act on" and the
// provider reads as one nothing has ever swept -- correct at the instant it
// was seeded and wrong while somebody is still reading it, which is the
// heartbeat's failure mode on the half of the fleet that spends money.
//
// A machine whose ownership genuinely is in doubt is left alone: re-proving it
// here would erase the one complaint an operator is meant to act on.
func (c *Controller) freshenDemoMachines(ctx context.Context) {
	now := c.Now()
	providers, err := c.st.ListProviders(ctx)
	if err != nil {
		c.log.Warn("demo refresh could not list providers", "error", err)
		return
	}
	for _, p := range providers {
		if !IsDemoID(p.ID) {
			continue
		}
		if err := c.st.SetProviderSwept(ctx, p.ID, now); err != nil {
			c.log.Warn("demo refresh failed", "provider", p.ID, "error", err)
		}
	}
	machines, _, err := c.st.ListMachines(ctx, store.MachineFilter{}, store.Page{Limit: 100})
	if err != nil {
		c.log.Warn("demo refresh could not list machines", "error", err)
		return
	}
	for _, m := range machines {
		if !IsDemoID(m.ID) || !m.Owns() || m.OwnershipError != "" || m.State == store.MachineQuarantined {
			continue
		}
		if err := c.st.SetMachineOwnershipVerified(ctx, m.ID, now); err != nil {
			c.log.Warn("demo refresh failed", "machine", m.ID, "error", err)
		}
	}
}

func (c *Controller) beatDemoHosts(ctx context.Context) {
	hosts, err := c.st.ListHosts(ctx)
	if err != nil {
		c.log.Warn("demo heartbeat could not list hosts", "error", err)
		return
	}
	now := c.Now()
	for _, h := range hosts {
		if !IsDemoID(h.ID) {
			continue
		}
		if err := c.st.Heartbeat(ctx, h.ID, now); err != nil {
			c.log.Warn("demo heartbeat failed", "host", h.ID, "error", err)
			continue
		}
		h.LastHeartbeat = now
		// A fixture host with a measurement keeps it fresh, for the same
		// reason the heartbeat is kept fresh: the diagnostics fixture's
		// throttled host is throttled for a load average, and a sample that
		// aged past HostUsageMaxAge would read on its card as a throttle
		// nobody can see the reason for.
		if !h.Usage.SampledAt.IsZero() {
			h.Usage.SampledAt = now
			if err := c.st.SetHostUsage(ctx, h.ID, h.Usage); err != nil {
				c.log.Warn("demo heartbeat could not freshen a host's usage", "host", h.ID, "error", err)
			}
		}
		c.PublishHost(h)
	}
}

func (c *Controller) seedPools(ctx context.Context) (*store.Pool, *store.Pool, error) {
	linux := &store.Pool{
		ID:             demoPoolLinuxID,
		Name:           demoPoolNames[0],
		InstallationID: demoInstallationID,
		Labels:         store.StringSlice(store.BrandLabels([]string{"linux", "x64", "zoomies-demo-linux-x64"})),
		Backend:        store.BackendDocker,
		// No image: the platform picks the variant, which is what a pool
		// created today does and what the Pools page should demonstrate.
		Platform:     store.Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "amd64"},
		MinRunners:   1,
		MaxRunners:   8,
		IdleTimeout:  store.Duration(5 * time.Minute),
		Ephemeral:    true,
		DockerMode:   store.DockerNone,
		Resources:    store.Resources{CPUs: 2, MemoryMB: 4096},
		HostSelector: store.StringMap{"arch": "amd64"},
		// The memory valve lending, with a little swap as the last resort, and
		// the work folder and /tmp kept in memory: the two things a runner's row
		// says about its memory, so the Runners page has both to show. The
		// folders are typed and small beside the limit, because a pool whose
		// folders may fill half of it carries a warning of its own and the demo
		// is a fleet with nothing wrong that it did not mean to show.
		MemoryBurst: store.MemoryBurstPolicy{Mode: store.MemoryBurstAutomatic, SpillMB: 2048},
		Tmpfs: store.TmpfsConfig{
			Work: store.TmpfsMount{Enabled: true, SizeMB: 1024},
			Tmp:  store.TmpfsMount{Enabled: true, SizeMB: 512},
		},
		Enabled: true,
	}
	arm := &store.Pool{
		ID:             demoPoolArmID,
		Name:           demoPoolNames[1],
		InstallationID: demoInstallationID,
		Labels:         store.StringSlice(store.BrandLabels([]string{"linux", "arm64", "zoomies-demo-linux-arm64"})),
		Backend:        store.BackendDocker,
		Platform:       store.Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "arm64"},
		// The image a pool that gives its jobs a daemon actually runs, written
		// here as the API would write it, so the demo does not show the one
		// combination the wizard and the migration exist to remove.
		Image:       config.RunnerImageFor(c.cfg().GitHub.RunnerImage, true),
		MinRunners:  0,
		MaxRunners:  4,
		IdleTimeout: store.Duration(10 * time.Minute),
		// Persistent runners, so the problems drawer has a dangerous setting to
		// show and the UI's warning styling is exercised.
		Ephemeral:  false,
		DockerMode: store.DockerDinD,
		// The same size as the pool above, which on this pool is twice the
		// charge: the figure is typed, so the daemon the builds run in is
		// given it too and the host carries both. It is the shape a host is
		// sized wrong for most often, so the demo fleet has one -- and it is
		// sized rather than left blank because a pool that leaves its size to
		// the host puts its pair in one slot and charges one.
		Resources:    store.Resources{CPUs: 2, MemoryMB: 4096},
		HostSelector: store.StringMap{"arch": "arm64"},
		// Observing, which is how a pool that has just been made starts: the
		// valve decides and records what it would have lent and changes nothing,
		// and the image store its builds write to is kept in memory.
		MemoryBurst: store.MemoryBurstPolicy{Mode: store.MemoryBurstObserve},
		Tmpfs:       store.TmpfsConfig{Daemon: store.TmpfsMount{Enabled: true, SizeMB: 1024}},
		Enabled:     true,
	}
	if err := c.st.CreatePool(ctx, linux); err != nil {
		return nil, nil, fmt.Errorf("seeding pool %s: %w", linux.Name, err)
	}
	if err := c.st.CreatePool(ctx, arm); err != nil {
		return nil, nil, fmt.Errorf("seeding pool %s: %w", arm.Name, err)
	}
	return linux, arm, nil
}

// seedProvider writes the one place the demo fleet rents machines from: a
// Proxmox cluster answered the way docs/proxmox.md describes, down to a VMID
// range of its own.
//
// Every setting is filled in because the Providers page is where somebody
// looks to find out what a configured provider is meant to look like, and a
// row with half its answers blank teaches them the wrong shape. The machine it
// offers is the linux pool's platform, so the pool that runs out of hosts in
// this fixture is a pool this provider could actually serve -- a provider no
// pool can use is a row that never explains why it exists.
func (c *Controller) seedProvider(ctx context.Context, now time.Time) (*store.Provider, error) {
	p := &store.Provider{
		ID:       demoProviderID,
		Kind:     store.ProviderProxmox,
		Name:     "demo-pve",
		Endpoint: "https://pve.acme.example:8006",
		Settings: store.StringMap{
			"nodes":       "pve-1,pve-2",
			"template_id": "8000",
			"storage":     "local-zfs",
			"bridge":      "vmbr0",
			// A block nothing else allocates from, which is the blast radius
			// as well as the budget -- and the template sits outside it, since
			// a template inside the range is an identifier the allocator would
			// hand to a clone.
			"vmid_min":      "9000",
			"vmid_max":      "9099",
			"template_node": "pve-1",
			"pool":          "zoomies",
		},
		MachineLabels:   store.StringMap{"arch": "amd64", "zone": "demo"},
		MachineCapacity: 4,
		MachineBackend:  store.BackendDocker,
		MachinePlatform: store.Platform{OS: "ubuntu", OSVersion: "24.04", Arch: "amd64"},
		MachineCPUs:     8,
		MachineMemoryMB: 16384,
		MachineDiskMB:   524_288,
		// A ceiling with room above what it owns: at the ceiling the card says
		// the provider is full, which is not the state a demo should open on.
		MaxMachines:        4,
		MaxCreatesInFlight: 1,
		IdleTimeout:        store.Duration(15 * time.Minute),
		Enabled:            true,
	}
	if err := c.st.CreateProvider(ctx, p); err != nil {
		return nil, fmt.Errorf("seeding provider %s: %w", p.Name, err)
	}
	// Sealed by its own writer, as the API does it: a credential is the one
	// edit that never travels with the rest of the form. Nothing ever calls
	// Proxmox with it -- the machines below are written straight to the
	// database -- so it is a token-shaped string rather than a token.
	cred, err := c.key.SealString("zoomies@pve!demo=DEMO FIXTURE, NOT A TOKEN")
	if err != nil {
		return nil, fmt.Errorf("sealing the demo provider credential: %w", err)
	}
	if err := c.st.SetProviderCredentials(ctx, p.ID, cred); err != nil {
		return nil, fmt.Errorf("storing the demo provider credential: %w", err)
	}
	p.CredentialsEnc = cred
	// A preflight that passed and a sweep that found nothing out of place.
	// Without the first the card reads "Never checked. Run it before anything
	// is built on this", which is the one sentence a demo provider should not
	// be the example of.
	if err := c.st.SetProviderChecked(ctx, p.ID, now, ""); err != nil {
		return nil, fmt.Errorf("recording the demo provider's preflight: %w", err)
	}
	if err := c.st.SetProviderSwept(ctx, p.ID, now); err != nil {
		return nil, fmt.Errorf("recording the demo provider's sweep: %w", err)
	}
	p.LastCheckAt, p.LastSweepAt = &now, &now
	return p, nil
}

// seedMachines writes the two machines the demo provider has rented: one that
// became a host, and one the fleet is still waiting for.
//
// Two, and deliberately unalike. The ready one is what the Hosts page's
// provider badge and the machine-to-host link are rendered from; the other is
// what the lifecycle band, the pending counts and the timeline's live last row
// are rendered from, none of which a fleet of finished machines would show.
//
// The ready one is linked to a host the seed has already made rather than to a
// fourth of its own: every host on that page is a fixture something counts, and
// a rented host that nothing else in the fleet knows about would be a host with
// no runners, no jobs and no history on it.
func (c *Controller) seedMachines(ctx context.Context, now time.Time, prov *store.Provider, pool *store.Pool, hosts []*store.Host) ([]*store.Machine, error) {
	var host *store.Host
	for _, h := range hosts {
		if h.ID == demoHostPrefix+"b" {
			host = h
		}
	}
	if host == nil {
		return nil, fmt.Errorf("seeding machines: the demo fleet has no host %sb for one to have become", demoHostPrefix)
	}

	// The store stamps created_at itself, so a machine's phases are placed
	// forward from the seeding instant rather than behind it. A clone that
	// finished before the row that ordered it existed draws a timeline running
	// backwards, and MachineTimeline clamps every duration in one of those to
	// zero -- which is the one thing this fixture is here to give it. A minute
	// and a half is a linked clone, a boot, an agent install and a join, and
	// all of it is behind the clock by the time anybody has opened the page.
	after := func(d time.Duration) *time.Time { at := now.Add(d); return &at }

	ready := &store.Machine{
		ID:                demoMachineReadyID,
		ProviderID:        prov.ID,
		Name:              store.NewMachineName(demoMachineReadyID),
		State:             store.MachineReady,
		Message:           "the agent joined and this machine is serving runners",
		PoolID:            pool.ID,
		OwnerControllerID: c.controllerID(),
		// Not a credential: it is the mark a delete is checked against, and it
		// is fixed here for the same reason every other fixture identifier is.
		OwnerFingerprint: "demofixture2",
		ResourceZone:     "pve-1",
		ResourceID:       "9000",
		// The host's own address, because they are the same computer.
		Address:         host.Address,
		Capacity:        prov.MachineCapacity,
		Labels:          prov.MachineLabels,
		CreateStartedAt: after(time.Second),
		CreatedOKAt:     after(37 * time.Second),
		StartedAt:       after(52 * time.Second),
		BootstrappedAt:  after(82 * time.Second),
		ReadyAt:         after(89 * time.Second),
	}
	building := &store.Machine{
		ID:                demoMachineBuildingID,
		ProviderID:        prov.ID,
		Name:              store.NewMachineName(demoMachineBuildingID),
		State:             store.MachineBootstrapping,
		Message:           "the machine is running; installing the agent",
		PoolID:            pool.ID,
		OwnerControllerID: c.controllerID(),
		OwnerFingerprint:  "demofixture3",
		// The second node, because machines are spread across the nodes a
		// provider is given and a demo that only ever used the first would not
		// show it.
		ResourceZone:    "pve-2",
		ResourceID:      "9001",
		Address:         "10.0.0.13",
		Capacity:        prov.MachineCapacity,
		Labels:          prov.MachineLabels,
		CreateStartedAt: after(2 * time.Second),
		CreatedOKAt:     after(44 * time.Second),
		StartedAt:       after(58 * time.Second),
	}

	out := []*store.Machine{ready, building}
	for _, m := range out {
		if err := c.st.CreateMachine(ctx, m); err != nil {
			return nil, fmt.Errorf("seeding machine %s: %w", m.Name, err)
		}
		// The observation a delete is authorised by, written through the same
		// writer the ownership sweep uses.
		if err := c.st.SetMachineOwnershipVerified(ctx, m.ID, now); err != nil {
			return nil, fmt.Errorf("recording that machine %s is ours: %w", m.Name, err)
		}
		m.OwnershipVerifiedAt = &now
	}

	enrolled := now.Add(87 * time.Second)
	if err := c.seedMachineEnrolment(ctx, ready, host, enrolled); err != nil {
		return nil, err
	}
	return out, nil
}

// seedMachineEnrolment gives the ready machine the credential its host joined
// with, and links the two.
//
// The link is the only thing that grants this controller authority to delete a
// host, and LinkMachineHost is the only writer of it, so the fixture earns it
// the way a real machine does rather than by setting a column: a host_id with
// no token behind it would claim an enrolment nothing else in the system could
// account for. Only the hash of a join token is ever stored, so the fixture's
// is the hash of a string that is not a token -- and the row is spent, which is
// the state a token that did its job is left in.
func (c *Controller) seedMachineEnrolment(ctx context.Context, m *store.Machine, host *store.Host, at time.Time) error {
	tok := &store.JoinToken{
		ID:        demoJoinTokenID,
		TokenHash: cryptox.HashToken("zoomies demo fixture, not a join token"),
		Prefix:    "zoojoin_demo01",
		CreatedBy: "machine " + m.ID,
		Labels:    m.Labels,
		Capacity:  m.Capacity,
		ExpiresAt: at.Add(c.cfg().Provider.EnrolTimeout + machineTokenGrace),
		UsedAt:    &at,
		UsedByID:  host.ID,
		// Scoped to this machine and to the one name it may enrol under, which
		// is what stops a copy of the template joining as somebody else.
		MachineID:    m.ID,
		ExpectedName: m.Name,
	}
	if err := c.st.CreateJoinToken(ctx, tok); err != nil {
		return fmt.Errorf("seeding machine %s's join token: %w", m.Name, err)
	}
	if err := c.st.LinkMachineHost(ctx, m.ID, host.ID, tok.ID, at); err != nil {
		return fmt.Errorf("enrolling machine %s as host %s: %w", m.Name, host.Name, err)
	}
	m.HostID, m.JoinTokenID, m.EnrolledAt = host.ID, tok.ID, &at
	return nil
}

// demoRunnerName is the name a demo runner would have been given, in the
// grammar a real one is: its pool's shape, a word from the kennel, and a
// discriminator.
//
// The discriminator is the index rather than a random token, because the demo
// is a fixture. A screenshot taken today has to match one taken last week, and
// a Playwright test navigates to a name it was told at build time; both break
// on a name that is different every time the controller starts. "demo" in it
// is not decoration either -- somebody looking at a screenshot should be able
// to tell it from a fleet.
func demoRunnerName(pool *store.Pool, i int) string {
	word := naming.Kennel[i%len(naming.Kennel)]
	return naming.RunnerName(pool.Spec().String(), fmt.Sprintf("%s-demo%02d", word, i))
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
		// fault is the category behind a failed runner. The demo carries one
		// because the demo is where the split is seen before anybody has a
		// failure of their own, and a failed runner with no category would
		// show the feature turned off.
		fault store.FaultKind
	}
	specs := []spec{
		{store.RunnerBusy, 0, 0, 12, "", 3, ""},
		{store.RunnerBusy, 0, 0, 9, "", 1, ""},
		{store.RunnerBusy, 0, 1, 7, "", 2, ""},
		{store.RunnerIdle, 0, 0, 30, "", 5, ""},
		{store.RunnerIdle, 0, 1, 24, "", 4, ""},
		{store.RunnerIdle, 1, 2, 45, "", 9, ""},
		{store.RunnerRegistering, 0, 1, 1, "", 0, ""},
		{store.RunnerProvisioning, 0, 0, 0, "3 jobs queued > 30s", 0, ""},
		{store.RunnerDraining, 0, 1, 60, "idle for 6m, over the 5m idle timeout", 6, ""},
		{store.RunnerFailed, 0, 0, 20, "GitHub would not register zoomies-demo-linux-x64-f4k3: github: create jit config: 403 Forbidden", 0, store.FaultRegistration},
		{store.RunnerRemoved, 0, 0, 90, "ephemeral runner exited cleanly after its job", 1, ""},
		{store.RunnerRemoved, 1, 2, 120, "runner exited cleanly", 2, ""},
	}

	out := make([]*store.Runner, 0, len(specs))
	for i, s := range specs {
		pool := pools[s.pool]
		host := hosts[s.host]
		created := now.Add(-time.Duration(s.ageMin) * time.Minute)
		r := &store.Runner{
			ID:        fmt.Sprintf("run_demo%02d", i),
			PoolID:    pool.ID,
			HostID:    host.ID,
			Name:      demoRunnerName(pool, i),
			State:     s.state,
			Ephemeral: pool.Ephemeral,
			Labels:    pool.Labels,
			Image:     pool.Image,
			// The size the runner was created with, which is what its page
			// says it is held at. Both demo pools type a size, so it is
			// theirs; a pool that named none would carry the host's share.
			AllocatedCPUs:     pool.Resources.CPUs,
			AllocatedMemoryMB: pool.Resources.MemoryMB,
			AllocationSource:  store.AllocationFromPool,
			ContainerID:       fmt.Sprintf("demo%032d", i),
			Message:           s.message,
			FaultKind:         s.fault,
			JobsHandled:       s.jobs,
			CPUPercent:        float64((i*17)%90) + 1,
			MemoryBytes:       int64(256+i*64) << 20,
			GitHubRunnerID:    int64(9000 + i),
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

// seedBacklog tops a pool's queue up until every runner in it that is not
// already working is explained by a job waiting for one.
//
// The fixture is a snapshot; the reconcile loop reads it as a fleet. A pool
// holding more runners than its queue justifies has the ones that have not
// finished starting drained on the very first pass -- "queued demand
// disappeared before this runner finished starting" -- so the two states the
// demo exists to show would be gone a second after it was seeded, the
// screenshots would lose them, and the diagnostics fixture that ages them into
// stuck runners would have nothing to age. Freshening them cannot help: they
// are drained before the first refresh, and a fixture that fought the loop
// every tick would be a fleet nobody could read.
//
// One queued job per runner that is neither busy nor draining is what makes
// the snapshot add up. It is the demand the scheduler would have created those
// runners for, and it is what the provisioning runner's own message already
// claims.
func (c *Controller) seedBacklog(ctx context.Context, now time.Time, pool *store.Pool, runners []*store.Runner, first int) (int, error) {
	waiting := 0
	for _, r := range runners {
		if r.PoolID != pool.ID || !r.State.Live() {
			continue
		}
		if r.State != store.RunnerBusy && r.State != store.RunnerDraining {
			waiting++
		}
	}
	_, queued, err := c.st.ListJobs(ctx, store.JobFilter{
		PoolIDs: []string{pool.ID},
		States:  []store.JobState{store.JobQueued},
	}, store.Page{Limit: 1})
	if err != nil {
		return 0, fmt.Errorf("counting the queue pool %s already has: %w", pool.Name, err)
	}
	written := 0
	for i := queued; i < waiting; i++ {
		n := first + written
		repo := demoRepos[n%len(demoRepos)]
		run := int64(41000 + n)
		j := &store.Job{
			ID:             fmt.Sprintf("job_demo%03d", n),
			GitHubJobID:    int64(80000 + n),
			GitHubRunID:    run,
			RunNumber:      int64(500 + n),
			Repo:           repo,
			Workflow:       "CI",
			JobName:        demoBacklogJobNames[n%len(demoBacklogJobNames)],
			Labels:         pool.Labels,
			InstallationID: pool.InstallationID,
			PoolID:         pool.ID,
			Matched:        true,
			State:          store.JobQueued,
			// Older than the scale-up delay, or the pool would not have acted
			// on them and the runners they explain would be surplus again.
			QueuedAt:   now.Add(-time.Duration(40+written*17) * time.Second),
			HTMLURL:    fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, run),
			HeadBranch: "main",
			HeadSHA:    fmt.Sprintf("%040x", 0xC0FFEE+n*7919),
			RunAttempt: 1,
		}
		saved, change, err := c.st.ApplyJob(ctx, j)
		if err != nil {
			return written, fmt.Errorf("seeding the backlog job %s: %w", j.ID, err)
		}
		if err := c.seedJobTimeline(ctx, saved, change); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// demoBacklogJobNames keeps the backlog reading like a morning's work rather
// than one job name repeated.
var demoBacklogJobNames = []string{"build", "test", "lint"}

// seedBacklogFirstJob is where the backlog's identifiers start, past every job
// the fixture writes by hand.
const seedBacklogFirstJob = 52

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
		// Two jobs to a run, and everything a run has in common -- the
		// repository, the workflow, the branch, the commit -- is chosen by the
		// run rather than by the job. The Workflows page lists runs, and a run
		// whose two jobs named different repositories was listed as two.
		run := i / 2
		repo := repos[run%len(repos)]
		j := &store.Job{
			ID:             fmt.Sprintf("job_demo%03d", i),
			GitHubJobID:    int64(80000 + i),
			GitHubRunID:    int64(40000 + run),
			RunNumber:      int64(300 + run),
			Repo:           repo,
			Workflow:       workflows[run%len(workflows)],
			JobName:        jobNames[i%len(jobNames)],
			Labels:         pool.Labels,
			InstallationID: pool.InstallationID,
			PoolID:         pool.ID,
			Matched:        true,
			QueuedAt:       queued,
			HTMLURL:        fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, 40000+run),
			HeadBranch:     branches[run%len(branches)],
			HeadSHA:        fmt.Sprintf("%040x", 0xC0FFEE+run*7919),
			RunAttempt:     1 + i%7/6,
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
			// recent finished job, so that it remains well inside the hour the
			// problems drawer looks back over after both browser projects and a
			// retry have run.
			lostRunner := i == 43
			if lostRunner {
				j.Conclusion = "failure"
				started = now.Add(-25 * time.Minute)
				completed = now.Add(-20 * time.Minute)
			}
			j.StartedAt, j.CompletedAt = &started, &completed
			r := runners[i%12]
			j.RunnerID, j.RunnerName = r.ID, r.Name
			j.Steps = demoSteps(j.JobName, j.Conclusion, started, completed)
			if lostRunner {
				j.RunnerFault = fmt.Sprintf("runner %s stopped while this job was running: runner exited with code 137: the container was killed for exceeding its memory limit", r.Name)
				j.FaultKind = store.FaultOutOfMemory
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

		// Claimed jobs carry the release that ran them, as a fleet upgraded
		// twice does: the oldest predate the stamp and carry none, the next
		// were run by v1.3.2 and the rest by v1.3.3. The Usage page's
		// release table and the job drawer both need something to show, and
		// a fleet with one release would never show the table comparing two.
		if j.Matched {
			switch {
			case i < 15:
			case i < 30:
				j.ControllerVersion, j.ControllerChannel, j.AgentVersion = "v1.3.2", "stable", "v1.3.2"
			default:
				j.ControllerVersion, j.ControllerChannel, j.AgentVersion = "v1.3.3", "stable", "v1.3.3"
			}
			if j.ControllerVersion != "" {
				for _, r := range runners {
					if r.ID == j.RunnerID {
						j.HostID = r.HostID
					}
				}
			}
		}

		saved, change, err := c.st.ApplyJob(ctx, j)
		if err != nil {
			return fmt.Errorf("seeding job %d: %w", i, err)
		}
		// The size of the runner that took the job, as a controller that
		// records it does: the jobs the stamped releases ran, half of them on
		// a share of their host and half on a standard size an operator named,
		// so the job drawer has both sentences to show.
		if j.Matched && j.ControllerVersion != "" {
			cpus, source := 4.0, store.AllocationFromHost
			if i%2 == 1 {
				cpus, source = 3, store.AllocationFromProfile
			}
			if _, err := c.st.StampJobGranted(ctx, saved.ID, cpus, 8192, source); err != nil {
				return fmt.Errorf("seeding job %d's granted size: %w", i, err)
			}
		}
		if err := c.seedJobTimeline(ctx, saved, change); err != nil {
			return err
		}
	}

	// The vendor repository's other job: one running right now, on a machine
	// this fleet has never seen. GitHub reports it because the installation
	// covers the repository, and the Overview's panels must not count it as
	// what the fleet is doing -- which is exactly what they did.
	vendorStarted := now.Add(-3 * time.Minute)
	vendor := &store.Job{
		ID:             "job_demo050",
		GitHubJobID:    80050,
		GitHubRunID:    40025,
		RunNumber:      325,
		Repo:           repos[2],
		Workflow:       "CI",
		JobName:        "images",
		Labels:         store.StringSlice{"blacksmith-4vcpu-ubuntu-2404"},
		InstallationID: demoInstallationID,
		State:          store.JobInProgress,
		QueuedAt:       vendorStarted.Add(-9 * time.Second),
		StartedAt:      &vendorStarted,
		RunnerName:     "blacksmith-4vcpu-ubuntu-2404-3a71",
		HTMLURL:        fmt.Sprintf("https://github.com/%s/actions/runs/%d", repos[2], 40025),
		HeadBranch:     "main",
		HeadSHA:        fmt.Sprintf("%040x", 0xC0FFEE+50*7919),
		RunAttempt:     1,
		Steps:          demoSteps("images", "", vendorStarted, time.Time{}),
	}
	saved, change, err := c.st.ApplyJob(ctx, vendor)
	if err != nil {
		return fmt.Errorf("seeding the vendor job: %w", err)
	}
	if err := c.seedJobTimeline(ctx, saved, change); err != nil {
		return err
	}

	// And its third: one queued, in the seconds between GitHub reporting it and
	// the vendor picking it up. It is here because it is the case the default
	// view got wrong -- a job nothing here claims is normally this fleet's to
	// see, and this one never was.
	queuedVendor := &store.Job{
		ID:             "job_demo051",
		GitHubJobID:    80051,
		GitHubRunID:    40026,
		RunNumber:      326,
		Repo:           repos[2],
		Workflow:       "CI",
		JobName:        "package",
		Labels:         store.StringSlice{"blacksmith-4vcpu-ubuntu-2404"},
		InstallationID: demoInstallationID,
		State:          store.JobQueued,
		QueuedAt:       now.Add(-11 * time.Second),
		HTMLURL:        fmt.Sprintf("https://github.com/%s/actions/runs/%d", repos[2], 40026),
		HeadBranch:     "main",
		HeadSHA:        fmt.Sprintf("%040x", 0xC0FFEE+51*7919),
		RunAttempt:     1,
	}
	saved, change, err = c.st.ApplyJob(ctx, queuedVendor)
	if err != nil {
		return fmt.Errorf("seeding the queued vendor job: %w", err)
	}
	if err := c.seedJobTimeline(ctx, saved, change); err != nil {
		return err
	}

	// And the queue that explains the runners this fleet has not finished
	// starting: without it the reconcile loop drains them on its first pass.
	next := seedBacklogFirstJob
	for _, pool := range pools {
		written, err := c.seedBacklog(ctx, now, pool, runners, next)
		if err != nil {
			return err
		}
		next += written
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

		// Keep the burst near the end of the fixture hour. The browser suite shares
		// a live controller for long enough that an early burst aged out of its
		// one-hour view before the mobile project reached it.
		var queued, running, total int
		switch {
		case i < 40:
			queued = jitter(rng, 0, 1)
			running = jitter(rng, 1, 2)
			total = 2 + running
		case i < 52:
			// Nothing idle through the burst, and that is the point: the queue
			// is deep precisely because every runner is taken. A demo whose
			// busiest ten minutes still showed four free runners would say the
			// scheduler had simply not bothered.
			queued = jitter(rng, 4, 9)
			running = jitter(rng, 2, 4)
			total = running
		case i < 58:
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
			At:          at,
			QueuedJobs:  queued,
			RunningJobs: running,
			// Every job the demo seeds belongs to one of its own pools, so the
			// fleet's own figures are the same figures. Leaving them at zero
			// would give the Overview a flat sparkline in its default view and
			// make the demo look broken.
			FleetQueuedJobs:  queued,
			FleetRunningJobs: running,
			IdleRunners:      idle,
			BusyRunners:      busy,
			TotalRunners:     total,
		}); err != nil {
			return fmt.Errorf("seeding the fleet sample for %s: %w", at.Format(time.RFC3339), err)
		}
	}
	return nil
}

// jitter returns a value in [lo, hi]. The fixtures use a seeded source, so the
// shape is the same on every run and a screenshot taken today matches one taken
// last week.
// demoHostUsage is what a demo host measured at `at`: a working day drawn
// as a curve rather than a random walk, so the capacity map shows the shape
// an operator recognises -- quiet overnight, climbing from nine, a lunchtime
// dip, an afternoon peak -- and each host sits on its own band of it. The
// third host is the cordoned one and idles. Deterministic, so two controllers
// seeded in the same minute draw the same chart.
func demoHostUsage(cpus int, memoryMB int64, host int, at, now time.Time) store.HostUsage {
	local := at.In(time.Local)
	hour := float64(local.Hour()) + float64(local.Minute())/60
	// The day's shape, 0..1.
	var day float64
	switch {
	case hour < 7:
		day = 0.08
	case hour < 9:
		day = 0.08 + (hour-7)/2*0.5
	case hour < 12.5:
		day = 0.58 + (hour-9)/3.5*0.3
	case hour < 13.5:
		day = 0.55
	case hour < 17:
		day = 0.6 + (hour-13.5)/3.5*0.35
	case hour < 20:
		day = 0.95 - (hour-17)/3*0.7
	default:
		day = 0.25 - (hour-20)/4*0.17
	}
	// Ten-minute texture on top, so the line reads as a measurement and not
	// a drawing; the phase differs per host so their peaks do not coincide.
	minute := float64(at.Unix()/60) + float64(host*7)
	texture := 0.06*math.Sin(minute/10*2*math.Pi/6) + 0.04*math.Sin(minute/3.3)
	band := []float64{0.9, 0.7, 0.2}[host%3]
	cpu := math.Max(1, math.Min(92, 100*(day*band+texture+0.05)))
	// The most recent point is the one the card shows, and the throttle
	// ladder reads it too: the first builder is under real pressure at the
	// afternoon peak and nowhere else.
	load := cpu / 100 * float64(cpus) * 1.15
	memUsed := 0.25 + 0.55*day*band + texture/2
	available := int64(float64(memoryMB) * (1 - memUsed))
	if available < 256 {
		available = 256
	}
	return store.HostUsage{
		CPUPercent:        &cpu,
		LoadAverage1:      &load,
		MemoryAvailableMB: &available,
		SampledAt:         now,
	}
}

// seedHostSamples writes a day of per-minute history for every demo host, so
// the Hosts page's capacity map has a past to draw on a controller that was
// started a moment ago. The runner counts follow the same curve as the
// measurements: a host is busy because its slots are.
func (c *Controller) seedHostSamples(ctx context.Context, now time.Time, hosts []*store.Host, runners []*store.Runner) error {
	const minutes = 24 * 60
	// The closing hour carries the runners the fixture actually has, so the
	// chart's history meets its live edge instead of stepping to it: the
	// newest point is drawn from the fleet, and a line that jumps there
	// reads as a fault in the chart rather than a fact about the host.
	live := map[string]int{}
	for _, r := range runners {
		if !r.State.Terminal() {
			live[r.HostID]++
		}
	}
	start := now.Add(-minutes * time.Minute).Truncate(time.Minute)
	samples := make([]store.HostSample, 0, (minutes+1)*len(hosts))
	for i, h := range hosts {
		alloc := h.Allocatable()
		for m := 0; m <= minutes; m++ {
			at := start.Add(time.Duration(m) * time.Minute)
			usage := demoHostUsage(h.CPUs, h.MemoryMB, i, at, at)
			busy := int(math.Round(*usage.CPUPercent / 100 * float64(h.Capacity)))
			if h.Cordoned {
				busy = 0
			}
			// Over the last hour the curve's count gives way to the fixture's
			// real one, so the line arrives at the live edge rather than
			// stepping to it.
			if left := minutes - m; left <= 60 {
				t := math.Min(1, float64(60-left)/45)
				busy = int(math.Round(float64(busy) + (float64(live[h.ID])-float64(busy))*t))
			}
			reservedCPUs := float64(busy) * 2
			reservedMem := int64(busy) * 4096
			samples = append(samples, store.HostSample{
				HostID:              h.ID,
				At:                  at,
				Capacity:            h.Capacity,
				ActiveRunners:       busy,
				CPUPercent:          usage.CPUPercent,
				LoadAverage1:        usage.LoadAverage1,
				CPUs:                int64(h.CPUs),
				MemoryMB:            h.MemoryMB,
				MemoryAvailableMB:   usage.MemoryAvailableMB,
				AllocatableCPUs:     alloc.CPUs,
				AllocatableMemoryMB: alloc.MemoryMB,
				ReservedCPUs:        &reservedCPUs,
				ReservedMemoryMB:    &reservedMem,
				DiskTotalMB:         h.DiskTotalMB,
				// Disk fills slowly through the day: the caches a runner
				// leaves behind, which is what the second builder is short of.
				DiskFreeMB: h.DiskFreeMB + int64(minutes-m)*8,
			})
		}
	}
	if err := c.st.RecordHostSamples(ctx, samples); err != nil {
		return fmt.Errorf("seeding host samples: %w", err)
	}
	return nil
}

func jitter(rng *rand.Rand, lo, hi int) int {
	if hi <= lo {
		return lo
	}
	return lo + rng.IntN(hi-lo+1)
}
