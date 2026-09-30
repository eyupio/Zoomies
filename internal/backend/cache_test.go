package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/store"
)

func TestCacheMountConstructionAndDisabledBehavior(t *testing.T) {
	s := Spec{PoolID: "pool_one", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool}}
	cfg := buildRunnerConfig(s, dockerFlavor(), containerOptions{})
	want := "zoomies-cache-v2-pool-one-5fa452050886970a2c7b84f6c3ec78f6:" + RunnerCacheMount
	if len(cfg.HostConfig.Binds) != 1 || cfg.HostConfig.Binds[0] != want {
		t.Fatalf("binds = %v, want %q", cfg.HostConfig.Binds, want)
	}
	s.Cache.Enabled = false
	if got := buildRunnerConfig(s, dockerFlavor(), containerOptions{}).HostConfig.Binds; len(got) != 0 {
		t.Fatalf("disabled cache mounted: %v", got)
	}
}

func TestCacheScopesAreIsolated(t *testing.T) {
	pool := Spec{PoolID: "pool_one", Repository: "acme/one", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool}}
	a, _ := cacheSource(pool)
	pool.Repository = "acme/two"
	b, _ := cacheSource(pool)
	if a != b {
		t.Fatalf("pool scope changed by repository: %q != %q", a, b)
	}
	pool.Cache.Scope = store.CacheScopeRepository
	a, _ = cacheSource(pool)
	pool.Repository = "acme/one"
	b, _ = cacheSource(pool)
	if a == b {
		t.Fatalf("repository caches unexpectedly shared: %q", a)
	}
}

func TestCacheRejectsUnsafeInput(t *testing.T) {
	for _, source := range []string{"../escape", "bad/name", "name:option"} {
		_, err := cacheSource(Spec{PoolID: "p", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool, Source: source}})
		if err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Errorf("source %q: err = %v", source, err)
		}
	}
}

func TestPodmanCacheMountGetsSELinuxSuffix(t *testing.T) {
	s := Spec{PoolID: "p", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool}}
	got := buildRunnerConfig(s, podmanFlavor(), containerOptions{}).HostConfig.Binds[0]
	if got != "zoomies-cache-v2-p-53376dd3c3bd3405c60ccc089cc42204:"+RunnerCacheMount+":z" {
		t.Fatalf("bind = %q", got)
	}
}

// An organisation-wide installation gives the spec the organisation as its
// repository, which is not a repository at all. The pool names the repository
// its cache is for, and that is what has to reach the mount.
func TestRepositoryCacheUsesThePoolsConfiguredRepository(t *testing.T) {
	org := Spec{PoolID: "pool_one", Repository: "acme", Cache: store.CacheConfig{
		Enabled: true, Scope: store.CacheScopeRepository, Repository: "acme/widgets",
	}}
	got, err := cacheSource(org)
	if err != nil {
		t.Fatalf("cacheSource: %v", err)
	}
	if want := "zoomies-cache-v2-pool-one-acme-widgets-a92d8a756bd760f626e999cafe84137a"; got != want {
		t.Fatalf("cache source = %q, want %q", got, want)
	}
	// A repository-targeted installation keeps supplying it, unchanged.
	repo := Spec{PoolID: "pool_one", Repository: "acme/widgets", Cache: store.CacheConfig{
		Enabled: true, Scope: store.CacheScopeRepository,
	}}
	same, err := cacheSource(repo)
	if err != nil {
		t.Fatalf("cacheSource: %v", err)
	}
	if same != got {
		t.Fatalf("the same repository got two caches: %q and %q", got, same)
	}
}

func TestRepositoryCacheRefusesAnOrganisationWithNoRepository(t *testing.T) {
	_, err := cacheSource(Spec{PoolID: "p", Repository: "acme", Cache: store.CacheConfig{
		Enabled: true, Scope: store.CacheScopeRepository,
	}})
	if err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Fatalf("err = %v, want a message naming what to set", err)
	}
}

