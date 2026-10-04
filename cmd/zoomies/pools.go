package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// runPools is `zoomies pools ...`: everything the Pools page does, over the
// same routes it uses.
func runPools(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "pools", "What runners to make, and how many.", []*subcommand{
		{"list", "", "Every pool, with its live counts and utilisation", poolsList},
		{"get", "<pool-id>", "One pool in full, including any dangerous settings", poolsGet},
		{"create", "--name <n> --labels <l>", "Create a pool", poolsCreate},
		{"edit", "<pool-id> [flags]", "Change the settings you name and nothing else", poolsEdit},
		{"delete", "<pool-id>", "Delete a pool, draining its runners first", poolsDelete},
		{"enable", "<pool-id>", "Let a pool create runners again", poolsEnable},
		{"disable", "<pool-id>", "Stop creating runners; existing ones drain", poolsDisable},
		{"prewarm", "<pool-id>", "Pre-pull the image on every matching host", poolsPrewarm},
		{"export", "[--file pools.yaml]", "Write every pool to a file another instance can import", poolsExport},
		{"import", "<file> [--dry-run]", "Create and change pools to match an export, previewed first", poolsImport},
	}, args)
}

func poolsPrewarm(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies pools prewarm <pool-id>", "Pre-pull a pool image on every matching host.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a pool ID")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var out struct {
		Queued int `json:"queued"`
		Hosts  []struct {
			HostName string `json:"host_name"`
			State    string `json:"state"`
			Digest   string `json:"digest"`
			Error    string `json:"error"`
		} `json:"hosts"`
	}
	raw, err := client.post(ctx, "/pools/"+url.PathEscape(id)+"/prewarm", nil, nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.note(fmt.Sprintf("Queued image prewarm on %d host(s).", out.Queued))
	for _, h := range out.Hosts {
		p.note(fmt.Sprintf("%s: %s %s%s", h.HostName, h.State, h.Digest, h.Error))
	}
	return nil
}

func poolsList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies pools list [--output table|json|yaml]", "List every pool with its live runner counts.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies pools list", "zoomies pools list --output json | jq '.items[].name'")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	var out listResponse[poolItem]
	raw, err := client.get(ctx, "/pools", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No pools yet. Create one with: zoomies pools create --name zoomies-4vcpu-ubuntu-2404 " +
			"--labels zoomies-4vcpu-ubuntu-2404 --cpus 4 --os ubuntu --os-version 24.04 --installation <id>")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, item := range out.Items {
		enabled := p.paint(colourGreen, "yes")
		if !item.Enabled {
			enabled = p.paint(colourDim, "no")
		}
		// A pool the controller keeps is out of use for one of two reasons, and
		// they are different things to do something about.
		if a := item.Auto; a != nil && !item.Enabled {
			enabled = p.paint(colourDim, "paused")
			if !a.Paused {
				enabled = p.paint(colourDim, "no hosts")
			}
		}
		rows = append(rows, []string{
			item.Name,
			item.ID,
			truncate(strings.Join(item.Labels, ","), 30),
			item.Backend,
			item.Platform.String(),
			strconv.Itoa(item.Priority),
			fmt.Sprintf("%d/%d", item.Counts.Live, item.MaxRunners),
			strconv.Itoa(item.Counts.Busy),
			strconv.Itoa(item.QueuedJobs),
			p.bar(item.Utilisation, 10),
			enabled,
		})
	}
	p.table([]string{"name", "id", "labels", "backend", "platform", "priority", "live/max", "busy", "queued", "utilisation", "enabled"}, rows)
	return nil
}

func poolsGet(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies pools get <pool-id>", "Show one pool in full.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a pool ID, as shown by `zoomies pools list`")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	var pool poolItem
	raw, err := client.get(ctx, "/pools/"+url.PathEscape(id), nil, &pool)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}

	rows := [][2]string{
		{"name", pool.Name},
		{"id", pool.ID},
		{"enabled", p.yesNo(pool.Enabled, false)},
		{"installation", dash(pool.InstallationTarget) + " (" + pool.InstallationID + ")"},
		{"labels", strings.Join(pool.Labels, ", ")},
		{"runner group", dash(pool.RunnerGroup)},
		{"backend", pool.Backend},
		{"platform", pool.Platform.String()},
		{"image", poolImage(pool)},
		{"runner version", dash(pool.RunnerVersion)},
		{"runners", fmt.Sprintf("%d live of %d max, %d minimum", pool.Counts.Live, pool.MaxRunners, pool.MinRunners)},
		{"priority", strconv.Itoa(pool.Priority)},
		{"states", fmt.Sprintf("%d idle, %d busy, %d draining, %d provisioning, %d failed",
			pool.Counts.Idle, pool.Counts.Busy, pool.Counts.Draining, pool.Counts.Provisioning, pool.Counts.Failed)},
		{"queued jobs", strconv.Itoa(pool.QueuedJobs)},
		{"utilisation", fmt.Sprintf("%s %.0f%%", p.bar(pool.Utilisation, 12), pool.Utilisation*100)},
		{"idle timeout", pool.IdleTimeout},
		{"ephemeral", p.yesNo(pool.Ephemeral, false)},
		{"docker mode", pool.DockerMode},
		{"run as root", p.yesNo(pool.RunAsRoot, true)},
		{"default labels", p.yesNo(!pool.NoDefaultLabels, false)},
		{"host selector", dash(kvValue(pool.HostSelector).String())},
		{"sizing", poolSizing(pool)},
		{"elastic CPU", poolCPUBurstLabel(pool)},
		{"in memory", poolTmpfsLabel(pool)},
		{"created", p.relTime(pool.CreatedAt)},
		{"updated", p.relTime(pool.UpdatedAt)},
	}
	if a := pool.Auto; a != nil {
		rows = append(rows,
			[2]string{"kept by the controller", plain(a.Summary)},
			[2]string{"asked of it", autoAsk(a)})
	}
	p.keyValues(rows)
	printProblems(p, pool.Warnings, "This pool has settings that weaken the defaults:")
	return nil
}

