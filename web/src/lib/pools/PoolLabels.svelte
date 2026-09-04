<!--
  A pool's labels, as chips.

  Labels are what a workflow writes in `runs-on`, so they are shown verbatim and
  monospaced -- an operator comparing a pool against a workflow file is matching
  strings character for character.
-->
<script lang="ts">
  interface Props {
    labels?: readonly string[];
    /** Show at most this many chips; the rest are summarised. */
    max?: number;
    class?: string;
  }

  let { labels = [], max = 4, class: className = '' }: Props = $props();

  const shown = $derived(labels.slice(0, max));
  const hidden = $derived(labels.slice(max));
</script>

{#if labels.length === 0}
  <span class="none {className}">No labels</span>
{:else}
  <span class="labels {className}">
    {#each shown as label (label)}
      <code class="chip">{label}</code>
    {/each}
    {#if hidden.length > 0}
      <span class="more" aria-hidden="true">+{hidden.length}</span>
      <span class="sr-only">and {hidden.length} more: {hidden.join(', ')}</span>
    {/if}
  </span>
{/if}

<style>
  .labels {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .chip {
    display: inline-block;
    max-width: 22ch;
    padding: 0 var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    color: var(--z-text);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    vertical-align: middle;
  }
  .more {
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
  }
  .none {
    color: var(--z-text-subtle);
  }
</style>
