/**
 * What the Add a host dialog knows about Windows, in one place.
 *
 * Windows is a platform the agent runs on, not yet one the project has
 * qualified: the support matrix (`roadmap/support-and-measurement.md`) moves
 * its row only when a person has run a job on a real Windows machine and kept
 * the evidence. Until then the dialog says so, and says it from one constant so
 * that the day the row moves is a one-line change here and not a hunt through
 * components.
 */

/** Flip to true in the same change that moves the Windows row in the support matrix. */
export const WINDOWS_QUALIFIED = false;

export type HostOS = 'linux' | 'windows';

/**
 * The operating system the person is most likely adding a host for: the one
 * their browser is on. Most people add a host from the machine they are at, and
 * the choice is one click away when they are not. macOS gets the Linux command,
 * which is the shell installer.
 */
export function defaultHostOS(platform: string = browserPlatform()): HostOS {
  return /win/i.test(platform) && !/darwin/i.test(platform) ? 'windows' : 'linux';
}

function browserPlatform(): string {
  if (typeof navigator === 'undefined') return '';
  const nav = navigator as Navigator & { userAgentData?: { platform?: string } };
  return nav.userAgentData?.platform || nav.platform || '';
}

/** The host selector that places a pool's runners on Windows hosts, and only those. */
export const WINDOWS_HOST_SELECTOR = 'os=windows';

/**
 * A host the Windows agent enrolled: it reports `os=windows` and offers only
 * the process backend (there are no Windows containers here), so a pool has to
 * name that backend and ask for the OS to reach it.
 */
export function isWindowsProcessHost(
  host: { os?: string; backends?: string[] } | null | undefined,
): boolean {
  return Boolean(host && host.os === 'windows' && (host.backends ?? []).includes('process'));
}
