<!--
  What the fleet did, and roughly what it cost, over a bounded range of days or hours.

  This is the one page whose numbers somebody puts in front of a finance team,
  so two things matter more than they do elsewhere. The first is that every
  figure says what it counts: "jobs" alone is ambiguous between queued, started
  and finished, and the three differ at the edges of any interval, so all three
  are columns. The second is that an estimate never reads as a fact -- cost
  comes from rates an administrator typed, Zoomies knows no cloud prices, and
  the page says so above the table rather than in a footnote.

  The range and the grouping live in the address bar, so a report can be sent to
  somebody else as a link rather than as a description of which boxes to tick.
-->
<script lang="ts">
  import InstallationReport from '$lib/usage/InstallationReport.svelte';
  import ReleaseTable from '$lib/usage/ReleaseTable.svelte';
  import UsageInsights from '$lib/usage/UsageInsights.svelte';
  import { Download, Receipt } from '@lucide/svelte';
  import { getUsage, usageCsvUrl } from '$lib/api/client';
  import type { Usage, UsageGrouping } from '$lib/api/types';
  import { router } from '$lib/router';
  import { fleet } from '$lib/state/fleet.svelte';
  import { formatAbsolute, formatNumber, formatPercent } from '$lib/format';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import LoadingBoundary from '$lib/components/LoadingBoundary.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import DateRange, { endOfMoment, localMoment, startOfMoment } from '$lib/jobs/DateRange.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import { tableLayout } from '$lib/actions/tableLayout';

  /** The groupings the API offers, in the order an operator narrows through them. */
  const GROUPINGS: { value: UsageGrouping; label: string }[] = [
    { value: 'pool', label: 'Pool' },
    { value: 'host', label: 'Host' },
    { value: 'installation', label: 'Installation' },
    { value: 'repository', label: 'Repository' },
    { value: 'workflow', label: 'Workflow' },
  ];

  /** What the column of keys is called, once a grouping is chosen. */
  const KEY_HEADER: Record<UsageGrouping, string> = {
    pool: 'Pool',
    host: 'Host',
    installation: 'Installation',
    repository: 'Repository',
    workflow: 'Workflow',
  };

  /**
   * The quick ranges. The hours are the last so many hours up to now, as the
   * Hosts page's windows are, for "what just happened?"; the days are whole
   * calendar days ending today, since a report somebody takes to a finance
   * meeting is read in days.
   */
  const PRESETS = [
    { value: '1h', label: '1h', name: 'The last hour', hours: 1 },
    { value: '6h', label: '6h', name: 'The last 6 hours', hours: 6 },
    { value: '12h', label: '12h', name: 'The last 12 hours', hours: 12 },
    { value: '1d', label: '1d', name: 'Today', days: 1 },
    { value: '7d', label: '7d', name: 'The last 7 days, today included', days: 7 },
    { value: '30d', label: '30d', name: 'The last 30 days, today included', days: 30 },
    { value: '90d', label: '90d', name: 'The last 90 days, today included', days: 90 },
  ] as const;
  type Preset = (typeof PRESETS)[number];

  // A day, like every other range the UI opens on. A wider report is one
  // preset away, and it lands in the address bar where it can be sent on.
  const DEFAULT_PRESET = '1d';

  /**
   * When "now" is for a quick range. Held rather than read on every render so
   * the query is stable until somebody asks for it again -- by choosing a
   * range or refreshing -- and a report does not refetch itself each second.
   */
  let now = $state(Date.now());

  /** A range typed by hand wins; otherwise the quick range named, or a day. */
  const custom = $derived(Boolean(router.param('since') || router.param('until')));
  const preset = $derived<Preset | null>(
    custom
      ? null
      : (PRESETS.find((p) => p.value === router.param('range')) ??
          PRESETS.find((p) => p.value === DEFAULT_PRESET)!),
  );

  /** A preset's bounds, as instants. */
  function bounds(p: Preset, at: number): { from: Date; to: Date } {
    if ('hours' in p) return { from: new Date(at - p.hours * 3_600_000), to: new Date(at) };
    const d = new Date(at);
    const from = new Date(d.getFullYear(), d.getMonth(), d.getDate() - (p.days - 1));
    const to = new Date(d.getFullYear(), d.getMonth(), d.getDate() + 1, 0, 0, 0, -1);
    return { from, to };
  }

  const today = $derived(localMoment(new Date(now)).slice(0, 10));
  /**
   * What the From and To fields show: the preset's bounds when one is in
   * force, so choosing "6h" and then nudging its start is one edit rather
   * than typing both ends. A bare date from an older link reads as its day.
   */
  const since = $derived.by(() => {
    if (preset) return localMoment(bounds(preset, now).from);
    const v = router.param('since') || today;
    return v.includes('T') ? v : `${v}T00:00`;
  });
  const until = $derived.by(() => {
    if (preset) return localMoment(bounds(preset, now).to);
    const v = router.param('until') || today;
    return v.includes('T') ? v : `${v}T23:59`;
  });
  const grouping = $derived<UsageGrouping>(
    (GROUPINGS.find((g) => g.value === router.param('group_by'))?.value ?? 'pool') as UsageGrouping,
  );

  /**
   * The query both the table and the CSV link are built from, so the file an
   * operator downloads is the report they are looking at.
   */
  const query = $derived.by(() => {
    if (preset) {
      const { from, to } = bounds(preset, now);
      return { from: from.toISOString(), to: to.toISOString(), group_by: grouping };
    }
    // The address's own values, so a bare date keeps its whole day.
    const s = router.param('since') || today;
    const u = router.param('until') || today;
    return { from: startOfMoment(s) ?? '', to: endOfMoment(u) ?? '', group_by: grouping };
  });

  let report = $state.raw<Usage | null>(null);
  /** When the report on screen landed; the matrix throws its hours away on a new one. */
  let fetchedAt = $state(0);
  let loading = $state(true);
  let error = $state<unknown>(null);
  let pending: AbortController | null = null;

  async function load(q: typeof query): Promise<void> {
    // Refresh, retry and filter changes all replace the same request. A slow
    // response must never be rendered under a newer range or grouping.
    pending?.abort();
    const controller = new AbortController();
    pending = controller;
    loading = true;
    error = null;
    try {
      const next = await getUsage(q, controller.signal);
      if (controller.signal.aborted || pending !== controller) return;
      report = next;
      fetchedAt = Date.now();
    } catch (cause) {
      if (controller.signal.aborted || pending !== controller) return;
      error = cause;
    } finally {
      if (!controller.signal.aborted && pending === controller) loading = false;
    }
  }

  $effect(() => {
    const q = query;
    void load(q);
    return () => pending?.abort();
  });

  const entity = $derived(router.param('entity'));
  const rows = $derived((report?.items ?? []).filter((r) => !entity || r.key === entity));
  const entities = $derived([
    { value: '', label: 'All groups' },
    ...(report?.items ?? [])
      .filter((r) => r.key)
      .map((r) => ({ value: r.key, label: label(r.key) })),
  ]);
  function focus(key: string): void {
    router.setQuery({ entity: key || null });
  }
  function choose(value: string): void {
    now = Date.now();
    router.setQuery({
      range: value === DEFAULT_PRESET ? null : value,
      since: null,
      until: null,
    });
  }

  /**
   * Whether runner time can be attributed at this grouping at all. A runner
   * idles on behalf of a pool and never on behalf of a repository, so those
   * columns are absent rather than zero -- a zero would be a claim.
   */
  const attributable = $derived(report?.allocation_attributable !== false);
  const usageColumns = $derived([
    'key',
    'queued',
    'started',
    'completed',
    'peak',
    'queue-wait',
    'executing',
    ...(attributable ? ['runner-hours', 'busy-share', 'cost'] : []),
  ]);
  const backwards = $derived(Boolean(query.from && query.to && query.from > query.to));

  /**
   * The instant the report's runner history begins, when the range asked for
   * reaches further back than that. Runner-hours come from the usage ledger,
   * which begins with the oldest runner the database still knew when it was
   * upgraded into one -- and a runner-hours figure that is silently short
   * before then is the one number on this page somebody takes to a finance
   * meeting.
   */
  const runnersFrom = $derived.by(() => {
    const from = report?.history_from?.runners;
    if (!from || !attributable) return null;
    return new Date(from) > new Date(report?.from ?? from) ? from : null;
  });
  const jobsFrom = $derived.by(() => {
    const from = report?.history_from?.jobs;
    if (!from) return null;
    return new Date(from) > new Date(report?.from ?? from) ? from : null;
  });

  function setRange(next: { since: string; until: string }): void {
    router.setQuery({ since: next.since || null, until: next.until || null, range: null });
  }

  /** Hours, to two places: the unit an invoice is written in. */
  function hours(seconds: number | null): string {
    if (seconds === null) return 'Not attributable';
    return (seconds / 3600).toFixed(2);
  }

  /** A mean wait in the units a person reads it in. */
  function wait(seconds: number | null): string {
    if (seconds === null) return 'Nothing started';
    if (seconds < 90) return `${seconds.toFixed(1)}s`;
    return `${(seconds / 60).toFixed(1)} min`;
  }

  /** An estimate, marked as one by the note above the table, not by a symbol. */
  function cost(value: number | undefined): string {
    if (value === undefined) return 'No rate set';
    return value.toFixed(2);
  }

  /**
   * How much of a runner's paid-for life was spent executing a job. The number
   * that answers "are we paying for idle runners?", which is the question this
   * page is usually opened to settle.
   */
  function utilisation(executed: number, allocated: number | null): string {
    if (allocated === null || allocated <= 0) return '--';
    return formatPercent(executed / allocated);
  }

  /**
   * The row's key in the words the rest of the product uses.
   *
   * The API groups by identifier, which is right for a stable report and wrong
   * for a person: a table of `pool_demoarm` is not something anybody takes to a
   * finance meeting. Repositories and workflows are already names.
   *
   * Three cases have to look different, and used to look the same. A live pool
   * is its name. A pool the fleet has no record of -- deleted since the range
   * began -- keeps its id, because its hours were still spent, but it is said
   * to be gone rather than shown as a bare identifier that reads like a name.
   * And a row with no pool at all is not a pool: it is the work this fleet had
   * no hand in, which on a one-pool fleet otherwise looks like a second pool.
   */
  function label(key: string): string {
    if (!key) return 'Unattributed';
    if (grouping === 'host') return fleet.host(key)?.name ?? key;
    if (grouping !== 'pool') return key;
    return fleet.pool(key)?.name ?? key;
  }

  /**
   * What this row is, when the grouping is by pool.
   *
   * `unknown` while the fleet cache is still loading: every pool would
   * otherwise be reported as deleted for the moment it takes to arrive, which
   * is worse than saying nothing.
   */
  function kind(key: string): 'live' | 'gone' | 'unattributed' | 'unknown' {
    if (grouping !== 'pool') return 'live';
    if (!key) return 'unattributed';
    if (fleet.pool(key)) return 'live';
    return fleet.loaded ? 'gone' : 'unknown';
  }
