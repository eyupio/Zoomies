<!--
  The Usage page's charts: the report as a trend, the groups that used the
  most, and the calendar of squares.

  The trend used to draw one fixed set of lines, chosen from a dropdown, into
  a fixed picture stretched to whatever width the panel had, with a label at
  each end and nothing in between. It is the Overview's fleet activity chart
  now -- literally the same drawing, and the same chips and rows around it --
  and so it reads the way the capacity map does: a figure per chip, drawn one
  unit to a pixel, on an axis of round numbers, with the times along the
  bottom on round hours and midnights. Pointing at a line singles it out; the
  rows beneath are the legend, the switchboard and the reading at once.

  Where a pool had nowhere to put a runner, the chart is shaded. It is this
  panel's version of the fleet trend's starvation band: the report already
  counts those minutes -- the matrix can colour its squares by them -- and
  "the work arrived and the fleet had no room for it" is what an operator
  scanning a fortnight is looking for.

  Two measures rather than one chart, because jobs and hours do not share an
  axis. And the crosshair reads a moment rather than choosing one: choosing
  is the matrix's job, since selecting a day there fetches that day's hours,
  and a finger dragged across a month would otherwise ask for thirty of them.
  A square chosen in the matrix does move the crosshair here, which is the
  direction that costs nothing.

  The matrix is drawn in the squares the Overview would draw the same length
  of window in -- hours for a week, six squares a day for a month, three for a
  quarter -- rather than in the report's own days, which for a month were five
  columns in the corner of the panel. Where those squares are narrower than
  the report's, the matrix asks for its own series, in the report's grouping
  and focus; the chart keeps the report's, and a square chosen in the matrix
  moves the crosshair to the interval it falls in.
