/**
 * The pool editor's draft: what is being edited, how it becomes a request body,
 * and the rules that can be checked without asking the server.
 *
 * Pure, and in a `.ts` rather than in the form's `<script module>`, so that the
 * unit tests -- which run under Node with no Svelte compiler -- can import it.
 * A rule that lives inside a component is a rule nothing can test without a
 * browser, and these are the rules an operator meets first.
 */
import type {
  BackendKind,
  DockerMode,
  Platform,
  Pool,
  PoolCreate,
  Resources,
} from '$lib/api/types';
import { brandedName } from '$lib/brand';
import { parseGoDuration } from '$lib/format';
import { MIN_TMPFS_MB, memoryLabel } from './sizing';
import { backendUnavailable } from './vocabulary';
import type { BackendOffer } from './vocabulary';

/**
 * What the editor is editing.
 *
 * Numbers are held as strings because that is what a text input gives back,
 * and because "" and "0" are different answers -- one is "not filled in yet",
 * the other is a deliberate zero. They become numbers exactly once, in
 * `toPoolBody`.
 */
export interface PoolDraft {
  name: string;
  installation_id: string;
  runner_group: string;
  labels: string[];
  backend: BackendKind;
  /**
   * The machine these runners need. Empty means the pool promises nothing,
   * which is what a pool created before platforms existed looks like.
   */
  platform_os: string;
  platform_os_version: string;
  platform_arch: string;
  image: string;
  runner_version: string;
  min_runners: string;
  max_runners: string;
  priority: string;
  idle_timeout: string;
  ephemeral: boolean;
  no_default_labels: boolean;
  docker_mode: DockerMode;
  run_as_root: boolean;
  enabled: boolean;
  /**
   * How this pool decides what one runner gets.
   *
   * `automatic` sets no CPU and no memory on the pool at all: the scheduler
   * charges each runner one slot's share of the host it lands on and gives
   * it exactly that share as a real cgroup limit, so a fleet of unequal
   * machines is sized correctly on every one of them without anybody typing
   * a number. `profile` is the same absence with one more answer given: each
   * runner is the standard size of the host's own runner profile, and the
   * pool carries `size_from_profile` to say so. `fixed` is the two figures
   * below, the same on every host.
   *
   * It is editor state rather than a pool field because `automatic` and
   * `fixed` are told apart by emptiness, and holding the choice separately is
   * what keeps the sliders' last position while an operator looks at the
   * other answers and changes their mind back.
   */
  sizing: 'automatic' | 'fixed' | 'profile';
  cpu_burst_mode: 'off' | 'observe' | 'automatic';
  cpu_burst_max: string;
  cpu_burst_size_builds: boolean;
  /**
   * The memory valve: `off`, `observe` (decide what it would lend and record it,
   * changing nothing) or `automatic` (raise a live runner's limit out of memory
   * its host has not promised to anyone). A new pool starts on observe, for the
   * reason elastic CPU does: the honest first step is to watch.
   */
  memory_burst_mode: 'off' | 'observe' | 'automatic';
  /** The most one runner may hold, its own share and what it is lent together, in MB; empty is half as much again as it starts with. */
  memory_burst_max: string;
  /** The swap each container may use past its limit as the last resort, in MB; empty is none. */
  memory_burst_spill: string;
  cpus: string;
  memory_mb: string;
  /** The least a runner may be given where no host has room for the size above; empty is none. */
  min_cpus: string;
  min_memory_mb: string;
  /**
   * The sidecar's share of a slot's CPU and of its memory, in percent, as typed;
   * empty is the even split. Two figures because a build is CPU in the sidecar
   * while the runner holds the memory.
   */
  daemon_cpu_share: string;
  daemon_memory_share: string;
  /** Whether the division has been chosen, so a recommended one is only ever the start. */
  split_chosen: boolean;
  disk_gb: string;
  /**
   * The fleet timings this pool overrides. Empty is "follow the fleet",
   * which is what every pool does until somebody says otherwise -- and an
   * operator who clears one of these inputs is saying it again.
   */
  provision_timeout: string;
  drain_timeout: string;
  max_runner_lifetime: string;
  scale_up_delay: string;
  docker_wait: string;
  cache_enabled: boolean;
  cache_tools: boolean;
  cache_scope: 'pool' | 'repository';
  cache_size_limit: string;
  cache_source: string;
  cache_repository: string;
  /**
   * Folders kept in memory instead of on the host's disk. Off for every pool
   * until somebody chooses otherwise, because a tmpfs is charged to the
   * runner's memory limit: the size here is room taken out of it. A size left
   * empty is fitted to the limit by the controller.
   */
  /**
   * Whether each in-memory folder is placed per runner (in memory where it has
   * room to be useful, on disk where it has not) rather than always in memory.
   * It is the recommended answer, so it is what a pool starts with; a pool saved
   * with a folder that was always in memory keeps that.
   */
  tmpfs_auto: boolean;
  tmpfs_work: boolean;
  tmpfs_work_size: string;
  tmpfs_tmp: boolean;
  tmpfs_tmp_size: string;
  /** The Docker-in-Docker sidecar's image store; only a dind pool has one. */
  tmpfs_daemon: boolean;
  tmpfs_daemon_size: string;
  /** Carried through untouched: the editor does not edit it, and must not lose it. */
  pids_limit: string;
  host_selector: Record<string, string>;
  /**
   * The editor's own state, not the pool's: whether the operator chose to
   * keep this pool to some hosts. The selector cannot answer it, because
   * "only hosts that match" with no rule typed yet is an empty map exactly
   * like "any host" -- and reading the choice back off the selector is how
   * a half-made rule reset itself when the section was shut and opened again.
   */
  restrict_hosts: boolean;
  env: Record<string, string>;
}

