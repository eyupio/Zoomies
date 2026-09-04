<!--
  The right-hand detail panel. Same focus rules as Dialog: trapped on open,
  restored on close, Escape closes one layer.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from '@lucide/svelte';
  import { layers, lockScroll, trapFocus } from '../keys';
  import IconButton from './IconButton.svelte';

  interface Props {
    open?: boolean;
    title: string;
    description?: string;
    width?: 'sm' | 'md' | 'lg';
    onclose?: () => void;
    footer?: Snippet;
    class?: string;
    children: Snippet;
  }

  let {
    open = $bindable(false),
    title,
    description,
    width = 'md',
    onclose,
    footer,
    class: className = '',
    children,
  }: Props = $props();

  const uid = $props.id();
  const id = `drawer-${uid}`;

  function close(): void {
    if (!open) return;
    open = false;
    onclose?.();
  }

  $effect(() => {
    if (!open) return;
    const layer = layers.push('drawer', close);
    const unlock = lockScroll();
    return () => {
      layers.remove(layer);
      unlock();
    };
  });
</script>

{#if open}
  <div class="backdrop">
    <button type="button" class="scrim" tabindex="-1" aria-hidden="true" onclick={close}></button>
    <div
      class="panel {width} {className}"
      role="dialog"
      aria-modal="true"
      aria-labelledby="{id}-title"
      aria-describedby={description ? `${id}-description` : undefined}
      use:trapFocus
    >
      <header>
        <div class="heading">
          <h2 id="{id}-title">{title}</h2>
          {#if description}<p id="{id}-description">{description}</p>{/if}
        </div>
        <IconButton icon={X} label="Close" size="sm" onclick={close} />
      </header>
      <div class="body">{@render children()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: var(--z-layer-drawer);
    display: flex;
    justify-content: flex-end;
  }
  .scrim {
    position: absolute;
    inset: 0;
    border: 0;
    padding: 0;
    /* A veil made from the page's own ground, so it dims in light and in dark
       without a hard-coded black that only works in one of them. */
    background: color-mix(in srgb, var(--z-bg) 72%, transparent);
    backdrop-filter: blur(3px);
    cursor: default;
  }
  .panel {
    position: relative;
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    border-left: 1px solid var(--z-border);
    background: var(--z-surface);
    box-shadow: var(--z-shadow-lg);
    animation: slide var(--z-motion-slow) var(--z-ease);
  }
  .panel:focus {
    outline: none;
  }
  .sm {
    max-width: 360px;
  }
  .md {
    max-width: 520px;
  }
  .lg {
    max-width: 760px;
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    font-weight: var(--z-weight-semibold);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
  }
  .body {
    flex: 1;
    padding: var(--z-space-5);
    overflow-y: auto;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: var(--z-space-2);
    padding: var(--z-space-4) var(--z-space-5);
    border-top: 1px solid var(--z-border);
  }
  @keyframes slide {
    from {
      transform: translateX(16px);
      opacity: 0;
    }
  }
</style>
