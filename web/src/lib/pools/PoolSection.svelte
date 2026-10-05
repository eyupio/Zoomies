<!--
  One decision on the pool editor, as a row that opens in place.

  The row says what the answer is *now*, in a line, without being opened -- so
  the whole pool can be read down the page, and a first-time reader sees that
  every default has already been chosen for them. Opening it shows the controls
  that change the answer. That is the whole of the navigation on a phone: the
  rows are the table of contents, each is one tap, and nothing has to be reached
  by pressing Next up to it.

  It is the disclosure pattern from the ARIA Authoring Practices: a heading
  that holds a button, the button carrying `aria-expanded`. The summary and the
  marks are the button's description rather than its name, so the name stays
  "Size" and a screen reader says the rest after it.

  The whole header is the target. The button's own text is small, and a row
  that opens only when its title is hit is a row a thumb misses; the overlay
  below is the same arrangement the choice cards use.

  A closed section is not mounted. The draft is one object held by the editor,
  so nothing typed is lost by closing one, and a page of six sections that all
  carried a slider apiece would be paid for by every phone that never opened
  most of them.
-->
<script lang="ts">
  import { ChevronDown, TriangleAlert } from '@lucide/svelte';
  import type { Snippet } from 'svelte';

  interface Props {
    /** The section's name for the rail and a deep link: `size` is `#size`. */
    id: string;
    title: string;
    /** What it decides, in a sentence. Shown at the top of the open section. */
    description?: string;
    /** The current answer, in a line. Always on screen. */
    summary: string;
    open?: boolean;
    /** The rules this section is breaking, as the operator can see them. */
    problems?: number;
    /** True when saving would change something here. */
    edited?: boolean;
    children: Snippet;
  }

  let {
    id,
    title,
    description,
    summary,
    open = $bindable(false),
    problems = 0,
    edited = false,
    children,
  }: Props = $props();

  const domId = $derived(`pool-${id}`);
  const marks = $derived(
    [
      problems > 0 ? (problems === 1 ? '1 to fix' : `${problems} to fix`) : '',
      edited ? 'Edited' : '',
    ]
      .filter(Boolean)
      .join(', '),
  );
</script>

