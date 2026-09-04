<!--
  The jobs this pool has run recently. Enough to answer "is it actually doing
  work, and is that work passing" without leaving the page.
-->
<script lang="ts">
  import { ExternalLink, ListChecks } from '@lucide/svelte';
  import type { Job } from '$lib/api/types';
  import { jobStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';

  interface Props {
    jobs: readonly Job[];
    loading?: boolean;
    error?: unknown;
    onretry?: () => void;
    class?: string;
  }

  let { jobs, loading = false, error, onretry, class: className = '' }: Props = $props();
</script>

<div class={className}>
  {#if error}
    <ErrorState {error} {onretry} compact />
  {:else if loading && jobs.length === 0}
    <ul class="list">
      {#each Array.from({ length: 4 }, (_, i) => i) as line (line)}
        <li class="row"><Skeleton width="70%" height="1rem" /></li>
      {/each}
    </ul>
  {:else if jobs.length === 0}
    <EmptyState
      compact
      icon={ListChecks}
      title="No jobs on this pool yet"
      description="Zoomies records a job the first time GitHub tells it about one, so this fills in as soon as a workflow asks for these labels."
    />
  {:else}
    <ul class="list">
      {#each jobs as job (job.id)}
        {@const status = jobStatus(job.state, job.conclusion)}
        <li class="row">
          <Badge {status} size="sm" />
          <span class="what">
            <span class="repo">{job.repo ?? 'unknown repository'}</span>
            <span class="job">{job.job_name ?? job.workflow ?? 'a job'}</span>
          </span>
          <span class="timing">
            {#if job.state === 'queued'}
              waited <Duration from={job.queued_at} live />
            {:else}
              <Duration ms={job.duration_ms ?? null} />
            {/if}
          </span>
          <span class="when"><RelativeTime value={job.queued_at} plain /></span>
          {#if job.html_url}
            <a class="out" href={job.html_url} target="_blank" rel="noreferrer external">
              <ExternalLink size={12} aria-hidden="true" />
              <span class="sr-only">Open this run on GitHub</span>
            </a>
          {:else}
            <span></span>
          {/if}
        </li>
      {/each}
    </ul>
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
    grid-template-columns: auto minmax(0, 1fr) auto auto auto;
    align-items: center;
    gap: var(--z-space-3);
    padding: var(--z-space-2) 0;
    border-bottom: 1px solid var(--z-border);
    font-size: var(--z-text-sm);
  }
  .row:last-child {
    border-bottom: 0;
  }
  .what {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .repo {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--z-text);
  }
  .job {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .timing,
  .when {
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
  .out {
    display: inline-flex;
    color: var(--z-text-subtle);
  }
  .out:hover {
    color: var(--z-accent);
  }
  @media (max-width: 768px) {
    .row {
      grid-template-columns: auto minmax(0, 1fr) auto;
    }
    .timing {
      display: none;
    }
  }
</style>
