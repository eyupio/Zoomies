<!--
  Elastic CPU: whether a busy runner may use CPU its neighbours are not using.

  A runner is always given its share as a guarantee. Elastic CPU lends the
  spare above it to a runner that is actually busy, and takes it back the
  moment the runner that owns it needs it, so a quiet host stops being a
  wasted one without a busy runner ever being slower than it was promised.

  It is a choice of three rather than a switch because the honest first step is
  to watch: "observe" measures which boosts would have been safe and applies
  none, which is why a new pool starts there. Memory is not part of it, and has
  a choice of its own below: a limit that is lowered kills the process holding
  it, so memory is lent on different terms.

  The one thing on a host that elastic CPU needs, and the one the controller
  cannot supply itself, is an agent new enough to move a live quota; the hosts
  that lack one are named here, beside the choice, rather than found in a metric
  afterwards.
-->
<script lang="ts">
  import { CircleCheck, TriangleAlert } from '@lucide/svelte';
  import type { PoolRoom as PoolRoomShape } from '$lib/api/types';
  import { prefs } from '$lib/state/prefs.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Field from '$lib/components/Field.svelte';
  import QuantityField from '$lib/components/QuantityField.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import { CPU_NOTCHES, cpuLabel, withValue } from './sizing';
  import type { PoolDraft } from './draft';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    /** What the controller counted per host, for the hosts that cannot lend. */
    room: PoolRoomShape | null;
  }

  let { draft, errors, touch, room }: Props = $props();

  /* The boost ceiling's notches start at nothing, which leaves it to the host. */
  const burstNotches = $derived(withValue([0, ...CPU_NOTCHES], Number(draft.cpu_burst_max) || 0));

  /* The hosts on which an elastic pool would not be elastic: their agent is too
     old to move a live quota, so a runner there is held at its share whatever
     is chosen here. */
  const cannotLend = $derived((room?.hosts ?? []).filter((host) => !host.elastic_cpu));
  const mode = $derived(draft.cpu_burst_mode);
</script>

<fieldset class="group">
  <legend>Elastic CPU</legend>
  <p class="hint">
    Lend spare CPU to a runner that is busy, and take it back the moment its owner needs it. Memory
    cannot be taken back, so it has a choice of its own, below.
  </p>

  <RadioGroup
    name="pool-cpu-burst"
    bind:value={draft.cpu_burst_mode}
    options={[
      {
        value: 'off',
        label: 'Off',
        description: 'Every runner keeps exactly its share.',
      },
      {
        value: 'observe',
        label: 'Observe only',
        description: 'Measures which boosts would have been safe, and applies none.',
      },
      {
        value: 'automatic',
        label: 'Automatic boost',
        description:
          'Lends spare CPU to busy runners, preserving every runner’s guarantee and room for the next queued job.',
      },
    ]}
    onchange={() => touch('cpu_burst.mode')}
  />

  {#if mode !== 'off'}
    <Field
      label="Boost ceiling"
      error={errors['cpu_burst.max_cpus']}
      hint="The most CPU one runner may use, such as 4 or 1.5. Empty uses whatever the host can safely lend; a host's own ceiling can lower this and never raise it."
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
  {/if}

  {#if mode === 'automatic' && (room?.hosts ?? []).length > 0}
    {#if cannotLend.length > 0}
      <p class="echo lend lend-warn" role="status">
        <TriangleAlert size={14} aria-hidden="true" />
        <span>
          {cannotLend.map((host) => host.host).join(', ')}
          {cannotLend.length === 1 ? 'runs' : 'run'} an agent that cannot lend CPU, so a runner placed
          there is held at its guaranteed share. Upgrade those agents from
          <a href="/hosts">Hosts</a>; the command is on each card.
        </span>
      </p>
    {:else}
      <p class="echo lend" role="status">
        <CircleCheck size={14} aria-hidden="true" />
        <span>Every host this pool can land on runs an agent that can lend CPU.</span>
      </p>
    {/if}
  {/if}

  {#if mode === 'automatic'}
    <Checkbox
      bind:checked={draft.cpu_burst_size_builds}
      label="Size builds for the ceiling"
      description="Starts each runner with CARGO_BUILD_JOBS, DOTNET_PROCESSOR_COUNT and the JVM's processor count set to the boost ceiling. Those toolchains count CPUs once, when they start, and would otherwise have no workers for CPU lent later. A value this pool's environment sets itself always wins."
      onchange={() => touch('cpu_burst.size_for_ceiling')}
    />
    <p class="echo">
      Busy runners can sprint; quiet runners keep their guarantee.
      {#if prefs.quirkyStatus}
        “Squirrel spotted” marks a major boost, and “Leash tightened” means host-pressure protection
        has taken precedence.
      {:else}
        “Maximum boost” marks a major boost, and “Throttled” means host-pressure protection has
        taken precedence.
      {/if}
    </p>
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
