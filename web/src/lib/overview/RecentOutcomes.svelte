<!--
  How the last jobs ended, failures first.

  The Overview's other panels are the present tense; this is the recent past,
  which is where "is CI broken?" gets answered. A job enters the list the moment
  GitHub reports it over -- one fetch when the page opens, then `job.updated`
  frames -- and a job whose runner stopped under it is marked as the fleet's
  failure rather than the workflow's, because that is the distinction an
  operator on this page is paid to make.
-->
<script lang="ts">
  import { CircleCheck } from '@lucide/svelte';
  import { listJobs } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Job } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { formatDuration, formatNumber, toMillis } from '$lib/format';
  import { jobFailed, jobStatus, RUNNER_LOST } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import Panel from './Panel.svelte';

  interface Props {
    class?: string;
  }

  let { class: className = '' }: Props = $props();

  /** Enough to see whether the morning is going well; the Jobs page has the rest. */
  const SHOWN = 10;
  const FETCH = 30;

  let jobs = $state.raw<Job[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);

  function finishedAt(job: Job): number {
    return toMillis(job.completed_at) ?? toMillis(job.started_at) ?? 0;
  }

  /** Newest first. */
  function order(rows: Job[]): Job[] {
    return rows.sort((a, b) => finishedAt(b) - finishedAt(a));
  }

  async function load(signal?: AbortSignal): Promise<void> {
    try {
      const page = await listJobs(
        { state: ['completed'], managed: true, limit: FETCH, sort: 'completed_at', order: 'desc' },
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
      // Only this fleet's own outcomes, as the default Jobs view: a job that
      // ran on somebody else's runner is not news about this fleet.
      if (job.state === 'completed' && (job.pool_id || job.runner_id)) next.push(job);
      jobs = order(next).slice(0, FETCH);
    }),
  );

  const shown = $derived(jobs.slice(0, SHOWN));
  const failedThisHour = $derived(fleet.stats?.failed ?? 0);

  /** The one phrase there is room for: the step, or the fleet's fault. */
  function why(job: Job): string {
    if (job.runner_fault) return 'the runner stopped under it';
    if (job.failed_step)
      return `at ${job.failed_step.name ?? `step ${job.failed_step.number ?? '?'}`}`;
    return '';
  }
</script>

<Panel
  title="Recent outcomes"
  description="How the fleet's last jobs ended, newest first."
  class={className}
  flush
>
  {#snippet actions()}
    {#if !loading && failedThisHour > 0}
      <Badge tone="danger" label="{formatNumber(failedThisHour)} failed this hour" dot={false} />
    {/if}
  {/snippet}

  {#if error}
    <div class="pad">
      <ErrorState
        {error}
        compact
        title="The recent outcomes could not be loaded"
        onretry={() => void load()}
      />
    </div>
  {:else if loading}
    <p class="sr-only">Loading recent outcomes.</p>
    <ul class="rows" aria-hidden="true">
      {#each [0, 1, 2] as row (row)}
        <li class="row">
          <span class="mark"></span>
          <div class="what"><Skeleton width="60%" height="var(--z-text-base)" /></div>
          <div class="when"><Skeleton width="4rem" height="var(--z-text-xs)" /></div>
        </li>
      {/each}
    </ul>
  {:else if jobs.length === 0}
    <EmptyState
      icon={CircleCheck}
      compact
      title="Nothing has finished yet"
      description="A job appears here the moment GitHub reports it over, with how it ended."
    />
  {:else}
    <ul class="rows">
      {#each shown as job (job.id)}
        {@const status = jobStatus(job.state, job.conclusion)}
        {@const failed = jobFailed(job)}
        <li class="row" class:failed>
          <span class="mark"><StatusDot {status} /></span>
          <div class="what">
            <p class="title">
              <a
                href="/jobs?q={encodeURIComponent(job.job_name ?? '')}&repo={encodeURIComponent(
                  job.repo ?? '',
                )}"
              >
                <span class="workflow">{job.workflow ?? 'Unknown workflow'}</span>
                <span class="sep" aria-hidden="true">/</span>
                <span class="job-name">{job.job_name ?? 'unnamed job'}</span>
              </a>
              {#if job.runner_fault}
                <Badge status={RUNNER_LOST} size="sm" />
              {/if}
            </p>
            <p class="meta">
              <span class="outcome" style="color: {status.colour}">{status.label}</span>
              {#if why(job)}<span>{why(job)}</span>{/if}
              <span aria-hidden="true" class="dot">·</span>
              <span class="repo mono">{job.repo ?? 'unknown repository'}</span>
              {#if job.head_branch}
                <span aria-hidden="true" class="dot">·</span>
                <span class="mono">{job.head_branch}</span>
              {/if}
              <span aria-hidden="true" class="dot">·</span>
              <span class="tabular">{formatDuration(job.duration_ms)}</span>
            </p>
          </div>
          <p class="when"><RelativeTime value={job.completed_at} /></p>
        </li>
      {/each}
    </ul>
    <p class="more">
      <a href="/jobs?failed=true">Every failed job</a>
      <span aria-hidden="true" class="dot">·</span>
      <a href="/jobs?state=completed">All finished jobs</a>
    </p>
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
    grid-template-columns: auto minmax(0, 1fr) auto;
    align-items: start;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-5);
    border-bottom: 1px solid var(--z-border);
  }
  .row.failed {
    background: var(--z-danger-subtle);
  }
  .row:last-child {
    border-bottom: 0;
  }
  .mark {
    display: flex;
    align-items: center;
    height: var(--z-leading-base);
  }
  .what {
    min-width: 0;
  }
  .title {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
  }
  .title a {
    color: var(--z-text);
    text-decoration: none;
    overflow-wrap: anywhere;
  }
  .title a:hover {
    color: var(--z-accent);
    text-decoration: underline;
  }
  .workflow {
    font-weight: var(--z-weight-medium);
  }
  .sep {
    margin: 0 var(--z-space-1);
    color: var(--z-text-subtle);
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-1) var(--z-space-2);
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
    overflow-wrap: anywhere;
  }
  .outcome {
    font-weight: var(--z-weight-medium);
  }
  .dot {
    color: var(--z-text-subtle);
  }
  .when {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-subtle);
    white-space: nowrap;
  }
  .more {
    display: flex;
    gap: var(--z-space-2);
    margin: 0;
    padding: var(--z-space-3) var(--z-space-5);
    border-top: 1px solid var(--z-border);
    font-size: var(--z-text-xs);
  }
  .more a {
    color: var(--z-accent);
  }
</style>
