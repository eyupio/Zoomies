<!--
  Every repository Kennel Club has looked at, worst first.

  The filters are the API's own, in the URL under its own names, so a view is a
  link a colleague can open: ?severity=error, ?code=exposure.public_repo_weak_pool,
  ?state=attention. A filter that matches nothing says so and offers to clear
  itself, and the page says why when there is nothing to list because Kennel Club
  is off.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { Search, Trophy } from '@lucide/svelte';
  import {
    getKennelOverview,
    listInstallations,
    listKennelChecks,
    listKennelRepositories,
  } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { KennelCatalogueEntry, KennelRepository } from '$lib/api/types';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import DataGrid from '$lib/components/DataGrid.svelte';
  import type { GridColumn, GridPage, GridQuery } from '$lib/components/DataGrid.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import FilterBar from '$lib/components/FilterBar.svelte';
  import type { FilterChip } from '$lib/components/FilterBar.svelte';
  import Input from '$lib/components/Input.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import { KENNEL_SETTING_HREF, openFindingsText, worstSeverity } from '$lib/kennel/words';
  import { registerSearch } from '$lib/keys';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { prefs } from '$lib/state/prefs.svelte';
  import { session } from '$lib/state/session.svelte';
  import { kennelStatus } from '$lib/status';

  /* -- whether there is anything to list ------------------------------------- */

  let known = $state(false);
  let enabled = $state(true);
  let reload = $state(0);

  $effect(() => {
    void reload;
    const controller = new AbortController();
    void getKennelOverview(controller.signal)
      .then((overview) => {
        enabled = overview.enabled;
        known = true;
      })
      .catch((cause: unknown) => {
        if (cause instanceof DOMException && cause.name === 'AbortError') return;
        // The grid reports a failure itself; this only decides whether to draw it.
        if (!untrack(() => known)) known = true;
      });
    return () => controller.abort();
  });

  /* -- filters, in the URL under the API's own names -------------------------- */

  const search = $derived(router.param('q'));
  const severity = $derived(router.param('severity'));
  const standing = $derived(router.param('state'));
  const code = $derived(router.param('code'));
  const installation = $derived(router.param('installation'));

  const filters = $derived({ search, severity, standing, code, installation });
  const anyFilter = $derived(Boolean(search || severity || standing || code || installation));

  let searchField = $state<HTMLInputElement | null>(null);
  $effect(() => registerSearch(searchField));

  let checks = $state<KennelCatalogueEntry[]>([]);
  let installations = $state<{ id: string; target: string }[]>([]);
  $effect(() => {
    if (!known || !enabled) return;
    const controller = new AbortController();
    void listKennelChecks(controller.signal)
      .then((result) => (checks = result.items ?? []))
      .catch(() => {
        // The menu stays short; the grid reports an outage itself.
      });
    void listInstallations(controller.signal)
      .then(
        (result) =>
          (installations = (result.items ?? []).map((i) => ({
            id: i.id ?? '',
            target: i.target ?? i.id ?? '',
          }))),
      )
      .catch(() => {});
    return () => controller.abort();
  });

  const STATES = ['attention', 'partial', 'pending', 'best_in_show'] as const;

  const severityOptions = [
    { value: '', label: 'Any severity' },
    { value: 'error', label: 'Errors' },
    { value: 'warning', label: 'Warnings' },
    { value: 'info', label: 'Notes' },
  ];
  const stateOptions = $derived([
    { value: '', label: 'Any standing' },
    ...STATES.map((state) => ({
      value: state,
      label: kennelStatus(state, undefined, prefs.quirkyStatus).label,
    })),
  ]);
  const codeOptions = $derived([
    { value: '', label: 'Any check' },
    ...checks.map((check) => ({ value: check.code, label: check.code })),
  ]);
  // One installation is the usual fleet, and a menu of one is noise.
  const installationOptions = $derived([
    { value: '', label: 'Any installation' },
    ...installations.map((i) => ({ value: i.id, label: i.target })),
  ]);

  const chips = $derived<FilterChip[]>(
    [
      search && {
        id: 'q',
        label: 'Name',
        value: search,
        onremove: () => router.setQuery({ q: null, offset: null }),
      },
      severity && {
        id: 'severity',
        label: 'Severity',
        value: severityOptions.find((o) => o.value === severity)?.label ?? severity,
        onremove: () => router.setQuery({ severity: null, offset: null }),
      },
      standing && {
        id: 'state',
        label: 'Standing',
        value: stateOptions.find((o) => o.value === standing)?.label ?? standing,
        onremove: () => router.setQuery({ state: null, offset: null }),
      },
      code && {
        id: 'code',
        label: 'Check',
        value: code,
        onremove: () => router.setQuery({ code: null, offset: null }),
      },
      installation && {
        id: 'installation',
        label: 'Installation',
        value: installations.find((i) => i.id === installation)?.target ?? installation,
        onremove: () => router.setQuery({ installation: null, offset: null }),
      },
    ].filter((chip): chip is FilterChip => Boolean(chip)),
  );

  function clearFilters(): void {
    router.setQuery({
      q: null,
      severity: null,
      state: null,
      code: null,
      installation: null,
      offset: null,
    });
  }

  /* -- rows -------------------------------------------------------------------- */

  let liveKey = $state(0);
  $effect(() => events.subscribe(['kennel.updated', 'kennel.deleted'], () => (liveKey += 1)));
  $effect(() => events.subscribe('resync', () => (liveKey += 1)));
  let previous = '';
  $effect(() => {
    const next = fleet.connection;
    if (next === 'live' && previous && previous !== 'live') untrack(() => (liveKey += 1));
    previous = next;
  });

  async function fetchRepositories(
    query: GridQuery,
    signal: AbortSignal,
  ): Promise<GridPage<KennelRepository>> {
    const page = await listKennelRepositories(
      {
        q: search || undefined,
        severity: (severity || undefined) as 'error' | 'warning' | 'info' | undefined,
        state: (standing || undefined) as
          'attention' | 'partial' | 'pending' | 'best_in_show' | undefined,
        code: code || undefined,
        installation: installation || undefined,
        limit: query.limit,
        offset: query.offset,
        sort: query.sort,
        order: query.order,
      },
      signal,
    );
    return { items: page.items ?? [], total: page.total };
  }

  function capitalise(word: string): string {
    return word ? word.charAt(0).toUpperCase() + word.slice(1) : word;
  }

  const columns = $derived<GridColumn<KennelRepository>[]>([
    {
      id: 'name',
      header: 'Repository',
      sortable: true,
      hideable: false,
      value: (row) => row.name,
      cell: nameCell,
    },
    {
      id: 'severity',
      header: 'Standing',
      sortable: true,
      width: '11rem',
      value: (row) => row.state,
      cell: standingCell,
    },
    {
      id: 'findings',
      header: 'Open',
      value: (row) => openFindingsText(row.counts),
      cell: findingsCell,
    },
    {
      id: 'visibility',
      header: 'Visibility',
      priority: 'wide',
      width: '8rem',
      value: (row) => capitalise(row.visibility) || '--',
    },
    {
      id: 'evaluated_at',
      header: 'Evaluated',
      sortable: true,
      width: '9rem',
      cell: evaluatedCell,
    },
  ]);

  function open(row: KennelRepository): void {
    router.navigate(`/kennel/repositories/${encodeURIComponent(row.id)}`);
  }
