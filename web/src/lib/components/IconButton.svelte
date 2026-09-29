<!--
  A square button whose only content is an icon, so it always carries a label
  for anyone who cannot see the icon.
-->
<script lang="ts">
  import type { LucideIcon } from '@lucide/svelte';

  interface Props {
    icon: LucideIcon;
    /** Required. Becomes the accessible name and the tooltip. */
    label: string;
    variant?: 'ghost' | 'secondary' | 'danger';
    size?: 'sm' | 'md';
    disabled?: boolean;
    loading?: boolean;
    href?: string;
    /** An `href` that leaves the product: opens in a new tab, and says so. */
    newTab?: boolean;
    /** For toggle buttons: renders `aria-pressed`. */
    pressed?: boolean;
    expanded?: boolean;
    controls?: string;
    haspopup?: 'menu' | 'dialog' | 'listbox' | 'true';
    /** Suppress the native tooltip when a Tooltip component wraps this. */
    showTitle?: boolean;
    onclick?: (event: MouseEvent) => void;
    class?: string;
  }

  let {
    icon: Icon,
    label,
    variant = 'ghost',
    size = 'md',
    disabled = false,
    loading = false,
    href,
    newTab = false,
    pressed,
    expanded,
    controls,
    haspopup,
    showTitle = true,
    onclick,
    class: className = '',
  }: Props = $props();

  const iconSize = $derived(size === 'sm' ? 14 : 16);
</script>

{#if href}
  <a
    {href}
    class="icon-btn {variant} {size} {className}"
    aria-label={newTab ? `${label} (opens in a new tab)` : label}
    title={showTitle ? label : undefined}
    target={newTab ? '_blank' : undefined}
    rel={newTab ? 'noopener noreferrer' : undefined}
  >
    <Icon size={iconSize} aria-hidden="true" />
  </a>
{:else}
  <!--
    Loading is `aria-disabled`, not `disabled`, for the reason Button gives:
    disabling the element the operator has just activated blurs it, so focus
    falls to <body> -- and when the request is refused and the editor stays
    open, the next Tab starts from the top of the page.
  -->
  <button
    type="button"
    class="icon-btn {variant} {size} {className}"
    aria-label={label}
    title={showTitle ? label : undefined}
    aria-pressed={pressed}
    aria-expanded={expanded}
    aria-controls={controls}
    aria-haspopup={haspopup}
    aria-busy={loading ? 'true' : undefined}
    aria-disabled={loading ? 'true' : undefined}
    {disabled}
    onclick={(event) => {
      if (loading) {
        event.preventDefault();
        return;
      }
      onclick?.(event);
    }}
  >
    <Icon size={iconSize} aria-hidden="true" />
  </button>
{/if}

<style>
  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: var(--z-border-width) solid transparent;
    border-radius: var(--z-radius-md);
    background: transparent;
    color: var(--z-text-muted);
    cursor: pointer;
    transition:
      background-color var(--z-motion-fast) var(--z-ease),
      color var(--z-motion-fast) var(--z-ease);
  }
  .md {
    width: var(--z-space-8);
    height: var(--z-space-8);
  }
  .sm {
    width: var(--z-space-6);
    height: var(--z-space-6);
  }
  .ghost:hover:not(:disabled):not([aria-disabled='true']) {
    background: var(--z-surface-hover);
    color: var(--z-text);
  }
  .secondary {
    background: var(--z-surface);
    border-color: var(--z-border-strong);
    color: var(--z-text);
  }
  .secondary:hover:not(:disabled):not([aria-disabled='true']) {
    background: var(--z-surface-hover);
  }
  .danger {
    color: var(--z-danger);
  }
  .danger:hover:not(:disabled):not([aria-disabled='true']) {
    background: var(--z-danger-subtle);
  }
  /* A button that is busy still holds focus, so it must not also be clickable
     -- otherwise a second press fires the same request again. */
  .icon-btn[aria-disabled='true'] {
    pointer-events: none;
  }
  .icon-btn:disabled,
  .icon-btn[aria-disabled='true'] {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .icon-btn[aria-pressed='true'] {
    background: var(--z-accent-subtle);
    color: var(--z-accent);
  }
</style>
