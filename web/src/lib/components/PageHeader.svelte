<!--
  The top of every page.

  It owns the `<h1>` that route navigation moves focus to, so a keyboard user
  lands on the page name rather than back at the top of the navigation.

  It also owns where refresh lives. A page that can be fetched again passes
  `onrefresh`, and the control appears in the same place on every one of them --
  first in the actions row, ahead of whatever this page's own actions are. That
  uniformity is the point: the gesture is worth nothing if it has to be found.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { ChevronRight } from '@lucide/svelte';
  import type { RefreshHandler } from '../state/refresh.svelte';
  import RefreshButton from './RefreshButton.svelte';

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
    /**
     * What this page does when it is asked to refresh. Renders the refresh
     * button and becomes what the `R` shortcut does while the page is open.
     * Pages with nothing to fetch -- a wizard, the not-found page -- omit it
     * and get no button.
     */
    onrefresh?: RefreshHandler;
    /** The primary action, and any secondary ones beside it. */
    children?: Snippet;
    class?: string;
  }

  let {
    title,
    subtitle,
    breadcrumb,
    meta,
    onrefresh,
    children,
    class: className = '',
  }: Props = $props();
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
    {#if onrefresh || children}
      <div class="actions">
        {#if onrefresh}<RefreshButton {onrefresh} />{/if}
        {#if children}{@render children()}{/if}
      </div>
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
    max-width: var(--z-measure-prose);
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
    /* Wraps rather than pushing the page out from under itself. The row above
       already drops this block onto its own line, but a button does not wrap
       its own label, so a detail page carrying five of them -- refresh,
       prewarm, disable, edit, delete -- asked for 538px inside a 412px phone
       and got it: the name was cut off mid-word, the cards were clipped on the
       left, and Delete sat off the right edge where it could not be pressed.
       The `width: 100%` below made the block wide without letting it wrap,
       which is why the breakpoint alone did not save it. */
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  @media (max-width: 768px) {
    .actions {
      width: 100%;
    }
  }
</style>
