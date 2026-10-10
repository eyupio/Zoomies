/**
 * What Eli's tools are called to a person. The tool names are the MCP server's,
 * which are written for an agent; a person reading "Looked at list_runners" is
 * reading somebody else's variable.
 */
const LABELS: Record<string, string> = {
  fleet_status: 'the fleet at a glance',
  list_problems: 'the problems',
  list_jobs: 'jobs',
  get_job: 'a job',
  job_stats: 'job statistics',
  list_runners: 'runners',
  list_pools: 'pools',
  list_hosts: 'hosts',
  host_health: 'a host’s health',
  label_advice: 'labels',
  get_runner_log: 'a runner’s log',
  kennel_overview: 'Kennel Club',
  kennel_repository: 'a repository in Kennel Club',
  kennel_findings: 'Kennel Club findings',
  list_providers: 'infrastructure providers',
  provider_pairings: 'which providers serve which pools',
  list_machines: 'rented machines',
  get_catalog: 'the catalog of codes and checks',
};

export function toolLabel(name: string): string {
  return LABELS[name] ?? name.replace(/_/g, ' ');
}