export function emptyDraft(): PoolDraft {
  return {
    name: '',
    installation_id: '',
    runner_group: '',
    labels: [],
    backend: 'docker',
    platform_os: '',
    platform_os_version: '',
    platform_arch: '',
    image: '',
    runner_version: '',
    min_runners: '0',
    max_runners: '4',
    priority: '0',
    idle_timeout: '5m',
    ephemeral: true,
    no_default_labels: false,
    docker_mode: 'none',
    run_as_root: false,
    enabled: true,
    // A new pool leaves its size to its hosts, which is what the server
    // does with a pool that names none. The sliders below are filled from
    // the fleet's suggestion as soon as it is known -- see the effect that
    // fetches it -- so that an operator who switches to a fixed size opens
    // on the fleet's own answer rather than on an empty box.
    sizing: 'automatic',
    cpu_burst_mode: 'observe',
    cpu_burst_max: '',
    cpu_burst_size_builds: true,
    memory_burst_mode: 'observe',
    memory_burst_max: '',
    memory_burst_spill: '',
    cpus: '',
    memory_mb: '',
    min_cpus: '',
    min_memory_mb: '',
    daemon_cpu_share: '',
    daemon_memory_share: '',
    split_chosen: false,
    disk_gb: '',
    provision_timeout: '',
    drain_timeout: '',
    max_runner_lifetime: '',
    scale_up_delay: '',
    docker_wait: '',
    cache_enabled: false,
    cache_tools: false,
    cache_scope: 'pool',
    cache_size_limit: '',
    cache_source: '',
    cache_repository: '',
    tmpfs_auto: true,
    tmpfs_work: false,
    tmpfs_work_size: '',
    tmpfs_tmp: false,
    tmpfs_tmp_size: '',
    tmpfs_daemon: false,
    tmpfs_daemon_size: '',
    pids_limit: '',
    host_selector: {},
    restrict_hosts: false,
    env: {},
  };
}

function fromNumber(value: number | undefined): string {
  return value === undefined || value === null ? '' : String(value);
}

