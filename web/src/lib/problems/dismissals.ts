/**
 * The rules for what an operator's dismissals still cover, kept apart from the
 * store that holds them because they are plain enough to test in Node and
 * because the store has three places that must agree on them: what hides a
 * problem, what a sweep may forget, and what is worth sending to the server.
 */
import type { Problem, ProblemDismissal, Severity } from '../api/types';

/** Worst first. The panel, the badge and the summary all read this order. */
export const SEVERITY_ORDER: readonly Severity[] = ['error', 'warning', 'info'];

export function severityOf(problem: Problem): Severity {
  return problem.severity ?? 'info';
}

export function rank(value: Severity): number {
  const i = SEVERITY_ORDER.indexOf(value);
  return i < 0 ? SEVERITY_ORDER.length : i;
}

export interface Dismissal {
  /** The severity that was read. Anything worse comes back. */
  severity: Severity;
  /** When it was dismissed, so the drawer can say how old the decision is. */
  at: string;
  /** Set on a snooze: the moment it expires and the problem becomes active
   * again on its own. Absent on a plain "dismiss until resolved". */
  until?: string;
}

export type Dismissals = Record<string, Dismissal>;

export function expired(record: Dismissal, now: number): boolean {
  return record.until !== undefined && new Date(record.until).getTime() <= now;
}

/** Whether this decision still hides a problem of the given severity. */
export function covers(record: Dismissal | undefined, problem: Problem, now: number): boolean {
  if (!record || expired(record, now)) return false;
  // A warning that has since become an error was never read as an error.
  return rank(severityOf(problem)) >= rank(record.severity);
}

/**
 * The keys a sweep may forget.
 *
 * A plain dismissal is spent the moment the controller stops reporting the
 * problem, so the fault recurring is news again. A snooze is not: the operator
 * chose how long, and a problem that clears for one reconciliation pass and
 * comes back -- a queue that drains and refills, a host that blips -- is the
 * very thing a 24 hour snooze is for. Dropping it on the first quiet pass is
 * what made "snooze for 24 hours" last until the next gap, and a snooze of a
 * whole kind could not outlive the last problem of that kind at all. A snooze
 * ends when its clock does, and not before.
 */
export function spentKeys(
  dismissals: Dismissals,
  reported: ReadonlySet<string>,
  now: number,
): string[] {
  const out: string[] = [];
  for (const [key, record] of Object.entries(dismissals)) {
    if (expired(record, now)) out.push(key);
    else if (record.until === undefined && !reported.has(key)) out.push(key);
  }
  return out;
}

/** What the server holds, in the shape the store keeps. Expired snoozes are
 * left out: the server drops them on read, but a clock that disagrees must not
 * resurrect one. */
export function fromServer(items: readonly ProblemDismissal[], now: number): Dismissals {
  const out: Dismissals = {};
  for (const item of items) {
    const record: Dismissal = {
      severity: item.severity,
      at: item.at ?? new Date(now).toISOString(),
      ...(item.until ? { until: item.until } : {}),
    };
    if (!expired(record, now)) out[item.key] = record;
  }
  return out;
}

export function toServer(key: string, record: Dismissal): ProblemDismissal {
  return {
    key,
    severity: record.severity,
    at: record.at,
    ...(record.until ? { until: record.until } : {}),
  };
}

/**
 * What a browser's older, local-only dismissals add to an account's. Anything
 * the account already holds wins: it was made on purpose, more recently than a
 * copy that has been sitting in a browser, and uploading over it would undo it.
 */
export function carriedOver(
  local: Dismissals,
  remote: Dismissals,
  now: number,
): ProblemDismissal[] {
  return Object.entries(local)
    .filter(([key, record]) => !(key in remote) && !expired(record, now))
    .map(([key, record]) => toServer(key, record));
}
