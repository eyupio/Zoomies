package store

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// shippedMigrations is every migration that has ever shipped, in order. The
// ledger keys on the full file name, so renaming one re-applies its DDL on
// every existing database: a name on this list is fixed for good, and a new
// migration is appended here as well as to the directory.
var shippedMigrations = []string{
	"0001_init.sql",
	"0002_host_backend_info.sql",
	"0003_pool_cache.sql",
	"0004_capacity_demand.sql",
	"0005_pool_priority.sql",
	"0005_runner_startup_timestamps.sql",
	"0006_image_prewarm.sql",
	"0006_usage_and_repository_quotas.sql",
	"0007_repository_scale_up_limit.sql",
	"0008_job_detail.sql",
	"0009_jobs_waiting_state.sql",
}

// The two prefixes shared by files that already shipped. They sort by what
// follows the underscore, which happened to be the right order; nothing after
// them may rely on that again.
var grandfatheredPrefixes = map[string]bool{"0005": true, "0006": true}

func TestMigrationNamesAreFixedAndNewPrefixesAreUnique(t *testing.T) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatalf("reading the embedded migrations: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)

	if !slices.Equal(names, shippedMigrations) {
		t.Fatalf("the migration directory does not match the shipped list:\n got  %v\n want %v\n"+
			"a shipped name must never change, and a new migration is added to both", names, shippedMigrations)
	}

	seen := map[string]string{}
	for _, name := range names {
		prefix, _, ok := strings.Cut(name, "_")
		if !ok || len(prefix) != 4 || strings.Trim(prefix, "0123456789") != "" {
			t.Fatalf("%s does not start with a four-digit prefix and an underscore", name)
		}
		if other, dup := seen[prefix]; dup && !grandfatheredPrefixes[prefix] {
			t.Fatalf("%s and %s share the prefix %s; take the next unused number", other, name, prefix)
		}
		seen[prefix] = name
	}
}
