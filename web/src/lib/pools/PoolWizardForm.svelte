<script module lang="ts">
  import type {
    BackendKind,
    DockerMode,
    Installation,
    Platform,
    Pool,
    PoolCreate,
    Resources,
    RunnerGroup,
  } from '$lib/api/types';
  import { parseGoDuration } from '$lib/format';
  import { MIN_TMPFS_MB } from './sizing';
  import {
    backendOffers,
    backendUnavailable,
    stepFields,
    stepIndex,
    wizardSteps,
    stepForField,
  } from './PoolVocabulary.svelte';
  import type { BackendOffer, WizardMode } from './PoolVocabulary.svelte';

  /**
   * What the wizard is editing.
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
     * a number. `fixed` is the two figures below, the same on every host.
     *
     * It is wizard state rather than a pool field because the pool has no such
     * field: emptiness is the answer. Holding the choice separately is what
     * keeps the sliders' last position while an operator looks at automatic
     * and changes their mind back.
     */
    sizing: 'automatic' | 'fixed';
    cpu_burst_mode: 'off' | 'observe' | 'automatic';
    cpu_burst_max: string;
    cpu_burst_size_builds: boolean;
    cpus: string;
    memory_mb: string;
    /** The least a runner may be given where no host has room for the size above; empty is none. */
    min_cpus: string;
    min_memory_mb: string;
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
    tmpfs_work: boolean;
    tmpfs_work_size: string;
    tmpfs_tmp: boolean;
    tmpfs_tmp_size: string;
    /** Carried through untouched: the wizard does not edit it, and must not lose it. */
    pids_limit: string;
    host_selector: Record<string, string>;
    /**
     * The wizard's own state, not the pool's: whether the operator chose to
     * keep this pool to some hosts. The selector cannot answer it, because
     * "only hosts that match" with no rule typed yet is an empty map exactly
     * like "any host" -- and reading the choice back off the selector is how
     * a half-made rule reset itself on the way to the next step and back.
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
      cpus: '',
      memory_mb: '',
      min_cpus: '',
      min_memory_mb: '',
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
      tmpfs_work: false,
      tmpfs_work_size: '',
      tmpfs_tmp: false,
      tmpfs_tmp_size: '',
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
      sizing: pool.sizing === 'fixed' ? 'fixed' : 'automatic',
      cpu_burst_mode: pool.cpu_burst?.mode ?? 'off',
      cpu_burst_max: fromNumber(pool.cpu_burst?.max_cpus),
      cpu_burst_size_builds: pool.cpu_burst?.size_for_ceiling ?? true,
      cpus: fromNumber(resources.cpus),
      memory_mb: fromNumber(resources.memory_mb),
      min_cpus: fromNumber(resources.min_cpus),
      min_memory_mb: fromNumber(resources.min_memory_mb),
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
      tmpfs_work: pool.tmpfs?.work?.enabled === true,
      tmpfs_work_size: fromNumber(pool.tmpfs?.work?.size_mb),
      tmpfs_tmp: pool.tmpfs?.tmp?.enabled === true,
      tmpfs_tmp_size: fromNumber(pool.tmpfs?.tmp?.size_mb),
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
      draft.sizing === 'fixed' ||
      draft.cpu_burst_mode === 'automatic' ||
      draft.restrict_hosts ||
      Object.keys(draft.host_selector).length > 0 ||
      draft.backend !== 'docker' ||
      draft.docker_mode === 'host-socket' ||
      draft.run_as_root ||
      draft.cache_enabled ||
      draft.tmpfs_work ||
      draft.tmpfs_tmp ||
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

  function toNumber(value: string): number | undefined {
    const trimmed = value.trim();
    if (trimmed === '') return undefined;
    const parsed = Number(trimmed);
    return Number.isFinite(parsed) ? parsed : undefined;
  }

  function toInteger(value: string): number | undefined {
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
    // Disk and the pids limit are independent of the choice: neither has a
    // share to be given, so a pool may cap its cache's disk and still leave
    // its size to the host.
    if (disk !== undefined) resources.disk_gb = disk;
    if (pids !== undefined) resources.pids_limit = pids;

    const body: PoolCreate = {
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
          size_mb: elasticBackend && draft.tmpfs_work ? (toInteger(draft.tmpfs_work_size) ?? 0) : 0,
        },
        tmp: {
          enabled: elasticBackend && draft.tmpfs_tmp,
          size_mb: elasticBackend && draft.tmpfs_tmp ? (toInteger(draft.tmpfs_tmp_size) ?? 0) : 0,
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

  /**
   * The rules the wizard can check without asking the server. The server checks
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
      else if (cpus < 0.25)
        errors['resources.cpus'] = 'A runner needs at least a quarter of a core.';
      const memory = toInteger(draft.memory_mb);
      if (memory === undefined || memory <= 0)
        errors['resources.memory_mb'] =
          'A fixed size needs a memory limit. Move the slider to choose one.';
      else if (memory < 512) errors['resources.memory_mb'] = 'A runner needs at least 512 MB.';
      // The same rule the server keeps, said where the sliders are rather
      // than on the review step: a minimum sits under a stated standard.
      const minCpus = toNumber(draft.min_cpus);
      if (minCpus !== undefined && cpus !== undefined && minCpus > cpus)
        errors['resources.min_cpus'] = 'The minimum has to be at or below the standard CPU.';
      const minMemory = toInteger(draft.min_memory_mb);
      if (minMemory !== undefined && memory !== undefined && minMemory > memory)
        errors['resources.min_memory_mb'] =
          'The minimum has to be at or below the standard memory.';
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
      draft.sizing === 'automatic' &&
      (draft.backend === 'docker' || draft.backend === 'podman') &&
      draft.cpu_burst_max.trim() !== ''
    ) {
      const ceiling = toNumber(draft.cpu_burst_max);
      if (ceiling === undefined || ceiling < 0.25)
        errors['cpu_burst.max_cpus'] =
          'Use at least a quarter of a core, or leave it empty to use the host ceiling.';
    }
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
    let typedTmpfs = 0;
    for (const [on, raw, key] of [
      [draft.tmpfs_work, draft.tmpfs_work_size, 'tmpfs.work.size_mb'],
      [draft.tmpfs_tmp, draft.tmpfs_tmp_size, 'tmpfs.tmp.size_mb'],
    ] as const) {
      if (!on || raw.trim() === '') continue;
      const mb = toInteger(raw);
      if (mb === undefined || mb < MIN_TMPFS_MB)
        errors[key] =
          `Use a whole number of megabytes, at least ${MIN_TMPFS_MB}, or leave it empty to size it from the memory limit.`;
      else typedTmpfs += mb;
    }
    if (draft.sizing === 'fixed' && typedTmpfs > 0) {
      const memory = toInteger(draft.memory_mb);
      if (memory !== undefined && memory > 0 && typedTmpfs >= memory)
        errors['tmpfs.work.size_mb'] ||=
          `These folders may take ${typedTmpfs} MB and the memory limit is ${memory} MB. They are charged to that limit, so raise it to at least ${memory + typedTmpfs} MB or shrink them.`;
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
</script>

<!--
  Pool creation and pool editing, in the same steps.

  The draft is one object held here, so going back never loses what was typed;
  the steps are presentation only. Client-side rules run continuously and gate
  the Next button; the server's own verdict is asked for on the review step,
  before anything is created, because "the pool exists but no host can run it"
  is a much worse place to find out.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import {
    ApiError,
    createPool,
    getPoolDefaults,
    listInstallations,
    listPoolPlatforms,
    listRunnerGroups,
    updatePool,
    validatePool,
  } from '$lib/api/client';
  import type { Body, PoolPlatform, Result } from '$lib/api/types';
  import { BRAND_LABEL, brandedLabel, brandedName } from '$lib/brand';
  import { nicknamedPoolName, poolName, spinWord } from './names';
  import { fleet } from '$lib/state/fleet.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Wizard from '$lib/components/Wizard.svelte';
  import StepTarget from './StepTarget.svelte';
  import StepLabels from './StepLabels.svelte';
  import StepHosts from './StepHosts.svelte';
  import { hostMatchesSelector } from './hostSelector';
  import StepBackend from './StepBackend.svelte';
  import StepDocker from './StepDocker.svelte';
  import StepSize from './StepSize.svelte';
  import StepScaling from './StepScaling.svelte';
  import StepMode from './StepMode.svelte';
  import StepRunners from './StepRunners.svelte';
  import StepReview from './StepReview.svelte';
  import PoolStartupStability from './PoolStartupStability.svelte';

  interface Props {
    /** The pool being edited. Leave it out to create a new one. */
    pool?: Pool;
    oncancel: () => void;
    ondone: (pool: Pool) => void;
    class?: string;
  }

  let { pool, oncancel, ondone, class: className = '' }: Props = $props();

  const editing = $derived(pool !== undefined);

  // Captured once on purpose: the routes remount this form with a {#key} when
  // they start editing a different pool, so a live reference would be wrong.
  let draft = $state<PoolDraft>(untrack(() => (pool ? draftFromPool(pool) : emptyDraft())));
  /*
    Which of the two paths the wizard is walking.

    A new pool starts on the simple one, because that is the pool most fleets
    want and the one every default already describes. Editing opens on the
    advanced path whenever the pool has anything the simple path cannot show --
    a fixed size, a host selector, a runner override, a platform or an image --
    so that opening a tuned pool never hides the settings it was tuned with,
    and never quietly saves them away.
  */
  let mode = $state<WizardMode>(
    untrack(() => (pool && poolIsTuned(draft) ? 'advanced' : 'simple')),
  );
  const steps = $derived(wizardSteps(mode, editing));
  const fieldsByStep = $derived(stepFields(mode, editing));
  let current = $state(0);
  let touched = $state<Record<string, boolean>>({});
  let serverErrors = $state<Record<string, string>>({});
  let socketConfirmed = $state(untrack(() => pool?.docker_mode === 'host-socket'));

  /*
    Auto-naming, on creation only.

    `autoName` is the last name the wizard produced. While the field still
    holds it the name is the wizard's to keep current -- so choosing Podman on
    the backend step renames the pool -- and the moment an operator types over
    it the wizard stops touching it, because a field that rewrites itself under
    someone's cursor is worse than no help at all. Editing an existing pool
    never generates anything: its name is already in workflows.

    `autoLabel` plays the same part for the labels, with one more state:
    `null`, meaning the operator has taken the labels over. Removing the
    suggested chip has to stick, and without that third state an empty list
    looks exactly like a list nothing has been put in yet.
  */
  let kennelWord = $state(spinWord());
  let autoName = $state('');
  let autoLabel = $state<string | null>('');
  let submitting = $state(false);
  let panel = $state<HTMLDivElement | null>(null);

  let installations = $state<Installation[]>([]);
  let installationsLoading = $state(true);
  let installationsError = $state<unknown>(null);
  /** Bumped by the error state's retry, which re-runs the fetch below. */
  let installationsAttempt = $state(0);

  let groups = $state<RunnerGroup[]>([]);
  let groupsLoading = $state(false);
  let groupsError = $state<unknown>(null);

  /**
   * The fleet's own default size, and the maximum the wizard worked out from
   * the room the hosts have for it.
   *
   * `autoMax` is the same idea as `autoName` above: while the field still
   * holds what the wizard put there, the wizard keeps it current, so choosing
   * bigger runners or fewer hosts lowers the cap in front of the operator. The
   * moment they type their own it stops following, because a number that
   * rewrites itself under somebody's cursor is worse than no help at all.
   * Editing an existing pool never follows: that cap is in force right now,
   * and the Scaling step offers the fleet's figure rather than taking it.
   */
  let defaults = $state<Resources | null>(null);
  let fleetDefaults = $state<Result<'getPoolDefaults'>['runner_settings'] | null>(null);
  let autoMax = $state<string | null>('4');

  // The operating systems a runner image is published for. Served rather than
  // hard-coded so the picker cannot offer one that does not exist.
  let platforms = $state<PoolPlatform[]>([]);

  let verdict = $state<Result<'validatePool'> | null>(null);
  let validating = $state(false);
  let stabilityRevision = $state(0);
  let validateError = $state<unknown>(null);

  const reviewStep = $derived(steps.length - 1);
  // -1 on the simple path, which walks no hosts step. Everything that reads it
  // compares with `>=`, so "there is no such step" reads as "we are past it" --
  // which is what the simple path means: it restricts nothing, so the
  // placement question is answered and behind us from the start.
  const hostsStep = $derived(steps.findIndex((step) => step.id === 'hosts'));
  // The backend step counts over the hosts this pool is allowed to land on, not
  // the whole fleet: "offered by 3 hosts" is a lie if two of them are the amd64
  // boxes an arm64 pool will never touch. Placement is chosen first for exactly
  // this reason.
  const selectedHosts = $derived(
    fleet.hosts.filter((host) => hostMatchesSelector(host, draft.host_selector)),
  );
  const restrictedToHosts = $derived(Object.keys(draft.host_selector).length > 0);
  const offers = $derived(backendOffers(selectedHosts));
  const clientErrors = $derived(draftErrors(draft, socketConfirmed, offers, fleet.loaded));
  const body = $derived(toPoolBody(draft));

  /** Client rules show once a field has been left; server rules show at once. */
  const errors = $derived.by(() => {
    const out: Record<string, string> = { ...serverErrors };
    for (const [field, message] of Object.entries(clientErrors)) {
      if (touched[field]) out[field] = message;
    }
    return out;
  });

  const blocking = $derived.by(() => {
    const fields =
      current === reviewStep ? Object.keys(clientErrors) : (fieldsByStep[current] ?? []);
    return fields
      .map((field) => clientErrors[field])
      .filter((message): message is string => Boolean(message));
  });
  const canAdvance = $derived(blocking.length === 0 && !submitting);

  const installationLabel = $derived(
    installations.find((entry) => entry.id === draft.installation_id)?.target ?? '',
  );

  function touch(field: string): void {
    touched = { ...touched, [field]: true };
    if (serverErrors[field] !== undefined) {
      const rest = { ...serverErrors };
      delete rest[field];
      serverErrors = rest;
    }
  }

  function touchStep(step: number): void {
    const fields = fieldsByStep[step] ?? [];
    if (fields.length === 0) return;
    const next = { ...touched };
    for (const field of fields) next[field] = true;
    touched = next;
  }

  function goTo(step: number): void {
    current = Math.min(Math.max(step, 0), reviewStep);
  }

  /*
    The simple path's way to the settings it leaves to the fleet.

    An edit skips the fork, and a pool with nothing the simple path cannot
    show opens on that path: target, labels, docker, review. That is the right
    opening for a pool somebody came to relabel, and a dead end for the one
    they came to make elastic -- the plain automatic pool is exactly the pool
    elastic CPU is for, and the fork it never walks was the only way to the
    size step that offers it. So a simple edit offers the advanced path from
    every step, and taking it lands on the first step the simple path skipped.
    The draft is one object and the steps only ways of looking at it, so
    nothing typed so far is lost on the way.
  */
  function showEverySetting(): void {
    mode = 'advanced';
    goTo(stepIndex('hosts', 'advanced', editing));
  }

  /* -- what the fleet and GitHub can offer --------------------------------- */

  /*
    Where a fixed size opens. It is a fleet setting, so the sliders cannot have
    a figure of their own: a wizard showing two cores while the fleet says
    eight would be describing a pool it is not about to create.

    It is the fleet's *suggestion* rather than what a pool becomes -- a pool
    that names no size is sized by its host -- so the figures are put on the
    sliders and nowhere else. A pool being edited already has whatever size it
    was given and is left alone.
  */
  $effect(() => {
    const controller = new AbortController();
    getPoolDefaults(controller.signal)
      .then((response) => {
        const resources = response.suggested_resources ?? response.resources ?? {};
        defaults = resources;
        // The fleet's own timings, so the overrides step can say what each
        // setting is being overridden *from*. An input whose placeholder reads
        // "20m0s, the fleet's" is one an operator can leave alone with
        // confidence; an empty box beside the word "timeout" is one they feel
        // obliged to fill.
        fleetDefaults = response.runner_settings ?? null;
        untrack(() => {
          if (editing) return;
          if (draft.cpus === '' && resources.cpus !== undefined)
            draft.cpus = String(resources.cpus);
          if (draft.memory_mb === '' && resources.memory_mb !== undefined) {
            draft.memory_mb = String(resources.memory_mb);
          }
          // The minimum is deliberately left empty rather than opened on the
          // fleet's: an empty minimum already follows runners.minimum_*, and
          // copying today's figure in would freeze it into the pool, so a
          // later change to the fleet setting would pass this pool by.
        });
      })
      // A failure here is not worth an error state: the sliders fall back to
      // the built-in figures, and nothing on the automatic path reads them.
      .catch(() => {});
    return () => controller.abort();
  });

  /*
    The maximum follows what the fleet can actually place, until it is typed
    over. This is the half of "how many runners" that nothing else can answer:
    the cap is a number about machines, and the fleet is the only thing that
    knows how many runners of this size its hosts can hold.
  */
  const followingMax = $derived(!editing && autoMax !== null && draft.max_runners === autoMax);
  $effect(() => {
    const room = verdict?.room?.runners;
    if (editing || room === undefined || room <= 0) return;
    untrack(() => {
      if (autoMax === null) return;
      if (draft.max_runners !== autoMax) {
        // Typed over: the wizard is done with this field.
        autoMax = null;
        return;
      }
      const next = String(Math.max(room, toInteger(draft.min_runners) ?? 0, 1));
      if (next === draft.max_runners) return;
      draft.max_runners = next;
      autoMax = next;
    });
  });

  $effect(() => {
    void installationsAttempt;
    const controller = new AbortController();
    // A failure here is not worth an error state: the picker falls back to
    // "Any", which is what a pool got before platforms existed.
    listPoolPlatforms(controller.signal)
      .then((response) => {
        platforms = response.items ?? [];
      })
      .catch(() => {});
    return () => controller.abort();
  });

  $effect(() => {
    const controller = new AbortController();
    installationsLoading = true;
    installationsError = null;
    listInstallations(controller.signal)
      .then((response) => {
        const items = response.items ?? [];
        installations = items;
        // One installation is the common case; choosing it for the operator is
        // the difference between a wizard and a form.
        const only = items[0];
        if (draft.installation_id === '' && items.length === 1 && only?.id) {
          draft.installation_id = only.id;
        }
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        installationsError = cause;
      })
      .finally(() => {
        installationsLoading = false;
      });
    return () => controller.abort();
  });

  $effect(() => {
    const id = draft.installation_id;
    groups = [];
    groupsError = null;
    if (id === '') {
      groupsLoading = false;
      return;
    }
    const controller = new AbortController();
    groupsLoading = true;
    listRunnerGroups(id, controller.signal)
      .then((response) => {
        groups = response.items ?? [];
        if (!editing && !touched.runner_group && draft.runner_group === '') {
          const managed = groups.find((group) => group.name?.toLowerCase() === 'zoomies');
          if (managed?.name) draft.runner_group = managed.name;
        }
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        groupsError = cause;
      })
      .finally(() => {
        groupsLoading = false;
      });
    return () => controller.abort();
  });

  /* -- the name, and the label it implies ---------------------------------- */

  /** The names already in use, so a new pool is not offered one of them. */
  const poolNames = $derived(fleet.pools.map((p) => p.name ?? ''));

  /**
   * Whether the operator has asked for a spaniel in the name.
   *
   * Without this the dice would be undone by the next thing they typed: the
   * suggested name is recomputed as the draft changes, and a shape that needs
   * no word would quietly drop the one they just rolled.
   */
  let nicknamed = $state(false);

  // The name follows the shape as the operator fills it in: a name generated
  // before any host had connected must not go on claiming the pool is 2 vCPU
  // Ubuntu after they have said 8 vCPU Debian, and the pools already in the
  // fleet decide whether this one needs a spaniel to be told from them.
  $effect(() => {
    if (editing) return;
    const suggested = nicknamed
      ? nicknamedPoolName(kennelWord, draft, fleet.hosts, defaults)
      : poolName(kennelWord, draft, fleet.hosts, poolNames, defaults);
    untrack(() => {
      if (draft.name !== '' && draft.name !== autoName) return;
      draft.name = suggested;
      autoName = suggested;
    });
  });

  $effect(() => {
    if (editing) return;
    const suggested = brandedLabel(draft.name);
    untrack(() => {
      if (autoLabel === null) return;
      const pristine =
        autoLabel === '' ? draft.labels.length === 0 : draft.labels.join() === autoLabel;
      if (!pristine) {
        autoLabel = null;
        return;
      }
      // A name that reduces to the brand alone says nothing the server does not
      // already add on save, so there is nothing worth filling in yet.
      if (suggested === BRAND_LABEL) return;
      if (draft.labels.join() === suggested) return;
      draft.labels = [suggested];
      autoLabel = suggested;
    });
  });

  /**
   * Roll a name from the kennel, whatever is in the field now.
   *
   * The suggested name carries a spaniel only when the shape cannot tell this
   * pool from another, so the dice ask for one outright: pressing it on a pool
   * whose shape is already unique has to change something, or it reads as a
   * broken button.
   */
  function spin(): void {
    if (editing) return;
    nicknamed = true;
    kennelWord = spinWord(kennelWord);
    const next = nicknamedPoolName(kennelWord, draft, fleet.hosts, defaults);
    draft.name = next;
    autoName = next;
    touch('name');
  }

  /* -- the server's verdict, before anything is created --------------------- */

  $effect(() => {
    // From the placement step on, not only at the end. Every step after it
    // edits something the controller's count depends on -- which hosts, which
    // backend, how big a runner is -- so the answer belongs beside the setting
    // that changes it, while there is still a reason to change it.
    if (current < hostsStep) return;
    void stabilityRevision;
    const payload = body;
    const controller = new AbortController();
    validating = true;
    const timer = setTimeout(() => {
      validatePool(payload, pool?.id, controller.signal)
        .then((result) => {
          verdict = result;
          validateError = null;
        })
        .catch((cause: unknown) => {
          if (cause instanceof DOMException && cause.name === 'AbortError') return;
          validateError = cause;
        })
        .finally(() => {
          validating = false;
        });
    }, 250);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  });

  /* -- focus follows the step ----------------------------------------------- */

  let lastStep = -1;
  $effect(() => {
    const step = current;
    if (step === lastStep) return;
    const moved = lastStep !== -1;
    lastStep = step;
    // Not on the first render: the shell has just put focus on the page
    // heading, and taking it away again would undo that.
    if (moved) untrack(() => panel)?.focus();
  });

  /* -- submitting ------------------------------------------------------------ */

  function applyFieldErrors(cause: ApiError): void {
    const fields = cause.fieldErrors();
    serverErrors = fields;
    const first = Object.keys(fields)[0];
    if (first !== undefined) goTo(stepForField(first, mode, editing));
  }

  /**
   * The controller's refusal when an edit would leave this pool with no host
   * in the fleet that could run it. It is not a field error -- every figure on
   * the form is valid, and it is the machines that cannot keep up -- so it is
   * answered with the consequence stated rather than a red label on a slider.
   */
  let stranding = $state('');

  async function finish(confirm = false): Promise<void> {
    if (submitting) return;
    touchStep(current);
    const outstanding = Object.keys(clientErrors);
    if (outstanding.length > 0) {
      // Show every one of them inline, then land on the first offending step.
      const next = { ...touched };
      for (const field of outstanding) next[field] = true;
      touched = next;
      goTo(stepForField(outstanding[0] ?? 'name', mode, editing));
      return;
    }
    submitting = true;
    try {
      const payload = toPoolBody(draft, { complete: editing });
      const saved =
        pool && pool.id
          ? await updatePool(
              pool.id,
              payload as Body<'updatePool'>,
              confirm ? { confirm: true } : undefined,
            )
          : await createPool(payload);
      toasts.success(
        editing ? `Saved ${payload.name}` : `Created ${payload.name}`,
        editing
          ? 'The scheduler picks the new settings up on its next pass.'
          : 'Runners appear as soon as a job asks for these labels.',
      );
      void fleet.reconcile();
      ondone(saved);
    } catch (cause) {
      if (cause instanceof ApiError && cause.isConflict) {
        stranding = cause.message;
        return;
      }
      if (cause instanceof ApiError) applyFieldErrors(cause);
      toasts.fromError(cause, editing ? 'The pool was not saved' : 'The pool was not created');
    } finally {
      submitting = false;
    }
  }
