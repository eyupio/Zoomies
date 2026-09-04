<!--
  A runner's state, wherever it appears in a row.

  The whole point of this component is what happens when the state changes
  underneath somebody who is reading the table: the shape and the colour cross-
  fade and a tint fades out behind them, and **nothing moves**. The cell keeps
  the padding and the border it always had, so a state change never reflows the
  row -- a table that jumps while you are reading it is worse than one that does
  not animate at all (docs/ui-guidelines.md section 1.6).
-->
<script module lang="ts">
  /**
   * How long the tint lingers, taken from the motion token so that anyone who
   * asked for less motion gets none: under `prefers-reduced-motion` the token
   * collapses to 1ms and the tint is gone before it is seen.
   */
  function tintDuration(): number {
    if (typeof document === 'undefined') return 0;
    const raw = getComputedStyle(document.documentElement)
      .getPropertyValue('--z-motion-slow')
      .trim();
    const value = Number.parseFloat(raw);
    if (!Number.isFinite(value)) return 0;
    return raw.endsWith('ms') ? value : value * 1000;
  }
</script>

<script lang="ts">
  import StatusDot from '$lib/components/StatusDot.svelte';
  import type { StatusMeta } from '$lib/status';

  interface Props {
    status: StatusMeta;
    /** Hide the label, for somewhere the label is already on screen. */
    hideLabel?: boolean;
    class?: string;
  }

  let { status, hideLabel = false, class: className = '' }: Props = $props();

  let tinted = $state(false);
  let seen: string | undefined;

  $effect(() => {
    const key = status.key;
    // The first render is not a change; only a genuine transition is worth
    // drawing attention to.
    if (seen === undefined || seen === key) {
      seen = key;
      return;
    }
    seen = key;
    tinted = true;
    const timer = setTimeout(() => (tinted = false), tintDuration());
    return () => clearTimeout(timer);
  });
</script>

<span
  class="cell {className}"
  class:tinted
  style="--cell-colour: {status.colour}; --cell-tint: {status.subtle}"
>
  <StatusDot {status} size="sm" />
  {#if !hideLabel}<span class="label">{status.label}</span>{/if}
</span>

<style>
  .cell {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    /* Padding and border are always here, tint or no tint: this is what keeps
       a state change from moving anything. */
    padding: var(--z-space-1) var(--z-space-2);
    border: 1px solid transparent;
    border-radius: var(--z-radius-sm);
    color: var(--cell-colour);
    white-space: nowrap;
    transition:
      background-color var(--z-motion-slow) var(--z-ease),
      border-color var(--z-motion-slow) var(--z-ease),
      color var(--z-motion-slow) var(--z-ease);
  }
  .cell.tinted {
    background: var(--cell-tint);
    border-color: var(--cell-tint);
  }
  .label {
    font-weight: var(--z-weight-medium);
  }
</style>
