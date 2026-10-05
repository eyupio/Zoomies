<!--
  The pool editor's one sticky layer: what the controller makes of the pool so
  far, and the button that saves it.

  The wizard it replaces had a footer that said "Step 1 of 8" and a Next button
  that was disabled until the step was valid -- so the one control that could
  explain what was wrong was the one that could not be pressed. Here the button
  is enabled whenever there is something to save, and pressing it while
  something is wrong shows what: the editor opens the first section that has a
  problem and puts the cursor in it. The sentence beside it says the same thing
  before the button is pressed, and is a live region so that it is also heard.

  On a phone it sits above the navigation bar that is fixed to the bottom edge
  there (the same clearance the shell's footer reserves), and shows nothing
  unless there is something to act on: a line of good news is not worth a
  third of the height a form has left. The sentence is still in the page for a
  screen reader.
-->
<script lang="ts">
  import { CircleCheck, Info, TriangleAlert } from '@lucide/svelte';
  import Button from '$lib/components/Button.svelte';

  interface Props {
    tone: 'neutral' | 'ok' | 'warning' | 'danger';
    message: string;
    /** A way to act on the message, such as opening the first thing to fix. */
    actionLabel?: string;
    onaction?: () => void;
    submitLabel: string;
    /** False when there is nothing to save, so pressing the button would do nothing. */
    canSubmit: boolean;
    busy: boolean;
    onsubmit: () => void;
    oncancel: () => void;
  }

  let {
    tone,
    message,
    actionLabel,
    onaction,
    submitLabel,
    canSubmit,
    busy,
    onsubmit,
    oncancel,
  }: Props = $props();
</script>

<div class="bar">
  <p class="status" data-tone={tone} role="status">
    {#if tone === 'danger' || tone === 'warning'}
      <TriangleAlert size={15} aria-hidden="true" />
    {:else if tone === 'ok'}
      <CircleCheck size={15} aria-hidden="true" />
    {:else}
      <Info size={15} aria-hidden="true" />
    {/if}
    <span class="text">{message}</span>
    {#if actionLabel && onaction}
      <button type="button" class="act" onclick={onaction}>{actionLabel}</button>
    {/if}
  </p>
  <div class="buttons">
    <Button variant="ghost" onclick={oncancel} disabled={busy}>Cancel</Button>
    <Button variant="primary" onclick={onsubmit} disabled={!canSubmit} loading={busy}>
      {submitLabel}
    </Button>
  </div>
</div>

<style>
  .bar {
    position: sticky;
    bottom: var(--z-space-4);
    z-index: var(--z-layer-sticky);
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3) var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-4);
    border: var(--z-border-width) solid var(--z-border-strong);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-md);
  }
  .status {
    flex: 1 1 18rem;
    min-width: 0;
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text-muted);
  }
  .status :global(svg) {
    flex: none;
    /* Level with the first line of text, whatever the line height is. */
    margin-top: var(--z-nudge-2);
  }
  .status[data-tone='ok'] {
    color: var(--z-idle);
  }
  .status[data-tone='warning'] {
    color: var(--z-pending);
  }
  .status[data-tone='danger'] {
    color: var(--z-danger);
  }
  .text {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .act {
    flex: none;
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    font-weight: var(--z-weight-medium);
    text-decoration: underline;
    cursor: pointer;
  }
  .buttons {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin-left: auto;
  }

  /* --z-bp-md, written out: a media query is evaluated before custom
     properties exist. */
  @media (max-width: 768px) {
    .bar {
      bottom: calc(var(--z-space-16) + var(--z-safe-bottom));
      padding: var(--z-space-3);
    }
    /* Out of sight, not out of the page: `display: none` would also take the
       reason Save is off away from a screen reader, which is the one place a
       disabled button explains nothing. The recipe is the global .sr-only's,
       which a media query cannot apply as a class. */
    .status[data-tone='ok'],
    .status[data-tone='neutral'] {
      position: absolute;
      width: var(--z-nudge-1);
      height: var(--z-nudge-1);
      padding: 0;
      margin: calc(-1 * var(--z-nudge-1));
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
    }
    .buttons {
      flex: 1 1 100%;
    }
    .buttons :global(button:last-child) {
      flex: 1 1 auto;
    }
  }
</style>
