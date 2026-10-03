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
	"0010_docker_pools_get_a_client.sql",
	"0011_platform.sql",
	"0012_job_installation.sql",
	"0013_controller_lease.sql",
	"0014_task_issue_and_agent_sessions.sql",
	"0015_cleanup_record.sql",
	"0016_draining_since.sql",
	"0017_host_resources.sql",
	"0018_fleet_scoped_samples.sql",
	"0019_job_eligible_at.sql",
	"0020_host_protocol.sql",
	"0021_audit_action_index.sql",
	"0022_cleanup_failure_sources.sql",
	"0023_runner_confirmed_timings.sql",
	"0024_host_cleanup_recovery.sql",
	"0025_usage_capacity.sql",
	"0026_provisioning_queue.sql",
	"0027_host_connection.sql",
	"0028_host_usage.sql",
	"0029_host_throttle_and_runner_allocation.sql",
	"0030_providers.sql",
	"0031_instance_settings.sql",
	"0032_host_samples.sql",
	"0033_provider_connection.sql",
	"0034_job_fault_kind.sql",
	"0035_runner_fault_kind.sql",
	"0036_backup_remotes.sql",
	"0037_pool_runner_settings.sql",
	"0038_runner_resource_sample.sql",
	"0039_pool_cpu_burst.sql",
	"0040_user_preferences.sql",
	"0041_job_cancellation_requested.sql",
	"0042_host_features.sql",
	"0043_job_run_number.sql",
	"0044_platform_role.sql",
	"0045_upgrade_keeps_what_admins_had.sql",
	"0046_token_owner_role.sql",
	"0047_runner_sessions.sql",
	"0048_usage_daily.sql",
	"0049_host_incidents.sql",
	"0051_installation_report.sql",
	"0052_pool_no_default_labels.sql",
	"0053_job_run_number_backfill.sql",
	"0054_jobs_run_index.sql",
	"0055_control_enrichment.sql",
	"0056_startup_service_time.sql",
	"0057_two_step.sql", "0058_runner_sized_for_cpus.sql",
	"0059_mcp_oauth.sql",
	"0060_job_versions.sql",
	"0061_job_usage.sql",
	"0062_job_indexes_for_scans.sql",
	"0063_ai_context.sql",
	"0064_ai_context_setup.sql",
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
