<!--
  Step five: how much machine one runner gets, and how much disk its cache may
  keep.

  It is a step of its own, after the hosts and before the count, because that
  is the order the decision is actually made in: these are the machines, this
  is what one runner of mine costs on them, and therefore this is how many
  there can be. Asked the other way round -- a maximum typed first, a size
  typed last -- the number that mattered was chosen before anything on screen
  could say what it would buy.

  There are three answers, and the first one is the usual one. A pool that
  names no size is given one slot's share of whichever host each runner lands
  on -- charged against that host and applied as a real cgroup limit, so the
  books and the cgroups agree -- which is correct on every machine in an
  unequal fleet without anybody typing a number. A pool can instead take the
  size each host names for itself, for a fleet where the operator, not the slot
  count, knows how big a runner should be on each machine. A fixed size is for
  the pool whose jobs need a particular amount of machine wherever they run.

  What is not an answer is "no limit at all". A runner with no limit takes
  every core on the machine it lands on while the fleet charges it one slot's
  share, so the host reads as half committed, its daemon stops answering, and
  the creates queued behind it time out on a machine every page calls busy.
  Neither choice here does that.

  The fixed figures are sliders rather than boxes. A box invites 3000 MB as
  readily as 4096 and says nothing about whether any host can back it; a notch
  is a value somebody has a reason to choose, and the count underneath says
  what choosing it costs.
