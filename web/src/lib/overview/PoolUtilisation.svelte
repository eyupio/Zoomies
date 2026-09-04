<!--
  Per-pool utilisation.

  The one thing this section exists to make obvious: a pool sitting on its
  ceiling while jobs are still queued for it. That pool cannot grow, so the
  queue will not move until somebody raises its maximum or adds capacity. It is
  called out three ways -- a marked row, a badge that says so, and the bar's own
  ceiling tick -- because colour on its own is not an answer.
-->
<script lang="ts">
  import { Boxes, TriangleAlert } from '@lucide/svelte';
  import type { Pool, PoolStats } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { formatNumber, pluralise } from '$lib/format';
  import { poolStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import UtilisationBar from '$lib/components/UtilisationBar.svelte';
  import Panel from './Panel.svelte';

  interface Props {
    loading?: boolean;
    class?: string;
  }

  let { loading = false, class: className = '' }: Props = $props();

  interface Row {
    id: string;
    name: string;
    min: number;
    max: number;
    live: number;
    busy: number;
    queued: number;
    enabled: boolean;
    /** At its ceiling with work still waiting: the queue cannot move. */
    pinned: boolean;
  }

  function fromPool(pool: Pool, stats: PoolStats | undefined): Row {
    const counts = pool.counts;
    const max = stats?.max ?? pool.max_runners ?? 0;
    const live = stats?.live ?? counts?.live ?? 0;
    const queued = stats?.queued ?? pool.queued_jobs ?? 0;
    return {
      id: pool.id ?? '',
      name: pool.name ?? stats?.pool_name ?? 'Unnamed pool',
      min: stats?.min ?? pool.min_runners ?? 0,
      max,
      live,
      busy: stats?.busy ?? counts?.busy ?? 0,
      queued,
      enabled: pool.enabled !== false,
      pinned: max > 0 && live >= max && queued > 0,
    };
  }

  function fromStats(stats: PoolStats): Row {
    const max = stats.max ?? 0;
    const live = stats.live ?? 0;
    const queued = stats.queued ?? 0;
    return {
      id: stats.pool_id ?? '',
      name: stats.pool_name ?? 'Unnamed pool',
      min: stats.min ?? 0,
      max,
      live,
      busy: stats.busy ?? 0,
      queued,
      enabled: true,
      pinned: max > 0 && live >= max && queued > 0,
    };
  }

  const rows = $derived.by(() => {
    // Plain records rather than Maps: these are rebuilt from scratch on every
    // change, so there is nothing here that wants to be reactive itself.
    const byId: Record<string, PoolStats> = {};
    for (const entry of fleet.stats?.pools ?? []) {
      if (entry.pool_id) byId[entry.pool_id] = entry;
    }
    const out: Row[] = [];
    const seen: Record<string, true> = {};
    for (const pool of fleet.pools) {
      if (!pool.id) continue;
      seen[pool.id] = true;
      out.push(fromPool(pool, byId[pool.id]));
    }
    // A pool the stream told us about before the cache caught up still belongs
    // on the page.
    for (const [id, entry] of Object.entries(byId)) {
      if (!seen[id]) out.push(fromStats(entry));
    }
    // Pinned pools first, then alphabetical. Rows only move when a pool starts
    // or stops being stuck, which is precisely when moving is useful.
    return out.sort((a, b) => {
      if (a.pinned !== b.pinned) return a.pinned ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
  });

  const pinnedCount = $derived(rows.filter((r) => r.pinned).length);
  const canCreate = $derived(session.can('operator'));
</script>

<Panel
  title="Pools"
  description="Busy runners against live ones, with each pool's floor and ceiling marked."
  class={className}
  flush
>
  {#snippet actions()}
    {#if pinnedCount > 0}
      <Badge tone="pending" label="{formatNumber(pinnedCount)} at the ceiling" dot={false} />
    {/if}
  {/snippet}

  {#if loading}
    <p class="sr-only">Loading pools.</p>
    <ul class="rows" aria-hidden="true">
      {#each [0, 1, 2] as row (row)}
        <li class="row">
          <div class="head"><Skeleton width="7rem" height="var(--z-text-base)" /></div>
          <div class="bar"><Skeleton height="var(--z-space-4)" /></div>
          <div class="queued"><Skeleton width="4rem" height="var(--z-text-xs)" /></div>
        </li>
      {/each}
    </ul>
  {:else if rows.length === 0}
    <EmptyState
      icon={Boxes}
      compact
      title="No pools yet"
      description="A pool decides what labels your runners answer to and how many of them exist."
    >
      {#if canCreate}
        <Button variant="primary" href="/pools/new">Create a pool</Button>
      {/if}
    </EmptyState>
  {:else}
    <ul class="rows">
      {#each rows as row (row.id)}
        <li class="row" class:pinned={row.pinned}>
          <div class="head">
            <a class="name" href="/pools/{row.id}">{row.name}</a>
            <span class="range tabular"
              >{formatNumber(row.min)}–{formatNumber(row.max)} runners</span
            >
            <div class="badges">
              {#if !row.enabled}
                <Badge status={poolStatus({ enabled: row.enabled })} size="sm" />
              {/if}
              {#if row.pinned}
                <Badge tone="pending" size="sm" label="At its ceiling" dot={false} />
              {/if}
            </div>
          </div>
          <div class="bar">
            <UtilisationBar
              busy={row.busy}
              live={row.live}
              min={row.min}
              max={row.max}
              label="{row.name} utilisation"
            />
          </div>
          <p class="queued tabular" class:waiting={row.queued > 0}>
            {#if row.pinned}
              <TriangleAlert size={12} aria-hidden="true" />
            {/if}
            {pluralise(row.queued, 'job')} queued
          </p>
        </li>
      {/each}
    </ul>
  {/if}
</Panel>

<style>
  .rows {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(9rem, 1fr) minmax(10rem, 1.6fr) auto;
    align-items: center;
    gap: var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
    border-left: 3px solid transparent;
  }
  .row:last-child {
    border-bottom: 0;
  }
  .row.pinned {
    border-left-color: var(--z-pending);
    background: var(--z-pending-subtle);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-1) var(--z-space-2);
    min-width: 0;
  }
  .name {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    text-decoration: none;
    overflow-wrap: anywhere;
  }
  .name:hover {
    color: var(--z-accent);
    text-decoration: underline;
  }
  .range {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .badges {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    width: 100%;
  }
  .badges:empty {
    display: none;
  }
  .bar {
    min-width: 0;
  }
  .queued {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    margin: 0;
    justify-self: end;
    white-space: nowrap;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .queued.waiting {
    color: var(--z-pending);
    font-weight: var(--z-weight-medium);
  }
  @media (max-width: 768px) {
    .row {
      grid-template-columns: minmax(0, 1fr);
      gap: var(--z-space-2);
    }
    .queued {
      justify-self: start;
    }
  }
</style>