// A cache the operator asked to cap has to actually be capped, or the number in
// the pool form is decoration and the host's disk is the thing that runs out.
func TestPruneCacheEvictsTheLeastRecentlyUsedEntriesFirst(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int, age time.Duration) {
		sub := filepath.Join(dir, name)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(sub, "blob")
		if err := os.WriteFile(file, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		for _, p := range []string{file, sub} {
			if err := os.Chtimes(p, when, when); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("old", 4000, 3*time.Hour)
	write("middle", 4000, 2*time.Hour)
	write("fresh", 4000, time.Minute)

	pruneCache(dir, 9000, slog.New(slog.DiscardHandler))

	names := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if names["old"] {
		t.Fatal("the oldest cache entry survived a prune that had to free space")
	}
	if !names["middle"] || !names["fresh"] {
		t.Fatalf("prune removed more than it needed to: %v", names)
	}
}

func TestPruneCacheLeavesACacheUnderItsLimitAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob"), make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	pruneCache(dir, 1000, slog.New(slog.DiscardHandler))
	pruneCache(dir, 0, slog.New(slog.DiscardHandler)) // zero is unlimited, not "empty it"
	if _, err := os.Stat(filepath.Join(dir, "blob")); err != nil {
		t.Fatalf("a cache within its limit was pruned: %v", err)
	}
}

// Only a host directory can be measured, so only a host directory is offered up
// for eviction; a named volume must not be mistaken for a relative path.
func TestCacheDirectoryOnlyRecognisesHostPaths(t *testing.T) {
	requirePOSIX(t)
	volume := Spec{PoolID: "p", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool}}
	if _, ok := cacheDirectory(volume); ok {
		t.Fatal("a named volume was treated as a directory this agent can prune")
	}
	host := Spec{PoolID: "p", Cache: store.CacheConfig{
		Enabled: true, Scope: store.CacheScopePool, Source: "/var/lib/zoomies/cache",
	}}
	dir, ok := cacheDirectory(host)
	if !ok || dir != "/var/lib/zoomies/cache/v2-p-53376dd3c3bd3405c60ccc089cc42204" {
		t.Fatalf("cacheDirectory = %q, %v", dir, ok)
	}
}

// cacheFixture writes one oversized cache entry and returns the directory and a
// spec whose cache is that directory with a limit it already exceeds.
func cacheFixture(t *testing.T) (string, Spec) {
	t.Helper()
	root := t.TempDir()
	spec := Spec{
		PoolID: "pool_one",
		Cache: store.CacheConfig{
			Enabled:   true,
			Scope:     store.CacheScopePool,
			Source:    root,
			SizeLimit: 1000,
		},
	}
	dir, ok := cacheDirectory(spec)
	if !ok {
		t.Fatal("the fixture's cache must be a host directory, or there is nothing to prune")
	}
	entry := filepath.Join(dir, "blob")
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "big"), make([]byte, 4000), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, spec
}

// cacheUsers builds an engine that answers the "who is using this cache?"
// listing with containers in the given states, and checks the question asked.
func cacheUsers(t *testing.T, dir string, states ...string) *fakeEngine {
	t.Helper()
	return newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			var filters map[string][]string
			if err := json.Unmarshal([]byte(r.Form.Get("filters")), &filters); err != nil {
				t.Errorf("filters not JSON: %v", err)
			}
			// The daemon must be asked about this cache, not about everything:
			// a listing of the whole host would let another pool's runner keep
			// this cache from ever being pruned.
			if !slices.Contains(filters["label"], LabelCacheVolume+"="+dir) {
				t.Errorf("the listing must be scoped to this cache, got %v", filters)
			}
			out := make([]ContainerSummary, 0, len(states))
			for i, state := range states {
				out = append(out, ContainerSummary{
					ID: fmt.Sprint("c", i), State: state,
					Labels: map[string]string{LabelCacheVolume: dir},
				})
			}
			writeJSON(w, 200, out)
		},
	})
}

func cacheStillHasItsEntry(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "blob"))
	return err == nil
}

// The eviction happens as a runner is created because that was taken to be the
// moment the cache is idle. On a pool that runs two runners it is not: the
// second runner's start would delete the files the first runner's job is part
// way through using, which is a build failure an operator cannot explain and
// cannot reproduce.
func TestACacheAnotherRunnerIsUsingIsNotPruned(t *testing.T) {
	dir, spec := cacheFixture(t)
	b := dockerBackendFor(t, cacheUsers(t, dir, "running"), DockerOptions{})

	b.pruneCacheFor(context.Background(), spec)

	if !cacheStillHasItsEntry(t, dir) {
		t.Fatal("evicted from a cache a running job is using")
	}
}

