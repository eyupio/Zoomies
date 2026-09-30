/**
 * What makes two reports of a problem the same problem.
 *
 * It lives apart from the two features that ask -- the drawer, which remembers
 * which problems an operator has read, and the Overview's feed, which reports
 * one the first time it is raised -- because a second definition of "the same
 * fault" is how those two would come to disagree about it, and because this
 * one is plain enough to test in Node.
 */
import type { Problem } from '../api/types';

/**
 * A pool.dangerous problem is raised once per weakened setting on the same
 * pool -- persistent runners, the host docker socket, docker-in-docker, root
 * -- so it shares a code and a target across genuinely different problems,
 * with the title the only thing that separates them. The two host codes below
 * are raised once per container backend on the same host (docker and podman)
 * and name the backend in the title for the same reason. No other code does
 * this, and every one of those titles is a fixed sentence, never one built
 * from a count that changes between reconciliation passes.
 */
const CODES_DISTINGUISHED_BY_TITLE: ReadonlySet<string> = new Set([
  'pool.dangerous',
  'host.limits_unenforceable',
  'host.shared_folder_unmounted',
]);

/**
 * The identity of a problem across refreshes.
 *
 * Deliberately built from what the problem is *about* rather than from its
 * prose: "5 webhook deliveries were rejected" becoming "6 webhook deliveries
 * were rejected" is the same fault, and re-asking about it every minute is the
 * nagging the dismissals exist to stop. That is why the title only joins the
 * key for the handful of codes that need it to tell two simultaneous problems
 * apart -- folding it in for every pool-targeted problem would make
 * "21 jobs waiting" and "22 jobs waiting" different faults, undoing a
 * dismissal every time the queue length moved.
 */
export function problemKey(problem: Problem): string {
  return [
    problem.code,
    problem.target_kind ?? '',
    problem.target_id ?? '',
    problem.setting ?? '',
    CODES_DISTINGUISHED_BY_TITLE.has(problem.code) ? (problem.title ?? '') : '',
  ].join('|');
}

/**
 * The identity of every problem of one kind, whatever it is about: what a
 * snooze of "all like this" is kept under. An operator who knows every pool
 * will be short of capacity through a planned outage wants to put that away
 * once, not once per pool -- and a pool that runs short during the outage is
 * covered too, which a per-problem snooze could never do. It is prefixed so
 * it can never equal a problemKey.
 */
export function problemTypeKey(problem: Problem): string {
  return `type:${problem.code}`;
}
