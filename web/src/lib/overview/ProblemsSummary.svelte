<!--
  One line on the Overview: how many things need a person, and the way in.

  This used to be the full list, and on a fleet with two settled configuration
  warnings it pushed the pools, the scaling feed and the running jobs below the
  fold -- so the page that exists to show what the fleet is doing spent its best
  space on decisions somebody had already made. The trade this makes is that the
  count and the worst severity stay on the page, in the position the list had,
  and the detail is one click away in the drawer.

  When nothing is wrong it is still one quiet line and never a green banner:
  an operator who glances at this fifty times a day should be told "nothing" in
  the smallest way that can still be read from across the room.
-->
<script lang="ts">
  import { pluralise } from '$lib/format';
  import { severityStatus } from '$lib/status';
  import { notifications, SEVERITY_NOUN } from '$lib/state/notifications.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';

  interface Props {
    loading?: boolean;
    /**
     * True while the first-run checklist above is on screen. The all-clear line
     * is then suppressed: two things competing to explain the same emptiness is
     * worse than one, and only one of them is right.
     */
    setupPending?: boolean;
    class?: string;
  }

  let { loading = false, setupPending = false, class: className = '' }: Props = $props();

  const active = $derived(notifications.active);
  const dismissed = $derived(notifications.dismissed);
  const worst = $derived(notifications.worst);
  const status = $derived(severityStatus(worst ?? 'info'));

  /** "1 error and 2 warnings", worst first. */
  const counts = $derived(
    notifications.groups.map((g) => pluralise(g.items.length, SEVERITY_NOUN[g.severity])),
  );

  /**
   * Say nothing at all while the first-run checklist is on screen with nothing
   * else wrong. "Nothing needs your attention" is true of a working fleet and
   * false of a controller that cannot register a runner: the problems endpoint
   * is deliberately quiet on an unconfigured instance -- one with no
   * installation is not misconfigured, it is unfinished -- so the line stands
   * down and lets the checklist be the message.
   */
  const silent = $derived(!loading && active.length === 0 && setupPending);

  function open(): void {
    notifications.open = true;
  }
</script>

{#if !silent}
  <div class="summary {className}" data-severity={worst ?? 'none'}>
    {#if loading}
      <Skeleton width="14rem" height="var(--z-text-base)" />
    {:else if active.length > 0}
      <StatusDot {status} size="sm" />
      <p class="line">
        {#each counts as count, i (count)}{i > 0 ? ' and ' : ''}<strong>{count}</strong>{/each}
        {active.length === 1 ? 'needs' : 'need'} your attention.
      </p>
      <button type="button" class="review" onclick={open}>Review</button>
    {:else}
      <p class="line quiet">
        Nothing needs your attention.
        {#if dismissed.length > 0}
          <button type="button" class="review" onclick={open}>
            {pluralise(dismissed.length, 'problem')} dismissed
          </button>
        {/if}
      </p>
    {/if}
  </div>
{/if}

<style>
  .summary {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    min-height: var(--z-space-6);
    padding: var(--z-space-2) var(--z-space-3);
    border: 1px solid var(--z-border);
    border-left-width: 3px;
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  /* Nothing wrong is not a card: it is a line of text where the card was. */
  .summary[data-severity='none'] {
    padding: 0;
    border: 0;
    background: none;
  }
  .summary[data-severity='error'] {
    border-left-color: var(--z-danger);
    background: var(--z-danger-subtle);
  }
  .summary[data-severity='warning'] {
    border-left-color: var(--z-pending);
  }
  .line {
    margin: 0;
    min-width: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .line strong {
    font-weight: var(--z-weight-semibold);
  }
  .quiet {
    color: var(--z-text-muted);
  }
  .review {
    margin-left: auto;
    padding: 0 var(--z-space-1);
    border: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    cursor: pointer;
  }
  .quiet .review {
    margin-left: 0;
    color: var(--z-text-subtle);
  }
  .review:hover {
    text-decoration: underline;
  }
</style>
