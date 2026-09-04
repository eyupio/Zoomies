<!--
  The job this runner is working on, with the way out to the run on GitHub.

  When something has gone wrong this is usually the first thing an operator
  wants, and the second is the run itself -- so the link is a real link, opened
  in a new tab, and never hidden behind a menu.
-->
<script lang="ts">
  import { ExternalLink } from '@lucide/svelte';
  import type { Job } from '$lib/api/types';
  import { formatDuration } from '$lib/format';
  import { UNMATCHED, jobStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';

  interface Props {
    job?: Job | null;
    /** Shapes the empty wording: a runner that has not registered yet cannot have one. */
    idle?: boolean;
    class?: string;
  }

  let { job, idle = true, class: className = '' }: Props = $props();

  const status = $derived(
    job ? (job.matched === false ? UNMATCHED : jobStatus(job.state, job.conclusion)) : null,
  );
  const running = $derived(job?.state === 'in_progress');
</script>

{#if !job || !status}
  <EmptyState
    compact
    title="Not running a job"
    description={idle
      ? 'It is registered and waiting. The job appears here as soon as GitHub hands it one.'
      : 'It has not registered with GitHub yet, so nothing has been sent to it.'}
  />
{:else}
  <div class="job {className}">
    <div class="head">
      <Badge {status} size="sm" />
      <h3>{job.job_name ?? 'Unnamed job'}</h3>
    </div>

    <p class="where">
      <span class="repo mono">{job.repo ?? 'unknown repository'}</span>
      {#if job.workflow}<span class="sep" aria-hidden="true">·</span><span>{job.workflow}</span
        >{/if}
    </p>

    <dl class="facts">
      <div>
        <dt>Started</dt>
        <dd>
          {#if job.started_at}<RelativeTime value={job.started_at} />{:else}Not yet{/if}
        </dd>
      </div>
      <div>
        <dt>{running ? 'Running for' : 'Took'}</dt>
        <dd>
          {#if running}
            <Duration from={job.started_at} live />
          {:else}
            <Duration ms={job.duration_ms ?? null} />
          {/if}
        </dd>
      </div>
      <div>
        <dt>Queue wait</dt>
        <dd class="tabular">{formatDuration(job.queue_wait_ms ?? null)}</dd>
      </div>
    </dl>

    {#if (job.labels ?? []).length > 0}
      <ul class="labels" aria-label="Labels this job asked for">
        {#each job.labels ?? [] as label (label)}
          <li class="mono">{label}</li>
        {/each}
      </ul>
    {/if}

    <div class="links">
      {#if job.html_url}
        <a class="out" href={job.html_url} target="_blank" rel="noopener noreferrer">
          View the run on GitHub
          <ExternalLink size={13} aria-hidden="true" />
          <span class="sr-only">(opens in a new tab)</span>
        </a>
      {/if}
      {#if job.id}
        <CopyButton value={job.id} label="Copy the job ID" size="sm" />
      {/if}
    </div>
  </div>
{/if}

<style>
  .job {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    min-width: 0;
  }
  .head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .where {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .repo {
    color: var(--z-text);
  }
  .sep {
    color: var(--z-text-subtle);
  }
  .facts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2) var(--z-space-6);
    margin: 0;
  }
  dt {
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-subtle);
  }
  dd {
    margin: var(--z-space-1) 0 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .labels {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1);
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .labels li {
    padding: 0 var(--z-space-2);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-size: var(--z-text-2xs);
    line-height: var(--z-leading-2xs);
    color: var(--z-text-muted);
  }
  .links {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
  }
  .out {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    color: var(--z-accent);
    font-weight: var(--z-weight-medium);
    text-decoration: none;
  }
  .out:hover {
    text-decoration: underline;
  }
</style>
