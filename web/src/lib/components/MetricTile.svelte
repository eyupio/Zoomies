<!--
  One number, what it means, and the last hour of it. Four of these are the top
  of the Overview.
-->
<script lang="ts">
  import { ArrowDown, ArrowUp, Minus } from '@lucide/svelte';
  import type { StatusTone } from '../status';
  import Skeleton from './Skeleton.svelte';
  import Sparkline from './Sparkline.svelte';

  interface Props {
    label: string;
    value: string | number;
    /** "s", "jobs" -- rendered small, after the number. */
    unit?: string;
    /** Change over the window. Positive is not automatically good, so pass `goodWhen`. */
    delta?: number;
    deltaLabel?: string;
    /** Which direction is the healthy one, so the colour is never misleading. */
    goodWhen?: 'up' | 'down' | 'either';
    sparkline?: readonly number[];
    tone?: StatusTone;
    href?: string;
    loading?: boolean;
    /** One line under the number, for context the number cannot carry. */
    hint?: string;
    class?: string;
  }

  let {
    label,
    value,
    unit,
    delta,
    deltaLabel,
    goodWhen = 'either',
    sparkline,
    tone = 'busy',
    href,
    loading = false,
    hint,
    class: className = '',
  }: Props = $props();

  const direction = $derived(
    delta === undefined || delta === 0 ? 'flat' : delta > 0 ? 'up' : 'down',
  );
  const deltaTone = $derived(
    goodWhen === 'either' || direction === 'flat'
      ? 'neutral'
      : direction === goodWhen
        ? 'good'
        : 'bad',
  );
</script>

<svelte:element
  this={href ? 'a' : 'div'}
  {href}
  class="tile {className}"
  class:link={Boolean(href)}
>
  <p class="label">{label}</p>
  {#if loading}
    <Skeleton width="4rem" height="var(--z-text-2xl)" />
  {:else}
    <p class="value tabular">
      {value}{#if unit}<span class="unit">{unit}</span>{/if}
    </p>
  {/if}
  <div class="foot">
    {#if delta !== undefined && !loading}
      <span class="delta" data-tone={deltaTone}>
        {#if direction === 'up'}<ArrowUp size={12} aria-hidden="true" />
        {:else if direction === 'down'}<ArrowDown size={12} aria-hidden="true" />
        {:else}<Minus size={12} aria-hidden="true" />{/if}
        {Math.abs(delta)}{deltaLabel ? ` ${deltaLabel}` : ''}
      </span>
    {/if}
    {#if hint}<span class="hint">{hint}</span>{/if}
    {#if sparkline && sparkline.length > 0}
      <span class="spark"
        ><Sparkline values={sparkline} {label} {tone} width={96} height={26} /></span
      >
    {/if}
  </div>
</svelte:element>

<style>
  .tile {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
    padding: var(--z-space-5);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    text-decoration: none;
    color: inherit;
    min-width: 0;
  }
  .tile.link:hover {
    border-color: var(--z-border-strong);
    background: var(--z-surface-hover);
  }
  .label {
    margin: 0;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .value {
    margin: 0;
    font-size: var(--z-text-2xl);
    line-height: var(--z-leading-2xl);
    font-weight: var(--z-weight-bold);
    color: var(--z-text);
    font-variant-numeric: tabular-nums;
  }
  .unit {
    margin-left: 2px;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
  }
  .foot {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    min-height: var(--z-space-6);
  }
  .delta {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    font-size: var(--z-text-xs);
    font-variant-numeric: tabular-nums;
    color: var(--z-text-muted);
  }
  .delta[data-tone='good'] {
    color: var(--z-idle);
  }
  .delta[data-tone='bad'] {
    color: var(--z-danger);
  }
  .hint {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  .spark {
    margin-left: auto;
  }
</style>
