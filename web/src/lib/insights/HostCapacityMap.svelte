<!--
  The Host capacity map: every host's utilisation on one chart, live and
  backwards in time.

  Each host is a colour and each measurement is a stroke, so a line is read
  the way the usage chart's are: the colour says which machine, the dash says
  what about it. Everything is a share of the machine -- CPU measured, memory
  measured, load against the CPU count, runners against slots, what the
  scheduler has promised against what it may place on, disk -- so seven
  different figures sit on one 0-100% axis and can be read against each other.
  Only load may pass 100, because a load of twice the CPUs is a real thing and
  is what steps a throttle up. It gets a lane of its own above the axis, on
  its own scale up to the highest load in view: a spike to eight times the
  cores is still drawn as a spike, and the axis everything else is read on
  keeps its room rather than being squashed into the bottom tenth of the chart
  behind thirty labels.

  Two layouts, because two questions get asked of it. Every host on one plot
  answers "which machine is the busy one" -- the lines are read against each
  other, and pointing at one, or at its row below, brings it forward and
  steps the rest back. A plot per host answers "what has this machine been
  doing": each has its own drawing, with the wash under its lead line, and
  the plots share one x axis and one crosshair so a moment is still read
  across the fleet. Which one the map opens in is the fleet's to say, and it
  says it per page -- ui.capacity_map.overview_layout and .hosts_layout --
  because the Overview is glanced at and the Hosts page is looked into, and
  a fleet may want each to open on its own answer. An operator who picks the
  other one keeps their pick in this browser, for this page; until they do,
  the fleet's answer follows them, including when an administrator changes
  it. Every other choice here is remembered the moment it is made.

  The rows beneath are the legend and the switchboard, and they are also the
  reading: each host's meters show its figures at the moment under the
  crosshair, or now when nothing is being read, so the exact numbers for a
  moment are never only in a card that follows the pointer. The line above
  the chart says what an operator would otherwise have to hunt for -- the
  highest figure in view and where it was, and how many hosts are past the
  pressure line right now.

  The controller writes one sample per host per minute; the stream keeps the
  newest point moving between them, from the same host view the cards below
  are drawn from, so the chart's right-hand edge and the card never disagree.
  A host that stops reporting draws a gap, not a flat line: a flat line is
  what a healthy, quiet machine draws too. One missing minute is not that,
  though -- it is a sampler a moment late or a tab that slept -- so the line
  is drawn across it and only two absent in a row break it.

  The three shortest windows -- the last minute, five, ten -- are drawn ten
  seconds to a point, from what the stream delivers while the page is open,
  because an operator watching a job land wants each heartbeat and not the
  last of every two. Behind them the minute samples are still the history,
  so those windows fill in as the stream arrives.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { listHostSamples } from '$lib/api/client';
  import type { Host, HostSample } from '$lib/api/types';
  import ChartPanel from '$lib/components/ChartPanel.svelte';
  import Segmented from '$lib/components/Segmented.svelte';
  import StatusDot from '$lib/components/StatusDot.svelte';
  import Switch from '$lib/components/Switch.svelte';
  import { NO_VALUE } from '$lib/format';
  import { fleet } from '$lib/state/fleet.svelte';
  import { remember, remembered } from '$lib/state/prefs.svelte';
  import { session } from '$lib/state/session.svelte';
  import { hostStatus } from '$lib/status';
  import CapacityPlot from './CapacityPlot.svelte';
  import {
    DEFAULT_METRICS,
    LONGEST_FINE_WINDOW,
    LONGEST_WINDOW,
    METRICS,
    PRESSURE,
    WINDOWS,
    grainOf,
    hostLines,
    hostTone,
    leadMetric,
    liveSample,
    mergeHostSamples,
    metricText,
    metricValue,
    overflowCeiling,
    peakOf,
    windowSlots,
    type Metric,
    type MetricKey,
    type WindowKey,
  } from './hostSeries';

  let {
    hosts,
    page,
    onmanage,
  }: {
    hosts: Host[];
    /** Which page this map is on: each has its own default layout in the fleet's settings. */
    page: 'overview' | 'hosts';
    onmanage?: (host: Host) => void;
  } = $props();

  /* -- what is remembered -------------------------------------------------- */

  const KEY = 'zoomies.hosts.map';
  const LAYOUTS = [
    { value: 'overlay', label: 'Overlay', name: 'Every host on one chart' },
    { value: 'split', label: 'Per host', name: 'A chart for each host' },
  ] as const;
  type Layout = (typeof LAYOUTS)[number]['value'];
  const isWindow = (v: unknown): v is WindowKey =>
    typeof v === 'string' && WINDOWS.some((w) => w.value === v);
  const isLayout = (v: unknown): v is Layout =>
    typeof v === 'string' && LAYOUTS.some((l) => l.value === v);
  const isMetrics = (v: unknown): v is MetricKey[] =>
    Array.isArray(v) && v.every((k) => METRICS.some((m) => m.key === k));
  const isIds = (v: unknown): v is string[] =>
    Array.isArray(v) && v.every((k) => typeof k === 'string');

  /** The layout the fleet's settings open this page's map in. */
  function fleetLayout(): Layout {
    const defaults = session.meta?.capacity_map;
    const chosen = page === 'overview' ? defaults?.overview_layout : defaults?.hosts_layout;
    return isLayout(chosen) ? chosen : 'overlay';
  }

  // The day is the default, as it is on every other trend: an hour is too
  // narrow to show a fleet that goes quiet overnight and busy at nine.
  let windowKey = $state<WindowKey>(remembered(`${KEY}.window`, '24h', isWindow));
  // The layout is written only when an operator picks one, never on sight:
  // a browser that had merely opened the page would otherwise have "chosen"
  // whatever the fleet's default was that day, and stop following it.
  // A map belongs to one page for its whole life, so the page it opened on
  // is the page it is on: reading it once is the intent, not an oversight.
  let layout = $state<Layout>(
    untrack(() => remembered(`${KEY}.layout.${page}`, fleetLayout(), isLayout)),
  );
  function chooseLayout(next: Layout): void {
    layout = next;
    remember(`${KEY}.layout.${page}`, next);
  }
  let live = $state(remembered(`${KEY}.live`, true, (v): v is boolean => typeof v === 'boolean'));
  let enabled = $state<MetricKey[]>(remembered(`${KEY}.metrics`, [...DEFAULT_METRICS], isMetrics));
  let hidden = $state<string[]>(remembered(`${KEY}.hidden`, [], isIds));
  $effect(() => remember(`${KEY}.window`, windowKey));
  $effect(() => remember(`${KEY}.live`, live));
  $effect(() => remember(`${KEY}.metrics`, enabled));
  $effect(() => remember(`${KEY}.hidden`, hidden));

  const chosen = $derived(
    WINDOWS.find((w) => w.value === windowKey) ?? WINDOWS.find((w) => w.value === '24h')!,
  );

  /* -- the samples ----------------------------------------------------------- */

  let history = $state.raw<HostSample[]>([]);
  let observed = $state.raw<HostSample[]>([]);
  let recent = $state.raw<HostSample[]>([]);
  let current = $state(Date.now());
  let failed = $state(false);
  let loading = $state(false);
  let attempt = $state(0);

  /** The window's slots: its edges, how many points it has, how fine a slot is. */
  const slots = $derived(windowSlots(current, chosen.seconds, chosen.bucket));
  const bucketMs = $derived(chosen.bucket * 1000);

  $effect(() => {
    void attempt;
    const window = chosen;
    const controller = new AbortController();
    let disposed = false;
    untrack(() => (loading = true));
    void listHostSamples({ window: window.value }, controller.signal)
      .then((page) => {
        if (disposed) return;
        history = page.items ?? [];
        failed = false;
      })
      .catch((cause: unknown) => {
        if (disposed || (cause instanceof DOMException && cause.name === 'AbortError')) return;
        failed = true;
      })
      .finally(() => {
        if (!disposed) loading = false;
      });
    // The right-hand edge moves on every point: once a minute is enough for
    // a window drawn by the minute, and a window drawn finer keeps step.
    const timer = setInterval(() => (current = Date.now()), Math.min(30_000, window.bucket * 1000));
    return () => {
      disposed = true;
      controller.abort();
      clearInterval(timer);
    };
  });

  // The newest point of every line is the host as the stream last showed it.
  // Off, the chart is history alone, which is the honest picture when the
  // question is "what did yesterday look like" rather than "what is it doing".
  //
  // Two buffers, because the two kinds of window want different things kept.
  // The long one holds a point a minute for as far back as the widest window
  // reaches, which is what the windows drawn by the minute fold from. The
  // short one holds every ten-second slot for the last ten minutes, so the
  // sub-minute windows can show each heartbeat: kept at that grain for a
  // week it would be tens of thousands of samples a host, re-sorted on every
  // frame, for detail no window that wide could draw.
  $effect(() => {
    if (!live) return;
    const now = Date.now();
    const fresh = hosts.map((h) => liveSample(h, now));
    untrack(() => {
      observed = [
        ...mergeHostSamples(observed, fresh, now, LONGEST_WINDOW.seconds).values(),
      ].flat();
      recent = [
        ...mergeHostSamples(
          recent,
          fresh,
          now,
          LONGEST_FINE_WINDOW.seconds,
          grainOf(LONGEST_FINE_WINDOW.bucket),
        ).values(),
      ].flat();
      current = now;
    });
  });

  // The replay buffer is finite, so a reconnect reloads history, as every
  // other trend does.
  let wasLive = false;
  $effect(() => {
    const connected = fleet.connection === 'live';
    if (connected && !wasLive) untrack(() => (attempt += 1));
    wasLive = connected;
  });

  /* -- the lines ------------------------------------------------------------- */

  const byHost = $derived(
    mergeHostSamples(
      history,
      live ? [...observed, ...recent] : [],
      current,
      chosen.seconds,
      slots.grain,
    ),
  );
  const count = $derived(slots.count);
  const visibleHosts = $derived(hosts.filter((h) => !hidden.includes(h.id ?? '')));
  const metrics = $derived(METRICS.filter((m) => enabled.includes(m.key)));
  const lead = $derived(leadMetric(enabled));

  // Load is the one figure that can pass 100%, and when it does in this
  // window every plot gets the same lane above its axis, so the stacked
  // plots keep one scale between them.
  const overflow = $derived.by(() => {
    if (!enabled.includes('load')) return 0;
    let peak: number | null = null;
    for (const host of visibleHosts)
      for (const s of byHost.get(host.id ?? '') ?? [])
        peak = Math.max(peak ?? 0, metricValue(s, 'load') ?? 0);
    return overflowCeiling(peak);
  });

  const lines = $derived(
    hostLines(hosts, hidden, byHost, metrics, current, chosen.seconds, chosen.bucket),
  );
  const anyObserved = $derived(lines.some((s) => s.last !== null));

  /* -- reading a moment ------------------------------------------------------ */

  let hover = $state<number | null>(null);
  let selected = $state<number | null>(null);
  const reading = $derived(hover !== null || selected !== null);
  const activeIndex = $derived(Math.min(count - 1, hover ?? selected ?? count - 1));
  /** The last slot of the active point, which is the moment its label names. */
  const activeAt = $derived(slots.end - (count - 1 - activeIndex) * bucketMs);

  const fine = $derived(chosen.bucket < 60);
  const time = (at: number) =>
    new Date(at).toLocaleTimeString(undefined, {
      hour: '2-digit',
      minute: '2-digit',
      ...(fine ? { second: '2-digit' } : {}),
    });
  const stamp = (at: number) =>
    chosen.seconds > 86400
      ? new Date(at).toLocaleString(undefined, {
          weekday: 'short',
          hour: '2-digit',
          minute: '2-digit',
        })
      : time(at);

  /** The window's edges, for the labels along the bottom. */
  const end = $derived(slots.end);
  const start = $derived(end - (count - 1) * bucketMs);

  interface Figure {
    metric: Metric;
    percent: number | null;
    /** The figure in the operator's words, where one point is one sample. */
    exact: string | null;
  }
  interface Reading {
    host: Host;
    tone: string;
    figures: Figure[];
  }
  /** What every visible host read at the active moment. */
  const readings = $derived.by((): Map<string, Reading> => {
    // The sample in the active slot, for the exact figure: only where one
    // point is one slot, since a folded point is a peak of several.
    const grainMs = slots.grain * 1000;
    return new Map(
      visibleHosts.map((host): [string, Reading] => {
        const id = host.id ?? '';
        const sample =
          chosen.bucket === slots.grain
            ? byHost
                .get(id)
                ?.find(
                  (s) =>
                    Math.floor(new Date(s.at ?? '').getTime() / grainMs) * grainMs === activeAt,
                )
            : undefined;
        return [
          id,
          {
            host,
            tone: hostTone(hosts.indexOf(host)),
            figures: metrics.map((metric) => {
              const point = lines.find((s) => s.host === host && s.metric === metric)?.points[
                activeIndex
              ];
              return {
                metric,
                percent: point?.value ?? null,
                exact: sample ? metricText(sample, metric.key) : null,
              };
            }),
          },
        ];
      }),
    );
  });
  /** The readings with the busiest host first, for the card. */
  const ranked = $derived.by(() => {
    const value = (r: Reading) =>
      r.figures.find((f) => f.metric === lead)?.percent ?? Number.NEGATIVE_INFINITY;
    return [...readings.values()].sort((a, b) => value(b) - value(a));
  });
  /** How many rows the card shows before it says "and N more". */
  const CARD_ROWS = 8;

  /* -- the headline ------------------------------------------------------------ */

  const peak = $derived(lead ? peakOf(lines, lead.key) : null);
  const pressed = $derived(
    lead
      ? [...readings.values()].filter(
          (r) => (r.figures.find((f) => f.metric === lead)?.percent ?? 0) >= PRESSURE,
        ).length
      : 0,
  );

  /* -- emphasis ------------------------------------------------------------------ */

  // Three ways to single something out, and they combine: a host from its
  // row or from the line the pointer is nearest, a measurement from its chip.
  let rowHost = $state<string | null>(null);
  let nearHost = $state<string | null>(null);
  let chipMetric = $state<MetricKey | null>(null);
  // A chip that is not on the chart singles out nothing: dimming every line
  // to show that a measurement is absent reads as a fault, not an answer.
  const emphasis = $derived({
    host: layout === 'overlay' ? (rowHost ?? nearHost) : null,
    metric: chipMetric !== null && enabled.includes(chipMetric) ? chipMetric : null,
  });

  /* -- toggles ----------------------------------------------------------------- */

  function toggleMetric(key: MetricKey): void {
    enabled = enabled.includes(key) ? enabled.filter((k) => k !== key) : [...enabled, key];
  }
  function toggleHost(id: string): void {
    hidden = hidden.includes(id) ? hidden.filter((h) => h !== id) : [...hidden, id];
  }
  function only(id: string): void {
    const others = hosts.map((h) => h.id ?? '').filter((h) => h !== id);
    // A second "only" on the one host already alone brings everyone back.
    hidden =
      hidden.length === others.length && others.every((h) => hidden.includes(h)) ? [] : others;
  }

  /** The host as it is right now, for a row that is off the chart. */
  const now = $derived(new Map(hosts.map((h) => [h.id ?? '', liveSample(h, current)])));

  const message = $derived(
    anyObserved
      ? undefined
      : hosts.length === 0
        ? 'No hosts connected yet.'
        : metrics.length === 0
          ? 'Choose a measurement above to draw it.'
          : visibleHosts.length === 0
            ? 'Every host is switched off. Choose one below.'
            : 'No samples in this window yet. The controller writes one a minute.',
  );

  const description = $derived(
    `${hosts.length === 0 ? 'No hosts yet. ' : ''}${chosen.name}. Solid lines are measured by the agent; dashed ones are what the scheduler has promised or what the host holds. Each figure is a share of the machine, so they read against each other, and from ${PRESSURE}% the scheduler places one runner at a time.`,
  );
  const moment = $derived(reading ? `at ${stamp(activeAt)}` : 'now');
  const valuetext = $derived(
    `${stamp(activeAt)}: ${[...readings.values()]
      .map(
        (r) =>
          `${r.host.name ?? r.host.id} ${r.figures
            .map(
              (f) =>
                `${f.metric.label} ${f.percent === null ? 'not measured' : `${f.percent.toFixed(0)}%`}`,
            )
            .join(', ')}`,
      )
      .join('; ')}`,
  );
