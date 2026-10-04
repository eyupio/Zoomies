import type { FleetSample, Host, Pool, Stats } from '../api/types';
import { slotsOf } from '../hosts/slots';
import { seriesLine, type Series, type SeriesLine, type SeriesPoint } from './plot';

export function finite(value: number | undefined): number | null {
  return value !== undefined && Number.isFinite(value) ? Math.max(0, value) : null;
}
export function percent(used: number | null, total: number | null): number | null {
  return used !== null && total !== null && total > 0 ? (100 * used) / total : null;
}

/**
 * Whether a queue depth is worth its tile's warning colour, against
 * ui.queue_warning_threshold. The setting defaults to 1, which is the fixed
 * "anything queued at all" behaviour this replaces; a fleet whose queue
 * normally sits above zero can raise it so the colour still means something.
 */
export function queueTone(
  value: number | null | undefined,
  threshold: number | undefined,
): 'warning' | 'neutral' {
  return (value ?? 0) >= (threshold ?? 1) ? 'warning' : 'neutral';
}

/** Read controller aggregates, never the paginated runner cache. */
export function poolSignals(pools: readonly Pool[], stats: Stats | null) {
  return pools.map((pool) => {
    const latest = stats?.pools?.find((p) => p.pool_id === pool.id);
    const queued = finite(latest?.queued ?? pool.queued_jobs);
    const live = finite(latest?.live ?? pool.counts?.live);
    const max = finite(latest?.max ?? pool.max_runners);
    return {
      pool,
      queued,
      live,
      max,
      busy: finite(latest?.busy ?? pool.counts?.busy),
      idle: finite(latest?.idle ?? pool.counts?.idle),
      atCeiling:
        pool.enabled === true &&
        queued !== null &&
        queued > 0 &&
        live !== null &&
        max !== null &&
        max > 0 &&
        live >= max,
      headroom: live !== null && max !== null ? Math.max(0, max - live) : null,
    };
  });
}

/** Slot headroom is not a promise that a particular runner fits. */
export function hostSignals(host: Host) {
  const capacity = finite(slotsOf(host));
  const used = finite(host.active_runners);
  const free =
    finite(host.free) ?? (capacity !== null && used !== null ? Math.max(0, capacity - used) : null);
  const eligible = host.healthy === true && host.cordoned !== true && host.incompatible !== true;
  const state = host.incompatible
    ? 'Incompatible'
    : host.healthy === false
      ? 'Offline'
      : host.cordoned
        ? 'Cordoned'
        : host.healthy !== true
          ? 'Unknown'
          : free === 0
            ? 'No free slots'
            : 'Available slots';
  return {
    capacity,
    used,
    free,
    eligible,
    state,
    cpu:
      host.resources_known === true && host.reserved_known === true
        ? percent(finite(host.reserved_cpus), finite(host.allocatable_cpus))
        : null,
    memory:
      host.resources_known === true && host.reserved_known === true
        ? percent(finite(host.reserved_memory_mb), finite(host.allocatable_memory_mb))
        : null,
    diskFree:
      host.resources_known === true && (host.disk_total_mb ?? 0) > 0
        ? percent(finite(host.disk_free_mb), finite(host.disk_total_mb))
        : null,
  };
}

/** Fill missing minutes with null; never draw an outage as zero demand. */
export function minuteSeries(
  points: readonly SeriesPoint[],
  now: number,
  minutes = 60,
): SeriesPoint[] {
  const end = Math.floor(now / 60_000) * 60_000;
  const byMinute: Record<number, number | null> = {};
  for (const p of points)
    if (Number.isFinite(p.at)) byMinute[Math.floor(p.at / 60_000) * 60_000] = p.value;
  return Array.from({ length: minutes }, (_, i) => {
    const at = end - (minutes - 1 - i) * 60_000;
    return { at, value: byMinute[at] ?? null };
  });
}

/** Keep one sample per minute. Later inputs win, so streamed data beats a fetch. */
export function mergeSamples(
  history: readonly FleetSample[],
  live: readonly FleetSample[],
  now: number,
  minutes = 60,
): FleetSample[] {
  const byMinute: Record<number, FleetSample> = {};
  const end = Math.floor(now / 60_000) * 60_000;
  for (const point of [...history, ...live]) {
    const at = Math.floor(new Date(point.at ?? '').getTime() / 60_000) * 60_000;
    if (Number.isFinite(at) && at >= end - (minutes - 1) * 60_000 && at <= end)
      byMinute[at] = point;
  }
  return Object.entries(byMinute)
    .sort(([a], [b]) => Number(a) - Number(b))
    .map(([, point]) => point);
}

/**
 * A minute series folded into wider intervals, each carrying the peak of its
 * minutes. Peaks rather than means because a queue that hit twelve for two
 * minutes is what an operator looking at a day wants to see, and a mean of
 * fifteen minutes would round it away. An interval with no observed minute
 * stays a gap.
 */
export function foldMinutes(points: readonly SeriesPoint[], step: number): SeriesPoint[] {
  if (step <= 1) return [...points];
  const out: SeriesPoint[] = [];
  for (let i = 0; i < points.length; i += step) {
    const slice = points.slice(i, i + step);
    const known = slice.map((p) => p.value).filter((v): v is number => v !== null);
    out.push({
      at: slice[0]?.at ?? 0,
      value: known.length ? Math.max(...known) : null,
    });
  }
  return out;
}

