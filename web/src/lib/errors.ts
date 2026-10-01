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
 *
 * Only ever the first letter and the last mark: not a word is changed, because
 * a 403 names the role required. Two things are left as written. A message that
 * opens with an identifier (`limits.job_stats_window ...`, `pool_ab12 ...`)
 * keeps its case, since a capital would name a setting that does not exist; and
 * one that ends in a path, an address or a colon takes no full stop, which
 * would be copied along with the address.
 */
export function sentence(text: string): string {
  const trimmed = text.trim();
  if (trimmed === '') return '';
  const identifier = /^\S*[._]\S*(\s|$)/.test(trimmed);
  const capitalised = identifier ? trimmed : trimmed.charAt(0).toUpperCase() + trimmed.slice(1);
  const open = /[.!?:]$/.test(capitalised) || /\S*\/\S*$/.test(capitalised);
  return open ? capitalised : `${capitalised}.`;
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

/**
 * The one sentence every "we could not tell you why" ends with.
 *
 * It used to say the controller log would have the cause, which assumes the
 * reader can read that log. On an instance one team operates while another
 * uses the fleet they cannot: the log belongs to the process, and the person
 * reading this runs pools on it. Naming the request ID and who to hand it to
 * is true for both — someone running their own instance is "whoever operates
 * this instance", and its log is theirs to open.
 *
 * One function rather than nine sentences, because nine sentences is how the
 * ninth came to say something the other eight had stopped saying.
 */
export function supportHint(requestId?: string): string {
  return requestId
    ? `Quote ${requestId} to whoever operates this instance; the cause is in its log.`
    : 'Quote the request ID to whoever operates this instance; the cause is in its log.';
}

/**
 * What went wrong saving one setting, in words for whoever asked.
 *
 * The API answers 422 with a message written for a person, and it names the
 * key it is about -- so that sentence is returned as it is, for the row or the
 * dialog to put beside the field rather than in a toast that floats away.
 *
 * A refusal about the whole request rather than about this field -- the
 * combination would leave a controller that will not start, and the setting at
 * fault is one nobody touched -- carries its reason in the field errors, and
 * that reason is the useful half. It is kept, after the envelope's one-line
 * summary, rather than dropped for it.
 *
 * It is one function because the Settings page and the Connect dialog both
 * save a setting, and a second copy of this is how the two come to describe the
 * same refusal in different words.
 */
export function settingSaveError(cause: unknown, key: string): string {
  if (cause instanceof ApiError) {
    const errors = cause.fieldErrors();
    if (errors[key]) return errors[key];
    const general = Object.values(errors).filter(Boolean);
    return general.length > 0 ? `${cause.message}: ${general.join(' ')}` : cause.message;
  }
  return `That change could not be made. ${supportHint()}`;
}
