/**
 * What changed between two reports from the same host, for the host page to say
 * out loud.
 *
 * Route-only, like next-step.ts: health.ts is shell code (the feed imports it),
 * so the words that only this page needs live here.
 *
 * The diff reads `attention()` rather than the raw results, so it inherits the
 * rules the pill already follows -- counted checks only, the optional and tier
 * rules, and the kernel.pending warning folded into the reboot flag. A change
 * announced here can therefore never disagree with the badge beside it.
 */
import { attention, healthSummary, type DoctorReport, type DoctorResult } from './health';
import { reportOnly } from './report-only';

export interface ReportChange {
  id: string;
  title: string;
  kind: 'resolved' | 'new' | 'escalated';
  /** What a `new` check now says, so the words can tell a warning from an error. */
  status?: 'warn' | 'error';
}

/** A title is the agent's text and the page's only copy of it in a live region; bound it. */
const TITLE_LIMIT = 60;

function titleOf(result: DoctorResult): string {
  const text = [...(result.title || result.id)];
  return text.length > TITLE_LIMIT ? `${text.slice(0, TITLE_LIMIT).join('')}…` : text.join('');
}

function index(report: DoctorReport): Map<string, DoctorResult> {
  const byId = new Map<string, DoctorResult>();
  // First wins: a report that repeats an id is the agent's mistake, and both
  // sides of the diff read it the same way.
  for (const r of report.results) if (!byId.has(r.id)) byId.set(r.id, r);
  return byId;
}

function time(report: DoctorReport): number {
  return Date.parse(report.checked_at);
}

/**
 * The same source, so the diff compares like with like: a host that was
 * reinstalled, upgraded or moved into a container changes which checks exist,
 * and "resolved" would then be a statement about the agent, not the host.
 */
export function sameSource(
  prev: DoctorReport,
  next: DoctorReport,
  ctx: { prevVersion: string; nextVersion: string },
): boolean {
  return (
    prev.os === next.os &&
    prev.distro === next.distro &&
    prev.container === next.container &&
    ctx.prevVersion === ctx.nextVersion
  );
}

/**
 * Checks that cleared or started needing attention between two reports. Empty
 * unless every gate passes:
 *
 * 1. there is a previous report (the first one seen is the baseline, so opening
 *    the page announces nothing);
 * 2. the new one is strictly later, which is the controller's own rule -- a
 *    repeated frame, a cordon frame or a replay is a no-op, and a frame count is
 *    never a signal;
 * 3. both come from the same source (the caller rebaselines silently when not);
 * 4. both carry the controller's count and neither is a partial report, whose
 *    checks are mostly skipped and which the pill calls no verdict.
 *
 * Only ids present in both reports count: a check that appeared or vanished
 * with an agent change says nothing about the host.
 */
export function reportChanges(
  prev: DoctorReport | undefined,
  next: DoctorReport | undefined,
  ctx: { prevVersion: string; nextVersion: string },
): ReportChange[] {
  if (!prev || !next) return [];
  const before = time(prev);
  const after = time(next);
  if (!Number.isFinite(before) || !Number.isFinite(after) || after <= before) return [];
  if (!sameSource(prev, next, ctx)) return [];
  if (!prev.summary || !next.summary) return [];
  if (prev.container || next.container || reportOnly(prev) || reportOnly(next)) return [];

  const was = new Map(attention(prev).map((r) => [r.id, r]));
  const now = new Map(attention(next).map((r) => [r.id, r]));
  const before2 = index(prev);
  const after2 = index(next);
  const changes: ReportChange[] = [];

  for (const [id, old] of was) {
    const current = after2.get(id);
    if (!current) continue;
    // A skipped check is not an OK one: a stopped Docker daemon skips a whole
    // group at once, and "now OK" for it would be false.
    if (current.status === 'ok') changes.push({ id, title: titleOf(current), kind: 'resolved' });
    else if (current.status === 'error' && old.status === 'warn' && now.has(id))
      changes.push({ id, title: titleOf(current), kind: 'escalated' });
  }
  for (const [id, current] of now) {
    if (was.has(id) || !before2.has(id)) continue;
    changes.push({
      id,
      title: titleOf(current),
      kind: 'new',
      status: current.status === 'error' ? 'error' : 'warn',
    });
  }
  return changes;
}

/** "A", "A and B", "A, B and C": no verb follows, so no agreement to get wrong. */
function list(titles: string[]): string {
  return titles.length < 2
    ? (titles[0] ?? '')
    : `${titles.slice(0, -1).join(', ')} and ${titles[titles.length - 1]}`;
}

/** One group of check names and what happened to them, collapsing to a count when long. */
function group(titles: string[], single: string, many: (n: number) => string): string {
  if (titles.length === 0) return '';
  if (titles.length > 3) return many(titles.length);
  return `${list(titles)} — ${single}`;
}

/**
 * One string for a whole report, so a screen reader hears one announcement
 * rather than one per check. Contains no time: a reconnect can legitimately
 * announce a change that happened during a gap, and "just now" would be false.
 */
export function announcement(changes: ReportChange[], allClear: boolean): string {
  if (changes.length === 0) return '';
  const titles = (pick: (c: ReportChange) => boolean) => changes.filter(pick).map((c) => c.title);
  const parts = [
    group(
      titles((c) => c.kind === 'resolved'),
      'now OK',
      (n) => `${n} checks are now OK`,
    ),
    group(
      titles((c) => c.kind === 'new' && c.status !== 'error'),
      'now needs attention',
      (n) => `${n} checks now need attention`,
    ),
    group(
      titles((c) => c.kind === 'new' && c.status === 'error'),
      'now has an error',
      (n) => `${n} checks now have an error`,
    ),
    group(
      titles((c) => c.kind === 'escalated'),
      'warning became an error',
      (n) => `${n} warnings became errors`,
    ),
    allClear ? 'Nothing on this host needs attention now' : '',
  ].filter(Boolean);
  return parts.join('. ');
}

/** The same facts one per row for the visible list. */
export function logLine(c: ReportChange): string {
  if (c.kind === 'resolved') return `${c.title} — now OK`;
  if (c.kind === 'escalated') return `${c.title} — warning became an error`;
  return `${c.title} — ${c.status === 'error' ? 'now has an error' : 'now needs attention'}`;
}

/** Whether a report now says everything is fine, for the "nothing needs attention" sentence. */
export function allClear(report: DoctorReport, now: number): boolean {
  return attention(report).length === 0 && healthSummary(report, now, true).label === 'Health OK';
}