/* -- what the fleet trend draws ------------------------------------------- */

export type SignalKey = 'queue' | 'running' | 'idle' | 'busy' | 'live';

/**
 * A fleet figure. Its colour is the status colour the fleet already uses for
 * that state everywhere else, so an operator who has learnt that amber is
 * pending and teal is busy reads this chart without a legend, and its stroke
 * says whether the figure counts jobs or runners: the two busy figures --
 * the jobs running and the runners running them -- are one colour and can be
 * read against each other.
 */
export interface Signal extends Series {
  key: SignalKey;
  /** Jobs come from GitHub; runners are ours. It is what the stroke says. */
  jobs: boolean;
}

/** In the order the chips appear: the demand first, then what is meeting it. */
export const SIGNALS: readonly Signal[] = [
  {
    key: 'queue',
    label: 'Queued jobs',
    hint: 'jobs waiting for a runner',
    tone: 'var(--z-pending)',
    dash: '',
    jobs: true,
  },
  {
    key: 'running',
    label: 'Running jobs',
    hint: 'jobs a runner has picked up',
    tone: 'var(--z-busy)',
    dash: '8 4',
    jobs: true,
  },
  {
    key: 'idle',
    label: 'Idle runners',
    hint: 'runners registered and waiting for work',
    tone: 'var(--z-idle)',
    dash: '',
    jobs: false,
  },
  {
    key: 'busy',
    label: 'Busy runners',
    hint: 'runners executing a job',
    tone: 'var(--z-busy)',
    dash: '',
    jobs: false,
  },
  {
    key: 'live',
    label: 'Live runners',
    hint: 'every runner short of removed: provisioning, registering, idle, busy, draining',
    tone: 'var(--z-neutral)',
    dash: '2 4',
    jobs: false,
  },
];

// Demand and the two halves of the answer to it. Queued against idle is the
// question the page is opened with -- is anything waiting, and was there
// anything free -- and running is what says whether the fleet is working or
// merely awake.
export const DEFAULT_SIGNALS: readonly SignalKey[] = ['queue', 'running', 'idle'];

/**
 * One sample's figure for a signal. `others` chooses between the jobs the
 * whole organisation reported and the ones this fleet is responsible for;
 * runner counts are always ours, because nobody else's runners are visible
 * from here.
 */
export function signalValue(sample: FleetSample, key: SignalKey, others: boolean): number | null {
  switch (key) {
    case 'queue':
      return (others ? sample.queued_jobs : sample.fleet_queued_jobs) ?? null;
    case 'running':
      return (others ? sample.running_jobs : sample.fleet_running_jobs) ?? null;
    case 'idle':
      return sample.idle_runners ?? null;
    case 'busy':
      return sample.busy_runners ?? null;
    case 'live':
      return sample.total_runners ?? null;
  }
}

/** The signal the chart leads with: the first chosen one, in the chips' order. */
export function leadSignal(enabled: readonly SignalKey[]): Signal | null {
  return SIGNALS.find((s) => enabled.includes(s.key)) ?? null;
}

/** One signal's line over the window, as the chart receives it. */
export type SignalLine = SeriesLine<Signal>;

/** A line for every chosen signal, in the chips' order. */
export function signalLines(
  samples: readonly FleetSample[],
  enabled: readonly SignalKey[],
  others: boolean,
  now: number,
  minutes: number,
  step: number,
): SignalLine[] {
  return SIGNALS.filter((signal) => enabled.includes(signal.key)).map((signal) =>
    seriesLine(
      signal,
      foldMinutes(
        minuteSeries(
          samples.map((s) => ({
            at: new Date(s.at ?? '').getTime(),
            value: signalValue(s, signal.key, others),
          })),
          now,
          minutes,
        ),
        step,
      ),
    ),
  );
}

/**
 * The stretches in view where jobs were queued with no idle runner to take
 * them. It is this chart's pressure band: the capacity map can draw a line
 * at 85% because every figure on it is a share of a machine, and a count of
 * jobs has no such threshold -- but "something was waiting and nothing was
 * free" is the moment an operator is looking for, and it can be found
 * without knowing what a healthy queue depth is for this fleet.
 *
 * It is judged a minute at a time and then folded, which is why it takes the
 * minute series and the width of a point rather than the drawn lines: a
 * fifteen-minute point carries each figure's peak, and the most idle runners
 * there were at any moment in a quarter of an hour is not an answer to
 * whether anything was free when the queue was deep. Folded the other way
 * round -- was there a minute in this interval when work waited and nothing
 * was free -- the band survives zooming out, which is the zoom an operator
 * asking "did we run short overnight" is on.
 *
 * Contiguous intervals come back as one run, so the chart shades a spell of
 * starvation as one band rather than a picket fence, and an interval nobody
 * sampled ends the run rather than extending it across the unknown.
 */
export function starvedRuns(
  queue: SignalLine | null,
  idle: SignalLine | null,
  step = 1,
): Array<{ from: number; to: number }> {
  if (!queue || !idle) return [];
  const out: Array<{ from: number; to: number }> = [];
  queue.points.forEach((p, minute) => {
    const free = idle.points[minute]?.value;
    if (p.value === null || p.value <= 0 || free === null || free === undefined || free !== 0)
      return;
    const i = Math.floor(minute / Math.max(1, step));
    const open = out[out.length - 1];
    if (open && open.to >= i - 1) open.to = i;
    else out.push({ from: i, to: i });
  });
  return out;
}
