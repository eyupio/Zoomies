package backend

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

func TestCacheMountConstructionAndDisabledBehavior(t *testing.T) {
	s := Spec{PoolID: "pool_one", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool}}
	cfg := buildRunnerConfig(s, dockerFlavor(), containerOptions{})
	want := "zoomies-cache-pool-one:" + RunnerCacheMount
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
	if got != "zoomies-cache-p:"+RunnerCacheMount+":z" {
		t.Fatalf("bind = %q", got)
	}
}
