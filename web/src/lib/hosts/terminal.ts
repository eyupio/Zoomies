/**
 * Commands the UI hands somebody to paste into a terminal.
 *
 * What is built here is text a person runs, so each value is quoted for a shell
 * unless it is made only of characters a shell leaves alone. An address is
 * whatever an administrator typed into a setting, and "it is only ever a URL" is
 * the sort of belief a command line is a poor place to keep.
 */

const PLAIN = /^[A-Za-z0-9_@%+=:,./-]+$/;

/** A value as one shell word: itself when plain, and otherwise in single quotes. */
export function shellQuote(value: string): string {
  if (PLAIN.test(value)) return value;
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

/**
 * `zoomies doctor --host`, with everything it needs: the host, the controller's
 * address and, when the controller wants one, a token. Without a token the
 * command is the one for a controller that has authentication off.
 */
export function doctorCommand(hostId: string, url: string, token?: string): string {
  const parts = ['zoomies', 'doctor', '--host', shellQuote(hostId), '--verbose'];
  parts.push('--url', shellQuote(url));
  if (token) parts.push('--token', shellQuote(token));
  return parts.join(' ');
}
