<!--
  What the scheduler has decided lately, in the scheduler's own words.

  The reason string is printed verbatim -- "scaled linux-x64 2 -> 4: 3 jobs
  queued > 30s" -- because paraphrasing the one sentence that explains why a
  runner exists is how a dashboard stops being trustworthy.
-->
<script lang="ts">
  import { History, Minus, TrendingDown, TrendingUp } from '@lucide/svelte';
  import type { LucideIcon } from '@lucide/svelte';
  import type { ScalingEvent } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toMillis } from '$lib/format';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Panel from './Panel.svelte';

  interface Props {
    loading?: boolean;
    class?: string;
  }

  let { loading = false, class: className = '' }: Props = $props();

  /** How many decisions fit on a dashboard before it becomes a log viewer. */
  const SHOWN = 10;

  interface Decision {
    key: string;
    poolId: string | undefined;
    poolName: string;
    from: number | undefined;
    to: number | undefined;
    reason: string;
    at: string | undefined;
    direction: 'up' | 'down' | 'hold';
    label: string;
    icon: LucideIcon;
  }

  function decide(event: ScalingEvent, index: number): Decision {
    const from = event.from;
    const to = event.to;
    const direction =
      from === undefined || to === undefined || to === from ? 'hold' : to > from ? 'up' : 'down';
    return {
      key: event.id ?? `${event.pool_id ?? ''}:${event.created_at ?? ''}:${index}`,
      poolId: event.pool_id,
      poolName: event.pool_name ?? 'Unnamed pool',
      from,
      to,
      reason: event.reason ?? '',
      at: event.created_at,
      direction,
      label: direction === 'up' ? 'Scaled up' : direction === 'down' ? 'Scaled down' : 'Held',
      icon: direction === 'up' ? TrendingUp : direction === 'down' ? TrendingDown : Minus,
    };
  }

  const decisions = $derived(
    [...fleet.scalingEvents]
      .sort((a, b) => (toMillis(b.created_at) ?? 0) - (toMillis(a.created_at) ?? 0))
      .slice(0, SHOWN)
      .map(decide),
  );

  const canCreate = $derived(session.can('operator'));
  const hasPools = $derived(fleet.pools.length > 0);
</script>

<Panel title="Recent scaling" description="Newest first." class={className} flush>
  {#if loading}
    <p class="sr-only">Loading recent scaling decisions.</p>
    <ul class="feed" aria-hidden="true">
      {#each [0, 1, 2, 3] as row (row)}
        <li class="item">
          <span class="mark"></span>
          <div class="lines">
            <Skeleton width="60%" height="var(--z-text-xs)" />
            <Skeleton width="90%" height="var(--z-text-sm)" />
          </div>
        </li>
      {/each}
    </ul>
  {:else if decisions.length === 0}
    <EmptyState
      icon={History}
      compact
      title="No scaling decisions yet"
      description="The scheduler writes a line here every time it creates or removes runners, and says why."
    >
      {#if canCreate && !hasPools}
        <Button variant="primary" href="/pools/new">Create a pool</Button>
      {/if}
    </EmptyState>
  {:else}
    <ul class="feed">
      {#each decisions as decision (decision.key)}
        <li class="item">
          <span class="mark" data-direction={decision.direction}>
            <decision.icon size={14} aria-hidden="true" />
          </span>
          <div class="lines">
            <p class="meta">
              <span class="direction">{decision.label}</span>
              {#if decision.from !== undefined && decision.to !== undefined}
                <span class="count tabular">{decision.from} → {decision.to}</span>
              {/if}
              <span aria-hidden="true" class="dot">·</span>
              {#if decision.poolId}
                <a href="/pools/{decision.poolId}">{decision.poolName}</a>
              {:else}
                <span>{decision.poolName}</span>
              {/if}
              <span aria-hidden="true" class="dot">·</span>
              <RelativeTime value={decision.at} />
            </p>
            {#if decision.reason}<p class="reason">{decision.reason}</p>{/if}
          </div>
        </li>
      {/each}
    </ul>
  {/if}
</Panel>

<style>
  .feed {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .item {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  .item:last-child {
    border-bottom: 0;
  }
  .mark {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex: none;
    width: var(--z-space-6);
    height: var(--z-space-6);
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
  }
  .lines {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-1) var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .direction {
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .count {
    color: var(--z-text-muted);
  }
  .dot {
    color: var(--z-text-subtle);
  }
  .meta a {
    color: var(--z-accent);
    text-decoration: none;
  }
  .meta a:hover {
    text-decoration: underline;
  }
  .reason {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
</style>