</script>

{#snippet strokeSample(metric: Metric, tone?: string)}
  <svg class="stroke" viewBox="0 0 24 8" aria-hidden="true">
    <line
      x1="1"
      y1="4"
      x2="23"
      y2="4"
      stroke-dasharray={metric.dash}
      stroke-linecap="round"
      style:stroke={tone}
    />
  </svg>
{/snippet}

{#snippet hostRow(host: Host, index: number)}
  {@const id = host.id ?? ''}
  {@const shown = !hidden.includes(id)}
  {@const tone = hostTone(index)}
  {@const read = shown ? readings.get(id) : undefined}
  {@const sample = now.get(id)}
  <div
    class="host"
    class:off={!shown}
    class:lit={emphasis.host === id}
    role="presentation"
    onpointerenter={() => (rowHost = id)}
    onpointerleave={() => (rowHost = null)}
    onfocusin={() => (rowHost = id)}
    onfocusout={() => (rowHost = null)}
  >
    <button
      type="button"
      class="toggle"
      aria-pressed={shown}
      onclick={() => toggleHost(id)}
      title={shown ? `Hide ${host.name ?? id}` : `Show ${host.name ?? id}`}
    >
      <span class="swatch" style:background={tone}></span>
      <span class="name">{host.name ?? id}</span>
      <StatusDot status={hostStatus(host)} size="sm" />
    </button>
    <span class="meters">
      {#each metrics as metric (metric.key)}
        {@const figure = read?.figures.find((f) => f.metric === metric)}
        {@const v = read
          ? (figure?.percent ?? null)
          : sample
            ? metricValue(sample, metric.key)
            : null}
        {@const words =
          v === null
            ? 'not measured'
            : (figure?.exact ?? (sample && !read ? metricText(sample, metric.key) : null))}
        <span
          class="meter"
          title={`${metric.label} ${shown ? moment : 'now'}: ${words ?? `${v?.toFixed(0)}%`}`}
        >
          {@render strokeSample(metric, tone)}
          <span class="key" aria-label={metric.label}>{metric.short}</span>
          <span class="track">
            {#if v !== null}
              <span
                class="fill"
                class:promised={!metric.measured}
                style:width="{Math.min(100, v)}%"
                style:background={tone}
              ></span>
            {/if}
          </span>
          {#if v === null}<span class="gap">{NO_VALUE}</span>{:else}<strong>{v.toFixed(0)}%</strong
            >{/if}
        </span>
      {/each}
    </span>
    <span class="host-actions">
      <button type="button" class="text" onclick={() => only(id)}>Only</button>
      {#if onmanage}<button type="button" class="text" onclick={() => onmanage?.(host)}
          >Manage</button
        >{/if}
      <a href={`/usage?group_by=host&entity=${encodeURIComponent(id)}`}>History ↗</a>
    </span>
  </div>
{/snippet}

{#snippet card()}
  <div class="reading">
    <p class="when">
      <strong>{stamp(activeAt)}</strong>
      {#if chosen.bucket > 60}<span>{chosen.bucket / 60}-minute peak</span>{/if}
    </p>
    <table>
      <thead>
        <tr>
          <th scope="col"><span class="sr-only">Host</span></th>
          {#each metrics as metric (metric.key)}
            <th scope="col" title={metric.label}>{metric.short}</th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#each ranked.slice(0, CARD_ROWS) as r (r.host.id)}
          <tr class:lit={emphasis.host === r.host.id}>
            <th scope="row">
              <span class="who">
                <span class="swatch" style:background={r.tone}></span>
                <span class="name">{r.host.name ?? r.host.id}</span>
              </span>
            </th>
            {#each r.figures as f (f.metric.key)}
              <td class:pressed={f.percent !== null && f.percent >= PRESSURE}>
                {#if f.percent === null}<span class="gap">{NO_VALUE}</span
                  >{:else}{f.percent.toFixed(0)}%{/if}
              </td>
            {/each}
          </tr>
        {/each}
      </tbody>
    </table>
    {#if ranked.length > CARD_ROWS}
      <p class="more">and {ranked.length - CARD_ROWS} more below</p>
    {/if}
  </div>
{/snippet}

<ChartPanel title="Host capacity map" {description}>
  {#snippet actions()}
    <div class="controls">
      <Switch bind:checked={live} label="Live" />
      <Segmented
        label="Layout"
        value={layout}
        options={LAYOUTS}
        onchange={(v) => chooseLayout(v as Layout)}
      />
      <Segmented
        label="Window"
        value={windowKey}
        options={WINDOWS}
        onchange={(v) => (windowKey = v as WindowKey)}
      />
    </div>
  {/snippet}

  <div class="map">
    <div class="toolbar">
      <div class="metrics" role="group" aria-label="Measurements shown">
        {#each METRICS as metric, i (metric.key)}
          {#if i > 0 && METRICS[i - 1]!.measured && !metric.measured}
            <span class="divider" aria-hidden="true"></span>
          {/if}
          <button
            type="button"
            class="metric"
            aria-pressed={enabled.includes(metric.key)}
            title={metric.hint}
            onclick={() => toggleMetric(metric.key)}
            onpointerenter={() => (chipMetric = metric.key)}
            onpointerleave={() => (chipMetric = null)}
            onfocus={() => (chipMetric = metric.key)}
            onblur={() => (chipMetric = null)}
          >
            {@render strokeSample(metric)}
            {metric.label}
          </button>
        {/each}
      </div>
      {#if lead && anyObserved}
        <p class="headline" aria-live="off">
          {#if peak}
            <span
              >Peak in view: <strong>{peak.value.toFixed(0)}%</strong>
              {lead.label}, on <em>{peak.line.host.name ?? peak.line.host.id}</em> at {stamp(
                slots.end - (count - 1 - peak.i) * bucketMs,
              )}</span
            >
          {/if}
          <span
            ><strong class:warn={pressed > 0}>{pressed} of {visibleHosts.length}</strong>
            {visibleHosts.length === 1 ? 'host' : 'hosts'} past {PRESSURE}% {moment}</span
          >
        </p>
      {/if}
    </div>

    {#if layout === 'overlay'}
      <CapacityPlot
        {lines}
        {count}
        bucket={chosen.bucket}
        {overflow}
        {start}
        {end}
        {activeIndex}
        {reading}
        {emphasis}
        lead={lead?.key ?? null}
        {live}
        labels={visibleHosts.length <= 8}
        wash={visibleHosts.length === 1}
        revealKey={`${windowKey}:${layout}`}
        {stamp}
        {loading}
        {message}
        label={`${lines.length} lines across ${visibleHosts.length} of ${hosts.length} hosts, ${chosen.name.toLowerCase()}. Inspect the timeline below for exact values.`}
        onhover={(i) => (hover = i)}
        onpress={(i) => (selected = i)}
        onnear={(id) => (nearHost = id)}
        card={metrics.length > 0 && readings.size > 0 ? card : undefined}
      />
    {:else}
      <!-- The rows head their plots here, so the group the switches are
           found by is the stack itself. -->
      <div class="panes" role="group" aria-label="Hosts shown">
        {#each hosts as host, index (host.id)}
          {@const id = host.id ?? ''}
          {@const shown = !hidden.includes(id)}
          {@const own = lines.filter((l) => l.host === host)}
          {@const last = visibleHosts[visibleHosts.length - 1] === host}
          <div class="pane" class:off={!shown}>
            {@render hostRow(host, index)}
            {#if shown}
              <CapacityPlot
                lines={own}
                {count}
                bucket={chosen.bucket}
                {overflow}
                {start}
                {end}
                {activeIndex}
                {reading}
                {emphasis}
                lead={lead?.key ?? null}
                {live}
                compact
                axis={last}
                wash
                revealKey={`${windowKey}:${layout}`}
                {stamp}
                {loading}
                message={own.some((l) => l.last !== null)
                  ? undefined
                  : metrics.length === 0
                    ? 'Choose a measurement above to draw it.'
                    : 'No samples in this window yet.'}
                label={`${host.name ?? id}: ${own.length} lines, ${chosen.name.toLowerCase()}. Inspect the timeline below for exact values.`}
                onhover={(i) => (hover = i)}
                onpress={(i) => (selected = i)}
              />
            {/if}
          </div>
        {/each}
        {#if hosts.length === 0}
          <p class="empty">No hosts connected yet.</p>
        {/if}
      </div>
    {/if}

    <div class="scrub">
      <label>
        <span>Inspect a moment</span>
        <input
          type="range"
          min="0"
          max={Math.max(0, count - 1)}
          value={selected ?? count - 1}
          oninput={(e) => (selected = Number(e.currentTarget.value))}
          aria-valuetext={valuetext}
        />
      </label>
      <output
        >{stamp(activeAt)}{#if !reading}<span class="now"> · now</span>{/if}</output
      >
      {#if selected !== null}
        <!-- A chosen moment stays chosen until it is let go of, so there has
             to be a way to let go: on a phone, where a tap chose it, there is
             no pointer to move away. -->
        <button type="button" class="text" onclick={() => (selected = null)}>Back to now</button>
      {/if}
    </div>

    {#if layout === 'overlay'}
      <div class="hosts" role="group" aria-label="Hosts shown">
        {#each hosts as host, index (host.id)}
          {@render hostRow(host, index)}
        {/each}
      </div>
    {/if}

    {#if failed}
      <p class="retry">
        Earlier samples could not be loaded. Live observations are still shown.
        <button type="button" class="text" onclick={() => (attempt += 1)}>Retry history</button>
      </p>
    {/if}
  </div>
</ChartPanel>

<style>
  .controls {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-3);
  }
  .map {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-4);
    min-width: 0;
  }
  .toolbar {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-3);
  }

  /* -- measurements: a row of chips, each carrying its stroke ---------------- */
  .metrics {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--z-space-2);
  }
  .divider {
    width: var(--z-border-width);
    height: var(--z-space-4);
    background: var(--z-border-strong);
    margin: 0 var(--z-space-1);
  }
  .metric {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    padding: var(--z-space-1) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-full);
    background: var(--z-surface);
    color: var(--z-text-muted);
    font: inherit;
    font-size: var(--z-text-xs);
    cursor: pointer;
    transition:
      background var(--z-motion-fast) var(--z-ease),
      color var(--z-motion-fast) var(--z-ease),
      border-color var(--z-motion-fast) var(--z-ease);
  }
  .metric:hover {
    border-color: var(--z-border-strong);
    color: var(--z-text);
  }
  .metric[aria-pressed='true'] {
    background: var(--z-accent-subtle);
    border-color: var(--z-accent-border);
    color: var(--z-text);
    font-weight: var(--z-weight-medium);
  }
  .stroke {
    width: 24px;
    height: 8px;
    flex: none;
  }
  .stroke line {
    stroke: currentColor;
    stroke-width: 2;
  }

  /* -- the headline: what the chart says, in a sentence ----------------------- */
  .headline {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-5);
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
  .headline em {
    font-style: normal;
    color: var(--z-text);
  }

  /* -- the per-host stack ------------------------------------------------------ */
  .panes {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-2);
  }
  .pane {
    display: flex;
    flex-direction: column;
    gap: var(--z-space-1);
  }
  .pane + .pane {
    border-top: var(--z-border-width) solid var(--z-border);
    padding-top: var(--z-space-2);
  }
  .empty {
    margin: 0;
    padding: var(--z-space-8) 0;
    text-align: center;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }

  /* -- the reading beside the crosshair --------------------------------------- */
  .reading {
    min-width: 12rem;
    max-width: 24rem;
    padding: var(--z-space-2) var(--z-space-3);
    border: var(--z-border-width) solid var(--z-border);
    border-radius: var(--z-radius-md);
    background: var(--z-surface-raised);
    box-shadow: var(--z-shadow-md);
    font-size: var(--z-text-xs);
    line-height: var(--z-leading-xs);
  }
  .reading p {
    margin: 0;
  }
  .when {
    display: flex;
    justify-content: space-between;
    gap: var(--z-space-3);
    padding-bottom: var(--z-space-1);
    margin-bottom: var(--z-space-1);
    border-bottom: var(--z-border-width) solid var(--z-border);
    color: var(--z-text-subtle);
    font-variant-numeric: tabular-nums;
  }
  .when strong {
    color: var(--z-text);
    font-weight: var(--z-weight-semibold);
  }
  .reading table {
    border-collapse: collapse;
    width: 100%;
  }
  .reading th,
  .reading td {
    padding: var(--z-nudge-2) 0 var(--z-nudge-2) var(--z-space-3);
    text-align: right;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .reading thead th {
    color: var(--z-text-subtle);
    font-size: var(--z-text-2xs);
    font-weight: var(--z-weight-medium);
    letter-spacing: var(--z-tracking-wide);
    text-transform: uppercase;
  }
  /*
    The name column takes whatever the figures do not want, and clips.

    It used to be a flex container with a 14rem name in it, which took the
    cell out of the table's layout: on a phone a host called
    zoomies-8vcpu-31gb-ubuntu-2404-ollama1 held the column open at its full
    width and pushed the measurements off the right of the card, so the
    figures the reading exists for were the part that went. `width: 100%` with
    `max-width: 0` is the table's way of saying "absorb the rest of the row",
    and it is what lets the ellipsis inside happen at all.
  */
  .reading th[scope='row'] {
    width: 100%;
    max-width: 0;
    padding-left: 0;
    font-weight: var(--z-weight-medium);
    text-align: left;
  }
  .reading th[scope='row'] .who {
    display: flex;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
  }
  .reading th[scope='row'] .name {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .reading td {
    font-weight: var(--z-weight-semibold);
  }
  .reading td.pressed {
    color: var(--z-pending);
  }
  .reading tr.lit th,
  .reading tr.lit td {
    background: var(--z-surface-sunken);
  }
  .more {
    margin-top: var(--z-space-1);
    color: var(--z-text-subtle);
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }

  .swatch {
    display: inline-block;
    width: 10px;
    height: 10px;
    border-radius: var(--z-radius-sm);
    flex: none;
  }
  .gap {
    color: var(--z-text-subtle);
  }

  /* -- inspecting by keyboard ------------------------------------------------ */
  .scrub {
    display: flex;
    align-items: center;
    gap: var(--z-space-4);
    flex-wrap: wrap;
  }
  .scrub label {
    display: flex;
    align-items: center;
    gap: var(--z-space-3);
    flex: 1;
    font-size: var(--z-text-xs);
    min-width: 0;
  }
  .scrub label span {
    white-space: nowrap;
    color: var(--z-text-muted);
  }
  /* The track is painted by the input itself, as the fleet's sliders paint
     theirs: the share behind the thumb in the accent, the rest sunken. */
  .scrub input {
    -webkit-appearance: none;
    appearance: none;
    width: 100%;
    min-width: 70px;
    height: var(--z-space-6);
    margin: 0;
    background: transparent;
    cursor: pointer;
  }
  .scrub input::-webkit-slider-runnable-track {
    height: var(--z-nudge-2);
    border-radius: var(--z-radius-full);
    background: var(--z-border-strong);
  }
  .scrub input::-moz-range-track {
    height: var(--z-nudge-2);
    border-radius: var(--z-radius-full);
    background: var(--z-border-strong);
  }
  .scrub input::-webkit-slider-thumb {
    -webkit-appearance: none;
    appearance: none;
    width: var(--z-space-4);
    height: var(--z-space-4);
    margin-top: calc((var(--z-nudge-2) - var(--z-space-4)) / 2);
    border: var(--z-border-width-thick) solid var(--z-surface);
    border-radius: var(--z-radius-full);
    background: var(--z-accent);
    box-shadow: var(--z-shadow-md);
  }
  .scrub input::-moz-range-thumb {
    width: var(--z-space-4);
    height: var(--z-space-4);
    border: var(--z-border-width-thick) solid var(--z-surface);
    border-radius: var(--z-radius-full);
    background: var(--z-accent);
    box-shadow: var(--z-shadow-md);
  }
  .scrub input:focus-visible {
    outline: none;
  }
  .scrub input:focus-visible::-webkit-slider-thumb {
    box-shadow: var(--z-focus-ring);
  }
  .scrub input:focus-visible::-moz-range-thumb {
    box-shadow: var(--z-focus-ring);
  }
  output {
    min-width: 7ch;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  output .now {
    margin-left: var(--z-space-1);
    color: var(--z-text-subtle);
    font-weight: var(--z-weight-normal);
  }

  /* -- the hosts: one row each, the legend, the switchboard and the reading -- */
  .hosts {
    display: grid;
    gap: var(--z-space-1);
    border-top: var(--z-border-width) solid var(--z-border);
    padding-top: var(--z-space-3);
  }
  .host {
    display: grid;
    grid-template-columns: minmax(10rem, 1fr) auto auto;
    align-items: center;
    gap: var(--z-space-2) var(--z-space-4);
    padding: var(--z-space-1) var(--z-space-2);
    border-radius: var(--z-radius-sm);
    font-size: var(--z-text-xs);
    transition: background var(--z-motion-fast) var(--z-ease);
  }
  .host:hover,
  .host.lit {
    background: var(--z-surface-sunken);
  }
  .host.off .meters,
  .host.off .name {
    opacity: 0.5;
  }
  .host.off .swatch {
    background: transparent !important;
    box-shadow: inset 0 0 0 var(--z-border-width) var(--z-border-strong);
  }
  .toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    min-width: 0;
    padding: var(--z-space-1) var(--z-space-2);
    margin-left: calc(-1 * var(--z-space-2));
    border: 0;
    border-radius: var(--z-radius-sm);
    background: none;
    color: var(--z-text);
    font: inherit;
    font-size: var(--z-text-xs);
    font-weight: var(--z-weight-medium);
    text-align: left;
    cursor: pointer;
  }
  .toggle .name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .meters {
    display: flex;
    flex-wrap: wrap;
    gap: var(--z-space-1) var(--z-space-4);
    justify-content: flex-end;
  }
  .meter {
    display: inline-flex;
    align-items: center;
    gap: var(--z-space-2);
    white-space: nowrap;
  }
  .meter .key {
    color: var(--z-text-muted);
    min-width: 3.5em;
  }
  .meter strong {
    min-width: 3.5ch;
    text-align: right;
    font-weight: var(--z-weight-semibold);
    font-variant-numeric: tabular-nums;
  }
  /* The meter: a track the width of a short word, with the pressure line
     marked on it where it is marked on the chart. */
  .track {
    position: relative;
    width: 64px;
    height: calc(var(--z-space-2) - var(--z-nudge-2));
    border-radius: var(--z-radius-full);
    background: var(--z-surface-sunken);
    box-shadow: inset 0 0 0 var(--z-border-width) var(--z-border);
    overflow: hidden;
  }
  .track::after {
    content: '';
    position: absolute;
    top: 0;
    bottom: 0;
    left: 85%;
    width: var(--z-border-width);
    background: var(--z-pending-border);
  }
  .fill {
    display: block;
    height: 100%;
    border-radius: var(--z-radius-full);
    transition: width var(--z-motion-base) var(--z-ease);
  }
  .fill.promised {
    opacity: 0.55;
  }
  .host-actions {
    display: flex;
    gap: var(--z-space-3);
    white-space: nowrap;
  }
  .host-actions a {
    color: var(--z-accent);
  }
  .text {
    padding: 0;
    border: 0;
    background: none;
    color: var(--z-accent);
    font: inherit;
    font-size: var(--z-text-xs);
    cursor: pointer;
  }
  .text:hover {
    text-decoration: underline;
  }
  .retry {
    margin: 0;
    font-size: var(--z-text-xs);
    color: var(--z-text-muted);
  }
  @media (max-width: 768px) {
    .host {
      grid-template-columns: 1fr;
    }
    .meters {
      justify-content: flex-start;
    }
    .reading {
      max-width: none;
    }
  }
  /* A finger is wider than a pointer. Every target on the map -- the chips,
     the hosts' switches, Only and Manage, the timeline's thumb -- rises to
     the touch size where the pointer is coarse, and nowhere else: the desktop
     rows stay dense, since a mouse is fine with a row of text. */
  @media (pointer: coarse) {
    .metric,
    .toggle,
    .text,
    .host-actions a {
      min-height: var(--z-control-touch);
    }
    .text,
    .host-actions a {
      display: inline-flex;
      align-items: center;
      padding: 0 var(--z-space-2);
    }
    .scrub input {
      height: var(--z-control-touch);
    }
    .host {
      padding-block: 0;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .fill {
      transition: none;
    }
  }
</style>
