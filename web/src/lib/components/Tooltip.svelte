<!--
  A tooltip that appears on hover *and* on focus, and whose text is also present
  for assistive technology at all times. Nothing may live only in a tooltip.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    text: string;
    placement?: 'top' | 'bottom' | 'left' | 'right';
    class?: string;
    children: Snippet;
  }

  let { text, placement = 'top', class: className = '', children }: Props = $props();

  let open = $state(false);

  function show(): void {
    open = true;
  }
  function hide(): void {
    open = false;
  }
  function onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Escape' && open) {
      event.stopPropagation();
      open = false;
    }
  }
</script>

<span
  class="tip-wrap {className}"
  role="presentation"
  onmouseenter={show}
  onmouseleave={hide}
  onfocusin={show}
  onfocusout={hide}
  onkeydown={onKeydown}
>
  {@render children()}
  <span class="sr-only">{text}</span>
  {#if open}
    <span class="bubble {placement}" role="presentation" aria-hidden="true">{text}</span>
  {/if}
</span>

<style>
  .tip-wrap {
    position: relative;
    display: inline-flex;
    align-items: center;
  }
  .bubble {
    position: absolute;
    z-index: var(--z-layer-dropdown);
    max-width: 260px;
    padding: var(--z-space-1) var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-raised);
    color: var(--z-text);
    box-shadow: var(--z-shadow-md);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    white-space: normal;
    width: max-content;
    pointer-events: none;
  }
  .top {
    bottom: calc(100% + var(--z-space-2));
    left: 50%;
    transform: translateX(-50%);
  }
  .bottom {
    top: calc(100% + var(--z-space-2));
    left: 50%;
    transform: translateX(-50%);
  }
  .left {
    right: calc(100% + var(--z-space-2));
    top: 50%;
    transform: translateY(-50%);
  }
  .right {
    left: calc(100% + var(--z-space-2));
    top: 50%;
    transform: translateY(-50%);
  }
</style>