-->
<script lang="ts">
  import { CircleCheck, Sparkles, TriangleAlert } from '@lucide/svelte';
  import type { PoolRoom as PoolRoomShape, Resources, Result } from '$lib/api/types';
  import { formatMegabytes, pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { prefs } from '$lib/state/prefs.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import PoolFit from './PoolFit.svelte';
  import PoolRoom from './PoolRoom.svelte';
  import {
    CACHE_NOTCHES,
    CPU_NOTCHES,
    DISK_NOTCHES,
    MEMORY_NOTCHES,
    cacheBytes,
    cacheGb,
    chargedSize,
    cpuLabel,
    gbLabel,
    memoryLabel,
    nearest,
    daemonReserveMb,
    recommendedMemoryMb,
    tmpfsIsTight,
    tmpfsReserveMb,
    withValue,
  } from './sizing';
  import type { PoolDraft } from './PoolWizardForm.svelte';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    /** The fleet's own default, which is what "recommended" means here. */
    defaults: Resources | null;
    /** What the controller makes of the size, asked as the sliders move. */
    verdict: Result<'validatePool'> | null;
    validating: boolean;
  }

  let { draft, errors, touch, defaults, verdict, validating }: Props = $props();

  /* -- the folders kept in memory ------------------------------------------- */

  /** A typed size, or undefined for one left to the memory limit. */
  function typedMb(raw: string): number | undefined {
    const n = Number(raw.trim());
    return raw.trim() !== '' && Number.isInteger(n) && n > 0 ? n : undefined;
  }

  const containerBackend = $derived(draft.backend === 'docker' || draft.backend === 'podman');
  /** The limit typed here; zero where the host picks, which there is nothing to raise. */
  const typedLimitMb = $derived(draft.sizing === 'fixed' ? (typedMb(draft.memory_mb) ?? 0) : 0);
  /**
   * The limit that leaves the job the room it has now with the folders on top.
   * A tmpfs is charged to the runner's memory limit, so switching one on
   * without raising the limit leaves the job less than it had.
   */
  const workFolder = $derived({
    enabled: draft.tmpfs_work,
    sizeMb: typedMb(draft.tmpfs_work_size),
  });
  const tmpFolder = $derived({ enabled: draft.tmpfs_tmp, sizeMb: typedMb(draft.tmpfs_tmp_size) });
  /**
   * Only a Docker-in-Docker pool on the Docker backend has a sidecar, and the
   * sidecar is a second container with a memory limit of its own: the image
   * store is charged to it, and the work folder and /tmp are not.
   */
  const hasSidecar = $derived(draft.backend === 'docker' && draft.docker_mode === 'dind');
  const daemonFolder = $derived({
    enabled: hasSidecar && draft.tmpfs_daemon,
    sizeMb: typedMb(draft.tmpfs_daemon_size),
  });
  // Only when a container's folders take enough of the limit to matter, each judged
  // against the limit it is charged to, so accepting the proposal ends it rather
  // than moving it.
  const proposedLimitMb = $derived(
    tmpfsIsTight(typedLimitMb, tmpfsReserveMb(workFolder, tmpFolder)) ||
      tmpfsIsTight(typedLimitMb, daemonReserveMb(daemonFolder))
      ? recommendedMemoryMb(typedLimitMb, workFolder, tmpFolder, daemonFolder)
      : 0,
  );
  const tmpfsOn = $derived(draft.tmpfs_work || draft.tmpfs_tmp || daemonFolder.enabled);

  /* -- the size ------------------------------------------------------------- */

  const cpus = $derived(Number(draft.cpus) || 0);
  const memoryMb = $derived(Number(draft.memory_mb) || 0);
  const diskGb = $derived(Number(draft.disk_gb) || 0);

  const defaultCpus = $derived(defaults?.cpus ?? 2);
  const defaultMemoryMb = $derived(defaults?.memory_mb ?? 4096);

  const cpuNotches = $derived(withValue(CPU_NOTCHES, cpus));
  /*
   * A minimum sits under the standard, so under a fixed size its notches stop
   * there. Under an automatic size the standard is each host's own share, so
   * every notch is offered and a host whose share is smaller simply does not
   * use the minimum.
   */
  const automaticSize = $derived(draft.sizing === 'automatic');
  const profileSize = $derived(draft.sizing === 'profile');
  /* The host decides, by its slot share or by its own standard: neither has a
     figure on the pool for a minimum to sit under. */
  const hostSized = $derived(draft.sizing !== 'fixed');
  const minCpus = $derived(Number(draft.min_cpus) || 0);
  const minMemoryMb = $derived(Number(draft.min_memory_mb) || 0);
  const minCpuNotches = $derived(
    withValue([0, ...CPU_NOTCHES.filter((n) => hostSized || n <= cpus)], minCpus),
  );
  const minMemoryNotches = $derived(
    withValue([0, ...MEMORY_NOTCHES.filter((n) => hostSized || n <= memoryMb)], minMemoryMb),
  );
  /* The boost ceiling's notches start at nothing, which leaves it to the host. */
  const burstNotches = $derived(withValue([0, ...CPU_NOTCHES], Number(draft.cpu_burst_max) || 0));
  const memoryNotches = $derived(withValue(MEMORY_NOTCHES, memoryMb));
  const diskNotches = $derived(withValue(DISK_NOTCHES, diskGb));

  const cpuMarks = $derived([
    { value: defaultCpus, label: "the fleet's default", recommended: true },
    { value: cpuNotches[0] ?? 0.25, label: cpuLabel(cpuNotches[0] ?? 0.25) },
    {
      value: cpuNotches[cpuNotches.length - 1] ?? 64,
      label: cpuLabel(cpuNotches[cpuNotches.length - 1] ?? 64),
    },
  ]);
  const memoryMarks = $derived([
    { value: defaultMemoryMb, label: "the fleet's default", recommended: true },
    { value: memoryNotches[0] ?? 512, label: memoryLabel(memoryNotches[0] ?? 512) },
    {
      value: memoryNotches[memoryNotches.length - 1] ?? 131072,
      label: memoryLabel(memoryNotches[memoryNotches.length - 1] ?? 131072),
    },
  ]);
  const diskMarks = $derived([
    { value: 0, label: 'no limit' },
    { value: 40, label: '40 GB' },
    {
      value: diskNotches[diskNotches.length - 1] ?? 640,
      label: gbLabel(diskNotches[diskNotches.length - 1] ?? 640),
    },
  ]);

  const charged = $derived(chargedSize(cpus, memoryMb, draft.docker_mode));
  const atDefaults = $derived(cpus === defaultCpus && memoryMb === defaultMemoryMb);

  /* The room is the controller's count, which knows what the fleet is actually
     charged. It is what both this step and the next one are read against. */
  const room = $derived<PoolRoomShape | null>(verdict?.room ?? null);
  // What Auto comes to on this pool's hosts, worked out by the controller from the
  // sizes on this form: per host, what each folder is given, and the smallest
  // change that would put more in memory. Present only while a folder is Auto.
  const plan = $derived(room?.tmpfs_plan ?? null);
  const planOnDisk = $derived(plan ? (plan.in_memory ?? 0) < (plan.total ?? 0) : false);
  const roomTotal = $derived(room?.runners ?? 0);
  /* The hosts on which an elastic pool would not be elastic: their agent is
     too old to move a live quota, so a runner there is held at its share
     whatever the select above says. The one thing on a host elastic CPU
     needs, and the one the controller cannot do itself, so it is named here
     beside the choice rather than found in a metric afterwards. */
  const cannotLend = $derived((room?.hosts ?? []).filter((host) => !host.elastic_cpu));

  function setCpus(value: number): void {
    draft.cpus = String(value);
    touch('resources.cpus');
  }
  function setMemory(value: number): void {
    draft.memory_mb = String(value);
    touch('resources.memory_mb');
  }
  function setDisk(value: number): void {
    draft.disk_gb = value > 0 ? String(value) : '';
    touch('resources.disk_gb');
  }
  function useDefaults(): void {
    setCpus(defaultCpus);
    setMemory(defaultMemoryMb);
  }

  /*
    What the automatic answer amounts to on this fleet, said in one line beside
    the choice. The range comes from the controller's own per-host figures, so
    the sentence beside the radio and the table under it can never disagree.
  */
  const automaticDescription = $derived.by(() => {
    const plain =
      'Each runner is given one slot\u2019s share of the machine it lands on. Correct on every host in an unequal fleet, and it follows a host that is resized.';
    const hosts = room?.hosts ?? [];
    if (hosts.length === 0) return plain;
    const charges = hosts.map((h) => h.charge_cpus ?? 0);
    const low = Math.min(...charges);
    const high = Math.max(...charges);
    const range = low === high ? cpuLabel(low) : `${cpuLabel(low)} to ${cpuLabel(high)}`;
    return `${plain} Today that is ${range} per runner across ${pluralise(hosts.length, 'host')}.`;
  });

  /*
    The third answer, said in one line beside the choice. How many hosts have
    given themselves a standard size is read from the fleet's own hosts rather
    than from the room, because the room is counted for whichever answer is
    chosen now and says nothing about this one until it is.
  */
  const sizedHosts = $derived(
    fleet.hosts.filter(
      (h) =>
        (h.runner_profile?.standard?.cpus ?? 0) > 0 ||
        (h.runner_profile?.standard?.memory_mb ?? 0) > 0,
    ).length,
  );
  const fleetStandard = $derived(`${cpuLabel(defaultCpus)} and ${memoryLabel(defaultMemoryMb)}`);
  const profileDescription = $derived(
    `Each runner is given the standard size set on the host it lands on, so a large host and a small one can give the same pool different sizes. A host that sets none gives the fleet\u2019s default, ${fleetStandard}.`,
  );

  /* -- the cache ------------------------------------------------------------ */

  const cacheLimitGb = $derived(cacheGb(Number(draft.cache_size_limit) || 0));
  const cacheNotches = $derived(withValue(CACHE_NOTCHES, cacheLimitGb));
  /* The disk the smallest matching host can spare. A limit above it is not a
     limit: the disk fills first, and a host at or below its disk reserve takes
     no runner of any pool. */
  const spareGb = $derived(room?.disk_known ? Math.floor((room.smallest_disk_mb ?? 0) / 1024) : 0);
  const cacheMarks = $derived.by(() => {
    const marks: { value: number; label: string; recommended?: boolean }[] = [
      { value: 0, label: 'no limit' },
    ];
    if (spareGb > 0) {
      const fits = nearest(
        cacheNotches.filter((n) => n > 0 && n <= spareGb),
        spareGb / 2,
      );
      if (fits > 0) marks.push({ value: fits, label: 'fits every host', recommended: true });
    }
    const top = cacheNotches[cacheNotches.length - 1] ?? 500;
    if (!marks.some((m) => m.value === top)) marks.push({ value: top, label: gbLabel(top) });
    return marks;
  });
  const cacheAboveDisk = $derived(spareGb > 0 && cacheLimitGb > spareGb);
  /* The server refuses a limit on a named volume, because there is no
     directory to measure behind one. Saying so beside the slider beats finding
     out on the review step. */
  const cacheNeedsPath = $derived(cacheLimitGb > 0 && !draft.cache_source.trim().startsWith('/'));

  function setCacheLimit(gb: number): void {
    draft.cache_size_limit = gb > 0 ? String(cacheBytes(gb)) : '';
    touch('cache.size_limit');
  }
