<!--
  Jobs: what has run, what is running, and what is waiting.

  The page exists to answer two questions. "Why is this slow?" is answered by
  sorting on queue wait or duration, which is why those two columns are sorted
  by the server rather than by the browser -- the interesting job is rarely on
  the page you are looking at. "Why has this not started?" is answered by the
  unmatched filter, which finds the queued jobs no enabled pool claims. A job
  that already ran is never counted there: its labels may name a hosted or vendor
  runner Zoomies does not own, and something ran it. Neither is a job
  whose labels all name such a runner, however long it queues -- a vendor is
  about to start it, and this fleet was never in the running.

  This is the Workflows page one step down: every job on a row of its own,
  where Workflows has the run each belongs to. The two share their filters and
  their views, so a link into either means the same on the other; what differs
  is the question. A run is what an operator arrives with from a pull request,
  and a job is what a pool claimed, a runner ran and a queue wait belongs to --
  which is why every link that names a job still lands here.

  GitHub reports every job in an installed repository, most of which this fleet
  never touches. The page therefore shows Zoomies' own work by default -- jobs a
  pool claims, jobs its runners ran, and queued jobs nothing here claims that
  something here could have -- and the "Include other runners" switch widens it
  to everything GitHub has reported.

  Status is the filter this page is opened for, so it is a row of buttons above
  the grid rather than a menu to open, and a page nobody has asked anything of
  answers "what is running?" before being asked. That default holds for a bare
  visit only: a link that already carries a filter -- the problems panel's, the
  Overview's, a colleague's -- said what it wanted, and narrowing it to running
  would answer a different question.

  The note explaining unmatched jobs belongs to the filtered view, not to the
  default one. An organisation that also rents runners elsewhere keeps queueing
  jobs this fleet has no pool for, and the ones whose labels leave any doubt are
  counted here because nothing here ran them -- so a note above the default grid
  is a standing red warning about somebody else's work, which is the one thing
  this page must not do. Discovery is the problems panel's job: `jobs.unmatched`
  waits out a grace period first, and its link opens this page with the filter
  already on.
