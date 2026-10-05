import type { Body, Problem } from '$lib/api/types';

type Resources = NonNullable<Body<'updatePool'>['resources']>;

/**
 * The resources to send for applying a suggested sidecar share.
 *
 * An update replaces a pool's resources whole, so the pool's own settings go back
 * as they were with only the shares the notice proposes changed: sending the shares
 * alone would clear its minimum and every limit it typed, in the click that was
 * meant to move a percentage. A resource the notice has nothing for keeps whatever
 * share the pool has.
 */
export function withSuggestedShare(
  current: Resources | undefined,
  change: NonNullable<Problem['daemon_share']>,
): Resources {
  const next: Resources = { ...current };
  if (change.cpu_percent) next.daemon_cpu_share_percent = change.cpu_percent;
  if (change.memory_percent) next.daemon_memory_share_percent = change.memory_percent;
  return next;
}
