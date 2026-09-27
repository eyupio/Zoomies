package backend

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// A pool that keeps a tool cache starts it empty: the first job to ask for
// Python 3.12 downloads it, and so does every job on every other host until
// one of them has. Filling it is the same download done once per host, ahead
// of the jobs, for the versions the toolchain scan found the pool's workflows
// asking for.
//
// The fill runs inside the pool's own image rather than in the agent, which
// is a static binary in a distroless image with nothing to unpack a toolchain
// with. The image is also the only thing that knows which build is the right
// one: setup-python's archives are per Ubuntu release, and a toolchain built
// for another C library is a cache entry that fails in the job that finds it.

//go:embed toolfill.sh
var toolFillScript string

// ToolRequest is one toolchain version to put in a pool's tool cache, as a
// workflow asked for it: "python" "3.12", "node" "lts/*", "java" "21"
// "temurin".
type ToolRequest struct {
	Tool         string `json:"tool"`
	Version      string `json:"version"`
	Distribution string `json:"distribution,omitempty"`
}

// ToolFill is what became of one ToolRequest.
type ToolFill struct {
	Tool    string `json:"tool"`
	Request string `json:"request"`
	// Outcome is installed, present (it was already there), skipped (this
	// request is not one a fill can serve, with the reason) or failed.
	Outcome string `json:"outcome"`
	// Version is the release the request resolved to, for installed and
	// present; Reason says why for skipped and failed.
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// The outcomes a fill reports.
const (
	ToolInstalled = "installed"
	ToolPresent   = "present"
	ToolSkipped   = "skipped"
	ToolFailed    = "failed"
)

// ToolCacheFiller is implemented by a backend that can fill a pool's kept tool
// cache. spec carries what the pool's runners would: its ID, image, pull
// policy, env and cache, from which the backend finds the same folder the
// runners are given.
type ToolCacheFiller interface {
	FillToolCache(ctx context.Context, spec Spec, tools []ToolRequest) ([]ToolFill, error)
}

const (
	roleToolFill = "toolcache-fill"
	// A fill is a background download and must not take a running job's
	// share of the host to do it.
	toolFillCPUs     = 1
	toolFillMemoryMB = 2048
)

// FillToolCache runs the fill in a short-lived container of the pool's image,
// with the pool's tool cache mounted where its runners have it.
func (b *DockerBackend) FillToolCache(ctx context.Context, spec Spec, tools []ToolRequest) ([]ToolFill, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	if dir, err := toolCacheDir(spec, b.sharedDir); err != nil || dir == "" {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("backend: this pool keeps no tool cache on this host")
	}
	ref, _, _, _, err := b.prepareImage(ctx, spec.Image, spec.PullPolicy)
	if err != nil {
		return nil, err
	}
	spec.Image = ref
	dir, err := toolCacheDir(spec, b.sharedDir)
	if err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, errors.New("backend: this pool keeps no tool cache on this host; it needs the pool cache and its tool cache turned on, and a shared folder the agent could create")
	}
	if err := ensureRunnerWritableDir(dir); err != nil {
		return nil, fmt.Errorf("backend: creating the tool cache folder %s: %w", dir, err)
	}

	cfg := buildToolFillConfig(spec, b.fl, dir, b.extraCA, inheritedProxyEnv(os.LookupEnv, spec.Env), tools)
	// The reference prepareImage resolved, which is the digest a pinned pool
	// names rather than whatever its tag points at now.
	cfg.Image = ref
	if network := firstNonEmpty(strings.TrimSpace(spec.Network), b.network); network != "" {
		if err := b.ensureNetwork(ctx, network); err != nil {
			return nil, err
		}
		cfg.HostConfig.NetworkMode = network
	}
	name := "zoomies-toolfill-" + sanitizeHostname(cfg.Labels[LabelPoolID])
	if id, err := cacheIdentity(spec); err == nil && id != "" {
		name = "zoomies-toolfill-" + id
	}

	// Removed before and after, under a bounded context of its own: a fill
	// that was cancelled half way leaves a container of this name, and one
	// left behind is what would make the next fill's create fail.
	cleanup := func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := b.removeContainer(rctx, name); err != nil && !errors.Is(err, ErrNotFound) {
			b.log.Warn("could not remove the tool cache fill container", "container", name, "error", err)
		}
	}
	cleanup()
	id, err := b.api.ContainerCreate(ctx, name, cfg)
	if err != nil {
		return nil, fmt.Errorf("backend: creating the tool cache fill container: %w", err)
	}
	defer cleanup()
	if err := b.api.ContainerStart(ctx, id); err != nil {
		return nil, fmt.Errorf("backend: starting the tool cache fill container: %w", err)
	}
	code, waitErr := b.api.ContainerWait(ctx, id)

	// The logs are read even when the wait failed: every request that
	// finished before a deadline is in them, and is in the cache.
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var fills []ToolFill
	var tail []string
	if rc, err := b.api.ContainerLogs(lctx, id, LogQuery{Stdout: true, Stderr: true}); err == nil {
		fills, tail = parseToolFills(rc)
		_ = rc.Close()
	}
	switch {
	case waitErr != nil:
		return fills, fmt.Errorf("backend: the tool cache fill did not finish: %w", waitErr)
	case code != 0:
		return fills, fmt.Errorf("backend: the tool cache fill exited with status %d: %s", code, strings.Join(tail, " / "))
	}
	return fills, nil
}