-->
<script lang="ts">
  import { getUsage } from '$lib/api/client';
  import type { UsageRow, UsageGrouping } from '$lib/api/types';
  import MetricGrid from '$lib/components/MetricGrid.svelte';
  import ChartPanel from '$lib/components/ChartPanel.svelte';
  import ErrorState from '$lib/components/ErrorState.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import Select from '$lib/components/Select.svelte';
  import Skeleton from '$lib/components/Skeleton.svelte';
  import ActivityMatrix, { type ActivityRange } from '$lib/insights/ActivityMatrix.svelte';
  import FigureChips from '$lib/insights/FigureChips.svelte';
  import FigureRows from '$lib/insights/FigureRows.svelte';
  import ReadingCard from '$lib/insights/ReadingCard.svelte';
  import TrendPlot from '$lib/insights/TrendPlot.svelte';
  import { countAxis, seriesPeak } from '$lib/insights/plot';
  import {
    DEFAULT_FIGURES,
    USAGE_FIGURES,
    USAGE_MEASURES,
    capacityRuns,
    inProgress,
    usageFigures,
    usageFormat,
    usageLines,
    type UsageKey,
    type UsageMeasure,
  } from '$lib/insights/usageSeries';
  import {
    addDays,
    bucketIndex,
    fillWindow,
    hasCapacity,
    intervalWidth,
    localDate,
    matrixInterval,
    mergeHistories,
    MODES,
    slicesPerDay,
    startOfLocalDay,
    type ActivityBucket,
    type ActivityMode,
    type Interval,
  } from '$lib/insights/activity';
  import { remember, remembered } from '$lib/state/prefs.svelte';
  import { formatDuration, formatNumber, NO_VALUE, toMillis } from '$lib/format';
  let {
    rows,
    grouping,
    range,
    entity = '',
    fetchedAt = 0,
    label,
    onselect,
  }: {
    rows: UsageRow[];
    grouping: UsageGrouping;
    /** The report's own bounds, as the request carried them. */
    range: { from: string; to: string };
    /** The group the report is focused on, or empty for all of them. */
    entity?: string;
    /** When the report landed, so the matrix knows its hours are stale. */
    fetchedAt?: number;
    label: (key: string) => string;
    onselect: (key: string) => void;
  } = $props();
  type Bucket = ActivityBucket;

  const KEY = 'zoomies.usage.chart';
  const isMeasure = (v: unknown): v is UsageMeasure => USAGE_MEASURES.some((m) => m.value === v);
  const isFigures = (v: unknown): v is UsageKey[] =>
    Array.isArray(v) && v.every((k) => USAGE_FIGURES.some((f) => f.key === k));

  let measure = $state<UsageMeasure>(remembered(`${KEY}.measure`, 'jobs', isMeasure));
  let enabled = $state<UsageKey[]>(remembered(`${KEY}.figures`, [...DEFAULT_FIGURES], isFigures));
  $effect(() => remember(`${KEY}.measure`, measure));
  $effect(() => remember(`${KEY}.figures`, enabled));

  /** The square the matrix has chosen, which is also the crosshair's moment. */
  let selected = $state<number | null>(null);
  let mode = $state<ActivityMode>('outcomes');

  const total = $derived(
    rows.reduce(
      (a, r) => ({
        jobs: a.jobs + r.jobs,
        completed: a.completed + r.jobs_completed,
        succeeded: a.succeeded + (r.succeeded ?? 0),
        failed: a.failed + (r.failed ?? 0),
        cancelled: a.cancelled + (r.cancelled ?? 0),
        unknown: a.unknown + (r.unknown ?? 0),
        seconds: a.seconds + r.job_execution_seconds,
        allocated: a.allocated + (r.allocated_runner_seconds ?? 0),
        starts: a.starts + r.jobs_started,
        wait: a.wait + (r.average_queue_wait_seconds ?? 0) * r.jobs_started,
      }),
      {
        jobs: 0,
        completed: 0,
        succeeded: 0,
        failed: 0,
        cancelled: 0,
        unknown: 0,
        seconds: 0,
        allocated: 0,
        starts: 0,
        wait: 0,
      },
    ),
  );

  /**
   * The API cuts hourly buckets for a window of two days or less and daily
   * ones beyond, anchored at the start the request named; the same rule here
   * is what lets the matrix know whether a square is an hour or a day.
   */
  const interval = $derived<Interval>(
    (toMillis(range.to) ?? 0) - (toMillis(range.from) ?? 0) <= 48 * intervalWidth('hour')
      ? 'hour'
      : 'day',
  );
  const first = $derived(startOfLocalDay(new Date(toMillis(range.from) ?? Date.now())));
  const buckets = $derived.by(() => {
    const from = new Date(toMillis(range.from) ?? 0);
    const span = (toMillis(range.to) ?? 0) - from.getTime();
    const count = Math.max(1, Math.ceil(span / intervalWidth(interval)));
    return fillWindow(mergeHistories(rows), from, count, interval);
  });
  const modes = $derived(MODES.filter((m) => m.value !== 'capacity' || hasCapacity(buckets)));

  /* -- the matrix's own squares ---------------------------------------------- */

  /** The window's length, elapsed, which is what the API bounds its buckets by. */
  const span = $derived((toMillis(range.to) ?? 0) - (toMillis(range.from) ?? 0));
  /** The square the matrix draws in, which the Overview would use for a window this long. */
  const squares = $derived(matrixInterval(span));
  /** How big a square would like to be: the Overview's size for the same squares. */
  const size = $derived(squares === 'hour' ? 'lg' : squares === 'day' ? 'sm' : 'md');
  /** Whether the matrix needs a series of its own, or can draw the report's. */
  const own = $derived(squares !== interval);
  /**
   * The request the matrix's own series answers, as a string: a series is
   * drawn only under the request it came from, so a report narrowed or
   * refreshed never shows the squares of the one before it.
   */
  const request = $derived(
    own ? [grouping, entity, range.from, range.to, squares, fetchedAt].join('|') : '',
  );
  let fetched = $state.raw<{ request: string; buckets: Bucket[] } | null>(null);
  let failed = $state<{ request: string; cause: unknown } | null>(null);
  let attempt = $state(0);

  $effect(() => {
    void attempt;
    const asked = request;
    if (!asked) return;
    const width = squares;
    const from = new Date(toMillis(range.from) ?? 0);
    const count = Math.max(1, Math.ceil(span / intervalWidth(width)));
    const controller = new AbortController();
    failed = null;
    getUsage(
      {
        from: range.from,
        to: range.to,
        group_by: grouping,
        key: entity || undefined,
        interval: width,
      },
      controller.signal,
    )
      .then((report) => {
        fetched = {
          request: asked,
          buckets: fillWindow(mergeHistories(report.items ?? []), from, count, width),
        };
      })
      .catch((cause) => {
        if (!controller.signal.aborted) failed = { request: asked, cause };
      });
    return () => controller.abort();
  });

  /** What the matrix draws, or null while its own series is on its way. */
  const squaresDrawn = $derived(
    own ? (fetched?.request === request ? fetched.buckets : null) : buckets,
  );
  const squaresFailed = $derived(own && failed?.request === request ? failed.cause : null);

  /** A day's hours for the detail, in the report's own grouping and focus. */
  async function hourly(day: Date): Promise<Bucket[]> {
    const to = addDays(day, 1);
    const report = await getUsage({
      from: day.toISOString(),
      to: to.toISOString(),
      group_by: grouping,
      key: entity || undefined,
    });
    const count = Math.round((to.getTime() - day.getTime()) / intervalWidth('hour'));
    return fillWindow(mergeHistories(report.items ?? []), day, count, 'hour');
  }

  /**
   * The Jobs page cut to the square's day and, where the grouping is one the
   * Jobs page can filter by, to the focused group. An hour cannot be asked
   * for there, so an hour's link is its day's.
   */
  function links(at: ActivityRange) {
    const day = localDate(at.from);
    const params = [`since=${day}`, `until=${day}`];
    const filter = { pool: 'pool_id', repository: 'repo', workflow: 'workflow' }[
      grouping as string
    ];
    if (entity && filter) params.push(`${filter}=${encodeURIComponent(entity)}`);
    const out = [{ href: `/jobs?${params.join('&')}`, label: 'Jobs that day' }];
    if (at.bucket.failed > 0) {
      out.push({ href: `/jobs?failed=true&${params.join('&')}`, label: 'Failed jobs' });
    }
    return out;
  }
  const metrics = $derived([
    {
      label: 'Jobs queued',
      value: formatNumber(total.jobs),
      detail: `${formatNumber(total.starts)} started in this window`,
    },
    {
      label: 'Success rate',
      value: total.completed
        ? `${((100 * total.succeeded) / total.completed).toFixed(1)}%`
        : NO_VALUE,
      detail: `${formatNumber(total.succeeded)} of ${formatNumber(total.completed)} completed`,
      tone: 'success' as const,
    },
    {
      label: 'Failed jobs',
      value: formatNumber(total.failed),
      detail: `${formatNumber(total.cancelled)} cancelled / skipped · ${formatNumber(total.unknown)} unknown`,
      tone: total.failed ? ('danger' as const) : ('neutral' as const),
    },
    {
      label: 'Execution hours',
      value: (total.seconds / 3600).toFixed(2),
      detail: 'Running time inside this window',
    },
    {
      label: 'Average queue wait',
      value: total.starts ? formatDuration((1000 * total.wait) / total.starts) : NO_VALUE,
      detail: 'Weighted across jobs that started',
    },
    {
      label: 'Runner busy share',
      // A share only while the runner records are all there: runners are
      // kept for less time than jobs, so a range that reaches past runner
      // retention has its jobs' hours and only some of its runners', and the
      // ratio is then a number that means nothing rather than a percentage.
      value:
        total.allocated && total.allocated >= total.seconds
          ? `${((100 * total.seconds) / total.allocated).toFixed(1)}%`
          : NO_VALUE,
      detail:
        grouping === 'repository' || grouping === 'workflow'
          ? 'Allocation not attributable'
          : total.allocated && total.allocated < total.seconds
            ? 'Runner records for part of this range are no longer retained'
            : `${(total.allocated / 3600).toFixed(2)} allocated runner-hours`,
    },
  ]);
  /**
   * Runner-hours belong to pools and installations: a runner idles on behalf
   * of a pool and never on behalf of a repository or a workflow, so at those
   * groupings the allocated line is dropped rather than drawn as somebody
   * else's hours. The table drops its column for the same reason.
   */
  const attributable = $derived(grouping !== 'repository' && grouping !== 'workflow');
  const figures = $derived(usageFigures(measure, attributable));
  const drawn = $derived(figures.filter((f) => enabled.includes(f.key)));
  /**
   * When the report was counted, which is where "not yet" begins. A report
   * that has not landed yet is read against the clock, so an empty chart is
   * still drawn against the right window.
   */
  const now = $derived(fetchedAt || Date.now());
  const lines = $derived(usageLines(buckets, drawn, now));
  const count = $derived(Math.max(1, buckets.length));
  const format = $derived(usageFormat(measure));
  const axis = $derived(
    countAxis(
      lines.reduce(
        (top, line) =>
          line.points.reduce((n, p) => (p.value !== null && p.value > n ? p.value : n), top),
        0,
      ),
      4,
      measure === 'runtime',
    ),
  );
  // The chart leads with the first figure on it, in the chips' order: the
  // peak in the headline is that figure's, and a chart down to one line
  // washes the area under it.
  const lead = $derived(drawn[0] ?? null);
  const peak = $derived(seriesPeak(lead ? lines.filter((l) => l.series === lead) : []));

  // The band is drawn from the report's capacity observations whether or not
  // a figure of the chosen measure is on the chart: it says the fleet had
  // nowhere to put a runner, which is true of the window however it is being
  // read. Only pools observe placement, so only a report that carries such
  // observations claims anything about them.
  const bands = $derived(capacityRuns(buckets));
  const bandCount = $derived(bands.reduce((n, run) => n + (run.to - run.from + 1), 0));
  const observed = $derived(hasCapacity(buckets));

  /* -- reading an interval --------------------------------------------------- */

  const at = (bucket: Bucket | undefined) => toMillis(bucket?.from ?? '') ?? now;

  // The pointer reads; the matrix chooses. A square chosen there moves the
  // crosshair, and the crosshair moved here chooses nothing, because choosing
  // a day fetches that day's hours and a drag across a month would fetch a
  // month of them.
  let hover = $state<number | null>(null);
  /**
   * The chart's interval a chosen square falls in. The same index where the
   * two draw the same series; a moment looked up where the matrix cuts the
   * report's days into narrower squares.
   */
  let cursor = $derived.by(() => {
    if (selected === null) return null;
    if (!own) return selected;
    const moment = toMillis(squaresDrawn?.[selected]?.from);
    if (moment === null) return null;
    const i = bucketIndex(buckets, moment, interval);
    return i < 0 ? null : i;
  });
  $effect(() => {
    void buckets;
    selected = null;
    cursor = null;
  });

  const reading = $derived(hover !== null || cursor !== null);
  /**
   * The newest interval the report has actually counted, which is where the
   * reading sits when nobody is inspecting one. Not the last interval of the
   * window: a report to the end of today has hours in it that have not
   * happened, and the meters would then read as a fleet that had gone quiet.
   */
  const newest = $derived.by(() => {
    for (let i = buckets.length - 1; i >= 0; i--) if (at(buckets[i]) <= now) return i;
    return Math.max(0, buckets.length - 1);
  });
  const latest = $derived(buckets[newest]);
  const activeIndex = $derived(Math.min(count - 1, hover ?? cursor ?? newest));
  const active = $derived(buckets[activeIndex]);
  const unit = $derived(interval === 'day' ? 'days' : 'hours');
  /**
   * Two ways to write a moment: the axis wants the shortest thing that is
   * still unambiguous, since its labels sit side by side and the first of
   * them is against the edge, and a reading wants the day named. A report of
   * one day drops the weekday from its axis, where every label would carry
   * the same one.
   */
  const oneDay = $derived(
    buckets.length > 0 &&
      // The whole window, not the part of it that has happened: the axis is
      // drawn across all of it, and two midnights on one axis both labelled
      // "00:00" are a chart nobody can read left to right.
      localDate(new Date(at(buckets[0]))) === localDate(new Date(at(buckets[buckets.length - 1]))),
  );
  const stamp = (moment: number) =>
    new Date(moment).toLocaleString(
      undefined,
      interval === 'day'
        ? { day: 'numeric', month: 'short' }
        : oneDay
          ? { hour: '2-digit', minute: '2-digit' }
          : { weekday: 'short', hour: '2-digit', minute: '2-digit' },
    );
  const when = (moment: number) =>
    new Date(moment).toLocaleString(
      undefined,
      interval === 'day'
        ? { weekday: 'short', day: 'numeric', month: 'short' }
        : { weekday: 'short', hour: '2-digit', minute: '2-digit' },
    );
  const activeAt = $derived(at(active));
  const moment = $derived(reading ? `at ${when(activeAt)}` : 'in the latest interval');

  /** What each drawn figure read at the crosshair's interval. */
  const readings = $derived(
    lines.map((line) => ({
      series: line.series,
      value: line.points[activeIndex]?.value ?? null,
    })),
  );

  // Two ways to single a figure out, and either will do: its chip or its row,
  // and the line the pointer is nearest. A figure that is not on the chart
  // singles out nothing -- dimming every line to show that one is absent
  // reads as a fault, not an answer.
  let chip = $state<UsageKey | null>(null);
  let near = $state<string | null>(null);
  const emphasis = $derived.by(() => {
    const key = chip ?? near;
    return figures.find((f) => f.key === key && enabled.includes(f.key))?.key ?? null;
  });
  function toggle(key: UsageKey): void {
    enabled = enabled.includes(key) ? enabled.filter((k) => k !== key) : [...enabled, key];
  }

  const valuetext = $derived(
    `${when(activeAt)}: ${
      readings.length
        ? readings
            .map((r) => `${r.series.label} ${r.value === null ? 'not yet' : format(r.value)}`)
            .join(', ')
        : 'no figures chosen'
    }`,
  );
  const description = $derived(
    `${
      measure === 'runtime'
        ? 'Executing and allocated runner time, in hours'
        : 'Job demand and what became of it, counted'
    } per ${interval}, across the selected window. Select a square in the matrix below to bring its interval under the crosshair.`,
  );

  const ranked = $derived(
    [...rows].sort((a, b) => b.job_execution_seconds - a.job_execution_seconds).slice(0, 10),
  );
  const maxHours = $derived(Math.max(1, ...ranked.map((r) => r.job_execution_seconds)));
