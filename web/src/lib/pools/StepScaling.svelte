<!--
  Step four: how many runners, for how long, and how big.

  The maximum is the one number worth explaining: it is not a target, it is the
  backstop that stops a runaway workflow turning into a hosting bill.
-->
<script lang="ts">
  import { formatGoDuration, parseGoDuration } from '$lib/format';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import type { PoolDraft } from './PoolWizardForm.svelte';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
  }

  let { draft, errors, touch }: Props = $props();

  const timeout = $derived(parseGoDuration(draft.idle_timeout));
  const timeoutText = $derived(timeout === null ? '' : formatGoDuration(draft.idle_timeout));
</script>

<div class="pair">
  <Field
    label="Minimum runners"
    error={errors['min_runners']}
    hint="Kept warm even when nothing is queued. Zero means the pool costs nothing while it is idle, at the price of a cold start on the first job."
  >
    {#snippet children({ id, describedBy, invalid })}
      <Input
        bind:value={draft.min_runners}
        {id}
        {describedBy}
        {invalid}
        type="number"
        min={0}
        step={1}
        onblur={() => touch('min_runners')}
      />
    {/snippet}
  </Field>

  <Field
    label="Maximum runners"
    required
    error={errors['max_runners']}
    hint="The backstop. However many jobs GitHub queues, this pool will never create more runners than this — which is what stops one misconfigured workflow filling every host you have."
  >
    {#snippet children({ id, describedBy, invalid })}
      <Input
        bind:value={draft.max_runners}
        {id}
        {describedBy}
        {invalid}
        type="number"
        min={1}
        step={1}
        onblur={() => touch('max_runners')}
      />
    {/snippet}
  </Field>
</div>

<Field
  label="Idle timeout"
  error={errors['idle_timeout']}
  hint="How long a runner waits for work before it is destroyed. A Go duration: 5m, 90s, 1h30m."
>
  {#snippet children({ id, describedBy, invalid })}
    <Input
      bind:value={draft.idle_timeout}
      {id}
      {describedBy}
      {invalid}
      mono
      placeholder="5m"
      autocomplete="off"
      onblur={() => touch('idle_timeout')}
    />
  {/snippet}
</Field>

{#if timeoutText}
  <p class="echo">Runners above the minimum are destroyed after {timeoutText} with no work.</p>
{/if}

<Checkbox
  bind:checked={draft.ephemeral}
  label="Destroy each runner after one job"
  description="The safe default. Turning it off reuses a runner between jobs, which is faster and means one job can leave files, credentials or processes behind for the next."
  onchange={() => touch('ephemeral')}
/>

<fieldset class="resources">
  <legend>Resources per runner</legend>
  <p class="hint">Leave a box empty for no limit. The host's own capacity still applies.</p>
  <div class="triple">
    <Field label="CPUs" error={errors['resources.cpus']}>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={draft.cpus}
          {id}
          {describedBy}
          {invalid}
          type="number"
          min={0}
          step={0.5}
          placeholder="2"
          onblur={() => touch('resources.cpus')}
        />
      {/snippet}
    </Field>

    <Field label="Memory (MB)" error={errors['resources.memory_mb']}>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={draft.memory_mb}
          {id}
          {describedBy}
          {invalid}
          type="number"
          min={0}
          step={256}
          placeholder="4096"
          onblur={() => touch('resources.memory_mb')}
        />
      {/snippet}
    </Field>

    <Field label="Disk (GB)" error={errors['resources.disk_gb']}>
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={draft.disk_gb}
          {id}
          {describedBy}
          {invalid}
          type="number"
          min={0}
          step={1}
          placeholder="20"
          onblur={() => touch('resources.disk_gb')}
        />
      {/snippet}
    </Field>
  </div>
</fieldset>

<style>
  .pair {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--z-space-5);
    align-items: start;
  }
  .echo {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .resources {
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
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .triple {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--z-space-4);
  }
  @media (max-width: 768px) {
    .pair,
    .triple {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
