/**
 * Where a host problem's "open" link goes.
 *
 * Kept apart from `ProblemItem.svelte`, which cannot be loaded outside a
 * browser, so the one rule here is tested in Node: the three OS health codes
 * are about what a host's report says, and the page that has the report is the
 * host's own. Every other host problem keeps the list. An offline host has no
 * report to show, so `host.unhealthy` sending somebody to a page that says "no
 * report yet" would be a link that did nine tenths of the work and stopped.
 */
const REPORT_CODES: ReadonlySet<string> = new Set([
  'host.os_health',
  'host.health_stale',
  'host.reboot_pending',
]);

export function hostTarget(code: string | undefined, id: string): { href: string; label: string } {
  return code !== undefined && REPORT_CODES.has(code)
    ? { href: `/hosts/${id}`, label: 'Open the host' }
    : { href: '/hosts', label: 'Open hosts' };
}
