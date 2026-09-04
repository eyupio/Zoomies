<!--
  What an unmatched job is, said in full.

  This is the one thing on the Jobs page that an operator cannot work out from
  the row itself: the job is not slow, it is never going to start. The
  explanation appears wherever unmatched jobs do -- above the grid when the
  filter is on or the page contains one, and again in the drawer.
-->
<script lang="ts">
  import { TriangleAlert } from '@lucide/svelte';
  import { pluralise } from '$lib/format';
  import Button from '$lib/components/Button.svelte';

  interface Props {
    /** How many unmatched jobs are in view, when that is known. */
    count?: number;
    /** The labels of the job being explained, for the single-job case. */
    labels?: readonly string[];
    /** Offer the link to the pools page. Off inside a drawer that has its own. */
    action?: boolean;
    compact?: boolean;
    class?: string;
  }

  let { count, labels, action = true, compact = false, class: className = '' }: Props = $props();

  const heading = $derived(
    count === undefined
      ? 'No enabled pool claims these labels'
      : count === 1
        ? '1 job here will never run'
        : `${pluralise(count, 'job')} here will never run`,
  );
</script>

<div class="note {className}" class:compact role="note">
  <TriangleAlert size={16} aria-hidden="true" class="icon" />
  <div class="body">
    <p class="heading">{heading}</p>
    <p class="detail">
      No enabled pool answers
      {#if labels && labels.length > 0}
        <span class="mono">{labels.join(', ')}</span>, so GitHub
      {:else}
        their labels, so GitHub
      {/if}
      has nothing to hand the job to and it will sit queued until it is cancelled. It is almost always
      a typo in <span class="mono">runs-on</span>, and otherwise a pool that is disabled or was
      never created.
    </p>
    {#if action}
      <Button size="sm" variant="secondary" href="/pools">Check the pools and their labels</Button>
    {/if}
  </div>
</div>

<style>
  .note {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
  }
  .note.compact {
    padding: var(--z-space-3);
  }
  .note :global(.icon) {
    flex: none;
    color: var(--z-danger);
  }
  .body {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .heading {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  .detail {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text);
    max-width: 78ch;
  }
</style>
