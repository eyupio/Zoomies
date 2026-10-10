import { Activity, Handshake, Hourglass, LoaderCircle, type LucideIcon } from '@lucide/svelte';

/**
 * How an Off status icon moves, chosen by glyph rather than by state: one
 * glyph means one thing wherever it appears, so the thing being set up turns
 * whether it is a runner or a machine. Only work still in progress moves, and
 * quietly; whatever is waiting or finished holds still, so a page of idle
 * runners is a calm one and a failure never draws the eye by moving.
 */
export type OffMotion = 'spin' | 'trace' | 'breathe' | 'turn' | 'none';

const MOTIONS = new Map<LucideIcon, OffMotion>([
  [LoaderCircle, 'spin'],
  [Activity, 'trace'],
  [Handshake, 'breathe'],
  [Hourglass, 'turn'],
]);

export function offMotion(icon: LucideIcon): OffMotion {
  return MOTIONS.get(icon) ?? 'none';
}