// autoAsk says what an operator has asked of a pool the controller keeps.
func autoAsk(a *poolAuto) string {
	var parts []string
	if a.Warm > 0 {
		parts = append(parts, fmt.Sprintf("%s kept warm", plural(a.Warm, "runner")))
	}
	if a.Cap > 0 {
		parts = append(parts, fmt.Sprintf("at most %s", plural(a.Cap, "runner")))
	}
	if a.Paused {
		parts = append(parts, "paused")
	}
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// poolSizing says what one runner of this pool is given, in the wizard's own
// words, because "cpus 0" reads as unlimited and it is the opposite.
func poolSizing(pool poolItem) string {
	out := poolSizingBase(pool)
	if share := pool.Resources.DaemonSharePercent; share > 0 && pool.Sizing != "fixed" {
		out += fmt.Sprintf("; the Docker daemon takes %d%% of a slot, the runner %d%%", share, 100-share)
	}
	return out
}

func poolSizingBase(pool poolItem) string {
	if pool.Sizing == "profile" || pool.SizeFromProfile {
		out := "profile: the standard size each host sets"
		if std := pool.FleetStandard; std.CPUs > 0 || std.MemoryMB > 0 {
			out += fmt.Sprintf(", or the fleet's default of %s CPUs and %d MB where a host sets none",
				strconv.FormatFloat(std.CPUs, 'f', -1, 64), std.MemoryMB)
		}
		return out
	}
	if pool.Sizing == "fixed" || pool.Resources.CPUs > 0 || pool.Resources.MemoryMB > 0 {
		var parts []string
		if pool.Resources.CPUs > 0 {
			parts = append(parts, strconv.FormatFloat(pool.Resources.CPUs, 'f', -1, 64)+" CPUs")
		}
		if pool.Resources.MemoryMB > 0 {
			parts = append(parts, strconv.FormatInt(pool.Resources.MemoryMB, 10)+" MB")
		}
		return "fixed: " + strings.Join(parts, ", ") + " on every host"
	}
	return "automatic: one slot's share of each host"
}

// poolTmpfsLabel says which of the runner's folders the pool keeps in memory,
// with the ceiling beside each, because "on" alone does not say how much memory
// the pool is spending.
func poolTmpfsLabel(pool poolItem) string {
	var parts []string
	for _, m := range []struct {
		name  string
		mount poolTmpfsMount
	}{{"_work", pool.Tmpfs.Work}, {"/tmp", pool.Tmpfs.Tmp}, {"Docker image store", pool.Tmpfs.Daemon}} {
		if !m.mount.Enabled {
			continue
		}
		how := "sized from the memory limit"
		if m.mount.SizeMB > 0 {
			how = fmt.Sprintf("%d MB", m.mount.SizeMB)
		}
		if m.mount.Auto {
			// Said beside the size, because "in memory" is not what an automatic
			// folder promises: on a runner too small for it, it is on disk.
			how += ", auto: on disk where a runner is too small"
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", m.name, how))
	}
	if len(parts) == 0 {
		return "no"
	}
	return strings.Join(parts, ", ")
}

// poolCPUBurstLabel renders the elastic CPU policy the way the pool wizard
// offers it, with the ceiling beside the mode when there is one.
func poolCPUBurstLabel(pool poolItem) string {
	mode := pool.CPUBurst.Mode
	if mode == "" || mode == "off" {
		return "off"
	}
	label := mode
	if mode == "observe" {
		label = "observe only"
	}
	if mode == "automatic" && pool.CPUBurst.SizeForCeiling != nil && !*pool.CPUBurst.SizeForCeiling {
		label += ", builds sized for the guarantee"
	}
	if pool.CPUBurst.MaxCPUs > 0 {
		return fmt.Sprintf("%s, up to %s CPUs per runner", label, strconv.FormatFloat(pool.CPUBurst.MaxCPUs, 'f', -1, 64))
	}
	return label + ", up to the host's allocatable CPU"
}

// poolImage says which image this pool's runners will boot, and where that
// came from. A pool that pins nothing is the common case now that a platform
// picks the variant, and "-" would leave an operator guessing at the single
// most important thing about their runners.
func poolImage(pool poolItem) string {
	switch {
	case pool.Image != "":
		return pool.Image
	case pool.EffectiveImage != "":
		return pool.EffectiveImage + " (from the pool's platform)"
	}
	return "-"
}

// poolSpec holds the flags shared by create and edit. Keeping them in one place
// is what makes `edit` accept exactly the settings `create` does.
type poolSpec struct {
	name         *string
	installation *string
	backend      *string
	image        *string
	pullPolicy   *string
	version      *string
	group        *string
	idleTimeout  *string
	dockerMode   *string
	labels       *listValue
	hostSelector kvValue
	envVars      kvValue
	minRunners   *int
	maxRunners   *int
	priority     *int
	ephemeral    *bool
	runAsRoot    *bool
	noDefault    *bool
	enabled      *bool
	cpus         *float64
	memoryMB     *int64
	diskGB       *int64
	pidsLimit    *int64
	sizeFromHost *bool
	// current is the size the pool has now, so that an edit touching one part
	// of it carries the rest forward rather than clearing it. Zero on a
	// create, where there is nothing to carry.
	current poolResources
	// daemonShare is --daemon-share, the daemon's part of a split slot.
	daemonShare *int
	// currentBurst is the elastic CPU policy the pool has now, kept for the
	// same reason: a ceiling typed alone must not switch the mode off.
	currentBurst poolCPUBurst
	// currentTmpfs is the pool's in-memory folders as they stand, for the same
	// reason: a size typed for one folder must not switch the other off.
	currentTmpfs poolTmpfs
	os           *string
	osVersion    *string
	arch         *string
	cacheEnabled *bool
	cacheScope   *string
	cacheSize    *int64
	cacheSource  *string
	cacheRepo    *string
	// The runner timings this pool overrides of the fleet's. Empty means the
	// pool follows the fleet, and on an edit an empty string clears an
	// override rather than setting one -- which is the only way to say "stop
	// overriding this" from a flag.
	provisionTimeout  *string
	drainTimeout      *string
	maxRunnerLifetime *string
	scaleUpDelay      *string
	dockerWait        *string
	cpuBurst          *string
	cpuBurstMax       *float64
	sizeBuilds        *bool
	tmpfsWork         *bool
	tmpfsWorkSize     *int64
	tmpfsTmp          *bool
	tmpfsTmpSize      *int64
	tmpfsDocker       *bool
	tmpfsDockerSize   *int64
	tmpfsAuto         *bool
}

// registerPoolFlags declares them, with the API's own defaults so that a
// created pool is the same whether it came from here or from the wizard.
func registerPoolFlags(fs *flagSet) *poolSpec {
	spec := &poolSpec{
		labels:       &listValue{},
		hostSelector: kvValue{},
		envVars:      kvValue{},
	}
	spec.name = fs.String("name", "", "the pool's name; it is stored with the zoomies- prefix, e.g. zoomies-4vcpu-ubuntu-2404")
	spec.installation = fs.String("installation", "", "the GitHub App installation this pool registers runners with")
	fs.Var(spec.labels, "labels", "the labels a workflow's runs-on must ask for (repeatable, or comma-separated)")
	spec.backend = fs.String("backend", "docker", "docker, podman or process")
	spec.image = fs.String("image", "", "runner image (default: the variant --os selects, else the controller's github.runner_image)")
	spec.pullPolicy = fs.String("pull-policy", "if-not-present", "if-not-present, always, or pinned-only")
	spec.version = fs.String("runner-version", "", "pin the actions/runner release")
	spec.group = fs.String("runner-group", "", "the GitHub runner group to register into")
	spec.minRunners = fs.Int("min", 0, "runners to keep even when nothing is queued")
	spec.maxRunners = fs.Int("max", 4, "the most runners this pool may have at once")
	spec.priority = fs.Int("priority", 0, "scheduling priority; higher-priority pools receive create slots first")
	spec.idleTimeout = fs.String("idle-timeout", "5m", "how long an idle runner waits before being drained")
	spec.ephemeral = fs.Bool("ephemeral", true, "one job per runner; the safe default")
	spec.dockerMode = fs.String("docker-mode", "none", "none, dind or host-socket (host-socket gives jobs root on the host)")
	spec.runAsRoot = fs.Bool("run-as-root", false, "run job steps as root inside the runner")
	spec.noDefault = fs.Bool("no-default-labels", false, "register runners without self-hosted, os and arch labels; needs --ephemeral=false")
	spec.enabled = fs.Bool("enabled", true, "whether the pool may create runners")
	fs.Var(spec.hostSelector, "host-selector", "only use hosts that match, e.g. arch=arm64 or os=windows; os and arch need no label")
	fs.Var(spec.envVars, "env", "environment variables for every job in this pool, e.g. HTTP_PROXY=...")
	spec.cpus = fs.Float64("cpus", 0, "CPU limit per runner; 0 leaves the size to the host, which gives each runner one slot's share of its machine")
	spec.memoryMB = fs.Int64("memory-mb", 0, "memory limit per runner, in MiB; 0 leaves the size to the host")
	spec.daemonShare = fs.Int("daemon-share", 0, "for a --docker-mode dind pool sized by its host: the percentage of one slot given to the Docker daemon, 10 to 90; 0 is an even split")
	spec.sizeFromHost = fs.Bool("size-from-host", false, "take each runner's size from the host it lands on, as that host's runner sizes say (see zoomies hosts edit); a pool does this or states a size, so it cannot be combined with --cpus or --memory-mb")
	spec.diskGB = fs.Int64("disk-gb", 0, "disk limit per runner, in GiB")
	spec.pidsLimit = fs.Int64("pids-limit", 0, "the container's pids cgroup limit (0 is no limit)")
	spec.os = fs.String("os", "", "the distribution these runners need: ubuntu, debian, fedora or rocky. It picks the runner image and restricts placement to hosts that match")
	spec.osVersion = fs.String("os-version", "", "the release, e.g. 24.04")
	spec.arch = fs.String("arch", "", "amd64 or arm64")
	spec.cacheEnabled = fs.Bool("cache", false, "mount a disposable performance cache (not workflow storage)")
	spec.cacheScope = fs.String("cache-scope", "pool", "cache isolation: pool or repository")
	spec.cacheSize = fs.Int64("cache-size", 0, "cache limit in bytes, enforced by eviction; needs an absolute cache-source (0 is unlimited)")
	spec.cacheSource = fs.String("cache-source", "", "absolute host path or named-volume prefix")
	spec.cacheRepo = fs.String("cache-repository", "", "owner/name for a repository-scoped cache under an organisation installation")
	spec.provisionTimeout = fs.String("provision-timeout", "", "override scheduler.provision_timeout for this pool, e.g. 30m (empty follows the fleet; 0 never gives up)")
	spec.drainTimeout = fs.String("drain-timeout", "", "override scheduler.drain_timeout for this pool (empty follows the fleet; 0 leaves a drain unbounded)")
	spec.maxRunnerLifetime = fs.String("max-runner-lifetime", "", "override scheduler.max_runner_lifetime for this pool (empty follows the fleet; 0 is no limit)")
	spec.scaleUpDelay = fs.String("scale-up-delay", "", "override scheduler.scale_up_delay for this pool (empty follows the fleet; 0 scales the moment a job is queued)")
	spec.dockerWait = fs.String("docker-wait", "", "override runners.docker_wait for this pool's runners (empty follows the fleet)")
	spec.cpuBurst = fs.String("cpu-burst", "", "elastic CPU: off, observe (measure without moving a quota) or automatic (lend spare host CPU to busy runners); needs automatic sizing on docker or podman")
	spec.sizeBuilds = fs.Bool("cpu-burst-size-builds", true, "with --cpu-burst automatic, start runners with CARGO_BUILD_JOBS, DOTNET_PROCESSOR_COUNT and the JVM's processor count set to the ceiling, so a build has workers for CPU lent after it started")
	spec.cpuBurstMax = fs.Float64("cpu-burst-max", 0, "the most CPU one runner may be lent up to, in cores; 0 is the host's allocatable CPU")
	spec.tmpfsWork = fs.Bool("tmpfs-work", false, "keep the runner's _work folder in memory instead of on the host's disk; the folder is charged to the runner's memory limit; needs docker or podman")
	spec.tmpfsWorkSize = fs.Int64("tmpfs-work-size", 0, "the _work folder's ceiling in MiB (at least 64); 0 sizes it from the memory limit, at most 4096 and half the limit")
	spec.tmpfsTmp = fs.Bool("tmpfs-tmp", false, "keep /tmp in memory as well; off unless asked for, because some jobs leave gigabytes there")
	spec.tmpfsTmpSize = fs.Int64("tmpfs-tmp-size", 0, "the /tmp folder's ceiling in MiB (at least 64); 0 sizes it from the memory limit, at most 1024")
	spec.tmpfsDocker = fs.Bool("tmpfs-docker", false, "keep the Docker-in-Docker sidecar's image store in memory; needs --docker-mode dind; an image bigger than the store does not pull, and it is charged to the sidecar's memory limit")
	spec.tmpfsAuto = fs.Bool("tmpfs-auto", false, "let each runner decide where the in-memory folders live: in memory where the runner has room for one to be useful (2 GB for _work, 1 GB for /tmp, 4 GB for the image store), on disk where it has not; --tmpfs-auto=false makes them always in memory")
	spec.tmpfsDockerSize = fs.Int64("tmpfs-docker-size", 0, "the image store's ceiling in MiB (at least 64); 0 sizes it from the sidecar's memory limit, at most 8192 and half of it")
	return spec
}

// body renders the flags as the request the API expects. When onlyChanged is
// set -- which is what `edit` wants -- a field the operator did not type is
// absent, so a PATCH cannot quietly reset a setting to this CLI's default.
func (spec *poolSpec) body(fs *flagSet, onlyChanged bool) map[string]any {
	body := map[string]any{}
	put := func(flagName, field string, value any) {
		if !onlyChanged || fs.changed(flagName) {
			body[field] = value
		}
	}
	put("name", "name", *spec.name)
	put("installation", "installation_id", *spec.installation)
	put("labels", "labels", []string(*spec.labels))
	put("backend", "backend", *spec.backend)
	put("pull-policy", "pull_policy", *spec.pullPolicy)
	put("min", "min_runners", *spec.minRunners)
	put("max", "max_runners", *spec.maxRunners)
	put("priority", "priority", *spec.priority)
	put("idle-timeout", "idle_timeout", *spec.idleTimeout)
	put("ephemeral", "ephemeral", *spec.ephemeral)
	put("docker-mode", "docker_mode", *spec.dockerMode)
	put("run-as-root", "run_as_root", *spec.runAsRoot)
	put("no-default-labels", "no_default_labels", *spec.noDefault)
	put("enabled", "enabled", *spec.enabled)
	// Sent whichever way it is, because a PATCH reads an absent key as "leave it
	// alone": `--size-from-host=false` is how an edit hands a pool back to the
	// share of its host, and has to reach the controller to do it.
	put("size-from-host", "size_from_profile", *spec.sizeFromHost)
	if !onlyChanged || fs.changed("cache") || fs.changed("cache-scope") || fs.changed("cache-size") ||
		fs.changed("cache-source") || fs.changed("cache-repository") {
		body["cache"] = map[string]any{
			"enabled": *spec.cacheEnabled, "scope": *spec.cacheScope, "size_limit": *spec.cacheSize,
			"source": *spec.cacheSource, "repository": *spec.cacheRepo,
		}
	}
	if fs.changed("image") {
		body["image"] = *spec.image
	}
	if fs.changed("runner-version") {
		body["runner_version"] = *spec.version
	}
	if fs.changed("runner-group") {
		body["runner_group"] = *spec.group
	}
	if fs.changed("host-selector") {
		body["host_selector"] = map[string]string(spec.hostSelector)
	}
	if fs.changed("env") {
		body["env"] = map[string]string(spec.envVars)
	}
	if fs.changed("os") || fs.changed("os-version") || fs.changed("arch") {
		body["platform"] = map[string]any{
			"os": *spec.os, "os_version": *spec.osVersion, "arch": *spec.arch,
		}
	}
	// The overrides go as a group, and only the ones typed. An empty string
	// is sent as null, which is how the API is told to hand a setting back to
	// the fleet: sending "" would be indistinguishable from not saying.
	settings := map[string]any{}
	for flagName, field := range map[string]string{
		"provision-timeout":   "provision_timeout",
		"drain-timeout":       "drain_timeout",
		"max-runner-lifetime": "max_runner_lifetime",
		"scale-up-delay":      "scale_up_delay",
		"docker-wait":         "docker_wait",
	} {
		if !fs.changed(flagName) {
			continue
		}
		var v *string
		switch field {
		case "provision_timeout":
			v = spec.provisionTimeout
		case "drain_timeout":
			v = spec.drainTimeout
		case "max_runner_lifetime":
			v = spec.maxRunnerLifetime
		case "scale_up_delay":
			v = spec.scaleUpDelay
		case "docker_wait":
			v = spec.dockerWait
		}
		if strings.TrimSpace(*v) == "" {
			settings[field] = nil
			continue
		}
		settings[field] = *v
	}
	if len(settings) > 0 {
		body["runner_settings"] = settings
	}
	// The size, when any of it was typed.
	//
	// It is sent whole because the API takes it whole -- `resources` is one
	// object, and a partial one clears what it leaves out. On a create that is
	// exactly right. On an edit it is a trap the caller cannot see: `--disk-gb
	// 40` on a pool with a fixed size would send a resources object with no
	// cpus and no memory, which is not "leave them alone" but "leave the size
	// to the host", quietly turning a fixed pool automatic.
	//
	// So an edit that touches part of the size carries the rest of it forward,
	// and `resourcesFrom` is where the caller supplies what the pool has now.
	// An edit that touches none of it sends no `resources` at all, and the
	// pool keeps whatever it had.
	switch {
	case fs.changed("size-from-host") && *spec.sizeFromHost:
		// A pool takes its size from the host or states one, never both, so
		// moving to the host's clears the stated figures in the same request.
		// Disk and the process limit are independent of the choice, and carry
		// forward like they do for any other edit.
		res := spec.resources(fs)
		delete(res, "cpus")
		delete(res, "memory_mb")
		body["resources"] = res
	case fs.changed("cpus") || fs.changed("memory-mb") || fs.changed("disk-gb") || fs.changed("pids-limit") || fs.changed("daemon-share"):
		body["resources"] = spec.resources(fs)
	}
	// The elastic CPU policy is one object for the same reason the size is,
	// so an edit that types only the ceiling carries the mode forward from
	// the pool as it stands rather than resetting it to off.
	if fs.changed("cpu-burst") || fs.changed("cpu-burst-max") || fs.changed("cpu-burst-size-builds") {
		mode, ceiling := spec.currentBurst.Mode, spec.currentBurst.MaxCPUs
		if fs.changed("cpu-burst") {
			mode = *spec.cpuBurst
		}
		if fs.changed("cpu-burst-max") {
			ceiling = *spec.cpuBurstMax
		}
		burst := map[string]any{"mode": mode, "max_cpus": ceiling}
		if fs.changed("cpu-burst-size-builds") {
			burst["size_for_ceiling"] = *spec.sizeBuilds
		} else if spec.currentBurst.SizeForCeiling != nil {
			burst["size_for_ceiling"] = *spec.currentBurst.SizeForCeiling
		}
		body["cpu_burst"] = burst
	}
	// The in-memory folders are one object, so an edit that types only a size
	// carries the other folder, and the mode of this one, forward from the pool
	// as it stands rather than switching them off.
	if spec.tmpfsChanged(fs) {
		mount := func(current poolTmpfsMount, toggle string, on *bool, sizeFlag string, size *int64) map[string]any {
			enabled, mb, auto := current.Enabled, current.SizeMB, current.Auto
			if fs.changed("tmpfs-auto") {
				auto = *spec.tmpfsAuto
			}
			if fs.changed(toggle) {
				enabled = *on
			}
			if fs.changed(sizeFlag) {
				mb = *size
			}
			out := map[string]any{"enabled": enabled}
			if mb > 0 {
				out["size_mb"] = mb
			}
			if enabled && auto {
				out["auto"] = true
			}
			return out
		}
		body["tmpfs"] = map[string]any{
			"work":   mount(spec.currentTmpfs.Work, "tmpfs-work", spec.tmpfsWork, "tmpfs-work-size", spec.tmpfsWorkSize),
			"tmp":    mount(spec.currentTmpfs.Tmp, "tmpfs-tmp", spec.tmpfsTmp, "tmpfs-tmp-size", spec.tmpfsTmpSize),
			"daemon": mount(spec.currentTmpfs.Daemon, "tmpfs-docker", spec.tmpfsDocker, "tmpfs-docker-size", spec.tmpfsDockerSize),
		}
	}
	return body
}

// tmpfsChanged is whether any in-memory folder flag was typed.
func (spec *poolSpec) tmpfsChanged(fs *flagSet) bool {
	return fs.changed("tmpfs-work") || fs.changed("tmpfs-work-size") ||
		fs.changed("tmpfs-tmp") || fs.changed("tmpfs-tmp-size") ||
		fs.changed("tmpfs-docker") || fs.changed("tmpfs-docker-size") || fs.changed("tmpfs-auto")
}

// resources is the size this invocation means, with anything not typed taken
// from the pool as it stands.
//
// Zero is a deliberate answer for the CPU and memory pair and not an absence:
// `--cpus 0 --memory-mb 0` is how the CLI says "leave the size to the host",
// which is the same thing as leaving both out on a create.
func (spec *poolSpec) resources(fs *flagSet) map[string]any {
	out := map[string]any{}
	cpus, memory, disk, pids := spec.current.CPUs, spec.current.MemoryMB, spec.current.DiskGB, spec.current.PidsLimit
	if fs.changed("cpus") {
		cpus = *spec.cpus
	}
	if fs.changed("memory-mb") {
		memory = *spec.memoryMB
	}
	if fs.changed("disk-gb") {
		disk = *spec.diskGB
	}
	share := spec.current.DaemonSharePercent
	if fs.changed("daemon-share") {
		share = *spec.daemonShare
	}
	if fs.changed("pids-limit") {
		pids = *spec.pidsLimit
	}
	if cpus > 0 {
		out["cpus"] = cpus
	}
	if memory > 0 {
		out["memory_mb"] = memory
	}
	if disk > 0 {
		out["disk_gb"] = disk
	}
	if pids > 0 {
		out["pids_limit"] = pids
	}
	if share > 0 {
		out["daemon_share_percent"] = share
	}
	return out
}

// checkSizeChoice refuses the two ways of saying how big a runner is, together.
// The API refuses them too, but a flag that cannot be honoured is better turned
// down before a request is made than reported as a field error after one.
func (spec *poolSpec) checkSizeChoice(fs *flagSet, command string) error {
	if fs.changed("size-from-host") && *spec.sizeFromHost &&
		((fs.changed("cpus") && *spec.cpus > 0) || (fs.changed("memory-mb") && *spec.memoryMB > 0)) {
		return usagef(command, "--size-from-host cannot be combined with --cpus or --memory-mb: a pool takes its size from the host or states one")
	}
	return nil
}

func poolsCreate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies pools create --name <name> --labels <labels> --installation <id>",
		"Create a pool. The server validates exactly as the UI's wizard does.")
	cf := registerClientFlags(fs, true)
	spec := registerPoolFlags(fs)
	dryRun := fs.Bool("dry-run", false, "validate the pool and print the verdict without creating anything")
	fs.example(
		"zoomies pools create --name zoomies-4vcpu-ubuntu-2404 --labels zoomies-4vcpu-ubuntu-2404 "+
			"--installation ins_k3f9qz2m --cpus 4 --os ubuntu --os-version 24.04 --max 8",
		"zoomies pools create --name zoomies-8vcpu-debian-12-arm64 --labels zoomies-8vcpu-debian-12-arm64 "+
			"--installation ins_k3f9qz2m --cpus 8 --os debian --os-version 12 --arch arm64 --dry-run",
		"zoomies pools create --name zoomies-ubuntu-2404 --labels zoomies-ubuntu-2404 "+
			"--installation ins_k3f9qz2m --size-from-host --max 12",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	// A ceiling on its own would be sent with an empty mode, which the API
	// reads as off -- and the API's own default of observe applies only when
	// the policy is absent -- so a create that names one has to name the other.
	if fs.changed("cpu-burst-max") && !fs.changed("cpu-burst") {
		return usagef("pools create", "--cpu-burst-max needs --cpu-burst to say which mode the ceiling applies to: observe or automatic")
	}
	// A size alone would be sent for a folder that is off, which the API reads
	// as nothing to size; saying so here is kinder than a pool that quietly
	// keeps its folder on disk.
	if fs.changed("tmpfs-work-size") && !*spec.tmpfsWork {
		return usagef("pools create", "--tmpfs-work-size needs --tmpfs-work, which is what puts the _work folder in memory")
	}
	if fs.changed("tmpfs-tmp-size") && !*spec.tmpfsTmp {
		return usagef("pools create", "--tmpfs-tmp-size needs --tmpfs-tmp, which is what puts /tmp in memory")
	}
	if fs.changed("tmpfs-auto") && *spec.tmpfsAuto && !*spec.tmpfsWork && !*spec.tmpfsTmp && !*spec.tmpfsDocker {
		return usagef("pools create", "--tmpfs-auto decides where the in-memory folders live, so name one with --tmpfs-work, --tmpfs-tmp or --tmpfs-docker")
	}
	if fs.changed("tmpfs-docker-size") && !*spec.tmpfsDocker {
		return usagef("pools create", "--tmpfs-docker-size needs --tmpfs-docker, which is what puts the image store in memory")
	}
	if err := spec.checkSizeChoice(fs, "pools create"); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if strings.TrimSpace(*spec.name) == "" {
		return usagef("pools create", "needs --name")
	}
	if len(*spec.labels) == 0 {
		return usagef("pools create", "needs --labels: without one your workflows have no runs-on to ask for")
	}
	if strings.TrimSpace(*spec.installation) == "" {
		return usagef("pools create", "needs --installation; `zoomies installations list` shows the IDs")
	}

	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	body := spec.body(fs, false)

	if *dryRun {
		var verdict struct {
			Valid  bool `json:"valid"`
			Errors []struct {
				Field   string `json:"field"`
				Message string `json:"message"`
			} `json:"errors"`
			Warnings      []problemItem `json:"warnings"`
			MatchingHosts int           `json:"matching_hosts"`
			SelectedHosts int           `json:"selected_hosts"`
			ExcludedHosts []struct {
				Host   string `json:"host"`
				Reason string `json:"reason"`
			} `json:"excluded_hosts"`
			// What the hosts that can run it have room for, at the size this
			// pool asks for. A maximum is only as real as this number, and a
			// dry run that printed the verdict without it left the one figure
			// an operator cannot work out for themselves off the screen.
			Room struct {
				Runners int `json:"runners"`
				Hosts   []struct {
					Host  string `json:"host"`
					Slots int    `json:"slots"`
					Fits  int    `json:"fits"`
					Room  int    `json:"room"`
				} `json:"hosts"`
			} `json:"room"`
			Resources struct {
				CPUs     float64 `json:"cpus"`
				MemoryMB int64   `json:"memory_mb"`
			} `json:"resources"`
		}
		raw, err := client.post(ctx, "/pools/validate", nil, body, &verdict)
		if err != nil {
			return err
		}
		if p.structured() {
			return p.emit(raw)
		}
		if !verdict.Valid {
			for _, fe := range verdict.Errors {
				p.note("%s: %s", fe.Field, fe.Message)
			}
			return fmt.Errorf("that pool would be refused")
		}
		p.note("Valid. %d of the %d host(s) its selector reaches could run it.",
			verdict.MatchingHosts, verdict.SelectedHosts)
		// The gap, host by host. A count alone sends an operator round the whole
		// fleet looking for the machine that was turned down.
		for _, ex := range verdict.ExcludedHosts {
			p.note("  %s: %s", ex.Host, ex.Reason)
		}
		if verdict.Resources.CPUs > 0 || verdict.Resources.MemoryMB > 0 {
			p.note("Each runner gets %s CPU and %d MB, and its hosts have room for %d of them.",
				strconv.FormatFloat(verdict.Resources.CPUs, 'f', -1, 64),
				verdict.Resources.MemoryMB, verdict.Room.Runners)
			// A host promising more slots than it can back at this size is the
			// failure that looks like health, so it is named rather than left
			// inside the total.
			for _, h := range verdict.Room.Hosts {
				if h.Slots > h.Fits {
					p.note("  %s: %d slots, room for %d at this size", h.Host, h.Slots, h.Fits)
				}
			}
		}
		printProblems(p, verdict.Warnings, "It would have these dangerous settings:")
		return nil
	}

	var pool poolItem
	raw, err := client.post(ctx, "/pools", nil, body, &pool)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.note("Created pool %s (%s).", pool.Name, pool.ID)
	printProblems(p, pool.Warnings, "It has settings that weaken the defaults:")
	return nil
}

func poolsEdit(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies pools edit <pool-id> [flags]",
		"Change the settings you name. Anything you do not name is left alone.")
	cf := registerClientFlags(fs, true)
	spec := registerPoolFlags(fs)
	warm := fs.Int("warm", 0, "on a pool the controller keeps: runners to keep ready (0 for none)")
	capRunners := fs.Int("cap", 0, "on a pool the controller keeps: the most runners to allow, however many slots its hosts give (0 for no cap)")
	fs.example("zoomies pools edit pool_k3f9qz2m --max 12",
		"zoomies pools edit pool_k3f9qz2m --warm 2 --cap 6   # a pool the controller keeps",
		"zoomies pools edit pool_k3f9qz2m --size-from-host",
		"zoomies pools edit pool_k3f9qz2m --cpu-burst automatic --cpu-burst-max 6",
		"zoomies pools edit pool_k3f9qz2m --tmpfs-work --memory-mb 12288",
		"zoomies pools edit pool_k3f9qz2m --os ubuntu --os-version 24.04",
		"zoomies pools edit pool_k3f9qz2m --labels zoomies-4vcpu-ubuntu-2404,gpu")
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a pool ID")
	if err != nil {
		return err
	}
	if err := spec.checkSizeChoice(fs, "pools edit"); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	// An edit that touches part of the size has to carry the rest of it
	// forward -- `resources` is one object, and a partial one clears what it
	// leaves out -- so the pool as it stands is read first. It is read only
	// when it is needed, so an edit that changes a label costs no extra call.
	if fs.changed("cpus") || fs.changed("memory-mb") || fs.changed("disk-gb") || fs.changed("pids-limit") || fs.changed("daemon-share") ||
		fs.changed("size-from-host") ||
		fs.changed("cpu-burst") || fs.changed("cpu-burst-max") || fs.changed("cpu-burst-size-builds") ||
		spec.tmpfsChanged(fs) {
		var existing poolItem
		if _, err := client.get(ctx, "/pools/"+url.PathEscape(id), nil, &existing); err != nil {
			return fmt.Errorf("reading the pool as it stands, which an edit to part of its size, elastic CPU policy or in-memory folders has to keep: %w", err)
		}
		spec.current = existing.Resources
		spec.currentBurst = existing.CPUBurst
		spec.currentTmpfs = existing.Tmpfs
	}

	body := spec.body(fs, true)
	if fs.changed("warm") || fs.changed("cap") {
		auto := map[string]any{}
		if fs.changed("warm") {
			auto["warm"] = *warm
		}
		if fs.changed("cap") {
			auto["cap"] = *capRunners
		}
		body["auto"] = auto
	}
	if len(body) == 0 {
		return usagef("pools edit", "nothing to change; name at least one setting, for example --max 8")
	}

	var pool poolItem
	raw, err := client.patch(ctx, "/pools/"+url.PathEscape(id), nil, body, &pool)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.note("Updated pool %s (%s).", pool.Name, pool.ID)
	printProblems(p, pool.Warnings, "It has settings that weaken the defaults:")
	return nil
}

