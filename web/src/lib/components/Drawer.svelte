<!--
  The right-hand detail panel. Same focus rules as Dialog: trapped on open,
  restored on close, Escape closes one layer.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from '@lucide/svelte';
  import { layers, lockScroll, pageInert, trapFocus } from '../keys';
  import IconButton from './IconButton.svelte';

  interface Props {
    open?: boolean;
    title: string;
    description?: string;
    width?: 'sm' | 'md' | 'lg';
    /** Drop the body padding, for lists that draw their own full-width rows. */
    flush?: boolean;
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
    flush = false,
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

  let backdrop = $state<HTMLDivElement | null>(null);

  $effect(() => {
    if (!open) return;
    const layer = layers.push('drawer', close);
    const unlock = lockScroll();
    const uninert = pageInert(backdrop);
    return () => {
      layers.remove(layer);
      unlock();
      uninert();
    };
  });
</script>

{#if open}
  <div class="backdrop" bind:this={backdrop}>
    <!-- A div, not a focusable-but-aria-hidden button. See Dialog.svelte. -->
    <div class="scrim" aria-hidden="true" onclick={close}></div>
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
      <div class="body" class:flush>{@render children()}</div>
      {#if footer}<footer>{@render footer()}</footer>{/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    /* Anchored to the window rather than stretched across the document: see
       --z-window-width. This panel is the one that showed what that costs --
       it hangs off the right edge, so a document a few hundred pixels too wide
       took the whole drawer off the side of the screen with it. */
    inset: 0 auto 0 0;
    width: var(--z-window-width);
    z-index: var(--z-layer-drawer);
    display: flex;
    justify-content: flex-end;
  }
  .scrim {
    position: absolute;
    inset: 0;
    border: 0;
    padding: 0;
    background: var(--z-scrim);
    backdrop-filter: blur(var(--z-scrim-blur));
    cursor: default;
  }
  .panel {
    position: relative;
    display: flex;
    flex-direction: column;
    width: 100%;
    height: 100%;
    border-left: var(--z-border-width) solid var(--z-border);
    background: var(--z-surface);
    box-shadow: var(--z-shadow-lg);
    animation: slide var(--z-motion-slow) var(--z-ease);
  }
  .panel:focus {
    outline: none;
  }
  .sm {
    max-width: var(--z-width-drawer-sm);
  }
  .md {
    max-width: var(--z-width-drawer-md);
  }
  .lg {
    max-width: var(--z-width-drawer-lg);
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-5);
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  /* A title is a job's name or an audit action, so it is as long as whoever
     wrote the workflow made it. Without these the heading kept its full width
     and pushed Close off the side of the panel, which on a phone is the only
     way out of the drawer an operator can see. */
  .heading {
    min-width: 0;
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    font-weight: var(--z-weight-semibold);
    overflow-wrap: anywhere;
  }
  header p {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .body {
    flex: 1;
    padding: var(--z-space-5);
    overflow-y: auto;
  }
  .body.flush {
    padding: 0;
  }
  footer {
    display: flex;
    justify-content: flex-end;
    gap: var(--z-space-2);
    padding: var(--z-space-4) var(--z-space-5);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  @keyframes slide {
    from {
      transform: translateX(var(--z-space-4));
      opacity: 0;
    }
  }
  /*
    On a phone the panel is the whole screen, so its bottom edge is the one the
    home indicator sits on. index.html asks for viewport-fit=cover, so the gap
    is honoured here rather than declared and ignored -- the footer holds the
    drawer's actions, and a button half under the indicator is a button that
    takes two goes to press.
  */
  @media (max-width: 768px) {
    .body,
    .body.flush,
    footer {
      padding-bottom: calc(var(--z-space-4) + env(safe-area-inset-bottom, 0px));
    }
  }
</style>
