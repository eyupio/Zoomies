<!--
  The reading beside the crosshair: every drawn figure at the moment under
  the pointer. It follows the pointer, so it says what the moment is as well
  as what it held -- a card that only carried figures would leave the reader
  to work out which point they were hovering.
-->
<script lang="ts" generics="S extends Series">
  import { formatNumber, NO_VALUE } from '$lib/format';
  import Stroke from './Stroke.svelte';
  import type { Series } from './plot';

  let {
    when,
    note,
    readings,
    emphasis,
    format = formatNumber,
  }: {
    /** The moment being read, already written out. */
    when: string;
    /** What kind of moment it is: "15-minute peaks", "still running". */
    note?: string;
    readings: ReadonlyArray<{ series: S; value: number | null }>;
    emphasis: S['key'] | null;
    format?: (value: number) => string;
  } = $props();
</script>

<div class="reading">
  <p class="when">
    <strong>{when}</strong>
    {#if note}<span>{note}</span>{/if}
  </p>
  <dl>
    {#each readings as r (r.series.key)}
      <div class:lit={emphasis === r.series.key}>
        <dt><Stroke series={r.series} />{r.series.label}</dt>
        <dd>{r.value === null ? NO_VALUE : format(r.value)}</dd>
      </div>
    {/each}
  </dl>
</div>

<style>
  .reading {
    min-width: 12rem;
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-md);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .when {
    display: flex;
    justify-content: space-between;
    gap: var(--z-space-3);
    margin: 0 0 var(--z-space-1);
    padding-bottom: var(--z-space-1);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text-subtle);
  }
  .when strong {
    color: var(--z-text);
    font-variant-numeric: tabular-nums;
  }
  dl {
    display: grid;
    gap: var(--z-nudge-2);
    margin: 0;
  }
  dl div {
    display: flex;
    justify-content: space-between;
    gap: var(--z-space-4);
  }
  dl div.lit {
    font-weight: var(--z-weight-semibold);
  }
  dt {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
    font-variant-numeric: tabular-nums;
  }
</style>