</script>

<PageHeader
  title="Usage"
  subtitle="This fleet's runner capacity and job activity over a range of hours or days. Jobs GitHub ran on its own hosted runners are not counted: they used no runner here."
  onrefresh={() => {
    // A quick range is "up to now", so a refresh moves it along; the
    // effect fetches it. A range typed by hand is fetched as it stands.
    if (preset) now = Date.now();
    else void load(query);
  }}
>
  <Button
    variant="secondary"
    size="sm"
    icon={Download}
    href={usageCsvUrl({ ...query, key: entity || undefined })}
    disabled={backwards}
  >
    Export CSV
  </Button>
</PageHeader>

<div class="controls">
  <DateRange {since} {until} withTime label="Usage between" onchange={setRange} />
  <label class="grouping">
    <span>Group by</span>
    <Select
      value={grouping}
      size="sm"
      ariaLabel="Group by"
      options={GROUPINGS}
      onchange={(value) =>
        router.setQuery({ group_by: value === 'pool' ? null : value, entity: null })}
    />
  </label>
  <Select
    value={entity || ''}
    size="sm"
    ariaLabel="Focus group"
    options={entities}
    onchange={focus}
  />
  <!-- The range in force is the one pressed, the default included; a range
       typed by hand is none of them, so none is. -->
  <Segmented options={PRESETS} value={preset?.value ?? ''} label="Quick ranges" onchange={choose} />
