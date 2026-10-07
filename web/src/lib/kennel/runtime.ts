/**
 * What the fleet knows about one repository, as the sentences and figures the
 * Overview tab shows.
 *
 * Everything here is a function of what `GET /jobs/stats` answered, kept out of
 * the component so that the words an operator reads -- how a window ended, what
 * a success rate counts, what to say of a pool that no longer exists -- are
 * tested once. None of it comes from GitHub: it is the fleet's own record of the
 * jobs it ran.
 */
import type { JobStatsGroup } from '../api/types';
import { formatDuration, formatPercent, NO_VALUE, pluralise } from '../format';

/** The two windows the Overview offers. */
export const OVERVIEW_WINDOWS = [
  { id: '7d', label: '7 days', days: 7 },
  { id: '30d', label: '30 days', days: 30 },
] as const;

export type OverviewWindow = (typeof OVERVIEW_WINDOWS)[number]['id'];

export const DEFAULT_OVERVIEW_WINDOW: OverviewWindow = '7d';

/** `retention.jobs`, as it is out of the box: how long the fleet keeps a job. */
export const DEFAULT_JOB_RETENTION_DAYS = 30;

/** The start of a window of `days` days that ends now. */
export function windowStart(days: number, now: Date): string {
  return new Date(now.getTime() - days * 24 * 60 * 60 * 1000).toISOString();
}

/**
 * What a window cannot promise. The fleet keeps a job for a limited time, so a
 * window as long as that is partial at its oldest edge, and saying nothing would
 * read as a month of jobs when it is less. Empty when the window fits inside what
 * is kept.
 */
export function partialWindowNote(
  days: number,
  retentionDays: number = DEFAULT_JOB_RETENTION_DAYS,
): string {
  if (days < retentionDays) return '';
  return `Only jobs the fleet still holds are counted, and by default that is the last ${retentionDays} days.`;
}

type Outcomes = Pick<
  JobStatsGroup,
  'count' | 'succeeded' | 'failed' | 'cancelled' | 'fleet_failed'
>;

/**
 * How a window's jobs ended, in a sentence. A failure the fleet caused is said
 * apart from one the workflow did, because they are fixed in different places.
 */
export function outcomeText(group: Outcomes | undefined): string {
  if (!group || group.count === 0) return 'No jobs finished';
  const parts: string[] = [];
  if (group.succeeded > 0) parts.push(`${group.succeeded} succeeded`);
  if (group.failed > 0) {
    parts.push(
      group.fleet_failed > 0
        ? `${group.failed} failed, ${group.fleet_failed} of them lost to this fleet`
        : `${group.failed} failed`,
    );
  }
  if (group.cancelled > 0) parts.push(`${group.cancelled} cancelled or skipped`);
  return parts.join(', ');
}

/**
 * Succeeded over everything that finished with a verdict. A job that was
 * cancelled or skipped has none, so it is not counted for or against; and the API
 * sends no rate, so this is the one place it is worked out.
 */
export function successRate(
  group: Pick<JobStatsGroup, 'succeeded' | 'failed'> | undefined,
): number | null {
  if (!group) return null;
  const decided = group.succeeded + group.failed;
  return decided === 0 ? null : group.succeeded / decided;
}

export function successRateText(
  group: Pick<JobStatsGroup, 'succeeded' | 'failed'> | undefined,
): string {
  return formatPercent(successRate(group));
}

/**
 * A median and a 95th percentile as a tile: the median is the figure, and the
 * 95th is the detail beside it. A tile with nothing measured says so in words,
 * and never shows zero, which would read as an instant.
 */
export function percentileTile(p: { p50_ms: number | null; p95_ms: number | null } | undefined): {
  value: string;
  detail: string;
} {
  if (!p || p.p50_ms === null) return { value: NO_VALUE, detail: 'Nothing was measured' };
  return {
    value: formatDuration(p.p50_ms),
    detail:
      p.p95_ms === null ? 'median' : `median, ${formatDuration(p.p95_ms)} at the 95th percentile`,
  };
}

/**
 * What `GET /jobs/stats` calls a group of jobs that never recorded a pool or a
 * host: a job from before hosts were recorded, or one no pool claimed. The API
 * says it in words rather than leaving a null group, which a client would drop.
 */
const UNRECORDED_GROUP = 'unknown';

/** One place the repository's jobs ran: a pool or a host. */
export interface PlaceRow {
  /** The pool or host ID; empty when the job never recorded one. */
  id: string;
  name: string;
  /** Whether the fleet still has it. A pool that was deleted is shown by its ID, not hidden. */
  known: boolean;
  jobs: number;
  /** Of the jobs that finished, 0 to 1. */
  share: number;
}

/**
 * The pools or hosts that ran a repository's jobs, the busiest first.
 *
 * A job's pool and host are IDs, and the fleet may have deleted either since, or
 * never recorded a host for a job that started before hosts were recorded. Those
 * are shown as what they are instead of being dropped, because a table whose rows
 * do not add up to the total is one nobody trusts.
 */
export function placeRows(
  groups: readonly JobStatsGroup[],
  key: 'pool' | 'host',
  nameOf: (id: string) => string | undefined,
): PlaceRow[] {
  const total = groups.reduce((sum, g) => sum + g.count, 0);
  return groups
    .map((g): PlaceRow => {
      const raw = g.keys[key] ?? '';
      // "unknown" is the API's word for no record, and it is not an ID: linking
      // it would open a page for a host that was never there.
      const id = raw === UNRECORDED_GROUP ? '' : raw;
      const name = id ? nameOf(id) : undefined;
      return {
        id,
        name: name ?? (id ? id : 'Not recorded'),
        known: name !== undefined,
        jobs: g.count,
        share: total === 0 ? 0 : g.count / total,
      };
    })
    .sort((a, b) => b.jobs - a.jobs || a.name.localeCompare(b.name));
}

/** "3 jobs are waiting", for the label nobody serves. */
export function waitingText(count: number): string {
  if (count === 0) return 'Nothing is waiting for a label no pool serves.';
  return `${pluralise(count, 'job is', 'jobs are')} waiting for a label no pool serves.`;
}
