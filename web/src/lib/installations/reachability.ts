/**
 * Whether GitHub can be told to deliver to this controller, and what an
 * operator may write down to say that it can.
 *
 * Two screens ask the same question and used to answer it separately: the
 * Connect dialog, which refuses to build an App against an address GitHub could
 * never reach, and the Overview's checklist, which offered a blue "Connect
 * GitHub" button whose first screen was exactly that refusal. A rule held in
 * two places is how one of them comes to say "go" while the other says
 * "stop", so both read it here.
 *
 * The reason it matters is that an App's webhook URL is fixed when GitHub
 * creates it. An App built against `http://localhost:8080` carries that
 * address for ever, and the symptom, weeks later, is "scaling is slow" -- not
 * anything that points back to the afternoon it was made.
 */
import { isLoopbackHost, isLoopbackURL } from '$lib/addresses';

/**
 * What the controller says its own address is.
 *
 * `missing` and `loopback` are told apart because they need different words --
 * "no address is set" against "it believes it is at an address only this
 * machine has" -- not because they need different fixes.
 */
export type Reachability = 'reachable' | 'missing' | 'loopback';

/**
 * Classify `server.external_url` as `/meta` reports it.
 *
 * An address that does not parse is called reachable, as `isLoopbackURL` does
 * not call it local: a controller cannot be running with one (the validator
 * stops startup on it), and "only this machine can reach it" would be the
 * wrong complaint about a typo.
 */
export function classifyExternalURL(raw: string | null | undefined): Reachability {
  const address = (raw ?? '').trim();
  if (address === '') return 'missing';
  return isLoopbackURL(address) ? 'loopback' : 'reachable';
}

/**
 * The form an address is saved and compared in: no surrounding space and no
 * trailing slash, which is what the controller does to it at startup. Saving
 * the same thing the controller will end up reporting is what lets the dialog
 * recognise, after a restart, that its change is in force.
 */
export function normaliseAddress(raw: string | null | undefined): string {
  return (raw ?? '').trim().replace(/\/+$/, '');
}

/** Whether two spellings of an address are the one the controller would hold. */
export function sameAddress(a: string | null | undefined, b: string | null | undefined): boolean {
  const left = normaliseAddress(a);
  return left !== '' && left === normaliseAddress(b);
}

export type AddressCheck = { ok: true; value: string } | { ok: false; message: string };

/**
 * Whether what an operator typed is an address worth saving, and if so the
 * form to save it in.
 *
 * It follows the controller's own rules (`ExternalURLValid` and
 * `ExternalURLIsLocal` in internal/config) and is a little stricter in three
 * places. The server accepts any scheme, but GitHub delivers webhooks over
 * http or https and nothing else. Loopback is refused here although the
 * server would store it, because a loopback address is a good default for a
 * fleet reached through an SSH tunnel and a bad one for an App -- which is
 * the whole reason this dialog is refusing. And a query or a fragment is
 * refused, because the controller builds every address it hands out by
 * appending a path to this one: `https://zoomies.example.com?x=1` would give
 * GitHub `https://zoomies.example.com?x=1/webhooks/github`, fixed into the App
 * for ever.
 *
 * Messages say what to write, not just that it is wrong.
 */
export function checkPublicAddress(raw: string): AddressCheck {
  const text = raw.trim();
  if (text === '') {
    return {
      ok: false,
      message:
        'Give the address GitHub will use to reach this controller, for example https://zoomies.example.com.',
    };
  }
  let url: URL;
  try {
    url = new URL(text);
  } catch {
    return {
      ok: false,
      message: 'Write it as a full address, for example https://zoomies.example.com.',
    };
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    return {
      ok: false,
      message: 'Start it with https:// (or http:// if you have no certificate).',
    };
  }
  if (url.hostname === '') {
    return { ok: false, message: 'It needs a host name or an IP address after the https://.' };
  }
  if (isLoopbackHost(url.hostname)) {
    return {
      ok: false,
      message:
        'That address only works on this machine, so GitHub could not reach it. Use a domain name, a public IP address or the address of a tunnel.',
    };
  }
  // The raw text, not `url.search`: a bare `?` or `#` parses to an empty one.
  if (/[?#]/.test(text)) {
    return {
      ok: false,
      message:
        'Write only the address, with nothing after a ? or a #. Zoomies adds its own paths to it.',
    };
  }
  return { ok: true, value: normaliseAddress(text) };
}

/**
 * The address to offer in the box, when this browser's own is a good one.
 *
 * An operator who tunnelled in over SSH is looking at `http://localhost:8080`,
 * which is exactly what must not be saved, so a loopback origin offers nothing
 * and the box starts empty. Anything the check above would refuse is treated
 * the same way, including the `null` origin of a page opened from a file.
 */
export function suggestAddress(origin: string): string {
  const checked = checkPublicAddress(origin);
  return checked.ok ? checked.value : '';
}