export function draftFromPool(pool: Pool): PoolDraft {
  const base = emptyDraft();
  const resources = pool.resources ?? {};
  return {
    ...base,
    name: pool.name ?? '',
    installation_id: pool.installation_id ?? '',
    runner_group: pool.runner_group ?? '',
    labels: [...(pool.labels ?? [])],
    backend: pool.backend ?? 'docker',
    platform_os: pool.platform?.os ?? '',
    platform_os_version: pool.platform?.os_version ?? '',
    platform_arch: pool.platform?.arch ?? '',
    image: pool.image ?? '',
    runner_version: pool.runner_version ?? '',
    min_runners: fromNumber(pool.min_runners),
    max_runners: fromNumber(pool.max_runners),
    priority: fromNumber(pool.priority),
    idle_timeout: pool.idle_timeout ?? base.idle_timeout,
    ephemeral: pool.ephemeral !== false,
    no_default_labels: pool.no_default_labels === true,
    docker_mode: pool.docker_mode ?? 'none',
    run_as_root: pool.run_as_root === true,
    enabled: pool.enabled !== false,
    // The server says which of the two this pool is doing rather than the
    // browser inferring it from two absent numbers, because "no CPU limit"
    // alone cannot tell "the host decides" from "nobody set one".
    sizing: pool.sizing === 'fixed' ? 'fixed' : pool.sizing === 'profile' ? 'profile' : 'automatic',
    cpu_burst_mode: pool.cpu_burst?.mode ?? 'off',
    cpu_burst_max: fromNumber(pool.cpu_burst?.max_cpus),
    cpu_burst_size_builds: pool.cpu_burst?.size_for_ceiling ?? true,
    memory_burst_mode: pool.memory_burst?.mode ?? 'off',
    memory_burst_max: fromNumber(pool.memory_burst?.max_memory_mb || undefined),
    memory_burst_spill: fromNumber(pool.memory_burst?.spill_mb || undefined),
    cpus: fromNumber(resources.cpus),
    memory_mb: fromNumber(resources.memory_mb),
    min_cpus: fromNumber(resources.min_cpus),
    min_memory_mb: fromNumber(resources.min_memory_mb),
    // A pool that said one figure said it for both; a specific one wins. A pool
    // being edited has its division already, so it is never preselected over.
    daemon_cpu_share: fromNumber(
      resources.daemon_cpu_share_percent ?? resources.daemon_share_percent,
    ),
    daemon_memory_share: fromNumber(
      resources.daemon_memory_share_percent ?? resources.daemon_share_percent,
    ),
    split_chosen: true,
    disk_gb: fromNumber(resources.disk_gb),
    provision_timeout: pool.runner_settings?.provision_timeout ?? '',
    drain_timeout: pool.runner_settings?.drain_timeout ?? '',
    max_runner_lifetime: pool.runner_settings?.max_runner_lifetime ?? '',
    scale_up_delay: pool.runner_settings?.scale_up_delay ?? '',
    docker_wait: pool.runner_settings?.docker_wait ?? '',
    cache_enabled: pool.cache?.enabled === true,
    cache_tools: pool.cache?.tools === true,
    cache_scope: pool.cache?.scope ?? 'pool',
    cache_size_limit: fromNumber(pool.cache?.size_limit),
    cache_source: pool.cache?.source ?? '',
    cache_repository: pool.cache?.repository ?? '',
    tmpfs_auto: (['work', 'tmp', 'daemon'] as const).every((k) => {
      const m = pool.tmpfs?.[k];
      return !m?.enabled || m.auto === true;
    }),
    tmpfs_work: pool.tmpfs?.work?.enabled === true,
    tmpfs_work_size: fromNumber(pool.tmpfs?.work?.size_mb),
    tmpfs_tmp: pool.tmpfs?.tmp?.enabled === true,
    tmpfs_tmp_size: fromNumber(pool.tmpfs?.tmp?.size_mb),
    tmpfs_daemon: pool.tmpfs?.daemon?.enabled === true,
    tmpfs_daemon_size: fromNumber(pool.tmpfs?.daemon?.size_mb),
    pids_limit: fromNumber(resources.pids_limit),
    host_selector: { ...(pool.host_selector ?? {}) },
    restrict_hosts: Object.keys(pool.host_selector ?? {}).length > 0,
    env: { ...(pool.env ?? {}) },
  };
}

/**
 * Whether this draft holds anything the simple path cannot show.
 *
 * It decides which path an edit opens on, and it is deliberately generous:
 * the cost of opening the advanced path on a plain pool is one extra click,
 * and the cost of opening the simple path on a tuned one is an operator
 * saving away a host selector or a provision timeout they never saw.
 */
