package controller

import (
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// A repository cache under an organisation installation is only as private
// as the pool's labels, because GitHub can hand an organisation-registered
// runner any repository's job that asks for them. That is a fact about the
// installation, which the pool cannot see in itself, so the warning has to
// come from the pairing -- and must not fire where the installation already
// confines the runner to one repository.
func TestPoolWarningsSayWhenARepositoryCacheIsOnlyAsPrivateAsItsLabels(t *testing.T) {
	cache := store.CacheConfig{Enabled: true, Scope: store.CacheScopeRepository, Repository: "acme/widgets"}
	org := &store.Installation{Target: "acme", TargetType: store.TargetOrg}
	repo := &store.Installation{Target: "acme/widgets", TargetType: store.TargetRepo}

	cases := []struct {
		name string
		pool store.Pool
		inst *store.Installation
		want bool
	}{
		{"repository cache under an organisation", store.Pool{Name: "widgets", Cache: cache}, org, true},
		{"the same cache under a repository installation", store.Pool{Name: "widgets", Cache: cache}, repo, false},
		{"pool scope is a different choice, documented as such", store.Pool{Name: "shared", Cache: store.CacheConfig{Enabled: true, Scope: store.CacheScopePool}}, org, false},
		{"a disabled cache", store.Pool{Name: "plain", Cache: store.CacheConfig{Scope: store.CacheScopeRepository, Repository: "acme/widgets"}}, org, false},
		{"an unknown installation, which validation reports itself", store.Pool{Name: "orphan", Cache: cache}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var found *Problem
			for _, w := range PoolWarnings(&tc.pool, tc.inst) {
				if w.Code == "pool.cache_shared" {
					w := w
					found = &w
				}
			}
			if (found != nil) != tc.want {
				t.Fatalf("warned = %v, want %v; warnings: %+v", found != nil, tc.want, PoolWarnings(&tc.pool, tc.inst))
			}
			if found == nil {
				return
			}
			// The sentence has to carry the mechanism, the repository at
			// stake and the one thing the operator can do about it.
			for _, want := range []string{"acme/widgets", "labels"} {
				if !strings.Contains(found.Title+found.Detail+found.Fix, want) {
					t.Errorf("the warning does not mention %q: %+v", want, *found)
				}
			}
			if found.Fix == "" || found.TargetID != tc.pool.ID {
				t.Errorf("the warning needs a fix and a target: %+v", *found)
			}
		})
	}
}
