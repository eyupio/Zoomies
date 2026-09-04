<!--
  What this runner is actually using, against what its pool allows it.

  The numbers are the carrier; the bars are there so a runner pinned against
  its memory limit is obvious from across the room. Both arrive on the same SSE
  updates as the state does, so they move on their own.
-->
<script lang="ts">
  import type { Resources } from '$lib/api/types';
  import { formatBytes, formatMegabytes, formatPercent, ratio } from '$lib/format';

  interface Props {
    /** Percentage of one core, as the agent reports it. 200 means two cores. */
    cpuPercent?: number;
    memoryBytes?: number;
    /** The pool's limits, when it sets any. They scale the bars. */
    limits?: Resources;
    class?: string;
  }

  let { cpuPercent, memoryBytes, limits, class: className = '' }: Props = $props();

  const cpuCeiling = $derived((limits?.cpus ?? 0) > 0 ? (limits?.cpus ?? 0) * 100 : 100);
  const cpuShare = $derived(ratio((cpuPercent ?? 0) / cpuCeiling));
  const memoryCeiling = $derived((limits?.memory_mb ?? 0) * 1024 * 1024);
  const memoryShare = $derived(memoryCeiling > 0 ? ratio((memoryBytes ?? 0) / memoryCeiling) : 0);

  const hasCpu = $derived(cpuPercent !== undefined && cpuPercent !== null);
  const hasMemory = $derived(memoryBytes !== undefined && memoryBytes !== null);
</script>

<dl class="resources {className}">
  <div class="row">
    <dt>CPU</dt>
    <dd>
      <span class="value tabular">
        {hasCpu ? formatPercent((cpuPercent ?? 0) / 100, 0) : 'Not reported'}
      </span>
      <span class="ceiling">
        {(limits?.cpus ?? 0) > 0 ? `of ${limits?.cpus} allowed` : 'no limit set'}
      </span>
      {#if hasCpu}
        <div class="track" aria-hidden="true">
          <span class="fill busy" style="width: {(cpuShare * 100).toFixed(1)}%"></span>
        </div>
      {/if}
    </dd>
  </div>

  <div class="row">
    <dt>Memory</dt>
    <dd>
      <span class="value tabular">
        {hasMemory ? formatBytes(memoryBytes ?? 0) : 'Not reported'}
      </span>
      <span class="ceiling">
        {memoryCeiling > 0 ? `of ${formatMegabytes(limits?.memory_mb)} allowed` : 'no limit set'}
      </span>
      {#if hasMemory && memoryCeiling > 0}
        <div class="track" aria-hidden="true">
          <span class="fill pending" style="width: {(memoryShare * 100).toFixed(1)}%"></span>
        </div>
      {/if}
    </dd>
  </div>
</dl>

<style>
  .resources {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    margin: 0;
  }
  .row {
    display: grid;
    grid-template-columns: 5rem 1fr;
    align-items: baseline;
    gap: var(--z-space-3);
  }
  dt {
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-subtle);
  }
  dd {
    margin: 0;
    min-width: 0;
  }
  .value {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .ceiling {
    margin-left: var(--z-space-2);
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .track {
    margin-top: var(--z-space-2);
    height: var(--z-space-1);
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
    overflow: hidden;
  }
  .fill {
    display: block;
    height: 100%;
    border-radius: var(--z-radius-full);
  }
  .fill.busy {
    background: var(--z-busy);
  }
  .fill.pending {
    background: var(--z-pending);
  }
</style>