export function poolIsTuned(draft: PoolDraft): boolean {
  return (
    draft.sizing !== 'automatic' ||
    draft.cpu_burst_mode === 'automatic' ||
    draft.memory_burst_mode === 'automatic' ||
    draft.restrict_hosts ||
    Object.keys(draft.host_selector).length > 0 ||
    draft.backend !== 'docker' ||
    draft.docker_mode === 'host-socket' ||
    draft.run_as_root ||
    draft.cache_enabled ||
    draft.tmpfs_work ||
    draft.tmpfs_tmp ||
    draft.tmpfs_daemon ||
    draft.image.trim() !== '' ||
    draft.runner_version.trim() !== '' ||
    draft.platform_os.trim() !== '' ||
    draft.platform_arch.trim() !== '' ||
    draft.disk_gb.trim() !== '' ||
    draft.pids_limit.trim() !== '' ||
    draft.provision_timeout.trim() !== '' ||
    draft.drain_timeout.trim() !== '' ||
    draft.max_runner_lifetime.trim() !== '' ||
    draft.scale_up_delay.trim() !== '' ||
    draft.docker_wait.trim() !== ''
  );
}

/** A duration input as the API takes it: the text, or null for "follow the fleet". */
function nullableDuration(value: string): string | null {
  const trimmed = value.trim();
  return trimmed === '' ? null : trimmed;
}

export function toNumber(value: string): number | undefined {
  const trimmed = value.trim();
  if (trimmed === '') return undefined;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? parsed : undefined;
}

export function toInteger(value: string): number | undefined {
  const parsed = toNumber(value);
  return parsed !== undefined && Number.isInteger(parsed) ? parsed : undefined;
}

/**
 * The request body this draft produces.
 *
 * On create, empty optional fields are left out and the server fills in its
 * defaults. On edit they are sent as empty: a PATCH treats an absent key as
 * "leave it as it is", so leaving them out would make clearing the image, a
 * resource limit or the last host-selector entry a change the server never
 * hears about -- while the toast says it was saved.
 */
