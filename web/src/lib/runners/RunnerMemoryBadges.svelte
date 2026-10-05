<!--
  The small badges for one runner: what the memory valve has done for it, and
  which of its folders are in memory.

  Nothing is drawn for a runner with nothing to say, so a pool that uses neither
  keeps the grid exactly as it was. The runner is read from the fleet's cache by
  the caller, so a pill appears the moment the event that changes it lands.
-->
<script lang="ts">
  import type { MemoryResourceState, RunnerScratch } from '$lib/api/types';
  import MemoryBadgePill from './MemoryBadgePill.svelte';
  import { memoryBadges } from './memory-badges';

  interface Props {
    runner: { memory_resource?: MemoryResourceState; scratch?: RunnerScratch };
    /** Say so when the valve is on and has done nothing, which the grid does not. */
    quiet?: boolean;
    placement?: 'top' | 'bottom';
    /** Wrap onto another line, flush right, where the room is a table cell's. */
    wrap?: boolean;
  }

  let { runner, quiet = false, placement = 'bottom', wrap = false }: Props = $props();

  const badges = $derived(memoryBadges(runner, { quiet }));
</script>

{#if badges.length > 0}
  <span class="badges" class:wrap data-testid="memory-badges">
    {#each badges as badge (badge.key)}
      <MemoryBadgePill {badge} {placement} />
    {/each}
  </span>
{/if}

<style>
  .badges {
    display: inline-flex;
    flex-wrap: nowrap;
    align-items: center;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .badges.wrap {
    flex-wrap: wrap;
    justify-content: flex-end;
  }
</style>
