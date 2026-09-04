<!--
  A status pill. Colour and shape both, always, from the state map in
  lib/status.ts. Never hand-write a colour here.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { StatusMeta, StatusTone } from '../status';
  import StatusDot from './StatusDot.svelte';

  interface Props {
    /** Preferred: the entry from the state map, carrying colour, shape and label. */
    status?: StatusMeta;
    /** For labels that are not a status: a count, a role, a backend name. */
    tone?: StatusTone | 'accent';
    label?: string;
    size?: 'sm' | 'md';
    /** Draw the shape. On by default whenever a status is given. */
    dot?: boolean;
    title?: string;
    class?: string;
    children?: Snippet;
  }

  let {
    status,
    tone,
    label,
    size = 'md',
    dot = true,
    title,
    class: className = '',
    children,
  }: Props = $props();

  const resolvedTone = $derived(tone ?? status?.tone ?? 'neutral');
  const text = $derived(label ?? status?.label ?? '');
</script>

<span
  class="badge {size} {className}"
  data-tone={resolvedTone}
  title={title ?? status?.hint}
  style="--badge-colour: var(--z-{resolvedTone}); --badge-subtle: var(--z-{resolvedTone}-subtle); --badge-border: var(--z-{resolvedTone}-border)"
>
  {#if status && dot}<StatusDot {status} size="sm" />{/if}
  {#if children}{@render children()}{:else}{text}{/if}
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    border: 1px solid var(--badge-border);
    border-radius: var(--z-radius-sm);
    background: var(--badge-subtle);
    color: var(--badge-colour);
    font-weight: var(--z-weight-medium);
    white-space: nowrap;
  }
  .md {
    height: var(--z-space-5);
    padding: 0 var(--z-space-2);
    font-size: var(--z-text-xs);
  }
  .sm {
    height: var(--z-space-4);
    padding: 0 var(--z-space-1) 0 var(--z-space-2);
    font-size: var(--z-text-2xs);
  }
  .badge[data-tone='accent'] {
    background: var(--z-accent-subtle);
    border-color: var(--z-accent-border);
    color: var(--z-accent);
  }
</style>
