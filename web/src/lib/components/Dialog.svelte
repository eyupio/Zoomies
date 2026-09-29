<!--
  A modal dialog.

  Focus moves in on open and returns to whatever opened it on close. Escape
  closes exactly one layer, via the shared stack in lib/keys.ts. A backdrop
  click closes only when the dialog is not destructive -- losing a half-typed
  confirmation because the mouse slipped is a bad afternoon.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from '@lucide/svelte';
  import { layers, lockScroll, pageInert, trapFocus } from '../keys';
  import IconButton from './IconButton.svelte';

  interface Props {
    open?: boolean;
    title: string;
    /** One line under the title. Becomes the dialog's accessible description. */
    description?: string;
    size?: 'sm' | 'md' | 'lg';
    /** Backdrop click and the close button. Off for destructive confirmations. */
    dismissible?: boolean;
    onclose?: () => void;
    /** The buttons. Right-aligned, primary last. */
    footer?: Snippet;
    class?: string;
    children: Snippet;
  }

  let {
    open = $bindable(false),
    title,
    description,
    size = 'md',
    dismissible = true,
    onclose,
    footer,
    class: className = '',
    children,
  }: Props = $props();

  const uid = $props.id();
  const id = `dialog-${uid}`;

  function close(): void {
    if (!open) return;
    open = false;
    onclose?.();
  }

  let backdrop = $state<HTMLDivElement | null>(null);

  $effect(() => {
    if (!open) return;
    const layer = layers.push('dialog', close);
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
    <!--
      A plain div, not a button: a focusable element that is also hidden from
      assistive technology is a contradiction, and clicking it left the dialog
      with an activeElement it could not describe. Escape already closes
      through the layer stack, so the scrim needs no keyboard role of its own.
    -->
    <div class="scrim" aria-hidden="true" onclick={() => dismissible && close()}></div>
    <div
      class="panel {size} {className}"
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
        {#if dismissible}
          <IconButton icon={X} label="Close" size="sm" onclick={close} />
        {/if}
      </header>
      <div class="body">{@render children()}</div>
      {#if footer}
        <footer>{@render footer()}</footer>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    /* The window, not the document: see --z-window-width. */
    inset: 0 auto 0 0;
    width: var(--z-window-width);
    z-index: var(--z-layer-dialog);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--z-space-6);
  }
  .scrim {
    position: absolute;
    inset: 0;
    border: 0;
    padding: 0;
    background: var(--z-scrim);
    backdrop-filter: blur(var(--z-scrim-blur));
    cursor: default;
    animation: fade var(--z-motion-base) var(--z-ease);
  }
  .panel {
    position: relative;
    display: flex;
    flex-direction: column;
    width: 100%;
    /* dvh, not vh: on iOS Safari 100vh is the viewport with the chrome
       retracted, so a tall dialog put its footer -- and therefore its primary
       action -- under the browser's own bar with no way to scroll to it. */
    max-height: calc(100dvh - var(--z-space-12));
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-lg);
    animation: rise var(--z-motion-slow) var(--z-ease);
  }
  .panel:focus {
    outline: none;
  }
  .sm {
    max-width: var(--z-width-dialog-sm);
  }
  .md {
    max-width: var(--z-width-dialog-md);
  }
  .lg {
    max-width: var(--z-width-dialog-lg);
  }
  header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-5) var(--z-space-5) var(--z-space-3);
  }
  .heading {
    min-width: 0;
  }
  /* min-width alone only lets the box shrink -- a name with no spaces in it
     then runs out of the box and over Close. */
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  header p {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .body {
    padding: 0 var(--z-space-5);
    overflow-y: auto;
  }
  footer {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--z-space-2);
    padding: var(--z-space-5);
  }
  @keyframes fade {
    from {
      opacity: 0;
    }
  }
  @keyframes rise {
    from {
      opacity: 0;
      transform: translateY(var(--z-space-2));
    }
  }
  @media (max-width: 768px) {
    .backdrop {
      padding: var(--z-space-3);
      align-items: flex-end;
    }
  }
</style>
