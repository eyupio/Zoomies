<!--
  The three ways a provider's records and its reality can disagree, and what an
  operator may do about each.

  Nothing on this page deletes anything by itself, and that is the design
  rather than an omission. A resource wearing this fleet's naming with no row
  behind it might be a machine a crashed controller created, or it might be
  somebody's hand-made VM that happens to be called the same thing -- and the
  cost of guessing wrong is a destroyed machine that was not ours. So the
  untracked list is a report: it names what was found and where to look, and
  the only button here forgets a row we hold, never a resource somebody runs.
-->
<script lang="ts">
  import type { Machine, Orphan, ProviderOrphans } from '$lib/api/types';
  import { pluralise } from '$lib/format';
  import { machineStatus } from '$lib/status';
  import Badge from '$lib/components/Badge.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import DataGrid from '$lib/components/DataGrid.svelte';
  import type { GridColumn, GridPage, GridQuery } from '$lib/components/DataGrid.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';

  interface Props {
    orphans: ProviderOrphans;
    canAdmin?: boolean;
    /** Forget one row. Resolve false to keep the confirmation open for a retry. */
    onrelease: (machine: Machine) => Promise<boolean>;
    class?: string;
  }

  let { orphans, canAdmin = false, onrelease, class: className = '' }: Props = $props();

  const untracked = $derived(orphans.untracked ?? []);
  const noResource = $derived(orphans.no_resource ?? []);
  const unverified = $derived(orphans.unverified ?? []);

  /**
   * The grids page over an answer already in hand rather than over the server.
   *
   * A provider's orphans arrive in one response -- they are counted in tens,
   * and the sweep that found them is what makes the list expensive, not the
   * paging -- so each grid's fetcher slices the array it was given. `filters`
   * carries the array itself, which is what makes a grid refetch when a fresh
   * sweep lands.
   */
  function localFetcher<T>(rows: readonly T[]): (q: GridQuery) => Promise<GridPage<T>> {
    return async (query) => ({
      items: rows.slice(query.offset, query.offset + query.limit),
      total: rows.length,
    });
  }

  const untrackedColumns: GridColumn<Orphan>[] = [
    { id: 'name', header: 'Resource', hideable: false, value: (row) => row.name ?? '' },
    { id: 'note', header: 'What to do about it', value: (row) => row.note ?? '' },
  ];

  const machineColumns = $derived<GridColumn<Machine>[]>([
    { id: 'name', header: 'Machine', hideable: false, value: (row) => row.name ?? '' },
    {
      id: 'state',
      header: 'State',
      width: '9rem',
      value: (row) => row.state ?? '',
      cell: stateCell,
    },
    {
      id: 'resource',
      header: 'Resource',
      width: '11rem',
      value: (row) => [row.resource_zone, row.resource_id].filter(Boolean).join(' / '),
    },
    {
      id: 'why',
      header: 'Why it is here',
      value: (row) => row.ownership_error || row.safe_to_delete_why || row.message || '',
    },
  ]);

  // One more column than the other two grids: the only destructive act on this
  // page belongs to the list where destroying nothing is guaranteed.
  const releaseColumns = $derived<GridColumn<Machine>[]>([
    ...machineColumns,
    // A column of buttons takes its width outright: under the grid's arithmetic
    // a declared width is otherwise a share of the frame, and the button would
    // be squeezed out of its own column on a narrow desktop.
    {
      id: 'release',
      header: '',
      fixed: true,
      overflows: true,
      width: '7rem',
      align: 'end',
      hideable: false,
      cell: releaseCell,
    },
  ]);

  let releasing = $state<Machine | null>(null);
  let releaseOpen = $state(false);
  let busy = $state(false);

  function release(machine: Machine): void {
    releasing = machine;
    releaseOpen = true;
  }

  async function confirmRelease(): Promise<boolean> {
    const machine = releasing;
    if (!machine) return true;
    busy = true;
    try {
      return await onrelease(machine);
    } finally {
      busy = false;
    }
  }
</script>

