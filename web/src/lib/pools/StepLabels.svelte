<!--
  Step two: the labels.

  Labels are the whole contract between a workflow and this pool, so the step
  shows the `runs-on` line as it is typed rather than describing it in prose.
-->
<script lang="ts">
  import { joinWords } from '$lib/format';
  import Field from '$lib/components/Field.svelte';
  import LabelInput, { isImplicit } from './LabelInput.svelte';
  import RunsOnPreview from './RunsOnPreview.svelte';
  import type { PoolDraft } from './PoolWizardForm.svelte';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
  }

  let { draft, errors, touch }: Props = $props();

  const duplicated = $derived(draft.labels.filter((label) => isImplicit(label)));
</script>

<p class="lede">
  A job reaches this pool when the labels in its <code>runs-on</code> are all labels this pool
  answers to. Name them for what the runner actually is —
  <code>linux-x64</code>, <code>gpu</code>, <code>large</code> — rather than for the team that asked for
  it.
</p>

<Field
  label="Labels"
  required
  error={errors['labels']}
  hint="Press Enter or a comma after each one. Order does not matter."
>
  {#snippet children({ id, describedBy, invalid })}
    <LabelInput
      bind:value={draft.labels}
      {id}
      {describedBy}
      {invalid}
      onblur={() => touch('labels')}
    />
  {/snippet}
</Field>

<RunsOnPreview labels={draft.labels} />

{#if duplicated.length > 0}
  <p class="warn">
    GitHub already gives every self-hosted runner {joinWords(duplicated)}, so adding
    {duplicated.length === 1 ? 'it' : 'them'} here narrows nothing down. It is harmless, but the pool
    will look more specific than it is.
  </p>
{/if}

<style>
  .lede {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .lede code {
    padding: 0 var(--z-space-1);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
  }
  .warn {
    margin: 0;
    padding: var(--z-space-3) var(--z-space-4);
    border: 1px solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
    color: var(--z-pending);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
</style>
