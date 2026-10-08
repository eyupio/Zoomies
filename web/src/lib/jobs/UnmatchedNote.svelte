<!--
  What an unmatched job is, said in full.

  This is the one thing on the Jobs page that an operator cannot work out from
  the row itself: the job is not slow, nothing in this fleet is going to start
  it. The explanation appears where an operator has asked about unmatched jobs
  -- above the grid when the filter is on, and again in the drawer of a job
  that is one. Never above the default grid: see the note in Jobs.svelte.

  It says "this fleet" rather than "never", and it reaches only queued jobs
  whose labels are not GitHub's own or a vendor's. A controller cannot see the
  whole of GitHub: the installation's webhooks cover every job in its
  repositories, and a job on hosted runners, or on another self-hosted provider
  in the same organisation, has labels no pool here claims and runs perfectly
  well. Telling an operator those jobs will never run would be alarming and
  false, so the note names both possibilities and lets them decide which this
  is.

  The one case it can be certain about is a repository no GitHub App
  installation here covers. Then the labels are beside the point -- Zoomies
  holds no credential for that target and could not register a runner there
  whatever a pool advertises -- so the note says that instead, because sending
  an operator to check their runs-on would waste their afternoon.
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
    /** The job's repository, for the single-job case. */
    repo?: string;
    /**
     * The installation covering that repository, empty when none does. Only
     * meaningful alongside repo, and only for a single job.
     */
    installationId?: string;
    /** Offer the link to the pools page. Off inside a drawer that has its own. */
    action?: boolean;
    compact?: boolean;
    class?: string;
  }

  let {
    count,
    labels,
    repo,
    installationId,
    action = true,
    compact = false,
    class: className = '',
  }: Props = $props();

  const one = $derived(count === 1 || (count === undefined && labels !== undefined));
  const uncovered = $derived(one && !!repo && !installationId);

  const heading = $derived(
    uncovered
      ? 'No GitHub App installation here covers this repository'
      : count === undefined
        ? 'No enabled pool here claims these labels'
        : count === 1
          ? '1 queued job has no pool here'
          : `${pluralise(count, 'queued job')} have no pool here`,
  );
</script>

<div class="note {className}" class:compact role="note">
  <TriangleAlert size={16} aria-hidden="true" class="icon" />
  <div class="body">
    <p class="heading">{heading}</p>
    {#if uncovered}
      <p class="detail">
        Zoomies holds no credential for <span class="mono">{repo}</span>, so it cannot register a
        runner there and no pool here can claim this job, whatever labels that pool advertises. If
        this fleet is meant to run it, install the GitHub App on that organisation or repository and
        add the installation here. If another runner provider serves it, this is expected.
      </p>
      {#if action}
        <Button size="sm" variant="secondary" href="/installations">Check the installations</Button>
      {/if}
    {:else}
      <p class="detail">
        No enabled pool here answers
        {#if labels && labels.length > 0}
          <span class="mono">{labels.join(', ')}</span>, so
        {:else}
          their labels, so
        {/if}
        nothing in this fleet will start {one ? 'it' : 'them'}. If {one ? 'it is' : 'they are'} meant
        for this fleet, that is the fault: a typo in <span class="mono">runs-on</span>, or a pool
        that is disabled or was never created. If another runner provider serves those labels, this
        is expected and the job starts there. Jobs on GitHub's own runners are not counted here.
      </p>
      {#if action}
        <Button size="sm" variant="secondary" href="/pools">Check the pools and their labels</Button
        >
      {/if}
    {/if}
  </div>
</div>

<style>
  .note {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .note.compact {
    padding: var(--z-space-3);
  }
  .note :global(.icon) {
    flex: none;
    color: var(--z-pending);
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