// A container that exists but has finished is not reading anything, and a pool
// that has just run a job would otherwise never prune again.
func TestACacheOnlyFinishedRunnersHeldIsPruned(t *testing.T) {
	dir, spec := cacheFixture(t)
	b := dockerBackendFor(t, cacheUsers(t, dir, "exited", "dead"), DockerOptions{})

	b.pruneCacheFor(context.Background(), spec)

	if cacheStillHasItsEntry(t, dir) {
		t.Fatal("a cache over its limit that nothing is using was left alone")
	}
}

// A container the daemon has created but not started is about to read the
// cache, so it counts. Guessing the other way costs a job for no gain.
func TestACacheAStartingRunnerHoldsIsNotPruned(t *testing.T) {
	dir, spec := cacheFixture(t)
	b := dockerBackendFor(t, cacheUsers(t, dir, "created"), DockerOptions{})

	b.pruneCacheFor(context.Background(), spec)

	if !cacheStillHasItsEntry(t, dir) {
		t.Fatal("evicted from a cache a starting runner is about to use")
	}
}

// A daemon that will not answer says nothing about whether the cache is idle.
// Carrying the excess for one more runner costs disk; guessing wrong costs a
// job, so the unprovable case keeps the files.
func TestACacheIsNotPrunedWhenTheDaemonWillNotSayWhoIsUsingIt(t *testing.T) {
	dir, spec := cacheFixture(t)
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "daemon is unwell"})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	b.pruneCacheFor(context.Background(), spec)

	if !cacheStillHasItsEntry(t, dir) {
		t.Fatal("evicted from a cache without being able to tell whether it was in use")
	}
}

// With no limit there is nothing to enforce, so the daemon is not asked at all.
func TestACacheWithNoLimitAsksTheDaemonNothing(t *testing.T) {
	dir, spec := cacheFixture(t)
	spec.Cache.SizeLimit = 0
	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"GET " + v + "/containers/json": func(w http.ResponseWriter, r *http.Request) {
			t.Error("a cache with no size limit must not cost a listing")
			writeJSON(w, 200, []ContainerSummary{})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{})

	b.pruneCacheFor(context.Background(), spec)

	if !cacheStillHasItsEntry(t, dir) {
		t.Fatal("a cache with no limit was pruned")
	}
}

// runnerOwnedDirs returns the directories deploy/Dockerfile.runner creates
// owned by the runner account, from its `install -d -o runner` line.
func runnerOwnedDirs(t *testing.T, dockerfile string) []string {
	t.Helper()
	for _, line := range strings.Split(dockerfile, "\n") {
		if _, rest, ok := strings.Cut(line, "install -d -o runner -g runner -m 0755 "); ok {
			return strings.Fields(rest)
		}
	}
	t.Fatal("deploy/Dockerfile.runner has no `install -d -o runner` line")
	return nil
}

// A fresh named volume takes its owner from the directory it is mounted over.
// With no such directory in the image, the pool cache came up owned by root
// and the runner -- uid 1001, not root -- could not write to it at all.
func TestTheRunnerImageOwnsThePoolCacheMountPoint(t *testing.T) {
	dockerfile := readFile(t, filepath.Join("..", "..", "deploy", "Dockerfile.runner"))
	if !slices.Contains(runnerOwnedDirs(t, dockerfile), RunnerCacheMount) {
		t.Errorf("deploy/Dockerfile.runner does not create %s owned by the runner, so a new cache volume is root's", RunnerCacheMount)
	}
}

