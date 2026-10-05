<!--
  The pool editor's table of contents, at the left where there is room for one.

  The sections are already a list of rows with their answers on them, and on a
  phone that list *is* the navigation. A wide screen has room to keep a map in
  view while one section is open and the page scrolls, so the same names are
  here, each one a link, each marked when something in it needs attention or has
  been edited. Below the width where Settings does the same, the rail is not
  drawn at all: the rows are on screen anyway, and a second copy of them above
  the form would be one more thing to scroll past.

  The links are real anchors, so they can be copied and opened in a new tab, but
  a click is answered here rather than by the browser: the section has to be
  opened before there is anything to scroll to, and a hash change is a history
  entry the router would take for a navigation.
-->
<script lang="ts">
  import { TriangleAlert } from '@lucide/svelte';
  import type { SectionId } from './sections';

  export interface RailItem {
    id: SectionId;
    title: string;
    problems: number;
    edited: boolean;
  }

  interface Props {
    items: readonly RailItem[];
    onjump: (id: SectionId) => void;
  }

  let { items, onjump }: Props = $props();
</script>

<nav class="rail" aria-label="On this page">
  <ul>
    {#each items as item (item.id)}
      <li>
        <a
          href="#{item.id}"
          onclick={(event) => {
            event.preventDefault();
            onjump(item.id);
          }}
        >
          <span class="label">{item.title}</span>
          {#if item.problems > 0}
            <TriangleAlert class="problem" size={14} aria-hidden="true" />
            <span class="sr-only"
              >{item.problems === 1 ? '1 to fix' : `${item.problems} to fix`}</span
            >
          {:else if item.edited}
            <span class="dot" aria-hidden="true"></span>
            <span class="sr-only">Edited</span>
          {/if}
        </a>
      </li>
    {/each}
  </ul>
</nav>

<style>
  .rail {
    position: sticky;
    top: calc(var(--z-topbar-height) + var(--z-space-6));
    width: var(--z-settings-rail-width);
  }
  ul {
    display: flex;
    flex-direction: column;
    gap: var(--z-nudge-2);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  a {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    height: var(--z-space-8);
    padding: 0 var(--z-space-2);
    border-radius: var(--z-radius-md);
    color: var(--z-text-muted);
    text-decoration: none;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    white-space: nowrap;
    transition:
      background-color var(--z-motion-fast) var(--z-ease),
      color var(--z-motion-fast) var(--z-ease);
  }
  a:hover {
    background: var(--z-surface-hover);
    color: var(--z-text);
  }
  .label {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  a :global(.problem) {
    flex: none;
    color: var(--z-danger);
  }
  .dot {
    flex: none;
    width: var(--z-space-2);
    height: var(--z-space-2);
    border-radius: var(--z-radius-full);
    background: var(--z-accent);
  }
  /* --z-bp-lg, written out. Below it the rows are the navigation. */
  @media (max-width: 1180px) {
    .rail {
      display: none;
    }
  }
</style>
