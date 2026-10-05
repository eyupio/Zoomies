/**
 * Why the controller did something it did on its own.
 *
 * Kept apart from the entries that show it because it is plain data handling and
 * wants a test, and the module those entries live in imports components.
 */
import type { AuditEvent } from '../api/types';

/**
 * The sentence an automatic change carries as its reason, where this is one.
 *
 * Every change the controller makes by itself -- an automatic pool following its
 * hosts, a host moving class, a job's class moving -- is recorded under the
 * system identity with a `cause` in the row's after-picture, so that the audit
 * log answers "why did that change" without anybody having read the controller's
 * decision. An operator's own action has no such field, and an after-picture
 * that is not JSON at all is somebody else's shape, so both read as no cause.
 */
export function causeOf(event: Pick<AuditEvent, 'after'>): string | undefined {
  if (!event.after) return undefined;
  try {
    const after: unknown = JSON.parse(event.after);
    const cause = (after as { cause?: unknown } | null)?.cause;
    return typeof cause === 'string' && cause.trim() !== '' ? cause.trim() : undefined;
  } catch {
    return undefined;
  }
}

/**
 * What each thing the controller does on its own is called in the feed.
 *
 * Every one of these is recorded under the system identity with a `cause`, and
 * the sentence is what an entry is read for, so the title only says what kind of
 * change it was. The action names are what the Audit page filters on and are
 * kept as the target of the entry's link; this is the plain-words side.
 */
export const AUTOMATIC_ACTIONS: Readonly<Record<string, string>> = {
  'pool.auto_create': 'Automatic pool made',
  'pool.auto_reshape': 'Automatic pool brought back in line',
  'pool.auto_resize': 'Automatic pool resized',
  'pool.auto_enable': 'Automatic pool put back in use',
  'pool.auto_disable': 'Automatic pool put out of use',
  'host.size_class': 'Host size class changed',
  'size_class.move': 'Job size class changed',
};

/** The feed's title for an action the controller took on its own, or undefined for any other. */
export function automaticTitle(action: string | undefined): string | undefined {
  return action ? AUTOMATIC_ACTIONS[action] : undefined;
}

/**
 * What an automatic change was about, by name: the pool's, or the host's. The
 * audit row carries the id of what it concerns, which says nothing to a person,
 * and the name beside it in the after-picture, which does.
 */
export function subjectOf(event: Pick<AuditEvent, 'after'>): string | undefined {
  if (!event.after) return undefined;
  try {
    const after: unknown = JSON.parse(event.after);
    const doc = (after ?? {}) as { name?: unknown; host?: unknown };
    for (const name of [doc.name, doc.host]) {
      if (typeof name === 'string' && name.trim() !== '') return name.trim();
    }
  } catch {
    // Somebody else's shape; the entry says what it concerns by id instead.
  }
  return undefined;
}
