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

/** -- keep in step with diskCheck in internal/hosttune/checks.go */
export const DISK_LOW_PERCENT = 10;
/** -- keep in step with diskCheck in internal/hosttune/checks.go (10 GiB, in MiB) */
export const DISK_LOW_FLOOR_MB = 10 * 1024;

/**
 * Whether the card should call the disk low: the rule the Work directory free
 * space check warns by, so the amber figure and the Warning row never disagree.
 *
 * Strictly less than on both limbs, as the check is, so exactly 10% or exactly
 * 10 GiB free is not low. Zero total means the agent did not measure it.
 */
export function diskIsLow(freeMb: number, totalMb: number): boolean {
  if (!(totalMb > 0)) return false;
  return (freeMb * 100) / totalMb < DISK_LOW_PERCENT || freeMb < DISK_LOW_FLOOR_MB;
}

/**
 * Where the card's disk figure leads: the check that reads the same filesystem.
 *
 * Null when there is no honest destination -- no report, a container's partial
 * view, a stale report, or a report without the check -- so the figure stays
 * plain text rather than linking to a row that is not there.
 */
export function diskLink(
  host: Pick<Host, 'id' | 'doctor'>,
  health: Pick<HealthSummary, 'stale'>,
): string | null {
  const report = host.doctor;
  if (!host.id || !report || report.container || health.stale) return null;
  if (!report.results.some((r) => r.id === 'disk.space')) return null;
  return `/hosts/${encodeURIComponent(host.id)}#disk.space`;
}
