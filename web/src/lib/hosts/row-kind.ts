/**
 * What a flagged check row says about fixing it, and the command to preview that.
 *
 * Route-only on purpose: health.ts is shell code under the app-shell budget, and
 * only the host page needs these words. Zoomies never runs any of this -- the
 * command is text for a person to run on the host.
 */
import type { DoctorResult } from './health';
import { shellQuote } from './terminal';

export type RowKind = 'fixable' | 'advice' | 'optional';

export const KIND_WORDS: Record<RowKind, string> = {
  fixable: 'Fixable',
  advice: 'Advice',
  optional: 'Optional',
};

/**
 * A check id is `family.name`, and some names are real mixed-case unit names
 * (`service.ModemManager`), so a lower-case-only pattern would silently drop a
 * row. The `environment` row has no dot and never matches.
 */
export const CHECK_ID = /^[a-z]+(\.[A-Za-z0-9-]+)+$/;
export const CHECK_ID_MAX = 64;

/**
 * Null unless this is a warning on a host that can be tuned at all. `=== true`
 * means an older agent that omits `actionable` reads as Advice, the safe way to
 * be wrong; Optional wins because a trade-off is read about before it is fixed.
 */
export function rowKind(check: DoctorResult, readOnly: boolean): RowKind | null {
  // An accepted row is a decision already taken: it says so in its badge, and a
  // word about fixing it would argue with that.
  if (check.status !== 'warn' || readOnly || check.accepted) return null;
  if (check.optional === true) return 'optional';
  if (check.actionable === true) return 'fixable';
  return 'advice';
}

// tune refuses an aggressive or dedicated id without its flag, and there is no
// `--tier dedicated`, so each tier spells its own.
const TIER_FLAGS: Record<string, string[]> = {
  safe: [],
  aggressive: ['--tier', 'aggressive'],
  dedicated: ['--dedicated'],
};

/**
 * The dry-run a person runs on the host, or null for anything unvalidated -- never
 * a sanitised guess. The quoted id is the only agent-written text in the string,
 * and the quoting is defence in depth behind the pattern.
 */
export function previewCommand(check: DoctorResult): string | null {
  const tier: unknown = check.tier;
  if (typeof tier !== 'string' || !Object.hasOwn(TIER_FLAGS, tier)) return null;
  const flags = TIER_FLAGS[tier] ?? [];
  const id: unknown = check.id;
  if (typeof id !== 'string' || id.length > CHECK_ID_MAX || !CHECK_ID.test(id)) return null;
  return ['sudo', 'zoomies', 'tune', ...flags, '--only', shellQuote(id), '--dry-run'].join(' ');
}
