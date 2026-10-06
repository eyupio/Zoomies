import type { Host } from '$lib/api/types';
import type { StatusTone } from '$lib/status';
import { pluralise } from '../format';
export type DoctorReport = NonNullable<Host['doctor']>;
export type DoctorResult = DoctorReport['results'][number];

/**
 * Whether a result counts towards a host's health: the safe tier, and not an
 * optional suggestion.
 *
 * That is what `zoomies doctor` counts by default. The monitor reports every
 * tier, and the aggressive and dedicated ones are choices an operator opts into
 * (docs/host-health.md) -- a stock Ubuntu host has swappiness at 60 and a /tmp
 * that is not tmpfs from its first minute -- so counting them here put "9
 * warnings" on a host whose every safe check passed, and a number that
 * disagreed with the command the page tells the operator to run.
 */
export function counts(result: DoctorResult): boolean {
  return result.tier === 'safe' && result.optional !== true;
}

export function isFinding(result: DoctorResult): boolean {
  return result.status === 'warn' || result.status === 'error';
}

const RANK: Record<DoctorResult['status'], number> = { error: 0, warn: 1, ok: 2, skip: 3 };

/** Worst first. The sort is stable, so within a rank the engine's own order holds. */
export function bySeverity(results: readonly DoctorResult[]): DoctorResult[] {
  return [...results].sort((a, b) => RANK[a.status] - RANK[b.status]);
}

/** What needs doing: the counted warnings and errors, errors first. */
export function attention(report: DoctorReport): DoctorResult[] {
  return bySeverity(report.results.filter((r) => counts(r) && isFinding(r)));
}

export interface HealthSummary {
  label: string;
  tone: StatusTone;
  hint: string;
  stale: boolean;
  errors: number;
  warnings: number;
  skipped: number;
  /** Warnings that do not count: other tiers, and anything optional. */
  suggestions: number;
}

export function healthSummary(
  report: Host['doctor'],
  now: number,
  reachable = true,
): HealthSummary {
  const empty = { errors: 0, warnings: 0, skipped: 0, suggestions: 0 };
  if (!report)
    return {
      label: 'Health unavailable',
      tone: 'neutral',
      hint: 'No host OS report yet. Run zoomies doctor on the host or check its health service.',
      stale: true,
      ...empty,
    };
  const checked = Date.parse(report.checked_at);
  const stale = !reachable || !Number.isFinite(checked) || now - checked > 3 * 60_000;
  const counted = report.results.filter(counts);
  const warnings = counted.filter((r) => r.status === 'warn').length;
  const errors = counted.filter((r) => r.status === 'error').length;
  const skipped = counted.filter((r) => r.status === 'skip').length;
  const suggestions = report.results.filter((r) => !counts(r) && r.status === 'warn').length;
  const scope = report.container
    ? 'Partial report from the container. The native host health service supplies the full OS report.'
    : 'Host OS report from the native Zoomies binary.';
  const hint =
    `${scope} ${pluralise(warnings, 'warning')}, ${pluralise(errors, 'error')}, ` +
    `${pluralise(skipped, 'skipped check')}.` +
    (suggestions
      ? ` Also ${pluralise(suggestions, 'optional suggestion')}, which do not count towards health.`
      : '') +
    (stale ? ' Report is stale or the host is unreachable.' : '');
  const tally = { errors, warnings, skipped, suggestions };
  if (stale)
    return {
      label: report.reboot_pending ? 'Reboot pending · stale' : 'Health stale',
      tone: 'neutral',
      hint,
      stale,
      ...tally,
    };
  // Worst first, and every state that applies is said: a reboot used to return
  // before the errors were read, so a full disk showed as an amber "Reboot
  // pending" and the danger tone could not be reached at all.
  const parts: string[] = [];
  if (errors) parts.push(pluralise(errors, 'health error'));
  if (warnings) parts.push(pluralise(warnings, 'warning'));
  if (report.reboot_pending) parts.push('reboot pending');
  if (parts.length) {
    const label = parts.join(' · ');
    return {
      label: label.charAt(0).toUpperCase() + label.slice(1),
      tone: errors ? 'danger' : 'pending',
      hint,
      stale,
      ...tally,
    };
  }
  if (skipped === counted.length)
    return { label: 'Checks unavailable', tone: 'neutral', hint, stale, ...tally };
  return { label: 'Health OK', tone: 'idle', hint, stale, ...tally };
}
