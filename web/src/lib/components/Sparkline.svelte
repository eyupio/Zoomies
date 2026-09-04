<!--
  A sparkline, hand-written. There is no chart library in Zoomies: an operator
  dashboard cannot afford 90 KB of one to draw sixty points.

  It carries `role="img"` and a sentence describing the trend, because a line
  with no text is invisible to half the ways people read a dashboard.
-->
<script lang="ts">
  import type { StatusTone } from '../status';
  import { formatNumber } from '../format';

  interface Props {
    values: readonly number[];
    /** What is being plotted: "Queued jobs". Used to build the description. */
    label: string;
    tone?: StatusTone;
    width?: number;
    height?: number;
    /** Shade the area under the line. */
    fill?: boolean;
    class?: string;
  }

  let {
    values,
    label,
    tone = 'busy',
    width = 120,
    height = 32,
    fill = true,
    class: className = '',
  }: Props = $props();

  const padding = 2;

  const points = $derived.by(() => {
    if (values.length === 0) return [] as Array<{ x: number; y: number }>;
    const max = Math.max(...values, 0);
    const min = Math.min(...values, 0);
    const span = max - min || 1;
    const stepX = values.length > 1 ? (width - padding * 2) / (values.length - 1) : 0;
    return values.map((v, i) => ({
      x: padding + i * stepX,
      y: height - padding - ((v - min) / span) * (height - padding * 2),
    }));
  });

  const line = $derived(
    points.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)} ${p.y.toFixed(1)}`).join(' '),
  );

  const area = $derived(
    points.length > 1
      ? `${line} L${(points[points.length - 1]?.x ?? 0).toFixed(1)} ${height} L${(points[0]?.x ?? 0).toFixed(1)} ${height} Z`
      : '',
  );

  const last = $derived(values.length ? (values[values.length - 1] ?? 0) : 0);
  const first = $derived(values.length ? (values[0] ?? 0) : 0);
  const peak = $derived(values.length ? Math.max(...values) : 0);

  const description = $derived(
    values.length === 0
      ? `${label}: no samples yet`
      : `${label}: ${formatNumber(last)} now, ${formatNumber(first)} at the start of the window, peaking at ${formatNumber(peak)}`,
  );
</script>

<svg
  class="spark {className}"
  {width}
  {height}
  viewBox="0 0 {width} {height}"
  preserveAspectRatio="none"
  role="img"
  aria-label={description}
  style="color: var(--z-{tone})"
>
  {#if points.length > 1}
    {#if fill}
      <path d={area} fill="currentColor" opacity="0.12" />
    {/if}
    <path
      d={line}
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
    <circle
      cx={points[points.length - 1]?.x}
      cy={points[points.length - 1]?.y}
      r="1.8"
      fill="currentColor"
    />
  {:else}
    <line
      x1={padding}
      y1={height / 2}
      x2={width - padding}
      y2={height / 2}
      stroke="var(--z-border)"
      stroke-width="1.5"
      stroke-dasharray="3 3"
    />
  {/if}
</svg>

<style>
  .spark {
    display: block;
    overflow: visible;
  }
</style>
