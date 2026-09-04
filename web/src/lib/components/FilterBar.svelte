<!--
  The controls that narrow a grid, and a chip for each filter currently applied.
  Every chip is individually removable, and there is always a way to clear the
  lot -- filters that cannot be seen are filters that get blamed on the server.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from '@lucide/svelte';

  export interface FilterChip {
    id: string;
    /** The field: "State". */
    label: string;
    /** The value: "busy". */
    value: string;
    onremove: () => void;
  }

  interface Props {
    chips?: readonly FilterChip[];
    onclear?: () => void;
    /** The search field and the filter menus. */
    children?: Snippet;
    class?: string;
  }

  let { chips = [], onclear, children, class: className = '' }: Props = $props();
</script>

<div class="filter-bar {className}">
  {#if children}
    <div class="controls">{@render children()}</div>
  {/if}
  {#if chips.length > 0}
    <div class="chips" role="group" aria-label="Filters in effect">
      {#each chips as chip (chip.id)}
        <span class="chip">
          <span class="chip-label">{chip.label}</span>
          <span class="chip-value">{chip.value}</span>
          <button
            type="button"
            aria-label="Remove the {chip.label} filter {chip.value}"
            onclick={chip.onremove}
          >
            <X size={11} aria-hidden="true" />
          </button>
        </span>
      {/each}
      {#if onclear}
        <button type="button" class="clear" onclick={onclear}>Clear all</button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .filter-bar {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .controls {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .chips {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    height: var(--z-space-5);
    padding: 0 var(--z-space-1) 0 var(--z-space-2);
    border: 1px solid var(--z-accent-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-accent-subtle);
    font-size: var(--z-text-xs);
  }
  .chip-label {
    color: var(--z-text-muted);
  }
  .chip-value {
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
  }
  .chip button {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--z-space-4);
    height: var(--z-space-4);
    border: 0;
    border-radius: var(--z-radius-sm);
    background: transparent;
    color: var(--z-text-muted);
    cursor: pointer;
  }
  .chip button:hover {
    background: var(--z-surface);
    color: var(--z-text);
  }
  .clear {
    border: 0;
    background: transparent;
    color: var(--z-accent);
    font-family: inherit;
    font-size: var(--z-text-xs);
    text-decoration: underline;
    cursor: pointer;
  }
</style>