-->
<script lang="ts">
  import { readableJobText } from '$lib/jobs/name';
  import JobsInsights from '$lib/insights/JobsInsights.svelte';
  import FleetHistory from '$lib/insights/FleetHistory.svelte';
  import { getJobFacets, listJobs } from '$lib/api/client';
  import type { Job } from '$lib/api/types';
  import { events } from '$lib/api/sse';
  import { faultLabel, fleetFailed } from '$lib/faults';
  import { formatDuration } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import {
    HOSTED,
    jobStatus,
    queueStatus,
    RUNNER_LOST,
    stuckUnmatched,
    UNMATCHED,
  } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import DataGrid from '$lib/components/DataGrid.svelte';
  import type { GridColumn, GridPage, GridQuery } from '$lib/components/DataGrid.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import StateCell from '$lib/components/StateCell.svelte';
  import { endOfMoment, startOfMoment } from '$lib/jobs/DateRange.svelte';
  import GitHubLink from '$lib/jobs/GitHubLink.svelte';
  import JobDrawer from '$lib/jobs/JobDrawer.svelte';
  import JobFilters from '$lib/jobs/JobFilters.svelte';
  import JobLabels from '$lib/jobs/JobLabels.svelte';
  import JobViewFilter from '$lib/jobs/JobViewFilter.svelte';
  import SizeAdvice from '$lib/jobs/SizeAdvice.svelte';
  import { classCell } from '$lib/jobs/size';
  import UnmatchedNote from '$lib/jobs/UnmatchedNote.svelte';
  import { jobFilterState } from '$lib/jobs/filter-state.svelte';
  import LevelSwitch from '$lib/workflows/LevelSwitch.svelte';

  /* -- filter state, held in the URL ---------------------------------------
   * Shared with the Workflows page, which reads the same keys at the run's
   * level, so a link into either means the same on the other. What each key
   * means, and why the default is what it is, is with the code in
   * lib/jobs/filter-state.svelte.ts.
   * --------------------------------------------------------------------- */

  const filterState = jobFilterState();
  const filters = $derived(filterState.filters);
  const view = $derived(filterState.view);
  const inHand = $derived(filterState.inHand);
  const narrowed = $derived(filterState.narrowed);
  const { patch, clearFilters, setView } = filterState;

  /* -- facets and live rows -------------------------------------------------- */

  let facets = $state<{ repos?: string[]; workflows?: string[]; conclusions?: string[] }>({});
  let pageRows = $state<Job[]>([]);
  let liveKey = $state(0);

  async function loadFacets(signal: AbortSignal): Promise<void> {
    try {
      facets = await getJobFacets(signal);
    } catch {
      // The menus fall back to what is on the page. A failed facet request is
      // not worth a toast: the grid itself will have reported the outage.
    }
  }

  $effect(() => {
    const controller = new AbortController();
    void loadFacets(controller.signal);
    return () => controller.abort();
  });

  // A job changing state anywhere refetches the current page, debounced by the
  // grid, so in the ordinary case there is never anything to press.
  // The job open in the drawer is replaced outright: the frame is the job's
  // GET shape, so the drawer moves from "running" to "failed at step 3" the
  // moment GitHub says so, without the operator closing and reopening it.
  $effect(() =>
    events.subscribe('job.updated', (job) => {
      liveKey += 1;
      if (selected && job.id === selected.id) selected = job;
    }),
  );

  /**
   * Refreshing fetches both halves: the grid follows `liveKey`, and the facet
   * menus are re-read here so a repository that has just run its first job
   * appears in the filter rather than only in the rows.
   */
  async function refreshPage(): Promise<void> {
    liveKey += 1;
    adviceKey += 1;
    await loadFacets(new AbortController().signal);
  }

  /** Bumped by the refresh button so the size-label report reads again too. */
  let adviceKey = $state(0);

  /** Labels worth offering in the filter: what the pools answer to, plus what this page asked for. */
  const labelOptions = $derived.by(() => {
    const seen: Record<string, true> = {};
    for (const pool of fleet.pools) for (const label of pool.labels ?? []) seen[label] = true;
    for (const job of pageRows) for (const label of job.labels ?? []) seen[label] = true;
    return Object.keys(seen).sort((a, b) => a.localeCompare(b));
  });

  const unmatchedOnPage = $derived(pageRows.filter(stuckUnmatched).length);

  /* -- the empty state ------------------------------------------------------
   * An empty grid means something different in each of the three views, and
   * saying "no jobs recorded yet" to somebody whose jobs all ran on hosted
   * runners would be a lie the page can easily avoid telling.
   * ---------------------------------------------------------------------- */

  const emptyTitle = $derived(
    narrowed
      ? 'No jobs match these filters'
      : filters.unmatched
        ? 'No unmatched jobs'
        : filters.faulted
          ? 'This fleet has broken nothing'
          : filters.failed
            ? 'No failed jobs'
            : view === 'running'
              ? 'Nothing is running right now'
              : view === 'queued'
                ? 'Nothing is queued'
                : view === 'finished'
                  ? 'Nothing has finished yet'
                  : filters.all
                    ? 'No jobs recorded yet'
                    : 'No jobs have run on this fleet',
  );

  const emptyDescription = $derived(
    narrowed
      ? 'Nothing fits the search and filters above. Clear filters returns to what is running now, and All shows every status.'
      : filters.unmatched
        ? 'Nothing is queued with labels no pool claims, which is how it should be. Jobs that already ran are not counted here however their labels read.'
        : filters.faulted
          ? "No runner here stopped under a job or failed to start one within these filters. Any failures in this period are the workflows' own. Widen the dates to look further back."
          : filters.failed
            ? 'Nothing GitHub reported as failed or timed out, and no runner here has stopped under a job. Widen the dates to look further back.'
            : view === 'running'
              ? 'No runner here is working on a job at this moment, which on a quiet fleet is the ordinary state. Queued shows what is waiting for one, and All shows everything this fleet has been asked to do.'
              : view === 'queued'
                ? 'Nothing is waiting for a runner, so the fleet is keeping up with what GitHub is asking of it. Running shows what is being worked on now, and the Queue holds anything removed from the queue rather than run.'
                : view === 'finished'
                  ? 'Nothing has ended within these filters. Running and Queued show the work still in hand, and All shows every status at once.'
                  : filters.all
                    ? 'Zoomies records a job the first time GitHub tells it about one, over a webhook delivery. If workflows are running and nothing appears here, the delivery is not arriving.'
                    : 'This view shows jobs a pool claims or a runner here ran. Include other runners to see everything GitHub has reported, hosted runners included.',
  );

  /* -- the grid ---------------------------------------------------------------- */

  async function fetchJobs(query: GridQuery, signal: AbortSignal): Promise<GridPage<Job>> {
    const page = await listJobs(
      {
        q: filters.q || undefined,
        repo: filters.repo,
        workflow: filters.workflow,
        pool_id: filters.pool_id,
        host_id: filters.host_id,
        label: filters.label,
        conclusion: filters.conclusion,
        state: filters.state,
        provisioning: filters.provisioning,
        cancelling: inHand ? false : undefined,
        since: startOfMoment(filters.since),
        until: endOfMoment(filters.until),
        unmatched: filters.unmatched ? true : undefined,
        hosted: filters.hosted === 'false' ? false : undefined,
        failed: filters.failed ? true : undefined,
        faulted: filters.faulted ? true : undefined,
        managed: filters.all ? undefined : true,
        limit: query.limit,
        offset: query.offset,
        sort: query.sort,
        order: query.order,
      },
      signal,
    );
    return { items: page.items ?? [], total: page.total };
  }

  let selected = $state<Job | null>(null);
  let drawerOpen = $state(false);

  function open(job: Job): void {
    selected = job;
    drawerOpen = true;
  }

  // A named function, not an inline arrow: the grid's fetch effect reads its
  // props, and a fresh function identity on every render would make it refetch
  // in response to its own results.
  function takePage(rows: Job[]): void {
    pageRows = rows;
  }

  function rowId(job: Job): string {
    return job.id ?? '';
  }

  /**
   * The one phrase a row has room for on a job that went wrong: the fleet's own
   * category, or the step the workflow failed at.
   *
   * The category rather than "Runner lost" for every one of them, because the
   * column is read down rather than across: nine rows saying the same two words
   * say only that the fleet is unwell, and six saying "Out of memory" say what
   * to do about it.
   */
  function failedAt(job: Job): string {
    if (fleetFailed(job)) return faultLabel(job.fault_kind) || 'Runner lost';
    if (job.failed_step) return job.failed_step.name ?? `step ${job.failed_step.number ?? '?'}`;
    return '';
  }

  // The sortable ids are the column names the store understands: queued_at,
  // started_at, completed_at, repo, workflow, state, duration and queue_wait.
  const columns = $derived<GridColumn<Job>[]>([
    {
      id: 'state',
      header: 'State',
      sortable: true,
      width: '9.5rem',
      hideable: false,
      value: (job) => {
        const queue = queueStatus(job);
        const state = jobStatus(job.state, job.conclusion).label;
        return queue ? `${state} (${queue.label.toLowerCase()})` : state;
      },
      cell: stateCell,
    },
    { id: 'failed_at', header: 'Failed at', priority: 'wide', value: failedAt, cell: failedAtCell },
    { id: 'repo', header: 'Repository', sortable: true, value: (job) => job.repo ?? '' },
    { id: 'workflow', header: 'Workflow', sortable: true, value: (job) => job.workflow ?? '' },
    { id: 'job_name', header: 'Job', value: (job) => readableJobText(job.job_name) },
    {
      id: 'labels',
      header: 'Labels',
      priority: 'wide',
      value: (job) => (job.labels ?? []).join(' '),
      cell: labelsCell,
    },
    {
      id: 'pool',
      header: 'Pool',
      priority: 'wide',
      value: (job) => job.pool_name ?? '',
      cell: poolCell,
    },
    {
      // Which class of host the job was put in, and where it ran when that
      // was another. Off by default: until size routing is on there is nothing
      // in it, and a column of dashes is not a reason to move the ones people
      // read.
      id: 'class',
      header: 'Size class',
      priority: 'wide',
      hiddenByDefault: true,
      value: (job) => classCell(job) || '--',
    },
    {
      id: 'runner',
      header: 'Runner',
      priority: 'wide',
      value: (job) => job.runner_name ?? '',
      cell: runnerCell,
    },
    {
      id: 'controller_version',
      header: 'Controller version',
      priority: 'wide',
      hiddenByDefault: true,
      value: (job) => job.controller_version ?? '--',
    },
    {
      id: 'queue_wait',
      header: 'Queue wait',
      sortable: true,
      align: 'end',
      width: '8rem',
      value: (job) => formatDuration(job.queue_wait_ms),
      cell: queueWaitCell,
    },
    {
      id: 'duration',
      header: 'Duration',
      sortable: true,
      align: 'end',
      width: '8rem',
      value: (job) => formatDuration(job.duration_ms),
      cell: durationCell,
    },
    {
      id: 'queued_at',
      header: 'Queued',
      sortable: true,
      width: '9rem',
      value: (job) => job.queued_at ?? '',
      cell: queuedCell,
    },
    { id: 'link', header: 'Run', priority: 'wide', width: '5.5rem', align: 'end', cell: linkCell },
  ]);
