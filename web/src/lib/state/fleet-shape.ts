/**
 * When a frame changes the fleet's shape, and when it only moves a live figure.
 *
 * Apart from the cache so a test can reach it: the cache itself is runes, which
 * a plain unit test cannot load, and this is the one rule in it that decides
 * how often a page asks the controller again.
 */

/**
 * The fields a frame may change without the fleet's shape having changed: the
 * live metrics an agent reports on every heartbeat.
 *
 * The memory valve's two views are on the list because they are two more of
 * them. A host's `memory_pool` carries the free memory its agent measured, so it
 * moves with every heartbeat of every host with a pool that uses the valve; a
 * runner's `memory_resource` moves as its job climbs, at the same cadence, from
 * the same heartbeat's sample. Left off, each of those frames would read as a
 * change to the fleet and every Runners and Pools grid would fetch its page
 * again -- a round trip per host or runner per heartbeat, for pills and a card
 * that read the figures from the cache.
 */
export const VOLATILE: ReadonlySet<string> = new Set([
  'cpu_percent',
  'memory_bytes',
  'last_heartbeat',
  'resource_sample',
  'usage',
  'memory_pool',
  'memory_resource',
]);

/** True when two rows differ in anything but a live metric. */
export function shapeDiffers(
  a: Record<string, unknown> | undefined,
  b: Record<string, unknown>,
): boolean {
  if (!a) return true;
  for (const key of new Set([...Object.keys(a), ...Object.keys(b)])) {
    if (VOLATILE.has(key)) continue;
    if (JSON.stringify(a[key]) !== JSON.stringify(b[key])) return true;
  }
  return false;
}
