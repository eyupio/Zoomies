<!--
  Settings inside a section that most pools never touch.

  A section is the answer to one question, and a question usually has one or
  two controls that decide it and a handful that refine it. The refinements are
  behind this: a native disclosure, so the keyboard, the screen reader and
  find-in-page all work without anything of ours, with the one line that says
  what is inside and whether any of it is in use.

  It is for the exception, not the structure. A section that opens on a wall of
  these is the form it replaced, one level down, so each section has at most one
  or two, and whatever is on in one is open when the section is.
-->
<script lang="ts">
  import { ChevronRight } from '@lucide/svelte';
  import type { Snippet } from 'svelte';

  interface Props {
    title: string;
    /** What is inside, or whether any of it is in use: the closed row's second line. */
    note?: string;
    open?: boolean;
    children: Snippet;
  }

  let { title, note, open = $bindable(false), children }: Props = $props();
</script>

<details class="more" bind:open>
  <summary>
    <ChevronRight class="chev" size={16} aria-hidden="true" />
    <span class="text">
      <span class="title">{title}</span>
      {#if note}<span class="note">{note}</span>{/if}
    </span>
  </summary>
  <div class="inner">{@render children()}</div>
</details>

<style>
  .more {
    min-width: 0;
    border-top: var(--z-border-width) solid var(--z-border);
  }
  summary {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    min-height: var(--z-control-touch);
    padding-block: var(--z-space-3);
    list-style: none;
    cursor: pointer;
  }
  summary::-webkit-details-marker {
    display: none;
  }
  summary:focus-visible {
    outline: none;
    box-shadow: var(--z-focus-ring);
    border-radius: var(--z-radius-sm);
  }
  summary :global(.chev) {
    flex: none;
    margin-top: var(--z-nudge-2);
    color: var(--z-text-subtle);
    transition: transform var(--z-motion-fast) var(--z-ease);
  }
  .more[open] summary :global(.chev) {
    transform: rotate(90deg);
  }
  .text {
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: var(--z-nudge-2);
  }
  .title {
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
  }
  .note {
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .inner {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
    min-width: 0;
    padding-bottom: var(--z-space-1);
  }
</style>
