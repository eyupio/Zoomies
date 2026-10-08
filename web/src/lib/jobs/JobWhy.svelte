<!--
  Why a job is where it is: failed, stalled or waiting, in one section.

  It leads with the verdict, because that is the sentence somebody opened the
  drawer for. The verdict comes from GET /jobs/{id}/explanation, which is
  computed on the controller from the last scheduler plan and the fleet around
  the job, and is the same answer `zoomies why` prints and an assistant reads
  over MCP; the drawer renders it and reasons about nothing, so the three
  surfaces cannot disagree. Under the verdict: the class and how sure the
  fleet is, the evidence as facts a person can check, the next steps as
  buttons where an action exists, the runner's last lines up to the one that
  decided it, and the catalog code to read on.

  A failed job keeps the heading the outcome panel used to give it, naming
  the right author first: the fleet's own fault category when its runner
  stopped under the job, the failed step when the workflow did it. A waiting
  job keeps the lead word and the pool's live counts, which are facts about
  the pool and belong beside the wait.
-->
<script lang="ts">
  import { ExternalLink, RotateCcw, TriangleAlert } from '@lucide/svelte';
  import type { Job, JobExplanation } from '$lib/api/types';
  import { getJobExplanation } from '$lib/api/client';
  import { faultDetail, faultLabel, fleetFailed } from '$lib/faults';
  import { formatDuration, toMillis } from '$lib/format';
  import { jobStatus } from '$lib/status';
  import { fleet } from '$lib/state/fleet.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import { catalogLink, evidenceRows, stepAction } from './why';

  interface Props {
    job: Job;
    /** failed: the job is over and went wrong; waiting: it has not started. */
    mode: 'failed' | 'waiting';
    /** Set when the viewer may ask GitHub to run the run's failed jobs again. */
    onRerun?: (() => void) | null;
    rerunning?: boolean;
    class?: string;
  }

  let { job, mode, onRerun = null, rerunning = false, class: className = '' }: Props = $props();

  let why = $state<JobExplanation | null>(null);

  // Refetched whenever the drawer is given a different job and whenever the
  // event stream replaces this one: the answer is about the fleet around the
  // job, so it goes stale for reasons the job row does not show.
  $effect(() => {
    const id = job.id;
    void job.state;
    if (!id) return;
    const controller = new AbortController();
    void (async () => {
      try {
        why = await getJobExplanation(id, controller.signal);
      } catch {
        // A diagnostic that is down is not the drawer's news to break: the
        // facts on the job row are still true, and they are most of the answer.
        why = null;
      }
    })();
    return () => controller.abort();
  });

  const failed = $derived(mode === 'failed');
  const status = $derived(jobStatus(job.state, job.conclusion));
  const step = $derived(job.failed_step ?? null);
  const ours = $derived(fleetFailed(job));
  const kindLabel = $derived(faultLabel(job.fault_kind));
  const kindDetail = $derived(faultDetail(job.fault_kind));

  /** How long the failing step ran, when both of its stamps are known. */
  const stepTook = $derived.by(() => {
    if (!step) return null;
    const from = toMillis(step.started_at);
    const to = toMillis(step.completed_at);
    return from === null || to === null ? null : to - from;
  });

  const heading = $derived.by(() => {
    // The category leads when there is one: "Out of memory" is a heading
    // somebody can act on, and "The runner stopped under this job" is the same
    // news with the useful half removed.
    if (ours && kindLabel) return kindLabel;
    if (job.runner_fault) return 'The runner stopped under this job';
    if (step) return `${status.label} at step ${step.number ?? '?'}, ${step.name ?? 'unnamed'}`;
    if (job.state === 'completed')
      return `${status.label} after ${formatDuration(job.duration_ms)}`;
    return status.label;
  });

  const pool = $derived(fleet.pool(job.pool_id));
  const counts = $derived(pool?.counts);
  const warming = $derived((counts?.provisioning ?? 0) + (counts?.registering ?? 0));

  const rows = $derived(why ? evidenceRows(why.evidence) : []);
  const readOn = $derived(why ? catalogLink(why.next_steps) : null);
  // The catalog chip carries the "read on" link, so that step is not listed twice.
  const steps = $derived(
    why ? why.next_steps.filter((s) => !(readOn && s.link === readOn)).map(stepAction) : [],
  );
  const excerpt = $derived(why?.log_excerpt ?? null);
  const lines = $derived(excerpt?.lines ?? []);
  const firstLine = $derived(lines.length ? lines[0]?.n : null);
  const lastLine = $derived(lines.length ? lines[lines.length - 1]?.n : null);
</script>

<div
  class="why {className}"
  class:failed
  class:waiting={!failed}
  role={failed ? 'note' : 'status'}
  aria-label={failed ? 'Why this job went wrong' : 'What is happening to this job'}
