import type { Host } from '$lib/api/types';
import type { StatusTone } from '$lib/status';
export type DoctorReport = NonNullable<Host['doctor']>;
export function healthSummary(
  report: Host['doctor'],
  now: number,
  reachable = true,
): { label: string; tone: StatusTone; hint: string; stale: boolean } {
  if (!report)
    return {
      label: 'Health unavailable',
      tone: 'neutral',
      hint: 'No host OS report yet. Run zoomies doctor on the host or check its health service.',
      stale: true,
    };
  const checked = Date.parse(report.checked_at);
  const stale = !reachable || !Number.isFinite(checked) || now - checked > 3 * 60_000;
  const warnings = report.results.filter((r) => r.status === 'warn').length;
  const errors = report.results.filter((r) => r.status === 'error').length;
  const skipped = report.results.filter((r) => r.status === 'skip').length;
  const scope = report.container
    ? 'Partial report from the container. The native host health service supplies the full OS report.'
    : 'Host OS report from the native Zoomies binary.';
  const hint = `${scope} ${warnings} warning(s), ${errors} error(s), ${skipped} skipped check(s).${stale ? ' Report is stale or the host is unreachable.' : ''}`;
  if (stale)
    return {
      label: report.reboot_pending ? 'Reboot pending · stale' : 'Health stale',
      tone: 'neutral',
      hint,
      stale,
    };
  if (report.reboot_pending) return { label: 'Reboot pending', tone: 'pending', hint, stale };
  if (errors)
    return {
      label: `${errors} health ${errors === 1 ? 'error' : 'errors'}`,
      tone: 'danger',
      hint,
      stale,
    };
  if (warnings)
    return {
      label: `${warnings} ${warnings === 1 ? 'warning' : 'warnings'}`,
      tone: 'pending',
      hint,
      stale,
    };
  if (skipped === report.results.length)
    return { label: 'Checks unavailable', tone: 'neutral', hint, stale };
  return { label: 'Health OK', tone: 'idle', hint, stale };
}
