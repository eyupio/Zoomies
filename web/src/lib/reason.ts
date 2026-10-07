/**
 * How a person's reason for a decision is measured and prompted for.
 *
 * Shared by the two forms that record one -- a Kennel waiver and a host check
 * accepted as deliberate -- because the controller counts them the same way and
 * a form that argued with it about the tenth character would do so twice.
 */

/** The controller's own bounds on a reason, in characters. A test holds them to what it says. */
export const REASON_MIN = 10;
export const REASON_MAX = 500;

/**
 * How long a reason is, counted the way the controller counts it: characters,
 * after the ends are trimmed. `.length` counts UTF-16 units, so an emoji would
 * be two, and a form that says "9 of 10" while the controller says "10" is a
 * form that argues with the person typing.
 */
export function reasonLength(reason: string): number {
  return [...reason.trim()].length;
}

/**
 * What the reason field says about itself: the rule while it is unmet, the count
 * after. `what` finishes "say why this is ...", so each form asks its own question.
 */
export function reasonHint(reason: string, what = 'acceptable here'): string {
  const n = reasonLength(reason);
  if (n < REASON_MIN) {
    return `At least ${REASON_MIN} characters: say why this is ${what}, for whoever reads the audit log in a year.`;
  }
  return `${n} of ${REASON_MAX} characters.`;
}
