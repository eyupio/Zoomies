<script module lang="ts">
  import type { Snippet } from 'svelte';
  import type { LucideIcon } from '@lucide/svelte';

  /** What the grid asks the server for. It lives in the URL, so a view is shareable. */
  export interface GridQuery {
    limit: number;
    offset: number;
    sort: string;
    order: 'asc' | 'desc';
  }

  /** What the fetcher gives back: this page of rows, and how many there are in total. */
  export interface GridPage<T> {
    items: T[];
    total: number;
  }

  export interface GridColumn<T> {
    /** Also the `sort` value sent to the API, and the key column visibility persists under. */
    id: string;
    header: string;
    /** Plain text for the cell. Used when no `cell` snippet is given. */
    value?: (row: T) => string;
    /** Rich cell content -- a badge, a link, a duration. */
    cell?: Snippet<[T]>;
    sortable?: boolean;
    /** Whether the operator may hide it. Defaults to true. */
    hideable?: boolean;
    width?: string;
    align?: 'start' | 'end';
  }

  export interface BulkAction {
    id: string;
    label: string;
    icon?: LucideIcon;
    danger?: boolean;
    run: (ids: string[]) => void | Promise<void>;
  }
</script>

<!--
  The grid.

  TanStack Table's core builds the column and row models and owns column
  visibility; every pixel of markup is ours, because a table is where this
  product's density and keyboard behaviour actually live.

  Everything is server side: the fetcher receives the query, returns the page
  and the total, and the query itself lives in the URL so a filtered view can be
  pasted to a colleague. Sorting, paging and filters therefore never touch the
  rows in the browser.

  Keyboard: up and down move between rows, Enter opens, Space selects,
  Shift with the arrows extends the selection, Home and End jump to the ends.
