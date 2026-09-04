<!--
  Runners: what exists right now.

  The grid is the page. Every filter lives in the query string, so a narrowed
  view -- "busy runners on host-2" -- is a URL somebody can paste into a chat
  window and land on exactly what you were looking at.

  It stays live without a refresh button: the fleet cache applies runner.*
  events as they arrive, the grid refetches off its version counter, and the
  state cell reads the cached runner first so a badge flips the moment the
  event lands rather than when the next page of rows does.
-->
<script lang="ts">
  import { CircleSlash, Search, Trash2 } from '@lucide/svelte';
  import { listRunners } from '$lib/api/client';
  import { RUNNER_STATES, type Runner, type RunnerState } from '$lib/api/types';
  import { formatBytes, formatPercent } from '$lib/format';
  import { registerSearch } from '$lib/keys';
  import { navigate, router } from '$lib/router';
  import { runnerStatus } from '$lib/status';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import DataGrid from '$lib/components/DataGrid.svelte';
  import type {
    BulkAction,
    GridColumn,
    GridPage,
    GridQuery,
  } from '$lib/components/DataGrid.svelte';
  import DropdownMenu from '$lib/components/DropdownMenu.svelte';
  import type { MenuItem } from '$lib/components/DropdownMenu.svelte';
  import Duration from '$lib/components/Duration.svelte';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import type { FilterChip } from '$lib/components/FilterBar.svelte';
  import Input from '$lib/components/Input.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Select from '$lib/components/Select.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import RunnerConfirm from '$lib/runners/RunnerConfirm.svelte';
  import RunnerStateCell from '$lib/runners/RunnerStateCell.svelte';
  import RunnerStateFilter from '$lib/runners/RunnerStateFilter.svelte';

  const canOperate = $derived(session.can('operator'));

  /* -- filters, kept in the URL under the API's own names ------------------- */

  const search = $derived(router.param('q'));
  const poolId = $derived(router.param('pool_id'));
  const hostId = $derived(router.param('host_id'));
  const includeRemoved = $derived(router.param('include_removed') === '1');
  const states = $derived(
    router
      .paramList('state')
      .filter((value): value is RunnerState =>
        (RUNNER_STATES as readonly string[]).includes(value),
      ),
  );

  const filters = $derived({ search, poolId, hostId, includeRemoved, states });
  const filtered = $derived(
    search !== '' || poolId !== '' || hostId !== '' || includeRemoved || states.length > 0,
  );

  let searchField = $state<HTMLInputElement | null>(null);
  $effect(() => registerSearch(searchField));

  const poolOptions = $derived([
    { value: '', label: 'Any pool' },
    ...fleet.pools.map((pool) => ({ value: pool.id ?? '', label: pool.name ?? 'unnamed' })),
  ]);

  const hostOptions = $derived([
    { value: '', label: 'Any host' },
    ...fleet.hosts.map((host) => ({ value: host.id ?? '', label: host.name ?? 'unnamed' })),
  ]);

  function nameOf(options: { value: string; label: string }[], id: string): string {
    return options.find((option) => option.value === id)?.label ?? id;
  }

  const chips = $derived.by(() => {
    const active: FilterChip[] = [];
    if (search) {
      active.push({
        id: 'q',
        label: 'Matching',
        value: search,
        onremove: () => router.setQuery({ q: null }),
      });
    }
    if (poolId) {
      active.push({
        id: 'pool',
        label: 'Pool',
        value: nameOf(poolOptions, poolId),
        onremove: () => router.setQuery({ pool_id: null }),
      });
    }
    if (hostId) {
      active.push({
        id: 'host',
        label: 'Host',
        value: nameOf(hostOptions, hostId),
        onremove: () => router.setQuery({ host_id: null }),
      });
    }
    for (const state of states) {
      active.push({
        id: `state-${state}`,
        label: 'State',
        value: runnerStatus(state).label,
        onremove: () => router.setQuery({ state: states.filter((s) => s !== state) }),
      });
    }
    if (includeRemoved) {
      active.push({
        id: 'include_removed',
        label: 'Removed',
        value: 'Shown',
        onremove: () => router.setQuery({ include_removed: null }),
      });
    }
    return active;
  });

  function clearFilters(): void {
    router.setQuery({
      q: null,
      pool_id: null,
      host_id: null,
      state: null,
      include_removed: null,
      offset: null,
    });
  }

  /* -- rows ------------------------------------------------------------------ */

  /** The page currently on screen, so a bulk action knows what it is acting on. */
  let onScreen: Runner[] = [];

  async function fetchRunners(query: GridQuery, signal: AbortSignal): Promise<GridPage<Runner>> {
    const response = await listRunners(
      {
        limit: query.limit,
        offset: query.offset,
        sort: query.sort || undefined,
        order: query.order,
        q: search || undefined,
        pool_id: poolId ? [poolId] : undefined,
        host_id: hostId ? [hostId] : undefined,
        state: states.length > 0 ? states : undefined,
        include_removed: includeRemoved || undefined,
      },
      signal,
    );
    return { items: response.items ?? [], total: response.total ?? 0 };
  }

  /**
   * Warm the fleet cache with the page on screen, so the command palette can
   * find a runner the operator is looking at.
   *
   * Only when it would actually change something: ingesting bumps the fleet's
   * version, and the fleet's version is this grid's `liveKey`, so warming the
   * cache on every page unconditionally would have the grid refetching itself
   * for ever. Identity is what the palette needs, so identity is what is
   * compared -- CPU and memory move constantly and are not worth a round trip.
   */
  function takeRows(rows: Runner[]): void {
    onScreen = rows;
    const changed = rows.some((row) => {
      const cached = fleet.runner(row.id);
      return (
        !cached ||
        cached.name !== row.name ||
        cached.state !== row.state ||
        cached.pool_id !== row.pool_id ||
        cached.host_id !== row.host_id
      );
    });
    if (changed) fleet.ingestRunners(rows);
  }

  function resolve(ids: readonly string[]): Runner[] {
    return ids.map((id) => onScreen.find((row) => row.id === id) ?? fleet.runner(id) ?? { id });
  }

  /* -- drain and delete ------------------------------------------------------- */

  let confirmOpen = $state(false);
  let confirmAction = $state<'drain' | 'delete'>('drain');
  let confirmTargets = $state<Runner[]>([]);

  function ask(action: 'drain' | 'delete', targets: Runner[]): void {
    if (targets.length === 0) return;
    confirmAction = action;
    confirmTargets = targets;
    confirmOpen = true;
  }

  const bulkActions = $derived<BulkAction[]>(
    canOperate
      ? [
          {
            id: 'drain',
            label: 'Drain',
            icon: CircleSlash,
            run: (ids) => ask('drain', resolve(ids)),
          },
          {
            id: 'delete',
            label: 'Delete',
            icon: Trash2,
            danger: true,
            run: (ids) => ask('delete', resolve(ids)),
          },
        ]
      : [],
  );

  function actionsFor(runner: Runner): MenuItem[] {
    const terminal = runner.state === 'removed' || runner.state === 'failed';
    return [
      {
        id: 'drain',
        label: 'Drain',
        icon: CircleSlash,
        disabled: terminal || runner.state === 'draining',
        onSelect: () => ask('drain', [runner]),
      },
      {
        id: 'delete',
        label: 'Delete',
        icon: Trash2,
        danger: true,
        separated: true,
        onSelect: () => ask('delete', [runner]),
      },
    ];
  }

  /* -- columns ---------------------------------------------------------------- */

  const columns = $derived.by(() => {
    const list: GridColumn<Runner>[] = [
      {
        id: 'state',
        header: 'State',
        sortable: true,
        hideable: false,
        width: '9rem',
        value: (row) => runnerStatus(row.state).label,
        cell: stateCell,
      },
      {
        id: 'name',
        header: 'Name',
        sortable: true,
        hideable: false,
        width: '18rem',
        value: (row) => row.name ?? '',
        cell: nameCell,
      },
      {
        id: 'pool',
        header: 'Pool',
        sortable: true,
        value: (row) => row.pool_name ?? '',
        cell: poolCell,
      },
      { id: 'host', header: 'Host', sortable: true, value: (row) => row.host_name ?? '' },
      {
        id: 'job',
        header: 'Current job',
        value: (row) => row.current_job?.job_name ?? '',
        cell: jobCell,
      },
      {
        id: 'created_at',
        header: 'Age',
        sortable: true,
        align: 'end',
        width: '7rem',
        value: (row) => row.created_at ?? '',
        cell: ageCell,
      },
      {
        id: 'jobs',
        header: 'Jobs handled',
        sortable: true,
        align: 'end',
        width: '7rem',
        value: (row) => String(row.jobs_handled ?? 0),
      },
      {
        id: 'cpu',
        header: 'CPU',
        align: 'end',
        width: '6rem',
        value: (row) =>
          row.cpu_percent === undefined ? '--' : formatPercent((row.cpu_percent ?? 0) / 100, 0),
      },
      {
        id: 'memory',
        header: 'Memory',
        align: 'end',
        width: '7rem',
        value: (row) => (row.memory_bytes === undefined ? '--' : formatBytes(row.memory_bytes)),
      },
    ];
    if (canOperate) {
      list.push({
        id: 'actions',
        header: 'Actions',
        hideable: false,
        align: 'end',
        width: '5rem',
        value: () => '',
        cell: actionsCell,
      });
    }
    return list;
  });

  function stopRowClick(event: Event): void {
    event.stopPropagation();
  }
