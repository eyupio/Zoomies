<!--
  Never a blank table. An empty state says what the thing is, and what to do
  next, with the action inline.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { Inbox } from '@lucide/svelte';
  import type { LucideIcon } from '@lucide/svelte';

  interface Props {
    icon?: LucideIcon;
    title: string;
    /** One sentence. What this is, or why it is empty. */
    description?: string;
    compact?: boolean;
    class?: string;
    /** The action that fills it. */
    children?: Snippet;
  }

  let {
    icon: Icon = Inbox,
    title,
    description,
    compact = false,
    class: className = '',
    children,
  }: Props = $props();
</script>

<div class="empty {className}" class:compact>
  <span class="icon" aria-hidden="true"><Icon size={compact ? 18 : 22} /></span>
  <p class="title">{title}</p>
  {#if description}<p class="description">{description}</p>{/if}
  {#if children}
    <div class="action">{@render children()}</div>
  {/if}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: var(--z-space-2);
    padding: var(--z-space-12) var(--z-space-6);
    text-align: center;
  }
  .empty.compact {
    padding: var(--z-space-6);
  }
  .icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--z-space-10);
    height: var(--z-space-10);
    margin-bottom: var(--z-space-1);
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
    color: var(--z-text-subtle);
  }
  .compact .icon {
    width: var(--z-space-8);
    height: var(--z-space-8);
  }
  .title {
    margin: 0;
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .description {
    margin: 0;
    max-width: 46ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .action {
    margin-top: var(--z-space-3);
  }
</style>
