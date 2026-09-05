<!--
  One job, in full, and live.

  The grid shows what fits; this shows everything Zoomies recorded and what it
  is doing about it now. Reading order is the operator's question order: what
  went wrong (or what the fleet is doing while the job waits), then the facts,
  then the steps, then the story of how it got here. The job it shows is
  replaced on every `job.updated` frame for it by the page that owns the drawer,
  and the timeline refetches itself on the same frames, so nothing in here is
  older than the stream.
-->
<script lang="ts">
  import { formatDuration, shortId } from '$lib/format';
  import { jobFailed, jobStatus, RUNNER_LOST, stuckUnmatched, UNMATCHED } from '$lib/status';
  import type { Job } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Drawer from '$lib/components/Drawer.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import GitHubLink from './GitHubLink.svelte';
  import JobLabels from './JobLabels.svelte';
  import JobOutcome from './JobOutcome.svelte';
  import JobSteps from './JobSteps.svelte';
  import JobTimeline from './JobTimeline.svelte';
  import JobWaiting from './JobWaiting.svelte';
  import UnmatchedNote from './UnmatchedNote.svelte';

  interface Props {
    open?: boolean;
    job: Job | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), job, onclose }: Props = $props();

  const status = $derived(job ? jobStatus(job.state, job.conclusion) : undefined);
  const unmatched = $derived(job ? stuckUnmatched(job) : false);
  const failed = $derived(job ? jobFailed(job) : false);
  const running = $derived(job?.state === 'in_progress');
  const waiting = $derived(job?.state === 'queued' && !job?.started_at);
  const steps = $derived(job?.steps ?? []);
</script>

<Drawer
  bind:open
  title={job?.job_name || 'Job'}
  description={job?.repo ? `${job.repo} · ${job.workflow ?? 'workflow'}` : undefined}
  {onclose}
>
  {#if job}
    <div class="stack">
      <div class="badges">
        {#if status}<Badge {status} />{/if}
        {#if job.runner_fault}<Badge status={RUNNER_LOST} />{/if}
        {#if unmatched}<Badge status={UNMATCHED} />{/if}
      </div>

      {#if failed}
        <JobOutcome {job} />
      {:else if unmatched}
        <UnmatchedNote labels={job.labels} compact />
      {:else if waiting && job.pool_id}
        <JobWaiting {job} />
      {/if}

      <dl class="facts">
        <dt>Repository</dt>
        <dd>{job.repo || '--'}</dd>

        <dt>Workflow</dt>
        <dd>{job.workflow || '--'}</dd>

        <dt>Branch</dt>
        <dd>
          {#if job.head_branch}
            <span class="mono">{job.head_branch}</span>
            {#if job.head_sha}<span class="muted mono"> @ {shortId(job.head_sha, 7)}</span>{/if}
          {:else}
            <span class="muted">Not reported</span>
          {/if}
        </dd>

        {#if (job.run_attempt ?? 0) > 1}
          <dt>Attempt</dt>
          <dd>{job.run_attempt} <span class="muted">(the run was re-run)</span></dd>
        {/if}

        <dt>Labels</dt>
        <dd><JobLabels labels={job.labels} max={0} /></dd>

        <dt>Pool</dt>
        <dd>
          {#if job.pool_id}
            <a href="/pools/{job.pool_id}">{job.pool_name || job.pool_id}</a>
          {:else}
            <span class="muted">No pool claimed it</span>
          {/if}
        </dd>

        <dt>Runner</dt>
        <dd>
          {#if job.runner_id}
            <a href="/runners/{job.runner_id}">{job.runner_name || job.runner_id}</a>
            {#if running}
              <span class="muted"> · </span>
              <a href="/runners/{job.runner_id}">Follow its output</a>
            {/if}
          {:else if job.runner_name}
            <span class="mono">{job.runner_name}</span>
            <span class="muted"> (not managed by this fleet)</span>
          {:else}
            <span class="muted">Not started on a runner yet</span>
          {/if}
        </dd>

        <dt>Queued</dt>
        <dd><RelativeTime value={job.queued_at} /></dd>

        <dt>Started</dt>
        <dd>
          {#if job.started_at}<RelativeTime value={job.started_at} />{:else}<span class="muted"
              >Not started</span
            >{/if}
        </dd>

        <dt>Completed</dt>
        <dd>
          {#if job.completed_at}<RelativeTime value={job.completed_at} />{:else}<span class="muted"
              >Not finished</span
            >{/if}
        </dd>

        <dt>Queue wait</dt>
        <dd class="tabular">
          {#if waiting}
            <Duration from={job.queued_at} live /> so far
          {:else}
            {formatDuration(job.queue_wait_ms)}
          {/if}
        </dd>

        <dt>Duration</dt>
        <dd class="tabular">
          {#if running && job.started_at}
            <Duration from={job.started_at} live /> so far
          {:else}
            {formatDuration(job.duration_ms)}
          {/if}
        </dd>

        <dt>Job ID</dt>
        <dd><CopyButton value={job.id ?? ''} label="Copy the job ID" showValue /></dd>
      </dl>

      {#if steps.length > 0 || running}
        <section class="section" aria-labelledby="job-steps-heading">
          <h3 id="job-steps-heading">Steps</h3>
          <JobSteps {steps} failedStep={job.failed_step ?? null} {running} />
        </section>
      {/if}

      {#if job.id}
        <section class="section" aria-labelledby="job-timeline-heading">
          <h3 id="job-timeline-heading">Timeline</h3>
          <JobTimeline jobId={job.id} conclusion={job.conclusion} />
        </section>
      {/if}
    </div>
  {/if}

  {#snippet footer()}
    <GitHubLink href={job?.html_url} label="Open the run on GitHub" showLabel variant="button" />
  {/snippet}
</Drawer>

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .badges {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .facts {
    display: grid;
    grid-template-columns: minmax(0, 9rem) minmax(0, 1fr);
    gap: var(--z-space-2) var(--z-space-4);
    margin: 0;
    font-size: var(--z-text-base);
  }
  dt {
    color: var(--z-text-muted);
  }
  dd {
    margin: 0;
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .muted {
    color: var(--z-text-subtle);
  }
  a {
    color: var(--z-accent);
  }
  .section {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    font-weight: var(--z-weight-semibold);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--z-text-subtle);
  }
</style>