-->
<script lang="ts" generics="T extends Record<string, unknown>">
  import { untrack } from 'svelte';
  import { Columns3 } from '@lucide/svelte';
  import {
    columnVisibilityFeature,
    createTable,
    tableFeatures,
    type ColumnDef,
  } from '@tanstack/svelte-table';
  import { layers } from '../keys';
  import { prefs } from '../state/prefs.svelte';
  import { router } from '../router';
  import Button from './Button.svelte';
  import Checkbox from './Checkbox.svelte';
  import EmptyState from './EmptyState.svelte';
  import ErrorState from './ErrorState.svelte';
  import Pagination from './Pagination.svelte';
  import Skeleton from './Skeleton.svelte';

  interface Props {
    /** Stable id. Column visibility and page size persist under it. */
    gridId: string;
    /** Names the table for assistive technology: "Runners". */
    label: string;
    columns: ReadonlyArray<GridColumn<T>>;
    /** Server-side fetch. Called whenever the query, the filters or `liveKey` change. */
    fetcher: (query: GridQuery, signal: AbortSignal) => Promise<GridPage<T>>;
    rowId: (row: T) => string;
    /** Anything serialisable. A change refetches from the first page. */
    filters?: unknown;
    defaultSort?: string;
    defaultOrder?: 'asc' | 'desc';
    selectable?: boolean;
    bulkActions?: ReadonlyArray<BulkAction>;
    /** What Enter and a click on a row do. */
    onopen?: (row: T) => void;
    /** Called with every page as it lands, for warming the fleet cache. */
    onrows?: (rows: T[], total: number) => void;
    /** Bump to refetch -- pages pass the fleet version so SSE keeps the grid live. */
    liveKey?: number;
    noun?: string;
    emptyTitle?: string;
    emptyDescription?: string;
    /** The action that fills an empty grid. */
    emptyAction?: Snippet;
    class?: string;
  }

  let {
    gridId,
    label,
    columns,
    fetcher,
    rowId,
    filters,
    defaultSort = '',
    defaultOrder = 'desc',
    selectable = false,
    bulkActions = [],
    onopen,
    onrows,
    liveKey = 0,
    noun = 'rows',
    emptyTitle = `No ${noun} yet`,
    emptyDescription,
    emptyAction,
    class: className = '',
  }: Props = $props();

  /* -- query state, held in the URL --------------------------------------- */

  const limit = $derived(router.paramNumber('limit', prefs.pageSize(gridId)));
  const offset = $derived(router.paramNumber('offset', 0));
  const sort = $derived(router.param('sort', defaultSort));
  const order = $derived<'asc' | 'desc'>(
    router.param('order', defaultOrder) === 'asc' ? 'asc' : 'desc',
  );

  const request = $derived({
    query: { limit, offset, sort, order } satisfies GridQuery,
    filterKey: JSON.stringify(filters ?? null),
    liveKey,
  });

  let rows = $state<T[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let lastFilterKey = '';

  /** A short debounce so a burst of live events costs one request, not forty. */
  const DEBOUNCE_MS = 120;

  $effect(() => {
    const { query, filterKey } = request;
    // A filter change means the operator is looking at a different set; page one.
    if (lastFilterKey && lastFilterKey !== filterKey && query.offset !== 0) {
      lastFilterKey = filterKey;
      router.setQuery({ offset: null });
      return;
    }
    lastFilterKey = filterKey;

    const controller = new AbortController();
    let cancelled = false;
    loading = true;
    const timer = setTimeout(() => {
      void (async () => {
        try {
          const page = await fetcher(query, controller.signal);
          if (cancelled) return;
          rows = page.items;
          total = page.total;
          error = null;
          onrows?.(page.items, page.total);
        } catch (cause) {
          if (cancelled) return;
          if (cause instanceof DOMException && cause.name === 'AbortError') return;
          error = cause;
        } finally {
          if (!cancelled) loading = false;
        }
      })();
    }, DEBOUNCE_MS);

    return () => {
      cancelled = true;
      clearTimeout(timer);
      controller.abort();
    };
  });

  /* -- the table model ------------------------------------------------------ */

  const features = tableFeatures({ columnVisibilityFeature });

  // Read once on mount: the operator's stored choice is the starting point, and
  // it is theirs to change from there.
  let visibility = $state<Record<string, boolean>>(
    untrack(() =>
      Object.fromEntries(columns.map((c) => [c.id, prefs.isColumnVisible(gridId, c.id)])),
    ),
  );

  const definitions = $derived(
    columns.map(
      (column) =>
        ({
          id: column.id,
          header: column.header,
          accessorFn: (row: T) => (column.value ? column.value(row) : ''),
        }) as ColumnDef<typeof features, T, unknown>,
    ),
  );

  const table = createTable<typeof features, T>({
    features,
    get data() {
      return rows;
    },
    get columns() {
      return definitions;
    },
    getRowId: (row: T) => rowId(row),
    state: {
      get columnVisibility() {
        return visibility;
      },
    },
  });

  const byId = $derived(new Map(columns.map((c) => [c.id, c])));
  const visibleColumns = $derived(
    table
      .getVisibleLeafColumns()
      .map((c) => byId.get(c.id))
      .filter((c): c is GridColumn<T> => Boolean(c)),
  );
  const modelRows = $derived(table.getRowModel().rows);

  function toggleColumn(id: string, visible: boolean): void {
    visibility = { ...visibility, [id]: visible };
    prefs.setColumnVisible(gridId, id, visible);
  }

  /* -- sorting and paging --------------------------------------------------- */

  function toggleSort(column: GridColumn<T>): void {
    if (!column.sortable) return;
    const next = sort === column.id && order === 'desc' ? 'asc' : 'desc';
    router.setQuery({ sort: column.id, order: next, offset: null });
  }

  function goTo(nextOffset: number): void {
    router.setQuery({ offset: nextOffset || null });
  }

  function setLimit(nextLimit: number): void {
    prefs.setPageSize(gridId, nextLimit);
    router.setQuery({ limit: nextLimit, offset: null });
  }

  /* -- selection ------------------------------------------------------------- */

  let selected = $state<string[]>([]);
  let anchor = $state(-1);
  let focused = $state(-1);
  let body = $state<HTMLTableSectionElement | null>(null);

  const pageIds = $derived(modelRows.map((r) => r.id));
  const allSelected = $derived(pageIds.length > 0 && pageIds.every((id) => selected.includes(id)));
  const someSelected = $derived(selected.length > 0 && !allSelected);

  function isSelected(id: string): boolean {
    return selected.includes(id);
  }

  function setSelected(id: string, on: boolean): void {
    selected = on ? [...new Set([...selected, id])] : selected.filter((s) => s !== id);
  }

  function toggleAll(): void {
    selected = allSelected
      ? selected.filter((id) => !pageIds.includes(id))
      : [...new Set([...selected, ...pageIds])];
  }

  function selectRange(from: number, to: number): void {
    const [lo, hi] = from < to ? [from, to] : [to, from];
    const ids = pageIds.slice(lo, hi + 1);
    selected = [...new Set([...selected, ...ids])];
  }

  function focusRow(index: number): void {
    if (index < 0 || index >= modelRows.length) return;
    focused = index;
    queueMicrotask(() => body?.querySelector<HTMLElement>(`[data-row="${index}"]`)?.focus());
  }

  function openRow(index: number): void {
    const row = modelRows[index];
    if (row && onopen) onopen(row.original);
  }

  function onBodyKeydown(event: KeyboardEvent): void {
    const index = focused;
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        if (event.shiftKey && selectable && index >= 0)
          selectRange(anchor >= 0 ? anchor : index, Math.min(index + 1, modelRows.length - 1));
        focusRow(index + 1);
        break;
      case 'ArrowUp':
        event.preventDefault();
        if (event.shiftKey && selectable && index >= 0)
          selectRange(anchor >= 0 ? anchor : index, Math.max(index - 1, 0));
        focusRow(index - 1);
        break;
      case 'Home':
        event.preventDefault();
        focusRow(0);
        break;
      case 'End':
        event.preventDefault();
        focusRow(modelRows.length - 1);
        break;
      case 'Enter':
        event.preventDefault();
        openRow(index);
        break;
      case ' ':
        if (!selectable) return;
        event.preventDefault();
        {
          const id = pageIds[index];
          if (id) {
            setSelected(id, !isSelected(id));
            anchor = index;
          }
        }
        break;
      default:
        break;
    }
  }

  /* -- the column chooser ----------------------------------------------------- */

  let chooserOpen = $state(false);
  let chooser = $state<HTMLDivElement | null>(null);

  $effect(() => {
    if (!chooserOpen) return;
    const layer = layers.push('dropdown', () => (chooserOpen = false));
    const onDocument = (event: MouseEvent) => {
      if (!chooser?.contains(event.target as Node)) chooserOpen = false;
    };
    document.addEventListener('mousedown', onDocument);
    return () => {
      layers.remove(layer);
      document.removeEventListener('mousedown', onDocument);
    };
  });

  const hideable = $derived(columns.filter((c) => c.hideable !== false));
  const isEmpty = $derived(!loading && !error && modelRows.length === 0);