// buildToolFillConfig is the fill container: the pool's image, as the image's
// runner user, with the tool cache and nothing else of the host's mounted.
//
// It carries no io.zoomies.managed label, so the agent's reconciliation never
// counts it as a runner or reaps it as an orphan; its fixed name is how a
// leftover one is found instead.
func buildToolFillConfig(spec Spec, fl flavor, dir, extraCA string, proxyEnv []string, tools []ToolRequest) ContainerCreateRequest {
	var requests strings.Builder
	for _, t := range tools {
		fmt.Fprintf(&requests, "%s %s %s\n", t.Tool, t.Version, t.Distribution)
	}
	env := []string{EnvToolsDirectory + "=" + RunnerToolCacheMount}
	env = append(env, proxyEnv...)
	// The pool's own proxy, where it sets one: the fill fetches from the same
	// hosts the pool's jobs would, through the same way out.
	for _, k := range proxyEnvKeys {
		if v, ok := spec.Env[k]; ok {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "ZOOMIES_TOOL_REQUESTS="+requests.String())

	cfg := ContainerCreateRequest{
		Image: spec.Image,
		// bash -c runs the script from memory, so the image needs nothing of
		// Zoomies' in it; "toolfill" is $0, which names it in any error bash
		// prints.
		Entrypoint: []string{"/bin/bash", "-c", toolFillScript, "toolfill"},
		Env:        env,
		Labels: map[string]string{
			LabelRole:   roleToolFill,
			LabelPoolID: spec.PoolID,
		},
		HostConfig: &HostConfig{
			LogConfig:     runnerLogConfig(fl),
			RestartPolicy: RestartPolicy{Name: "no"},
			CapDrop:       append([]string(nil), fl.capDrop...),
			NanoCPUs:      nanoCPUs(toolFillCPUs),
			Memory:        toolFillMemoryMB * 1024 * 1024,
			MemorySwap:    toolFillMemoryMB * 1024 * 1024,
			Binds:         []string{dir + ":" + RunnerToolCacheMount + fl.mountSuffix},
		},
	}
	// As the runner user whatever the pool says about root: what the fill
	// writes, the runners read and a later job may update, and a folder of
	// root's would be one they cannot.
	cfg.User = fl.runnerUser
	if extraCA != "" {
		cfg.HostConfig.Binds = append(cfg.HostConfig.Binds, extraCABind(fl, extraCA))
		cfg.Env = append(cfg.Env, EnvExtraCAFile+"="+ExtraCAPath)
	}
	return cfg
}

// parseToolFills reads the script's "zoomies-fill <outcome> <tool> <request>
// <version-or-reason>" lines, and keeps the last few other lines for an error
// message.
func parseToolFills(r io.Reader) ([]ToolFill, []string) {
	var fills []ToolFill
	var tail []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, "zoomies-fill ")
		if !ok {
			if line != "" {
				tail = append(tail, line)
				if len(tail) > 5 {
					tail = tail[1:]
				}
			}
			continue
		}
		f := strings.SplitN(rest, " ", 4)
		if len(f) < 3 {
			continue
		}
		fill := ToolFill{Outcome: f[0], Tool: f[1], Request: f[2]}
		detail := ""
		if len(f) == 4 {
			detail = f[3]
		}
		switch fill.Outcome {
		case ToolInstalled, ToolPresent:
			fill.Version = detail
		case ToolSkipped, ToolFailed:
			fill.Reason = detail
		default:
			continue
		}
		fills = append(fills, fill)
	}
	return fills, tail
}