// The tool cache has to be somewhere the runner can write, or the first
// setup-* step fails; and it has to be the directory the image sets, or an
// image built on this one fills a cache the runner never looks in.
func TestTheRunnerImagesToolCacheIsTheRunners(t *testing.T) {
	dockerfile := readFile(t, filepath.Join("..", "..", "deploy", "Dockerfile.runner"))
	var toolCache string
	for _, line := range strings.Split(dockerfile, "\n") {
		if v, ok := strings.CutPrefix(line, "ENV AGENT_TOOLSDIRECTORY="); ok {
			toolCache = strings.TrimSpace(v)
		}
	}
	if toolCache == "" {
		t.Fatal("deploy/Dockerfile.runner sets no AGENT_TOOLSDIRECTORY, so every job's setup-* steps download into _work and lose it")
	}
	if !slices.Contains(runnerOwnedDirs(t, dockerfile), toolCache) {
		t.Errorf("AGENT_TOOLSDIRECTORY is %s, which deploy/Dockerfile.runner does not create owned by the runner", toolCache)
	}
}

// A pool that keeps a tool cache has it bound from the host's shared folder,
// under the same identity as its pool cache, and the runner is pointed at it
// -- at a folder of its own, so an image's baked-in tool cache is not hidden.
func TestAToolCacheIsBoundFromTheSharedFolderAndNamedToTheRunner(t *testing.T) {
	shared := t.TempDir()
	s := Spec{PoolID: "pool_one", Repository: "acme/one",
		Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopeRepository, Tools: true}}
	dir, err := toolCacheDir(s, shared)
	if err != nil {
		t.Fatalf("toolCacheDir: %v", err)
	}
	if want := filepath.Join(shared, "cache", "tools", "v2-pool-one-acme-one-79e56659f49a53f0f81fbe59469a94ba", "image-e3b0c44298fc1c149afbf4c8996fb924"); dir != want {
		t.Fatalf("tool cache = %q, want %q: the pool cache's own identity, under cache/tools", dir, want)
	}
	farm := toolFarmDir(shared, "run-one")
	cfg := buildRunnerConfig(s, dockerFlavor(), containerOptions{ToolCacheDir: dir, ToolFarmDir: farm})
	if !slices.Contains(cfg.HostConfig.Binds, dir+":"+RunnerToolCacheSharedMount+":ro") {
		t.Errorf("binds = %v, want the kept tool cache read-only at %s", cfg.HostConfig.Binds, RunnerToolCacheSharedMount)
	}
	if !slices.Contains(cfg.HostConfig.Binds, farm+":"+RunnerToolCacheMount) {
		t.Errorf("binds = %v, want the runner's own tool cache at %s", cfg.HostConfig.Binds, RunnerToolCacheMount)
	}
	if cfg.Labels[LabelToolFarm] != farm {
		t.Errorf("labels = %v, want the runner's own tool cache recorded for removal", cfg.Labels)
	}
	if !slices.Contains(cfg.Env, EnvToolsDirectory+"="+RunnerToolCacheMount) {
		t.Errorf("env = %v, want %s pointed at the mount", cfg.Env, EnvToolsDirectory)
	}
	if RunnerToolCacheMount == "/opt/hostedtoolcache" {
		t.Error("the tool cache is mounted over the image's own, hiding the toolchains an image was built with")
	}
}

// Off unless the pool asks and the host has somewhere to keep it: no mount,
// no variable, and the image's tool cache is what the runner uses.
func TestAToolCacheNeedsThePoolToAskAndAHostFolder(t *testing.T) {
	on := Spec{PoolID: "pool_one", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool, Tools: true}}
	for name, tc := range map[string]struct {
		spec   Spec
		shared string
	}{
		"not asked":        {Spec{PoolID: "pool_one", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool}}, t.TempDir()},
		"cache off":        {Spec{PoolID: "pool_one", Cache: store.CacheConfig{Scope: store.CacheScopePool, Tools: true}}, t.TempDir()},
		"no shared folder": {on, ""},
	} {
		if dir, _ := toolCacheDir(tc.spec, tc.shared); dir != "" {
			t.Errorf("%s: tool cache = %q, want none", name, dir)
		}
	}
	cfg := buildRunnerConfig(on, dockerFlavor(), containerOptions{})
	for _, e := range cfg.Env {
		if strings.HasPrefix(e, EnvToolsDirectory+"=") {
			t.Errorf("env names a tool cache nothing was mounted for: %s", e)
		}
	}
}

