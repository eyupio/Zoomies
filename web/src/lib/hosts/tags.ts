/**
 * A host's tags, size class and pool, as the card and the dialog say them.
 *
 * Every sentence that is a decision -- why a host is in its class, why it counts
 * towards no pool -- is written by the controller, because it is the side that
 * knows. What is here is only the arrangement of those, so the card and the
 * dialog order and word a tag the same way and one screen never writes it two
 * ways.
 */
import type { Host, HostAutoPool, HostSizeClass, HostTag, SizeClass } from '../api/types';

/** The classes in the order a host grows through them. */
export const SIZE_CLASSES: readonly SizeClass[] = ['small', 'medium', 'large'];

/** `Medium`, for a badge. */
export function classWord(size: SizeClass | undefined): string {
  return size ? size.charAt(0).toUpperCase() + size.slice(1) : '';
}

/** `rack=b4`, or `gpu` for a tag whose value is empty. */
export function tagText(tag: Pick<HostTag, 'key' | 'value'>): string {
  return tag.value === '' ? tag.key : `${tag.key}=${tag.value}`;
}

/**
 * The labels a dialog's rows say, as the API takes them.
 *
 * A row with a name and no value is a flag, and a flag is `key=true` -- what
 * `zoomies hosts edit --tag gpu` writes -- so that a pool's host selector asks
 * for it the same way whichever surface set it. A label the host already carries
 * with an empty value keeps it, because saving the dialog must not rewrite what
 * nobody touched.
 */
export function labelsFromRows(
  rows: readonly { key: string; value: string }[],
  was: Readonly<Record<string, string>> = {},
): Record<string, string> {
  const labels: Record<string, string> = {};
  for (const row of rows) {
    const key = row.key.trim();
    if (!key) continue;
    const value = row.value.trim();
    labels[key] = value !== '' ? value : was[key] === '' ? '' : 'true';
  }
  return labels;
}

export interface TagRow {
  key: string;
  text: string;
  /** Worked out from the machine by the controller, never stored, never edited here. */
  automatic: boolean;
  /** What the tag is, for a tooltip: where it came from and what changing it does. */
  hint: string;
}

/**
 * The tags to list.
 *
 * The operator's come first, because those are the ones the card's own Edit
 * button changes and the card has always shown them in this place; the ones the
 * controller derives follow, so a host that has none of its own still says what
 * machine it is. Each group keeps the order the API sent, which is by key.
 */
export function tagRows(tags: readonly HostTag[] | undefined): TagRow[] {
  const rows = (tags ?? []).map((tag): TagRow => {
    const automatic = tag.source === 'automatic';
    return {
      key: tag.key,
      text: tagText(tag),
      automatic,
      hint: automatic ? automaticHint(tag.key) : operatorHint(tag),
    };
  });
  return [...rows.filter((row) => !row.automatic), ...rows.filter((row) => row.automatic)];
}

/**
 * The rows for one host. A host the API describes without `tags` -- an older
 * controller's, or a fixture that predates them -- has its labels listed as the
 * operator's own, which is what they were.
 */
export function hostTagRows(host: Pick<Host, 'tags' | 'labels'>): TagRow[] {
  if (host.tags) return tagRows(host.tags);
  return tagRows(
    Object.entries(host.labels ?? {})
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([key, value]) => ({ key, value, source: 'operator' as const })),
  );
}

function automaticHint(key: string): string {
  if (key === 'size') {
    return "Worked out by the controller from the host's CPU and memory, and held before it changes. Set a size tag of your own to say otherwise.";
  }
  return 'Worked out by the controller from what the agent reports about the machine. It is not stored and is not edited here.';
}

function operatorHint(tag: HostTag): string {
  if (tag.overrides) {
    return `Stored on this host. It replaces ${tag.key}=${tag.overrides}, which is what the controller would have worked out. Remove it to go back to that.`;
  }
  return 'Stored on this host. A pool whose host selector names it runs only on hosts that have it.';
}

/**
 * A sentence for a class an operator set, or '' for one that was measured.
 *
 * A measured class explains itself in the controller's `reason`, which sits in
 * a tooltip. A tag changes the answer the machine would give, so the card says
 * so in the open: otherwise a host that reads "large" beside a machine that
 * plainly is not leaves its operator to find the tag.
 */
export function overrideNote(size: HostSizeClass | undefined): string {
  if (!size || size.source !== 'tag') return '';
  if (size.measured && size.measured !== size.class) {
    return `Set by this host's size tag. Its machine measures as ${size.measured}.`;
  }
  return "Set by this host's size tag, which agrees with what its machine measures.";
}

/** A class move the host is being held before making, or null while it is not waiting on one. */
export function pendingMove(
  size: HostSizeClass | undefined,
): { to: SizeClass; since?: string; until?: string } | null {
  if (!size?.pending) return null;
  return { to: size.pending, since: size.pending_since, until: size.pending_until };
}

/**
 * The line saying whether this host's slots count towards an automatic pool.
 * Null where the controller has no view of it, which is every host while both
 * switches are off.
 */
export function autoPoolLine(
  auto: HostAutoPool | undefined,
): { counted: boolean; text: string; poolId?: string } | null {
  if (!auto) return null;
  if (!auto.counted) return { counted: false, text: auto.reason || 'It counts towards no pool.' };
  const name = auto.pool ?? 'an automatic pool';
  return auto.pool_id
    ? { counted: true, text: `Its slots count towards ${name}.`, poolId: auto.pool_id }
    : {
        counted: true,
        text: `Its slots would count towards ${name}, which has not been made yet.`,
      };
}
