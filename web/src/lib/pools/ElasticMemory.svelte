<!--
  Elastic memory: whether a runner that is about to be killed for its memory may
  be given more while its job runs.

  It is the memory counterpart of elastic CPU and differs in the one way that
  matters: CPU is lent and taken back by the same plan, and memory cannot be taken
  back, because lowering a live limit is what kills the process holding it. So
  what is lent here stays with the runner until it is gone, and is lent only out
  of memory that no runner's share needs and the host measures as free. That is why
  the choice is a ceiling for one runner and not a boost factor, and why the first
  step is to watch: "observe" decides what it would lend and records it, and a
  new pool starts there.

  What it needs of a host is an agent that can raise a limit between heartbeats,
  which is the one thing the controller cannot supply; the hosts without one are
  named beside the choice, as elastic CPU names its own.
-->
<script lang="ts">
  import { CircleCheck, TriangleAlert } from '@lucide/svelte';
  import type { PoolRoom as PoolRoomShape } from '$lib/api/types';
  import Field from '$lib/components/Field.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import { MEMORY_NOTCHES, SWAP_NOTCHES, memoryLabel, withValue } from './sizing';
  import type { PoolDraft } from './draft';

  /** The three fields of a draft this control reads and writes. A pool the controller keeps edits only these. */
  type MemoryBurstDraft = Pick<
    PoolDraft,
    'memory_burst_mode' | 'memory_burst_max' | 'memory_burst_spill'
  >;

  interface Props {
    draft: MemoryBurstDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    /** What the controller counted per host, for the hosts that cannot lend. */
    room: PoolRoomShape | null;
  }

  let { draft, errors, touch, room }: Props = $props();

  /* The ceiling's notches start at nothing, which is half as much again as a
     runner starts with. */
  const ceilingNotches = $derived(
    withValue([0, ...MEMORY_NOTCHES], Number(draft.memory_burst_max) || 0),
  );
  const swapNotches = $derived(withValue(SWAP_NOTCHES, Number(draft.memory_burst_spill) || 0));

  /* The hosts on which the valve would do nothing: their agent is too old to
     raise a live limit, so a runner there keeps the memory it was created with
     whatever is chosen here -- and an observing pool collects nothing from it. */
  const cannotLend = $derived((room?.hosts ?? []).filter((host) => !host.elastic_memory));
  const mode = $derived(draft.memory_burst_mode);
</script>

<fieldset class="group">
  <legend>Elastic memory</legend>
  <p class="hint">
    Raise the memory limit of a running job that is about to be killed for it, out of memory no
    other runner on its host has been promised. It is never taken back while the runner lives, so
    only what is spare is lent.
  </p>

  <RadioGroup
    name="pool-memory-burst"
    bind:value={draft.memory_burst_mode}
    options={[
      {
        value: 'off',
        label: 'Off',
        description: 'Every runner keeps exactly the memory it was created with.',
      },
      {
        value: 'observe',
        label: 'Observe only',
        description:
          'Works out what it would lend and records it, and changes nothing. Where to start: it shows whether this pool’s jobs would use the valve at all.',
      },
      {
        value: 'automatic',
        label: 'Lend memory',
        description:
          'Raises a running job’s limit just before the kernel would kill it, from memory no other runner has been promised and the host has free.',
      },
    ]}
    onchange={() => touch('memory_burst.mode')}
  />

  {#if mode !== 'off'}
    <Field
      label="Memory ceiling"
      error={errors['memory_burst.max_memory_mb']}
      hint="The most one runner may hold, its own share and what it is lent together. Empty is half as much again as it starts with; a host's own ceiling can lower this and never raise it."
    >
      {#snippet children({ id, describedBy, invalid })}
        <QuantityField
          quantity="mb"
          values={ceilingNotches}
          value={Number(draft.memory_burst_max) || 0}
          label="Memory ceiling"
          valuetext={(v) => (v === 0 ? 'half as much again' : memoryLabel(v))}
          marks={[{ value: 0, label: 'default' }]}
          empty={{ value: 0, placeholder: 'Half as much again' }}
          {id}
          {describedBy}
          {invalid}
          onchange={(v) => {
            draft.memory_burst_max = v ? String(v) : '';
            touch('memory_burst.max_memory_mb');
          }}
        />
      {/snippet}
    </Field>

    <Field
      label="Swap as the last resort"
      error={errors['memory_burst.spill_mb']}
      hint="Swap each container may use once its limit cannot be raised any further, so the job slows down instead of being killed. Empty allows none, and it is used only where the host has swap free."
    >
      {#snippet children({ id, describedBy, invalid })}
        <QuantityField
          quantity="mb"
          values={swapNotches}
          value={Number(draft.memory_burst_spill) || 0}
          label="Swap as the last resort"
          valuetext={(v) => (v === 0 ? 'none' : memoryLabel(v))}
          marks={[{ value: 0, label: 'none' }]}
          empty={{ value: 0, placeholder: 'None' }}
          {id}
          {describedBy}
          {invalid}
          onchange={(v) => {
            draft.memory_burst_spill = v ? String(v) : '';
            touch('memory_burst.spill_mb');
          }}
        />
      {/snippet}
    </Field>

    {#if (room?.hosts ?? []).length > 0}
      {#if cannotLend.length > 0}
        <p class="echo lend lend-warn" role="status">
          <TriangleAlert size={14} aria-hidden="true" />
          <span>
            {cannotLend.map((host) => host.host).join(', ')}
            {cannotLend.length === 1 ? 'runs' : 'run'} an agent that cannot
            {mode === 'automatic' ? 'lend memory' : 'watch memory'}, so a runner placed there keeps
            what it was created with. Upgrade those agents from <a href="/hosts">Hosts</a>; the
            command is on each card.
          </span>
        </p>
      {:else}
        <p class="echo lend" role="status">
          <CircleCheck size={14} aria-hidden="true" />
          <span>Every host this pool can land on runs an agent that can lend memory.</span>
        </p>
      {/if}
    {/if}
  {/if}
</fieldset>

<style>
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
