<!--
  Every repository Kennel Club has looked at, worst first.

  The filters are the API's own, in the URL under its own names, so a view is a
  link a colleague can open: ?severity=error, ?code=exposure.public_repo_weak_pool,
  ?state=attention. A filter that matches nothing says so and offers to clear
  itself, and the page says why when there is nothing to list because Kennel Club
  is off.

  By default only the repositories the fleet is serving are listed: Kennel Club
  keeps a row for one for a quarter after its last job, and under the installation
  scope for every one the App can see. Which view a person wants is theirs and is
  remembered in their browser. The address can override it with ?active=all, so a
  link that counts every repository -- a card on the Overview, the problem in the
  drawer -- opens on as many rows as it said.

  A repository Kennel Club has been told not to look at is left out the same way, and
  for the same reason: the Overview's numbers are of the ones it is looking at. It is
  a filter, though, and not a scope: it is the address's alone (?tracked=false for
  those, ?tracked=all for both), it has a chip, and clearing the filters brings the
  default back.
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
  import Switch from '$lib/components/Switch.svelte';
  import KennelShell from '$lib/kennel/KennelShell.svelte';
  import KennelTurnOn from '$lib/kennel/KennelTurnOn.svelte';
  import { openFindingsText, worstSeverity } from '$lib/kennel/words';
  import { kennelClub } from '$lib/state/kennel.svelte';
  import { registerSearch } from '$lib/keys';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { prefs, remember, remembered } from '$lib/state/prefs.svelte';
  import { session } from '$lib/state/session.svelte';
  import { kennelStatus, kennelTrackingStatus } from '$lib/status';

  /* -- whether there is anything to list ------------------------------------- */

  let known = $state(false);
  let enabled = $state(true);
  let reload = $state(0);

  $effect(() => {
    void reload;
    // Turned on or off from the rail, here or in another tab: ask again.
    void kennelClub.epoch;
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
  // Which view of the tracking: the ones Kennel Club is looking at unless the
  // address says otherwise.
  const trackedParam = $derived(router.param('tracked'));
  const tracking = $derived<'tracked' | 'untracked' | 'all'>(
    trackedParam === 'false' ? 'untracked' : trackedParam === 'all' ? 'all' : 'tracked',
  );

  // Only the repositories being served, unless this person has said otherwise or the
  // address does. It is a scope on the view and not a filter: it has no chip and
  // "Clear filters" leaves it alone.
  const ACTIVE_KEY = 'zoomies.kennel.activeOnly';
  let activeOnlyChoice = $state(
    remembered(ACTIVE_KEY, true, (value): value is boolean => typeof value === 'boolean'),
  );
  const activeOnly = $derived(router.param('active') !== 'all' && activeOnlyChoice);
  function chooseActiveOnly(next: boolean): void {
    activeOnlyChoice = next;
    remember(ACTIVE_KEY, next);
    // The choice is made, so the address no longer overrides it.
    router.setQuery({ active: null, offset: null });
  }

  const filters = $derived({
    search,
    severity,
    standing,
    code,
    installation,
    activeOnly,
    tracking,
  });
  const anyFilter = $derived(
    Boolean(search || severity || standing || code || installation || tracking !== 'tracked'),
  );

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
  const trackingOptions = [
    { value: '', label: 'Tracked' },
    { value: 'false', label: 'Not tracked' },
    { value: 'all', label: 'Tracked and not tracked' },
  ];
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
      tracking !== 'tracked' && {
        id: 'tracked',
        label: 'Tracking',
        value: trackingOptions.find((o) => o.value === trackedParam)?.label ?? trackedParam,
        onremove: () => router.setQuery({ tracked: null, offset: null }),
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
      tracked: null,
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

  // What is asked of the list, apart from the page of it and whether to keep only
  // the ones being served: the hidden count asks the same question of the rest.
  const asked = $derived({
    q: search || undefined,
    severity: (severity || undefined) as 'error' | 'warning' | 'info' | undefined,
    state: (standing || undefined) as
      'attention' | 'partial' | 'pending' | 'best_in_show' | undefined,
    code: code || undefined,
    installation: installation || undefined,
    // The API lists both when it is not asked, so "both" is the absence of the filter.
    tracked: tracking === 'all' ? undefined : tracking === 'tracked',
  });

  async function fetchRepositories(
    query: GridQuery,
    signal: AbortSignal,
  ): Promise<GridPage<KennelRepository>> {
    const page = await listKennelRepositories(
      {
        ...asked,
        active: activeOnly ? true : undefined,
        limit: query.limit,
        offset: query.offset,
        sort: query.sort,
        order: query.order,
      },
      signal,
    );
    return { items: page.items ?? [], total: page.total };
  }

  // How many more would be listed without the scope, with the same filters, so the
  // page can say what it is leaving out instead of quietly leaving it out.
  let hidden = $state<number | null>(null);
  $effect(() => {
    void liveKey;
    void reload;
    if (!known || !enabled || !activeOnly) {
      hidden = null;
      return;
    }
    const controller = new AbortController();
    void listKennelRepositories({ ...asked, active: false, limit: 1 }, controller.signal)
      .then((page) => {
        if (!controller.signal.aborted) hidden = page.total;
      })
      .catch(() => {
        // The grid reports an outage itself; without the count the page says less.
        if (!controller.signal.aborted) hidden = null;
      });
    return () => controller.abort();
  });

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
    status={row.tracking.tracked
      ? kennelStatus(row.state, worstSeverity(row.counts), prefs.quirkyStatus)
      : kennelTrackingStatus()}
    size="sm"
  />
{/snippet}

{#snippet findingsCell(row: KennelRepository)}
  {#if row.tracking.tracked}
    <span>{openFindingsText(row.counts)}</span>
    {#if row.counts.waived > 0}
      <span class="waived">, {row.counts.waived} waived</span>
    {/if}
  {:else}
    <span class="waived">Not evaluated</span>
  {/if}
{/snippet}

{#snippet evaluatedCell(row: KennelRepository)}
  {#if !row.tracking.tracked && row.tracking.since}
    <span class="waived">Stopped <RelativeTime value={row.tracking.since} plain /></span>
  {:else if row.evaluated_at}
    <RelativeTime value={row.evaluated_at} />
  {:else}
    <span class="waived">Not yet</span>
  {/if}
{/snippet}

<KennelShell current="repositories">
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
          <KennelTurnOn />
        {:else}
          <p class="waived">An administrator can turn it on, with the switch beside this page.</p>
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
        <Select
          value={trackedParam === 'false' || trackedParam === 'all' ? trackedParam : ''}
          options={trackingOptions}
          size="sm"
          ariaLabel="Filter by tracking"
          onchange={(value) => router.setQuery({ tracked: value || null, offset: null })}
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
        <Switch checked={activeOnly} label="Active on Zoomies" onchange={chooseActiveOnly} />
      </FilterBar>

      <p class="scope">
        {#if activeOnly}
          Showing repositories with a job in the last 30 days (fewer if this fleet keeps its jobs
          for less).
          {#if hidden}
            {hidden} more {hidden === 1 ? 'repository has' : 'repositories have'} no recent job.
            <button
              class="link"
              type="button"
              onclick={() => router.setQuery({ active: 'all', offset: null })}>Show them</button
            >
          {/if}
        {:else}
          Showing every repository Kennel Club has a row for, including those with no job in the
          last 30 days.
        {/if}
      </p>

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
        emptyTitle={anyFilter
          ? activeOnly
            ? 'No active repositories match those filters'
            : 'No repositories match those filters'
          : activeOnly && hidden
            ? 'No repository has had a job lately'
            : 'No repositories yet'}
        emptyDescription={hidden
          ? `${hidden} without a job in the last 30 days ${hidden === 1 ? 'is' : 'are'} not shown.`
          : anyFilter
            ? 'Try a wider search, or clear a filter.'
            : 'Kennel Club looks at repositories your fleet has run jobs for. None yet.'}
      >
        {#snippet emptyAction()}
          {#if hidden}
            <Button onclick={() => router.setQuery({ active: 'all', offset: null })}
              >Show them</Button
            >
          {/if}
          {#if anyFilter}
            <Button onclick={clearFilters}>Clear filters</Button>
          {/if}
        {/snippet}
      </DataGrid>
    {/if}
  </div>
</KennelShell>

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
  .scope {
    margin: 0;
    color: var(--z-text-muted);
    font-size: var(--z-text-sm);
  }
  .link {
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    text-decoration: underline;
    text-underline-offset: var(--z-underline-offset);
    cursor: pointer;
  }
</style>