</script>

{#snippet nameCell(row: KennelRepository)}
  <a class="name" href="/kennel/repositories/{encodeURIComponent(row.id)}">{row.name}</a>
{/snippet}

{#snippet standingCell(row: KennelRepository)}
  <Badge
    status={kennelStatus(row.state, worstSeverity(row.counts), prefs.quirkyStatus)}
    size="sm"
  />
{/snippet}

{#snippet findingsCell(row: KennelRepository)}
  <span>{openFindingsText(row.counts)}</span>
  {#if row.counts.waived > 0}
    <span class="waived">, {row.counts.waived} waived</span>
  {/if}
{/snippet}

{#snippet evaluatedCell(row: KennelRepository)}
  {#if row.evaluated_at}
    <RelativeTime value={row.evaluated_at} />
  {:else}
    <span class="waived">Not yet</span>
  {/if}
{/snippet}

<PageHeader
  title="Repositories"
  subtitle="Every repository Kennel Club has looked at, and what is open on it."
  breadcrumb={[{ label: 'Kennel Club', href: '/kennel' }]}
  onrefresh={() => {
    reload += 1;
    liveKey += 1;
  }}
/>

<div class="content">
  {#if known && !enabled}
    <EmptyState
      icon={Trophy}
      title="Kennel Club is off"
      description="There is nothing to list until it is on."
    >
      {#if session.can('admin')}
        <Button variant="primary" href={KENNEL_SETTING_HREF}>Turn on in Settings</Button>
      {:else}
        <p class="waived">An administrator can turn it on under Settings, Configuration.</p>
      {/if}
    </EmptyState>
  {:else if !known}
    <Skeleton lines={6} />
  {:else}
    <FilterBar {chips} onclear={anyFilter ? clearFilters : undefined}>
      <div class="search">
        <Input
          bind:element={searchField}
          value={search}
          type="search"
          size="sm"
          icon={Search}
          placeholder="Search repository names"
          ariaLabel="Search repositories"
          oninput={(event) =>
            router.setQuery({
              q: (event.currentTarget as HTMLInputElement).value || null,
              offset: null,
            })}
        />
      </div>
      <Select
        value={standing}
        options={stateOptions}
        size="sm"
        ariaLabel="Filter by standing"
        onchange={(value) => router.setQuery({ state: value || null, offset: null })}
      />
      <Select
        value={severity}
        options={severityOptions}
        size="sm"
        ariaLabel="Filter by the worst severity open"
        onchange={(value) => router.setQuery({ severity: value || null, offset: null })}
      />
      <Select
        value={code}
        options={codeOptions}
        size="sm"
        ariaLabel="Filter by check"
        onchange={(value) => router.setQuery({ code: value || null, offset: null })}
      />
      {#if installations.length > 1}
        <Select
          value={installation}
          options={installationOptions}
          size="sm"
          ariaLabel="Filter by installation"
          onchange={(value) => router.setQuery({ installation: value || null, offset: null })}
        />
      {/if}
    </FilterBar>

    <DataGrid
      gridId="kennel-repositories"
      label="Repositories"
      {columns}
      fetcher={fetchRepositories}
      rowId={(row) => row.id}
      {filters}
      defaultSort="severity"
      defaultOrder="desc"
      noun="repositories"
      {liveKey}
      onopen={open}
      emptyTitle={anyFilter ? 'No repositories match those filters' : 'No repositories yet'}
      emptyDescription={anyFilter
        ? 'Try a wider search, or clear a filter.'
        : 'Kennel Club looks at repositories your fleet has run jobs for. None yet.'}
    >
      {#snippet emptyAction()}
        {#if anyFilter}
          <Button onclick={clearFilters}>Clear filters</Button>
        {/if}
      {/snippet}
    </DataGrid>
  {/if}
</div>

<style>
  .content {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
  }
  .name {
    font-family: var(--z-font-mono);
    font-size: var(--z-text-sm);
    overflow-wrap: anywhere;
  }
  .waived {
    color: var(--z-text-subtle);
    font-size: var(--z-text-xs);
  }
</style>