export function toPoolBody(draft: PoolDraft, options: { complete?: boolean } = {}): PoolCreate {
  const resources: Resources = {};
  const fixed = draft.sizing === 'fixed';
  const elasticBackend = draft.backend === 'docker' || draft.backend === 'podman';
  const hasSidecar = draft.backend === 'docker' && draft.docker_mode === 'dind';
  const cpus = toNumber(draft.cpus);
  const memory = toInteger(draft.memory_mb);
  const disk = toInteger(draft.disk_gb);
  const pids = toInteger(draft.pids_limit);
  // Only a fixed pool sends a size. An automatic one sends neither figure,
  // whatever the sliders happen to be holding, because absence is how the
  // API is told to leave the size to the host -- and the sliders keep their
  // position so that switching back does not lose what was chosen.
  if (fixed && cpus !== undefined) resources.cpus = cpus;
  if (fixed && memory !== undefined) resources.memory_mb = memory;
  // A minimum is a floor under either kind of size: the figures of a fixed
  // pool, or the host's share for an automatic one, where it is what lets a
  // runner start on a host with a free slot but less than a share left. An
  // empty field sends nothing, which the fleet reads as "follow
  // runners.minimum_*" -- live, so a later change to that setting moves
  // this pool too.
  const minCpus = toNumber(draft.min_cpus);
  const minMemory = toInteger(draft.min_memory_mb);
  if (minCpus !== undefined && minCpus > 0) resources.min_cpus = minCpus;
  if (minMemory !== undefined && minMemory > 0) resources.min_memory_mb = minMemory;
  // How a host-sized slot is divided between the runner and its Docker
  // sidecar. It means nothing for a fixed size (both containers get the whole
  // figure), so it is not sent there, and empty is the even split.
  if (draft.backend === 'docker' && draft.docker_mode === 'dind' && !fixed) {
    // Each share is sent on its own, and an even one is left out: absent is the
    // even split, so the stored pool stays as small as what was said.
    const cpuShare = toInteger(draft.daemon_cpu_share);
    const memoryShare = toInteger(draft.daemon_memory_share);
    if (cpuShare !== undefined && cpuShare > 0 && cpuShare !== 50)
      resources.daemon_cpu_share_percent = cpuShare;
    if (memoryShare !== undefined && memoryShare > 0 && memoryShare !== 50)
      resources.daemon_memory_share_percent = memoryShare;
  }
  // Disk and the pids limit are independent of the choice: neither has a
  // share to be given, so a pool may cap its cache's disk and still leave
  // its size to the host.
  if (disk !== undefined) resources.disk_gb = disk;
  if (pids !== undefined) resources.pids_limit = pids;

  const body: PoolCreate = {
    // Always sent, true or false, because a PATCH reads an absent key as
    // "leave it alone": a pool moved back from the host's profile to a share
    // or a stated size would otherwise keep taking its size from the host
    // while the toast says it was saved.
    size_from_profile: draft.sizing === 'profile',
    name: brandedName(draft.name),
    installation_id: draft.installation_id,
    labels: draft.labels.map((label) => label.trim()).filter(Boolean),
    backend: draft.backend,
    min_runners: toInteger(draft.min_runners) ?? 0,
    max_runners: toInteger(draft.max_runners) ?? 1,
    priority: toInteger(draft.priority) ?? 0,
    idle_timeout: draft.idle_timeout.trim() || '5m',
    ephemeral: draft.ephemeral,
    // GitHub adds the default labels to every ephemeral runner itself, so
    // the setting only survives on a pool that reuses its runners.
    no_default_labels: !draft.ephemeral && draft.no_default_labels,
    docker_mode: draft.docker_mode,
    cpu_burst: {
      mode: fixed || !elasticBackend ? 'off' : draft.cpu_burst_mode,
      max_cpus: fixed || !elasticBackend ? 0 : (toNumber(draft.cpu_burst_max) ?? 0),
      size_for_ceiling: draft.cpu_burst_size_builds,
    },
    // The valve is a container runtime's feature, and its figures mean something
    // only while it is on: the server refuses swap on a valve that is off, so an
    // off valve sends none, whatever the fields still hold.
    memory_burst: {
      mode: elasticBackend ? draft.memory_burst_mode : 'off',
      max_memory_mb:
        elasticBackend && draft.memory_burst_mode !== 'off'
          ? (toInteger(draft.memory_burst_max) ?? 0)
          : 0,
      spill_mb:
        elasticBackend && draft.memory_burst_mode !== 'off'
          ? (toInteger(draft.memory_burst_spill) ?? 0)
          : 0,
    },
    run_as_root: draft.run_as_root,
    enabled: draft.enabled,
    cache: {
      enabled: draft.cache_enabled,
      // Only a container runner has a tool cache to keep, and only with the
      // cache on; anything else would be refused by the server.
      tools:
        draft.cache_enabled &&
        draft.cache_tools &&
        (draft.backend === 'docker' || draft.backend === 'podman'),
      scope: draft.cache_scope,
      size_limit: toInteger(draft.cache_size_limit) ?? 0,
      source: draft.cache_source.trim(),
      repository: draft.cache_scope === 'repository' ? draft.cache_repository.trim() : '',
    },
    // Only a container runner has a folder to mount over; a process runner
    // would be refused, so its draft sends both off whatever the toggles say.
    // A size is sent only for a folder that is on, and an empty one means "fit
    // it to the memory limit", which is how the API reads a zero.
    tmpfs: {
      work: {
        enabled: elasticBackend && draft.tmpfs_work,
        auto: elasticBackend && draft.tmpfs_work && draft.tmpfs_auto,
        size_mb: elasticBackend && draft.tmpfs_work ? (toInteger(draft.tmpfs_work_size) ?? 0) : 0,
      },
      tmp: {
        enabled: elasticBackend && draft.tmpfs_tmp,
        auto: elasticBackend && draft.tmpfs_tmp && draft.tmpfs_auto,
        size_mb: elasticBackend && draft.tmpfs_tmp ? (toInteger(draft.tmpfs_tmp_size) ?? 0) : 0,
      },
      // The image store is the sidecar's, so only a Docker-in-Docker pool on the
      // Docker backend has one; anything else would be refused by the server.
      daemon: {
        enabled: hasSidecar && draft.tmpfs_daemon,
        auto: hasSidecar && draft.tmpfs_daemon && draft.tmpfs_auto,
        size_mb: hasSidecar && draft.tmpfs_daemon ? (toInteger(draft.tmpfs_daemon_size) ?? 0) : 0,
      },
    },
  };
  // The draft holds plain strings because that is what a <select> gives
  // back; the API's enums are narrower, and the server is the one that
  // rejects a value outside them.
  const platform: Platform = {};
  if (draft.platform_os) platform.os = draft.platform_os as Platform['os'];
  if (draft.platform_os_version) platform.os_version = draft.platform_os_version;
  if (draft.platform_arch) platform.arch = draft.platform_arch as Platform['arch'];
  // Always sent, so that clearing a platform on an edit actually clears it
  // rather than being read as "leave it alone".
  body.platform = platform;

  const complete = options.complete === true;
  if (complete || draft.runner_group.trim()) body.runner_group = draft.runner_group.trim();
  if (complete || draft.image.trim()) body.image = draft.image.trim();
  if (complete || draft.runner_version.trim()) body.runner_version = draft.runner_version.trim();
  // Always sent, like the platform and for the same reason: a pool moved
  // from a fixed size to automatic clears its figures, and an absent key
  // would be read as "leave them alone".
  body.resources = resources;
  // Every override is sent on every save, as a duration or as null. Null is
  // how the API is told to hand a setting back to the fleet, and a form that
  // left a cleared input out would make "stop overriding this" unsayable.
  body.runner_settings = {
    provision_timeout: nullableDuration(draft.provision_timeout),
    drain_timeout: nullableDuration(draft.drain_timeout),
    max_runner_lifetime: nullableDuration(draft.max_runner_lifetime),
    scale_up_delay: nullableDuration(draft.scale_up_delay),
    docker_wait: nullableDuration(draft.docker_wait),
  };
  if (complete || Object.keys(draft.host_selector).length > 0) {
    body.host_selector = draft.host_selector;
  }
  if (complete || Object.keys(draft.env).length > 0) body.env = draft.env;
  return body;
}