</script>

{#snippet stateCell(job: Job)}
  <span class="state">
    <StateCell status={jobStatus(job.state, job.conclusion)} />
    <!-- GitHub still calls a stood-down job queued, because Zoomies cannot
         unqueue one. Without this the operator's own decision was invisible
         here, and a job they had removed from the queue sat in the list
         looking like work the fleet was about to pick up. -->
    {#if queueStatus(job)}
      {@const queue = queueStatus(job)}
      <Badge status={queue!} size="sm" title={queue!.hint} />
    {/if}
    {#if fleetFailed(job)}
      <Badge status={RUNNER_LOST} size="sm" title={job.fault_fix || RUNNER_LOST.hint} />
    {/if}
    {#if stuckUnmatched(job)}
      <Badge status={UNMATCHED} size="sm" title={UNMATCHED.hint} />
    {:else if job.hosted && job.matched === false}
      <Badge status={HOSTED} size="sm" title={HOSTED.hint} />
    {/if}
  </span>
{/snippet}

{#snippet failedAtCell(job: Job)}
  {#if fleetFailed(job)}
    <span class="failed-at danger" title={job.runner_fault || job.fault_fix}>{failedAt(job)}</span>
  {:else if job.failed_step}
    <span class="failed-at" title="Step {job.failed_step.number}: {job.failed_step.name}">
      {job.failed_step.name}
    </span>
  {:else}
    <span class="none">--</span>
  {/if}
{/snippet}

{#snippet labelsCell(job: Job)}
  <JobLabels labels={job.labels} />
{/snippet}

{#snippet poolCell(job: Job)}
  {#if job.pool_id}
    <a href="/pools/{job.pool_id}" onclick={(event) => event.stopPropagation()}>
      {job.pool_name || job.pool_id}
    </a>
  {:else}
    <span class="none">Unclaimed</span>
  {/if}
{/snippet}

{#snippet runnerCell(job: Job)}
  {#if job.runner_id}
    <a href="/runners/{job.runner_id}" onclick={(event) => event.stopPropagation()}>
      {job.runner_name || job.runner_id}
    </a>
  {:else}
    <span class="none">--</span>
  {/if}
{/snippet}

{#snippet queueWaitCell(job: Job)}
  {#if job.state === 'queued' && !job.started_at}
    <span class="waiting"><Duration from={job.queued_at} live /> so far</span>
  {:else}
    {formatDuration(job.queue_wait_ms)}
  {/if}
{/snippet}

{#snippet durationCell(job: Job)}
  {#if job.state === 'in_progress' && job.started_at}
    <span class="waiting"><Duration from={job.started_at} live /> so far</span>
  {:else if job.duration_ms}
    {formatDuration(job.duration_ms)}
  {:else}
    <span class="none">--</span>
  {/if}
{/snippet}

{#snippet queuedCell(job: Job)}
  <RelativeTime value={job.queued_at} />
{/snippet}

{#snippet linkCell(job: Job)}
  <GitHubLink
    href={job.html_url}
    runNumber={job.run_number}
    label="Open {readableJobText(job.job_name) || 'this job'} on GitHub, in a new tab"
    onclick={(event) => event.stopPropagation()}
  />
{/snippet}

<PageHeader
  title="Jobs"
  subtitle={filters.all
    ? 'Every workflow job GitHub has told Zoomies about, whatever ran it.'
    : 'The workflow jobs this fleet claims, runs, or is waiting to run.'}
  onrefresh={refreshPage}
/>

<JobsInsights others={filters.all} />
<details class="activity-history">
  <summary>Explore fleet activity over the last 24 hours</summary><FleetHistory
    others={filters.all}
  />
</details>
<SizeAdvice refresh={adviceKey} />
<div class="content">
  <div class="toolbar">
    <JobViewFilter value={view} onchange={setView} />
    <LevelSwitch level="jobs" />
  </div>

  <JobFilters
    statusChips={view === ''}
    value={filters}
    {facets}
    pools={fleet.pools}
    hosts={fleet.hosts}
    {labelOptions}
    onchange={patch}
    onclear={clearFilters}
  />

  {#if filters.unmatched && unmatchedOnPage > 0}
    <UnmatchedNote count={unmatchedOnPage} />
  {/if}

  <DataGrid
    gridId="jobs"
    label="Jobs"
    {columns}
    fetcher={fetchJobs}
    {rowId}
    {filters}
    defaultSort="queued_at"
    defaultOrder="desc"
    noun="jobs"
    {liveKey}
    onopen={open}
    onrows={takePage}
    {emptyTitle}
    {emptyDescription}
  >
    {#snippet emptyAction()}
      {#if narrowed}
        <Button variant="secondary" onclick={clearFilters}>Clear filters</Button>
      {:else if view !== '' && view !== 'all'}
        <!--
          A narrowed status view that came back empty is the one case where the
          way out is not another filter but the same page without this one, so
          it is offered as a button rather than left to be found in the row.
        -->
        <Button variant="secondary" onclick={() => setView('all')}>Show every status</Button>
      {:else if !filters.unmatched}
        {#if filters.all}
          <Button variant="secondary" href="/installations">Check webhook delivery</Button>
        {:else}
          <Button variant="secondary" onclick={() => patch({ all: true })}>
            Include other runners
          </Button>
        {/if}
      {/if}
    {/snippet}
  </DataGrid>

  <p class="footnote">
    Sort by queue wait to find work that waited, or by duration to find work that took its time.
    Both are sorted by the server, so the slowest job in the fleet is one click away rather than one
    page away.
  </p>
</div>

<JobDrawer bind:open={drawerOpen} job={selected} onclose={() => (selected = null)} />

<style>
  .activity-history {
    margin: var(--z-space-4) 0 var(--z-space-5);
  }
  .activity-history summary {
    padding: var(--z-space-3);
    color: var(--z-accent);
    font-size: var(--z-text-sm);
    cursor: pointer;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  .activity-history[open] summary {
    margin-bottom: var(--z-space-3);
  }
  .content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
  }
  .state {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    white-space: nowrap;
  }
  a {
    color: var(--z-accent);
  }
  .none {
    color: var(--z-text-subtle);
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
  .footnote {
    margin: 0;
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
</style>
