<script lang="ts">
  import type { Snippet } from 'svelte';
  let {
    title,
    description,
    children,
    actions,
  }: { title: string; description: string; children: Snippet; actions?: Snippet } = $props();
</script>

<section class="chart-panel" aria-label={title}>
  <header>
    <div>
      <h2>{title}</h2>
      <p>{description}</p>
    </div>
    {#if actions}{@render actions()}{/if}
  </header>
  <div class="body">{@render children()}</div>
</section>

<style>
  .chart-panel {
    min-width: 0;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    overflow: hidden;
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: var(--z-space-3);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  h2 {
    margin: 0;
    /* A card title is --z-text-lg wherever the card is drawn: this frame is
       Panel's, and a smaller title made the same words two sizes by route. */
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  p {
    margin: var(--z-space-1) 0 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    max-width: 75ch;
  }
  .body {
    padding: var(--z-space-5);
    min-width: 0;
  }
</style>
