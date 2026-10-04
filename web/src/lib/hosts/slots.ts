/**
 * How many runners a host takes before any throttle.
 *
 * `slots` is the controller's count: the host's capacity, or -- where the
 * operator has given the host a standard runner size -- as many runners of that
 * size as its machine holds, never more than the capacity. `capacity` on its
 * own is only the operator's ceiling, and over-counts on a host that is sized
 * by a profile, so nothing that counts runners reads it directly. A host held
 * by a cached or older response has no `slots` to say, and its capacity is what
 * it always was.
 */
import type { Host } from '../api/types';

export function slotsOf(host: Pick<Host, 'slots' | 'capacity'>): number {
  return host.slots ?? host.capacity ?? 0;
}
