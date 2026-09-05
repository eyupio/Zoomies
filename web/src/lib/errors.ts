/**
 * Turning an API failure into something an operator can act on.
 *
 * The sign-in and first-run cards are the same screen at two moments in a
 * controller's life, and they meet the same failures: a controller that never
 * answered, a rate limiter, an origin check, a password that was refused. They
 * used to disagree about all of it -- Login.svelte separated four kinds and
 * Bootstrap.svelte printed `failure.message` raw -- so the two halves of the
 * first five minutes read as two different products. This is the one ladder
 * both of them climb.
 */
import { ApiError } from './api/client';

/**
 * The server's sentences are terse and lowercase, because they are also read
 * in a terminal and in a log line. On a card they are prose, so they start
 * with a capital and end with a full stop.
 */
export function sentence(text: string): string {
  const trimmed = text.trim();
  if (trimmed === '') return '';
  const capitalised = trimmed.charAt(0).toUpperCase() + trimmed.slice(1);
  return /[.!?]$/.test(capitalised) ? capitalised : `${capitalised}.`;
}

/**
 * What actually went wrong, in the operator's terms.
 *
 * A controller that never answered and a password that was refused look
 * identical in a generic "that did not work", and they need completely
 * different next steps. A 401 or 403 is shown in the server's own words: it
 * distinguishes a wrong password from a disabled account, from an account that
 * signs in through SSO, and -- on a 403 -- from the origin check refusing the
 * request, which is a proxy or external_url problem that "wrong password"
 * would send the operator off to fix in entirely the wrong place.
 */
export function authFailureText(error: unknown): string {
  if (!(error instanceof ApiError)) {
    if (error instanceof Error && error.message) return sentence(error.message);
    return 'That did not work, and the controller did not say why. Its logs will.';
  }
  if (error.status === 0) {
    return 'The controller did not answer. Check that it is running and that this address can reach it.';
  }
  if (error.status === 429) {
    return 'Too many attempts from this address. Wait a minute, then try again.';
  }
  if (error.status >= 500) {
    return 'The controller answered with an error. Its logs will say more than this page can.';
  }
  return sentence(error.message);
}
