<!--
  One job, in full.

  The grid shows what fits; this shows everything Zoomies recorded, including
  the three timestamps the queue wait and the duration are derived from, so an
  operator can see why a number is what it is rather than trusting it.
-->
<script lang="ts">
  import { formatDuration } from '$lib/format';
  import { jobStatus, UNMATCHED } from '$lib/status';
  import type { Job } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Drawer from '$lib/components/Drawer.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import GitHubLink from './GitHubLink.svelte';
  import JobLabels from './JobLabels.svelte';
  import UnmatchedNote from './UnmatchedNote.svelte';

  interface Props {
    open?: boolean;
    job: Job | null;
    onclose?: () => void;
  }

  let { open = $bindable(false), job, onclose }: Props = $props();

  const status = $derived(job ? jobStatus(job.state, job.conclusion) : undefined);
  const unmatched = $derived(job?.matched === false);
  const running = $derived(job?.state === 'in_progress');
  const waiting = $derived(job?.state === 'queued' && !job?.started_at);
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
        {#if unmatched}<Badge status={UNMATCHED} />{/if}
      </div>

      {#if unmatched}
        <UnmatchedNote labels={job.labels} compact />
      {/if}

      <dl class="facts">
        <dt>Repository</dt>
        <dd>{job.repo || '--'}</dd>

        <dt>Workflow</dt>
        <dd>{job.workflow || '--'}</dd>

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
</style>
