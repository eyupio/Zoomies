/**
 * Where a host problem's "open" link goes.
 *
 * Kept apart from `ProblemItem.svelte`, which cannot be loaded outside a
 * browser, so the one rule here is tested in Node: the three OS health codes
 * are about what a host's report says, and the page that has the report is the
 * host's own. The two update codes are about one host's agent being updated, and
 * the host's page is where an administrator asks for that. No other host
 * problem belongs there: those are about the agent going quiet, the host's
 * capacity or its folders, and the list is where a host's state and its slots
 * are.
 */
const HOST_PAGE_CODES: ReadonlySet<string> = new Set([
  'host.os_health',
  'host.health_stale',
  'host.reboot_pending',
  'host.update_failed',
  'host.update_unavailable',
]);

export function hostTarget(code: string | undefined, id: string): { href: string; label: string } {
  return code !== undefined && HOST_PAGE_CODES.has(code)
    ? { href: `/hosts/${id}`, label: 'Open the host' }
    : { href: '/hosts', label: 'Open hosts' };
}

/**
 * Where a controller problem's "open" link goes, or null for one with no page
 * of its own.
 *
 * A controller problem names no pool, host or setting, so the code is all there
 * is to go by. The notice that a newer release exists is about updating, and the
 * Updates page reads that same release in full and says what the update mode
 * would do about it. A code is added here when it has been given a page, never
 * guessed at: a link to a page that says nothing about what the problem said is
 * worse than no link.
 */
export function controllerTarget(code: string | undefined): { href: string; label: string } | null {
  return code === 'controller.update_available'
    ? { href: '/settings/updates', label: 'Open Updates' }
    : null;
}