</script>

<div class="grid {className}">
  <div class="toolbar">
    {#if selectable && selected.length > 0}
      <div class="bulk" role="group" aria-label="Actions for the selected {noun}">
        <span class="bulk-count tabular">{selected.length} selected</span>
        {#each bulkActions as action (action.id)}
          <Button
            size="sm"
            variant={action.danger ? 'danger' : 'secondary'}
            icon={action.icon}
            onclick={() => void action.run(selected)}
          >
            {action.label}
          </Button>
        {/each}
        <Button size="sm" variant="ghost" onclick={() => (selected = [])}>Clear selection</Button>
      </div>
    {/if}
    <div class="chooser-wrap" bind:this={chooser}>
      <Button
        size="sm"
        variant="ghost"
        icon={Columns3}
        ariaExpanded={chooserOpen}
        ariaHaspopup="menu"
        ariaControls="{gridId}-columns"
        onclick={() => (chooserOpen = !chooserOpen)}
      >
        Columns
      </Button>
      {#if chooserOpen}
        <div class="chooser" id="{gridId}-columns" role="group" aria-label="Columns to show">
          {#each hideable as column (column.id)}
            <Checkbox
              label={column.header}
              checked={visibility[column.id] !== false}
              onchange={(on) => toggleColumn(column.id, on)}
            />
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <div class="scroll">
    <table role="grid" aria-label={label} aria-rowcount={total} onkeydown={onBodyKeydown}>
      <thead>
        <tr>
          {#if selectable}
            <th class="pick" scope="col">
              <Checkbox
                checked={allSelected}
                indeterminate={someSelected}
                ariaLabel="Select every {noun} on this page"
                onchange={toggleAll}
              />
            </th>
          {/if}
          {#each visibleColumns as column (column.id)}
            <th
              scope="col"
              style:width={column.width}
              class:end={column.align === 'end'}
              aria-sort={sort === column.id
                ? order === 'asc'
                  ? 'ascending'
                  : 'descending'
                : undefined}
            >
              {#if column.sortable}
                <button type="button" class="sort" onclick={() => toggleSort(column)}>
                  <span>{column.header}</span>
                  <span class="arrow" aria-hidden="true">
                    {#if sort === column.id}{order === 'asc' ? '↑' : '↓'}{/if}
                  </span>
                </button>
              {:else}
                {column.header}
              {/if}
            </th>
          {/each}
        </tr>
      </thead>
      <tbody bind:this={body}>
        {#if loading && modelRows.length === 0}
          {#each Array.from({ length: 8 }, (_, i) => i) as line (line)}
            <tr class="skeleton-row">
              {#if selectable}<td class="pick"><Skeleton width="15px" height="15px" /></td>{/if}
              {#each visibleColumns as column (column.id)}
                <td><Skeleton width={column.align === 'end' ? '3rem' : '70%'} height="0.9rem" /></td
                >
              {/each}
            </tr>
          {/each}
        {:else}
          {#each modelRows as row, index (row.id)}
            <tr
              data-row={index}
              tabindex={index === focused || (focused === -1 && index === 0) ? 0 : -1}
              class:selected={isSelected(row.id)}
              class:clickable={Boolean(onopen)}
              aria-selected={selectable ? isSelected(row.id) : undefined}
              onclick={() => {
                focused = index;
                openRow(index);
              }}
              onfocus={() => (focused = index)}
            >
              {#if selectable}
                <!--
                  The row's own click opens the row, so the cell that holds the
                  selection checkbox has to stop the event reaching it. Without
                  this, ticking a row navigates away and selects nothing, which
                  makes bulk selection impossible with a mouse -- the keyboard
                  path is unaffected, which is exactly why it went unnoticed.
                  Every other cell that holds a control does the same.
                -->
                <td class="pick" onclick={(event) => event.stopPropagation()}>
                  <Checkbox
                    checked={isSelected(row.id)}
                    ariaLabel="Select this row"
                    onchange={(on) => {
                      setSelected(row.id, on);
                      anchor = index;
                      focused = index;
                    }}
                  />
                </td>
              {/if}
              {#each visibleColumns as column (column.id)}
                <td class:end={column.align === 'end'}>
                  {#if column.cell}
                    {@render column.cell(row.original)}
                  {:else}
                    {column.value ? column.value(row.original) : ''}
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </div>

  {#if error}
    <ErrorState {error} onretry={() => router.setQuery({ offset: offset || null })} />
  {:else if isEmpty}
    <EmptyState title={emptyTitle} description={emptyDescription}>
      {#if emptyAction}{@render emptyAction()}{/if}
    </EmptyState>
  {/if}

  <Pagination {total} {limit} {offset} {noun} onpage={goTo} onlimit={setLimit} />
  <p class="sr-only" aria-live="polite">
    {loading ? 'Loading' : `${total} ${noun}`}
  </p>
</div>

<style>
  .grid {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
    min-width: 0;
  }
  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding: var(--z-space-2) var(--z-space-3);
    border-bottom: 1px solid var(--z-border);
    min-height: var(--z-space-10);
  }
  .bulk {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  .bulk-count {
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    color: var(--z-text-muted);
    font-variant-numeric: tabular-nums;
  }
  .chooser-wrap {
    position: relative;
    margin-left: auto;
  }
  .chooser {
    position: absolute;
    right: 0;
    top: calc(100% + var(--z-space-1));
    z-index: var(--z-layer-dropdown);
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
    min-width: 190px;
    padding: var(--z-space-3);
    border: 1px solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-md);
  }
  .scroll {
    overflow: auto;
    max-height: 70vh;
    /*
      Also the containing block for anything absolutely positioned inside it.
      Without this, a visually-hidden `.sr-only` span in a cell 500px along a
      1200px-wide table is positioned against the page instead, escapes this
      frame's clipping, and makes the whole document that wide -- so on a phone
      the page scrolls sideways and the browser zooms out to fit, which is a
      strange amount of damage for a one-pixel span nobody can see.
    */
    position: relative;
  }
  table {
    width: 100%;
    border-collapse: separate;
    border-spacing: 0;
    font-size: var(--z-text-sm);
    font-variant-numeric: tabular-nums;
  }
  thead th {
    position: sticky;
    top: 0;
    z-index: var(--z-layer-sticky);
    padding: var(--z-space-2) var(--z-space-4);
    border-bottom: 1px solid var(--z-border);
    background: var(--z-surface-sunken);
    color: var(--z-text-muted);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    white-space: nowrap;
  }
  th.end,
  td.end {
    text-align: right;
  }
  th.pick,
  td.pick {
    width: var(--z-space-8);
    padding-left: var(--z-space-3);
    padding-right: 0;
  }
  .sort {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-1);
    border: 0;
    padding: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-transform: inherit;
    letter-spacing: inherit;
    cursor: pointer;
  }
  .sort:hover {
    color: var(--z-text);
  }
  .arrow {
    display: inline-block;
    min-width: 8px;
  }
  tbody td {
    padding: var(--z-space-3) var(--z-space-4);
    border-bottom: 1px solid var(--z-border);
    color: var(--z-text);
    vertical-align: middle;
  }
  tbody tr:last-child td {
    border-bottom: 0;
  }
  tbody tr.clickable {
    cursor: pointer;
  }
  tbody tr:hover td {
    background: var(--z-surface-hover);
  }
  tbody tr.selected td {
    background: var(--z-accent-subtle);
  }
  tbody tr:focus-visible {
    outline: 2px solid var(--z-accent);
    outline-offset: -2px;
  }
  .skeleton-row td {
    padding: var(--z-space-3) var(--z-space-4);
  }
</style>
