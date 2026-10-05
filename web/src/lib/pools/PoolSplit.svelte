<!--
  How a host-sized slot is divided between the runner and its Docker sidecar.

  CPU and memory are two shares, not one, because the two are not used alike. A
  build is CPU in the sidecar, so that is the share worth raising; memory is held
  by whatever the runner keeps -- the checkout, the toolchain, an in-memory work
  folder, all charged to the runner -- and by the sidecar's image layers, so it is
  the share that is often worth leaving even or lowering. A single number could
  not say "more CPU to the sidecar, and keep the memory".

  Nobody knows where their jobs do their work when a pool is made, so the answer
  is offered as presets with the reason beside each, and each is priced on the
  hosts the pool will land on: a skewed split raises a thin half to its minimum and
  the slot grows with it, which on a small slot loses runners. The preset a new pool
  starts on is one the controller found costs nothing, and the figures stay two
  numbers a person can type.
-->
<script lang="ts">
  import type { Result } from '$lib/api/types';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import RadioGroup from '$lib/components/RadioGroup.svelte';
  import { SPLIT_PRESETS, splitPreset } from './sizing';
  import type { PoolDraft } from './draft';

  type Plan = NonNullable<Result<'validatePool'>['room']>['split_plan'];

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
    plan: Plan | null;
  }

  let { draft, errors, touch, plan }: Props = $props();

  // Custom is a mode of its own, because typing 70 and 50 is also the build preset
  // and the form should not flip back to it from under the person typing.
  let custom = $state(false);
  const chosen = $derived(
    custom ? 'custom' : splitPreset(draft.daemon_cpu_share, draft.daemon_memory_share),
  );

  function apply(id: string) {
    draft.split_chosen = true;
    if (id === 'custom') {
      custom = true;
      return;
    }
    custom = false;
    const p = SPLIT_PRESETS.find((x) => x.id === id);
    if (!p) return;
    // Even is empty, which is what the API reads as the even split.
    draft.daemon_cpu_share = p.cpu === 50 ? '' : String(p.cpu);
    draft.daemon_memory_share = p.memory === 50 ? '' : String(p.memory);
  }

  // The preset a new pool starts on, once the controller has priced them. A pool
  // being edited, or one the person has already chosen for, is never moved.
  $effect(() => {
    if (plan?.recommended && !draft.split_chosen) {
      apply(plan.recommended);
      draft.split_chosen = true;
    }
  });

  // What a preset costs on these hosts, in the fleet's own count.
  function price(id: string): string {
    const o = plan?.options?.find((x) => x.id === id);
    if (!o || plan == null) return '';
    const now = plan.runners_now ?? 0;
    if (o.loses) {
      const reason = 'because a thinner half is raised to its minimum and the slot grows with it';
      return (o.runners ?? 0) === 0
        ? ` On your hosts no runner would fit, ${reason}.`
        : ` On your hosts this holds ${o.runners} runners instead of ${now}, ${reason}.`;
    }
    return ` On your hosts this loses no runners.`;
  }

  const options = $derived([
    ...SPLIT_PRESETS.map((p) => ({
      value: p.id,
      label: p.id === plan?.recommended ? `${p.label} (suggested start)` : p.label,
      description: p.description + price(p.id),
    })),
    {
      value: 'custom',
      label: 'Custom',
      description: 'Choose the sidecar’s share of the CPU and of the memory yourself.',
    },
  ]);
</script>

<fieldset class="split" data-testid="pool-split">
  <legend>Runner and Docker sidecar</legend>
  <p class="hint">
    Each runner is two containers sharing one slot: the runner, which checks out and runs the job,
    and a Docker sidecar, which runs image builds and containers. The slot is divided between them,
    CPU and memory on their own shares. Where your jobs do their work decides what suits you, and
    <a href="/pools">the pool says</a> when it sees one side short of room while the other idles.
  </p>
  <RadioGroup
    value={chosen}
    name="pool-split"
    legend="How to divide a slot"
    {options}
    onchange={(v) => apply(v)}
  />
  {#if chosen === 'custom'}
    <div class="pair">
      <Field
        label="Sidecar's CPU share (%)"
        error={errors['resources.daemon_cpu_share_percent']}
        hint="Image builds are CPU in the sidecar, so this is usually the one worth raising. 10 to 90; empty is even (50%)."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.daemon_cpu_share}
            {id}
            {describedBy}
            {invalid}
            inputmode="numeric"
            placeholder="50 (even)"
            autocomplete="off"
            onblur={() => touch('resources.daemon_cpu_share_percent')}
          />
        {/snippet}
      </Field>
      <Field
        label="Sidecar's memory share (%)"
        error={errors['resources.daemon_memory_share_percent']}
        hint="The runner's memory holds the checkout, the toolchain and any in-memory work folder, so this is often the one to leave even or lower. 10 to 90; empty is even (50%)."
      >
        {#snippet children({ id, describedBy, invalid })}
          <Input
            bind:value={draft.daemon_memory_share}
            {id}
            {describedBy}
            {invalid}
            inputmode="numeric"
            placeholder="50 (even)"
            autocomplete="off"
            onblur={() => touch('resources.daemon_memory_share_percent')}
          />
        {/snippet}
      </Field>
    </div>
  {/if}
</fieldset>

<style>
  .split {
    display: grid;
    gap: var(--z-space-3);
    border: 0;
    margin: 0;
    padding: 0;
  }
  legend {
    font-weight: var(--z-weight-semibold);
    padding: 0;
  }
  .hint {
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
  }
  @media (max-width: 768px) {
    .pair {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
