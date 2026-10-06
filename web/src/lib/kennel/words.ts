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