</script>

<!--
  The floor under the size. For a fixed size the standard is the figures above
  it, placed wherever a host has room; for an automatic size it is a slot's
  share of whichever host a runner lands on, or the minimum where the share is
  smaller -- a runner is never given less. Either way the minimum is what a
  host short of that may give instead, so the job runs rather than waiting --
  for a machine that is never coming, or for a whole slot on a host that has a
  free slot and most of one's worth left. Empty follows the fleet's
  runners.minimum_* live; there is no per-pool "none" -- a pool that wants
  none of the fleet's gives its own figure instead.
-->
{#snippet minimum()}
  <div class="pair">
    <Field
      label="Minimum CPU"
      error={errors['resources.min_cpus']}
      hint={profileSize
        ? 'The least a runner of this pool is given. A host whose standard size is below it is not used for this pool at all, and the reason is listed under the hosts. Empty follows the fleet default, if one is set.'
        : automaticSize
          ? 'The least a runner is given. A slot share smaller than this is raised to it, and a host with less than a whole share left may start a runner on what it has, down to this. Empty follows the fleet default, if one is set.'
          : 'Where no host has room for the CPU above, a runner may be given less, down to this. Empty follows the fleet default, if one is set.'}
    >
      {#snippet children({ id, describedBy, invalid })}
        <QuantityField
          {id}
          quantity="cpus"
          values={minCpuNotches}
          value={minCpus}
          label="Minimum CPU"
          valuetext={(v) => (v === 0 ? 'the fleet default' : cpuLabel(v))}
          marks={[{ value: 0, label: 'fleet' }]}
          empty={{ value: 0, placeholder: 'fleet default' }}
          {describedBy}
          {invalid}
          onchange={(v) => {
            draft.min_cpus = v ? String(v) : '';
            touch('resources.min_cpus');
          }}
        />
      {/snippet}
    </Field>
    <Field
      label="Minimum memory"
      error={errors['resources.min_memory_mb']}
      hint={profileSize
        ? 'The least a runner of this pool is given. A host whose standard size is below it is not used for this pool at all, and the reason is listed under the hosts. Empty follows the fleet default, if one is set.'
        : automaticSize
          ? 'The least a runner is given. A slot share smaller than this is raised to it, and a host with less than a whole share left may start a runner on what it has, down to this. Empty follows the fleet default, if one is set.'
          : 'Where no host has room for the memory above, a runner may be given less, down to this. Empty follows the fleet default, if one is set.'}
    >
      {#snippet children({ id, describedBy, invalid })}
        <QuantityField
          {id}
          quantity="mb"
          values={minMemoryNotches}
          value={minMemoryMb}
          label="Minimum memory"
          valuetext={(v) => (v === 0 ? 'the fleet default' : memoryLabel(v))}
          marks={[{ value: 0, label: 'fleet' }]}
          empty={{ value: 0, placeholder: 'fleet default' }}
          {describedBy}
          {invalid}
          onchange={(v) => {
            draft.min_memory_mb = v ? String(v) : '';
            touch('resources.min_memory_mb');
          }}
        />
      {/snippet}
    </Field>
  </div>
  {#if hasSidecar && draft.sizing !== 'fixed'}
    <Field
      label="Docker sidecar's share (%)"
      error={errors['resources.daemon_share_percent']}
      hint="A runner and its sidecar split one slot. Builds, image pulls and container tests run in the sidecar, so give it more if the runner container is mostly idle. Empty is an even split (50%); the runner keeps the rest. A host too small to give the smaller half a workable size is not used."
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={draft.daemon_share}
          {id}
          {describedBy}
          {invalid}
          inputmode="numeric"
          placeholder="50 (even split)"
          autocomplete="off"
          onblur={() => touch('resources.daemon_share_percent')}
        />
      {/snippet}
    </Field>
  {/if}
  {#if minCpus > 0 || minMemoryMb > 0}
    {#if profileSize}
      <p class="echo">
        A runner is given its host's standard size, and never less than
        {#if minCpus > 0}<strong>{cpuLabel(minCpus)}</strong
          >{/if}{#if minCpus > 0 && minMemoryMb > 0}
          and
        {/if}{#if minMemoryMb > 0}<strong>{memoryLabel(minMemoryMb)}</strong>{/if}. A host whose
        standard is smaller than that is not given this pool's runners.
      </p>
    {:else if automaticSize}
      <p class="echo">
        A runner is given a whole slot's share of its host wherever one is left, or the minimum
        where that is more, and a host whose share is smaller holds fewer runners. Where no whole
        share is left, it goes on the host with a free slot that can spare the most and is given as
        much as it can, never less than
        {#if minCpus > 0}<strong>{cpuLabel(minCpus)}</strong
          >{/if}{#if minCpus > 0 && minMemoryMb > 0}
          and
        {/if}{#if minMemoryMb > 0}<strong>{memoryLabel(minMemoryMb)}</strong>{/if}.
      </p>
    {:else}
      <p class="echo">
        A runner goes at <strong>{cpuLabel(cpus)}</strong> and
        <strong>{memoryLabel(memoryMb)}</strong>
        wherever a host has room. Where none has, it goes on the host that can spare the most and is given
        as much of that as it can, never less than
        <strong>{cpuLabel(minCpus > 0 ? minCpus : cpus)}</strong>
        and <strong>{memoryLabel(minMemoryMb > 0 ? minMemoryMb : memoryMb)}</strong>.
      </p>
    {/if}
  {/if}
{/snippet}

<fieldset class="group">
  <legend>What one runner gets</legend>
  <p class="hint">
    Whichever you choose, the fleet holds this much room for the runner on the host it lands on and
    the backend applies it as a real limit. There is no “unlimited” here.
  </p>

  <RadioGroup
    name="pool-sizing"
    bind:value={draft.sizing}
    options={[
      {
        value: 'automatic',
        label: 'One share of each host',
        description: automaticDescription,
      },
      {
        value: 'profile',
        label: 'The size each host sets',
        description: profileDescription,
      },
      {
        value: 'fixed',
        label: 'A fixed size on every host',
        description:
          'The same CPU and memory wherever a runner lands. For jobs that need a particular amount of machine, and for a pool whose hosts you do not want to share evenly.',
      },
    ]}
    onchange={() => {
      touch('resources.cpus');
      touch('resources.memory_mb');
    }}
  />

  {#if hostSized}
    {#if profileSize}
      <div class="shares" data-testid="profile-sizing">
        {#if sizedHosts === 0}
          <p class="shares-title">No host has set a standard size yet</p>
          <p class="shares-note">
            Until one does, every runner of this pool is the fleet's default, {fleetStandard}. Set a
            host's size from <a href="/hosts">Hosts</a>, under “Set runner sizes” on its card.
          </p>
        {:else}
          <p class="shares-title">
            {sizedHosts} of {pluralise(fleet.hosts.length, 'host')}
            {sizedHosts === 1 ? 'has' : 'have'} set a standard size
          </p>
          <p class="shares-note">
            The rows under the hosts say what a runner is on each one, and whose figure that is.
            Change a host's size from <a href="/hosts">Hosts</a>.
          </p>
        {/if}
      </div>
    {:else}
      <div class="shares">
        {#if !room || (room.hosts ?? []).length === 0}
          <p class="shares-empty">
            The share is worked out per host, once a host has reported what machine it is.
          </p>
        {:else}
          <p class="shares-title">What each host would give one runner</p>
          <ul>
            {#each room.hosts ?? [] as host (host.host_id)}
              <li>
                <span class="shares-host">{host.host}</span>
                <span class="shares-value"
                  >{cpuLabel(host.charge_cpus ?? 0)} and {memoryLabel(
                    host.charge_memory_mb ?? 0,
                  )}</span
                >
                <span class="shares-room">{pluralise(host.room ?? 0, 'runner')}</span>
              </li>
            {/each}
          </ul>
          <p class="shares-note">
            Straight from the controller, so it is the figure a runner is actually created with. It
            moves on its own when a host is resized or its slot count changes.
          </p>
        {/if}
      </div>
    {/if}

    {@render minimum()}

    {#if draft.backend === 'docker' || draft.backend === 'podman'}
      <Field
        label="Elastic CPU"
        error={errors['cpu_burst.mode']}
        hint="Observe measures safe boosts first. Automatic may lend spare CPU while preserving every runner's guarantee and room for the next queued job."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Select
            bind:value={draft.cpu_burst_mode}
            options={[
              { value: 'off', label: 'Off' },
              { value: 'observe', label: 'Observe only' },
              { value: 'automatic', label: 'Automatic boost' },
            ]}
            {id}
            {describedBy}
            {invalid}
            onchange={() => touch('cpu_burst.mode')}
          />
        {/snippet}
      </Field>

      <!-- The ceiling is a slider of its own, so it has the full width rather
           than half a row beside a menu. -->
      <Field
        label="Boost ceiling"
        error={errors['cpu_burst.max_cpus']}
        hint="Maximum CPU for one runner, such as 4 or 1.5. Leave empty to use whatever the host can safely lend. A host's own ceiling, set with its runner sizes, can lower this and never raise it."
      >
        {#snippet children({ id, describedBy, invalid })}
          <QuantityField
            quantity="cpus"
            values={burstNotches}
            value={Number(draft.cpu_burst_max) || 0}
            label="Boost ceiling"
            valuetext={(v) => (v === 0 ? 'the host decides' : cpuLabel(v))}
            marks={[{ value: 0, label: 'the host decides' }]}
            empty={{ value: 0, placeholder: 'Host ceiling' }}
            {id}
            {describedBy}
            {invalid}
            onchange={(v) => {
              draft.cpu_burst_max = v ? String(v) : '';
              touch('cpu_burst.max_cpus');
            }}
          />
        {/snippet}
      </Field>

      {#if draft.cpu_burst_mode === 'automatic' && (room?.hosts ?? []).length > 0}
        {#if cannotLend.length > 0}
          <p class="shares-note lend lend-warn" role="status">
            <TriangleAlert size={14} aria-hidden="true" />
            <span>
              {cannotLend.map((host) => host.host).join(', ')}
              {cannotLend.length === 1 ? 'runs' : 'run'} an agent that cannot lend CPU, so a runner placed
              there is held at its guaranteed share. Upgrade those agents from
              <a href="/hosts">Hosts</a>; the command is on each card.
            </span>
          </p>
        {:else}
          <p class="shares-note lend" role="status">
            <CircleCheck size={14} aria-hidden="true" />
            <span>Every host this pool can land on runs an agent that can lend CPU.</span>
          </p>
        {/if}
      {/if}

      {#if draft.cpu_burst_mode === 'automatic'}
        <Checkbox
          bind:checked={draft.cpu_burst_size_builds}
          label="Size builds for the ceiling"
          description="Starts each runner with CARGO_BUILD_JOBS, DOTNET_PROCESSOR_COUNT and the JVM's processor count set to the boost ceiling. Those toolchains count CPUs once, when they start, and would otherwise have no workers for CPU lent later. A value this pool's environment sets itself always wins."
          onchange={() => touch('cpu_burst.size_for_ceiling')}
        />
        <p class="shares-note">
          Busy runners can sprint; quiet runners keep their guarantee.
          {#if prefs.quirkyStatus}
            “Squirrel spotted” marks a major boost, and “Leash tightened” means host-pressure
            protection has taken precedence.
          {:else}
            “Maximum boost” marks a major boost, and “Throttled” means host-pressure protection has
            taken precedence.
          {/if}
          Memory stays fixed throughout.
        </p>
      {/if}
    {:else}
      <p class="shares-note">
        Elastic CPU is off for process runners because they have no live cgroup quota to measure or
        move.
      </p>
    {/if}
  {/if}

  {#if draft.sizing === 'fixed'}
    <div class="lead">
      <p class="echo">
        One runner asks for <strong>{cpuLabel(cpus)}</strong> and
        <strong>{memoryLabel(memoryMb)}</strong>{charged.pair
          ? `, and is charged ${cpuLabel(charged.cpus)} and ${memoryLabel(charged.memoryMb)} on a host — a docker-in-docker slot is two containers at a size you typed, and the daemon its builds run in is given the same`
          : ''}.
      </p>
      <Button
        variant="secondary"
        size="sm"
        icon={Sparkles}
        disabled={atDefaults}
        onclick={useDefaults}
      >
        Use the fleet's default
      </Button>
    </div>

    <Field
      label="CPU per runner"
      error={errors['resources.cpus']}
      hint="Becomes the container's CPU quota, and the cores the scheduler holds for it on a host."
    >
      {#snippet children({ id, describedBy })}
        <QuantityField
          {id}
          quantity="cpus"
          values={cpuNotches}
          value={cpus}
          label="CPU per runner"
          valuetext={cpuLabel}
          marks={cpuMarks}
          {describedBy}
          onchange={(v) => setCpus(v ?? 0)}
        />
      {/snippet}
    </Field>

    <Field
      label="Memory per runner"
      error={errors['resources.memory_mb']}
      hint="The container's memory limit. A job that goes past it is killed, so this is the figure to raise when a build dies without a message."
    >
      {#snippet children({ id, describedBy })}
        <QuantityField
          {id}
          quantity="mb"
          values={memoryNotches}
          value={memoryMb}
          label="Memory per runner"
          valuetext={memoryLabel}
          marks={memoryMarks}
          {describedBy}
          onchange={(v) => setMemory(v ?? 0)}
        />
      {/snippet}
    </Field>

    {@render minimum()}

    <Field
      label="Disk per runner"
      error={errors['resources.disk_gb']}
      hint="Advisory, and charged against the host's free disk so the fleet does not promise the same space twice. No limit is the usual answer: what keeps a host from filling up is its own disk reserve."
    >
      {#snippet children({ id, describedBy })}
        <QuantityField
          {id}
          quantity="gb"
          values={diskNotches}
          value={diskGb}
          label="Disk per runner"
          valuetext={gbLabel}
          marks={diskMarks}
          empty={{ value: 0, placeholder: 'no limit' }}
          {describedBy}
          onchange={(v) => setDisk(v ?? 0)}
        />
      {/snippet}
    </Field>
  {/if}

  <!--
    Both answers belong here, under the sliders, while there is still a reason
    to move them: a size is the one setting on this form that costs a host
    quietly. The fit says which machines this size has just put out of reach,
    in the fleet's own words; the room says how many runners the rest can hold.

    They are shown for both answers. An automatic pool can still be refused by
    a host -- for its backend, its platform, or a share that falls under what a
    runner needs to be a runner -- and that is exactly as worth knowing.
  -->
  <PoolFit {verdict} {validating} />
  <PoolRoom
    {room}
    cpus={profileSize ? 0 : cpus}
    memoryMb={profileSize ? 0 : memoryMb}
    profile={profileSize}
    {validating}
  />
</fieldset>

{#if containerBackend}
  <fieldset class="group">
    <legend>Scratch space in memory</legend>
    <p class="hint">
      A runner's work folder and <code>/tmp</code> normally live on its host's disk. On a host with slow
      disks and memory to spare, keeping them in memory removes the wait on every checkout, install and
      build. They are charged to the runner's memory limit, and gone when the runner is.
    </p>

    <RadioGroup
      bind:value={
        () => (draft.tmpfs_auto ? 'auto' : 'always'), (v) => (draft.tmpfs_auto = v === 'auto')
      }
      name="tmpfs-placement"
      legend="Placement"
      options={[
        {
          value: 'auto',
          label: 'Auto (recommended)',
          description:
            'Each runner decides: a folder is in memory where the runner has room for it to be useful (2 GB for the work folder, 1 GB for /tmp, 4 GB for the image store), and on disk where it has not. Safe for a pool whose hosts differ, and the pool says where a folder stayed on disk.',
        },
        {
          value: 'always',
          label: 'Always in memory',
          description:
            'Every runner gets the folders in memory, fitted as small as its limit demands. A runner too small for a folder fills it and fails jobs with "no space left on device".',
        },
      ]}
    />

    <Checkbox
      bind:checked={draft.tmpfs_work}
      label="Keep the work folder in memory"
      description="The checkout, build output and the runner's own temporary files. The one worth having."
      onchange={() => touch('tmpfs.work.enabled')}
    />
    {#if draft.tmpfs_work}
      <Field
        label="Work folder size (MB)"
        error={errors['tmpfs.work.size_mb']}
        hint="Leave it empty to fit it to the memory limit: up to 4096 MB, and no more than half of the limit."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.tmpfs_work_size}
            {id}
            {describedBy}
            {invalid}
            inputmode="numeric"
            placeholder="sized from the memory limit"
            autocomplete="off"
            onblur={() => touch('tmpfs.work.size_mb')}
          />
        {/snippet}
      </Field>
    {/if}

    <Checkbox
      bind:checked={draft.tmpfs_tmp}
      label="Keep /tmp in memory as well"
      description="Some toolchains put their heaviest traffic there, and some jobs leave gigabytes behind. Off unless you ask."
      onchange={() => touch('tmpfs.tmp.enabled')}
    />
    {#if draft.tmpfs_tmp}
      <Field
        label="/tmp size (MB)"
        error={errors['tmpfs.tmp.size_mb']}
        hint="Leave it empty to fit it to the memory limit: up to 1024 MB."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.tmpfs_tmp_size}
            {id}
            {describedBy}
            {invalid}
            inputmode="numeric"
            placeholder="sized from the memory limit"
            autocomplete="off"
            onblur={() => touch('tmpfs.tmp.size_mb')}
          />
        {/snippet}
      </Field>
    {/if}

    {#if hasSidecar}
      <Checkbox
        bind:checked={draft.tmpfs_daemon}
        label="Keep the Docker image store in memory"
        description="The Docker-in-Docker sidecar writes every image a job pulls and every layer it builds here, which is usually most of this pool's disk traffic. It is charged to the sidecar's own memory limit, and an image bigger than the store does not pull — so size it for the largest image your jobs use."
        onchange={() => touch('tmpfs.daemon.enabled')}
      />
      {#if draft.tmpfs_daemon}
        <Field
          label="Image store size (MB)"
          error={errors['tmpfs.daemon.size_mb']}
          hint="Leave it empty to fit it to the sidecar's memory limit: up to 8192 MB, and no more than half of it."
        >
          {#snippet children({ id, describedBy, invalid })}
            <Input
              bind:value={draft.tmpfs_daemon_size}
              {id}
              {describedBy}
              {invalid}
              inputmode="numeric"
              placeholder="sized from the memory limit"
              autocomplete="off"
              onblur={() => touch('tmpfs.daemon.size_mb')}
            />
          {/snippet}
        </Field>
      {/if}
    {/if}

    {#if tmpfsOn && draft.tmpfs_auto && plan}
      <div class="callout" class:ok={!planOnDisk} role="status" data-testid="tmpfs-plan">
        <div>
          <p class="callout-title">
            {#if planOnDisk}
              Auto puts {plan.in_memory} of {plan.total} folders in memory on your hosts
            {:else}
              Auto puts every folder in memory on your hosts
            {/if}
          </p>
          <ul class="plan">
            {#each plan.hosts as h (h.host)}
              <li>
                <strong>{h.host}</strong>, a runner has {memoryLabel(h.runner_mb ?? 0)}{h.daemon_mb
                  ? ` and its sidecar ${memoryLabel(h.daemon_mb)}`
                  : ''}:
                {#each h.folders ?? [] as f, i (f.name)}{i > 0 ? ', ' : ' '}{f.name}
                  {(f.mb ?? 0) > 0 ? `${memoryLabel(f.mb ?? 0)} in memory` : 'on disk'}{/each}
              </li>
            {/each}
          </ul>
          {#if planOnDisk}
            <p>
              A folder stays on disk where a runner is too small for it to be useful, which beats
              filling it and failing jobs. To put more in memory:
            </p>
            <ul class="plan">
              {#if plan.share}
                <li>
                  Give the sidecar {plan.share.percent}% of a slot — {plan.share.in_memory} of {plan.total}
                  folders in memory, no runners lost.
                  <Button
                    variant="secondary"
                    size="sm"
                    onclick={() => (draft.daemon_share = String(plan.share?.percent ?? ''))}
                  >
                    Set the share to {plan.share.percent}%
                  </Button>
                </li>
              {/if}
              {#each plan.sizes ?? [] as o (o.host)}
                <li>
                  {#if o.lever === 'standard'}
                    Raise <strong>{o.host}</strong>'s standard runner memory to {memoryLabel(
                      o.memory_mb ?? 0,
                    )}
                    ({pluralise(o.slots ?? 0, 'slot')}, now {o.slots_now}) under
                    <a href="/hosts">Runner sizes</a>.
                  {:else}
                    Lower <strong>{o.host}</strong>'s capacity to {pluralise(o.slots ?? 0, 'slot')} so
                    each runner is about {memoryLabel(o.memory_mb ?? 0)}, on the
                    <a href="/hosts">Hosts</a> page.
                  {/if}
                </li>
              {/each}
              {#if !plan.share && (plan.sizes ?? []).length === 0}
                <li>No change to these hosts' runner sizes puts the work folder in memory.</li>
              {/if}
            </ul>
          {/if}
        </div>
      </div>
    {/if}

    {#if tmpfsOn && !draft.tmpfs_auto && proposedLimitMb > 0}
      <div class="callout" role="status">
        <TriangleAlert size={16} aria-hidden="true" />
        <div>
          <p class="callout-title">Raise the memory limit to {memoryLabel(proposedLimitMb)}</p>
          <p>
            These folders may fill up to {memoryLabel(proposedLimitMb - typedLimitMb)}, and they
            come out of the {memoryLabel(typedLimitMb)} limit rather than being added to it. At
            {memoryLabel(proposedLimitMb)} the job keeps the room it has now.
          </p>
          <Button
            variant="secondary"
            size="sm"
            onclick={() => (draft.memory_mb = String(proposedLimitMb))}
          >
            Set the limit to {memoryLabel(proposedLimitMb)}
          </Button>
        </div>
      </div>
    {:else if tmpfsOn && !draft.tmpfs_auto && draft.sizing === 'automatic'}
      <p class="echo">
        This pool's runners are each given a share of their host{hasSidecar
          ? ', split between the runner and its Docker sidecar'
          : ''}, and the folders are fitted into half of the container's part — so they cannot take
        the memory a job needs. Choose a fixed size to set the limit yourself{hasSidecar
          ? ', which gives each container the whole of it'
          : ''}.
      </p>
    {/if}
  </fieldset>
{/if}

<fieldset class="group">
  <legend>Performance cache</legend>
  <p class="hint">
    Mounted at <code>/opt/zoomies-cache</code> and kept between runners. This is disposable build acceleration
    — dependencies and build outputs — not persistent workflow storage, and it may be evicted at any time.
  </p>

  <Checkbox
    bind:checked={draft.cache_enabled}
    label="Keep a cache between runners"
    description="Faster builds, at the price of state one job can leave for the next within the boundary below."
    onchange={() => touch('cache.enabled')}
  />

  {#if draft.cache_enabled}
    {#if draft.backend === 'docker' || draft.backend === 'podman'}
      <!--
        The tool cache is the one thing in a cache that a later job runs rather
        than reads, which is why it is its own choice and why it says so.
      -->
      <Checkbox
        bind:checked={draft.cache_tools}
        label="Keep a tool cache as well"
        description="What setup-python, setup-node, setup-go and setup-java download is kept in the host's shared folder for the next runner, within the same boundary. A job that can write to it can replace a tool the next job runs."
        onchange={() => touch('cache.tools')}
      />
    {/if}
    <div class="pair">
      <Field
        label="Isolation scope"
        error={errors['cache.scope']}
        hint="Pool: one cache all this pool's jobs share. Repository: a cache per repository, which is only as private as the pool's labels."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Select
            bind:value={draft.cache_scope}
            options={[
              { value: 'pool', label: 'Pool' },
              { value: 'repository', label: 'Repository' },
            ]}
            {id}
            {describedBy}
            {invalid}
          />
        {/snippet}
      </Field>

      <Field
        label="Host path or volume prefix"
        error={errors['cache.source']}
        hint="An absolute path on the host, or a named-volume prefix. A size limit needs the path: there is nothing to measure inside a volume. The shared folder is already visible to a containerised controller."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.cache_source}
            {id}
            {describedBy}
            {invalid}
            mono
            placeholder="/var/lib/zoomies/shared/cache/pools"
            autocomplete="off"
            onblur={() => touch('cache.source')}
          />
        {/snippet}
      </Field>
    </div>

    <Field
      label="Cache size limit"
      error={errors['cache.size_limit']}
      hint="Kept by evicting whole entries, least recently used first, between one runner and the next."
    >
      {#snippet children({ id, describedBy })}
        <QuantityField
          {id}
          quantity="gb"
          values={cacheNotches}
          value={cacheLimitGb}
          label="Cache size limit"
          valuetext={gbLabel}
          marks={cacheMarks}
          tone={cacheAboveDisk ? 'warning' : 'accent'}
          empty={{ value: 0, placeholder: 'no limit' }}
          {describedBy}
          onchange={(v) => setCacheLimit(v ?? 0)}
        />
      {/snippet}
    </Field>

    {#if cacheAboveDisk}
      <div class="callout" role="status">
        <TriangleAlert size={16} aria-hidden="true" />
        <div>
          <p class="callout-title">More cache than the smallest host can spare</p>
          <p>
            {room?.smallest_disk_host} has {formatMegabytes(room?.smallest_disk_mb ?? 0)} free behind
            its work directory, and this cache may grow to {gbLabel(cacheLimitGb)}. Eviction happens
            between one runner and the next, so the disk fills first — and a host at or below its
            disk reserve takes no runner of any pool, not just this one.
          </p>
        </div>
      </div>
    {:else if cacheNeedsPath}
      <div class="callout" role="status">
        <TriangleAlert size={16} aria-hidden="true" />
        <div>
          <p class="callout-title">A size limit needs a host path</p>
          <p>
            The limit is kept by evicting entries from a directory on the host, and there is nothing
            to measure inside a named volume. Give the source above an absolute path, or leave the
            limit at no limit.
          </p>
        </div>
      </div>
    {:else if spareGb > 0 && cacheLimitGb === 0}
      <p class="echo">
        With no limit the cache grows until the disk does. The smallest host this pool reaches has
        {formatMegabytes(room?.smallest_disk_mb ?? 0)} free.
      </p>
    {/if}

    {#if draft.cache_scope === 'repository'}
      <Field
        label="Cache repository (owner/name)"
        error={errors['cache.repository']}
        hint="Leave it empty when the pool's installation is scoped to a single repository — it already says which one."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.cache_repository}
            {id}
            {describedBy}
            {invalid}
            placeholder="acme/widgets"
            autocomplete="off"
            onblur={() => touch('cache.repository')}
          />
        {/snippet}
      </Field>
    {/if}

    {#if roomTotal > 0 && draft.cache_scope === 'pool'}
      <p class="echo">
        One cache per host, shared by every runner of this pool on it — up to {pluralise(
          roomTotal,
          'runner',
        )} across the fleet.
      </p>
    {/if}
  {/if}
</fieldset>

<style>
  .group {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin: 0;
    padding: 0;
    border: 0;
  }
  legend {
    padding: 0 0 var(--z-space-1);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .hint {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
  .hint code {
    font-family: var(--z-font-mono);
  }
  .lead {
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
  .echo {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .pair {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--z-space-4);
    align-items: start;
  }
  .shares {
    margin: var(--z-space-4) 0 0;
    padding: var(--z-space-3) var(--z-space-4);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }

  .shares-title,
  .shares-empty {
    margin: 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }

  .shares-title {
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }

  .shares ul {
    display: grid;
    gap: var(--z-space-1);
    margin: var(--z-space-2) 0 0;
    padding: 0;
    list-style: none;
  }

  .shares li {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto auto;
    gap: var(--z-space-3);
    align-items: baseline;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }

  .shares-host {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--z-text);
  }

  .shares-value {
    font-variant-numeric: tabular-nums;
    color: var(--z-text);
  }

  .shares-room {
    min-width: 8ch;
    text-align: end;
    font-variant-numeric: tabular-nums;
    color: var(--z-text-muted);
  }

  .shares-note {
    margin: var(--z-space-2) 0 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }

  .callout {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .callout p {
    margin: 0;
    max-width: 70ch;
  }
  .callout-title {
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .callout.ok {
    border-color: var(--z-idle-border);
    background: var(--z-idle-subtle);
  }
  .plan {
    margin: var(--z-space-2) 0;
    padding-left: var(--z-space-4);
  }
  .plan li + li {
    margin-top: var(--z-space-1);
  }
  @media (max-width: 768px) {
    .pair {
      grid-template-columns: minmax(0, 1fr);
    }
  }
  .lend {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
  }
  .lend :global(svg) {
    flex: none;
    margin-top: var(--z-nudge-2);
  }
  .lend-warn {
    color: var(--z-pending);
  }
</style>
