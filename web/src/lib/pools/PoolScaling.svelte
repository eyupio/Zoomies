<!--
  What the scheduler decided, in its own words. The reason string is shown
  verbatim: it is the one place an operator can see why a pool grew or shrank,
  and paraphrasing it would throw that away.
-->
<script lang="ts">
  import { ArrowRight, TrendingUp } from '@lucide/svelte';
  import type { ScalingEvent } from '$lib/api/types';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    events: readonly ScalingEvent[];
    loading?: boolean;
    error?: unknown;
    onretry?: () => void;
    class?: string;
  }

  let { events, loading = false, error, onretry, class: className = '' }: Props = $props();

  function direction(event: ScalingEvent): 'up' | 'down' | 'flat' {
    const from = event.from ?? 0;
    const to = event.to ?? 0;
    if (to > from) return 'up';
    if (to < from) return 'down';
    return 'flat';
  }
</script>

<div class={className}>
  {#if error}
    <ErrorState {error} {onretry} compact />
  {:else if loading && events.length === 0}
    <ul class="list">
      {#each Array.from({ length: 3 }, (_, i) => i) as line (line)}
        <li class="row"><Skeleton width="80%" height="1rem" /></li>
      {/each}
    </ul>
  {:else if events.length === 0}
    <EmptyState
      compact
      icon={TrendingUp}
      title="No scaling decisions yet"
      description="The scheduler records a line here every time it changes how many runners this pool has."
    />
  {:else}
    <ul class="list">
      {#each events as event (event.id)}
        <li class="row" data-direction={direction(event)}>
          <span class="counts tabular">
            <span class="from">{event.from ?? 0}</span>
            <ArrowRight size={12} aria-hidden="true" />
            <span class="to">{event.to ?? 0}</span>
          </span>
          <span class="reason">{event.reason ?? 'No reason was recorded.'}</span>
          <span class="when"><RelativeTime value={event.created_at} plain /></span>
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .list {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .row {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: baseline;
    gap: var(--z-space-3);
    padding: var(--z-space-2) 0;
    border-bottom: 1px solid var(--z-border);
    font-size: var(--z-text-sm);
  }
  .row:last-child {
    border-bottom: 0;
  }
  .counts {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    padding: 0 var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .row[data-direction='up'] .to {
    color: var(--z-busy);
    font-weight: var(--z-weight-semibold);
  }
  .row[data-direction='down'] .to {
    color: var(--z-draining);
    font-weight: var(--z-weight-semibold);
  }
  .reason {
    min-width: 0;
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .when {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    white-space: nowrap;
  }
</style>
