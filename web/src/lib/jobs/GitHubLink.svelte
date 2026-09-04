<!--
  A link out to GitHub.

  Every job row has one, and it opens in a new tab: the dashboard is the thing
  left open all day, and a workflow run should not replace it. That is why this
  is a plain anchor rather than the Button component, which has no target.
-->
<script lang="ts">
  import { ExternalLink } from '@lucide/svelte';

  interface Props {
    href: string | undefined;
    /** The accessible name. Say what is being opened: "Open the run on GitHub". */
    label: string;
    /** Render the label beside the icon rather than only for assistive technology. */
    showLabel?: boolean;
    /** Draw it as a button rather than a bare icon. */
    variant?: 'icon' | 'button';
    /** A grid row uses this to stop the click also opening the row. */
    onclick?: (event: MouseEvent) => void;
    class?: string;
  }

  let {
    href,
    label,
    showLabel = false,
    variant = 'icon',
    onclick,
    class: className = '',
  }: Props = $props();
</script>

{#if href}
  <a
    {href}
    target="_blank"
    rel="noopener noreferrer"
    class="link {variant} {className}"
    {onclick}
    title={showLabel ? undefined : label}
    aria-label={showLabel ? undefined : label}
  >
    {#if showLabel}<span>{label}</span>{/if}
    <ExternalLink size={variant === 'button' ? 14 : 13} aria-hidden="true" />
  </a>
{:else}
  <span class="absent" title="Zoomies did not record a run URL for this job">--</span>
{/if}

<style>
  .link {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: var(--z-space-2);
    color: var(--z-accent);
    text-decoration: none;
    border-radius: var(--z-radius-sm);
  }
  .link.icon {
    width: var(--z-space-6);
    height: var(--z-space-6);
  }
  .link.button {
    height: var(--z-space-8);
    padding: 0 var(--z-space-4);
    border: 1px solid var(--z-border-strong);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    color: var(--z-text);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    transition: background var(--z-motion-fast) var(--z-ease);
  }
  .link.icon:hover {
    background: var(--z-surface-hover);
    color: var(--z-accent-hover);
  }
  .link.button:hover {
    background: var(--z-surface-hover);
  }
  .absent {
    color: var(--z-text-subtle);
  }
</style>
