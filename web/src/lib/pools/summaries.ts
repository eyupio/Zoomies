/**
 * What each section of the pool editor answers, in a line.
 *
 * Every section is a row that says its current answer without being opened, so
 * the whole pool can be read down the page: "One share of each host · elastic
 * CPU observing" tells an operator more than a closed row labelled "Size", and
 * it is the reason a first-time reader can see that every default has already
 * been chosen for them. The line is derived from the draft rather than stored,
 * so it cannot disagree with the controls under it.
 *
 * Pure, so the unit tests can hold the wording: this is the sentence an operator
 * reads instead of the form, and a figure that drifted from the control would
 * be believed.
 */
import type { Platform } from '$lib/api/types';
import { shortGoDuration } from '$lib/format';
import type { PoolDraft } from './draft';
import type { SectionId } from './sections';
import { cpuLabel, memoryLabel } from './sizing';
import { backendLabel, platformLabel } from './vocabulary';

export interface SummaryContext {
  /** The GitHub organisation or repository the chosen installation covers. */
  installation: string;
  /** How many hosts the fleet has, or null until it has been asked. */
  hostsTotal: number | null;
  /** How many of them this pool's host selector reaches, or null until known. */
  hostsMatching: number | null;
  /** How many providers are configured, or null until they have been asked. */
  providersTotal: number | null;
  /** How many of them this pool's provider selector allows, or null until known. */
  providersMatching: number | null;
}

const NONE: SummaryContext = {
  installation: '',
  hostsTotal: null,
  hostsMatching: null,
  providersTotal: null,
  providersMatching: null,
};

function count(value: string): number {
  const n = Number(value.trim());
  return Number.isFinite(n) ? n : 0;
}

function join(parts: readonly (string | false | undefined)[]): string {
  return parts
    .filter((part): part is string => typeof part === 'string' && part !== '')
    .join(' · ');
}

function basics(draft: PoolDraft, ctx: SummaryContext): string {
  const labels = draft.labels.map((label) => label.trim()).filter(Boolean);
  return join([
    labels.length > 0 ? `runs-on ${labels.join(', ')}` : 'no labels yet',
    ctx.installation || 'no GitHub connection chosen',
    draft.docker_mode === 'dind' && 'Docker for jobs',
    draft.docker_mode === 'host-socket' && 'the host’s Docker socket',
  ]);
}

function hosts(draft: PoolDraft, ctx: SummaryContext): string {
  const rules = Object.entries(draft.host_selector);
  if (!draft.restrict_hosts && rules.length === 0) {
    return ctx.hostsTotal === null || ctx.hostsTotal === 0
      ? 'Any host that can run it'
      : `Any host that can run it · ${ctx.hostsTotal} connected`;
  }
  if (rules.length === 0) return 'Only some hosts · no rule chosen yet';
  const shown = rules.map(([key, value]) => `${key}=${value}`).join(', ');
  return join([
    `Only hosts where ${shown}`,
    ctx.hostsMatching !== null && `${ctx.hostsMatching} match`,
  ]);
}

function providers(draft: PoolDraft, ctx: SummaryContext): string {
  const rules = Object.entries(draft.provider_selector);
  if (rules.length === 0) {
    return ctx.providersTotal === null || ctx.providersTotal === 0
      ? 'Any provider that can build a machine for it'
      : `Any provider that can build a machine for it · ${ctx.providersTotal} configured`;
  }
  const shown = rules.map(([key, value]) => (value === '' ? key : `${key}=${value}`)).join(', ');
  return join([
    `Only providers where ${shown}`,
    ctx.providersMatching !== null && `${ctx.providersMatching} match`,
  ]);
}

function runner(draft: PoolDraft): string {
  // The draft holds plain strings because that is what a <select> gives back;
  // the API's enums are narrower, and the server is what rejects a value
  // outside them.
  const platform: Platform = {};
  if (draft.platform_os) platform.os = draft.platform_os as Platform['os'];
  if (draft.platform_os_version) platform.os_version = draft.platform_os_version;
  if (draft.platform_arch) platform.arch = draft.platform_arch as Platform['arch'];
  return join([
    backendLabel(draft.backend),
    draft.backend === 'process' ? false : platformLabel(platform) || 'default image',
    draft.image.trim() !== '' && 'your own image',
    draft.runner_version.trim() !== '' && `runner ${draft.runner_version.trim()}`,
    draft.run_as_root && 'runs as root',
  ]);
}

