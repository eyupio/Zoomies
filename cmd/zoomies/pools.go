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
	}, args)
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
		p.note("No pools yet. Create one with: zoomies pools create --name zoomies-linux-x64 --labels self-hosted,linux-x64 --installation <id>")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, item := range out.Items {
		enabled := p.paint(colourGreen, "yes")
		if !item.Enabled {
			enabled = p.paint(colourDim, "no")
		}
		rows = append(rows, []string{
			item.Name,
			item.ID,
			truncate(strings.Join(item.Labels, ","), 34),
			item.Backend,
			strconv.Itoa(item.Priority),
			fmt.Sprintf("%d/%d", item.Counts.Live, item.MaxRunners),
			strconv.Itoa(item.Counts.Busy),
			strconv.Itoa(item.QueuedJobs),
			p.bar(item.Utilisation, 10),
			enabled,
		})
	}
	p.table([]string{"name", "id", "labels", "backend", "priority", "live/max", "busy", "queued", "utilisation", "enabled"}, rows)
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

	p.keyValues([][2]string{
		{"name", pool.Name},
		{"id", pool.ID},
		{"enabled", p.yesNo(pool.Enabled, false)},
		{"installation", dash(pool.InstallationTarget) + " (" + pool.InstallationID + ")"},
		{"labels", strings.Join(pool.Labels, ", ")},
		{"runner group", dash(pool.RunnerGroup)},
		{"backend", pool.Backend},
		{"image", dash(pool.Image)},
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
		{"host selector", dash(kvValue(pool.HostSelector).String())},
		{"created", p.relTime(pool.CreatedAt)},
		{"updated", p.relTime(pool.UpdatedAt)},
	})
	printProblems(p, pool.Warnings, "This pool has settings that weaken the defaults:")
	return nil
}

// poolSpec holds the flags shared by create and edit. Keeping them in one place
// is what makes `edit` accept exactly the settings `create` does.
type poolSpec struct {
	name         *string
	installation *string
	backend      *string
	image        *string
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
	enabled      *bool
	cpus         *float64
	memoryMB     *int64
	diskGB       *int64
	cacheEnabled *bool
	cacheScope   *string
	cacheSize    *int64
	cacheSource  *string
}

// registerPoolFlags declares them, with the API's own defaults so that a
// created pool is the same whether it came from here or from the wizard.
func registerPoolFlags(fs *flagSet) *poolSpec {
	spec := &poolSpec{
		labels:       &listValue{},
		hostSelector: kvValue{},
		envVars:      kvValue{},
	}
	spec.name = fs.String("name", "", "the pool's name; it is stored with the zoomies- prefix, e.g. zoomies-linux-x64")
	spec.installation = fs.String("installation", "", "the GitHub App installation this pool registers runners with")
	fs.Var(spec.labels, "labels", "the labels a workflow's runs-on must ask for (repeatable, or comma-separated)")
	spec.backend = fs.String("backend", "docker", "docker, podman or process")
	spec.image = fs.String("image", "", "runner image (default: the controller's github.runner_image)")
	spec.version = fs.String("runner-version", "", "pin the actions/runner release")
	spec.group = fs.String("runner-group", "", "the GitHub runner group to register into")
	spec.minRunners = fs.Int("min", 0, "runners to keep even when nothing is queued")
	spec.maxRunners = fs.Int("max", 4, "the most runners this pool may have at once")
	spec.priority = fs.Int("priority", 0, "scheduling priority; higher-priority pools receive create slots first")
	spec.idleTimeout = fs.String("idle-timeout", "5m", "how long an idle runner waits before being drained")
	spec.ephemeral = fs.Bool("ephemeral", true, "one job per runner; the safe default")
	spec.dockerMode = fs.String("docker-mode", "none", "none, dind or host-socket (host-socket gives jobs root on the host)")
	spec.runAsRoot = fs.Bool("run-as-root", false, "run job steps as root inside the runner")
	spec.enabled = fs.Bool("enabled", true, "whether the pool may create runners")
	fs.Var(spec.hostSelector, "host-selector", "only use hosts whose labels match, e.g. arch=arm64")
	fs.Var(spec.envVars, "env", "environment variables for every job in this pool, e.g. HTTP_PROXY=...")
	spec.cpus = fs.Float64("cpus", 0, "CPU limit per runner")
	spec.memoryMB = fs.Int64("memory-mb", 0, "memory limit per runner, in MiB")
	spec.diskGB = fs.Int64("disk-gb", 0, "disk limit per runner, in GiB")
	spec.cacheEnabled = fs.Bool("cache", false, "mount a disposable performance cache (not workflow storage)")
	spec.cacheScope = fs.String("cache-scope", "pool", "cache isolation: pool or repository")
	spec.cacheSize = fs.Int64("cache-size", 0, "approximate cache limit in bytes (0 is unlimited)")
	spec.cacheSource = fs.String("cache-source", "", "absolute host path or named-volume prefix")
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
	put("min", "min_runners", *spec.minRunners)
	put("max", "max_runners", *spec.maxRunners)
	put("priority", "priority", *spec.priority)
	put("idle-timeout", "idle_timeout", *spec.idleTimeout)
	put("ephemeral", "ephemeral", *spec.ephemeral)
	put("docker-mode", "docker_mode", *spec.dockerMode)
	put("run-as-root", "run_as_root", *spec.runAsRoot)
	put("enabled", "enabled", *spec.enabled)
	if !onlyChanged || fs.changed("cache") || fs.changed("cache-scope") || fs.changed("cache-size") || fs.changed("cache-source") {
		body["cache"] = map[string]any{"enabled": *spec.cacheEnabled, "scope": *spec.cacheScope, "size_limit": *spec.cacheSize, "source": *spec.cacheSource}
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
	if fs.changed("cpus") || fs.changed("memory-mb") || fs.changed("disk-gb") {
		resources := map[string]any{}
		if *spec.cpus > 0 {
			resources["cpus"] = *spec.cpus
		}
		if *spec.memoryMB > 0 {
			resources["memory_mb"] = *spec.memoryMB
		}
		if *spec.diskGB > 0 {
			resources["disk_gb"] = *spec.diskGB
		}
		body["resources"] = resources
	}
	return body
}

func poolsCreate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies pools create --name <name> --labels <labels> --installation <id>",
		"Create a pool. The server validates exactly as the UI's wizard does.")
	cf := registerClientFlags(fs, true)
	spec := registerPoolFlags(fs)
	dryRun := fs.Bool("dry-run", false, "validate the pool and print the verdict without creating anything")
	fs.example(
		"zoomies pools create --name zoomies-linux-x64 --labels self-hosted,linux-x64 --installation inst_k3f9qz2m --max 8",
		"zoomies pools create --name zoomies-arm --labels linux-arm64 --installation inst_k3f9qz2m --host-selector arch=arm64 --dry-run",
	)
	if err := fs.parse(args); err != nil {
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
		p.note("Valid. %d host(s) could run it.", verdict.MatchingHosts)
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
	fs.example("zoomies pools edit pool_k3f9qz2m --max 12", "zoomies pools edit pool_k3f9qz2m --labels self-hosted,linux-x64,gpu")
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a pool ID")
	if err != nil {
		return err
	}
	body := spec.body(fs, true)
	if len(body) == 0 {
		return usagef("pools edit", "nothing to change; name at least one setting, for example --max 8")
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
	if verb == "enable" {
		fmt.Fprintf(e.out, "Pool %s is enabled.\n", pool.Name)
	} else {
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
