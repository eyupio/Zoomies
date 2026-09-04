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
  import { formatDuration, formatNumber, toMillis } from '$lib/format';
  import MetricTile from '$lib/components/MetricTile.svelte';

  interface Props {
    /** True until the first authoritative fetch has landed. */
    loading?: boolean;
    class?: string;
  }

  let { loading = false, class: className = '' }: Props = $props();

  /** One minute of fleet history, in the shape the tiles plot. */
  interface Point {
    at: number;
    queued: number;
    running: number;
    live: number;
  }

  const WINDOW = '1h';
  const WINDOW_MS = 60 * 60 * 1000;
  const MINUTE_MS = 60 * 1000;

  // `$state.raw` because these are replaced wholesale, never mutated in place.
  let points = $state.raw<Point[]>([]);
  let waits = $state.raw<Array<{ at: number; ms: number }>>([]);

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
        live: liveRunners(stats),
      },
      now,
    );
    const wait = stats.median_wait_ms;
    if (typeof wait === 'number') waits = fold(waits, { at: now, ms: wait }, now);
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
  const queued = $derived(stats?.queued_jobs ?? 0);
  const running = $derived(stats?.running_jobs ?? 0);
  const live = $derived(liveRunners(stats));
  // Undefined rather than zero when the controller has nothing to say: a
  // median of "0ms" is a claim, and "--" is the truth.
  const medianWait = $derived(stats?.median_wait_ms);
  const p95Wait = $derived(stats?.p95_wait_ms);

  /** A series is only worth drawing, or comparing against, once it has a shape. */
  function trend(values: readonly number[]): readonly number[] | undefined {
    return values.length > 1 ? values : undefined;
  }
  function delta(values: readonly number[], now: number): number | undefined {
    const first = values[0];
    return values.length > 1 && first !== undefined ? now - first : undefined;
  }

  const queuedSeries = $derived(points.map((p) => p.queued));
  const runningSeries = $derived(points.map((p) => p.running));
  const liveSeries = $derived(points.map((p) => p.live));
  const waitSeries = $derived(waits.map((w) => w.ms));

  const waitDelta = $derived.by(() => {
    const ms = delta(waitSeries, medianWait ?? 0);
    return ms === undefined ? undefined : Math.round(ms / 1000);
  });
</script>

<div class="tiles {className}">
  <MetricTile
    label="Queued jobs"
    value={formatNumber(queued)}
    href="/jobs?state=queued"
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
    href="/jobs?state=in_progress"
    tone="busy"
    delta={delta(runningSeries, running)}
    deltaLabel="in the hour"
    sparkline={trend(runningSeries)}
    {loading}
  />
  <MetricTile
    label="Live runners"
    value={formatNumber(live)}
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
</div>

<style>
  .tiles {
    display: grid;
    gap: var(--z-space-4);
    /* Two up on a tablet, four up once there is room for four. Never three
       with an orphan. */
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  @media (min-width: 1181px) {
    .tiles {
      grid-template-columns: repeat(4, minmax(0, 1fr));
    }
  }
  @media (max-width: 640px) {
    .tiles {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
