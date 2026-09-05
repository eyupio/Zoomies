<!--
  Why a job went wrong, said first and said plainly.

  A failed job has two possible authors, and the drawer's first duty is to name
  the right one. When the fleet's own runner stopped under the job, that is
  said before anything else, because GitHub records it as an ordinary failure
  and the workflow's owner will otherwise go looking for a bug that is not
  there. Otherwise the step that failed is named, so the operator knows which
  log to open before they leave for GitHub.
-->
<script lang="ts">
  import { ExternalLink, TriangleAlert } from '@lucide/svelte';
  import type { Job } from '$lib/api/types';
  import { formatDuration, toMillis } from '$lib/format';
  import { jobStatus } from '$lib/status';

  interface Props {
    job: Job;
    class?: string;
  }

  let { job, class: className = '' }: Props = $props();

  const status = $derived(jobStatus(job.state, job.conclusion));
  const step = $derived(job.failed_step ?? null);

  /** How long the failing step ran, when both of its stamps are known. */
  const stepTook = $derived.by(() => {
    if (!step) return null;
    const from = toMillis(step.started_at);
    const to = toMillis(step.completed_at);
    return from === null || to === null ? null : to - from;
  });

  const heading = $derived.by(() => {
    if (job.runner_fault) return 'The runner stopped under this job';
    if (step) return `${status.label} at step ${step.number ?? '?'}, ${step.name ?? 'unnamed'}`;
    if (job.state === 'completed')
      return `${status.label} after ${formatDuration(job.duration_ms)}`;
    return status.label;
  });
</script>

<div class="note {className}" role="note" aria-label="Why this job went wrong">
  <TriangleAlert size={16} aria-hidden="true" class="icon" />
  <div class="body">
    <p class="heading">{heading}</p>
    {#if job.runner_fault}
      <p class="detail">{job.runner_fault}.</p>
      <p class="detail">
        GitHub records this as an ordinary failure; the workflow did nothing wrong. The runner's
        last message says why it stopped -- usually memory, disk, or an operator removing it with
        force.
      </p>
      {#if job.runner_id}
        <a class="action" href="/runners/{job.runner_id}">Open the runner</a>
      {/if}
    {:else if step}
      <p class="detail">
        {#if stepTook !== null}
          The step ran for {formatDuration(stepTook)} before it {step.conclusion === 'timed_out'
            ? 'timed out'
            : step.conclusion === 'cancelled'
              ? 'was cancelled'
              : 'failed'}.
        {/if}
        Every step after it was skipped. Its output is on GitHub.
      </p>
      {#if job.html_url}
        <a class="action" href={job.html_url} target="_blank" rel="noopener noreferrer">
          Open the failed step's log
          <ExternalLink size={13} aria-hidden="true" />
          <span class="sr-only">(opens in a new tab)</span>
        </a>
      {/if}
    {:else}
      <p class="detail">
        GitHub reported the job {status.label.toLowerCase()} without naming a step. The run on GitHub
        has the detail.
      </p>
    {/if}
  </div>
</div>

<style>
  .note {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-3);
    border: 1px solid var(--z-danger-border);
    border-radius: var(--z-radius-md);
    background: var(--z-danger-subtle);
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
    overflow-wrap: anywhere;
  }
  .detail {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    max-width: 70ch;
    overflow-wrap: anywhere;
  }
  .action {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
    color: var(--z-accent);
    text-decoration: none;
  }
  .action:hover {
    text-decoration: underline;
  }
</style>