</script>

<Wizard
  class={className}
  {steps}
  bind:current
  {canAdvance}
  busy={submitting}
  finishLabel={editing ? 'Save changes' : 'Create pool'}
  cancelLabel="Cancel"
  onnext={() => touchStep(current)}
  onback={() => touchStep(current)}
  onfinish={() => void finish()}
  {oncancel}
>
  {#snippet children(step)}
    <div class="step" bind:this={panel} tabindex="-1" role="group" aria-label={step.title}>
      {#if step.id === 'mode'}
        <StepMode bind:mode hosts={fleet.hosts} hostsKnown={fleet.loaded} />
      {:else if step.id === 'target'}
        <StepTarget
          {draft}
          {errors}
          {touch}
          {installations}
          loading={installationsLoading}
          error={installationsError}
          onretry={() => (installationsAttempt += 1)}
          {groups}
          {groupsLoading}
          {groupsError}
          onspin={editing ? undefined : spin}
        />
      {:else if step.id === 'labels'}
        <StepLabels {draft} {errors} {touch} />
      {:else if step.id === 'docker'}
        <StepDocker {draft} {touch} />
      {:else if step.id === 'hosts'}
        <StepHosts
          {draft}
          {touch}
          hosts={fleet.hosts}
          hostsKnown={fleet.loaded}
          {verdict}
          {validating}
        />
      {:else if step.id === 'backend'}
        <StepBackend
          {draft}
          {errors}
          {touch}
          {offers}
          {platforms}
          hosts={fleet.hosts}
          hostsKnown={fleet.loaded}
          restricted={restrictedToHosts}
          bind:socketConfirmed
        />
      {:else if step.id === 'size'}
        <StepSize {draft} {errors} {touch} {defaults} {verdict} {validating} />
      {:else if step.id === 'scaling'}
        <StepScaling {draft} {errors} {touch} {verdict} {validating} following={followingMax} />
      {:else if step.id === 'runners'}
        <StepRunners {draft} {errors} {touch} {fleetDefaults} />
      {:else}
        <StepReview
          {draft}
          {body}
          {editing}
          {installationLabel}
          {mode}
          {verdict}
          {validating}
          error={validateError}
          ongoto={goTo}
        />
      {/if}

      {#if draft.sizing === 'automatic' && (draft.backend === 'docker' || draft.backend === 'podman')}
        <PoolStartupStability
          warnings={verdict?.warnings ?? []}
          onfixed={() => stabilityRevision++}
        />
      {/if}

      {#if editing && mode === 'simple'}
        <div class="more">
          <p>
            Hosts, backend, size, scaling and the runner timings follow the fleet, and elastic CPU
            with them. Nothing typed here is lost on the way to them.
          </p>
          <Button size="sm" onclick={showEverySetting}>Show every setting</Button>
        </div>
      {/if}

      {#if blocking.length > 0}
        <div class="blocking">
          <p class="blocking-title">
            {steps[current + 1] ? 'Before the next step' : 'Before this pool can be saved'}
          </p>
          <ul>
            {#each blocking as message, index (index)}
              <li>{message}</li>
            {/each}
          </ul>
        </div>
      {/if}
    </div>
  {/snippet}
</Wizard>

<ConfirmDialog
  bind:open={
    () => stranding !== '',
    (open) => {
      if (!open) stranding = '';
    }
  }
  title="Save a pool with nowhere to run?"
  name={draft.name}
  description={stranding}
  consequences={[
    'Nothing has been saved yet.',
    'Saved as it is, jobs with these labels queue until a host that fits joins the fleet.',
    'Adjusting a host to match, or asking for less here, is the other way out.',
  ]}
  confirmLabel="Save anyway"
  busy={submitting}
  onconfirm={async () => {
    await finish(true);
    return true;
  }}
/>

<style>
  .step {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  /*
    The step is focused programmatically when the wizard advances, so that a
    screen reader lands on the new content. That is not a keyboard tab, so
    :focus-visible is the right test: it draws no ring for the move the wizard
    made, and still draws one if somebody tabs here themselves.
  */
  .step:focus:not(:focus-visible) {
    outline: none;
  }
  .blocking {
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .blocking-title {
    margin: 0;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .blocking ul {
    margin: var(--z-space-1) 0 0;
    padding-left: var(--z-space-5);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .more {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .more p {
    flex: 1 1 auto;
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
</style>