function size(draft: PoolDraft): string {
  const containers = draft.backend === 'docker' || draft.backend === 'podman';
  const base =
    draft.sizing === 'fixed'
      ? `${cpuLabel(count(draft.cpus))} and ${memoryLabel(count(draft.memory_mb))} on every host`
      : draft.sizing === 'profile'
        ? 'The size each host sets'
        : 'One share of each host';
  const floor = [
    count(draft.min_cpus) > 0 ? cpuLabel(count(draft.min_cpus)) : '',
    count(draft.min_memory_mb) > 0 ? memoryLabel(count(draft.min_memory_mb)) : '',
  ].filter(Boolean);
  return join([
    base,
    floor.length > 0 && `never below ${floor.join(' and ')}`,
    containers && elastic(draft),
  ]);
}

/**
 * What the pool may borrow, in one phrase. CPU needs a size left to the host,
 * because the host's share is its guaranteed base; memory needs only a limit to
 * watch, so a pool with a fixed size still says it. Two valves on the same
 * footing are one phrase rather than two.
 */
function elastic(draft: PoolDraft): string {
  const cpu = draft.sizing === 'fixed' ? 'off' : draft.cpu_burst_mode;
  const memory = draft.memory_burst_mode;
  if (cpu === 'observe' && memory === 'observe') return 'elastic CPU and memory observing';
  return join([
    cpu !== 'off' && `elastic CPU ${cpu === 'automatic' ? 'boosting' : 'observing'}`,
    memory !== 'off' && `elastic memory ${memory === 'automatic' ? 'lending' : 'observing'}`,
  ]);
}

function timings(draft: PoolDraft): number {
  return [
    draft.provision_timeout,
    draft.drain_timeout,
    draft.max_runner_lifetime,
    draft.scale_up_delay,
    draft.docker_wait,
  ].filter((value) => value.trim() !== '').length;
}

function scaling(draft: PoolDraft): string {
  // "5m" is how an operator says it; the API says "5m0s" and the display form
  // of the same duration reads "5m 00s".
  const idle = shortGoDuration(draft.idle_timeout);
  const overrides = timings(draft);
  return join([
    `${count(draft.min_runners)} to ${count(draft.max_runners)} runners`,
    idle && `idle ${idle}`,
    !draft.ephemeral && 'reused between jobs',
    count(draft.priority) !== 0 && `priority ${count(draft.priority)}`,
    overrides > 0 && `${overrides} timing ${overrides === 1 ? 'override' : 'overrides'}`,
  ]);
}

function speed(draft: PoolDraft): string {
  const folders = [
    draft.tmpfs_work && 'work folder',
    draft.tmpfs_tmp && '/tmp',
    draft.tmpfs_daemon && draft.backend === 'docker' && draft.docker_mode === 'dind'
      ? 'image store'
      : false,
  ].filter((name): name is string => typeof name === 'string');
  const containers = draft.backend === 'docker' || draft.backend === 'podman';
  return join([
    containers && folders.length > 0
      ? `${folders.join(', ')} in memory${draft.tmpfs_auto ? ' where there is room' : ''}`
      : 'Scratch space on disk',
    draft.cache_enabled
      ? `${draft.cache_scope === 'repository' ? 'per-repository' : 'shared'} cache${draft.cache_tools ? ' with tools' : ''}`
      : 'no cache',
  ]);
}

/** The line each section's row carries, for the draft as it stands. */
export function summarise(
  draft: PoolDraft,
  context: Partial<SummaryContext> = {},
): Record<SectionId, string> {
  const ctx = { ...NONE, ...context };
  return {
    basics: basics(draft, ctx),
    hosts: hosts(draft, ctx),
    providers: providers(draft, ctx),
    runner: runner(draft),
    size: size(draft),
    scaling: scaling(draft),
    speed: speed(draft),
  };
}
