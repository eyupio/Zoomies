<!--
  Step two: the labels.

  Labels are the whole contract between a workflow and this pool, so the step
  shows the `runs-on` line as it is typed rather than describing it in prose.
-->
<script lang="ts">
  import { joinWords } from '$lib/format';
  import Field from '$lib/components/Field.svelte';
  import { BRAND_LABEL, brandedLabel, isImplicit } from '$lib/brand';
  import LabelInput from './LabelInput.svelte';
  import RunsOnPreview from './RunsOnPreview.svelte';
  import type { PoolDraft } from './PoolWizardForm.svelte';

  interface Props {
    draft: PoolDraft;
    errors: Record<string, string>;
    touch: (field: string) => void;
  }

  let { draft, errors, touch }: Props = $props();

  const duplicated = $derived(draft.labels.filter((label) => isImplicit(label)));

  // Every pool answers to the brand, whether or not it is typed here: the
  // server adds it on save. Showing it is the honest thing to do, and it is
  // what "runs-on: zoomies" -- any runner in this fleet -- resolves to.
  const carriesBrand = $derived(
    draft.labels.some((label) => label.trim().toLowerCase() === BRAND_LABEL),
  );

  // The label this pool's name suggests. It is only offered while the operator
  // has not already given the pool a label of its own, so it never nags.
  const suggestion = $derived(brandedLabel(draft.name));
  const showSuggestion = $derived(
    draft.name.trim() !== '' &&
      suggestion !== BRAND_LABEL &&
      !draft.labels.some((label) => label.trim().toLowerCase() === suggestion),
  );

  function useSuggestion(): void {
    draft.labels = [...draft.labels, suggestion];
    touch('labels');
  }
</script>

<p class="lede">
  A job reaches this pool when the labels in its <code>runs-on</code> are all labels this pool
  answers to. Brand them — <code>zoomies-linux-x64</code>, <code>zoomies-gpu</code>,
  <code>zoomies-large</code> — so that a reviewer of the pull request that adds one can tell at a glance
  that the job leaves GitHub's runners for this fleet.
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

{#if showSuggestion}
  <p class="suggest">
    <button type="button" onclick={useSuggestion}>Use <code>{suggestion}</code></button>
    — the branded label this pool's name suggests.
  </p>
{/if}

<RunsOnPreview labels={draft.labels} />

{#if !carriesBrand}
  <p class="note">
    Every pool also answers to <code>{BRAND_LABEL}</code>, which Zoomies adds when the pool is
    saved. That is what a repository writes before anyone has decided which pool it belongs in, and
    what the migration wizard puts in a workflow it rewrites.
  </p>
{/if}

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
  .suggest {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .suggest button {
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    cursor: pointer;
    text-decoration: underline;
  }
  .suggest code,
  .note code {
    padding: 0 var(--z-space-1);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-2xs);
  }
  .note {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
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
