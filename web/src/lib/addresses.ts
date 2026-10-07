/**
 * Whether an address only the controller's own machine can reach.
 *
 * The same rule as `loopbackHost` in `internal/config`: localhost, any name
 * under `.localhost`, and the loopback ranges. Two dialogs had a copy of this
 * each and they did not agree -- one knew about `.localhost` and the whole
 * `127.0.0.0/8` range, the other matched exactly `127.0.0.1` -- so a controller
 * reached on `127.0.0.2` was refused by the host flow and accepted by the
 * GitHub App dialog, which is the one that bakes the address into an App for
 * ever.
 */
export function isLoopbackHost(host: string): boolean {
  const bare = host.replace(/^\[|\]$/g, '').toLowerCase();
  if (bare === 'localhost' || bare.endsWith('.localhost')) return true;
  if (bare === '::1' || bare === '0:0:0:0:0:0:0:1') return true;
  return /^127\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(bare);
}

/**
 * The same question asked of a whole URL. An address that will not parse is
 * not treated as local: the caller's next step is to complain about it, and
 * "this is only reachable from here" would be the wrong complaint.
 */
export function isLoopbackURL(raw: string): boolean {
  try {
    return isLoopbackHost(new URL(raw).hostname);
  } catch {
    return false;
  }
}

/**
 * Where a command run on another machine should be told to find the controller.
 *
 * `server.external_url` is right when it is set and is not loopback: the default
 * single-VM install sets it to http://localhost:8080, which no other machine
 * answers on. Failing that, the address this browser is using is the best guess
 * there is, because a machine on the same network usually reaches the controller
 * the way an operator does.
 *
 * It is one function because two places hand an address to somebody to paste --
 * the command that adds a host and the one that reads its health -- and an
 * address that differed between them would be one of them wrong.
 */
export function suggestedControllerURL(configured: string | undefined, origin: string): string {
  const url = (configured ?? '').replace(/\/+$/, '');
  if (url && !isLoopbackURL(url)) return url;
  return origin;
}
