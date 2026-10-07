/**
 * What Kennel Club's pages say, as functions of what the API answered.
 *
 * Kept out of the components so that the sentences an operator reads -- how many
 * of what are open, whether a number is a floor or a total -- are tested once
 * and the same on every page.
 */
import type { KennelCounts, KennelOverview, Severity } from '../api/types';
import { joinWords, pluralise } from '../format';

/** The worst severity that is open, or nothing when nothing is. */
export function worstSeverity(
  counts: Pick<KennelCounts, 'error' | 'warning' | 'info'>,
): Severity | undefined {
  if (counts.error > 0) return 'error';
  if (counts.warning > 0) return 'warning';
  if (counts.info > 0) return 'info';
  return undefined;
}

/**
 * What is open, in the words the rest of the product uses: an info finding is a
 * "note", as it is in the problems drawer.
 */
export function openFindingsText(counts: Pick<KennelCounts, 'error' | 'warning' | 'info'>): string {
  const parts: string[] = [];
  if (counts.error > 0) parts.push(pluralise(counts.error, 'error'));
  if (counts.warning > 0) parts.push(pluralise(counts.warning, 'warning'));
  if (counts.info > 0) parts.push(pluralise(counts.info, 'note'));
  return parts.length > 0 ? joinWords(parts) : 'No open findings';
}

/**
 * Whether the Overview's counts are a floor and not a total. A repository that
 * could not be read in full, or has not been looked at yet, may have findings
 * nobody has seen, and an installation whose reads are not getting through may
 * hold repositories there is no row for at all. The page says "at least" then,
 * because a count that reads as a total is the one thing it must not do.
 */
export function countsAreAFloor(overview: Pick<KennelOverview, 'states' | 'unavailable'>): boolean {
  return (
    overview.states.partial > 0 || overview.states.pending > 0 || overview.unavailable.length > 0
  );
}

/**
 * Why the counts are a floor, said once, above them. Empty when they are not.
 */
export function floorSentence(overview: Pick<KennelOverview, 'states' | 'unavailable'>): string {
  const { partial, pending } = overview.states;
  const parts: string[] = [];
  if (partial > 0) {
    parts.push(
      `${pluralise(partial, 'repository', 'repositories')} ${partial === 1 ? 'is' : 'are'} only partly checked`,
    );
  }
  if (pending > 0) {
    parts.push(
      `${pluralise(pending, 'repository', 'repositories')} ${pending === 1 ? 'has' : 'have'} not been looked at yet`,
    );
  }
  if (overview.unavailable.length > 0) {
    const where = joinWords(overview.unavailable.map((note) => note.target));
    parts.push(
      `reads from ${where} are not getting through, so some repositories may not be listed`,
    );
  }
  return parts.length > 0 ? `These counts are a minimum: ${joinWords(parts)}.` : '';
}

/** Where an administrator turns Kennel Club on, and what the 409 says to do. */
export const KENNEL_SETTING_HREF = '/settings/configuration?setting=kennel.enabled';

/**
 * How the repositories stand for one source of facts, as a sentence: "3 read,
 * 1 not granted". The order is the order of the worst news first, because a
 * panel that opens on "40 read" hides the one that was not.
 */
export const COVERAGE_STATE_ORDER = [
  'error',
  'held',
  'denied',
  'unavailable',
  'partial',
  'not_read',
  'ok',
] as const;

const COVERAGE_WORDS: Record<(typeof COVERAGE_STATE_ORDER)[number], string> = {
  error: 'could not be read',
  held: 'held for a rate limit',
  denied: 'not granted',
  unavailable: 'unavailable',
  partial: 'partly read',
  not_read: 'not read',
  ok: 'read',
};

export function coverageStatesText(states: Readonly<Record<string, number>>): string {
  const parts: string[] = [];
  for (const state of COVERAGE_STATE_ORDER) {
    const n = states[state] ?? 0;
    if (n > 0) parts.push(`${n} ${COVERAGE_WORDS[state]}`);
  }
  return parts.length > 0 ? parts.join(', ') : 'none read yet';
}

/** The state of a source that is worth drawing it by: the worst one any repository is in. */
export function worstCoverageState(
  states: Readonly<Record<string, number>>,
): (typeof COVERAGE_STATE_ORDER)[number] | undefined {
  return COVERAGE_STATE_ORDER.find((state) => (states[state] ?? 0) > 0);
}

/* -- waivers ------------------------------------------------------------------ */

/** The controller's own bounds on a reason, in characters. A test holds them to what it says. */
export const WAIVER_REASON_MIN = 10;
export const WAIVER_REASON_MAX = 500;

/**
 * How long a reason is, counted the way the controller counts it: characters,
 * after the ends are trimmed. `.length` counts UTF-16 units, so an emoji would
 * be two, and a form that says "9 of 10" while the controller says "10" is a
 * form that argues with the person typing.
 */
export function reasonLength(reason: string): number {
  return [...reason.trim()].length;
}

/** What the reason field says about itself: the rule while it is unmet, the count after. */
export function reasonHint(reason: string): string {
  const n = reasonLength(reason);
  if (n < WAIVER_REASON_MIN) {
    return `At least ${WAIVER_REASON_MIN} characters: say why this is acceptable here, for whoever reads the audit log in a year.`;
  }
  return `${n} of ${WAIVER_REASON_MAX} characters.`;
}

/**
 * How long a waiver can run. The longest is a day short of the 365 the
 * controller allows, so that a browser whose clock is a little ahead of the
 * controller's does not turn the longest choice into a refusal.
 */
export const WAIVER_EXPIRY_CHOICES = [
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '180', label: '6 months' },
  { value: '364', label: 'A year' },
] as const;

/** A waiver is a decision to be made again, and a quarter is the default for that. */
export const WAIVER_DEFAULT_DAYS = '90';

/** When a waiver of `days` days ends, if it is made at `now`. */
export function waiverEndsAt(days: number, now: Date): string {
  return new Date(now.getTime() + days * 24 * 60 * 60 * 1000).toISOString();
}

/**
 * The role a finding's waiver needs. An error is an administrator's decision;
 * a warning or a note is an operator's. It is the controller's rule, drawn here
 * so that a person who cannot make the decision is told so instead of being
 * handed a form the controller will refuse.
 */
export function waiverRole(severity: Severity): 'admin' | 'operator' {
  return severity === 'error' ? 'admin' : 'operator';
}

export const ERROR_WAIVER_SENTENCE =
  'Only an administrator can waive an error. An operator can waive a warning or a note.';
