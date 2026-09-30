<script lang="ts">
  import type { Pool } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import ChartPanel from '$lib/components/ChartPanel.svelte';
  import MetricGrid from '$lib/components/MetricGrid.svelte';
  import { poolSignals, queueTone } from './signals';
  import { formatNumber, NO_VALUE } from '$lib/format';
  let { pools, summary = true }: { pools: Pool[]; summary?: boolean } = $props();
  const signals = $derived(poolSignals(pools, fleet.stats));
  const ranked = $derived(
    [...signals]
      .sort(
        (a, b) =>
          (b.queued ?? -1) - (a.queued ?? -1) ||
          (a.pool.name ?? '').localeCompare(b.pool.name ?? ''),
      )
      .slice(0, 8),
  );
  const peak = $derived(Math.max(1, ...ranked.map((r) => r.queued ?? 0)));
  const known = $derived(signals.every((s) => s.queued !== null));
  const queue = $derived(signals.reduce((n, s) => n + (s.queued ?? 0), 0));
</script>

<div class="pressure">
  {#if summary}<MetricGrid
      items={[
        {
          label: 'Pools in view',
          value: formatNumber(pools.length),
          detail: `${pools.filter((p) => p.enabled === true).length} enabled · ${pools.filter((p) => p.enabled === false).length} disabled`,
        },
        {
          label: 'Matched job queue',
          value: known ? formatNumber(queue) : NO_VALUE,
          detail: 'Waiting jobs claimed by these pools',
          tone: queueTone(queue, session.meta?.queue_warning_threshold),
        },
        {
          label: 'Pools with waiting work',
          value: known ? String(signals.filter((s) => (s.queued ?? 0) > 0).length) : NO_VALUE,
          detail: 'Queue depth greater than zero',
        },
        {
          label: 'At ceiling with a queue',
          value: String(signals.filter((s) => s.atCeiling).length),
          detail: 'Enabled pools with no configured growth room',
          tone: signals.some((s) => s.atCeiling) ? 'warning' : 'neutral',
        },
      ]}
    />{/if}
  <ChartPanel
    title="Pool demand and headroom"
    description="Largest matched job queues first. Runner slots are pool limits; host resources, compatibility and provisioning policy still determine whether work can start."
  >
    <div class="rows">
      {#each ranked as s (s.pool.id)}
        <div class="row">
          <div class="identity">
            <a href={`/pools/${s.pool.id}`}>{s.pool.name ?? s.pool.id}</a><span
              >{s.pool.enabled === false
                ? 'Disabled'
                : s.atCeiling
                  ? 'At ceiling with queued jobs'
                  : `${s.headroom ?? NO_VALUE} pool slots below maximum`}</span
            >
          </div>
          <a
            class="queue"
            href={`/jobs?state=queued&pool_id=${encodeURIComponent(s.pool.id ?? '')}`}
            aria-label={`${s.pool.name}: ${s.queued ?? 'unknown'} queued jobs`}
            ><span class="track"
              ><span style:width={`${(100 * (s.queued ?? 0)) / peak}%`}></span></span
            ><strong>{s.queued ?? NO_VALUE}</strong><span>queued</span></a
          >
          <div class="capacity">
            <strong>{s.live ?? NO_VALUE} / {s.max ?? NO_VALUE}</strong><span>live / max</span>
          </div>
        </div>
      {:else}<p>No pools in this view.</p>{/each}
    </div>
    {#if signals.length > 8}<p class="note">
        Showing the eight largest queues of {signals.length} pools. All pools remain available below.
      </p>{/if}
  </ChartPanel>
</div>

<style>
  .pressure {
    margin: var(--z-space-4) 0 var(--z-space-5);
  }
  .rows {
    display: grid;
    gap: var(--z-space-3);
  }
  .row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(100px, 1fr) auto;
    gap: var(--z-space-4);
    align-items: center;
    padding: var(--z-space-2) 0;
  }
  .identity {
    min-width: 0;
    display: grid;
    gap: var(--z-space-1);
  }
  .identity a {
    color: var(--z-text);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    overflow-wrap: anywhere;
  }
  .identity span,
  .capacity span,
  .queue > span:last-child,
  .note,
  p {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  .queue {
    display: flex;
    gap: var(--z-space-2);
    align-items: center;
    color: var(--z-pending);
    text-decoration: none;
    min-width: 0;
  }
  .track {
    height: var(--z-space-2);
    background: var(--z-surface-sunken);
    border-radius: var(--z-radius-full);
    flex: 1;
    min-width: 30px;
    overflow: hidden;
  }
  .track > span {
    display: block;
    height: 100%;
    background: var(--z-pending);
    border-radius: inherit;
  }
  .capacity {
    display: grid;
    text-align: right;
    gap: var(--z-space-1);
  }
  strong {
    font-size: var(--z-text-sm);
    font-variant-numeric: tabular-nums;
  }
  .queue:hover,
  .identity a:hover {
    text-decoration: underline;
    color: var(--z-accent);
  }
  @media (max-width: 768px) {
    .row {
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--z-space-2);
    }
    .queue {
      grid-column: 1;
      grid-row: 2;
    }
    .capacity {
      grid-column: 2;
      grid-row: 1 / span 2;
    }
    .track {
      max-width: 180px;
    }
  }
</style>