{#snippet stateCell(row: Machine)}
  <Badge status={machineStatus(row.state)} size="sm" />
{/snippet}

{#snippet releaseCell(row: Machine)}
  <!-- Permanent, so the button names its machine: a list of them is otherwise a
       column of identical "Forget" buttons. The visible word stays the start of
       the accessible name. -->
  <Button
    size="sm"
    variant="secondary"
    ariaLabel="Forget {row.name ?? 'this machine'}"
    disabled={!canAdmin}
    onclick={() => release(row)}
  >
    Forget
  </Button>
{/snippet}

<div class="review {className}">
  <section aria-labelledby="orphans-untracked">
    <header>
      <h3 id="orphans-untracked">Resources with no row</h3>
      <p>
        {pluralise(untracked.length, 'resource')} at the provider wearing this fleet's naming that no
        machine of ours accounts for. Zoomies never deletes one — look at it, and remove it there if it
        really is rubbish.
        {#if orphans.last_sweep_at}
          Last swept <RelativeTime value={orphans.last_sweep_at} plain />.
        {:else}
          Nothing has swept this provider yet.
        {/if}
      </p>
    </header>
    {#if untracked.length === 0}
      <EmptyState
        compact
        title="Nothing untracked"
        description="Every resource carrying our naming has a machine behind it."
      />
    {:else}
      <DataGrid
        gridId="provider-orphans-untracked"
        label="Untracked resources"
        columns={untrackedColumns}
        fetcher={localFetcher(untracked)}
        rowId={(row) => row.name ?? ''}
        filters={untracked}
        noun="resources"
      />
    {/if}
  </section>

  <section aria-labelledby="orphans-no-resource">
    <header>
      <h3 id="orphans-no-resource">Machines holding nothing</h3>
      <p>
        {pluralise(noResource.length, 'row')} that never got as far as a resource, or whose resource is
        confirmed gone. Forgetting one loses nothing, because there is nothing behind it.
      </p>
    </header>
    {#if noResource.length === 0}
      <EmptyState compact title="None" description="Every machine here holds something." />
    {:else}
      <DataGrid
        gridId="provider-orphans-empty"
        label="Machines with no resource"
        columns={releaseColumns}
        fetcher={localFetcher(noResource)}
        rowId={(row) => row.id ?? ''}
        filters={noResource}
        noun="machines"
      />
    {/if}
  </section>

  <section aria-labelledby="orphans-unverified">
    <header>
      <h3 id="orphans-unverified">Ownership unverified</h3>
      <p>
        {pluralise(unverified.length, 'machine')} nothing has confirmed is ours, including every quarantined
        one. The reconciler will not touch these: deleting a machine we cannot prove we created is the
        one mistake that cannot be undone.
      </p>
    </header>
    {#if unverified.length === 0}
      <EmptyState
        compact
        title="Nothing in doubt"
        description="Every machine here was confirmed ours by a recent observation."
      />
    {:else}
      <DataGrid
        gridId="provider-orphans-unverified"
        label="Machines whose ownership is unverified"
        columns={machineColumns}
        fetcher={localFetcher(unverified)}
        rowId={(row) => row.id ?? ''}
        filters={unverified}
        noun="machines"
      />
    {/if}
  </section>
</div>

<ConfirmDialog
  bind:open={releaseOpen}
  title="Forget this machine"
  name={releasing?.name}
  requireName
  tone="danger"
  confirmLabel="Forget it"
  {busy}
  description="Zoomies stops tracking this machine. Nothing at the provider is touched."
  consequences={[
    'The row is removed from this fleet, along with its accounting.',
    'Anything that does exist at the provider stays exactly where it is, and stays your bill.',
    'Nothing here will ever delete it afterwards, because nothing will remember it.',
  ]}
  onconfirm={confirmRelease}
  oncancel={() => (releasing = null)}
/>

<style>
  .review {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-6);
  }
  header {
    margin-bottom: var(--z-space-3);
  }
  h3 {
    margin: 0;
    font-size: var(--z-text-base);
    line-height: var(--z-leading-base);
    font-weight: var(--z-weight-semibold);
    color: var(--z-text);
  }
  header p {
    margin: var(--z-space-1) 0 0;
    max-width: 80ch;
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
</style>
