<!--
  How a runner spent its life: one row per state, with how long it stayed there.

  The controller reconstructs this from the four timestamps a runner row
  carries, so it is a summary rather than an audit trail -- a runner that went
  idle, busy and idle again shows only the most recent of those. The panel says
  so; pretending otherwise would have an operator drawing conclusions the data
  cannot support.
-->
<script lang="ts">
  import type { TimelineEntry } from '$lib/api/types';
  import { ratio } from '$lib/format';
  import { runnerStatus } from '$lib/status';
  import Duration from '$lib/components/Duration.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';

  interface Props {
    entries: readonly TimelineEntry[];
    /** True while the runner is still going, so the last row keeps counting. */
    running?: boolean;
    class?: string;
  }

  let { entries, running = false, class: className = '' }: Props = $props();

  const total = $derived(
    entries.reduce((sum, entry) => sum + Math.max(0, entry.duration_ms ?? 0), 0),
  );

  function share(entry: TimelineEntry): number {
    if (total <= 0) return 0;
    return ratio((entry.duration_ms ?? 0) / total);
  }
</script>

{#if entries.length === 0}
  <EmptyState
    compact
    title="No history yet"
    description="The controller records a runner's states as it moves through them."
  />
{:else}
  <ol class="timeline {className}">
    {#each entries as entry, index (`${entry.state}-${entry.at}-${index}`)}
      {@const status = runnerStatus(entry.state)}
      {@const last = index === entries.length - 1}
      <li style="--entry-colour: {status.colour}; --entry-tint: {status.subtle}">
        <span class="marker" aria-hidden="true"><StatusDot {status} size="sm" /></span>
        <div class="body">
          <div class="head">
            <span class="state">{status.label}</span>
            <span class="spent tabular">
              {#if last && running}
                <Duration from={entry.at} live />
                <span class="still">so far</span>
              {:else}
                <Duration ms={entry.duration_ms ?? null} />
              {/if}
            </span>
            <RelativeTime value={entry.at} class="when" />
          </div>
          {#if entry.message}<p class="message">{entry.message}</p>{/if}
          <div class="bar" aria-hidden="true">
            <span class="fill" style="width: {(share(entry) * 100).toFixed(1)}%"></span>
          </div>
        </div>
      </li>
    {/each}
  </ol>
{/if}

<style>
  .timeline {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  li {
    position: relative;
    display: flex;
    gap: var(--z-space-3);
    padding-bottom: var(--z-space-4);
  }
  li:last-child {
    padding-bottom: 0;
  }
  /* The thread between the states. It stops at the last one. */
  li:not(:last-child)::before {
    content: '';
    position: absolute;
    left: 4px;
    top: var(--z-space-4);
    bottom: 0;
    width: 1px;
    background: var(--z-border);
  }
  .marker {
    display: flex;
    align-items: center;
    height: var(--z-leading-sm);
    flex: none;
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .head {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: var(--z-space-2) var(--z-space-3);
  }
  .state {
    font-weight: var(--z-weight-medium);
    color: var(--entry-colour);
  }
  .spent {
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .still {
    color: var(--z-text-subtle);
  }
  .head :global(.when) {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    margin-left: auto;
  }
  .message {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .bar {
    margin-top: var(--z-space-2);
    height: var(--z-space-1);
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
    overflow: hidden;
  }
  .fill {
    display: block;
    height: 100%;
    min-width: 2px;
    border-radius: var(--z-radius-full);
    background: var(--entry-colour);
  }
</style>
