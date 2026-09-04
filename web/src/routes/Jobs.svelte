<!--
  Jobs: what has run, what is running, and what is waiting.

  The page exists to answer two questions. "Why is this slow?" is answered by
  sorting on queue wait or duration, which is why those two columns are sorted
  by the server rather than by the browser -- the interesting job is rarely on
  the page you are looking at. "Why has this not started?" is answered by the
  unmatched filter, which finds the jobs no enabled pool claims.
-->
<script lang="ts">
  import { getJobFacets, listJobs } from '$lib/api/client';
  import type { Job, JobState } from '$lib/api/types';
  import { events } from '$lib/api/sse';
  import { formatDuration } from '$lib/format';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { jobStatus, UNMATCHED } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import DataGrid from '$lib/components/DataGrid.svelte';
  import type { GridColumn, GridPage, GridQuery } from '$lib/components/DataGrid.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import { endOfDay, startOfDay } from '$lib/jobs/DateRange.svelte';
  import GitHubLink from '$lib/jobs/GitHubLink.svelte';
  import JobDrawer from '$lib/jobs/JobDrawer.svelte';
  import JobFilters from '$lib/jobs/JobFilters.svelte';
  import type { JobFilterState } from '$lib/jobs/JobFilters.svelte';
  import JobLabels from '$lib/jobs/JobLabels.svelte';
  import UnmatchedNote from '$lib/jobs/UnmatchedNote.svelte';

  /* -- filter state, held in the URL ---------------------------------------
   * The keys are the API's own, so the address bar and the request agree and a
   * pasted link reproduces exactly what the sender was looking at.
   * --------------------------------------------------------------------- */

  const filters = $derived<JobFilterState>({
    q: router.param('q'),
    repo: router.paramList('repo'),
    workflow: router.paramList('workflow'),
    pool_id: router.paramList('pool_id'),
    label: router.paramList('label'),
    conclusion: router.paramList('conclusion'),
    state: router.paramList('state') as JobState[],
    since: router.param('since'),
    until: router.param('until'),
    unmatched: router.param('unmatched') === 'true',
  });

  /**
   * Merge a filter change into the URL.
   *
   * Only the keys the caller actually passed are written: `setQuery` removes a
   * key whose value is undefined, so spreading a partial object would quietly
   * clear every filter it did not mention.
   */
  function patch(next: Partial<JobFilterState>): void {
    const out: Record<string, string | readonly string[] | null> = { offset: null };
    for (const [key, value] of Object.entries(next)) {
      if (typeof value === 'boolean') out[key] = value ? 'true' : null;
      else if (Array.isArray(value)) out[key] = value;
      else out[key] = (value as string) || null;
    }
    router.setQuery(out);
  }

  function clearFilters(): void {
    router.setQuery({
      q: null,
      repo: null,
      workflow: null,
      pool_id: null,
      label: null,
      conclusion: null,
      state: null,
      since: null,
      until: null,
      unmatched: null,
      offset: null,
    });
  }

  /* -- facets and live rows -------------------------------------------------- */

  let facets = $state<{ repos?: string[]; workflows?: string[]; conclusions?: string[] }>({});
  let pageRows = $state<Job[]>([]);
  let liveKey = $state(0);

  $effect(() => {
    const controller = new AbortController();
    void getJobFacets(controller.signal)
      .then((result) => (facets = result))
      .catch(() => {
        // The menus fall back to what is on the page. A failed facet request is
        // not worth a toast: the grid itself will have reported the outage.
      });
    return () => controller.abort();
  });

  // A job changing state anywhere refetches the current page, debounced by the
  // grid. There is no refresh button because there is never anything to press.
  $effect(() => events.subscribe('job.updated', () => (liveKey += 1)));

  /** Labels worth offering in the filter: what the pools answer to, plus what this page asked for. */
  const labelOptions = $derived.by(() => {
    const seen: Record<string, true> = {};
    for (const pool of fleet.pools) for (const label of pool.labels ?? []) seen[label] = true;
    for (const job of pageRows) for (const label of job.labels ?? []) seen[label] = true;
    return Object.keys(seen).sort((a, b) => a.localeCompare(b));
  });

  const unmatchedOnPage = $derived(pageRows.filter((job) => job.matched === false).length);

  /* -- the grid ---------------------------------------------------------------- */

  async function fetchJobs(query: GridQuery, signal: AbortSignal): Promise<GridPage<Job>> {
    const page = await listJobs(
      {
        q: filters.q || undefined,
        repo: filters.repo,
        workflow: filters.workflow,
        pool_id: filters.pool_id,
        label: filters.label,
        conclusion: filters.conclusion,
        state: filters.state,
        since: startOfDay(filters.since),
        until: endOfDay(filters.until),
        unmatched: filters.unmatched ? true : undefined,
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

  // The sortable ids are the column names the store understands: queued_at,
  // started_at, completed_at, repo, workflow, state, duration and queue_wait.
  const columns = $derived<GridColumn<Job>[]>([
    {
      id: 'state',
      header: 'State',
      sortable: true,
      width: '9.5rem',
      hideable: false,
      value: (job) => jobStatus(job.state, job.conclusion).label,
      cell: stateCell,
    },
    { id: 'repo', header: 'Repository', sortable: true, value: (job) => job.repo ?? '' },
    { id: 'workflow', header: 'Workflow', sortable: true, value: (job) => job.workflow ?? '' },
    { id: 'job_name', header: 'Job', value: (job) => job.job_name ?? '' },
    {
      id: 'labels',
      header: 'Labels',
      value: (job) => (job.labels ?? []).join(' '),
      cell: labelsCell,
    },
    { id: 'pool', header: 'Pool', value: (job) => job.pool_name ?? '', cell: poolCell },
    { id: 'runner', header: 'Runner', value: (job) => job.runner_name ?? '', cell: runnerCell },
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
    { id: 'link', header: 'Run', width: '4rem', align: 'end', cell: linkCell },
  ]);
</script>

{#snippet stateCell(job: Job)}
  <span class="state">
    <StatusDot status={jobStatus(job.state, job.conclusion)} showLabel />
    {#if job.matched === false}
      <Badge status={UNMATCHED} size="sm" title={UNMATCHED.hint} />
    {/if}
  </span>
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
    label="Open {job.job_name || 'this job'} on GitHub, in a new tab"
    onclick={(event) => event.stopPropagation()}
  />
{/snippet}

<PageHeader title="Jobs" subtitle="Every workflow job GitHub has told this controller about." />

<div class="content">
  <JobFilters
    value={filters}
    {facets}
    pools={fleet.pools}
    {labelOptions}
    onchange={patch}
    onclear={clearFilters}
  />

  {#if unmatchedOnPage > 0}
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
    emptyTitle={filters.unmatched ? 'No unmatched jobs' : 'No jobs recorded yet'}
    emptyDescription={filters.unmatched
      ? 'Every job here has a pool that claims its labels, which is how it should be.'
      : 'Zoomies records a job the first time GitHub tells it about one, over a webhook delivery. If workflows are running and nothing appears here, the delivery is not arriving.'}
  >
    {#snippet emptyAction()}
      {#if !filters.unmatched}
        <Button variant="secondary" href="/installations">Check webhook delivery</Button>
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
  .content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
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
  .footnote {
    margin: 0;
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
</style>