</div>

<p class="note">
  Counts are additive, so adjacent reports sum: a job is counted in the interval it was queued,
  started or completed in. Costs are estimates from rates an administrator assigned to a pool.
  Zoomies does not know what your machines cost.
</p>

{#if !attributable}
  <p class="note attribution">
    A runner idles on behalf of a pool, never on behalf of a {grouping}, so runner-hours and cost
    cannot be attributed at this grouping. Group by pool, host or installation to see them.
  </p>
{/if}

{#if runnersFrom || jobsFrom}
  <p class="note attribution" data-testid="usage-history-from">
    {#if runnersFrom}
      Runner-hours, utilisation and cost are complete from {formatAbsolute(runnersFrom)}: runner
      history before then has been pruned.
    {/if}
    {#if jobsFrom}
      Job counts and waits are complete from {formatAbsolute(jobsFrom)}: job history before then has
      been pruned.
    {/if}
    Raise the retention settings to keep more.
  </p>
{/if}

{#if grouping === 'installation' && entity}
  <div class="installation-report"><InstallationReport installation={entity} /></div>
{:else if grouping === 'installation'}
  <p class="note" data-testid="installation-report-hint">
    Focus one installation to see its report: how many of its jobs the fleet ran, how long they
    waited for a runner, how many the fleet broke, and whether its runners were cleaned up.
  </p>
{/if}

<LoadingBoundary
  {loading}
  {error}
  empty={rows.length === 0}
  onretry={() => void load(query)}
  class="report"
>
  {#snippet skeleton()}
    <div class="frame">
      <Skeleton lines={6} />
    </div>
  {/snippet}

  {#snippet emptyState()}
    <EmptyState
      icon={Receipt}
      title="Nothing ran in this range"
      description="No job was queued, started or finished between these dates. Widen the range, or change what the report is grouped by."
    />
  {/snippet}

  <UsageInsights
    {rows}
    {grouping}
    range={{ from: query.from, to: query.to }}
    {entity}
    {fetchedAt}
    {label}
    onselect={focus}
  />
  <div class="detail-heading">
    <h2>Detailed usage</h2>
    <span>{rows.length} groups · execution and allocation in hours</span>
  </div>
  <div class="frame">
    <!--
    Every role is spelled out rather than left to the table's own display
    type. The rows become cards on a phone, which means `display` stops
    being `table-row`, and a browser drops the implicit row and cell roles
    the moment it does -- leaving a screen reader a run of loose text with
    nothing saying which value belongs to which record.
  -->
    <!-- svelte-ignore a11y_no_redundant_roles -->
    <table role="table" use:tableLayout={{ id: 'usage-report', columns: usageColumns }}>
      <caption class="sr-only">
        Usage by {grouping} between {since} and {until}
      </caption>
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <thead role="rowgroup">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tr role="row">
          <th role="columnheader" scope="col" class="key-header">{KEY_HEADER[grouping]}</th>
          <th role="columnheader" scope="col" class="end">Queued</th>
          <th role="columnheader" scope="col" class="end">Started</th>
          <th role="columnheader" scope="col" class="end">Completed</th>
          <th role="columnheader" scope="col" class="end">Peak at once</th>
          <th role="columnheader" scope="col" class="end">Average queue wait</th>
          <th role="columnheader" scope="col" class="end">Executing</th>
          {#if attributable}
            <th role="columnheader" scope="col" class="end">Runner-hours</th>
            <th role="columnheader" scope="col" class="end">Busy share</th>
            <th role="columnheader" scope="col" class="end">Estimated cost</th>
          {/if}
        </tr>
      </thead>
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <tbody role="rowgroup">
        {#each rows as row (row.key)}
          {@const rowKind = kind(row.key)}
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            <th role="rowheader" scope="row" class="key" title={row.key || undefined}>
              <span class="name" class:muted={rowKind !== 'live'}>{label(row.key)}</span>
              {#if rowKind === 'gone'}
                <span class="tag">deleted</span>
              {:else if rowKind === 'unattributed'}
                <span class="tag">no pool</span>
              {/if}
            </th>
            <td role="cell" class="end tabular" data-label="Queued">{formatNumber(row.jobs)}</td>
            <td role="cell" class="end tabular" data-label="Started"
              >{formatNumber(row.jobs_started)}</td
            >
            <td role="cell" class="end tabular" data-label="Completed"
              >{formatNumber(row.jobs_completed)}</td
            >
            <td role="cell" class="end tabular" data-label="Peak at once"
              >{formatNumber(row.peak_concurrency)}</td
            >
            <td role="cell" class="end tabular" data-label="Average queue wait"
              >{wait(row.average_queue_wait_seconds)}</td
            >
            <td role="cell" class="end tabular" data-label="Executing"
              >{hours(row.job_execution_seconds)}</td
            >
            {#if attributable}
              <td role="cell" class="end tabular" data-label="Runner-hours"
                >{hours(row.allocated_runner_seconds)}</td
              >
              <td role="cell" class="end tabular" data-label="Busy share"
                >{utilisation(row.job_execution_seconds, row.allocated_runner_seconds)}</td
              >
              <td role="cell" class="end tabular" data-label="Estimated cost"
                >{cost(row.estimated_cost)}</td
              >
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</LoadingBoundary>

<!-- Outside the boundary: an empty report has no groups to show, but a range
     with no job still has a plain answer to give here, and the release table's
     own request may fail on its own, for a window over the server's limit. -->
<ReleaseTable from={query.from} to={query.to} />

<style>
  .installation-report {
    margin: 0 0 var(--z-space-5);
  }
  .detail-heading {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: var(--z-space-2);
    margin: var(--z-space-5) 0 var(--z-space-3);
  }
  .detail-heading h2 {
    font-size: var(--z-text-sm);
    font-weight: var(--z-weight-semibold);
    margin: 0;
  }
  .detail-heading span {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }

  .controls {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3) var(--z-space-5);
    margin: var(--z-space-4) 0;
  }
  .grouping {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
  }
  .grouping span {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
    white-space: nowrap;
  }
  .note {
    max-width: 70ch;
    margin: 0 0 var(--z-space-3);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
    color: var(--z-text-muted);
  }
  .attribution {
    color: var(--z-text);
  }
  /*
    Not `.table`: Tailwind's utility of that name sets `display: table`, which
    made this frame size itself to the columns inside it instead of scrolling
    them. On a phone the whole document then grew to the table's width, the
    fixed navigation bar grew with it, and taps on the bar landed on the footer
    underneath.
  */
  .frame {
    overflow-x: auto;
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface);
  }
  table {
    width: 100%;
    /* The frame decides the width and the columns divide it, so the report
       never scrolls sideways -- see "Tables fit the window". */
    table-layout: fixed;
    border-collapse: collapse;
    font-size: var(--z-text-sm);
  }
  th,
  td {
    padding: var(--z-space-3) var(--z-space-4);
    text-align: left;
    /*
      Wrapped, not truncated. The column widths are settled by the fixed layout
      above, so wrapping cannot widen the table -- and "Average queue wait" cut
      to "Averag..." is a heading nobody can read, while a figure has no spaces
      to wrap at and stays on its line regardless.
    */
    white-space: normal;
    overflow: hidden;
    text-overflow: ellipsis;
    border-bottom: var(--z-border-width) solid var(--z-border);
  }
  /* The key is the long one, and the only one worth more than its share:
     every other column is a figure, and a figure cut short is the wrong
     figure rather than a shorter one. The name itself stays on one line --
     there is a whole card for it below the breakpoint -- and its title
     carries the rest. */
  .key-header,
  th.key {
    width: 20%;
  }
  th.key {
    white-space: nowrap;
  }
  tbody tr:last-child th,
  tbody tr:last-child td {
    border-bottom: 0;
  }
  thead th {
    /* A single long word -- "Completed", "Estimated cost" -- has nowhere to
       wrap, and a heading cut to "COMPLE..." names nothing. Breaking it is the
       lesser of the two. */
    overflow-wrap: anywhere;
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
    background: var(--z-surface-sunken);
  }
  .end {
    text-align: right;
  }
  .key {
    font-weight: var(--z-weight-medium);
    color: var(--z-text);
    max-width: 24rem;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .name {
    /* The tag sits beside the name rather than under it, so a narrow column
       truncates the name and never the word that says what the row is. */
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .name.muted {
    color: var(--z-text-muted);
    font-weight: var(--z-weight-normal);
  }
  .tag {
    margin-left: var(--z-space-2);
    padding: 0 var(--z-space-2);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    text-transform: uppercase;
    letter-spacing: var(--z-tracking-wide);
    color: var(--z-text-muted);
    white-space: nowrap;
  }

  /*
    On a phone this is ten columns in 412 pixels, and no amount of scrolling
    makes that a table: the heading is cut off mid-word, every pool is
    "zoomies-demo-lin..." and indistinguishable from the next, and the figure
    you scrolled to belongs to a row you can no longer name. Pinning the first
    column fixed only the last of those.

    So each row becomes a card, which is what the Hosts page does with the same
    problem, and each figure carries its own heading. Nothing is dropped and
    nothing is truncated; the report simply reads down instead of across.

    It reads down for longer than a phone, unlike every other table in the
    product. Ten columns is 79 pixels each in the 794 a 900px window leaves,
    and a number squeezed to eight characters is not a smaller number -- it is
    the wrong one. The same argument that makes this a card on a phone makes it
    a card on a laptop's half-screen window, so the threshold here is
    `--z-bp-lg` rather than `--z-bp-md`.
  */
  @media (max-width: 1180px) {
    .frame {
      border: 0;
      background: none;
      overflow-x: visible;
    }
    table {
      display: block;
    }
    /* The column headings move onto the cells, so the row of them is noise. */
    thead {
      display: none;
    }
    tbody {
      display: flex;
      flex-direction: column;
      gap: var(--z-space-3);
    }
    /* Already blockified by the flex tbody above, so it takes padding and a
       border without needing a display of its own. */
    tbody tr {
      padding: var(--z-space-3) var(--z-space-4);
      border: var(--z-border-width) solid var(--z-border);
      border-radius: var(--z-radius-md);
      background: var(--z-surface);
    }
    tbody th.key {
      display: block;
      max-width: none;
      padding: 0 0 var(--z-space-2);
      border-bottom: var(--z-border-width) solid var(--z-border);
      /* The name is the whole point of the row, so it wraps rather than
         truncating -- there is a whole card's width for it now. */
      overflow: visible;
      white-space: normal;
      overflow-wrap: anywhere;
    }
    tbody td {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--z-space-4);
      padding: var(--z-space-2) 0;
      border: 0;
      text-align: right;
      white-space: normal;
    }
    tbody td::before {
      content: attr(data-label);
      flex: none;
      font-size: var(--z-text-2xs);
      font-weight: var(--z-weight-medium);
      text-transform: uppercase;
      letter-spacing: var(--z-tracking-wide);
      color: var(--z-text-muted);
      text-align: left;
    }
    .name {
      overflow: visible;
      white-space: normal;
    }
  }
  .tabular {
    font-variant-numeric: tabular-nums;
  }
</style>
