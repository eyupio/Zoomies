<!--
  The shape half of the state encoding.

  Colour is never the only carrier of meaning, so every status is also a
  distinct outline: filled for busy, hollow for idle, dashed for provisioning,
  slashed for draining, a triangle for failed, a square for terminal. The shapes
  are legible in greyscale and from across a room, which is the actual test.
-->
<script lang="ts">
  import type { StatusMeta } from '../status';

  interface Props {
    status: StatusMeta;
    size?: 'sm' | 'md';
    /** Print the label after the shape. */
    showLabel?: boolean;
    class?: string;
  }

  let { status, size = 'md', showLabel = false, class: className = '' }: Props = $props();

  const px = $derived(size === 'sm' ? 8 : 10);
</script>

<span class="dot-wrap {className}" title={status.hint}>
  <svg
    width={px}
    height={px}
    viewBox="0 0 10 10"
    role="img"
    aria-label={showLabel ? undefined : status.label}
    aria-hidden={showLabel ? 'true' : undefined}
    style="color: {status.colour}"
  >
    {#if status.shape === 'filled'}
      <circle cx="5" cy="5" r="4" fill="currentColor" />
    {:else if status.shape === 'hollow'}
      <circle cx="5" cy="5" r="3.4" fill="none" stroke="currentColor" stroke-width="1.6" />
    {:else if status.shape === 'dashed'}
      <circle
        cx="5"
        cy="5"
        r="3.4"
        fill="none"
        stroke="currentColor"
        stroke-width="1.6"
        stroke-dasharray="2.2 1.8"
        stroke-linecap="round"
      />
    {:else if status.shape === 'slash'}
      <circle cx="5" cy="5" r="3.4" fill="none" stroke="currentColor" stroke-width="1.6" />
      <line x1="2.2" y1="7.8" x2="7.8" y2="2.2" stroke="currentColor" stroke-width="1.6" />
    {:else if status.shape === 'triangle'}
      <path
        d="M5 0.8 L9.4 8.6 H0.6 Z"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linejoin="round"
      />
    {:else}
      <rect x="1.4" y="1.4" width="7.2" height="7.2" rx="1.2" fill="currentColor" opacity="0.85" />
    {/if}
  </svg>
  {#if showLabel}<span class="label">{status.label}</span>{/if}
</span>

<style>
  .dot-wrap {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    white-space: nowrap;
  }
  svg {
    flex: none;
    display: block;
  }
  .label {
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
</style>
