<!--
  Busy against live, with the pool's floor and ceiling marked, so a pool pinned
  at its maximum is obvious without reading any numbers.
-->
<script lang="ts">
  import { formatNumber, formatPercent } from '../format';

  interface Props {
    busy: number;
    live: number;
    /** The pool's `min_runners`, drawn as a tick. */
    min?: number;
    /** The pool's `max_runners`. Also the scale, when it is set. */
    max?: number;
    label?: string;
    /** Show the "3 of 4 busy" line under the bar. */
    showText?: boolean;
    height?: number;
    class?: string;
  }

  let {
    busy,
    live,
    min,
    max,
    label = 'Utilisation',
    showText = true,
    height = 8,
    class: className = '',
  }: Props = $props();

  const scale = $derived(Math.max(max ?? 0, live, 1));
  const busyPct = $derived(Math.min(100, (busy / scale) * 100));
  const livePct = $derived(Math.min(100, (live / scale) * 100));
  const minPct = $derived(min === undefined ? null : Math.min(100, (min / scale) * 100));
  const maxPct = $derived(max === undefined ? null : Math.min(100, (max / scale) * 100));
  const atCeiling = $derived(max !== undefined && max > 0 && live >= max);

  const summary = $derived(
    `${label}: ${formatNumber(busy)} of ${formatNumber(live)} runners busy` +
      (max !== undefined
        ? `, ceiling ${formatNumber(max)}${atCeiling ? ', at the ceiling' : ''}`
        : '') +
      (min !== undefined ? `, floor ${formatNumber(min)}` : ''),
  );
</script>

<div class="bar-wrap {className}">
  <div class="track" style="height: {height}px" role="img" aria-label={summary}>
    <span class="live" style="width: {livePct}%"></span>
    <span class="busy" style="width: {busyPct}%"></span>
    {#if minPct !== null}
      <span class="tick min" style="left: {minPct}%"></span>
    {/if}
    {#if maxPct !== null}
      <span class="tick max" class:reached={atCeiling} style="left: {maxPct}%"></span>
    {/if}
  </div>
  {#if showText}
    <p class="text tabular">
      <strong>{formatNumber(busy)}</strong> of {formatNumber(live)} busy
      {#if live > 0}<span class="muted">· {formatPercent(busy / live)}</span>{/if}
      {#if atCeiling}<span class="ceiling">· at the ceiling</span>{/if}
    </p>
  {/if}
</div>

<style>
  .bar-wrap {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .track {
    position: relative;
    width: 100%;
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
    overflow: hidden;
  }
  .live,
  .busy {
    position: absolute;
    inset-block: 0;
    left: 0;
    border-radius: var(--z-radius-full);
  }
  .live {
    background: var(--z-idle-subtle);
    border: 1px solid var(--z-idle-border);
  }
  .busy {
    background: var(--z-busy);
  }
  .tick {
    position: absolute;
    inset-block: -2px;
    width: 2px;
    background: var(--z-text-subtle);
    transform: translateX(-1px);
  }
  .tick.max {
    background: var(--z-border-strong);
  }
  .tick.max.reached {
    background: var(--z-pending);
    width: 3px;
  }
  .text {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    font-variant-numeric: tabular-nums;
  }
  .text strong {
    color: var(--z-text);
    font-weight: var(--z-weight-semibold);
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .ceiling {
    color: var(--z-pending);
  }
</style>
