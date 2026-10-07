import type { Host } from '$lib/api/types';

/** The sentence around a relative time: the markup puts the time between `lead` and `tail`. */
export interface Freshness {
  lead: string;
  tail: string;
  /** `checked_at` is after the page's clock; print "just now", never "in 5s". */
  future: boolean;
}

/**
 * How old the report on the page is, and what that means, with no number for how
 * often a newer one arrives. The page cannot see the agent's heartbeat interval
 * and the monitor's period is derived rather than measured, so a figure here would
 * be a guess that read as a promise. `stale` comes from `healthSummary`, which owns
 * the threshold, so the header and the pill cannot disagree about it.
 *
 * Null when there is no report to date, which the no-report copy already covers.
 */
export function freshness(i: {
  report?: Host['doctor'];
  healthy: boolean;
  stale: boolean;
  now: number;
}): Freshness | null {
  const { report } = i;
  if (!report || !report.summary) return null;
  const checked = Date.parse(report.checked_at);
  if (!Number.isFinite(checked)) return null;
  const future = checked > i.now;
  if (!i.healthy)
    return {
      lead: 'Last report checked',
      tail: '. The agent is not connected, so nothing newer can arrive until it is.',
      future,
    };
  if (i.stale)
    return {
      lead: 'Last report checked',
      tail: '. The agent is connected but has sent nothing newer.',
      future,
    };
  if (report.container)
    return {
      lead: 'Report checked',
      tail: ', from inside the container. This page updates by itself when a newer one arrives.',
      future,
    };
  return {
    lead: 'Report checked',
    tail: '. This page updates by itself when the agent sends a newer one.',
    future,
  };
}
