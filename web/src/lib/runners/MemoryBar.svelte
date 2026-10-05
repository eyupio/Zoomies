<!--
  A runner's memory as one bar: what it was created with, what it was lent, the
  swap it may use past that, and where the most it may hold falls.

  The figures beside it are the carrier and the bar is there to be taken in at a
  glance -- a runner that has used all its headroom is a bar with its tick at the
  end of the fill -- so it is hidden from assistive technology.

  Everything is drawn in a scale that fits the largest of the three, so a runner
  on swap past its ceiling is still wholly on the bar rather than cut off at it.
-->
<script lang="ts">
  import type { BadgeBar } from './memory-badges';

  let { bar }: { bar: BadgeBar } = $props();

  const scale = $derived(Math.max(bar.ceiling, bar.guaranteed + bar.lent + bar.swap, 1));
  const pct = (mb: number) => `${Math.min(Math.max((mb / scale) * 100, 0), 100).toFixed(2)}%`;
  const held = $derived(bar.guaranteed + bar.lent);
</script>

<span class="bar" aria-hidden="true">
  <span class="track">
    <!-- The fills are clipped to the rounded track; the tick is not, because it
         stands proud of it. -->
    <span class="fills">
      <span class="seg base" style:left="0" style:width={pct(bar.guaranteed)}></span>
      {#if bar.lent > 0}
        <span class="seg lent" style:left={pct(bar.guaranteed)} style:width={pct(bar.lent)}></span>
      {/if}
      {#if bar.swap > 0}
        <span class="seg swap" style:left={pct(held)} style:width={pct(bar.swap)}></span>
      {/if}
    </span>
    <span class="tick" style:left={pct(bar.ceiling)}></span>
  </span>
  <span class="legend">
    <span class="key"><i class="swatch base"></i>Guaranteed</span>
    {#if bar.lent > 0}<span class="key"><i class="swatch lent"></i>Lent</span>{/if}
    {#if bar.swap > 0}<span class="key"><i class="swatch swap"></i>Swap</span>{/if}
    <span class="key"><i class="swatch tick-key"></i>Ceiling</span>
  </span>
</span>

<style>
  .bar {
    display: grid;
    gap: var(--z-space-2);
  }
  .track {
    position: relative;
    height: var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
  }
  .fills {
    position: absolute;
    inset: 0;
    overflow: hidden;
    border-radius: var(--z-radius-full);
  }
  .seg {
    position: absolute;
    top: 0;
    bottom: 0;
    min-width: 0;
  }
  .base {
    background: var(--z-neutral);
  }
  .lent {
    background: var(--z-accent);
  }
  /* Swap is memory that is not memory: hatched, so it cannot be mistaken for the
     solid fill beside it even where a screen has lost the colour. */
  .swap {
    background: repeating-linear-gradient(
      135deg,
      var(--z-pending) 0 var(--z-nudge-2),
      transparent var(--z-nudge-2) var(--z-nudge-3)
    );
  }
  .tick {
    position: absolute;
    top: calc(-1 * var(--z-nudge-2));
    bottom: calc(-1 * var(--z-nudge-2));
    width: var(--z-nudge-2);
    margin-left: calc(-1 * var(--z-nudge-1));
    border-radius: var(--z-radius-full);
    background: var(--z-text);
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-3);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
  }
  .key {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
  }
  .swatch {
    display: inline-block;
    width: var(--z-space-2);
    height: var(--z-space-2);
    border-radius: var(--z-radius-full);
  }
  .swatch.base {
    background: var(--z-neutral);
  }
  .swatch.lent {
    background: var(--z-accent);
  }
  .swatch.swap {
    background: repeating-linear-gradient(
      135deg,
      var(--z-pending) 0 var(--z-nudge-1),
      transparent var(--z-nudge-1) var(--z-nudge-2)
    );
    border: var(--z-border-width) solid var(--z-pending);
  }
  .swatch.tick-key {
    width: var(--z-nudge-2);
    height: var(--z-space-3);
    border-radius: var(--z-radius-full);
    background: var(--z-text);
  }
</style>
