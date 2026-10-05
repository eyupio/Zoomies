<!--
  Pools: what runners to make.

  Demand and headroom summaries lead into the grid. Everything an operator asks of this list -- is it
  enabled, is it at its ceiling, is anything queued behind it, and is anything
  dangerous switched on -- is answerable without opening a row.
-->
<script lang="ts">
  import PoolPressure from '$lib/insights/PoolPressure.svelte';
  import {
    FileDown,
    FileUp,
    Gauge,
    Pencil,
    Plug,
    Plus,
    Power,
    PowerOff,
    Search,
    Trash2,
  } from '@lucide/svelte';
  import {
    deletePool,
    disablePool,
    enablePool,
    getAutoPools,
    listInstallations,
    listPools,
    poolsExportUrl,
  } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { AutoPools, Pool } from '$lib/api/types';
  import { formatGoDuration, formatNumber, parseGoDuration, pluralise } from '$lib/format';
  import { registerSearch } from '$lib/keys';
  import { navigate, router } from '$lib/router';
  import { poolStatus } from '$lib/status';
  import { fleet } from '$lib/state/fleet.svelte';
  import { session } from '$lib/state/session.svelte';
  import { toasts } from '$lib/state/toasts.svelte';
  import StateCell from '$lib/components/StateCell.svelte';
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
  import AutoPoolsPanel from '$lib/pools/AutoPoolsPanel.svelte';
  import ImportPoolsDialog from '$lib/pools/ImportPoolsDialog.svelte';
  import PoolAutoDialog from '$lib/pools/PoolAutoDialog.svelte';
  import PoolLabels from '$lib/pools/PoolLabels.svelte';
  import PoolRunnerLimitsDialog from '$lib/pools/PoolRunnerLimitsDialog.svelte';
  import PoolRiskBadge from '$lib/pools/PoolRiskBadge.svelte';
  import { backendLabel, dockerModeLabel, platformLabelOrAny } from '$lib/pools/vocabulary';
  import { isAutomatic } from '$lib/pools/auto';
  import { deletionConsequences } from '$lib/pools/consequences';
  import Badge from '$lib/components/Badge.svelte';

  const canOperate = $derived(session.can('operator'));

  /*
    Moving pools as a file. The export is a navigation, as the settings export
    is, so the browser does the download and the cookie goes with it; YAML is
    the form a repository keeps, JSON carries when and where it was taken.
  */
  const exportItems: MenuItem[] = [
    {
      id: 'yaml',
      label: 'As YAML',
      icon: FileDown,
      onSelect: () => window.location.assign(poolsExportUrl('yaml')),
    },
    {
      id: 'json',
      label: 'As JSON, with provenance',
      icon: FileDown,
      onSelect: () => window.location.assign(poolsExportUrl('json')),
    },
  ];
  let importOpen = $state(false);

  /**
   * Whether a pool is even possible yet.
   *
   * The wizard's first step already refuses without an installation; knowing it
   * here means the page can offer the step that unblocks it rather than the one
   * that cannot be completed. Null while the answer is unknown, so neither
   * button flickers into the wrong label on load.
   */
  let installationCount = $state<number | null>(null);
  const noInstallation = $derived(installationCount === 0);
  $effect(() => {
    void listInstallations()
      .then((result) => (installationCount = (result.items ?? []).length))
      .catch(() => (installationCount = null));
  });

  /* -- what the controller is doing about size classes ----------------------
   * Fetched here, not kept in the fleet cache: it is read on this page and on a
   * pool's own, and a fourth collection there would cost every signed-in tab a
   * request on every reconcile. It follows the stream instead -- a pool or a
   * host changing is what changes it -- and the burst a host joining causes is
   * one fetch, because the answer is the same for all of them.
   * ------------------------------------------------------------------------ */

  let autoStatus = $state<AutoPools | null>(null);

  $effect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let controller = new AbortController();
    const load = (): void => {
      controller.abort();
      controller = new AbortController();
      const signal = controller.signal;
      // A failed read leaves what is on screen: the panel is context for the
      // grid, and the grid reports an outage for itself.
      void getAutoPools(signal)
        .then((result) => (autoStatus = result))
        .catch(() => undefined);
    };
    load();
    const soon = (): void => {
      clearTimeout(timer);
      timer = setTimeout(load, 800);
    };
    const stop = [
      events.subscribe(['pool.created', 'pool.updated', 'pool.deleted'], soon),
      events.subscribe(['host.updated', 'host.deleted'], soon),
      events.subscribe('problems.updated', soon),
    ];
    return () => {
      clearTimeout(timer);
      controller.abort();
      for (const off of stop) off();
    };
  });

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
      case 'platform':
        return platformLabelOrAny(a.platform).localeCompare(platformLabelOrAny(b.platform));
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

  /**
   * Warm the fleet cache with the page on screen, so the command palette can
   * find a pool the operator is looking at.
   *
   * Only when it would actually change something: ingesting a row that differs
   * bumps the fleet's shape, and the fleet's shape is this grid's `liveKey`,
   * so warming the cache on every page unconditionally has the grid
   * refetching itself for ever -- and, with no pools at all, never settling
   * on the empty state.
   * Identity is what the palette needs, so identity is what is compared;
   * the live counts move on their own and are not worth a round trip.
   */
  function takeRows(rows: Pool[]): void {
    const changed = rows.some((row) => {
      const cached = fleet.pool(row.id);
      return (
        !cached ||
        cached.name !== row.name ||
        cached.enabled !== row.enabled ||
        cached.installation_target !== row.installation_target ||
        (cached.labels ?? []).join(',') !== (row.labels ?? []).join(',')
      );
    });
    if (changed) fleet.ingestPools(rows);
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
            icon: Power,
            run: (ids) => bulkSetEnabled(ids, true),
          },
          {
            id: 'disable',
            label: 'Disable',
            icon: PowerOff,
            run: (ids) => bulkSetEnabled(ids, false),
          },
          {
            // Editing is the third thing an operator wants from a ticked row,
            // and it was only on the row's own menu -- so having ticked a pool
            // to disable it, changing its image meant untick, find the row
            // again, open the menu. One at a time, because there is nothing
            // sensible to show for five pools at once.
            id: 'edit',
            label: 'Edit',
            icon: Pencil,
            single: true,
            run: (ids) => {
              const id = ids[0];
              if (id) navigate(`/pools/${id}?edit=1`);
            },
          },
        ]
      : [],
  );

  function actionsFor(pool: Pool): MenuItem[] {
    const enabled = pool.enabled !== false;
    if (isAutomatic(pool)) {
      // A pool the controller keeps is taken out of use by pausing it, and its
      // limits follow its hosts, so those two items are the ones that change.
      const paused = pool.auto?.paused === true;
      return [
        paused
          ? {
              id: 'resume',
              label: 'Resume',
              icon: Power,
              onSelect: () => setPaused(pool, false),
            }
          : {
              id: 'pause',
              label: 'Pause',
              icon: PowerOff,
              onSelect: () => setPaused(pool, true),
            },
        {
          id: 'auto-settings',
          label: 'Settings',
          icon: Pencil,
          onSelect: () => askAuto(pool),
        },
        {
          id: 'delete',
          label: 'Delete',
          icon: Trash2,
          danger: true,
          separated: true,
          // Not while the controller is keeping it, because it would make the
          // pool again; the panel above the grid says why. A pool it is not
          // keeping -- the switch only reports, or the pool belongs to another
          // installation -- is a leftover, and goes like any other.
          disabled: pool.auto?.kept === true,
          onSelect: () => askDelete(pool),
        },
      ];
    }
    return [
      enabled
        ? {
            id: 'disable',
            label: 'Disable',
            icon: PowerOff,
            onSelect: () => setEnabled(pool, false),
          }
        : {
            id: 'enable',
            label: 'Enable',
            icon: Power,
            onSelect: () => setEnabled(pool, true),
          },
      {
        id: 'runner-limits',
        label: 'Adjust runner limits',
        icon: Gauge,
        onSelect: () => askSize(pool),
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

  /**
   * Pause or resume a pool the controller keeps. The server reads enable and
   * disable on such a pool as exactly this, so the request is the one the other
   * pools use; what differs is what the row says while it waits for the answer.
   */
  function setPaused(pool: Pool, paused: boolean): void {
    if (!pool.id || !pool.auto) return;
    const id = pool.id;
    void fleet.optimistic(
      id,
      { auto: { ...pool.auto, paused }, ...(paused ? { enabled: false } : {}) },
      () => (paused ? disablePool(id) : enablePool(id)),
      paused ? 'That pool was not paused' : 'That pool was not resumed',
    );
  }

  let autoPool = $state<Pool | null>(null);
  let autoOpen = $state(false);

  function askAuto(pool: Pool): void {
    autoPool = pool;
    autoOpen = true;
  }

  let sizing = $state<Pool | null>(null);
  let sizeOpen = $state(false);

  function askSize(pool: Pool): void {
    sizing = pool;
    sizeOpen = true;
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

  const doomedConsequences = $derived(
    doomed
      ? deletionConsequences({ ...doomed.counts, queued: doomed.queued_jobs }, forceDelete)
      : [],
  );

  async function confirmDelete(): Promise<boolean> {
    const pool = doomed;
    if (!pool?.id) return false;
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
      return true;
    } catch (cause) {
      toasts.fromError(cause, 'That pool was not deleted');
      return false;
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
        priority: 'wide',
        sortable: true,
        value: (row) => row.installation_target ?? '',
      },
      {
        id: 'backend',
        header: 'Backend',
        priority: 'wide',
        sortable: true,
        value: (row) => backendLabel(row.backend),
      },
      {
        id: 'platform',
        header: 'Platform',
        priority: 'wide',
        sortable: true,
        value: (row) => platformLabelOrAny(row.platform),
      },
      {
        id: 'live',
        header: 'Runners',
        sortable: true,
        width: '13rem',
        value: (row) => formatNumber(row.counts?.live ?? 0),
        cell: runnersCell,
      },
      {
        id: 'queued',
        header: 'Queued',
        priority: 'wide',
        sortable: true,
        align: 'end',
        width: '6rem',
        value: (row) => formatNumber(row.queued_jobs ?? 0),
        cell: queuedCell,
      },
      {
        id: 'idle_timeout',
        header: 'Idle timeout',
        priority: 'wide',
        sortable: true,
        align: 'end',
        value: (row) => formatGoDuration(row.idle_timeout),
      },
      {
        id: 'ephemeral',
        header: 'Lifetime',
        priority: 'wide',
        value: (row) => (row.ephemeral === false ? 'Reused' : 'One job'),
      },
      {
        id: 'docker_mode',
        header: 'Docker',
        priority: 'wide',
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
        fixed: true,
        // The menu is anchored in this cell, so the cell must let it out.
        overflows: true,
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
  <span class="name-line">
    <a class="pool-name" href="/pools/{pool.id}" title={pool.name ?? undefined}>
      {pool.name ?? 'unnamed'}
    </a>
    {#if pool.auto}
      <!-- The accent a host's "Embedded" badge has, never a status colour: that
           the controller keeps a pool is a fact about it, not a state it is in. -->
      <Badge tone="accent" label="Automatic" size="sm" dot={false} title={pool.auto.summary} />
    {/if}
  </span>
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
  <StateCell status={poolStatus(pool)} />
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
  onrefresh={() => fleet.reconcile()}
>
  <DropdownMenu
    items={exportItems}
    label="Export the pools"
    triggerLabel="Export"
    triggerIcon={FileDown}
  />
  {#if canOperate && !noInstallation}
    <Button variant="secondary" icon={FileUp} onclick={() => (importOpen = true)}>Import</Button>
  {/if}
  {#if canOperate}
    {#if noInstallation}
      <!-- A pool registers its runners with a GitHub App installation, so the
           wizard's first step refuses without one. Offering the answer here
           beats offering a button whose first screen is a refusal. -->
      <Button variant="primary" icon={Plug} href="/installations">Connect GitHub</Button>
    {:else}
      <Button variant="primary" icon={Plus} href="/pools/new">Create a pool</Button>
    {/if}
  {/if}
</PageHeader>

<div class="filters">
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
</div>

<PoolPressure pools={fleet.pools.filter(matches)} />

<AutoPoolsPanel status={autoStatus} />

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
  liveKey={fleet.shape}
  noun="pools"
  onopen={(row) => navigate(`/pools/${row.id ?? ''}`)}
  onrows={takeRows}
  emptyTitle={filtering ? 'No pools match those filters' : 'No pools yet'}
  emptyDescription={filtering
    ? 'Every pool is hidden by the search or the status filter currently applied.'
    : noInstallation
      ? 'A pool decides what labels your runners answer to and how many of them exist. It registers those runners with a GitHub App installation, so that comes first.'
      : 'A pool decides what labels your runners answer to and how many of them exist.'}
>
  {#snippet emptyAction()}
    {#if filtering}
      <Button onclick={clearFilters}>Clear filters</Button>
    {:else if canOperate && noInstallation}
      <Button variant="primary" icon={Plug} href="/installations">Connect GitHub</Button>
    {:else if canOperate}
      <Button variant="primary" icon={Plus} href="/pools/new">Create a pool</Button>
    {:else}
      <p class="no-permission">Ask an operator to create one.</p>
    {/if}
  {/snippet}
</DataGrid>

<ImportPoolsDialog bind:open={importOpen} onapplied={() => void fleet.reconcile()} />

<PoolRunnerLimitsDialog bind:open={sizeOpen} pool={sizing} onclose={() => (sizing = null)} />

<PoolAutoDialog bind:open={autoOpen} pool={autoPool} onclose={() => (autoPool = null)} />

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
  .filters {
    margin-bottom: var(--z-space-4);
  }
  .search {
    min-width: 18rem;
    flex: 1 1 18rem;
  }
  .status-filter {
    width: 10rem;
  }
  /* The badge under the name, not beside it: the Name column is the first the
     grid narrows on a laptop, and a name squeezed to one letter beside its own
     badge is a pool nobody can tell from the next. */
  .name-line {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: var(--z-space-1);
    min-width: 0;
    max-width: 100%;
  }
  .name-line .pool-name {
    min-width: 0;
  }
  /* Pool names are hyphenated, so without this they set one segment per line
     and the whole row grows to fit. The full name is in the title. */
  .pool-name {
    display: block;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
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
