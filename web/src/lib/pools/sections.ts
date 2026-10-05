/**
 * The pool editor's sections.
 *
 * A pool used to be edited as a procedure of eight steps, cut along the lines
 * of the code that handled each one rather than the questions an operator is
 * answering, and a step could not be reached except by pressing Next up to it.
 * The editor is one page now, and a section is a *decision*: who the pool
 * serves, where its runners may land, what one is made of, how big it is, how
 * many there are, and what makes it faster. Each one says what its answer is
 * on its own row (see `summaries.ts`), so the page can be read without opening
 * anything and any section can be reached in one tap.
 *
 * What lives here is the part that has to be agreed on by several places: which
 * API fields belong to which section, so that a refusal from the server opens
 * the section that can fix it, and which draft keys a section edits, so that an
 * edit can say which sections it changed.
 */
import type { PoolDraft } from './draft';

export type SectionId = 'basics' | 'hosts' | 'runner' | 'size' | 'scaling' | 'speed';

export interface SectionDef {
  id: SectionId;
  title: string;
  /** What this section decides, in a sentence; shown at the top once it is open. */
  description: string;
  /** The API's own field names, so a refusal can be pointed at the section that owns it. */
  fields: readonly string[];
  /** The draft keys it edits. A key may belong to two: Docker is asked in the first and set in the third. */
  keys: readonly (keyof PoolDraft)[];
}

/**
 * In the order the questions are best answered: who it is for, which machines,
 * what runs on them, how big, and how many. Hosts come before the runner
 * because the runner's "offered by N hosts" only means something once it is
 * known which hosts are in play, and the size before the count because a
 * maximum is a number about machines.
 */
export const SECTIONS: readonly SectionDef[] = [
  {
    id: 'basics',
    title: 'Name and labels',
    description: 'A name, the label workflows ask for, and whether jobs build container images.',
    fields: ['name', 'installation_id', 'runner_group', 'labels'],
    keys: ['name', 'installation_id', 'runner_group', 'labels', 'docker_mode'],
  },
  {
    id: 'hosts',
    title: 'Hosts',
    description: 'Which machines these runners may land on.',
    fields: ['host_selector'],
    keys: ['host_selector', 'restrict_hosts'],
  },
  {
    id: 'runner',
    title: 'Runner',
    description: 'What a runner is made of, and how it is run on a host.',
    fields: [
      'backend',
      'platform.os',
      'platform.os_version',
      'platform.arch',
      'image',
      'runner_version',
      'docker_mode',
      'run_as_root',
    ],
    keys: [
      'backend',
      'platform_os',
      'platform_os_version',
      'platform_arch',
      'image',
      'runner_version',
      'docker_mode',
      'run_as_root',
    ],
  },
  {
    id: 'size',
    title: 'Size',
    description: 'How much machine one runner gets, and whether it may borrow spare CPU.',
    fields: [
      'size_from_profile',
      'resources.cpus',
      'resources.memory_mb',
      'resources.min_cpus',
      'resources.min_memory_mb',
      'resources.daemon_share_percent',
      'resources.disk_gb',
      'resources.pids_limit',
      'cpu_burst.mode',
      'cpu_burst.max_cpus',
      'cpu_burst.size_for_ceiling',
    ],
    keys: [
      'sizing',
      'cpus',
      'memory_mb',
      'min_cpus',
      'min_memory_mb',
      'daemon_share',
      'disk_gb',
      'pids_limit',
      'cpu_burst_mode',
      'cpu_burst_max',
      'cpu_burst_size_builds',
    ],
  },
  {
    id: 'scaling',
    title: 'Scaling',
    description: 'How many runners there may be, how long one waits for work, and its timings.',
    fields: [
      'min_runners',
      'max_runners',
      'priority',
      'idle_timeout',
      'ephemeral',
      'no_default_labels',
      'runner_settings.provision_timeout',
      'runner_settings.drain_timeout',
      'runner_settings.max_runner_lifetime',
      'runner_settings.scale_up_delay',
      'runner_settings.docker_wait',
    ],
    keys: [
      'min_runners',
      'max_runners',
      'priority',
      'idle_timeout',
      'ephemeral',
      'no_default_labels',
      'provision_timeout',
      'drain_timeout',
      'max_runner_lifetime',
      'scale_up_delay',
      'docker_wait',
    ],
  },
  {
    id: 'speed',
    title: 'Speed-ups',
    description: 'Scratch space kept in memory, and a cache shared between runners.',
    fields: [
      'cache.enabled',
      'cache.tools',
      'cache.scope',
      'cache.size_limit',
      'cache.source',
      'cache.repository',
      'tmpfs.work.enabled',
      'tmpfs.work.size_mb',
      'tmpfs.tmp.enabled',
      'tmpfs.tmp.size_mb',
      'tmpfs.daemon.enabled',
      'tmpfs.daemon.size_mb',
    ],
    keys: [
      'cache_enabled',
      'cache_tools',
      'cache_scope',
      'cache_size_limit',
      'cache_source',
      'cache_repository',
      'tmpfs_auto',
      'tmpfs_work',
      'tmpfs_work_size',
      'tmpfs_tmp',
      'tmpfs_tmp_size',
      'tmpfs_daemon',
      'tmpfs_daemon_size',
    ],
  },
];

export function sectionDef(id: SectionId): SectionDef {
  const found = SECTIONS.find((section) => section.id === id);
  // Every id is in the list above; the fallback keeps the type honest.
  return found ?? (SECTIONS[0] as SectionDef);
}

/** What a section's row is headed with: the three props the row takes from it. */
export function sectionHead(id: SectionId): { id: SectionId; title: string; description: string } {
  const { title, description } = sectionDef(id);
  return { id, title, description };
}

/**
 * The section that owns a field, or null for one the editor does not show --
 * the pool's environment is carried through untouched, for instance -- so the
 * caller can say the refusal on the page rather than opening the wrong section.
 */
export function sectionForField(field: string): SectionId | null {
  const found = SECTIONS.find((section) => section.fields.includes(field));
  return found ? found.id : null;
}

function slice(draft: PoolDraft, keys: readonly (keyof PoolDraft)[]): string {
  return JSON.stringify(keys.map((key) => draft[key]));
}

/**
 * The sections whose answers differ from where the editor started.
 *
 * Compared by value rather than by tracking which control was touched, so that
 * typing a figure and typing the old one back is not an edit -- and a section
 * marked "Edited" is one that Save will actually change.
 */
export function editedSections(draft: PoolDraft, initial: PoolDraft): Set<SectionId> {
  const out = new Set<SectionId>();
  for (const section of SECTIONS) {
    if (slice(draft, section.keys) !== slice(initial, section.keys)) out.add(section.id);
  }
  return out;
}