</script>

{#snippet stateCell(runner: Runner)}
  <!-- The cached runner first: an SSE update lands here before the grid's next fetch. -->
  <RunnerStateCell status={runnerStatus(fleet.runner(runner.id)?.state ?? runner.state)} />
{/snippet}

{#snippet nameCell(runner: Runner)}
  <div class="name-cell">
    <a class="name mono" href="/runners/{runner.id}">{runner.name ?? 'unnamed'}</a>
    <span class="copy" role="presentation" onclick={stopRowClick}>
      <CopyButton value={runner.name ?? ''} label="Copy the runner name" size="sm" />
    </span>
  </div>
{/snippet}

{#snippet poolCell(runner: Runner)}
  {#if runner.pool_id}
    <a class="link" href="/pools/{runner.pool_id}" onclick={stopRowClick}>
      {runner.pool_name ?? runner.pool_id}
    </a>
  {:else}
    <span class="quiet">--</span>
  {/if}
{/snippet}

{#snippet jobCell(runner: Runner)}
  {#if runner.current_job}
    <span class="job">
      <span class="job-name">{runner.current_job.job_name ?? 'Unnamed job'}</span>
      <span class="job-repo mono">{runner.current_job.repo ?? ''}</span>
    </span>
  {:else}
    <span class="quiet">--</span>
  {/if}
{/snippet}

{#snippet ageCell(runner: Runner)}
  <Duration
    from={runner.created_at}
    to={runner.finished_at ?? undefined}
    live={!runner.finished_at}
  />
{/snippet}

{#snippet actionsCell(runner: Runner)}
  <div class="row-actions" role="presentation" onclick={stopRowClick}>
    <DropdownMenu
      items={actionsFor(runner)}
      label="Actions for {runner.name ?? 'this runner'}"
      size="sm"
    />
  </div>
{/snippet}

<PageHeader
  title="Runners"
  subtitle="Every runner the controller knows about right now, and what it is doing."
/>

<div class="filters">
  <FilterBar {chips} onclear={clearFilters}>
    <div class="search">
      <Input
        bind:element={searchField}
        value={search}
        type="search"
        icon={Search}
        size="sm"
        placeholder="Search by name, ID or container"
        ariaLabel="Search runners"
        oninput={(event) =>
          router.setQuery({ q: (event.currentTarget as HTMLInputElement).value || null })}
      />
    </div>
    <div class="picker">
      <Select
        value={poolId}
        size="sm"
        ariaLabel="Filter by pool"
        options={poolOptions}
        onchange={(value) => router.setQuery({ pool_id: value || null })}
      />
    </div>
    <div class="picker">
      <Select
        value={hostId}
        size="sm"
        ariaLabel="Filter by host"
        options={hostOptions}
        onchange={(value) => router.setQuery({ host_id: value || null })}
      />
    </div>
    <RunnerStateFilter
      selected={states}
      onchange={(next) => router.setQuery({ state: next.length > 0 ? next : null })}
    />
    <div class="removed">
      <Switch
        checked={includeRemoved}
        label="Include removed"
        description="Removed runners are hidden by default: a busy fleet makes and destroys thousands of them, and they are all history."
        onchange={(on) => router.setQuery({ include_removed: on ? '1' : null })}
      />
    </div>
  </FilterBar>
</div>

<DataGrid
  gridId="runners"
  label="Runners"
  {columns}
  {filters}
  {bulkActions}
  fetcher={fetchRunners}
  rowId={(row) => row.id ?? ''}
  defaultSort="created_at"
  defaultOrder="desc"
  selectable={canOperate}
  liveKey={fleet.version}
  noun="runners"
  onopen={(row) => navigate(`/runners/${row.id ?? ''}`)}
  onrows={takeRows}
  emptyTitle={filtered ? 'No runners match these filters' : 'No runners right now'}
  emptyDescription={filtered
    ? 'Nothing in the fleet answers all of these at once. Removing one of them will widen the search.'
    : 'That is normal when nothing is queued — runners are created on demand.'}
>
  {#snippet emptyAction()}
    {#if filtered}
      <Button variant="secondary" onclick={clearFilters}>Clear filters</Button>
    {:else}
      <Button variant="secondary" href="/pools">Look at the pools</Button>
    {/if}
  {/snippet}
</DataGrid>

<RunnerConfirm
  bind:open={confirmOpen}
  action={confirmAction}
  targets={confirmTargets}
  ondone={() => (confirmTargets = [])}
/>

<style>
  .filters {
    margin-bottom: var(--z-space-4);
  }
  .search {
    min-width: 18rem;
    flex: 1 1 18rem;
  }
  .picker {
    width: 11rem;
  }
  .removed {
    flex-basis: 100%;
    max-width: 46rem;
  }
  .name-cell {
    display: flex;
    align-items: center;
    gap: var(--z-space-1);
    min-width: 0;
  }
  .name {
    color: var(--z-accent);
    font-size: var(--z-text-xs);
    text-decoration: none;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .name:hover {
    text-decoration: underline;
  }
  .copy {
    display: inline-flex;
  }
  .link {
    color: var(--z-accent);
    text-decoration: none;
  }
  .link:hover {
    text-decoration: underline;
  }
  .quiet {
    color: var(--z-text-subtle);
  }
  .job {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .job-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .job-repo {
    font-size: var(--z-text-2xs);
    color: var(--z-text-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
  }
</style>
