<!--
  The jobs inside one workflow run, drawn under its row on the Workflows page.

  Fetched when the row is opened rather than carried on the run: a page of
  fifty runs is one query, and the jobs of the one an operator opens are one
  more. Kept live by the frames the page already receives -- a job.updated
  frame for a job of this run replaces it in place, so a step finishing moves
  the state here without the row being closed and opened again.

  Every job of the run is listed, earlier attempts included: the run's row
  sums up the latest attempt of each job, which is what GitHub shows, but the
  attempt that failed and was re-run is still the reason somebody is looking.
-->
<script lang="ts">
  import { readableJobText } from '$lib/jobs/name';
  import { CircleX, RotateCcw } from '@lucide/svelte';
  import {
    cancelJobWorkflow,
    controlProvisioning,
    listJobs,
    rerunJobWorkflow,
  } from '$lib/api/client';
  import type { Job, WorkflowRun } from '$lib/api/types';
  import { events } from '$lib/api/sse';
  import { faultLabel, fleetFailed } from '$lib/faults';
  import { formatDuration } from '$lib/format';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import {
    HOSTED,
    jobFailed,
    jobStatus,
    queueStatus,
    RUNNER_LOST,
    stuckUnmatched,
    UNMATCHED,
  } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import RowActions from '$lib/components/RowActions.svelte';
  import type { RowAction } from '$lib/components/RowActions.svelte';
  import StateCell from '$lib/components/StateCell.svelte';
  import GitHubLink from '$lib/jobs/GitHubLink.svelte';
  import JobLabels from '$lib/jobs/JobLabels.svelte';
  import {
    jobProvisioningActions,
    provisioningActionLabel,
    provisioningDescription,
    RUN_PROVISIONING_ACTIONS,
  } from '$lib/jobs/provisioning';
  import type { ProvisioningAction } from '$lib/jobs/provisioning';

  interface Props {
    run: WorkflowRun;
    /** Opens a job in the drawer. */
    onopen: (job: Job) => void;
  }

  let { run, onopen }: Props = $props();

  const canOperate = $derived(session.can('operator'));
  const cancellationEnabled = $derived(session.meta?.workflow_cancellation_enabled === true);

  // The run's identity as two values rather than the row, so a live refresh
  // that hands the page a fresh copy of the same run does not refetch its jobs.
  const repo = $derived(run.repo ?? '');
  const runId = $derived(run.github_run_id ?? 0);
  const reran = $derived((run.run_attempt ?? 1) > 1);

  let jobs = $state<Job[]>([]);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let attempt = $state(0);

  /** The most jobs one request may return: the API's own ceiling. */
  const PAGE = 500;

  /**
   * Every job of the run, however many pages that takes. A run with a wide
   * matrix and a few re-runs can carry more rows than one page holds, and a
   * panel that showed the first page as though it were the run would count
   * jobs its rows did not list.
   */
  async function fetchAll(signal: AbortSignal): Promise<Job[]> {
    const out: Job[] = [];
    for (let offset = 0; ; offset += PAGE) {
      const page = await listJobs(
        { repo: [repo], run_id: [runId], limit: PAGE, offset, sort: 'queued_at', order: 'asc' },
        signal,
      );
      const items = page.items ?? [];
      out.push(...items);
      if (items.length === 0 || out.length >= (page.total ?? 0)) return out;
    }
  }

  $effect(() => {
    void attempt;
    const controller = new AbortController();
    loading = true;
    error = null;
    fetchAll(controller.signal)
      .then((all) => {
        jobs = all;
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) error = err;
      })
      .finally(() => {
        if (!controller.signal.aborted) loading = false;
      });
    return () => controller.abort();
  });

  $effect(() =>
    events.subscribe('job.updated', (job) => {
      if (job.repo !== repo || job.github_run_id !== runId) return;
      const index = jobs.findIndex((known) => known.id === job.id);
      jobs = index >= 0 ? jobs.map((known, i) => (i === index ? job : known)) : [...jobs, job];
    }),
  );

  /** The one phrase there is room for on a job that went wrong: see the Jobs page. */
  function failedAt(job: Job): string {
    if (fleetFailed(job)) return faultLabel(job.fault_kind) || 'Runner lost';
    if (job.failed_step) return job.failed_step.name ?? `step ${job.failed_step.number ?? '?'}`;
    return '';
  }

  /* -- shaping one job's demand from its own row ----------------------------- */

  /** The job a provisioning action is waiting to be confirmed on, and which. */
  let provisioning = $state<{ action: ProvisioningAction; job: Job } | null>(null);
  let provisioningOpen = $state(false);

  function askProvisioning(action: ProvisioningAction, job: Job): void {
    provisioning = { action, job };
    provisioningOpen = true;
  }

  /**
   * One job through the same endpoint the Queue page's rows use, so what
   * "Pause" does here is exactly what it does there. The row itself is
   * repainted by the job.updated frame the controller publishes, which the
   * subscription above drops into place.
   */
  async function confirmProvisioning(): Promise<boolean> {
    const ask = provisioning;
    if (!ask?.job.id) return false;
    const label = provisioningActionLabel(ask.action);
    try {
      const result = await controlProvisioning({ ids: [ask.job.id], action: ask.action });
      const refused = result.results.find((r) => !r.ok);
      if (refused) {
        toasts.error(
          `Could not ${label.toLowerCase()} ${readableJobText(ask.job.job_name) || 'this job'}`,
          refused.error,
        );
        return false;
      }
      toasts.success(
        `${label}: ${readableJobText(ask.job.job_name) || 'job'}`,
        `${provisioningDescription(ask.action)} GitHub\u2019s view of the job is unchanged.`,
      );
      return true;
    } catch (cause) {
      toasts.fromError(
        cause,
        `Could not ${label.toLowerCase()} ${readableJobText(ask.job.job_name) || 'this job'}`,
      );
      return false;
    }
  }

  /* -- cancelling or re-running a job's run from its own row --------------- */

  let cancelTarget = $state<Job | null>(null);
  let cancelOpen = $state(false);
  let forceCancel = $state(false);
  let rerunningId = $state<string | null>(null);

  function askCancel(job: Job): void {
    cancelTarget = job;
    forceCancel = false;
    cancelOpen = true;
  }

  async function confirmCancel(): Promise<boolean> {
    const job = cancelTarget;
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

  async function rerun(job: Job): Promise<void> {
    if (!job.id || rerunningId) return;
    rerunningId = job.id;
    try {
      await rerunJobWorkflow(job.id);
      toasts.success(
        'Re-run requested',
        "GitHub is running this run's failed jobs again. They arrive as a new run attempt.",
      );
    } catch (cause) {
      toasts.fromError(cause, 'GitHub did not accept the re-run');
    } finally {
      rerunningId = null;
    }
  }

  /**
   * Run now, Pause and Resume for the job alone, in the Queue page's own
   * words and through its own endpoint, and then cancel and re-run, the same
   * two the JobDrawer's footer offers, reached without opening the drawer
   * first. The first three are the job's -- demand is raised per job -- and
   * the last two are run-scoped: GitHub has no cancel or re-run that touches
   * only one job, and the button's label says whose run it takes, the same
   * way JobDrawer's does. Removal is left to the Queue page, whose Removed
   * view is where a removed job is found again.
   */
  function rowActions(job: Job): RowAction[] {
    const out: RowAction[] = canOperate
      ? jobProvisioningActions(
          job,
          (action) => askProvisioning(action, job),
          RUN_PROVISIONING_ACTIONS,
        )
      : [];
    if (cancellationEnabled && canOperate) {
      const done = job.state === 'completed';
      out.push({
        id: 'cancel',
        label: 'Cancel workflow',
        icon: CircleX,
        danger: true,
        disabled: done,
        reason: done ? 'This job has already completed.' : undefined,
        onSelect: () => askCancel(job),
      });
    }
    if (canOperate) {
      const finished = job.state === 'completed';
      const failed = finished && jobFailed(job);
      out.push({
        id: 'rerun',
        label: "Re-run workflow's failed jobs",
        icon: RotateCcw,
        disabled: !failed,
        reason: !finished
          ? 'This job has not finished.'
          : !failed
            ? 'This job did not fail.'
            : undefined,
        onSelect: () => void rerun(job),
      });
    }
    return out;
  }
</script>

<div class="run-jobs">
  {#if error}
    <ErrorState {error} onretry={() => (attempt += 1)} />
  {:else if loading && jobs.length === 0}
    <p class="quiet">Loading the jobs of this run…</p>
  {:else if jobs.length === 0}
    <p class="quiet">GitHub has reported no jobs for this run yet.</p>
  {:else}
    <table class="jobs" aria-label="Jobs in run #{run.run_number || run.github_run_id}">
      <thead>
        <tr>
          <th scope="col">State</th>
          <th scope="col">Job</th>
          <th scope="col">Labels</th>
          <th scope="col">Pool</th>
          <th scope="col">Runner</th>
          <th scope="col">Failed at</th>
          <th scope="col" class="end">Queue wait</th>
          <th scope="col" class="end">Duration</th>
          <th scope="col" class="end"><span class="sr-only">On GitHub</span></th>
          {#if canOperate}
            <th scope="col" class="end"><span class="sr-only">Actions</span></th>
          {/if}
        </tr>
      </thead>
      <tbody>
        {#each jobs as job (job.id)}
          {@const queue = queueStatus(job)}
          <tr>
            <td data-label="State">
              <span class="state">
                <StateCell status={jobStatus(job.state, job.conclusion)} />
                {#if queue}<Badge status={queue} size="sm" title={queue.hint} />{/if}
                {#if fleetFailed(job)}
                  <Badge status={RUNNER_LOST} size="sm" title={job.fault_fix || RUNNER_LOST.hint} />
                {/if}
                {#if stuckUnmatched(job)}
                  <Badge status={UNMATCHED} size="sm" title={UNMATCHED.hint} />
                {:else if job.hosted && job.matched === false}
                  <Badge status={HOSTED} size="sm" title={HOSTED.hint} />
                {/if}
              </span>
            </td>
            <td data-label="Job">
              <button type="button" class="job-name" onclick={() => onopen(job)}>
                {readableJobText(job.job_name) || 'Unnamed job'}
              </button>
              {#if reran && job.run_attempt}
                <span class="attempt">attempt {job.run_attempt}</span>
              {/if}
            </td>
            <td data-label="Labels"><JobLabels labels={job.labels} /></td>
            <td data-label="Pool">
              {#if job.pool_id}
                <a href="/pools/{job.pool_id}">{job.pool_name || job.pool_id}</a>
              {:else}
                <span class="quiet">Unclaimed</span>
              {/if}
            </td>
            <td data-label="Runner">
              {#if job.runner_id}
                <a href="/runners/{job.runner_id}">{job.runner_name || job.runner_id}</a>
              {:else}
                <span class="quiet">--</span>
              {/if}
            </td>
            <td data-label="Failed at" class:empty={!fleetFailed(job) && !job.failed_step}>
              {#if fleetFailed(job)}
                <span class="failed-at danger" title={job.runner_fault || job.fault_fix}>
                  {failedAt(job)}
                </span>
              {:else if job.failed_step}
                <span
                  class="failed-at"
                  title="Step {job.failed_step.number}: {job.failed_step.name}"
                >
                  {job.failed_step.name}
                </span>
              {:else}
                <span class="quiet">--</span>
              {/if}
            </td>
            <td data-label="Queue wait" class="end">
              {#if job.state === 'queued' && !job.started_at}
                <span class="waiting"><Duration from={job.queued_at} live /> so far</span>
              {:else}
                {formatDuration(job.queue_wait_ms)}
              {/if}
            </td>
            <td data-label="Duration" class="end">
              {#if job.state === 'in_progress' && job.started_at}
                <span class="waiting"><Duration from={job.started_at} live /> so far</span>
              {:else if job.duration_ms}
                {formatDuration(job.duration_ms)}
              {:else}
                <span class="quiet">--</span>
              {/if}
            </td>
            <td data-label="On GitHub" class="end">
              <GitHubLink
                href={job.html_url}
                label="Open {readableJobText(job.job_name) || 'this job'} on GitHub, in a new tab"
              />
            </td>
            {#if canOperate}
              <td data-label="Actions" class="end">
                <RowActions
                  actions={rowActions(job)}
                  subject="{readableJobText(job.job_name) || 'this job'} in {job.repo ??
                    'this run'}"
                />
              </td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<ConfirmDialog
  bind:open={provisioningOpen}
  title={provisioningActionLabel(provisioning?.action)}
  name={readableJobText(provisioning?.job.job_name) || 'this job'}
  description={`${provisioning?.job.repo ?? ''} / ${readableJobText(provisioning?.job.job_name) || 'this job'}. ${provisioningDescription(provisioning?.action)}`}
  consequences={[
    'Only this job\u2019s provisioning demand changes. Its siblings in the run, existing runners and GitHub\u2019s view of the job are unaffected.',
    'If the job starts or finishes before this reaches the server, nothing changes and the refusal is reported.',
  ]}
  confirmLabel={provisioningActionLabel(provisioning?.action)}
  onconfirm={confirmProvisioning}
  oncancel={() => (provisioning = null)}
/>

<ConfirmDialog
  bind:open={cancelOpen}
  title="Cancel workflow run"
  name={cancelTarget?.workflow || readableJobText(cancelTarget?.job_name) || 'workflow run'}
  description="GitHub can only cancel the whole workflow run. Every queued or running job in this run will be stopped, not only the job shown here."
  consequences={[
    `Run ${cancelTarget?.run_number ? '#' + cancelTarget.run_number : (cancelTarget?.github_run_id ?? '')} in ${cancelTarget?.repo ?? 'GitHub'} will be cancelled.`,
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
  .run-jobs {
    color: var(--z-text);
    font-size: var(--z-text-sm);
  }
  .quiet {
    margin: 0;
    color: var(--z-text-subtle);
  }
  .jobs {
    width: 100%;
    border-collapse: collapse;
  }
  th {
    padding: 0 var(--z-space-3) var(--z-space-2);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    white-space: nowrap;
  }
  td {
    padding: var(--z-space-2) var(--z-space-3);
    border-top: var(--z-border-width) solid var(--z-border);
    vertical-align: middle;
  }
  .end {
    text-align: right;
  }
  .state {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    white-space: nowrap;
  }
  .job-name {
    border: 0;
    padding: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    font-weight: var(--z-weight-medium);
    text-align: left;
    cursor: pointer;
  }
  .job-name:hover {
    text-decoration: underline;
  }
  .job-name:focus-visible {
    outline: var(--z-focus-width) solid var(--z-focus-colour);
    outline-offset: var(--z-focus-offset);
    border-radius: var(--z-radius-sm);
  }
  .attempt {
    margin-left: var(--z-space-2);
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
  a {
    color: var(--z-accent);
  }
  .waiting {
    color: var(--z-pending);
    white-space: nowrap;
  }
  .failed-at {
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .failed-at.danger {
    color: var(--z-danger);
    font-weight: var(--z-weight-medium);
  }

  /*
    Read by a finger, and by pairs: the heading row goes, each job is a card
    of its own, and two short fields share a line instead of one apiece --
    state with the job's name, pool with runner, queue wait with duration --
    so the nine rows a desktop spreads across a wide table become four or
    five here rather than nine, without leaving any of them out. Labels and a
    failure can run long, so those keep a line to themselves; a job that has
    not failed drops that line rather than spending it on a dash.
  */
  @media (max-width: 768px) {
    thead {
      display: none;
    }
    tbody {
      display: block;
    }
    tr {
      display: grid;
      grid-template-columns: 1fr 1fr;
      column-gap: var(--z-space-4);
      padding: var(--z-space-2) 0;
      border-top: var(--z-border-width) solid var(--z-border);
    }
    td {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--z-space-3);
      padding: var(--z-space-1) 0;
      border: 0;
      text-align: right;
    }
    td[data-label='Labels'],
    td[data-label='Failed at'],
    td[data-label='On GitHub'] {
      grid-column: 1 / -1;
    }
    td.empty {
      display: none;
    }
    td::before {
      content: attr(data-label);
      flex: none;
      color: var(--z-text-muted);
      font-size: var(--z-text-2xs);
      font-weight: var(--z-weight-medium);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
      text-align: left;
    }
    /*
      The value beside the label is a flex item too, and a flex item refuses
      to shrink below the width its content wants unless told it may -- a
      runner's name is one long hyphenated word otherwise, pushing the card
      wider than the phone showing it rather than wrapping.
    */
    td > :global(a),
    td > .job-name,
    td > .quiet {
      min-width: 0;
      overflow-wrap: anywhere;
    }
  }
</style>
