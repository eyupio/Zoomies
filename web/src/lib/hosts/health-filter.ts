/**
 * Which hosts the Hosts page's filter row holds, and how many there are in each.
 *
 * Route-only, and a module of its own rather than more of health.ts: that file
 * is in the app shell (the feed imports from it), so anything added there spends
 * shell bytes that only this page would use.
 *
 * The pill on each card is the one authority on a host's health, and the filter
 * asks it. `healthBucket` calls `healthSummary` exactly as the card does, so the
 * count on the "Need attention" tile, the chip and the pills can never disagree.
 * Three things follow from that, and each is said where it shows:
 *
 *  - The three buckets are exclusive and do not add up to All. A host with a
 *    clean report, a partial report or "Checks unavailable" is in none of them,
 *    because its pill says nothing is wrong and nothing is missing.
 *  - A host whose agent is not connected is "Report stale" even with a fresh
 *    report, because its pill reads "Last known report". A stale host that is
 *    also waiting for a reboot is stale rather than attention, because its pill
 *    is neutral and never says "reboot pending".
 *  - The page goes stale at three minutes, where the controller's own
 *    `host.health_stale` problem waits ten, so a count here can be higher than
 *    the problems list for a few minutes. The pill is what the card shows, and
 *    the count follows the card.
 */
import type { Host } from '$lib/api/types';
import { pluralise } from '../format';
import { healthSummary } from './health';

export const HEALTH_FILTERS = [
  { value: 'all', label: 'All', title: 'Every host.', note: '' },
  {
    value: 'attention',
    label: 'Need attention',
    title:
      'OS settings below the recommendation, or a reboot pending. The same hosts whose health pill says warnings, errors or a reboot.',
    note: 'OS settings below the recommendation, or a reboot pending.',
  },
  {
    value: 'stale',
    label: 'Report stale',
    title:
      'No OS report in the last three minutes, or the agent is not connected. What the host last reported may still be true.',
    note: 'no OS report in the last three minutes, or the agent is not connected.',
  },
  {
    value: 'no-report',
    label: 'No report',
    title: 'No OS report has arrived from this host’s agent yet.',
    note: 'no OS report has arrived from the agent yet.',
  },
] as const;

export type HealthFilter = (typeof HEALTH_FILTERS)[number]['value'];
export type HealthBucket = Exclude<HealthFilter, 'all'>;
export type HealthCounts = Record<HealthFilter, number>;

/** What the pill reads, and nothing else of the host. */
type Judged = Pick<Host, 'doctor' | 'healthy'>;

/**
 * The filter a query value names. Exact matches only: an address somebody
 * typed or an older bookmark that names nothing here shows every host, which is
 * what no filter means, rather than an empty page that looks like good news.
 */
export function healthFilterFrom(raw: string): HealthFilter {
  return HEALTH_FILTERS.find((option) => option.value === raw)?.value ?? 'all';
}

/**
 * The one bucket a host is in, or null when its pill has nothing to say about
 * it: Health OK, Partial report and Checks unavailable.
 */
export function healthBucket(host: Judged, now: number): HealthBucket | null {
  // "Health unavailable": no report, or one that arrived without the
  // controller's count of it. Asked first, because healthSummary also calls a
  // missing report stale and would put it in the wrong bucket.
  if (!host.doctor?.summary) return 'no-report';
  const { stale, tone } = healthSummary(host.doctor, now, host.healthy);
  // Unreachable, an unreadable time, or older than three minutes.
  if (stale) return 'stale';
  return tone === 'danger' || tone === 'pending' ? 'attention' : null;
}

export function healthCounts(hosts: readonly Judged[], now: number): HealthCounts {
  const counts: HealthCounts = { all: hosts.length, attention: 0, stale: 0, 'no-report': 0 };
  for (const host of hosts) {
    const bucket = healthBucket(host, now);
    if (bucket) counts[bucket] += 1;
  }
  return counts;
}

/** The hosts a filter shows. With All it is the same array, so nothing downstream re-renders for it. */
export function hostsFor<T extends Judged>(
  hosts: readonly T[],
  filter: HealthFilter,
  now: number,
): readonly T[] {
  if (filter === 'all') return hosts;
  return hosts.filter((host) => healthBucket(host, now) === filter);
}

/** The line under the chips while a filter is on, or nothing for All. Written for a screen reader as much as for the eye. */
export function filterSentence(filter: HealthFilter, shown: number, total: number): string {
  const note = HEALTH_FILTERS.find((option) => option.value === filter)?.note ?? '';
  return note === '' ? '' : `Showing ${shown} of ${pluralise(total, 'host')}: ${note}`;
}

/**
 * What the "Need attention" tile says under its number.
 *
 * Zero beside hosts that have said nothing is not a clean bill of health, and a
 * tile that read "0" with the usual line under it would be the false calm the
 * filter exists to avoid. So at zero it says how many have no current report.
 */
export function attentionDetail(counts: HealthCounts): string {
  const silent = counts.stale + counts['no-report'];
  if (counts.attention === 0 && silent > 0) return `${silent} without a current OS report`;
  return 'OS settings below the recommendation, or a reboot pending';
}

/** What the page says when a filter matches nothing. */
export function emptyCopy(
  filter: HealthBucket,
  counts: HealthCounts,
): { title: string; description: string } {
  const silent = counts.stale + counts['no-report'];
  const has = (n: number) => (n === 1 ? 'has' : 'have');
  if (filter === 'attention') {
    return {
      title: 'No host needs attention',
      description:
        'Every host with a current report is within what Zoomies recommends, and none is waiting for a reboot.' +
        (silent > 0
          ? ` ${pluralise(silent, 'host')} ${has(silent)} no current report, so nothing is known about ${silent === 1 ? 'it' : 'them'} yet.`
          : ''),
    };
  }
  if (filter === 'stale') {
    const none = counts['no-report'];
    return {
      title: 'No stale reports',
      description:
        'Every OS report that has arrived is less than three minutes old, and every host that sent one is connected.' +
        (none > 0 ? ` ${pluralise(none, 'host')} ${has(none)} not reported at all.` : ''),
    };
  }
  return {
    title: 'Every host has reported',
    description:
      'Each agent has sent at least one OS report. A host that never does needs its health service checking or its agent updating.',
  };
}
