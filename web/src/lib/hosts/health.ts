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

/**
 * The check whose warning is the reboot flag. The agent sets `reboot_pending`
 * from it, so the one fact arrives twice in a report; the controller counts it
 * once, as the reboot, and so does this list. Must match hosttune.KernelPending.
 */
export const KERNEL_PENDING = 'kernel.pending';

/**
 * What needs doing: the counted warnings and errors, errors first, and never
 * the kernel.pending warning of a report that already says a reboot is pending.
 *
 * Its length is `summary.warnings + summary.errors`, the controller's own count,
 * and the host-health spec checks that against a real controller -- this is the
 * one place the rule is written a second time, so it is where the two would
 * drift.
 *
 * Empty for a container's partial report. The pill calls that report no verdict
 * and the controller raises nothing for it, so a list of what "needs attention"
 * on the same page would contradict both -- and its one warning, the image's own
 * distribution, is one nobody can clear. The rows stay in the tables below.
 */
export function attention(report: DoctorReport): DoctorResult[] {
  if (report.container) return [];
  return bySeverity(
    report.results.filter(
      (r) =>
        counts(r) &&
        isFinding(r) &&
        !(report.reboot_pending && r.id === KERNEL_PENDING && r.status === 'warn'),
    ),
  );
}

/**
 * The counted findings in words, errors first: "1 health error · 2 warnings".
 * Empty when there are none. The pill and the feed both say it this way, so a
 * line in the feed and the badge it came from read alike.
 */
export function findingsLabel(found: { errors: number; warnings: number }): string {
  const parts: string[] = [];
  if (found.errors) parts.push(pluralise(found.errors, 'health error'));
  if (found.warnings) parts.push(pluralise(found.warnings, 'warning'));
  return parts.join(' · ');
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

/**
 * The badge for one host, read from the controller's own count (`summary`).
 *
 * The count is not worked out here: the problems, the metrics, the feed and
 * `zoomies hosts list` read the same five numbers, and a pill that tallied the
 * results itself would be the one surface that could disagree with them. What
 * stays here is what only a browser knows -- the clock the report is judged
 * against -- and the wording.
 */
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
  // A controller older than this page sends the report without its count.
  // Counting the results here instead would put a second opinion on the screen,
  // so the page says what it does not know.
  const summary = report.summary;
  if (!summary)
    return {
      label: 'Health unavailable',
      tone: 'neutral',
      hint: 'This host’s OS report arrived without the controller’s count of it, so no verdict is shown. Reload the page; if this stays, the controller is older than the page.',
      stale: true,
      ...empty,
    };
  const checked = Date.parse(report.checked_at);
  const stale = !reachable || !Number.isFinite(checked) || now - checked > 3 * 60_000;
  const { errors, warnings, skipped, suggestions } = summary;
  const tally = { errors, warnings, skipped, suggestions };
  // A container's report is the container's view, not the host's: most checks
  // are skipped and the image's distribution warns for ever. The controller
  // raises nothing for it, so neither does the pill, and the counts are left
  // out of the hint rather than offered as a verdict.
  const partial = report.container === true;
  const hint = partial
    ? 'Partial report from the container. It can see only what the container can, so Zoomies draws no conclusion from it and raises no problem for it. The native host health service supplies the full OS report.' +
      (stale ? ' Report is stale or the host is unreachable.' : '')
    : 'Host OS report from the native Zoomies binary. ' +
      `${pluralise(warnings, 'warning')}, ${pluralise(errors, 'error')}, ` +
      `${pluralise(skipped, 'skipped check')}.` +
      (suggestions
        ? ` Also ${pluralise(suggestions, 'optional suggestion')}, which do not count towards health.`
        : '') +
      (stale ? ' Report is stale or the host is unreachable.' : '');
  // A partial report's reboot flag is no more a verdict than its checks are.
  const reboot = report.reboot_pending && !partial;
  if (stale)
    return {
      label: reboot ? 'Reboot pending · stale' : 'Health stale',
      tone: 'neutral',
      hint,
      stale,
      ...tally,
    };
  if (partial) return { label: 'Partial report', tone: 'neutral', hint, stale, ...tally };
  // Worst first, and every state that applies is said: a reboot used to return
  // before the errors were read, so a full disk showed as an amber "Reboot
  // pending" and the danger tone could not be reached at all.
  const label = [findingsLabel(tally), reboot ? 'reboot pending' : ''].filter(Boolean).join(' · ');
  if (label)
    return {
      label: label.charAt(0).toUpperCase() + label.slice(1),
      tone: errors ? 'danger' : 'pending',
      hint,
      stale,
      ...tally,
    };
  if (skipped === summary.counted)
    return { label: 'Checks unavailable', tone: 'neutral', hint, stale, ...tally };
  return { label: 'Health OK', tone: 'idle', hint, stale, ...tally };
}