>
  {#if failed}
    <TriangleAlert size={16} aria-hidden="true" class="icon" />
  {/if}
  <div class="body">
    {#if failed}
      <p class="heading">{heading}</p>
      {#if ours}
        {#if kindDetail}<p class="detail">{kindDetail}</p>{/if}
        {#if job.runner_fault}<p class="detail muted">{job.runner_fault}.</p>{/if}
        <p class="detail">
          GitHub records this as an ordinary failure; the workflow did nothing wrong.
        </p>
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
      {/if}
    {:else}
      <p class="line">
        <span class="lead" class:blocked={why?.blocked}>{why?.blocked ? 'Blocked' : 'Waiting'}</span
        >
        <span class="tabular"><Duration from={job.queued_at} live /></span>
        {#if pool}
          <span>in <a href="/pools/{pool.id}">{pool.name ?? pool.id}</a></span>
        {/if}
      </p>
    {/if}

    <p class="summary">{why?.summary ?? 'Working out what the fleet is doing about it…'}</p>
    {#if why?.detail}<p class="detail muted">{why.detail}</p>{/if}
    {#if why?.fix}
      <p class="detail fix"><span class="fix-label">Fix</span> {why.fix}</p>
    {:else if job.fault_fix}
      <p class="detail fix"><span class="fix-label">Fix</span> {job.fault_fix}</p>
    {/if}

    {#if why}
      <p class="chips">
        <span class="chip" title={why.confidence_reason || undefined}>
          <span class="chip-label">Class</span>
          {why.class} · {why.confidence}
        </span>
        {#if why.problem_code}
          {#if readOn}
            <a class="chip link" href={readOn} target="_blank" rel="noopener noreferrer">
              <span class="chip-label">Code</span>
              {why.problem_code}
              <ExternalLink size={11} aria-hidden="true" />
              <span class="sr-only">(opens in a new tab)</span>
            </a>
          {:else}
            <span class="chip"><span class="chip-label">Code</span> {why.problem_code}</span>
          {/if}
        {/if}
      </p>
      {#if why.confidence !== 'high' && why.confidence_reason}
        <p class="detail muted">{why.confidence_reason}</p>
      {/if}
    {/if}

    {#if !failed && counts}
      <dl class="counts">
        <div>
          <dt>Starting</dt>
          <dd class="tabular">{warming}</dd>
        </div>
        <div>
          <dt>Idle</dt>
          <dd class="tabular">{counts.idle ?? 0}</dd>
        </div>
        <div>
          <dt>Busy</dt>
          <dd class="tabular">{counts.busy ?? 0}</dd>
        </div>
        <div>
          <dt>Ceiling</dt>
          <dd class="tabular">{pool?.max_runners ?? '--'}</dd>
        </div>
      </dl>
    {/if}

    {#if rows.length}
      <dl class="evidence" aria-label="Evidence">
        {#each rows as row (row.label + row.value)}
          <div>
            <dt>{row.label}</dt>
            <dd class:mono={row.mono}>
              {#if row.href && /^https?:\/\//.test(row.href)}
                <a href={row.href} target="_blank" rel="noopener noreferrer">{row.value}</a>
              {:else if row.href}
                <a href={row.href}>{row.value}</a>
              {:else}
                {row.value}
              {/if}
            </dd>
          </div>
        {/each}
      </dl>
    {/if}

    {#if excerpt}
      <details class="excerpt">
        <summary>
          {#if firstLine !== null && lastLine !== null}
            Runner output, lines {firstLine} to {lastLine}
          {:else}
            Runner output
          {/if}
        </summary>
        {#if lines.length}
          <ol class="lines" aria-label="The runner's last lines">
            {#each lines as l (l.n)}
              <li class:decisive={l.decisive} value={l.n}>
                <span class="n">{l.n}</span><span class="text">{l.text}</span>
              </li>
            {/each}
          </ol>
        {/if}
        {#if excerpt.note}<p class="detail muted">{excerpt.note}</p>{/if}
      </details>
    {/if}

    <div class="actions">
      {#each steps as s, i (i)}
        {#if s.rerun && onRerun}
          <!--
            aria-disabled, not disabled: this sits inside the job drawer's
            focus trap. A natively disabled button is blurred by the browser,
            so focus would fall to <body> and the trap's next Tab would never
            intercept it. aria-disabled keeps it focused and announced, and
            the click handler refuses while busy.
          -->
          <button
            class="action rerun"
            type="button"
            aria-disabled={rerunning ? 'true' : undefined}
            aria-busy={rerunning ? 'true' : undefined}
            onclick={() => {
              if (!rerunning) onRerun?.();
            }}
          >
            <RotateCcw size={13} aria-hidden="true" />
            {rerunning ? 'Asking GitHub…' : 'Run it again'}
          </button>
        {:else if s.href && s.external}
          <a class="action" href={s.href} target="_blank" rel="noopener noreferrer">
            {s.label}
            <ExternalLink size={13} aria-hidden="true" />
            <span class="sr-only">(opens in a new tab)</span>
          </a>
        {:else if s.href}
          <a class="action" href={s.href}>{s.label}</a>
        {:else if !s.rerun}
          <span class="step">{s.label}</span>
        {/if}
      {/each}
      {#if failed && job.runner_id}
        <a class="action" href="/runners/{job.runner_id}">Open the runner</a>
      {/if}
      {#if failed && !ours && step && job.html_url}
        <a class="action" href={job.html_url} target="_blank" rel="noopener noreferrer">
          Open the failed step's log
          <ExternalLink size={13} aria-hidden="true" />
          <span class="sr-only">(opens in a new tab)</span>
        </a>
      {/if}
    </div>
    {#if failed && onRerun}
      <p class="detail muted">
        GitHub has no job-level re-run, so this runs every failed job in the run again.
      </p>
    {/if}
  </div>
</div>

<style>
  .why {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-3);
    border: var(--z-border-width) solid var(--z-pending-border);
    border-radius: var(--z-radius-md);
    background: var(--z-pending-subtle);
  }
  .why.failed {
    border-color: var(--z-danger-border);
    background: var(--z-danger-subtle);
  }
  .why :global(.icon) {
    flex: none;
    color: var(--z-danger);
  }
  .body {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-2);
    min-width: 0;
    width: 100%;
  }
  .heading {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  .line {
    display: flex;
    flex-wrap: wrap;
    align-items: baseline;
    gap: var(--z-space-2);
    margin: 0;
    font-size: var(--z-text-base);
    color: var(--z-text);
  }
  .lead {
    font-weight: var(--z-weight-semibold);
  }
  .lead.blocked {
    color: var(--z-danger);
  }
  .line a,
  .evidence a {
    color: var(--z-accent);
  }
  .summary,
  .detail {
    margin: 0;
    font-size: var(--z-text-sm);
    line-height: var(--z-leading-sm);
    color: var(--z-text);
    max-width: 70ch;
    overflow-wrap: anywhere;
  }
  .summary {
    font-weight: var(--z-weight-medium);
  }
  .muted {
    color: var(--z-text-muted);
  }
  .fix-label {
    font-weight: var(--z-weight-semibold);
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: 0;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    padding: 0 var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    background: var(--z-surface);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text);
    text-decoration: none;
  }
  .chip-label {
    font-family: inherit;
    font-size: var(--z-text-2xs);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-subtle);
  }
  .chip.link:hover {
    border-color: var(--z-accent);
  }
  .counts {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2) var(--z-space-5);
    margin: 0;
  }
  .evidence {
    display: grid;
    grid-template-columns: 1fr;
    gap: var(--z-space-1) var(--z-space-4);
    margin: 0;
    width: 100%;
  }
  .evidence > div {
    display: contents;
  }
  @media (min-width: 640px) {
    .evidence {
      grid-template-columns: max-content 1fr;
    }
  }
  dt {
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-subtle);
  }
  dd {
    margin: 0;
    font-size: var(--z-text-sm);
    color: var(--z-text);
    overflow-wrap: anywhere;
  }
  dd.mono {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
  }
  .excerpt {
    width: 100%;
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .excerpt summary {
    cursor: pointer;
    font-weight: var(--z-weight-medium);
  }
  .lines {
    margin: var(--z-space-2) 0 0;
    padding: var(--z-space-2);
    list-style: none;
    border-radius: var(--z-radius-sm);
    background: var(--z-surface-sunken);
    font-family: var(--z-font-mono);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    overflow-x: auto;
  }
  .lines li {
    display: flex;
    gap: var(--z-space-2);
    white-space: pre;
  }
  .lines .n {
    flex: none;
    min-width: 3ch;
    text-align: right;
    color: var(--z-text-subtle);
  }
  .lines .decisive {
    color: var(--z-danger);
    font-weight: var(--z-weight-semibold);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3);
  }
  .step {
    font-size: var(--z-text-sm);
    color: var(--z-text);
  }
  .rerun {
    border: var(--z-border-width) solid var(--z-danger-border);
    border-radius: var(--z-radius-sm);
    padding: var(--z-space-1) var(--z-space-2);
    background: transparent;
    cursor: pointer;
    font: inherit;
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
  }
  .rerun:hover:not([aria-disabled='true']) {
    background: var(--z-surface);
  }
  /* A button that is busy still holds focus, so it must not also be
     clickable: otherwise a second press fires the same request again. */
  .rerun[aria-disabled='true'] {
    pointer-events: none;
    cursor: progress;
    color: var(--z-text-muted);
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
