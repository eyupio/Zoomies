/**
 * The one line a host card says about the checks that need attention, and the
 * address the health pill takes you to.
 *
 * Route-only on purpose: health.ts is shell code (the feed reads it), and the
 * card is the only caller, so none of this belongs in the app shell's budget.
 */
import type { Host } from '$lib/api/types';
import { attention, type HealthSummary } from './health';

export interface CardLine {
  /** Empty when the card has nothing honest to name. */
  text: string;
  /** The first check named, which the host page scrolls to; null when there is none. */
  firstId: string | null;
  href: string;
}

/** A title is written by the host, up to 200 characters; a card has room for a phrase. */
const TITLE_LIMIT = 40;

function clip(title: string): string {
  const chars = [...title.trim()];
  return chars.length > TITLE_LIMIT
    ? `${chars.slice(0, TITLE_LIMIT - 1).join('')}…`
    : chars.join('');
}

/**
 * Names the worst checks, in the order Needs attention lists them.
 *
 * Empty for a stale report, a container and a reboot-only report: a stale
 * report must never list last-known checks as if they were current, and the
 * pill's own label already says the state. Never "findings": the glossary
 * keeps that word for the controller's own problems.
 */
export function cardLine(
  host: Pick<Host, 'id' | 'doctor'>,
  health: Pick<HealthSummary, 'stale'>,
): CardLine {
  const plain: CardLine = { text: '', firstId: null, href: `/hosts/${host.id}` };
  if (!host.doctor || health.stale) return plain;
  const list = attention(host.doctor);
  const first = list[0];
  if (!first) return plain;
  const [a, b] = list.map((r) => clip(r.title || r.id));
  const text =
    list.length === 1
      ? `${a}`
      : list.length === 2
        ? `${a} and ${b}`
        : `${a}, ${b} and ${list.length - 2} more`;
  const firstId = first.id;
  return { text, firstId, href: `/hosts/${host.id}#${encodeURIComponent(firstId)}` };
}