// The runner is not the agent. A folder the agent or the daemon creates for a
// bind is theirs, and the runner -- uid 1001 in the stock images -- could not
// write to its own cache. A new folder is opened to it; one an operator made
// is left as they made it.
func TestACacheFolderIsCreatedWritableByTheRunner(t *testing.T) {
	// The runner is another uid on a Linux host; Windows has no mode bits to
	// open up, and the Docker backend runs Linux runners.
	requirePOSIX(t)
	root := t.TempDir()
	dir := filepath.Join(root, "cache", "tools", "pool-one")
	if err := ensureRunnerWritableDir(dir); err != nil {
		t.Fatalf("ensureRunnerWritableDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o777 {
		t.Errorf("new folder mode = %v, want writable by every user", fi.Mode().Perm())
	}

	mine := filepath.Join(root, "operators")
	if err := os.Mkdir(mine, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensureRunnerWritableDir(mine); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(mine); fi.Mode().Perm() != 0o700 {
		t.Errorf("an existing folder's mode was changed to %v", fi.Mode().Perm())
	}
}

// A tool cache folder the runner cannot write to is worse than none: every
// setup action that downloads fails with EACCES instead of downloading. It is
// what a host's daemon leaves, root's, when the agent's container does not
// mount the shared folder -- and it outlives the mount being fixed, because a
// folder that exists is left as its owner made it. The runner starts without
// it, and the host says why.
func TestAToolCacheTheRunnerCannotWriteIsLeftOut(t *testing.T) {
	requirePOSIX(t)
	shared := t.TempDir()
	spec := Spec{Name: "r", PoolID: "pool_1", Cache: store.CacheConfig{Enabled: true, Tools: true, Scope: store.CacheScopePool}}
	dir, _ := toolCacheDir(spec, shared)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := &DockerBackend{sharedDir: shared, log: quietLogger()}

	var opts containerOptions
	b.prepareCacheDirs(spec, &opts)
	if os.Getuid() != stockRunnerUID {
		if opts.ToolCacheDir != "" {
			t.Fatalf("a folder the runner cannot write to was bound: %s", opts.ToolCacheDir)
		}
		if p := b.lastToolCacheProblem(); !strings.Contains(p, "chmod 0777") {
			t.Errorf("problem = %q, want one naming the fix", p)
		}
	}

	// Opened up, it is used again and the problem clears.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	opts = containerOptions{}
	b.prepareCacheDirs(spec, &opts)
	if opts.ToolCacheDir != dir || b.lastToolCacheProblem() != "" {
		t.Errorf("dir = %q, problem = %q; want the folder bound and no problem", opts.ToolCacheDir, b.lastToolCacheProblem())
	}
}

// A create that dies before the container exists has nothing carrying the farm's
// label, so Remove and the cleanup helper never find it, and a replacement runner
// has a new name and never rebuilds over it. The failure-cleanup defer is the
// only thing that can reap it, so the farm must not be built until that defer is
// in place -- an exhausted address pool or a stalled daemon on the network call
// would otherwise leave a tree of directories and links per attempt, for ever.
func TestAFailedNetworkSetupLeavesNoToolFarmBehind(t *testing.T) {
	requirePOSIX(t)
	shared := t.TempDir()
	spec := jitSpec()
	spec.Network = "zoomies-net"
	spec.Cache = store.CacheConfig{Enabled: true, Tools: true, Scope: store.CacheScopePool}
	dir, err := toolCacheDir(spec, shared)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "go", "1.27.1", "x64"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go", "1.27.1", "x64.complete"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	f := newFakeEngine(t, map[string]http.HandlerFunc{
		"DELETE " + v + "/containers/{id}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such container"})
		},
		"GET " + v + "/images/{ref...}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"Id": "sha256:cached"})
		},
		"GET " + v + "/networks/{name}": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such network"})
		},
		"POST " + v + "/networks/create": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "could not find an available, non-overlapping IPv4 address pool"})
		},
	})
	b := dockerBackendFor(t, f, DockerOptions{SharedDir: shared})

	if _, err := b.CreateWithResult(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "preparing network") {
		t.Fatalf("create err = %v, want the network failure", err)
	}
	if _, err := os.Stat(toolFarmDir(shared, spec.Name)); !os.IsNotExist(err) {
		t.Fatalf("the failed create left its tool farm behind: %v", err)
	}
}