<section class="section" class:open id={domId} data-section={id}>
  <div class="head">
    <div class="text">
      <div class="line">
        <h2 class="title">
          <button
            type="button"
            class="toggle"
            aria-expanded={open}
            aria-controls={open ? `${domId}-body` : undefined}
            aria-describedby="{domId}-summary{marks ? ` ${domId}-marks` : ''}"
            onclick={() => (open = !open)}>{title}</button
          >
        </h2>
        {#if problems > 0}
          <span class="mark problem">
            <TriangleAlert size={12} aria-hidden="true" />
            {problems === 1 ? '1 to fix' : `${problems} to fix`}
          </span>
        {/if}
        {#if edited}<span class="mark edited">Edited</span>{/if}
        <!-- The marks are drawn above for the eye; this is the same words for the
             description, so they are heard once and after the summary. -->
        {#if marks}<span class="sr-only" id="{domId}-marks">{marks}</span>{/if}
      </div>
      <p class="summary" id="{domId}-summary">{summary}</p>
    </div>
    <ChevronDown class="chev" size={18} aria-hidden="true" />
  </div>

  {#if open}
    <div class="body" id="{domId}-body" role="group" aria-labelledby="{domId}-title">
      {#if description}<p class="description">{description}</p>{/if}
      {@render children()}
    </div>
  {/if}
</section>

<style>
  .section {
    min-width: 0;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    /* Clear of the sticky top bar when a jump scrolls here. */
    scroll-margin-top: calc(var(--z-topbar-height) + var(--z-space-4));
  }
  .section.open {
    border-color: var(--z-border-strong);
  }

  .head {
    position: relative;
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    min-height: var(--z-control-touch);
    padding: var(--z-space-4) var(--z-space-5);
    border-radius: inherit;
    transition: background-color var(--z-motion-fast) var(--z-ease);
  }
  .head:hover {
    background: var(--z-surface-hover);
  }
  .head:has(.toggle:focus-visible) {
    box-shadow: var(--z-focus-ring);
  }
  .open .head {
    border-bottom: var(--z-border-width) solid var(--z-border);
    border-bottom-left-radius: 0;
    border-bottom-right-radius: 0;
  }

  .text {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: var(--z-nudge-2);
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-1) var(--z-space-3);
  }
  .title {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
  }
  .toggle {
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-text);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  /* The whole header is the target; the ring is drawn on the header, so the
     button's own outline would be a second, smaller one inside it. */
  .toggle::after {
    content: '';
    position: absolute;
    inset: 0;
  }
  .toggle:focus-visible {
    outline: none;
  }

  .summary {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
    /* A long selector or a run of labels wraps rather than pushing the row
       wider than the screen. */
    overflow-wrap: anywhere;
  }

  .mark {
    display: inline-flex;
    align-items: center;
    gap: var(--z-nudge-2);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    font-weight: var(--z-weight-medium);
    white-space: nowrap;
  }
  .mark.problem {
    color: var(--z-danger);
  }
  .mark.edited {
    color: var(--z-accent);
  }

  .head :global(.chev) {
    flex: none;
    color: var(--z-text-subtle);
    transition: transform var(--z-motion-base) var(--z-ease);
  }
  .open .head :global(.chev) {
    transform: rotate(180deg);
  }

  .body {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
    min-width: 0;
    padding: var(--z-space-5);
    animation: reveal var(--z-motion-base) var(--z-ease);
  }
  .description {
    margin: 0;
    max-width: 68ch;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  /*
    The form vocabulary every section body shares.

    The bodies are written as plain fragments -- a field, a paragraph, a group --
    and are laid out here, once, rather than each carrying its own copy of the
    same eight rules: the old Size step alone had four, and the copies had begun to
    disagree about where a pair of controls stacks. `.group` is a fieldset that
    belongs to the section and has a name of its own; `.hint` and `.echo` are
    the two sizes of small print (what a control is, and what the choice
    amounts to); `.pair` puts two short controls side by side until there is
    no room; `.proposal` is a sentence with the one button that acts on it; and
    `.callout` is the cost of something that is allowed.

    These are global rules, so a name here is a name no component inside a
    section may use for something else. `.proposal` was `.lead` until Button's own
    icon wrapper, a span called `.lead`, turned out to be inside every section
    too, and the rule drew a bordered tile round the icon of each button in them.
  */
  .body :global(.group) {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    min-width: 0;
    margin: 0;
    padding: 0;
    border: 0;
  }
  /* The legend is not a flex item, so the group's gap does not reach it. */
  .body :global(.group > legend) {
    padding: 0 0 var(--z-space-4);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  /* The rule between two groups is drawn by the second one's legend rather
     than by its fieldset: a fieldset's border is broken where its legend sits,
     which read as a line trailing off the end of the title. */
  .body :global(.group ~ .group > legend) {
    width: 100%;
    padding-top: var(--z-space-5);
    border-top: var(--z-border-width) solid var(--z-border);
  }
  .body :global(.hint),
  .body :global(.echo) {
    margin: 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    overflow-wrap: anywhere;
  }
  .body :global(.hint) {
    color: var(--z-text-subtle);
  }
  .body :global(.echo) {
    color: var(--z-text-muted);
  }
  .body :global(.hint code),
  .body :global(.echo code) {
    font-family: var(--z-font-mono);
  }
  /* An image reference, a host path or a selector is one long word: it wraps
     wherever it must rather than widening the page past the screen. */
  .body :global(code) {
    overflow-wrap: anywhere;
  }
  .body :global(.pair) {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--z-space-4);
    align-items: start;
  }
  .body :global(.pair > *) {
    min-width: 0;
  }
  .body :global(.proposal) {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-sunken);
  }
  .body :global(.callout) {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .body :global(.callout > svg) {
    flex: none;
  }
  .body :global(.callout p) {
    margin: 0;
    max-width: 70ch;
  }
  .body :global(.callout .callout-title) {
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  /* A callout that is good news rather than a cost: nothing was given up. */
  .body :global(.callout.ok) {
    border-color: var(--z-idle-border);
    background: var(--z-idle-subtle);
  }
  /* A list inside one: a line per host, or per way to change the answer. */
  .body :global(.callout .plan) {
    margin: var(--z-space-2) 0;
    padding-left: var(--z-space-4);
  }
  .body :global(.callout .plan li + li) {
    margin-top: var(--z-space-1);
  }
  @keyframes reveal {
    from {
      opacity: 0;
    }
    to {
      opacity: 1;
    }
  }

  /* --z-bp-md, written out: a media query is evaluated before custom
     properties exist. */
  @media (max-width: 768px) {
    .head {
      padding: var(--z-space-3) var(--z-space-4);
    }
    .body {
      padding: var(--z-space-4);
    }
    .body :global(.pair) {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