func poolsDelete(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies pools delete <pool-id> [--force]",
		"Delete a pool. Its runners drain first unless you force it.")
	cf := registerClientFlags(fs, false)
	drain := fs.Bool("drain", true, "let running jobs finish first")
	force := fs.Bool("force", false, "destroy the runners now, interrupting any job they are running")
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a pool ID")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("drain", strconv.FormatBool(*drain))
	q.Set("force", strconv.FormatBool(*force))
	var result struct {
		RunnersAffected int `json:"runners_affected"`
	}
	if _, err := client.del(ctx, "/pools/"+url.PathEscape(id), q, &result); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Deleted pool %s; %d runner(s) affected.\n", id, result.RunnersAffected)
	return nil
}

func poolsEnable(ctx context.Context, e *env, args []string) error {
	return poolsToggle(ctx, e, args, "enable")
}

func poolsDisable(ctx context.Context, e *env, args []string) error {
	return poolsToggle(ctx, e, args, "disable")
}

func poolsToggle(ctx context.Context, e *env, args []string, verb string) error {
	summary := "Let a pool create runners again."
	if verb == "disable" {
		summary = "Stop a pool creating runners. Existing ones drain as they go idle; nothing running is interrupted."
	}
	fs := newFlagSet(e, "zoomies pools "+verb+" <pool-id>", summary)
	cf := registerClientFlags(fs, false)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a pool ID")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	var pool poolItem
	if _, err := client.post(ctx, "/pools/"+url.PathEscape(id)+"/"+verb, nil, nil, &pool); err != nil {
		return err
	}
	// What the response says, not what was asked: a pool the controller keeps is
	// put back in use by the controller, from its hosts, and resuming it where it
	// has none leaves it out of use.
	switch {
	case verb == "enable" && pool.Enabled:
		fmt.Fprintf(e.out, "Pool %s is enabled.\n", pool.Name)
	case verb == "enable" && pool.Auto != nil && !pool.Auto.Paused:
		fmt.Fprintf(e.out, "Pool %s is no longer paused, and stays out of use until a host counts towards it: the controller works that out from its hosts.\n", pool.Name)
	case verb == "enable":
		fmt.Fprintf(e.out, "Pool %s is still not enabled.\n", pool.Name)
	default:
		fmt.Fprintf(e.out, "Pool %s is disabled; its runners will drain as they become idle.\n", pool.Name)
	}
	return nil
}

// printProblems renders a list of warnings under a heading, or nothing at all
// when there are none. Silence is the right output for "nothing is wrong".
func printProblems(p *printer, problems []problemItem, heading string) {
	if len(problems) == 0 {
		return
	}
	fmt.Fprintf(p.out, "\n%s\n", heading)
	for _, item := range problems {
		title := item.Title
		switch item.Severity {
		case "error":
			title = p.paint(colourRed, title)
		case "warning":
			title = p.paint(colourYellow, title)
		}
		fmt.Fprintf(p.out, "  - %s\n", title)
		if item.Detail != "" {
			fmt.Fprintf(p.out, "      %s\n", item.Detail)
		}
		if item.Fix != "" {
			fmt.Fprintf(p.out, "      fix: %s\n", item.Fix)
		}
	}
}
