<!--
  The four numbers at the top of the Overview: what is waiting, what is running,
  what exists to run it, and how long the queue is making people wait.

  The trend behind each one comes from GET /samples, which the controller writes
  once a minute. The stream keeps it going: every `stats` frame is folded into
  the series under the minute it belongs to, exactly as the server would have
  recorded it, so the line keeps moving without anyone polling anything.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { listSamples } from '$lib/api/client';
  import { events } from '$lib/api/sse';
  import type { Stats } from '$lib/api/types';
  import { fleet } from '$lib/state/fleet.svelte';
  import { prefs } from '$lib/state/prefs.svelte';
  import { formatDuration, formatNumber, pluralise, toMillis } from '$lib/format';
  import MetricTile from '$lib/components/MetricTile.svelte';

  interface Props {
    /** True until the first authoritative fetch has landed. */
    loading?: boolean;
    class?: string;
  }

  let { loading = false, class: className = '' }: Props = $props();

  /**
   * One minute of fleet history, in the shape the tiles plot.
   *
   * Both scopes are kept on every point rather than one being refetched when
   * the toggle moves: the series is an hour of minutes, and redrawing it from
   * the server would blank the sparkline for as long as the request takes,
   * every time somebody flips the switch.
   */
  interface Point {
    at: number;
    queued: number;
    running: number;
    fleetQueued: number;
    fleetRunning: number;
    live: number;
  }

  const WINDOW = '1h';
  const WINDOW_MS = 60 * 60 * 1000;
  const MINUTE_MS = 60 * 1000;

  // `$state.raw` because these are replaced wholesale, never mutated in place.
  let points = $state.raw<Point[]>([]);
  let waits = $state.raw<Array<{ at: number; ms: number; fleetMs: number }>>([]);

  /** Runners that exist or are on their way, which is what a sample counts. */
  function liveRunners(stats: Stats | null | undefined): number {
    const r = stats?.runners;
    if (!r) return 0;
    return (
      (r.provisioning ?? 0) +
      (r.registering ?? 0) +
      (r.idle ?? 0) +
      (r.busy ?? 0) +
      (r.draining ?? 0)
    );
  }

  /** Keep one point per minute, and only the last hour of them. */
  function fold<T extends { at: number }>(series: readonly T[], point: T, now: number): T[] {
    const next = series.slice();
    const last = next[next.length - 1];
    if (last && Math.floor(last.at / MINUTE_MS) === Math.floor(point.at / MINUTE_MS)) {
      next[next.length - 1] = point;
    } else {
      next.push(point);
    }
    const cutoff = now - WINDOW_MS;
    return next.filter((p) => p.at >= cutoff);
  }

  function record(stats: Stats): void {
    const now = Date.now();
    points = fold(
      points,
      {
        at: now,
        queued: stats.queued_jobs ?? 0,
        running: stats.running_jobs ?? 0,
        fleetQueued: stats.fleet?.queued_jobs ?? 0,
        fleetRunning: stats.fleet?.running_jobs ?? 0,
        live: liveRunners(stats),
      },
      now,
    );
    const wait = stats.median_wait_ms;
    const fleetWait = stats.fleet?.median_wait_ms;
    if (typeof wait === 'number') {
      waits = fold(waits, { at: now, ms: wait, fleetMs: fleetWait ?? 0 }, now);
    }
  }

  async function loadSamples(signal?: AbortSignal): Promise<void> {
    try {
      const page = await listSamples({ window: WINDOW }, signal);
      const cutoff = Date.now() - WINDOW_MS;
      const history = (page.items ?? [])
        .map((s) => ({
          at: toMillis(s.at) ?? 0,
          queued: s.queued_jobs ?? 0,
          running: s.running_jobs ?? 0,
          // Samples recorded before the fleet's own figures existed carry 0,
          // which is honest: nothing here can say what this fleet's queue was
          // at a minute that has already passed.
          fleetQueued: s.fleet_queued_jobs ?? 0,
          fleetRunning: s.fleet_running_jobs ?? 0,
          live: s.total_runners ?? 0,
        }))
        .filter((p) => p.at >= cutoff)
        .sort((a, b) => a.at - b.at);
      // Anything the stream has already delivered is newer than the fetch, so
      // it wins on any minute the two disagree about.
      const newest = Math.floor((history[history.length - 1]?.at ?? 0) / MINUTE_MS);
      const live = points.filter((p) => Math.floor(p.at / MINUTE_MS) > newest);
      points = [...history, ...live];
    } catch {
      // A missing trend is not worth an error state: the numbers themselves
      // come from the fleet cache and are still true. The tiles simply lose
      // their sparklines until the next attempt.
    }
  }

  // First load, and the seed for the wait series, which /samples does not carry.
  $effect(() => {
    const controller = new AbortController();
    untrack(() => {
      const current = fleet.stats;
      if (current) record(current);
    });
    void loadSamples(controller.signal);
    return () => controller.abort();
  });

  // Every reconnect ends with one reconciling fetch, for the same reason the
  // fleet cache does one: the replay buffer is finite.
  let previous: string | null = null;
  $effect(() => {
    const status = fleet.connection;
    if (previous !== null && previous !== 'live' && status === 'live') void loadSamples();
    previous = status;
  });

  $effect(() => events.subscribe('stats', (stats) => record(stats)));

  const stats = $derived(fleet.stats);

  /**
   * Whether the tiles are counting jobs this fleet had no hand in.
   *
   * GitHub reports every job in an installed repository, so on an organisation
   * that also uses hosted runners the unscoped queue depth is mostly somebody
   * else's -- and "why is my fleet slow?" is then answered with a number
   * nobody here can act on. Off by default, and shared with the panels below,
   * so the tiles and the lists under them always agree about what they mean.
   */
  const others = $derived(prefs.otherRunners);

  const queued = $derived(others ? (stats?.queued_jobs ?? 0) : (stats?.fleet?.queued_jobs ?? 0));
  const running = $derived(others ? (stats?.running_jobs ?? 0) : (stats?.fleet?.running_jobs ?? 0));
  const live = $derived(liveRunners(stats));

  /** The Jobs page, filtered to match whatever these tiles are counting. */
  function jobsHref(state: string): string {
    return `/jobs?state=${state}` + (others ? '&all=true' : '');
  }

  /**
   * Where the queued tile goes.
   *
   * The Queue, whose default view is this fleet's queued work minus whatever an
   * operator removed from it -- exactly the set the tile counts, so the number
   * and the list behind it cannot disagree. The Jobs page cannot be that list:
   * it is history, it has no provisioning filter, and a job removed from the
   * queue sits in it looking like any other queued job.
   *
   * With other runners switched on the tile is counting queues this fleet does
   * not own, which the Queue page has nothing to say about; that is the Jobs
   * page's `all` view, and the question has changed from "what is waiting on
   * me?" to "what is waiting anywhere?".
   */
  const queuedHref = $derived(others ? jobsHref('queued') : '/queue');
  // Undefined rather than zero when the controller has nothing to say: a
  // median of "0ms" is a claim, and "--" is the truth.
  /**
   * Where the capacity actually is.
   *
   * Four tiles about work and none about capacity leaves the operator's real
   * question -- "has this thing got anywhere to run a job yet?" -- unanswerable
   * without a trip to Hosts, and it is the number that explains a queue that is
   * not moving. `/stats` already carries it and the page already fetches it.
   */
  const capacityHint = $derived.by(() => {
    const hosts = stats?.hosts;
    if (!hosts) return undefined;
    if ((hosts.total ?? 0) === 0) return 'No hosts connected';
    return `${hosts.used ?? 0} of ${hosts.capacity ?? 0} slots on ${pluralise(hosts.healthy ?? 0, 'healthy host')}`;
  });

  /**
   * A percentile the controller reports as zero is one nothing was measured for.
   *
   * The payload carries no sample count, and a real median of exactly 0ms does
   * not happen -- a webhook has to arrive and a container has to start -- so a
   * zero is the empty window speaking. The tiles used to print it as "0ms" and
   * "p95 0ms" for a fleet that had never queued anything, which is the claim
   * the comment above says they must not make.
   */
  const measured = (ms: number | undefined): number | undefined => (ms ? ms : undefined);

  const medianWait = $derived(
    measured(others ? stats?.median_wait_ms : stats?.fleet?.median_wait_ms),
  );
  const p95Wait = $derived(measured(others ? stats?.p95_wait_ms : stats?.fleet?.p95_wait_ms));
  const p50Startup = $derived(measured(stats?.p50_startup_ms));
  const p95Startup = $derived(measured(stats?.p95_startup_ms));
  const p50Registration = $derived(measured(stats?.p50_registration_ms));
  const p95Registration = $derived(measured(stats?.p95_registration_ms));

  /** A series is only worth drawing, or comparing against, once it has a shape. */
  function trend(values: readonly number[]): readonly number[] | undefined {
    return values.length > 1 ? values : undefined;
  }
  function delta(values: readonly number[], now: number): number | undefined {
    const first = values[0];
    return values.length > 1 && first !== undefined ? now - first : undefined;
  }

  const queuedSeries = $derived(points.map((p) => (others ? p.queued : p.fleetQueued)));
  const runningSeries = $derived(points.map((p) => (others ? p.running : p.fleetRunning)));
  const liveSeries = $derived(points.map((p) => p.live));
  const waitSeries = $derived(waits.map((w) => (others ? w.ms : w.fleetMs)));

  const waitDelta = $derived.by(() => {
    const ms = delta(waitSeries, medianWait ?? 0);
    return ms === undefined ? undefined : Math.round(ms / 1000);
  });
