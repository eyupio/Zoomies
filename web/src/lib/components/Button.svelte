<!--
  The one button.

  `loading` swaps the label for a spinner without changing the button's width,
  because a row of actions that reflows while you are clicking it is how you
  click the wrong thing. Spinners appear here and nowhere else: content loads
  behind skeletons.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { LucideIcon } from '@lucide/svelte';

  interface Props {
    variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
    size?: 'sm' | 'md';
    type?: 'button' | 'submit' | 'reset';
    /**
     * The id of the form this button submits.
     *
     * A dialog pins its actions in a footer outside the scrolling body, so the
     * primary cannot be a descendant of the form it belongs to. This is how it
     * stays where it is and still means Enter.
     */
    form?: string;
    disabled?: boolean;
    loading?: boolean;
    /** Renders an anchor styled as a button. Use for navigation, not for actions. */
    href?: string;
    full?: boolean;
    icon?: LucideIcon;
    iconAfter?: LucideIcon;
    title?: string;
    ariaLabel?: string;
    ariaExpanded?: boolean;
    ariaControls?: string;
    ariaHaspopup?: 'menu' | 'dialog' | 'listbox' | 'true';
    onclick?: (event: MouseEvent) => void;
    class?: string;
    children: Snippet;
  }

  let {
    variant = 'secondary',
    size = 'md',
    type = 'button',
    form,
    disabled = false,
    loading = false,
    href,
    full = false,
    icon: Icon,
    iconAfter: IconAfter,
    title,
    ariaLabel,
    ariaExpanded,
    ariaControls,
    ariaHaspopup,
    onclick,
    class: className = '',
    children,
  }: Props = $props();

  const iconSize = $derived(size === 'sm' ? 13 : 14);
</script>

{#snippet body()}
  <span class="content" class:hidden={loading}>
    {#if Icon}<Icon size={iconSize} aria-hidden="true" />{/if}
    <span class="label">{@render children()}</span>
    {#if IconAfter}<IconAfter size={iconSize} aria-hidden="true" />{/if}
  </span>
  {#if loading}
    <span class="spinner" aria-hidden="true"></span>
    <span class="sr-only">Working</span>
  {/if}
{/snippet}

{#if href}
  <a
    {href}
    {title}
    class="btn {variant} {size} {className}"
    class:full
    aria-label={ariaLabel}
    aria-disabled={disabled ? 'true' : undefined}
    data-loading={loading ? '' : undefined}
  >
    {@render body()}
  </a>
{:else}
  <!--
    Loading is `aria-disabled`, not `disabled`.

    Disabling the element the operator has just activated makes the browser
    blur it, so focus falls to <body>. Inside a Dialog that also disarms the
    focus trap, which listens on the panel: a Tab pressed from <body> is never
    intercepted, and the operator walks out of an open modal into the page
    behind it. aria-disabled keeps the button focused and announced, the
    pointer is stopped in CSS, and the click handler refuses while busy.
  -->
  <button
    {type}
    {form}
    {title}
    class="btn {variant} {size} {className}"
    class:full
    {disabled}
    aria-disabled={loading ? 'true' : undefined}
    aria-label={ariaLabel}
    aria-busy={loading ? 'true' : undefined}
    aria-expanded={ariaExpanded}
    aria-controls={ariaControls}
    aria-haspopup={ariaHaspopup}
    onclick={(event) => {
      if (loading) {
        event.preventDefault();
        return;
      }
      onclick?.(event);
    }}
  >
    {@render body()}
  </button>
{/if}

<style>
  .btn {
    position: relative;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: var(--z-space-2);
    border: 1px solid transparent;
    border-radius: var(--z-radius-md);
    font-family: inherit;
    font-weight: var(--z-weight-medium);
    line-height: 1;
    text-decoration: none;
    white-space: nowrap;
    cursor: pointer;
    transition:
      background-color var(--z-motion-fast) var(--z-ease),
      border-color var(--z-motion-fast) var(--z-ease),
      color var(--z-motion-fast) var(--z-ease);
  }
  /* A button that is busy still holds focus, so it must not also be clickable
     -- otherwise a second press fires the same request again. */
  .btn[aria-disabled='true'] {
    pointer-events: none;
  }
  .btn.full {
    width: 100%;
  }
  .md {
    height: var(--z-space-8);
    padding: 0 var(--z-space-3);
    font-size: var(--z-text-sm);
  }
  .sm {
    height: var(--z-space-6);
    padding: 0 var(--z-space-2);
    font-size: var(--z-text-xs);
  }
  .btn:disabled,
  .btn[aria-disabled='true'] {
    cursor: not-allowed;
    opacity: 0.55;
  }

  .primary {
    background: var(--z-accent);
    color: var(--z-accent-contrast);
  }
  .primary:hover:not(:disabled):not([aria-disabled='true']) {
    background: var(--z-accent-hover);
  }
  .secondary {
    background: var(--z-surface);
    color: var(--z-text);
    border-color: var(--z-border-strong);
  }
  .secondary:hover:not(:disabled):not([aria-disabled='true']) {
    background: var(--z-surface-hover);
  }
  .ghost {
    background: transparent;
    color: var(--z-text-muted);
  }
  .ghost:hover:not(:disabled):not([aria-disabled='true']) {
    background: var(--z-surface-hover);
    color: var(--z-text);
  }
  .danger {
    background: var(--z-danger);
    color: var(--z-text-inverse);
  }
  .danger:hover:not(:disabled):not([aria-disabled='true']) {
    background: var(--z-danger-hover);
  }

  .content {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  .content.hidden {
    visibility: hidden;
  }
  .label {
    display: inline-block;
  }

  .spinner {
    position: absolute;
    inset: 0;
    margin: auto;
    width: 14px;
    height: 14px;
    border: 2px solid currentColor;
    border-top-color: transparent;
    border-radius: var(--z-radius-full);
    animation: spin calc(var(--z-motion-slow) * 2) linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  @media (prefers-reduced-motion: reduce) {
    /* The ring still reads as "working"; the label is replaced and `aria-busy`
       says so. Nobody has to watch something spin to learn that. */
    .spinner {
      animation: none;
    }
  }
</style>
