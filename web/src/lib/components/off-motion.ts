import {
  Activity,
  Handshake,
  Hourglass,
  LoaderCircle,
  TrendingUp,
  Zap,
  type LucideIcon,
} from '@lucide/svelte';

/**
 * How an Off status icon moves, chosen by glyph rather than by state: one
 * glyph means one thing wherever it appears, so the thing being set up turns
 * whether it is a runner or a machine. Only work still in progress moves, and
 * quietly; whatever is waiting or finished holds still, so a page of idle
 * runners is a calm one and a failure never draws the eye by moving. The two
 * boosts are work too: the trend line lifts now and then and the bolt charges,
 * while throttling, which is CPU being held back, holds still like the rest.
 */
export type OffMotion = 'spin' | 'trace' | 'breathe' | 'turn' | 'lift' | 'charge' | 'none';

const MOTIONS = new Map<LucideIcon, OffMotion>([
  [LoaderCircle, 'spin'],
  [Activity, 'trace'],
  [Handshake, 'breathe'],
  [Hourglass, 'turn'],
  [TrendingUp, 'lift'],
  [Zap, 'charge'],
]);

export function offMotion(icon: LucideIcon): OffMotion {
  return MOTIONS.get(icon) ?? 'none';
}
