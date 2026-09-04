<!--
  The top of every page.

  It owns the `<h1>` that route navigation moves focus to, so a keyboard user
  lands on the page name rather than back at the top of the navigation.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { ChevronRight } from '@lucide/svelte';

  export interface Crumb {
    label: string;
    href?: string;
  }

  interface Props {
    title: string;
    subtitle?: string;
    breadcrumb?: readonly Crumb[];
    /** Status badges and counts, under the title. */
    meta?: Snippet;
    /** The primary action, and any secondary ones beside it. */
    children?: Snippet;
    class?: string;
  }

  let { title, subtitle, breadcrumb, meta, children, class: className = '' }: Props = $props();
</script>

<header class="page-header {className}">
  {#if breadcrumb && breadcrumb.length > 0}
    <nav aria-label="Breadcrumb">
      <ol>
        {#each breadcrumb as crumb, i (crumb.label)}
          <li>
            {#if crumb.href}<a href={crumb.href}>{crumb.label}</a>{:else}<span>{crumb.label}</span
              >{/if}
            {#if i < breadcrumb.length - 1}
              <ChevronRight size={12} aria-hidden="true" />
            {/if}
          </li>
        {/each}
      </ol>
    </nav>
  {/if}
  <div class="row">
    <div class="titles">
      <h1 id="page-heading" tabindex="-1">{title}</h1>
      {#if subtitle}<p class="subtitle">{subtitle}</p>{/if}
      {#if meta}<div class="meta">{@render meta()}</div>{/if}
    </div>
    {#if children}
      <div class="actions">{@render children()}</div>
    {/if}
  </div>
</header>

<style>
  .page-header {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    margin-bottom: var(--z-space-6);
  }
  nav ol {
    display: flex;
    align-items: center;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
  }
  nav li {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
  }
  nav a {
    color: var(--z-text-muted);
    text-decoration: none;
  }
  nav a:hover {
    color: var(--z-accent);
    text-decoration: underline;
  }
  .row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--z-space-4);
    flex-wrap: wrap;
  }
  .titles {
    min-width: 0;
  }
  h1 {
    margin: 0;
    font-size: var(--z-text-xl);
    line-height: var(--z-leading-xl);
    font-weight: var(--z-weight-bold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  /*
    Route navigation moves focus here so a keyboard user lands on the page name
    rather than back at the top of the nav. That focus is programmatic, though,
    and drawing a ring around the title of every page the moment it loads reads
    as a rendering bug rather than as an affordance. :focus-visible would
    normally handle this, but a scripted .focus() on a tabindex="-1" element
    still matches it in Chromium, so the ring is suppressed explicitly. A
    keyboard user has not moved focus here themselves and is being told where
    they are by the live region instead.
  */
  h1:focus,
  h1:focus-visible {
    outline: none;
  }
  .subtitle {
    margin: var(--z-space-1) 0 0;
    max-width: 72ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .meta {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin-top: var(--z-space-3);
  }
  .actions {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  @media (max-width: 768px) {
    .actions {
      width: 100%;
    }
  }
</style>
