<!--
  Everything that needs a person, in one place that is not the middle of a
  page.

  The Overview used to carry this list at full height, above the panels an
  operator actually watches. Two settled warnings were enough to push the pools
  and the running jobs off the first screen, which is the wrong trade: a
  configuration choice made deliberately last month should not cost the fleet
  its visibility every day. So the list moved here, one click from the top bar
  and one click from the Overview's summary line, and what is on the page is a
  sentence saying how many there are.

  Nothing is hidden by moving it: the drawer holds the same four sentences per
  problem, worst first, and what an operator has put away is still listed at the
  bottom, with the date the decision was made.
-->
<script lang="ts">
  import { CheckCheck } from '@lucide/svelte';
  import { pluralise } from '$lib/format';
  import { severityStatus } from '$lib/status';
  import { notifications, SEVERITY_NOUN } from '$lib/state/notifications.svelte';
  import Button from '$lib/components/Button.svelte';
  import Drawer from '$lib/components/Drawer.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import ProblemItem from './ProblemItem.svelte';

  const active = $derived(notifications.active);
  const dismissed = $derived(notifications.dismissed);
  const groups = $derived(notifications.groups);
  const dismissedGroups = $derived(notifications.group(dismissed));

  const description = $derived(
    active.length === 0
      ? 'Nothing needs your attention.'
      : 'Worst first, with the fix the controller suggests.',
  );
</script>

<Drawer
  open={notifications.open}
  onclose={() => (notifications.open = false)}
  title="Problems"
  {description}
  width="lg"
  flush
>
  {#if active.length > 0}
    <div class="bulk">
      <Button
        size="sm"
        variant="ghost"
        icon={CheckCheck}
        onclick={() => notifications.dismissAll()}
      >
        Dismiss all
      </Button>
    </div>
  {/if}

  {#each groups as group (group.severity)}
    {@const status = severityStatus(group.severity)}
    <h3 class="group" data-severity={group.severity}>
      <StatusDot {status} size="sm" />
      {pluralise(group.items.length, SEVERITY_NOUN[group.severity])}
    </h3>
    <ul class="items">
      {#each group.items as problem (problem.code + (problem.target_id ?? '') + problem.title)}
        <ProblemItem {problem} ondismiss={(p) => notifications.dismiss(p)} />
      {/each}
    </ul>
  {/each}

  {#if active.length === 0}
    <!--
      An empty drawer is a result, not a blank: an operator who opened this
      deliberately is owed the same sentence the Overview gives them.
    -->
    <p class="clear">
      {#if dismissed.length > 0}
        Nothing needs your attention. {pluralise(dismissed.length, 'problem')}
        {dismissed.length === 1 ? 'is' : 'are'} dismissed.
      {:else}
        Nothing needs your attention.
      {/if}
    </p>
  {/if}

  {#if dismissed.length > 0}
    <div class="dismissed">
      <button
        type="button"
        class="toggle"
        aria-expanded={notifications.showDismissed}
        onclick={() => (notifications.showDismissed = !notifications.showDismissed)}
      >
        {notifications.showDismissed ? 'Hide' : 'Show'}
        {pluralise(dismissed.length, 'dismissed problem')}
      </button>
      {#if notifications.showDismissed}
        <p class="note">
          A dismissal is forgotten as soon as the controller stops reporting the problem, so the
          same fault happening again is news again.
        </p>
        {#each dismissedGroups as group (group.severity)}
          <ul class="items">
            {#each group.items as problem (problem.code + (problem.target_id ?? '') + problem.title)}
              <ProblemItem
                {problem}
                onrestore={(p) => notifications.restore(p)}
                dismissedAt={notifications.dismissedAt(problem)}
              />
            {/each}
          </ul>
        {/each}
        <div class="bulk">
          <Button size="sm" variant="ghost" onclick={() => notifications.restoreAll()}>
            Restore all
          </Button>
        </div>
      {/if}
    </div>
  {/if}
</Drawer>

<style>
  .bulk {
    display: flex;
    justify-content: flex-end;
    padding: var(--z-space-2) var(--z-space-4);
  }
  .group {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    padding: var(--z-space-2) var(--z-space-4);
    border-top: 1px solid var(--z-border);
    border-bottom: 1px solid var(--z-border);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text-muted);
  }
  .items {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .clear {
    margin: 0;
    padding: var(--z-space-4);
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .dismissed {
    border-top: 1px solid var(--z-border);
  }
  .toggle {
    display: block;
    width: 100%;
    padding: var(--z-space-3) var(--z-space-4);
    border: 0;
    background: none;
    color: var(--z-text-muted);
    font: inherit;
    font-size: var(--z-text-sm);
    text-align: left;
    cursor: pointer;
  }
  .toggle:hover {
    color: var(--z-text);
  }
  .note {
    margin: 0;
    padding: 0 var(--z-space-4) var(--z-space-3);
    max-width: 62ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-subtle);
  }
</style>