const NAME_SHAPE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/** The part of a draft the memory valve's rules are about. */
export type MemoryBurstFields = Pick<
  PoolDraft,
  | 'backend'
  | 'sizing'
  | 'memory_mb'
  | 'docker_mode'
  | 'memory_burst_mode'
  | 'memory_burst_max'
  | 'memory_burst_spill'
>;

/**
 * The memory valve's figures, held to the server's own rules and said beside the
 * control. Separate from the rest of a draft's errors because a pool the
 * controller keeps edits only this, in a dialog with no draft behind it.
 */
export function memoryBurstErrors(draft: MemoryBurstFields): Record<string, string> {
  const errors: Record<string, string> = {};
  if (
    (draft.backend === 'docker' || draft.backend === 'podman') &&
    draft.memory_burst_mode !== 'off'
  ) {
    if (draft.memory_burst_max.trim() !== '') {
      const ceiling = toInteger(draft.memory_burst_max);
      // A docker-in-docker pair is two containers, each given the typed limit.
      const started =
        draft.sizing === 'fixed'
          ? (toInteger(draft.memory_mb) ?? 0) * (draft.docker_mode === 'dind' ? 2 : 1)
          : 0;
      if (ceiling === undefined || ceiling < 512)
        errors['memory_burst.max_memory_mb'] =
          'Use at least 512 MB, or leave it empty for half as much again as a runner starts with.';
      else if (started > 0 && ceiling <= started)
        errors['memory_burst.max_memory_mb'] =
          `A ceiling at or below what a runner starts with (${memoryLabel(started)}) leaves nothing to lend. Raise it, or leave it empty for half as much again.`;
    }
    if (draft.memory_burst_spill.trim() !== '') {
      const spill = toInteger(draft.memory_burst_spill);
      if (spill === undefined || spill < 0)
        errors['memory_burst.spill_mb'] =
          'Use a whole number of megabytes, or leave it empty to allow no swap.';
      else if (spill > 1048576)
        errors['memory_burst.spill_mb'] = 'That is more than a terabyte of swap, which is a typo.';
    }
  }
  return errors;
}

