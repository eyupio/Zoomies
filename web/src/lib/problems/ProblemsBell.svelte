<!--
  The top bar's standing answer to "is anything wrong?".

  It is the one thing on screen that follows an operator from page to page, so
  it carries the count and nothing else: a shape and a colour for the worst
  severity, the number beside it, and the whole list one click away. A fleet
  with nothing wrong -- the common case -- gets a quiet outline and no badge,
  because a bell that is always red is a bell nobody looks at.

  This is also the only place the count is announced, so moving between pages
  does not read the same sentence again.
-->
<script lang="ts">
  import { Bell } from '@lucide/svelte';
  import { joinWords, pluralise } from '$lib/format';
  import { notifications, SEVERITY_NOUN } from '$lib/state/notifications.svelte';

  const active = $derived(notifications.active);
  const worst = $derived(notifications.worst);

  /** "1 error and 2 warnings", in severity order. */
  const counted = $derived(
    joinWords(
      notifications.groups.map((g) => pluralise(g.items.length, SEVERITY_NOUN[g.severity])),
    ),
  );

  const sentence = $derived(
    active.length === 0
      ? 'Nothing needs your attention.'
      : `${counted} ${active.length === 1 ? 'needs' : 'need'} your attention.`,
  );
</script>

<button
  type="button"
  class="bell"
  data-severity={worst ?? 'none'}
  aria-label="Problems. {sentence}"
  aria-haspopup="dialog"
  aria-expanded={notifications.open}
  title={sentence}
  onclick={() => (notifications.open = true)}
>
  <Bell size={15} aria-hidden="true" />
  {#if active.length > 0}
    <span class="count" aria-hidden="true">{active.length}</span>
  {/if}
</button>

<output class="sr-only" aria-live="polite">{sentence}</output>

<style>
  .bell {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    height: var(--z-space-6);
    padding: 0 var(--z-space-2);
    border: 1px solid transparent;
    border-radius: var(--z-radius-md);
    background: none;
    color: var(--z-text-muted);
    font-family: inherit;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    cursor: pointer;
  }
  .bell:hover {
    border-color: var(--z-border);
    background: var(--z-surface);
    color: var(--z-text);
  }
  .count {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-width: var(--z-space-4);
    padding: 0 var(--z-space-1);
    border-radius: var(--z-radius-full);
    font-variant-numeric: tabular-nums;
  }
  /* Colour is never the only carrier: the number is the count, and the label
     spells out how many of each severity there are. */
  .bell[data-severity='error'] {
    color: var(--z-danger);
  }
  .bell[data-severity='error'] .count {
    background: var(--z-danger-subtle);
  }
  .bell[data-severity='warning'] {
    color: var(--z-pending);
  }
  .bell[data-severity='warning'] .count {
    background: var(--z-pending-subtle);
  }
  .bell[data-severity='info'] .count {
    background: var(--z-surface-sunken);
  }
</style>
