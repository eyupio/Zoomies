<script lang="ts">
  import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight } from '@lucide/svelte';
  import { formatNumber } from '../format';
  import { PAGE_SIZES } from '../state/prefs.svelte';
  import IconButton from './IconButton.svelte';

  interface Props {
    total: number;
    limit: number;
    offset: number;
    onpage: (offset: number) => void;
    onlimit?: (limit: number) => void;
    /** The plural noun for the count: "runners". */
    noun?: string;
    sizes?: readonly number[];
    class?: string;
  }

  let {
    total,
    limit,
    offset,
    onpage,
    onlimit,
    noun = 'rows',
    sizes = PAGE_SIZES,
    class: className = '',
  }: Props = $props();

  const first = $derived(total === 0 ? 0 : offset + 1);
  const last = $derived(Math.min(offset + limit, total));
  const atStart = $derived(offset <= 0);
  const atEnd = $derived(offset + limit >= total);
  const lastOffset = $derived(Math.max(0, (Math.ceil(total / limit) - 1) * limit));
</script>

<nav class="pagination {className}" aria-label="Pagination">
  <p class="count tabular">
    {#if total === 0}
      No {noun}
    {:else}
      {formatNumber(first)}–{formatNumber(last)} of {formatNumber(total)}
      {noun}
    {/if}
  </p>
  <div class="controls">
    {#if onlimit}
      <label class="size">
        <span>Rows</span>
        <select
          value={String(limit)}
          onchange={(e) => onlimit?.(Number((e.currentTarget as HTMLSelectElement).value))}
        >
          {#each sizes as size (size)}
            <option value={String(size)}>{size}</option>
          {/each}
        </select>
      </label>
    {/if}
    <IconButton
      icon={ChevronsLeft}
      label="First page"
      size="sm"
      disabled={atStart}
      onclick={() => onpage(0)}
    />
    <IconButton
      icon={ChevronLeft}
      label="Previous page"
      size="sm"
      disabled={atStart}
      onclick={() => onpage(Math.max(0, offset - limit))}
    />
    <IconButton
      icon={ChevronRight}
      label="Next page"
      size="sm"
      disabled={atEnd}
      onclick={() => onpage(offset + limit)}
    />
    <IconButton
      icon={ChevronsRight}
      label="Last page"
      size="sm"
      disabled={atEnd}
      onclick={() => onpage(lastOffset)}
    />
  </div>
</nav>

<style>
  .pagination {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-2) var(--z-space-1);
  }
  .count {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    font-variant-numeric: tabular-nums;
  }
  .controls {
    display: flex;
    align-items: center;
    gap: var(--z-space-1);
  }
  .size {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    margin-right: var(--z-space-2);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  select {
    height: var(--z-space-6);
    padding: 0 var(--z-space-1);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    color: var(--z-text);
    font-family: inherit;
    font-size: var(--z-text-xs);
  }
</style>
