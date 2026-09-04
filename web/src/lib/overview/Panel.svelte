<!--
  One section of the Overview.

  Every panel on this page is the same object: a heading, an optional line of
  context, an optional action on the right, and a body. Keeping that in one
  place is what stops a dashboard of five different card designs.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    title: string;
    /** One line under the heading, when the heading cannot say it all. */
    description?: string;
    /** A count, a badge, or a link out. Sits opposite the heading. */
    actions?: Snippet;
    /** Drop the body padding, for lists that draw their own full-width rows. */
    flush?: boolean;
    class?: string;
    children: Snippet;
  }

  let {
    title,
    description,
    actions,
    flush = false,
    class: className = '',
    children,
  }: Props = $props();

  const headingId = $props.id();
</script>

<section class="panel {className}" aria-labelledby={headingId}>
  <header>
    <div class="titles">
      <h2 id={headingId}>{title}</h2>
      {#if description}<p>{description}</p>{/if}
    </div>
    {#if actions}<div class="actions">{@render actions()}</div>{/if}
  </header>
  <div class="body" class:flush>{@render children()}</div>
</section>

<style>
  .panel {
    display: flex;
    flex-direction: column;
    min-width: 0;
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: var(--z-space-4);
    padding: var(--z-space-4) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  .titles {
    min-width: 0;
  }
  h2 {
    margin: 0;
    font-size: var(--z-text-lg);
    line-height: var(--z-leading-lg);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .titles p {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    flex: none;
  }
  .body {
    flex: 1;
    min-width: 0;
    padding: var(--z-space-5);
  }
  .body.flush {
    padding: 0;
  }
</style>
