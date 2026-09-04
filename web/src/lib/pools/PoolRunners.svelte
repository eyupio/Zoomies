<!--
  The pool's live runners, compact enough to sit beside everything else on the
  detail page. It reads straight from the fleet cache, so it follows the SSE
  stream without a request of its own.
-->
<script lang="ts">
  import { Cpu } from '@lucide/svelte';
  import type { Runner } from '$lib/api/types';
  import { runnerStatus } from '$lib/status';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';

  interface Props {
    runners: readonly Runner[];
    loading?: boolean;
    /** How many to list before linking to the full grid. */
    max?: number;
    /** Where "see all" goes. */
    allHref?: string;
    class?: string;
  }

  let { runners, loading = false, max = 12, allHref, class: className = '' }: Props = $props();

  const shown = $derived(runners.slice(0, max));
  const hidden = $derived(Math.max(0, runners.length - shown.length));
</script>

<div class={className}>
  {#if loading && runners.length === 0}
    <ul class="list">
      {#each Array.from({ length: 3 }, (_, i) => i) as line (line)}
        <li class="row"><Skeleton width="60%" height="1rem" /></li>
      {/each}
    </ul>
  {:else if runners.length === 0}
    <EmptyState
      compact
      icon={Cpu}
      title="No runners right now"
      description="That is normal when nothing is queued — runners are created on demand."
    />
  {:else}
    <ul class="list">
      {#each shown as runner (runner.id)}
        {@const status = runnerStatus(runner.state)}
        <li class="row">
          <a class="name" href="/runners/{runner.id}">
            <StatusDot {status} size="sm" />
            <span class="mono">{runner.name ?? runner.id}</span>
          </a>
          <span class="state">{status.label}</span>
          <span class="job">
            {#if runner.current_job}
              {runner.current_job.repo ?? ''}
              <span class="sep">·</span>
              {runner.current_job.job_name ?? runner.current_job.workflow ?? 'a job'}
            {:else if runner.host_name}
              on {runner.host_name}
            {/if}
          </span>
          <span class="when">
            <RelativeTime value={runner.started_at ?? runner.created_at} plain />
          </span>
        </li>
      {/each}
    </ul>
    {#if hidden > 0 && allHref}
      <p class="more"><a href={allHref}>See all {runners.length} runners in this pool</a></p>
    {/if}
  {/if}
</div>

<style>
  .list {
    display: flex;
    flex-direction: column;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(0, 1.2fr) auto minmax(0, 1.4fr) auto;
    align-items: center;
    gap: var(--z-space-3);
    padding: var(--z-space-2) 0;
    border-bottom: 1px solid var(--z-border);
    font-size: var(--z-text-sm);
  }
  .row:last-child {
    border-bottom: 0;
  }
  .name {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
    color: var(--z-accent);
    text-decoration: none;
  }
  .name:hover {
    text-decoration: underline;
  }
  .name .mono {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .state {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .job {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--z-text-muted);
  }
  .sep {
    color: var(--z-text-subtle);
  }
  .when {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    white-space: nowrap;
  }
  .more {
    margin: var(--z-space-3) 0 0;
    font-size: var(--z-text-xs);
  }
  .more a {
    color: var(--z-accent);
  }
  @media (max-width: 768px) {
    .row {
      grid-template-columns: minmax(0, 1fr) auto;
    }
    .job {
      display: none;
    }
  }
</style>
