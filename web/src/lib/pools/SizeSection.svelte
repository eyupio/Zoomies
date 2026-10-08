<!--
  How much machine one runner gets, and whether it may borrow more.

  It comes after the hosts and before the count because that is the order the
  decision is actually made in: these are the machines, this is what one runner
  of mine costs on them, and therefore this is how many there can be. Asked the
  other way round -- a maximum typed first, a size typed last -- the number that
  mattered was chosen before anything on screen could say what it would buy.

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

  The floor under the size and the elastic CPU choice are the refinements, and
  each is kept to the pools that can use it: the floor behind a disclosure of
  its own, elastic CPU only for a size that follows its host.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { Sparkles } from '@lucide/svelte';
  import type { PoolRoom as PoolRoomShape, Resources, Result } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import Button from '$lib/components/Button.svelte';
  import Field from '$lib/components/Field.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import ElasticCpu from './ElasticCpu.svelte';
  import ElasticMemory from './ElasticMemory.svelte';
  import PoolFit from './PoolFit.svelte';
  import PoolMore from './PoolMore.svelte';
  import PoolRoom from './PoolRoom.svelte';
  import PoolSplit from './PoolSplit.svelte';
  import {
    CPU_NOTCHES,
    DISK_NOTCHES,
    MEMORY_NOTCHES,
    chargedSize,
    cpuLabel,
    gbLabel,
    memoryLabel,
    withValue,
  } from './sizing';
  import type { PoolDraft } from './draft';

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

  // Open already when the pool has a minimum of its own, or one is refused: a
  // setting that is in use is never hidden behind a closed row.
  let minimumOpen = $state(
    untrack(
      () =>
        Number(draft.min_cpus) > 0 ||
        Number(draft.min_memory_mb) > 0 ||
        Boolean(errors['resources.min_cpus'] || errors['resources.min_memory_mb']),
    ),
  );

  const containerBackend = $derived(draft.backend === 'docker' || draft.backend === 'podman');
  /**
   * Only a Docker-in-Docker pool on the Docker backend has a sidecar, and the
   * sidecar is a second container with a memory limit of its own.
   */
  const hasSidecar = $derived(draft.backend === 'docker' && draft.docker_mode === 'dind');

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
  const minimumNote = $derived(
    minCpus > 0 || minMemoryMb > 0
      ? `never below ${[
          minCpus > 0 ? cpuLabel(minCpus) : '',
          minMemoryMb > 0 ? memoryLabel(minMemoryMb) : '',
        ]
          .filter(Boolean)
          .join(' and ')}`
      : 'follows the fleet default',
  );
  const minCpuNotches = $derived(
    withValue([0, ...CPU_NOTCHES.filter((n) => hostSized || n <= cpus)], minCpus),
  );
  const minMemoryNotches = $derived(
    withValue([0, ...MEMORY_NOTCHES.filter((n) => hostSized || n <= memoryMb)], minMemoryMb),
  );
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
     charged. It is what both this section and the scaling one are read against. */
  const room = $derived<PoolRoomShape | null>(verdict?.room ?? null);
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

  What it does is said once, above the two figures, in the words of the size
  that is chosen, rather than under each of them: the two hints were the same
  three sentences twice.
-->
{#snippet minimum()}
  <p class="echo">
    {#if profileSize}
      A host whose standard size is below this is not used for this pool at all, and the reason is
      listed under the hosts.
    {:else if automaticSize}
      A slot share smaller than this is raised to it, and a host with less than a whole share left
      may start a runner on what it has, down to this.
    {:else}
      Where no host has room for the size above, a runner may be given less, down to this.
    {/if}
    Empty follows the fleet default, if one is set.
  </p>
  <div class="pair">
    <Field label="Minimum CPU" error={errors['resources.min_cpus']}>
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
    <Field label="Minimum memory" error={errors['resources.min_memory_mb']}>
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
  <!--
    The least a runner is given, as the sentences below say it: "1 core and 2 GB",
    "1 core", or "2 GB". One branch each, with the "and" between two elements in
    the branch that has both: Svelte trims the whitespace at either end of a
    block, so an "and" that was a block of its own, between two {#if} blocks,
    lost both its spaces and read "1 coreand2 GB".
  -->
  {#snippet minimumFloor()}
    {#if minCpus > 0 && minMemoryMb > 0}
      <strong>{cpuLabel(minCpus)}</strong> and <strong>{memoryLabel(minMemoryMb)}</strong>
    {:else if minCpus > 0}
      <strong>{cpuLabel(minCpus)}</strong>
    {:else}
      <strong>{memoryLabel(minMemoryMb)}</strong>
    {/if}
  {/snippet}
  {#if minCpus > 0 || minMemoryMb > 0}
    {#if profileSize}
      <p class="echo">
        A runner is given its host's standard size, and never less than
        {@render minimumFloor()}. A host whose standard is smaller than that is not given this
        pool's runners.
      </p>
    {:else if automaticSize}
      <p class="echo">
        A runner is given a whole slot's share of its host wherever one is left, or the minimum
        where that is more, and a host whose share is smaller holds fewer runners. Where no whole
        share is left, it goes on the host with a free slot that can spare the most and is given as
        much as it can, never less than
        {@render minimumFloor()}.
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

    {#if hasSidecar}
      <PoolSplit {draft} {errors} {touch} plan={room?.split_plan ?? null} />
    {/if}
  {/if}

  {#if draft.sizing === 'fixed'}
    <div class="proposal">
      <p class="echo">
        One runner asks for <strong>{cpuLabel(cpus)}</strong> and
        <strong>{memoryLabel(memoryMb)}</strong>{charged.pair
          ? `, and is charged ${cpuLabel(charged.cpus)} and ${memoryLabel(charged.memoryMb)} on a host, a docker-in-docker slot is two containers at a size you typed, and the daemon its builds run in is given the same`
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

  <PoolMore
    title="Smallest runner this pool will accept"
    note={minimumNote}
    bind:open={minimumOpen}
  >
    {@render minimum()}
  </PoolMore>

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
  {#if hostSized}
    <ElasticCpu {draft} {errors} {touch} {room} />
  {:else}
    <p class="echo">
      A fixed size cannot borrow spare CPU: every runner is held at the figure above. Choose a share
      of each host, or the size each host sets, to turn elastic CPU on.
    </p>
  {/if}
{:else}
  <p class="echo">
    Elastic CPU and memory are off for process runners because they have no live container limits to
    measure or move.
  </p>
{/if}

{#if containerBackend}
  <!-- Memory needs only a limit to watch, so unlike CPU it is offered whatever
       the size is decided by. -->
  <ElasticMemory {draft} {errors} {touch} {room} />
{/if}

<style>
  .shares {
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
  /* A phone has no room for a host, a figure and a count on one line: the host
     takes a line of its own and the other two share the next. */
  @media (max-width: 768px) {
    .shares li {
      grid-template-columns: minmax(0, 1fr) auto;
    }
    .shares-host {
      grid-column: 1 / -1;
    }
    .shares-room {
      min-width: 0;
    }
  }
</style>
