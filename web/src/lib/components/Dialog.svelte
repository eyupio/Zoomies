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
  import { layers, lockScroll, trapFocus } from '../keys';
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

  $effect(() => {
    if (!open) return;
    const layer = layers.push('dialog', close);
    const unlock = lockScroll();
    return () => {
      layers.remove(layer);
      unlock();
    };
  });
</script>

{#if open}
  <div class="backdrop">
    <button
      type="button"
      class="scrim"
      tabindex="-1"
      aria-hidden="true"
      onclick={() => dismissible && close()}
    ></button>
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
    inset: 0;
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
    /* A veil made from the page's own ground, so it dims in light and in dark
       without a hard-coded black that only works in one of them. */
    background: color-mix(in srgb, var(--z-bg) 72%, transparent);
    backdrop-filter: blur(3px);
    cursor: default;
    animation: fade var(--z-motion-base) var(--z-ease);
  }
  .panel {
    position: relative;
    display: flex;
    flex-direction: column;
    width: 100%;
    max-height: calc(100vh - var(--z-space-12));
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-lg);
    animation: rise var(--z-motion-slow) var(--z-ease);
  }
  .panel:focus {
    outline: none;
  }
  .sm {
    max-width: 400px;
  }
  .md {
    max-width: 560px;
  }
  .lg {
    max-width: 820px;
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
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
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
      transform: translateY(8px);
    }
  }
  @media (max-width: 768px) {
    .backdrop {
      padding: var(--z-space-3);
      align-items: flex-end;
    }
  }
</style>
