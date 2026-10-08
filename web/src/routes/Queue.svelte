<script lang="ts">
  import { readableJobText } from '$lib/jobs/name';
  import ChartPanel from '$lib/components/ChartPanel.svelte';
  import StateBreakdown from '$lib/insights/StateBreakdown.svelte';

  import { ListOrdered, Bookmark, X } from '@lucide/svelte';
  import {
    controlProvisioning,
    getJobFacets,
    listProvisioning,
    selectProvisioning,
    toQuery,
  } from '$lib/api/client';
  import type { Job, ProvisioningStatus, Query } from '$lib/api/types';
  import { events } from '$lib/api/sse';
  import { NO_VALUE } from '$lib/format';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { QUEUE_STATUS_LABELS } from '$lib/status';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import ActivityStatus from '$lib/jobs/ActivityStatus.svelte';
  import { queuedActivity } from '$lib/jobs/activity-status';
  import { prefs } from '$lib/state/prefs.svelte';
  import Button from '$lib/components/Button.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import DataGrid from '$lib/components/DataGrid.svelte';
  import type { BulkAction, GridColumn, GridQuery } from '$lib/components/DataGrid.svelte';
  import RowActions from '$lib/components/RowActions.svelte';
  import type { RowAction } from '$lib/components/RowActions.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import Dialog from '$lib/components/Dialog.svelte';
  import Field from '$lib/components/Field.svelte';
  import Input from '$lib/components/Input.svelte';
  import Select from '$lib/components/Select.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import JobFilters, { EMPTY_JOB_FILTERS } from '$lib/jobs/JobFilters.svelte';
  import type { JobFilterState } from '$lib/jobs/JobFilters.svelte';
  import FacetMenu from '$lib/jobs/FacetMenu.svelte';
  import JobDrawer from '$lib/jobs/JobDrawer.svelte';
  import JobLabels from '$lib/jobs/JobLabels.svelte';
  import GitHubLink from '$lib/jobs/GitHubLink.svelte';
  import { startOfDay, endOfDay } from '$lib/jobs/DateRange.svelte';
  import {
    jobProvisioningActions,
    PROVISIONING_ACTIONS,
    provisioningActionLabel,
    provisioningDescription,
  } from '$lib/jobs/provisioning';
  import type { ProvisioningAction as Action } from '$lib/jobs/provisioning';

  type Status = ProvisioningStatus;
  // The labels come from the shared map so the button here, the Jobs page's
  // filter chip and the badge on a job row all call the same state the same
  // thing.
  const statuses: { value: Status; label: string; hint: string }[] = [
    { value: 'ready', label: QUEUE_STATUS_LABELS.ready, hint: 'Normal provisioning demand' },
    {
      value: 'expedited',
      label: QUEUE_STATUS_LABELS.expedited,
      hint: 'Prioritised within pool priority',
    },
    { value: 'paused', label: QUEUE_STATUS_LABELS.paused, hint: 'Demand on hold' },
    {
      value: 'deleted',
      label: QUEUE_STATUS_LABELS.deleted,
      hint: 'Removed demand · can be restored',
    },
  ];
  // The verbs, icons and refusals are the ones the Workflows page uses on a
  // run and on the jobs inside it, from the module both read.
  const actions = PROVISIONING_ACTIONS;
  const canOperate = $derived(session.can('operator'));
  const filters = $derived<JobFilterState>({
    ...EMPTY_JOB_FILTERS,
    q: router.param('q'),
    repo: router.paramList('repo'),
    workflow: router.paramList('workflow'),
    pool_id: router.paramList('pool_id'),
    label: router.paramList('label'),
    since: router.param('since'),
    until: router.param('until'),
    unmatched: router.param('unmatched') === 'true',
  });
  const provisioning = $derived(
    router.paramList('provisioning').length
      ? router
          .paramList('provisioning')
          .filter((s): s is Status => statuses.some((v) => v.value === s))
      : (['ready', 'expedited', 'paused'] as Status[]),
  );
  const branch = $derived(router.param('branch'));
  const query = $derived<Query<'listProvisioning'>>({
    q: filters.q,
    repo: filters.repo,
    workflow: filters.workflow,
    pool_id: filters.pool_id,
    label: filters.label,
    branch: branch ? [branch] : [],
    provisioning,
    since: startOfDay(filters.since),
    until: endOfDay(filters.until),
    unmatched: filters.unmatched || undefined,
  });
  let liveKey = $state(0);
  let counts = $state<Record<string, number>>({});
  let total = $state(0);
  let pageRows = $state<Job[]>([]);
  let facets = $state<{ repos?: string[]; workflows?: string[] }>({});
  let snapshot = $state<string[]>([]);
  let selecting = $state(false);
  let selectionAbort: AbortController | undefined;
  let selectedJob = $state<Job | null>(null);
  let drawerOpen = $state(false);
  let pending = $state<{ action: Action; ids: string[]; name: string } | null>(null);
  let confirmOpen = $state(false);
  let settle: ((ok: boolean) => void) | undefined;
  let report = $state<{ changed: number; failed: { id: string; error?: string }[] } | null>(null);

  $effect(() => {
    const abort = new AbortController();
    getJobFacets(abort.signal)
      .then((v) => (facets = v))
      .catch(() => {});
    return () => {
      abort.abort();
      selectionAbort?.abort();
      settle?.(false);
    };
  });
  $effect(() =>
    events.subscribe('job.updated', (job) => {
      liveKey += 1;
      if (selectedJob?.id === job.id) selectedJob = job;
    }),
  );
  $effect(() => {
    toQuery(query);
    snapshot = [];
    selectionAbort?.abort();
    selecting = false;
  });
  const labelOptions = $derived(
    [
      ...new Set([
        ...fleet.pools.flatMap((p) => p.labels ?? []),
        ...pageRows.flatMap((j) => j.labels ?? []),
        ...filters.label,
      ]),
    ].sort(),
  );

  function patch(next: Partial<JobFilterState>): void {
    const out: Record<string, string | readonly string[] | null> = { offset: null };
    for (const [key, value] of Object.entries(next))
      out[key] = typeof value === 'boolean' ? (value ? 'true' : null) : value || null;
    router.setQuery(out);
  }
  function clear(): void {
    router.navigate('/queue');
  }
  function setStatus(values: string[]): void {
    router.setQuery({
      provisioning: values.length ? values : statuses.map((s) => s.value),
      offset: null,
    });
  }
  async function fetchRows(page: GridQuery, signal: AbortSignal) {
    const result = await listProvisioning({ ...query, ...page }, signal);
    if (!signal.aborted) {
      counts = result.counts ?? {};
      total = result.total ?? 0;
    }
    return { items: result.items ?? [], total: result.total ?? 0 };
  }
  function takeRows(rows: Job[]): void {
    pageRows = rows;
  }
  function rowId(job: Job): string {
    return job.id ?? '';
  }
  /** What an action acts on. Names the row's toolbar, and every button in it. */
  function subjectOf(job: Job): string {
    return `${job.repo ?? 'this repository'} / ${readableJobText(job.job_name) || 'this job'}`;
  }
  /**
   * The four actions, on the row.
   *
   * They were behind a menu, which cost a press before any of them could be
   * reached -- and pausing a repository's worth of demand is the job an
   * operator opens this page to do, one row after another. An action already
   * in force is disabled and says why: a control that refuses without a reason
   * is a control that gets reported as broken.
   */
  function rowActions(job: Job): RowAction[] {
    return jobProvisioningActions(job, (action) => {
      void ask(action, [rowId(job)]);
    });
  }
  function openJob(job: Job): void {
    selectedJob = job;
    drawerOpen = true;
  }
  async function selectAll(): Promise<void> {
    selectionAbort?.abort();
    const abort = new AbortController();
    selectionAbort = abort;
    selecting = true;
    try {
      const result = await selectProvisioning(query, abort.signal);
      if (!abort.signal.aborted) snapshot = result.ids;
    } catch (e) {
      if (!abort.signal.aborted) toasts.fromError(e, 'Could not select matching items');
    } finally {
      if (!abort.signal.aborted) selecting = false;
    }
  }
  function ask(action: Action, ids: string[]): Promise<boolean> {
    if (pending) return Promise.resolve(false);
    const job = ids.length === 1 ? pageRows.find((j) => j.id === ids[0]) : undefined;
    pending = {
      action,
      ids: [...ids],
      name: job
        ? `${job.repo} / ${readableJobText(job.job_name)}`
        : `${ids.length} provisioning items`,
    };
    confirmOpen = true;
    return new Promise((resolve) => (settle = resolve));
  }
  function cancel(): void {
    pending = null;
    settle?.(false);
    settle = undefined;
  }
  async function confirm(): Promise<boolean> {
    if (!pending) return false;
    const result = await controlProvisioning({ action: pending.action, ids: pending.ids });
    const failed = result.results.filter((r) => !r.ok);
    report = { changed: result.results.length - failed.length, failed };
    snapshot = [];
    liveKey += 1;
    pending = null;
    settle?.(true);
    settle = undefined;
    return true;
  }
  const bulkActions: BulkAction[] = actions.map((a) => ({ ...a, run: (ids) => ask(a.id, ids) }));
  const description = $derived(provisioningDescription(pending?.action));
  const columns = $derived<GridColumn<Job>[]>([
    {
      id: 'provisioning',
      header: 'Provisioning',
      width: '9rem',
      hideable: false,
      cell: statusCell,
    },
    {
      id: 'job_name',
      header: 'Job / workflow',
      hideable: false,
      cell: jobCell,
      value: (j) => readableJobText(j.job_name),
    },
    {
      id: 'repo',
      header: 'Repository',
      sortable: true,
      cell: repoCell,
      value: (j) => j.repo ?? '',
    },
    {
      id: 'run',
      header: 'Run',
      priority: 'wide',
      width: '5.5rem',
      align: 'end',
      cell: runCell,
    },
    {
      id: 'pool',
      header: 'Pool / priority',
      priority: 'wide',
      cell: poolCell,
      value: (j) => j.pool_name ?? '',
    },
    { id: 'labels', header: 'Labels', priority: 'wide', cell: labelsCell },
    { id: 'queued_at', header: 'Waiting', sortable: true, cell: waitCell, width: '7rem' },
    ...(canOperate
      ? [
          {
            id: 'actions',
            header: 'Actions',
            fixed: true,
            hideable: false,
            // Four buttons and the gaps between them, plus the cell's padding.
            width: '9rem',
            align: 'end' as const,
            cell: actionCell,
          },
        ]
      : []),
  ]);

  let saved = $state<{ name: string; url: string }[]>([]);
  let saveOpen = $state(false);
  let viewName = $state('');
  $effect(() => {
    try {
      const v: unknown = JSON.parse(localStorage.getItem('zoomies.queue.views') ?? '[]');
      if (Array.isArray(v))
        saved = v
          .filter(
            (x) =>
              typeof x?.name === 'string' &&
              typeof x?.url === 'string' &&
              x.url.startsWith('/queue?'),
          )
          .slice(0, 20);
    } catch {
      /* Browser storage is optional. */
    }
  });
  function saveView(): void {
    const name = viewName.trim();
    if (!name) return;
    const next = [
      ...saved.filter((v) => v.name !== name),
      { name, url: '/queue' + toQuery({ ...query, since: filters.since, until: filters.until }) },
    ].slice(-20);
    try {
      localStorage.setItem('zoomies.queue.views', JSON.stringify(next));
      saved = next;
      saveOpen = false;
      viewName = '';
    } catch (e) {
      toasts.fromError(e, 'Could not save this view');
    }
  }