</script>

{#snippet card()}
  <ReadingCard
    when={when(activeAt)}
    note={active && inProgress(active, interval, now) ? 'still being filled' : undefined}
    {readings}
    {emphasis}
    {format}
  />
{/snippet}

<MetricGrid items={metrics} />
<div class="insights">
  <ChartPanel title="Usage over time" {description}>
    {#snippet actions()}
      <Segmented
        label="Measure"
        value={measure}
        options={USAGE_MEASURES}
        onchange={(v) => (measure = v as UsageMeasure)}
      />
    {/snippet}

    <div class="trend">
      <div class="toolbar">
        <FigureChips
          label="Figures shown"
          {figures}
          {enabled}
          group={(figure) => (figure.demand ? 'demand' : 'outcome')}
          ontoggle={toggle}
          onchip={(key) => (chip = key)}
        />
        {#if buckets.length}
          <p class="headline" aria-live="off">
            {#if peak && lead}
              <span
                >Peak in view: <strong>{format(peak.value)}</strong>
                {lead.label.toLowerCase()}, {interval === 'day' ? 'on' : 'at'}
                {when(peak.line.points[peak.i]?.at ?? activeAt)}</span
              >
            {/if}
            {#if observed}
              <span>
                <strong class:warn={bandCount > 0}>{bandCount}</strong>
                of {count}
                {unit} with nowhere to place a runner
              </span>
            {/if}
          </p>
        {/if}
      </div>

      <TrendPlot
        {lines}
        {count}
        ceiling={axis.ceiling}
        grid={axis.values}
        start={at(buckets[0])}
        end={at(buckets[buckets.length - 1])}
        {bands}
        {activeIndex}
        {reading}
        {emphasis}
        lead={lead?.key ?? null}
        present={latest && inProgress(latest, interval, now) ? newest : null}
        revealKey={`${measure}:${grouping}:${entity}:${range.from}:${range.to}`}
        {stamp}
        {format}
        message={buckets.length === 0
          ? 'No retained history in this window.'
          : drawn.length === 0
            ? 'Choose a figure above to draw it.'
            : undefined}
        label={`Usage over time: ${drawn.map((f) => f.label.toLowerCase()).join(', ') || 'no figures chosen'}; ${count} ${unit}. Inspect the timeline below for exact values.`}
        onhover={(i) => (hover = i)}
        onpress={(i) => (cursor = i)}
        onnear={(key) => (near = key)}
        card={readings.length ? card : undefined}
      />

      <div class="scrub">
        <label>
          <span>Inspect an interval</span>
          <input
            type="range"
            min="0"
            max={Math.max(0, count - 1)}
            value={cursor ?? newest}
            oninput={(e) => (cursor = Number(e.currentTarget.value))}
            aria-valuetext={valuetext}
          />
        </label>
        <output
          >{when(activeAt)}{#if !reading}<span class="latest">· latest</span>{/if}</output
        >
        {#if cursor !== null}
          <!-- A chosen interval stays chosen until it is let go of, so there
               has to be a way to let go: on a phone, where a tap chose it,
               there is no pointer to move away. It lets the matrix's square
               go as well, since that is where the crosshair came from. -->
          <button
            type="button"
            class="text"
            onclick={() => {
              selected = null;
              cursor = null;
            }}>Back to the latest</button
          >
        {/if}
      </div>

      <FigureRows
        label="Figures read at this interval"
        {readings}
        ceiling={axis.ceiling}
        {emphasis}
        {moment}
        {format}
        empty="No figures chosen. Switch one on above to draw it."
        ontoggle={toggle}
        onchip={(key) => (chip = key)}
      />
    </div>
  </ChartPanel>
  <ChartPanel
    title="Execution by group"
    description="The ten largest consumers of runner execution time. Select a group to focus the report."
  >
    <div class="ranking">
      {#each ranked as row (row.key)}<button
          class="rank"
          disabled={!row.key}
          onclick={() => onselect(row.key)}
          ><span class="rank-heading"
            ><span>{label(row.key)}</span><strong
              >{(row.job_execution_seconds / 3600).toFixed(2)} h</strong
            ></span
          ><span class="track"
            ><span style:width={`${(100 * row.job_execution_seconds) / maxHours}%`}></span></span
          ><small
            >{formatNumber(row.jobs_completed)} completed · {formatNumber(row.failed ?? 0)} failed</small
          ></button
        >{/each}
    </div>
  </ChartPanel>
</div>
<ChartPanel
  title="Activity matrix"
  description={squares === 'day'
    ? 'Each square is one day of the range, laid out as a calendar. Greener as more jobs finish, red when any fail; hover a square for its figures and select it for its hours and its jobs.'
    : squares === 'hour'
      ? 'Each square is one hour of the range. Greener as more jobs finish, red when any fail; hover a square for its figures and select it for its jobs.'
      : `Each day of the range is ${slicesPerDay(squares)} squares of ${intervalWidth(squares) / intervalWidth('hour')} hours, laid out as a calendar. Greener as more jobs finish, red when any fail; hover a square for its figures and select it for its day's hours and its jobs.`}
>
  {#snippet actions()}<Select
      ariaLabel="Colour the matrix by"
      value={mode}
      size="sm"
      options={modes}
      onchange={(v) => (mode = v as ActivityMode)}
    />{/snippet}
  {#if squaresFailed}
    <ErrorState
      error={squaresFailed}
      compact
      title="The activity matrix could not be loaded"
      onretry={() => (attempt += 1)}
    />
  {:else if !squaresDrawn}
    <p class="sr-only">Loading the activity matrix.</p>
    <div class="placeholder" aria-hidden="true">
      <Skeleton width="100%" height="var(--z-space-10)" />
      <Skeleton width="100%" height="var(--z-space-10)" />
    </div>
  {:else}
    <ActivityMatrix
      buckets={squaresDrawn}
      interval={squares}
      {first}
      {mode}
      {size}
      bind:selected
      {fetchedAt}
      hourly={squares === 'hour' ? undefined : hourly}
      {links}
      subject={entity ? label(entity) : `every ${grouping}`}
    />
  {/if}
  <p class="muted">
    {#if grouping === 'pool'}Capacity reached counts observed pool-minutes with a host-capacity
      placement block, not incidents or exact duration. Dashed squares have no capacity
      observations. History starts after this feature is installed.{:else}Capacity observations
      belong to pools. Choose Pool to inspect recorded placement bottlenecks.{/if} All history reflects
    retained records; pruning can remove older activity.
  </p>
</ChartPanel>

<style>
  .insights {
    display: grid;
    grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
    gap: var(--z-space-4);
    margin-bottom: var(--z-space-4);
  }
  /* -- the trend: the chips, the headline, the plot and its timeline -------- */
  .trend {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
    min-width: 0;
  }
  .toolbar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--z-space-2) var(--z-space-4);
  }
  .headline {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-4);
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .headline strong {
    color: var(--z-text);
    font-weight: var(--z-weight-semibold);
    font-variant-numeric: tabular-nums;
  }
  .headline strong.warn {
    color: var(--z-pending);
  }
  .scrub {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    flex-wrap: wrap;
  }
  label {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    flex: 1;
    font-size: var(--z-text-xs);
    min-width: 0;
  }
  label span {
    white-space: nowrap;
  }
  input {
    width: 100%;
    min-width: 70px;
    accent-color: var(--z-accent);
    height: var(--z-space-6);
  }
  output {
    font-size: var(--z-text-xs);
    font-variant-numeric: tabular-nums;
  }
  output .latest {
    margin-left: var(--z-space-1);
    color: var(--z-text-muted);
  }
  button.text {
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-accent);
    font-size: var(--z-text-xs);
    text-decoration: underline;
    cursor: pointer;
  }

  .ranking {
    display: grid;
    gap: var(--z-space-3);
    max-height: 340px;
    overflow-y: auto;
  }
  .rank {
    padding: var(--z-space-2);
    text-align: left;
    background: none;
    border: 0;
    border-radius: var(--z-radius-sm);
    color: var(--z-text);
    cursor: pointer;
  }
  .rank:hover {
    background: var(--z-surface-hover);
  }
  .rank-heading {
    display: flex;
    justify-content: space-between;
    gap: var(--z-space-3);
    font-size: var(--z-text-xs);
  }
  .rank-heading > span {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  strong {
    font-weight: var(--z-weight-semibold);
  }
  .rank-heading strong {
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
  }
  .track {
    display: block;
    height: var(--z-space-2);
    margin: var(--z-space-2) 0;
    background: var(--z-surface-sunken);
    border-radius: var(--z-radius-full);
    overflow: hidden;
  }
  .track > span {
    display: block;
    height: 100%;
    background: var(--z-accent);
    border-radius: inherit;
  }
  small,
  .muted {
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  .muted {
    margin: var(--z-space-4) 0 0;
  }
  .placeholder {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  @media (max-width: 1024px) {
    .insights {
      grid-template-columns: 1fr;
    }
    .ranking {
      grid-template-columns: repeat(2, minmax(0, 1fr));
      max-height: none;
    }
  }
  @media (max-width: 768px) {
    .ranking {
      grid-template-columns: 1fr;
    }
  }
</style>
