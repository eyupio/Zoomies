package controller

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
)

// EnvDockerWait is the variable the runner image reads for how long to wait
// for its pool's Docker daemon, in whole seconds. The fleet's runners.docker_wait
// is rendered into it; a pool's own env may name it to say otherwise.
const EnvDockerWait = "ZOOMIES_DOCKER_WAIT"

// runnerEnv is the environment a runner of this pool starts with: the fleet's
// runners.env, then the Docker wait for a pool that provides a daemon, then
// the pool's own env over the top. The pool wins every collision, because it
// is the more specific thing an operator said, and the fleet-wide layer is
// what lets one setting on the Settings page reach every pool at once rather
// than being pasted into each.
//
// Nothing here is a credential. The runner's own identity and JIT
// configuration are written by the backend from the spec's Credentials, and
// the validator refuses a runners.env that names them.
func runnerEnv(r config.Runners, pool *store.Pool) map[string]string {
	wait := dockerWait(r, pool)
	if len(r.Env) == 0 && wait <= 0 && len(pool.Env) == 0 {
		return nil
	}
	out := make(map[string]string, len(r.Env)+len(pool.Env)+1)
	for k, v := range r.Env {
		out[k] = v
	}
	// Only a pool with a daemon needs to know how long to wait for one; the
	// image ignores the variable otherwise, but a runner's environment should
	// say what applies to it and nothing else.
	if wait > 0 && pool.DockerMode != "" && pool.DockerMode != store.DockerNone {
		// The image takes whole seconds and refuses zero, so a wait under a
		// second rounds up to one rather than down to a refusal.
		seconds := max(1, int64((wait+time.Second-1)/time.Second))
		out[EnvDockerWait] = strconv.FormatInt(seconds, 10)
	}
	for k, v := range pool.Env {
		out[k] = v
	}
	return out
}

// dockerWait is how long this pool's runners wait for their daemon: the pool's
// own override where it has one, and the fleet's figure otherwise.
//
// A pool that overrides it to zero leaves the image's own default in place,
// which is exactly what the fleet's zero means, so the two agree about what
// nothing means. The pool's `env` may still name ZOOMIES_DOCKER_WAIT directly
// and win over both -- it is layered last -- which is the escape hatch that
// existed before this setting did.
func dockerWait(r config.Runners, pool *store.Pool) time.Duration {
	if d := pool.RunnerSettings.DockerWait; d != nil {
		return d.Duration()
	}
	return r.DockerWait
}

// The variables that tell a toolchain how many CPUs to size its workers for.
// Each of these reads the container's CPU quota once, at start, and never
// again, so a job started at its guarantee has workers for its guarantee and
// no worker to use a loan that arrives a minute later.
const (
	EnvCargoBuildJobs      = "CARGO_BUILD_JOBS"
	EnvDotnetProcessors    = "DOTNET_PROCESSOR_COUNT"
	EnvJavaToolOptions     = "JAVA_TOOL_OPTIONS"
	javaActiveProcessorArg = "-XX:ActiveProcessorCount="
)

// GOMAXPROCS is deliberately not among them. Go 1.25 and later read the quota
// at start and again as it changes, so a loan reaches them without help, and
// pinning GOMAXPROCS would switch that off. Go before 1.25 ignored the quota
// and started a thread per host core -- more than any ceiling -- so it needs
// no help either.

// buildSizedCPUs is the CPU count a runner of pool p started on h tells its
// toolchains to size for: its ceiling, in whole cores, or zero when the pool
// does not ask for it. It is never below the guarantee, which a ceiling
// cannot take a runner under.
func buildSizedCPUs(p *store.Pool, h *store.Host, guarantee float64) int {
	if p == nil || h == nil || !p.Automatic() || !p.CPUBurst.SizesForCeiling() ||
		(p.Backend != store.BackendDocker && p.Backend != store.BackendPodman) {
		return 0
	}
	alloc := h.Allocatable()
	if !alloc.CPUsKnown || alloc.CPUs <= 0 {
		return 0
	}
	ceiling := max(elasticCeiling(p, h, alloc.CPUs), guarantee)
	// Floored: a worker for a fraction of a core is a worker queued behind the
	// others, and the guarantee's own rounding already gives the job one.
	n := int(math.Floor(ceiling + 1e-9))
	if n < 1 || float64(n) <= guarantee {
		// A ceiling no higher than the guarantee is what the toolchain would
		// read for itself; saying it again only hides where it came from.
		return 0
	}
	return n
}

// withBuildSizing adds the toolchain sizing for n CPUs to env, which is the
// runner's environment as runnerEnv built it. A variable the fleet or the pool
// already sets is left as it is: that is an operator's explicit choice. The
// one exception is JAVA_TOOL_OPTIONS, which carries every other JVM flag too,
// so the processor count is appended to it unless it already names one.
func withBuildSizing(env map[string]string, n int) map[string]string {
	if n <= 0 {
		return env
	}
	if env == nil {
		env = make(map[string]string, 3)
	}
	count := strconv.Itoa(n)
	for _, k := range []string{EnvCargoBuildJobs, EnvDotnetProcessors} {
		if _, set := env[k]; !set {
			env[k] = count
		}
	}
	arg := javaActiveProcessorArg + count
	switch opts, set := env[EnvJavaToolOptions]; {
	case !set || strings.TrimSpace(opts) == "":
		env[EnvJavaToolOptions] = arg
	case !strings.Contains(opts, "ActiveProcessorCount"):
		env[EnvJavaToolOptions] = strings.TrimSpace(opts) + " " + arg
	}
	return env
}