/**
 * The rules the editor can check without asking the server. The server checks
 * the same things and more; these exist so the operator finds out while they
 * are still typing rather than at the end.
 */
export function draftErrors(
  draft: PoolDraft,
  socketConfirmed: boolean,
  offers: readonly BackendOffer[] = [],
  hostsKnown = false,
): Record<string, string> {
  const errors: Record<string, string> = {};
  const name = draft.name.trim();
  if (name === '') errors['name'] = 'Give the pool a name so it can be told apart in the fleet.';
  else if (name.length > 64) errors['name'] = 'Keep the name to 64 characters or fewer.';
  else if (!NAME_SHAPE.test(name))
    errors['name'] =
      'Use letters, digits, dots, dashes and underscores, starting with a letter or digit.';

  if (draft.installation_id === '')
    errors['installation_id'] = 'Choose the GitHub installation these runners register with.';

  if (draft.labels.length === 0)
    errors['labels'] = 'Add at least one label, or no workflow can ask for this pool.';

  const min = toInteger(draft.min_runners);
  const max = toInteger(draft.max_runners);
  const priority = toInteger(draft.priority);
  if (min === undefined || min < 0) errors['min_runners'] = 'Use a whole number, zero or more.';
  if (max === undefined || max < 1) errors['max_runners'] = 'Use a whole number, one or more.';
  else if (min !== undefined && max < min)
    errors['max_runners'] = `The maximum must be at least the minimum, which is ${min}.`;
  if (priority === undefined) errors['priority'] = 'Use a whole number.';

  if (parseGoDuration(draft.idle_timeout) === null)
    errors['idle_timeout'] = 'Use a Go duration such as 5m, 90s or 1h30m.';

  // Only a pool that has chosen a fixed size has a figure to be wrong about.
  // The floors are the server's own: below them the runner binary cannot
  // keep up with its own job, or is killed before it takes one.
  // An automatic pool sends neither, and the floors below are the server's
  // rules for a number somebody typed -- applying them to a slider nothing
  // is going to read would refuse a pool the server would happily create.
  if (draft.sizing === 'fixed') {
    const cpus = toNumber(draft.cpus);
    if (cpus === undefined || cpus <= 0)
      errors['resources.cpus'] = 'A fixed size needs a CPU limit. Move the slider to choose one.';
    else if (cpus < 0.25) errors['resources.cpus'] = 'A runner needs at least a quarter of a core.';
    const memory = toInteger(draft.memory_mb);
    if (memory === undefined || memory <= 0)
      errors['resources.memory_mb'] =
        'A fixed size needs a memory limit. Move the slider to choose one.';
    else if (memory < 512) errors['resources.memory_mb'] = 'A runner needs at least 512 MB.';
    // The same rule the server keeps, said where the sliders are rather
    // than at the foot of the page: a minimum sits under a stated standard.
    const minCpus = toNumber(draft.min_cpus);
    if (minCpus !== undefined && cpus !== undefined && minCpus > cpus)
      errors['resources.min_cpus'] = 'The minimum has to be at or below the standard CPU.';
    const minMemory = toInteger(draft.min_memory_mb);
    if (minMemory !== undefined && memory !== undefined && minMemory > memory)
      errors['resources.min_memory_mb'] = 'The minimum has to be at or below the standard memory.';
  }
  for (const [value, field, what] of [
    [draft.daemon_cpu_share, 'resources.daemon_cpu_share_percent', 'CPU'],
    [draft.daemon_memory_share, 'resources.daemon_memory_share_percent', 'memory'],
  ] as const) {
    const pct = toInteger(value);
    if (pct !== undefined && (pct < 10 || pct > 90))
      errors[field] =
        `Give the sidecar between 10 and 90 percent of the ${what}, or leave it empty for an even split.`;
  }
  // Under either kind of size a minimum is held to what any runner needs:
  // it is sent for an automatic pool too, so the floor applies there.
  const minCpusFloor = toNumber(draft.min_cpus);
  if (
    !errors['resources.min_cpus'] &&
    minCpusFloor !== undefined &&
    minCpusFloor > 0 &&
    minCpusFloor < 0.25
  )
    errors['resources.min_cpus'] = 'A runner needs at least a quarter of a core.';
  const minMemoryFloor = toInteger(draft.min_memory_mb);
  if (
    !errors['resources.min_memory_mb'] &&
    minMemoryFloor !== undefined &&
    minMemoryFloor > 0 &&
    minMemoryFloor < 512
  )
    errors['resources.min_memory_mb'] = 'A runner needs at least 512 MB.';
  if (
    draft.sizing !== 'fixed' &&
    (draft.backend === 'docker' || draft.backend === 'podman') &&
    draft.cpu_burst_max.trim() !== ''
  ) {
    const ceiling = toNumber(draft.cpu_burst_max);
    if (ceiling === undefined || ceiling < 0.25)
      errors['cpu_burst.max_cpus'] =
        'Use at least a quarter of a core, or leave it empty to use the host ceiling.';
  }
  Object.assign(errors, memoryBurstErrors(draft));
  if (draft.disk_gb.trim() !== '') {
    const disk = toInteger(draft.disk_gb);
    if (disk === undefined || disk <= 0)
      errors['resources.disk_gb'] =
        'Use a whole number of gigabytes, or leave it empty for no limit.';
  }

  if (
    draft.cache_enabled &&
    (toInteger(draft.cache_size_limit) ?? 0) > 0 &&
    !draft.cache_source.trim().startsWith('/')
  )
    errors['cache.size_limit'] =
      'A size limit is kept by evicting from a directory on the host, so the cache source has to be an absolute host path. There is nothing to measure inside a named volume.';

  // The server's own rules for the folders, said beside the control. A tmpfs
  // is charged to the runner's memory limit, so typed sizes that take the
  // whole of a typed limit leave a job nothing to run in.
  // The runner's folders add up against the runner's limit; the image store is
  // charged to the daemon's container and is held to its own.
  let typedTmpfs = 0;
  let typedDaemon = 0;
  for (const [on, raw, key] of [
    [draft.tmpfs_work, draft.tmpfs_work_size, 'tmpfs.work.size_mb'],
    [draft.tmpfs_tmp, draft.tmpfs_tmp_size, 'tmpfs.tmp.size_mb'],
    [draft.tmpfs_daemon, draft.tmpfs_daemon_size, 'tmpfs.daemon.size_mb'],
  ] as const) {
    if (!on || raw.trim() === '') continue;
    const mb = toInteger(raw);
    if (mb === undefined || mb < MIN_TMPFS_MB)
      errors[key] =
        `Use a whole number of megabytes, at least ${MIN_TMPFS_MB}, or leave it empty to size it from the memory limit.`;
    else if (key === 'tmpfs.daemon.size_mb') typedDaemon += mb;
    else typedTmpfs += mb;
  }
  if (draft.sizing === 'fixed') {
    const memory = toInteger(draft.memory_mb);
    if (memory !== undefined && memory > 0 && typedTmpfs >= memory && typedTmpfs > 0)
      errors['tmpfs.work.size_mb'] ||=
        `These folders may take ${typedTmpfs} MB and the memory limit is ${memory} MB. They are charged to that limit, so raise it to at least ${memory + typedTmpfs} MB or shrink them.`;
    if (memory !== undefined && memory > 0 && typedDaemon >= memory && typedDaemon > 0)
      errors['tmpfs.daemon.size_mb'] ||=
        `The image store may take ${typedDaemon} MB and the memory limit is ${memory} MB. The daemon's tmpfs is charged to that limit, so raise it to at least ${memory + typedDaemon} MB or shrink the store.`;
  }

  if (draft.docker_mode === 'host-socket' && !socketConfirmed)
    errors['docker_mode'] =
      'Confirm that you understand what mounting the host socket gives every job on this pool.';

  const unrunnable = backendUnavailable(
    draft.backend,
    offers,
    hostsKnown,
    Object.keys(draft.host_selector).length > 0,
  );
  if (unrunnable) errors['backend'] = unrunnable;

  return errors;
}
