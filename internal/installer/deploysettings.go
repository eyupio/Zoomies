package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// deploymentSettings is the configuration the deployment being upgraded runs
// with: its file, its database and its environment, layered as its controller
// layers them at start (config.Effective).
//
// The database is the point. Settings are kept there -- the settings page
// writes nowhere else -- so whether this host runs runners, on which backend
// and through which socket is a question for the deployment's database, and a
// file or .env read on its own answers it wrongly the moment an operator has
// used the page. The upgrade reads it read-only, which is safe beside the
// running controller for the same reason a backup is.
//
// read is false when the database could not be read, and why says what
// stood in the way; cfg is then what the file and environment say over the
// defaults, and the caller must treat anything the database could have
// changed as unknown rather than as that.
type deploymentSettings struct {
	cfg  *config.Config
	read bool
	why  string
	// cacheDirs are the host folders pools keep a size-limited cache in. The
	// agent keeps that limit by measuring the folder itself, so a container
	// deployment has to mount each one at its own path.
	cacheDirs []cacheDir
}

// cacheDir is one host folder pool caches live under, and the pools using it.
type cacheDir struct {
	path  string
	pools []string
}

// settings reads the deployment's configuration. It never fails the upgrade:
// the answer is used to decide what to offer, and an upgrade that cannot read
// it offers what a host that runs runners needs rather than nothing.
func (p *upgradePlan) settings(ctx context.Context) deploymentSettings {
	env := map[string]string{}
	file := ""
	var dbPath string
	switch p.record.Deployment {
	case DeploymentCompose, DeploymentDocker:
		// The container's environment is its .env; that is the layer the
		// controller in it reads last. DOCKER_GID and the like in there are
		// Compose's own, and config ignores what is not a setting.
		if p.record.EnvFile != "" {
			if parsed, err := ParseEnvFile(p.record.EnvFile); err == nil {
				env = parsed
			}
		}
		path, err := p.volumeDatabase(ctx)
		if err != nil {
			return p.unreadSettings(file, env, err.Error())
		}
		dbPath = path
	default:
		if candidate := filepath.Join(p.opts.ConfigDir, "zoomies.yaml"); exists(candidate) {
			file = candidate
		}
		base, _, err := config.Effective(file, nil, nil, nil)
		if err != nil {
			return p.unreadSettings("", env, err.Error())
		}
		dbPath = base.Database.Path
	}
	if !exists(dbPath) {
		// No database yet: nothing has been set on the settings page, so the
		// file and environment are the whole answer.
		cfg, _, err := config.Effective(file, nil, nil, env)
		if err != nil {
			return p.unreadSettings("", env, err.Error())
		}
		return deploymentSettings{cfg: cfg, read: true}
	}
	st, err := store.Open(ctx, store.Options{Path: dbPath, ReadOnly: true})
	if err != nil {
		return p.unreadSettings(file, env, fmt.Sprintf("opening %s: %v", dbPath, err))
	}
	defer func() { _ = st.Close() }()
	rows, err := st.InstanceSettings(ctx)
	if err != nil {
		return p.unreadSettings(file, env, fmt.Sprintf("reading the settings in %s: %v", dbPath, err))
	}
	// No key: nothing this reads is a secret, and a sealed row it cannot open
	// is a finding, not a failure.
	cfg, _, err := config.Effective(file, rows, nil, env)
	if err != nil {
		return p.unreadSettings(file, env, err.Error())
	}
	// A pool that cannot be listed costs only the cache mounts: the settings
	// above still decide everything else.
	pools, _ := st.ListPools(ctx)
	return deploymentSettings{cfg: cfg, read: true, cacheDirs: sizeLimitedCacheDirs(pools)}
}

// sizeLimitedCacheDirs is every absolute host folder a pool keeps a
// size-limited cache in, once each, in a stable order. A cache with no limit
// is left out: the host's daemon mounts it into runners on its own, and only
// enforcing a limit needs the agent to see inside it.
func sizeLimitedCacheDirs(pools []*store.Pool) []cacheDir {
	byPath := map[string]*cacheDir{}
	var order []string
	for _, pool := range pools {
		c := pool.Cache
		source := strings.TrimSpace(c.Source)
		if !c.Enabled || c.SizeLimit <= 0 || !filepath.IsAbs(source) || strings.Contains(source, "..") {
			continue
		}
		dir := filepath.Clean(source)
		if byPath[dir] == nil {
			byPath[dir] = &cacheDir{path: dir}
			order = append(order, dir)
		}
		byPath[dir].pools = append(byPath[dir].pools, pool.Name)
	}
	sort.Strings(order)
	out := make([]cacheDir, 0, len(order))
	for _, dir := range order {
		out = append(out, *byPath[dir])
	}
	return out
}

func (p *upgradePlan) unreadSettings(file string, env map[string]string, why string) deploymentSettings {
	cfg, _, err := config.Effective(file, nil, nil, env)
	if err != nil {
		cfg = config.Default()
	}
	return deploymentSettings{cfg: cfg, why: why}
}

// volumeDatabase is the host path of the database in a container
// deployment's data volume. The running container is asked where its state
// directory is mounted from, rather than the volume by name: Compose prefixes
// a volume's name with its project unless the file names it, so the name the
// record holds is not always the name the daemon knows.
func (p *upgradePlan) volumeDatabase(ctx context.Context) (string, error) {
	container := containerOr(p.record)
	format := `{{range .Mounts}}{{if eq .Destination "` + ContainerStateDir + `"}}{{.Source}}{{end}}{{end}}`
	out, err := p.docker(ctx, "inspect", "--type", "container", "--format", format, container)
	if err != nil {
		return "", fmt.Errorf("asking the %s container where its data is: %w", container, err)
	}
	dir := strings.TrimSpace(out)
	if dir == "" {
		return "", fmt.Errorf("the %s container mounts nothing at %s", container, ContainerStateDir)
	}
	if _, err := os.Stat(dir); err != nil {
		// Docker Desktop keeps volumes inside its own VM, where this host
		// cannot open them.
		return "", fmt.Errorf("its data is at %s, which this host cannot open: %w", dir, err)
	}
	return filepath.Join(dir, filepath.Base(ContainerDBPath)), nil
}

// runsRunners is whether this host creates runners itself: an agent always
// does, a controller when its embedded agent is on. Unknown -- the database
// could not be read -- counts as yes, because a folder or a mount offered to a
// controller that does not need it costs a question, and one not offered to a
// controller that does breaks its runners' tool cache.
func (s deploymentSettings) runsRunners(mode Mode) bool {
	if mode == ModeAgent {
		return true
	}
	if !s.read {
		return true
	}
	return s.cfg.Agent.Embedded
}
