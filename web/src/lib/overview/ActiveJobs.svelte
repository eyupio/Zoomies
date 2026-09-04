<!--
  What is running right now.

  Jobs arrive here the moment a runner picks one up: one fetch when the page
  opens, then `job.updated` frames from the stream. A job that leaves
  `in_progress` leaves the list, so this is always the present tense.
-->
<script lang="ts">
  import { CircleSlash, ExternalLink } from '@lucide/svelte';
  import { listJobs } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Job } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { formatNumber, toMillis } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import Panel from './Panel.svelte';

  interface Props {
    class?: string;
  }

  let { class: className = '' }: Props = $props();

  /** Enough to see the shape of the fleet; the Jobs page is for the rest. */
  const SHOWN = 12;
  const FETCH = 50;

  let jobs = $state.raw<Job[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);

  function startedAt(job: Job): number {
    return toMillis(job.started_at) ?? toMillis(job.queued_at) ?? 0;
  }

  /** Newest first, and stable: a job's start time does not change under you. */
  function order(rows: Job[]): Job[] {
    return rows.sort((a, b) => startedAt(b) - startedAt(a));
  }

  async function load(signal?: AbortSignal): Promise<void> {
    try {
      const page = await listJobs(
        { state: ['in_progress'], limit: FETCH, sort: 'started_at', order: 'desc' },
        signal,
      );
      jobs = order((page.items ?? []).filter((row) => Boolean(row.id)));
      error = null;
    } catch (cause) {
      if (signal?.aborted) return;
      error = cause;
    } finally {
      if (!signal?.aborted) loading = false;
    }
  }

  $effect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  });

  // One reconciling fetch after a gap in the stream, for the same reason the
  // fleet cache does one: the replay buffer is finite.
  let previous: string | null = null;
  $effect(() => {
    const status = fleet.connection;
    if (previous !== null && previous !== 'live' && status === 'live') void load();
    previous = status;
  });

  $effect(() =>
    events.subscribe('job.updated', (job) => {
      if (!job.id) return;
      const next = jobs.filter((row) => row.id !== job.id);
      if (job.state === 'in_progress') next.push(job);
      jobs = order(next);
    }),
  );

  const shown = $derived(jobs.slice(0, SHOWN));
  const overflow = $derived(Math.max(0, jobs.length - SHOWN));

  function runnerName(job: Job): string {
    return job.runner_name ?? fleet.runner(job.runner_id)?.name ?? 'Unassigned';
  }
</script>

<Panel
  title="Active jobs"
  description="What the fleet is running at this moment."
  class={className}
  flush
>
  {#snippet actions()}
    {#if !loading && jobs.length > 0}
      <Badge tone="accent" label="{formatNumber(jobs.length)} running" dot={false} />
    {/if}
  {/snippet}

  {#if error}
    <div class="pad">
      <ErrorState
        {error}
        compact
        title="The running jobs could not be loaded"
        onretry={() => void load()}
      />
    </div>
  {:else if loading}
    <p class="sr-only">Loading running jobs.</p>
    <ul class="rows" aria-hidden="true">
      {#each [0, 1, 2] as row (row)}
        <li class="row">
          <div class="what"><Skeleton width="60%" height="var(--z-text-base)" /></div>
          <div class="who"><Skeleton width="8rem" height="var(--z-text-xs)" /></div>
          <div class="elapsed"><Skeleton width="3rem" height="var(--z-text-xs)" /></div>
        </li>
      {/each}
    </ul>
  {:else if jobs.length === 0}
    <EmptyState
      icon={CircleSlash}
      compact
      title="Nothing is running right now"
      description="A job appears here the moment a runner picks it up."
    />
  {:else}
    <ul class="rows">
      {#each shown as job (job.id)}
        <li class="row">
          <div class="what">
            {#if job.html_url}
              <a
                class="title"
                href={job.html_url}
                target="_blank"
                rel="noreferrer noopener"
                title="Open this run on GitHub"
              >
                <span class="repo mono">{job.repo ?? 'Unknown repository'}</span>
                <span class="workflow">{job.workflow ?? 'Unknown workflow'}</span>
                <ExternalLink size={12} aria-hidden="true" />
                <span class="sr-only">Opens on GitHub in a new tab</span>
              </a>
            {:else}
              <span class="title">
                <span class="repo mono">{job.repo ?? 'Unknown repository'}</span>
                <span class="workflow">{job.workflow ?? 'Unknown workflow'}</span>
              </span>
            {/if}
            {#if job.job_name}<p class="job-name">{job.job_name}</p>{/if}
          </div>
          <p class="who">
            <span class="sr-only">Runner </span>
            {#if job.runner_id}
              <a href="/runners/{job.runner_id}">{runnerName(job)}</a>
            {:else}
              <span class="muted">{runnerName(job)}</span>
            {/if}
          </p>
          <p class="elapsed">
            <span class="sr-only">Running for </span>
            <Duration from={job.started_at} live />
          </p>
        </li>
      {/each}
    </ul>
    {#if overflow > 0}
      <p class="more">
        <a href="/jobs?state=in_progress">and {formatNumber(overflow)} more on the jobs page</a>
      </p>
    {/if}
  {/if}
</Panel>

<style>
  .pad {
    padding: var(--z-space-4) var(--z-space-5);
  }
  .rows {
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(6rem, 12rem) 5rem;
    align-items: center;
    gap: var(--z-space-4);
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  .row:last-child {
    border-bottom: 0;
  }
  .what {
    min-width: 0;
  }
  .title {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-1) var(--z-space-2);
    color: var(--z-text);
    text-decoration: none;
  }
  a.title:hover .workflow,
  a.title:hover .repo {
    color: var(--z-accent);
    text-decoration: underline;
  }
  .repo {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .workflow {
    font-size: var(--z-text-base);
    font-weight: var(--z-weight-medium);
    overflow-wrap: anywhere;
  }
  .job-name {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    overflow-wrap: anywhere;
  }
  .who {
    margin: 0;
    min-width: 0;
    font-size: var(--z-text-xs);
    overflow-wrap: anywhere;
  }
  .who a {
    color: var(--z-accent);
    text-decoration: none;
  }
  .who a:hover {
    text-decoration: underline;
  }
  .muted {
    color: var(--z-text-subtle);
  }
  .elapsed {
    margin: 0;
    justify-self: end;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .more {
    margin: 0;
    padding: var(--z-space-3) var(--z-space-5);
    border-top: 1px solid var(--z-border);
    font-size: var(--z-text-xs);
  }
  .more a {
    color: var(--z-accent);
  }
  @media (max-width: 768px) {
    .row {
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--z-space-1) var(--z-space-3);
    }
    .what {
      grid-column: 1 / -1;
    }
    .elapsed {
      justify-self: end;
    }
  }
</style>
