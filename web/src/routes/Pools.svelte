<!--
  Pools: what runners to make.

  The grid is the whole page. Everything an operator asks of this list -- is it
  enabled, is it at its ceiling, is anything queued behind it, and is anything
  dangerous switched on -- is answerable without opening a row.
-->
<script lang="ts">
  import { Pause, Pencil, Play, Plus, Search, Trash2 } from '@lucide/svelte';
  import { deletePool, disablePool, enablePool, listPools } from '$lib/api/client';
  import type { Pool } from '$lib/api/types';
  import { formatGoDuration, formatNumber, parseGoDuration, pluralise } from '$lib/format';
  import { registerSearch } from '$lib/keys';
  import { navigate, router } from '$lib/router';
  import { poolStatus } from '$lib/status';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Checkbox from '$lib/components/Checkbox.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import DataGrid from '$lib/components/DataGrid.svelte';
  import type {
    BulkAction,
    GridColumn,
    GridPage,
    GridQuery,
  } from '$lib/components/DataGrid.svelte';
  import DropdownMenu from '$lib/components/DropdownMenu.svelte';
  import type { MenuItem } from '$lib/components/DropdownMenu.svelte';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import type { FilterChip } from '$lib/components/FilterBar.svelte';
  import Input from '$lib/components/Input.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Select from '$lib/components/Select.svelte';
  import UtilisationBar from '$lib/components/UtilisationBar.svelte';
  import PoolLabels from '$lib/pools/PoolLabels.svelte';
  import PoolRiskBadge from '$lib/pools/PoolRiskBadge.svelte';
  import { backendLabel, dockerModeLabel } from '$lib/pools/PoolVocabulary.svelte';

  const canOperate = $derived(session.can('operator'));

  /* -- filters, kept in the URL so a view can be pasted to a colleague ------ */

  const search = $derived(router.param('q'));
  const status = $derived(router.param('status'));
  const filters = $derived({ q: search, status });
  const filtering = $derived(search !== '' || status !== '');

  function clearFilters(): void {
    router.setQuery({ q: null, status: null });
  }

  let searchField = $state<HTMLInputElement | null>(null);

  $effect(() => registerSearch(searchField));

  const chips = $derived.by(() => {
    const active: FilterChip[] = [];
    if (search)
      active.push({
        id: 'q',
        label: 'Matching',
        value: search,
        onremove: () => router.setQuery({ q: null }),
      });
    if (status)
      active.push({
        id: 'status',
        label: 'Status',
        value: status === 'enabled' ? 'Enabled' : 'Disabled',
        onremove: () => router.setQuery({ status: null }),
      });
    return active;
  });

  function matches(pool: Pool): boolean {
    if (status === 'enabled' && pool.enabled === false) return false;
    if (status === 'disabled' && pool.enabled !== false) return false;
    const needle = search.trim().toLowerCase();
    if (needle === '') return true;
    const haystack = [
      pool.name ?? '',
      pool.installation_target ?? '',
      pool.backend ?? '',
      ...(pool.labels ?? []),
    ]
      .join(' ')
      .toLowerCase();
    return haystack.includes(needle);
  }

  function compare(a: Pool, b: Pool, key: string): number {
    switch (key) {
      case 'target':
        return (a.installation_target ?? '').localeCompare(b.installation_target ?? '');
      case 'backend':
        return (a.backend ?? '').localeCompare(b.backend ?? '');
      case 'live':
        return (a.counts?.live ?? 0) - (b.counts?.live ?? 0);
      case 'queued':
        return (a.queued_jobs ?? 0) - (b.queued_jobs ?? 0);
      case 'idle_timeout':
        return (parseGoDuration(a.idle_timeout) ?? 0) - (parseGoDuration(b.idle_timeout) ?? 0);
      case 'enabled':
        return Number(a.enabled !== false) - Number(b.enabled !== false);
      default:
        return (a.name ?? '').localeCompare(b.name ?? '');
    }
  }

  /**
   * `/pools` returns every pool in one response -- a fleet has tens of them, not
   * thousands -- so the filtering, sorting and paging the grid asks for happen
   * here rather than as query parameters the API does not have.
   */
  async function fetchPools(query: GridQuery, signal: AbortSignal): Promise<GridPage<Pool>> {
    const response = await listPools(signal);
    const rows = (response.items ?? []).filter(matches);
    const direction = query.order === 'asc' ? 1 : -1;
    rows.sort((a, b) => compare(a, b, query.sort) * direction);
    return {
      items: rows.slice(query.offset, query.offset + query.limit),
      total: rows.length,
    };
  }

  /* -- actions --------------------------------------------------------------- */

  function setEnabled(pool: Pool, enabled: boolean): void {
    if (!pool.id) return;
    void fleet.optimistic(
      pool.id,
      { enabled },
      () => (enabled ? enablePool(pool.id ?? '') : disablePool(pool.id ?? '')),
      enabled ? 'That pool was not enabled' : 'That pool was not disabled',
    );
  }

  async function bulkSetEnabled(ids: string[], enabled: boolean): Promise<void> {
    const results = await Promise.allSettled(
      ids.map((id) => (enabled ? enablePool(id) : disablePool(id))),
    );
    const failed = results.filter((result) => result.status === 'rejected').length;
    const verb = enabled ? 'enabled' : 'disabled';
    if (failed === 0) {
      toasts.success(`${pluralise(ids.length, 'pool')} ${verb}`);
    } else {
      toasts.error(
        `${failed} of ${pluralise(ids.length, 'pool')} could not be ${verb}`,
        'The rest went through. Open a pool that did not change to see what the controller said.',
      );
    }
    void fleet.reconcile();
  }

  const bulkActions = $derived<BulkAction[]>(
    canOperate
      ? [
          {
            id: 'enable',
            label: 'Enable',
            icon: Play,
            run: (ids) => bulkSetEnabled(ids, true),
          },
          {
            id: 'disable',
            label: 'Disable',
            icon: Pause,
            run: (ids) => bulkSetEnabled(ids, false),
          },
        ]
      : [],
  );

  function actionsFor(pool: Pool): MenuItem[] {
    const enabled = pool.enabled !== false;
    return [
      enabled
        ? {
            id: 'disable',
            label: 'Disable',
            icon: Pause,
            onSelect: () => setEnabled(pool, false),
          }
        : {
            id: 'enable',
            label: 'Enable',
            icon: Play,
            onSelect: () => setEnabled(pool, true),
          },
      {
        id: 'edit',
        label: 'Edit',
        icon: Pencil,
        onSelect: () => navigate(`/pools/${pool.id ?? ''}?edit=1`),
      },
      {
        id: 'delete',
        label: 'Delete',
        icon: Trash2,
        danger: true,
        separated: true,
        onSelect: () => askDelete(pool),
      },
    ];
  }

  /* -- deletion --------------------------------------------------------------- */

  let doomed = $state<Pool | null>(null);
  let deleteOpen = $state(false);
  let forceDelete = $state(false);

  function askDelete(pool: Pool): void {
    doomed = pool;
    forceDelete = false;
    deleteOpen = true;
  }

  const doomedConsequences = $derived.by(() => {
    const pool = doomed;
    if (!pool) return [];
    const live = pool.counts?.live ?? 0;
    const busy = pool.counts?.busy ?? 0;
    const lines = [
      live === 0
        ? 'It has no runners right now, so nothing is interrupted.'
        : forceDelete
          ? `${pluralise(live, 'runner')} will be destroyed immediately.`
          : `${pluralise(live, 'runner')} will be drained, then removed.`,
    ];
    if (busy > 0) {
      lines.push(
        forceDelete
          ? `${pluralise(busy, 'job')} running right now will be interrupted.`
          : `${pluralise(busy, 'job')} running right now will be allowed to finish first.`,
      );
    }
    lines.push('The runners are deregistered from GitHub either way.');
    return lines;
  });

  async function confirmDelete(): Promise<void> {
    const pool = doomed;
    if (!pool?.id) return;
    try {
      const result = await deletePool(pool.id, { drain: !forceDelete, force: forceDelete });
      const affected = result?.runners_affected ?? 0;
      toasts.success(
        `Deleted ${pool.name ?? 'the pool'}`,
        affected > 0
          ? `${pluralise(affected, 'runner')} ${forceDelete ? 'destroyed' : 'draining'}.`
          : undefined,
      );
      doomed = null;
      void fleet.reconcile();
    } catch (cause) {
      toasts.fromError(cause, 'That pool was not deleted');
    }
  }

  /* -- columns ---------------------------------------------------------------- */

  const columns = $derived.by(() => {
    const list: GridColumn<Pool>[] = [
      {
        id: 'name',
        header: 'Name',
        sortable: true,
        hideable: false,
        width: '16rem',
        value: (row) => row.name ?? '',
        cell: nameCell,
      },
      { id: 'risk', header: 'Risk', width: '7rem', value: () => '', cell: riskCell },
      {
        id: 'labels',
        header: 'Labels',
        value: (row) => (row.labels ?? []).join(', '),
        cell: labelsCell,
      },
      {
        id: 'target',
        header: 'Target',
        sortable: true,
        value: (row) => row.installation_target ?? '',
      },
      {
        id: 'backend',
        header: 'Backend',
        sortable: true,
        value: (row) => backendLabel(row.backend),
      },
      {
        id: 'live',
        header: 'Runners',
        sortable: true,
        width: '13rem',
        value: (row) => String(row.counts?.live ?? 0),
        cell: runnersCell,
      },
      {
        id: 'queued',
        header: 'Queued',
        sortable: true,
        align: 'end',
        width: '6rem',
        value: (row) => formatNumber(row.queued_jobs ?? 0),
        cell: queuedCell,
      },
      {
        id: 'idle_timeout',
        header: 'Idle timeout',
        sortable: true,
        align: 'end',
        value: (row) => formatGoDuration(row.idle_timeout),
      },
      {
        id: 'ephemeral',
        header: 'Lifetime',
        value: (row) => (row.ephemeral === false ? 'Reused' : 'One job'),
      },
      {
        id: 'docker_mode',
        header: 'Docker',
        value: (row) => dockerModeLabel(row.docker_mode),
      },
      {
        id: 'enabled',
        header: 'Status',
        sortable: true,
        width: '8rem',
        value: (row) => (row.enabled === false ? 'Disabled' : 'Enabled'),
        cell: statusCell,
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

{#snippet nameCell(pool: Pool)}
  <a class="pool-name" href="/pools/{pool.id}">{pool.name ?? 'unnamed'}</a>
{/snippet}

{#snippet riskCell(pool: Pool)}
  <PoolRiskBadge {pool} />
{/snippet}

{#snippet labelsCell(pool: Pool)}
  <PoolLabels labels={pool.labels ?? []} />
{/snippet}

{#snippet runnersCell(pool: Pool)}
  <UtilisationBar
    busy={pool.counts?.busy ?? 0}
    live={pool.counts?.live ?? 0}
    min={pool.min_runners}
    max={pool.max_runners}
    label="Runners in {pool.name ?? 'this pool'}"
  />
{/snippet}

{#snippet queuedCell(pool: Pool)}
  {#if (pool.queued_jobs ?? 0) > 0}
    <span class="queued">{formatNumber(pool.queued_jobs ?? 0)}</span>
  {:else}
    <span class="quiet">0</span>
  {/if}
{/snippet}

{#snippet statusCell(pool: Pool)}
  <Badge status={poolStatus(pool)} size="sm" />
{/snippet}

{#snippet actionsCell(pool: Pool)}
  <div class="row-actions" role="presentation" onclick={stopRowClick}>
    <DropdownMenu
      items={actionsFor(pool)}
      label="Actions for {pool.name ?? 'this pool'}"
      size="sm"
    />
  </div>
{/snippet}

<PageHeader
  title="Pools"
  subtitle="A pool decides what labels your runners answer to, and how many of them exist."
>
  {#if canOperate}
    <Button variant="primary" icon={Plus} href="/pools/new">Create a pool</Button>
  {/if}
</PageHeader>

<FilterBar {chips} onclear={clearFilters}>
  <div class="search">
    <Input
      bind:element={searchField}
      value={search}
      type="search"
      icon={Search}
      size="sm"
      placeholder="Search pools by name, label or target"
      ariaLabel="Search pools"
      oninput={(event) =>
        router.setQuery({ q: (event.currentTarget as HTMLInputElement).value || null })}
    />
  </div>
  <div class="status-filter">
    <Select
      value={status}
      size="sm"
      ariaLabel="Filter by status"
      options={[
        { value: '', label: 'Any status' },
        { value: 'enabled', label: 'Enabled' },
        { value: 'disabled', label: 'Disabled' },
      ]}
      onchange={(value) => router.setQuery({ status: value || null })}
    />
  </div>
</FilterBar>

<DataGrid
  gridId="pools"
  label="Pools"
  {columns}
  {filters}
  fetcher={fetchPools}
  rowId={(row) => row.id ?? ''}
  defaultSort="name"
  defaultOrder="asc"
  selectable={canOperate}
  {bulkActions}
  liveKey={fleet.version}
  noun="pools"
  onopen={(row) => navigate(`/pools/${row.id ?? ''}`)}
  onrows={(rows) => fleet.ingestPools(rows)}
  emptyTitle={filtering ? 'No pools match those filters' : 'No pools yet'}
  emptyDescription={filtering
    ? 'Every pool is hidden by the search or the status filter currently applied.'
    : 'A pool decides what labels your runners answer to and how many of them exist.'}
>
  {#snippet emptyAction()}
    {#if filtering}
      <Button onclick={clearFilters}>Clear filters</Button>
    {:else if canOperate}
      <Button variant="primary" icon={Plus} href="/pools/new">Create a pool</Button>
    {:else}
      <p class="no-permission">Ask an operator to create one.</p>
    {/if}
  {/snippet}
</DataGrid>

<ConfirmDialog
  bind:open={deleteOpen}
  title="Delete pool"
  name={doomed?.name ?? ''}
  description="Deleting {doomed?.name ??
    'this pool'} removes it from the controller. Queued jobs asking for its labels will stop matching anything."
  consequences={doomedConsequences}
  confirmLabel="Delete pool"
  requireName
  onconfirm={confirmDelete}
  oncancel={() => (doomed = null)}
>
  <Checkbox
    bind:checked={forceDelete}
    label="Destroy its runners immediately"
    description="Without this, runners finish the job they are on and then exit. With it, work in progress is interrupted."
  />
</ConfirmDialog>

<style>
  .search {
    min-width: 18rem;
    flex: 1 1 18rem;
  }
  .status-filter {
    width: 10rem;
  }
  .pool-name {
    color: var(--z-accent);
    font-weight: var(--z-weight-medium);
    text-decoration: none;
  }
  .pool-name:hover {
    text-decoration: underline;
  }
  .queued {
    font-weight: var(--z-weight-semibold);
    color: var(--z-pending);
  }
  .quiet {
    color: var(--z-text-subtle);
  }
  .row-actions {
    display: flex;
    justify-content: flex-end;
  }
  .no-permission {
    margin: 0;
    font-size: var(--z-text-base);
    color: var(--z-text-muted);
  }
</style>
