/**
 * The words and choices for accepting a host check as deliberate.
 *
 * Route-only on purpose: health.ts is shell code under the app-shell budget, and
 * only the host page needs these sentences. Everything here describes a decision
 * recorded on the controller; none of it changes the host, and none of it implies
 * the controller ran anything there.
 */
import { relativeTime } from '../format';
import type { DoctorResult } from './health';

/**
 * How long an acceptance can run. The longest is a day short of the 365 the
 * controller allows, so that a browser whose clock is a little ahead of the
 * controller's does not turn the longest choice into a refusal.
 */
export const ACCEPT_EXPIRY_CHOICES = [
  { value: '7', label: '7 days' },
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '180', label: '6 months' },
  { value: '364', label: 'A year' },
] as const;

/** An acceptance is a decision to be made again, and a year is the default for that. */
export const ACCEPT_DEFAULT_DAYS = '364';

/** When an acceptance of `days` days ends, if it is made at `now`. */
export function acceptEndsAt(days: number, now: Date): string {
  return new Date(now.getTime() + days * 24 * 60 * 60 * 1000).toISOString();
}

const DAY_MONTH = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' });
const DAY_MONTH_YEAR = new Intl.DateTimeFormat(undefined, {
  day: 'numeric',
  month: 'short',
  year: 'numeric',
});

function dayOf(value: string, withYear: boolean): string {
  const ms = Date.parse(value);
  if (!Number.isFinite(ms)) return 'an unknown date';
  return (withYear ? DAY_MONTH_YEAR : DAY_MONTH).format(ms);
}

/** The end date as the toast and the note both say it: "4 Oct 2027". */
export function untilDay(value: string): string {
  return dayOf(value, true);
}

/** "Accepted by Sam 3d ago, until 4 Oct 2027: “reason”". The reason is a person's, shown as text. */
export function acceptNote(
  accepted: NonNullable<DoctorResult['accepted']>,
  now: number = Date.now(),
): string {
  return `Accepted by ${accepted.by} ${relativeTime(accepted.at, now)}, until ${dayOf(accepted.expires_at, true)}: “${accepted.reason}”`;
}

/** Why a row counts again, or null when no acceptance has ended on it. */
export function endedNote(check: Pick<DoctorResult, 'ended' | 'current'>): string | null {
  const ended = check.ended;
  if (!ended) return null;
  const on = dayOf(ended.at, false);
  switch (ended.why) {
    case 'changed':
      return `Accepted by ${ended.by} on ${on} while it read “${ended.was}”. It now reads “${check.current}”, so it counts again. Accept it again if that is deliberate.`;
    case 'expired':
      return `${ended.by}’s acceptance ended on ${on}, so it counts again.`;
    case 'worse':
      return `${ended.by}’s acceptance ended on ${on} because the check could not run, so it counts again.`;
    default:
      return null;
  }
}

/**
 * A row is accepted, or can be: a counted warning the controller marked
 * acceptable. The mark is the controller's, never the page's, so an unknown id
 * or an error never offers the button.
 */
export function canAccept(check: DoctorResult): boolean {
  return check.acceptable === true && !check.accepted;
}

/** The rows in the order the findings table shows them: unaccepted first, accepted after. */
export function acceptedLast(rows: readonly DoctorResult[]): DoctorResult[] {
  return [...rows].sort((a, b) => Number(Boolean(a.accepted)) - Number(Boolean(b.accepted)));
}

/** The ids of the accepted rows, sorted, for telling an accept or a revoke frame from a heartbeat. */
export function acceptedIds(results: readonly DoctorResult[]): string {
  return results
    .filter((r) => r.accepted)
    .map((r) => r.id)
    .sort()
    .join('\n');
}
