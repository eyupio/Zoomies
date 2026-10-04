<!--
  What this runner is actually using, against what it was allowed.

  The numbers are the carrier; the bars are there so a runner pinned against
  its memory limit is obvious from across the room. Both arrive on the same SSE
  updates as the state does, so they move on their own.

  The ceiling is the allocation the runner was created with, which is the
  pool's own limits or one slot's share of its host when the pool sets none.
  It is on the runner because it is a fact about the container, not the pool:
  a pool whose limits changed last week still has runners on the old ones, and
  an OOM kill on a defaulted share points at the host's capacity rather than at
  a pool field nobody set.
-->
<script lang="ts">
  import type { BackendKind, HostThrottle, Resources } from '$lib/api/types';
  import { formatBytes, formatMegabytes, formatNumber, formatPercent, ratio } from '$lib/format';
  import { throttleCpuPercent, throttled, MAX_THROTTLE_LEVEL } from '$lib/status';
  import { allocationWords } from './allocation';

  interface Props {
    /** Percentage of one core, as the agent reports it. 200 means two cores. */
    cpuPercent?: number;
    memoryBytes?: number;
    /** The pool's limits, when it sets any. The fallback ceiling for a runner from before allocations were recorded. */
    limits?: Resources;
    /** What the runner's workload was created with, and where that came from. */
    allocatedCpus?: number;
    allocatedMemoryMb?: number;
    allocationSource?: string;
    /** The throttle its host is on, if any: it is what the CPU quota is running under. */
    hostThrottle?: HostThrottle | null;
    /** The backend its pool runs on. A `process` runner has no container, so no quota for a throttle to lower. */
    backend?: BackendKind;
    class?: string;
  }

  let {
    cpuPercent,
    memoryBytes,
    limits,
    allocatedCpus,
    allocatedMemoryMb,
    allocationSource,
    hostThrottle,
    backend,
    class: className = '',
  }: Props = $props();

  // The runner's own allocation wins over the pool's current limits: it is
  // what the container was given, and the pool may have changed since.
  const cpuLimit = $derived(allocatedCpus || limits?.cpus || 0);
  const memoryLimit = $derived(allocatedMemoryMb || limits?.memory_mb || 0);

  const cpuCeiling = $derived(cpuLimit > 0 ? cpuLimit * 100 : 100);
  const cpuShare = $derived(ratio((cpuPercent ?? 0) / cpuCeiling));
  const memoryCeiling = $derived(memoryLimit * 1024 * 1024);
  const memoryShare = $derived(memoryCeiling > 0 ? ratio((memoryBytes ?? 0) / memoryCeiling) : 0);

  const hasCpu = $derived(cpuPercent !== undefined && cpuPercent !== null);
  const hasMemory = $derived(memoryBytes !== undefined && memoryBytes !== null);

  /** "1.87 CPU · 3.9 GB, the host's default share": what it got, and why that figure. */
  const allocation = $derived(allocationWords(allocatedCpus, allocatedMemoryMb, allocationSource));

  const isThrottled = $derived(throttled(hostThrottle));
  // Whether the throttle reaches this runner. The agent lowers only a quota it
  // was told about -- the allocation recorded with the container -- and a
  // process-backend runner has no container at all. The pool's current limits
  // are deliberately not consulted here: they are the bars' fallback ceiling,
  // but a runner from before allocations were recorded carries none, and the
  // agent leaves it alone whatever its pool says now.
  const throttleReaches = $derived((allocatedCpus ?? 0) > 0 && backend !== 'process');
  // A runner on a pool limit from before allocations were recorded: limited,
  // but not by a figure the throttle can move.
  const unrecorded = $derived(
    !((allocatedCpus ?? 0) > 0) && backend !== 'process' && (limits?.cpus ?? 0) > 0,
  );
</script>

<dl class="resources {className}">
  <div class="row">
    <dt>CPU</dt>
    <dd>
      <span class="value tabular">
        {hasCpu ? formatPercent((cpuPercent ?? 0) / 100, 0) : 'Not reported'}
      </span>
      <span class="ceiling">
        {cpuLimit > 0 ? `of ${formatNumber(cpuLimit)} allowed` : 'no limit set'}
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
        {memoryCeiling > 0 ? `of ${formatMegabytes(memoryLimit)} allowed` : 'no limit set'}
      </span>
      {#if hasMemory && memoryCeiling > 0}
        <div class="track" aria-hidden="true">
          <span class="fill pending" style="width: {(memoryShare * 100).toFixed(1)}%"></span>
        </div>
      {/if}
    </dd>
  </div>

  {#if allocation}
    <div class="row">
      <dt>Allocation</dt>
      <dd>
        <span class="value allocation tabular">{allocation}</span>
      </dd>
    </div>
  {/if}
</dl>

{#if isThrottled}
  <!-- The host's rung is what the CPU figure above is running under. A runner
       the throttle cannot reach -- no recorded allocation, or no container to
       hold a quota -- is not being slowed, and saying so is what stops
       "throttled" reading as "this job is being slowed". -->
  <p class="throttled" data-testid="runner-throttle">
    {#if throttleReaches}
      Its host is throttled (step {hostThrottle?.level} of {MAX_THROTTLE_LEVEL}), so its CPU
      allocation is throttled to {throttleCpuPercent(hostThrottle)}% while that stands.
    {:else if unrecorded}
      Its host is throttled, but this runner was created before allocations were recorded, so the
      throttle does not reach it; it runs at its pool's limit.
    {:else}
      Its host is throttled, but this runner has no CPU limit to lower, so it runs at full speed.
    {/if}
  </p>
{/if}

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
    letter-spacing: var(--z-tracking-wide);
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
  .allocation {
    font-size: var(--z-text-sm);
  }
  .throttled {
    margin: var(--z-space-4) 0 0;
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
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
