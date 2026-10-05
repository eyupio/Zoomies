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
  import { CircleX } from '@lucide/svelte';
  import { cancelJobWorkflow, rerunJobWorkflow } from '$lib/api/client';
  import { formatDuration, formatMegabytes, formatNumber, shortId } from '$lib/format';
  import {
    HOSTED,
    jobFailed,
    jobStatus,
    queueStatus,
    OOM_KILLED,
    RUNNER_LOST,
    stuckUnmatched,
    UNMATCHED,
  } from '$lib/status';
  import type { Job } from '$lib/api/types';
  import { allocationWords } from '$lib/runners/allocation';
  import { jobSize } from './size';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
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
  // What an operator has done to this job's demand, where they have done
  // anything. The panel below explains it in a sentence; this is so that the
  // drawer's first line already says it, as the grid row behind it does.
  const queue = $derived(job ? queueStatus(job) : undefined);
  const unmatched = $derived(job ? stuckUnmatched(job) : false);
  const failed = $derived(job ? jobFailed(job) : false);
  const running = $derived(job?.state === 'in_progress');
  const waiting = $derived(job?.state === 'queued' && !job?.started_at);
  /**
   * Held by GitHub for a deployment review. It is not queued and it is not
   * this fleet's to start, so every panel below that explains a wait is about
   * something else -- which left a held job as the one kind that sat in the
   * drawer with nothing said about it at all.
   */
  const held = $derived(job?.state === 'waiting');
  // The size of the runner that took the job, and why it was that size.
  const grantedSize = $derived(
    allocationWords(job?.granted_cpus, job?.granted_memory_mb, job?.granted_source),
  );
  // Which class of host the job was put in, why, and where it ran. Null for a
  // job nobody classed -- every job while size routing is off -- so the drawer
  // of a fleet that has not turned it on is the one it always was.
  const size = $derived(job ? jobSize(job) : null);
  /**
   * Anything still waiting on something, whichever kind of waiting it is. One
   * panel answers all of them now, because the controller's explanation makes
   * the distinctions itself -- and a second opinion computed here is exactly
   * what that endpoint replaced.
   */
  const pending = $derived(waiting || held);
  const steps = $derived(job?.steps ?? []);
  const canCancel = $derived(
    session.meta?.workflow_cancellation_enabled === true &&
      session.can('operator') &&
      Boolean(job?.id) &&
      (job?.github_run_id ?? 0) > 0 &&
      job?.state !== 'completed',
  );
  /**
   * The re-run is offered on a finished job that failed, to an operator. It is
   * deliberately not restricted to failures the fleet caused: the server allows
   * either, and somebody who has read a failure and decided to run it again is
   * entitled to. What the fault domain does is decide how loudly it is offered
   * -- the panel puts it beside the fleet's own admission, which is where it
   * answers the question the reader has just been asked.
   */
  const canRerun = $derived(
    session.can('operator') &&
      Boolean(job?.id) &&
      (job?.github_run_id ?? 0) > 0 &&
      job?.state === 'completed' &&
      failed,
  );
  let rerunning = $state(false);
  let cancelOpen = $state(false);
  let forceCancel = $state(false);

  async function rerun(): Promise<void> {
    if (!job?.id || rerunning) return;
    rerunning = true;
    try {
      await rerunJobWorkflow(job.id);
      toasts.success(
        'Re-run requested',
        'GitHub is running this job again, together with any job that needs it. They arrive as a new run attempt.',
      );
    } catch (cause) {
      toasts.fromError(cause, 'GitHub did not accept the re-run');
    } finally {
      rerunning = false;
    }
  }

  async function confirmCancel(): Promise<boolean> {
    if (!job?.id) return false;
    try {
      await cancelJobWorkflow(job.id, { force: forceCancel });
      toasts.success(
        forceCancel ? 'Force cancellation requested' : 'Cancellation requested',
        'Zoomies is waiting for GitHub to confirm the workflow run has ended.',
      );
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'GitHub did not accept the cancellation');
      return false;
    }
  }
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
        {#if queue}<Badge status={queue} title={queue.hint} />{/if}
        {#if job.runner_fault}<Badge status={RUNNER_LOST} />{/if}
        {#if unmatched}<Badge status={UNMATCHED} />{:else if job.hosted && !job.matched}<Badge
            status={HOSTED}
          />{/if}
      </div>

      {#if failed}
        <JobOutcome {job} onRerun={canRerun ? rerun : null} {rerunning} />
      {:else if unmatched}
        <UnmatchedNote
          labels={job.labels}
          repo={job.repo}
          installationId={job.installation_id}
          compact
        />
      {:else if pending}
        <JobWaiting {job} />
      {/if}

      <dl class="facts">
        <dt>Repository</dt>
        <dd>{job.repo || '--'}</dd>

        <dt>Workflow</dt>
        <dd>
          {job.workflow || '--'}
          {#if job.run_number}<span class="muted">#{job.run_number}</span>{/if}
        </dd>

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
        <dd><JobLabels labels={job.labels} max={0} wrap /></dd>

        <dt>Pool</dt>
        <dd>
          {#if job.pool_id}
            <a href="/pools/{job.pool_id}">{job.pool_name || job.pool_id}</a>
          {:else}
            <span class="muted"
              >{job.hosted
                ? "No pool here claimed it; its labels name GitHub's own runners or a vendor's"
                : 'No pool claimed it'}</span
            >
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

        <!--
          Which build claimed the job, so a slow or failed one can be placed on a
          release. Absent, not blank, on a job from before it was recorded or one
          no pool here claimed: nothing is guessed, and saying so is the point.
        -->
        <dt>Controller version</dt>
        <dd data-testid="job-controller-version">
          {#if job.controller_version}
            <span class="mono">{job.controller_version}</span>
            {#if job.controller_channel}<span class="muted"> · {job.controller_channel}</span>{/if}
          {:else}
            <span class="muted">Not recorded</span>
          {/if}
        </dd>

        {#if job.agent_version}
          <dt>Agent version</dt>
          <dd><span class="mono">{job.agent_version}</span></dd>
        {/if}

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
          {#if held}
            <!--
              The time a held job spends is GitHub's, not the queue's. Counting
              it here would charge this fleet for a review it cannot influence,
              and the scheduling figures are measured from the approval for
              exactly that reason.
            -->
            <span class="muted">Not queued yet</span>
          {:else if waiting}
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

        {#if grantedSize}
          <!-- What the runner that took this job was given, copied when it took
               it and never rewritten: the pool and the host may have changed
               since, and "it ran out of memory" is a different question
               depending on which size it ran at. -->
          <dt>Runner size</dt>
          <dd class="tabular" data-testid="job-granted-size">{grantedSize}</dd>
        {/if}

        {#if size}
          <!-- Three facts, kept apart because they are asked in this order: the
               class it was put in and why, the pool it was sent to when that was
               not its own class, and the class of the host that took it. The
               sentences are the controller's; a job that ran somewhere other
               than it was sent is explained, not blamed. -->
          <dt>Size class</dt>
          <dd data-testid="job-size-class">
            <Badge tone="neutral" size="sm" dot={false} label={size.word} />
            {#if size.basisWords}<span class="muted"> · {size.basisWords}</span>{/if}
            {#if size.because}<span class="why">{size.because}</span>{/if}
          </dd>

          {#if size.fallback}
            <dt>Sent to</dt>
            <dd data-testid="job-size-fallback">
              <Badge tone="neutral" size="sm" dot={false} label="Fallback" />
              <span>the {size.fallback.to} pool, not its own class.</span>
              {#if size.fallback.note}<span class="why">{size.fallback.note}</span>{/if}
            </dd>
          {/if}

          {#if size.ran}
            <dt>Ran on</dt>
            <dd data-testid="job-size-ran">{size.ran.text}</dd>
          {/if}

          {#if size.throttledPercent !== null && size.throttledPercent > 0}
            <dt>CPU held back</dt>
            <dd class="tabular" data-testid="job-throttled">
              {size.throttledPercent}%
              <span class="muted"
                >of its CPU periods were cut short by the limit its runner had. A job held back
                often is one that wants a larger class.</span
              >
            </dd>
          {/if}
        {/if}

        {#if job.peak_cpus || job.peak_memory_mb || job.oom_killed}
          <dt>Peak usage</dt>
          <dd class="tabular" data-testid="job-peak-usage">
            {[
              job.peak_cpus ? `${formatNumber(job.peak_cpus)} CPU` : '',
              job.peak_memory_mb ? formatMegabytes(job.peak_memory_mb) : '',
            ]
              .filter(Boolean)
              .join(' · ') || '--'}
            {#if job.oom_killed}
              <Badge status={OOM_KILLED} />
            {/if}
          </dd>
        {/if}

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
    <div class="footer-actions">
      {#if canCancel}
        <Button variant="danger" icon={CircleX} onclick={() => (cancelOpen = true)}>
          Cancel workflow
        </Button>
      {/if}
      <GitHubLink
        href={job?.html_url}
        runNumber={job?.run_number}
        label="Open the run on GitHub"
        showLabel
        variant="button"
      />
    </div>
  {/snippet}
</Drawer>

<ConfirmDialog
  bind:open={cancelOpen}
  title="Cancel workflow run"
  name={job?.workflow || job?.job_name || 'workflow run'}
  description="GitHub can only cancel the whole workflow run. Every queued or running job in this run will be stopped, not only the job shown here."
  consequences={[
    `Run ${job?.run_number ? '#' + job.run_number : (job?.github_run_id ?? '')} in ${job?.repo ?? 'GitHub'} will be cancelled.`,
  ]}
  confirmLabel={forceCancel ? 'Force cancel run' : 'Cancel run'}
  onconfirm={confirmCancel}
  oncancel={() => (forceCancel = false)}
>
  <Checkbox
    bind:checked={forceCancel}
    label="Force cancellation"
    description="Use this only if GitHub leaves an ordinary cancellation stuck. It bypasses conditions that would otherwise keep the run alive."
  />
</ConfirmDialog>

<style>
  .stack {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-5);
  }
  .footer-actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: flex-end;
    gap: var(--z-space-2);
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
  /* The controller's sentence about a decision, on its own line under the badge
     that says what it decided, so a long one wraps under the answer and not
     beside it. */
  .why {
    display: block;
    margin-top: var(--z-space-1);
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
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
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-subtle);
  }
</style>