</script>

{#snippet statusCell(job: Job)}
  <ActivityStatus
    activity={queuedActivity(job, prefs.quirkyStatus)}
    seed={job.id ?? `${job.repo}/${job.job_name}`}
  />
{/snippet}
{#snippet jobCell(job: Job)}<div class="cell">
    <strong>{readableJobText(job.job_name) || 'Unnamed job'}</strong><span
      >{job.workflow || 'Workflow'}</span
    >
  </div>{/snippet}
{#snippet repoCell(job: Job)}<div class="cell">
    <span class="repo">{job.repo}</span><span>{job.head_branch || NO_VALUE}</span>
  </div>{/snippet}
{#snippet runCell(job: Job)}
  <GitHubLink
    href={job.html_url}
    runNumber={job.run_number}
    label="Open {readableJobText(job.job_name) || 'this job'} on GitHub, in a new tab"
    onclick={(event) => event.stopPropagation()}
  />
{/snippet}
{#snippet poolCell(job: Job)}
  <div class="cell">
    <span>{job.pool_name || 'Unclaimed'}</span><span
      >{job.pool_id
        ? `Priority ${fleet.pools.find((p) => p.id === job.pool_id)?.priority ?? 0}`
        : 'Open for matching details'}</span
    >
  </div>
{/snippet}
{#snippet labelsCell(job: Job)}<JobLabels labels={job.labels} />{/snippet}
{#snippet waitCell(job: Job)}<Duration from={job.queued_at} live />{/snippet}
{#snippet actionCell(job: Job)}
  <RowActions actions={rowActions(job)} subject={subjectOf(job)} />
{/snippet}

<PageHeader
  title="Queue"
  subtitle="Shape runner demand. Keep the right work moving."
  onrefresh={async () => {
    liveKey += 1;
  }}
>
  <Button variant="secondary" icon={Bookmark} onclick={() => (saveOpen = true)}>Save view</Button>
  <Button variant="ghost" href="/runners">View runners</Button>
</PageHeader>

<div class="queue-content">
  <div class="summary" aria-label="Provisioning status filters">
    {#each statuses as item (item.value)}
      <button
        class="summary-card"
        class:active={provisioning.length === 1 && provisioning[0] === item.value}
        onclick={() => setStatus([item.value])}
        aria-pressed={provisioning.length === 1 && provisioning[0] === item.value}
      >
        <span class="card-title">{item.label}</span><strong
          >{counts[item.value]?.toLocaleString() ?? NO_VALUE}</strong
        ><span class="card-hint">{item.hint}</span>
      </button>
    {/each}
  </div>
  {#if Object.keys(counts).length}
    <ChartPanel
      title="Provisioning demand"
      description="Counts match your repository, workflow, pool and other filters across all pages. Status filters do not narrow this breakdown. Ready and Run now remain subject to scheduling limits."
    >
      <div class="demand-heading">
        <strong
          >{((counts.ready ?? 0) + (counts.expedited ?? 0)).toLocaleString()} active demand</strong
        ><span
          >{(counts.paused ?? 0).toLocaleString()} on hold · {(
            counts.deleted ?? 0
          ).toLocaleString()} removed</span
        >
      </div>
      <StateBreakdown
        label="Provisioning states"
        segments={[
          { label: QUEUE_STATUS_LABELS.ready, value: counts.ready ?? 0, tone: 'idle' },
          { label: QUEUE_STATUS_LABELS.expedited, value: counts.expedited ?? 0, tone: 'accent' },
          { label: QUEUE_STATUS_LABELS.paused, value: counts.paused ?? 0, tone: 'draining' },
          { label: QUEUE_STATUS_LABELS.deleted, value: counts.deleted ?? 0, tone: 'neutral' },
        ]}
      />
    </ChartPanel>
  {/if}
  <div class="policy">
    <ListOrdered size={20} />
    <div>
      <strong>Predictable provisioning, within your limits</strong>
      <p>
        Higher pool priority first. Run now takes precedence within a priority tier; other pools
        take turns, least recently provisioned first. Within a pool, demand is oldest first with a
        stable ID tie-break.
      </p>
      <details>
        <summary>What these controls affect</summary>
        <p>
          Controls change new runner demand. Existing runners, tasks already issued and minimum warm
          capacity remain available. Run now respects pool and host limits, repository quotas and
          failure backoff. GitHub still assigns jobs to available runners.
        </p>
      </details>
    </div>
  </div>
  <div class="filters-panel">
    <JobFilters
      queue
      value={filters}
      {facets}
      pools={fleet.pools}
      {labelOptions}
      onchange={patch}
      onclear={clear}
    />
    <div class="extra-filters">
      <FacetMenu
        label="Provisioning"
        options={statuses}
        selected={provisioning}
        onchange={setStatus}
      />
      <Input
        size="sm"
        value={branch}
        ariaLabel="Filter by branch"
        placeholder="Exact branch name"
        oninput={(e) =>
          router.setQuery({
            branch: (e.currentTarget as HTMLInputElement).value || null,
            offset: null,
          })}
      />
      {#if saved.length}<Select
          size="sm"
          ariaLabel="Saved queue views"
          value=""
          options={[
            { value: '', label: 'Saved views' },
            ...saved.map((v) => ({ value: v.url, label: v.name })),
          ]}
          onchange={(url) => {
            if (url) router.navigate(url);
          }}
        />{/if}
      <Button variant="ghost" size="sm" onclick={clear}>Reset filters</Button>
      <span class="filter-hint"
        >Values within a filter match any; filters combine. Labels must all match.</span
      >
    </div>
  </div>
  {#if report}
    <div class="result" role="status">
      <div>
        <strong
          >{report.changed} updated{report.failed.length
            ? ` · ${report.failed.length} skipped`
            : ''}</strong
        >
        {#if report.failed.length}<details>
            <summary>Review skipped items</summary>
            <ul>
              {#each report.failed.slice(0, 50) as item (item.id)}<li>
                  {item.id}: {item.error}
                </li>{/each}
            </ul>
            {#if report.failed.length > 50}<p>
                Showing the first 50 of {report.failed.length} skipped items.
              </p>{/if}
          </details>{/if}
      </div>
      <Button
        variant="ghost"
        size="sm"
        icon={X}
        ariaLabel="Dismiss results"
        onclick={() => (report = null)}>Dismiss</Button
      >
    </div>
  {/if}
  {#if canOperate}
    <div class="selection-bar" aria-live="polite">
      {#if snapshot.length}
        <strong>{snapshot.length.toLocaleString()} matching items selected</strong>
        <span>Snapshot across all pages</span>
        <div class="selection-actions">
          {#each actions as action (action.id)}<Button
              size="sm"
              variant={action.danger ? 'danger' : 'secondary'}
              icon={action.icon}
              onclick={() => {
                void ask(action.id, snapshot);
              }}>{action.label}</Button
            >{/each}
          <Button size="sm" variant="ghost" onclick={() => (snapshot = [])}>Clear selection</Button>
        </div>
      {:else}
        <span
          ><strong>{total.toLocaleString()}</strong> matching items · select rows or the full filtered
          set</span
        >
        <Button
          size="sm"
          variant="secondary"
          loading={selecting}
          disabled={total === 0}
          onclick={() => {
            void selectAll();
          }}>Select all matching</Button
        >
      {/if}
    </div>
  {/if}
  {#key snapshot.length > 0}
    <DataGrid
      gridId="provisioning-queue"
      label="Provisioning queue"
      {columns}
      fetcher={fetchRows}
      {rowId}
      filters={query}
      defaultSort="queued_at"
      defaultOrder="asc"
      noun="items"
      {liveKey}
      selectable={canOperate && !snapshot.length}
      {bulkActions}
      onopen={openJob}
      onrows={takeRows}
      emptyTitle="No provisioning items match"
      emptyDescription="New demand appears here when GitHub queues work for this fleet. Widen your filters or review removed items."
    >
      {#snippet emptyAction()}<Button variant="secondary" onclick={clear}>Reset filters</Button
        >{/snippet}
    </DataGrid>
  {/key}
  <p class="footnote">
    The table is sorted by waiting time by default. Sorting changes this view, not scheduling
    priority. Open an item to see why it is waiting.
  </p>
</div>

<ConfirmDialog
  bind:open={confirmOpen}
  title={provisioningActionLabel(pending?.action)}
  name={pending?.name}
  description={`${pending?.name ?? ''}. ${description}`}
  consequences={[
    'Only the selected provisioning demand changes. Existing runners and GitHub jobs are unaffected.',
    'Items that start or finish before this action reaches the server are skipped and reported.',
  ]}
  confirmLabel={provisioningActionLabel(pending?.action)}
  tone={pending?.action === 'delete' ? 'danger' : 'default'}
  onconfirm={confirm}
  oncancel={cancel}
/>
<JobDrawer bind:open={drawerOpen} job={selectedJob} onclose={() => (selectedJob = null)} />
<Dialog bind:open={saveOpen} title="Save queue view" size="sm">
  <form
    id="queue-view-form"
    onsubmit={(e) => {
      e.preventDefault();
      saveView();
    }}
  >
    <Field
      label="View name"
      hint="Saves these filters in this browser. The address bar also provides a shareable link."
    >
      {#snippet children({ id, describedBy, invalid })}
        <Input
          bind:value={viewName}
          {id}
          {describedBy}
          {invalid}
          placeholder="Release builds on Linux"
        />
      {/snippet}
    </Field>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (saveOpen = false)}>Cancel</Button>
    <Button type="submit" form="queue-view-form" variant="primary" disabled={!viewName.trim()}>
      Save view
    </Button>
  {/snippet}
</Dialog>

<style>
  .demand-heading {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    justify-content: space-between;
    gap: var(--z-space-3);
    margin-bottom: var(--z-space-4);
  }
  .demand-heading strong {
    font-size: var(--z-text-lg);
    font-variant-numeric: tabular-nums;
  }
  .demand-heading span {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }

  .queue-content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    min-width: 0;
  }
  .summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: var(--z-space-3);
  }
  .summary-card {
    text-align: left;
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    padding: var(--z-space-4);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-lg);
    background: var(--z-surface);
    color: var(--z-text);
    cursor: pointer;
  }
  .summary-card:hover,
  .summary-card.active {
    border-color: var(--z-accent);
    background: var(--z-accent-subtle);
  }
  .summary-card:focus-visible {
    outline: 2px solid var(--z-accent);
    outline-offset: 2px;
  }
  .summary-card strong {
    font-size: var(--z-text-2xl);
    font-variant-numeric: tabular-nums;
  }
  .card-title {
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-medium);
  }
  .card-hint,
  .filter-hint,
  .footnote {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  .policy {
    display: flex;
    align-items: flex-start;
    gap: var(--z-space-3);
    padding: var(--z-space-4);
    background: var(--z-surface);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-lg);
    font-size: var(--z-text-sm);
  }
  .policy :global(svg) {
    flex-shrink: 0;
    color: var(--z-accent);
  }
  .policy p {
    color: var(--z-text-muted);
    margin-top: var(--z-space-2);
    max-width: 100ch;
  }
  details {
    margin-top: var(--z-space-2);
  }
  summary {
    cursor: pointer;
    color: var(--z-accent);
  }
  .filters-panel {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  .extra-filters {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .extra-filters :global(.wrap) {
    max-width: 240px;
  }
  .selection-bar,
  .result {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-3) var(--z-space-4);
    background: var(--z-surface);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    font-size: var(--z-text-sm);
  }
  /*
    Under the top bar, which is sticky too: pinned at the top of the page the
    bar slid up behind it, and its actions could be neither seen nor pressed
    until the operator scrolled back up a long queue.
  */
  .selection-bar {
    position: sticky;
    top: calc(var(--z-topbar-height) + var(--z-space-2));
    z-index: var(--z-layer-sticky);
  }
  .selection-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-2);
  }
  .selection-bar > span {
    color: var(--z-text-muted);
  }
  .cell {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
  }
  .cell strong {
    font-weight: var(--z-weight-medium);
  }
  .cell > span:last-child {
    color: var(--z-text-muted);
    font-size: var(--z-text-xs);
  }
  .cell .repo {
    color: var(--z-text);
  }
  form {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }
  ul {
    padding-left: var(--z-space-5);
    overflow-wrap: anywhere;
  }
  @media (max-width: 767px) {
    .summary {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
    .summary-card {
      padding: var(--z-space-3);
    }
    .filter-hint {
      flex-basis: 100%;
    }
    .selection-bar {
      position: static;
    }
    .extra-filters :global(.wrap) {
      max-width: 100%;
    }
  }
</style>
