<!--
  The frame both MCP tables sit in: a bordered table that divides its width
  between its columns, and on a phone turns each row into a card whose cells
  carry their own headings -- the same answer the API tokens list gives, in
  one place for the two tables that share it here.

  Each cell names its heading in `data-label`, which is what the card layout
  prints beside it.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';

  let { children }: { children: Snippet } = $props();
</script>

<div class="frame"><div class="scroll">{@render children()}</div></div>

<style>
  .frame {
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .scroll {
    overflow-x: auto;
    /* See the API tokens list: what a wide table clips stays inside it
       rather than growing a phone's layout viewport. */
    contain: paint;
  }
  .frame :global(table) {
    width: 100%;
    table-layout: fixed;
    border-collapse: separate;
    border-spacing: 0;
    font-size: var(--z-text-sm);
  }
  .frame :global(th) {
    overflow-wrap: anywhere;
    padding: var(--z-space-2) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
  }
  .frame :global(td) {
    overflow: hidden;
    overflow-wrap: anywhere;
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text);
    vertical-align: top;
  }
  .frame :global(tbody tr:last-child td) {
    border-bottom: 0;
  }
  .frame :global(tr.ended td) {
    color: var(--z-text-subtle);
  }
  .frame :global(td.actions) {
    text-align: right;
  }
  @media (max-width: 768px) {
    .scroll {
      overflow-x: visible;
      contain: none;
    }
    .frame :global(table) {
      display: block;
    }
    .frame :global(thead) {
      display: none;
    }
    .frame :global(tbody) {
      display: flex;
      flex-direction: column;
      gap: var(--z-space-3);
      padding: var(--z-space-3);
    }
    .frame :global(tbody tr) {
      display: block;
      border: var(--z-border-width) solid var(--z-border);
      border-radius: var(--z-radius-md);
      background: var(--z-surface);
    }
    .frame :global(tbody td) {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--z-space-4);
      padding: var(--z-space-2) var(--z-space-3);
      border: 0;
      text-align: right;
    }
    .frame :global(tbody td::before) {
      content: attr(data-label);
      flex: none;
      color: var(--z-text-muted);
      font-size: var(--z-text-2xs);
      font-weight: var(--z-weight-medium);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
      text-align: left;
    }
  }
</style>
