<!--
  Step five: what happened.

  One row per repository, in the same order they were reviewed, and every row
  says what it did. A repository that was skipped or failed is as important as
  one that worked: the operator has to know which of the eight repositories
  they picked still needs doing, and a screen that only listed the successes
  would leave them counting.
-->
<script lang="ts">
  import { CircleAlert, CircleCheck, CircleMinus, ExternalLink } from '@lucide/svelte';
  import type { MigrationOutcome } from '$lib/api/types';
  import EmptyState from '$lib/components/EmptyState.svelte';

  interface Props {
    outcome: MigrationOutcome | null;
  }

  let { outcome }: Props = $props();

  const results = $derived(outcome?.results ?? []);
  const icons = { opened: CircleCheck, skipped: CircleMinus, failed: CircleAlert } as const;

  function iconFor(status: string | undefined) {
    return icons[(status ?? 'skipped') as keyof typeof icons] ?? CircleMinus;
  }
</script>

{#if results.length === 0}
  <EmptyState
    title="Nothing was opened"
    description="No repository reached the pull request stage. Go back and check the mapping."
  />
{:else}
  <p class="lede">
    {outcome?.opened ?? 0} opened, {outcome?.skipped ?? 0} skipped, {outcome?.failed ?? 0} failed. Each
    pull request changes only the <code>runs-on</code> lines you reviewed; merging it is the repository's
    own decision.
  </p>

  <ul class="results">
    {#each results as row (row.repo)}
      {@const Icon = iconFor(row.status)}
      <li class={row.status}>
        <Icon size={15} aria-hidden="true" />
        <div class="body">
          <p class="repo">
            {row.repo}
            {#if row.pull_request_url}
              <a href={row.pull_request_url} target="_blank" rel="noopener noreferrer">
                #{row.pull_request_number}
                <ExternalLink size={11} aria-hidden="true" />
              </a>
            {/if}
          </p>
          <p class="detail">
            {#if row.status === 'opened'}
              {row.jobs}
              {row.jobs === 1 ? 'job' : 'jobs'} in {row.workflows}
              {row.workflows === 1 ? 'file' : 'files'}, on <code>{row.branch}</code>
            {:else}
              {row.reason}
            {/if}
          </p>
        </div>
      </li>
    {/each}
  </ul>

  {#if (outcome?.failed ?? 0) > 0}
    <p class="note">
      A failure here leaves that repository exactly as it was: each pull request is opened on its
      own branch, so nothing half-finished is left behind. Fix what the reason says and run the
      wizard again for those repositories.
    </p>
  {/if}
{/if}

<style>
  .lede {
    margin: 0 0 var(--z-space-4);
    max-width: 70ch;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    color: var(--z-text-muted);
  }
  .lede code {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
  }
  .results {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
  }
  .results li {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
  }
  .results li.opened {
    border-color: var(--z-idle-border);
    color: var(--z-idle);
  }
  .results li.failed {
    border-color: var(--z-failed-border);
    color: var(--z-failed);
  }
  .results li.skipped {
    color: var(--z-text-subtle);
  }
  .body {
    min-width: 0;
  }
  .body p {
    margin: 0;
  }
  .repo {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    font-size: var(--z-text-base);
    color: var(--z-text);
  }
  .repo a {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    color: var(--z-accent);
    font-size: var(--z-text-xs);
  }
  .detail {
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .detail code {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-2xs);
  }
  .note {
    margin: var(--z-space-4) 0 0;
    max-width: 70ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
</style>
