/**
 * Hosts whose report can be read but not acted on, and the words for them.
 *
 * Route-only, like next-step.ts: health.ts is shell code (the feed imports it),
 * so the sentences that only the host page needs live here instead of growing
 * the app shell.
 *
 * The page used to tell every host to "review changes locally" with
 * `zoomies tune`, which is a command that changes nothing on a macOS host, an
 * unsupported distribution or a container. Saying so is the point: a person
 * following the old sentence on a Mac was sent to a command that refuses.
 */
import type { DoctorReport } from './health';

export type ReportOnly = 'os' | 'distro' | 'container';

/**
 * Why a report cannot be acted on, or null when it can.
 *
 * Not `os === 'linux'`: the engine adds its `environment` row exactly when the
 * platform is unsupported (a skip for another system, a warning for a Linux
 * distribution outside the list), so reading that row keeps a second list of
 * distributions out of the UI. An agent that predates the row is treated as
 * tunable, which is what the page did before this existed.
 */
export function reportOnly(report: DoctorReport | undefined): ReportOnly | null {
  if (!report) return null;
  if (report.container) return 'container';
  const env = report.results.find((r) => r.id === 'environment');
  if (env?.status === 'skip') return 'os';
  if (env?.status === 'warn') return 'distro';
  return null;
}

/** The sentence that replaces "Review changes locally with ...". Words only: no command here is run by Zoomies. */
export function reportOnlySentence(kind: ReportOnly, os: string): string {
  switch (kind) {
    case 'os':
      return `Zoomies does not tune ${os ? `${os} hosts` : 'this kind of host'} — its OS checks need Linux, so this report is informational and there is nothing to change on this host.`;
    case 'distro':
      return 'This distribution is report-only: run sudo zoomies doctor on the host to read its checks. zoomies tune will not change it.';
    case 'container':
      return 'Run sudo zoomies doctor on the host itself with the native binary, not inside the container — a container cannot be tuned.';
  }
}

/** The page subtitle: null keeps the usual one. */
export function reportOnlySubtitle(kind: ReportOnly | null): string | null {
  return kind === 'os'
    ? 'OS checks run on Linux hosts only. Zoomies does not change this host.'
    : null;
}

/** The Safe tier's description when nothing on this host can be applied. */
export const REPORT_ONLY_SAFE_DESCRIPTION = 'Read-only. Zoomies does not change this host.';

/** "linux · ubuntu 24.04", without a stray separator when the distribution is blank. */
export function reportOrigin(report: Pick<DoctorReport, 'os' | 'distro'>): string {
  return [report.os, report.distro?.trim()].filter(Boolean).join(' · ');
}
