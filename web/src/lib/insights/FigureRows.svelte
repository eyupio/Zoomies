<!--
  The rows under a trend: the legend, the switchboard and the reading at
  once. Each figure's meter shows what it was at the moment under the
  crosshair, or now when nothing is being read, so the exact numbers for a
  moment are never only in a card that follows the pointer -- a card is gone
  the instant the finger lifts, and these stay to be read out.

  A row is also how a figure is taken off the chart, which is why the name is
  a button: with five lines on one chart, "just this one" is the commonest
  thing an operator wants, and reaching for the chips above to get it means
  looking away from the figures.
-->
<script lang="ts" generics="S extends Series">
  import { formatNumber, NO_VALUE } from '$lib/format';
  import Stroke from './Stroke.svelte';
  import type { Series } from './plot';

  let {
    label,
    readings,
    ceiling,
    emphasis,
    moment,
    format = formatNumber,
    empty,
    ontoggle,
    onchip,
  }: {
    /** What the rows are, for the group's accessible name. */
    label: string;
    /** Each drawn figure and what it read at the active moment. */
    readings: ReadonlyArray<{ series: S; value: number | null }>;
    /** The top of the axis, which is what the meters are filled against. */
    ceiling: number;
    /** The figure singled out, from a chip, a row or the line itself. */
    emphasis: S['key'] | null;
    /** When the reading is from: "now", or "at 14:05". */
    moment: string;
    format?: (value: number) => string;
    /** What to say when no figure is on the chart. */
    empty: string;
    ontoggle: (key: S['key']) => void;
    onchip: (key: S['key'] | null) => void;
  } = $props();
</script>

<div class="rows" role="group" aria-label={label}>
  {#each readings as r (r.series.key)}
    <div
      class="row"
      class:lit={emphasis === r.series.key}
      role="presentation"
      onpointerenter={() => onchip(r.series.key)}
      onpointerleave={() => onchip(null)}
      onfocusin={() => onchip(r.series.key)}
      onfocusout={() => onchip(null)}
    >
      <button
        type="button"
        class="toggle"
        aria-pressed="true"
        title={`Hide ${r.series.label.toLowerCase()}`}
        onclick={() => ontoggle(r.series.key)}
      >
        <Stroke series={r.series} />
        <span class="name">{r.series.label}</span>
      </button>
      <span class="track" aria-hidden="true">
        {#if r.value !== null}
          <span
            class="fill"
            style:width="{Math.min(100, (100 * r.value) / Math.max(1, ceiling))}%"
            style:background={r.series.tone}
          ></span>
        {/if}
      </span>
      <span class="value" title={`${r.series.label} ${moment}: ${r.series.hint}`}>
        {#if r.value === null}<span class="gap">{NO_VALUE}</span>{:else}<strong
            >{format(r.value)}</strong
          >{/if}
      </span>
    </div>
  {/each}
  {#if readings.length === 0}
    <p class="empty">{empty}</p>
  {/if}
</div>

<style>
  .rows {
    display: flex;
    flex-direction: column;
    gap: var(--z-nudge-2);
  }
  .row {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    padding: var(--z-nudge-2) var(--z-space-1);
    border-radius: var(--z-radius-sm);
  }
  .row.lit {
    background: var(--z-surface-sunken);
  }
  .toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    flex: none;
    min-width: 11rem;
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-text);
    font-size: var(--z-text-xs);
    text-align: left;
    cursor: pointer;
  }
  .track {
    flex: 1;
    min-width: var(--z-space-8);
    height: var(--z-space-2);
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
    overflow: hidden;
  }
  .fill {
    display: block;
    height: 100%;
    border-radius: var(--z-radius-full);
  }
  .value {
    flex: none;
    min-width: var(--z-space-10);
    font-size: var(--z-text-xs);
    font-variant-numeric: tabular-nums;
    text-align: right;
  }
  .value strong {
    font-weight: var(--z-weight-semibold);
  }
  .gap {
    color: var(--z-text-subtle);
  }
  .empty {
    margin: 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  @media (pointer: coarse) {
    .toggle {
      min-height: var(--z-control-touch);
    }
  }
</style>
