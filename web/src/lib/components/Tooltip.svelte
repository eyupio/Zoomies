<!--
  A tooltip that appears on hover *and* on focus, and whose text is also present
  for assistive technology at all times. Nothing may live only in a tooltip.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { layers } from '../keys';

  interface Props {
    /** The whole tooltip as one sentence: what assistive technology gets. */
    text: string;
    /** Optional control for touch triggers that must reopen while still focused. */
    open?: boolean;
    descriptionId?: string;
    /**
     * Richer markup for the bubble itself -- a heading, a figure, a line of
     * context -- for sighted readers. `text` stays the accessible copy, so a
     * card with three lines is still one sentence to a screen reader.
     */
    content?: Snippet;
    placement?: 'top' | 'bottom' | 'left' | 'right';
    /** A card with figures and rows in it wants more room than a sentence does. */
    wide?: boolean;
    class?: string;
    children: Snippet;
  }

  let {
    text,
    content,
    placement = 'top',
    wide = false,
    class: className = '',
    children,
    open = $bindable(false),
    descriptionId,
  }: Props = $props();

  let wrap = $state<HTMLSpanElement | null>(null);
  let bubble = $state<HTMLSpanElement | null>(null);
  let hovered = false;
  let focused = false;

  $effect(() => {
    if (!open || !wrap || !bubble) return;
    const anchor = wrap;
    const tip = bubble;
    const preferred = placement;
    // Reading text makes an already open tooltip reposition when its copy changes.
    void text;
    const layer = layers.push('tooltip', hide);
    // The browser's top layer escapes overflow clipping and stacking contexts,
    // while keeping the tooltip in its owner's DOM and accessibility tree.
    tip.showPopover();

    function position(): void {
      const rect = anchor.getBoundingClientRect();
      const width = document.documentElement.clientWidth;
      const height = document.documentElement.clientHeight;
      if (rect.bottom <= 0 || rect.top >= height || rect.right <= 0 || rect.left >= width) {
        hide();
        return;
      }
      // A focused trigger can scroll out of a table without leaving the
      // window. Its top-layer tooltip must not float over unrelated rows.
      for (let parent = anchor.parentElement; parent; parent = parent.parentElement) {
        const style = getComputedStyle(parent);
        const clip = parent.getBoundingClientRect();
        const clipsX = /auto|scroll|hidden|clip/.test(style.overflowX);
        const clipsY = /auto|scroll|hidden|clip/.test(style.overflowY);
        if (
          (clipsX && (rect.right <= clip.left || rect.left >= clip.right)) ||
          (clipsY && (rect.bottom <= clip.top || rect.top >= clip.bottom))
        ) {
          hide();
          return;
        }
      }
      // Padding uses the shared space token and supplies the viewport margin
      // and anchor gap too, rather than inventing another spacing scale.
      const gap = parseFloat(getComputedStyle(tip).paddingLeft);
      const bounds = tip.getBoundingClientRect();
      const space = {
        top: rect.top,
        bottom: height - rect.bottom,
        left: rect.left,
        right: width - rect.right,
      };
      const opposite = { top: 'bottom', bottom: 'top', left: 'right', right: 'left' } as const;
      const needed =
        (preferred === 'top' || preferred === 'bottom' ? bounds.height : bounds.width) + gap;
      const side =
        space[preferred] < needed && space[opposite[preferred]] > space[preferred]
          ? opposite[preferred]
          : preferred;
      let x = rect.left + (rect.width - bounds.width) / 2;
      let y = rect.top + (rect.height - bounds.height) / 2;
      if (side === 'top') y = rect.top - bounds.height - gap;
      if (side === 'bottom') y = rect.bottom + gap;
      if (side === 'left') x = rect.left - bounds.width - gap;
      if (side === 'right') x = rect.right + gap;
      tip.style.left = `${Math.max(gap, Math.min(x, width - bounds.width - gap))}px`;
      tip.style.top = `${Math.max(gap, Math.min(y, height - bounds.height - gap))}px`;
    }

    function dismissOutside(event: PointerEvent): void {
      if (event.target instanceof Node && !anchor.contains(event.target)) hide();
    }
    document.addEventListener('pointerdown', dismissOutside, true);
    position();
    // Capture sees scrolls from the table's own viewport, not just the window.
    document.addEventListener('scroll', position, true);
    window.addEventListener('resize', position);
    const observer = new ResizeObserver(position);
    observer.observe(anchor);
    observer.observe(tip);
    return () => {
      layers.remove(layer);
      document.removeEventListener('pointerdown', dismissOutside, true);
      observer.disconnect();
      document.removeEventListener('scroll', position, true);
      window.removeEventListener('resize', position);
      if (tip.matches(':popover-open')) tip.hidePopover();
    };
  });

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
  bind:this={wrap}
  role="presentation"
  onpointerenter={(event) => {
    if (event.pointerType === 'touch') return;
    hovered = true;
    show();
  }}
  onpointerleave={() => {
    hovered = false;
    if (!focused) hide();
  }}
  onfocusin={() => {
    focused = true;
    show();
  }}
  onfocusout={() => {
    focused = false;
    if (!hovered) hide();
  }}
  onkeydown={onKeydown}
>
  {@render children()}
  <span class="sr-only" id={descriptionId}>{text}</span>
  {#if open}
    <span
      bind:this={bubble}
      class="bubble"
      class:wide
      popover="manual"
      role="presentation"
      aria-hidden="true"
      >{#if content}{@render content()}{:else}{text}{/if}</span
    >
  {/if}
</span>

<style>
  .tip-wrap {
    position: relative;
    display: inline-flex;
    align-items: center;
  }
  .bubble {
    position: fixed;
    inset: auto;
    margin: 0;
    z-index: var(--z-layer-dropdown);
    max-width: min(260px, calc(100vw - var(--z-space-4)));
    max-height: calc(100dvh - var(--z-space-4));
    overflow-y: auto;
    overflow-wrap: anywhere;
    padding: var(--z-space-1) var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
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
  .bubble.wide {
    max-width: min(360px, calc(100vw - var(--z-space-4)));
  }
</style>
