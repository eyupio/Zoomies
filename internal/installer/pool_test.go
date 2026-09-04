package installer

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// poolFixture is a non-interactive installer writing into a scratch database,
// which is all stepFirstPool needs: it asks nothing when not interactive.
func poolFixture(t *testing.T) (*Installer, *store.Store, *config.Config, Plan) {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	i := &Installer{
		out: &bytes.Buffer{},
		ui:  newUI(&bytes.Buffer{}),
		det: Detection{OS: "linux", Arch: "amd64", Hostname: "runner-1"},
	}
	p := Plan{Mode: ModeSingle, Backend: store.BackendDocker, Capacity: 4}
	return i, st, config.Default(), p
}

// installedInstallation records the App the pool will belong to, because a
// pool with no installation is not a pool the API would accept either.
func installedInstallation(t *testing.T, st *store.Store) *store.Installation {
	t.Helper()
	inst := &store.Installation{
		AppID: 1, InstallationID: 2, Target: "acme", TargetType: store.TargetOrg,
		APIBaseURL: "https://api.github.com",
	}
	if err := st.CreateInstallation(context.Background(), inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	return inst
}

func TestFirstPoolMakesTheInstallHostUsable(t *testing.T) {
	ctx := context.Background()
	i, st, cfg, p := poolFixture(t)
	inst := installedInstallation(t, st)

	if err := i.stepFirstPool(ctx, st, cfg, &p); err != nil {
		t.Fatalf("stepFirstPool: %v", err)
	}

	pools, err := st.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("got %d pools, want exactly one", len(pools))
	}
	got := pools[0]
	if got.Name != "zoomies-linux-x64" {
		t.Errorf("pool name = %q, want the branded label a workflow's runs-on can ask for", got.Name)
	}
	if !slices.Equal([]string(got.Labels), []string{"zoomies", "zoomies-linux-x64"}) {
		t.Errorf("labels = %v, want the brand and this host's branded label", got.Labels)
	}
	if got.InstallationID != inst.ID {
		t.Errorf("installation = %q, want %q", got.InstallationID, inst.ID)
	}
	if got.Backend != store.BackendDocker {
		t.Errorf("backend = %q, want the one setup chose for this host", got.Backend)
	}
	if got.MaxRunners != 4 {
		t.Errorf("max_runners = %d, want the host's capacity", got.MaxRunners)
	}
	if !got.Ephemeral {
		t.Error("the pool must be ephemeral: one job per runner is what keeps workflows out of each other")
	}
	if !got.Enabled {
		t.Error("a pool created by setup that is not enabled runs nothing, which is the bug this step exists to fix")
	}
	if got.Image != cfg.GitHub.RunnerImage {
		t.Errorf("image = %q, want the configured runner image %q", got.Image, cfg.GitHub.RunnerImage)
	}
	// The summary reads this to say "you are ready" rather than printing a
	// command for a pool that now exists.
	if p.PoolName != "zoomies-linux-x64" {
		t.Errorf("plan.PoolName = %q, want the pool that was created", p.PoolName)
	}
}

// Re-running `zoomies init` on a live fleet must not add a second pool beside
// the ones an operator has since shaped by hand.
func TestFirstPoolLeavesAnExistingFleetAlone(t *testing.T) {
	ctx := context.Background()
	i, st, cfg, p := poolFixture(t)
	inst := installedInstallation(t, st)

	existing := &store.Pool{
		Name: "big-builders", InstallationID: inst.ID,
		Labels: store.StringSlice{"big-builders"}, Backend: store.BackendDocker,
		DockerMode: store.DockerNone, MaxRunners: 20, Enabled: true,
	}
	if err := st.CreatePool(ctx, existing); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}

	if err := i.stepFirstPool(ctx, st, cfg, &p); err != nil {
		t.Fatalf("stepFirstPool: %v", err)
	}
	pools, err := st.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 1 || pools[0].Name != "big-builders" {
		t.Fatalf("existing fleet was modified: %+v", pools)
	}
	if p.PoolName != "" {
		t.Errorf("plan.PoolName = %q, want empty when no pool was created", p.PoolName)
	}
}

// A pool belongs to an installation, so there is nothing to create until
// GitHub is connected. Setup says so rather than failing.
func TestFirstPoolWaitsForGitHub(t *testing.T) {
	ctx := context.Background()

	t.Run("github skipped", func(t *testing.T) {
		i, st, cfg, p := poolFixture(t)
		p.GitHub.Skip = true
		if err := i.stepFirstPool(ctx, st, cfg, &p); err != nil {
			t.Fatalf("stepFirstPool: %v", err)
		}
		assertNoPools(t, st)
	})

	t.Run("no installation recorded", func(t *testing.T) {
		i, st, cfg, p := poolFixture(t)
		if err := i.stepFirstPool(ctx, st, cfg, &p); err != nil {
			t.Fatalf("stepFirstPool: %v", err)
		}
		assertNoPools(t, st)
	})
}

// Only the single-host mode puts an agent on this machine; the other two have
// no runners here to describe.
func TestFirstPoolOnlyOnASingleHostInstall(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []Mode{ModeController, ModeAgent} {
		t.Run(string(mode), func(t *testing.T) {
			i, st, cfg, p := poolFixture(t)
			installedInstallation(t, st)
			p.Mode = mode
			if err := i.stepFirstPool(ctx, st, cfg, &p); err != nil {
				t.Fatalf("stepFirstPool: %v", err)
			}
			assertNoPools(t, st)
		})
	}
}

func TestFirstPoolHonoursTheAnswerFile(t *testing.T) {
	ctx := context.Background()

	t.Run("skip", func(t *testing.T) {
		i, st, cfg, p := poolFixture(t)
		installedInstallation(t, st)
		i.answers = &Answers{Pool: AnswersPool{Skip: true}}
		if err := i.stepFirstPool(ctx, st, cfg, &p); err != nil {
			t.Fatalf("stepFirstPool: %v", err)
		}
		assertNoPools(t, st)
	})

	t.Run("overrides", func(t *testing.T) {
		i, st, cfg, p := poolFixture(t)
		installedInstallation(t, st)
		i.answers = &Answers{Pool: AnswersPool{Name: "gpu", MaxRunners: 2}}
		if err := i.stepFirstPool(ctx, st, cfg, &p); err != nil {
			t.Fatalf("stepFirstPool: %v", err)
		}
		pools, err := st.ListPools(ctx)
		if err != nil {
			t.Fatalf("ListPools: %v", err)
		}
		if len(pools) != 1 {
			t.Fatalf("got %d pools, want one", len(pools))
		}
		if pools[0].Name != "gpu" || pools[0].MaxRunners != 2 {
			t.Fatalf("pool = %+v, want the answer file's name and cap", pools[0])
		}
		// A renamed pool that was given no labels answers to a branded form
		// of its new name, not the architecture label it would have had.
		if !slices.Equal([]string(pools[0].Labels), []string{"zoomies", "zoomies-gpu"}) {
			t.Fatalf("labels = %v, want [zoomies zoomies-gpu] to follow the rename", pools[0].Labels)
		}
	})
}

func assertNoPools(t *testing.T, st *store.Store) {
	t.Helper()
	pools, err := st.ListPools(context.Background())
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 0 {
		t.Fatalf("got %d pools, want none", len(pools))
	}
}