</script>

<div class="tiles {className}">
  <MetricTile
    label="Queued jobs"
    value={formatNumber(queued)}
    href={queuedHref}
    tone="pending"
    goodWhen="down"
    delta={delta(queuedSeries, queued)}
    deltaLabel="in the hour"
    sparkline={trend(queuedSeries)}
    {loading}
  />
  <MetricTile
    label="Running jobs"
    value={formatNumber(running)}
    href={jobsHref('in_progress')}
    tone="busy"
    delta={delta(runningSeries, running)}
    deltaLabel="in the hour"
    sparkline={trend(runningSeries)}
    {loading}
  />
  <MetricTile
    label="Live runners"
    value={formatNumber(live)}
    hint={capacityHint}
    href="/runners"
    tone="idle"
    delta={delta(liveSeries, live)}
    deltaLabel="in the hour"
    sparkline={trend(liveSeries)}
    {loading}
  />
  <MetricTile
    label="Median queue wait"
    value={formatDuration(medianWait)}
    href="/jobs"
    tone="pending"
    goodWhen="down"
    delta={waitDelta}
    deltaLabel="s in the hour"
    sparkline={trend(waitSeries)}
    hint={p95Wait === undefined ? undefined : `p95 ${formatDuration(p95Wait)}`}
    {loading}
  />
  <MetricTile
    label="Runner startup p50"
    value={formatDuration(p50Startup)}
    hint={p95Startup === undefined ? undefined : `p95 ${formatDuration(p95Startup)}`}
    href="/runners"
    tone="pending"
    goodWhen="down"
    {loading}
  />
  <MetricTile
    label="Registration p50"
    value={formatDuration(p50Registration)}
    hint={p95Registration === undefined ? undefined : `p95 ${formatDuration(p95Registration)}`}
    href="/runners"
    tone="pending"
    goodWhen="down"
    {loading}
  />
</div>

<style>
  .tiles {
    display: grid;
    gap: var(--z-space-4);
    /* Six tiles, so the column count is a divisor of six at every width: three
       up with room, two on a tablet, one on a phone. Four columns left the
       second row two-thirds empty, which is what the shipped screenshot
       showed. */
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  /* The sidebar breakpoint from the other side: a media query cannot say
     "above 1180" without naming the next pixel. */
  @media (min-width: 1181px) {
    .tiles {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }
  }
  @media (max-width: 768px) {
    .tiles {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
