/**
 * The name a new pool arrives with, and the dice that roll another one.
 *
 * A blank name field is the first thing an operator meets in the wizard, and
 * the name they invent to get past it -- "test", "pool1" -- is then in every
 * runner name, every audit line and every `runs-on` for the life of the fleet.
 * Renaming a pool later does not un-write the workflows that already point at
 * it. So the wizard fills one in instead.
 *
 * The generated name has two halves and both earn their place. The kennel word
 * makes one pool tellable from another at a glance, which a list of
 * `zoomies-docker-linux-x64-2` never is; the infrastructure half says what the
 * pool actually lands on, which is what an operator wants to know before they
 * send a job to it. The brand prefix is not optional: it is what marks a
 * runner in the GitHub UI as ours rather than GitHub's.
 */

import type { BackendKind, Host } from '$lib/api/types';
import { RUNNER_NAME_PREFIX, sanitizeLabel } from '$lib/brand';

/**
 * Cocker spaniels, as cocker spaniels are actually named.
 *
 * The mark is a cocker spaniel doing zoomies, so the fleet is a kennel. Every
 * word here is one segment of lowercase letters -- no spaces, nothing to
 * sanitise away -- and short enough that the infrastructure half of the name
 * survives the length budget below.
 */
export const KENNEL: readonly string[] = [
  'banjo',
  'biscuit',
  'boogie',
  'bramble',
  'bubbles',
  'cocoa',
  'crumpet',
  'custard',
  'digby',
  'disco',
  'flapjack',
  'gizmo',
  'hazel',
  'jellybean',
  'jitterbug',
  'maple',
  'marmalade',
  'muffin',
  'noodle',
  'pancake',
  'pepper',
  'pickles',
  'popcorn',
  'rascal',
  'rocket',
  'rusty',
  'scampi',
  'toffee',
  'truffle',
  'waffles',
  'wiggles',
  'ziggy',
];

/**
 * How long a generated name may be.
 *
 * `sanitizeLabel` truncates at 40 characters, so a longer name would produce a
 * label that is a chopped-off version of it -- two strings an operator has to
 * hold in their head at once, for no gain.
 */
const MAX_NAME = 40;

/** The value every entry agrees on, or "" when they do not all agree. */
function agreed(values: readonly (string | undefined)[]): string {
  let found = '';
  for (const value of values) {
    const clean = sanitizeLabel(value ?? '');
    if (clean === '') continue;
    if (found === '') found = clean;
    else if (found !== clean) return '';
  }
  return found;
}

function offersBackend(host: Host, backend: BackendKind): boolean {
  const info = (host.backend_info ?? []).find((entry) => entry.kind === backend);
  return info?.available === true || (host.backends ?? []).includes(backend);
}

/**
 * What the runners of such a pool would run on, most specific part last.
 *
 * The operating system and architecture come from the connected hosts, and
 * only when every host that could run this pool reports the same ones: a fleet
 * of Linux and Windows hosts gets a name that says neither, because a name
 * that picked one would be a name that lies about half the fleet. With no
 * hosts connected yet -- which is where the first pool is created from -- the
 * backend is all there is to say, and that is enough.
 */
export function infrastructure(backend: BackendKind, hosts: readonly Host[]): string[] {
  const parts = [sanitizeLabel(backend)];
  const offering = hosts.filter((host) => offersBackend(host, backend));
  const from = offering.length > 0 ? offering : hosts;
  const os = agreed(from.map((host) => host.os));
  const arch = agreed(from.map((host) => host.arch));
  if (os !== '') parts.push(os);
  if (arch !== '') parts.push(arch);
  return parts.filter((part) => part !== '');
}

/**
 * The name for a pool of this word on this infrastructure.
 *
 * Over budget, the least specific parts go first -- architecture, then
 * operating system -- because the kennel word is what an operator recognises
 * the pool by and the backend is what they most need to know about it.
 */
export function poolName(word: string, backend: BackendKind, hosts: readonly Host[]): string {
  const parts = [sanitizeLabel(word) || 'pool', ...infrastructure(backend, hosts)];
  while (parts.length > 1) {
    const candidate = RUNNER_NAME_PREFIX + parts.join('-');
    if (candidate.length <= MAX_NAME) return candidate;
    parts.pop();
  }
  return (RUNNER_NAME_PREFIX + parts.join('-')).slice(0, MAX_NAME).replace(/-+$/, '');
}

/**
 * A word from the kennel, never the one already on screen.
 *
 * Rolling the dice and getting the same name back reads as a broken button,
 * so the previous word is taken out of the hat rather than left to chance.
 */
export function spinWord(previous?: string): string {
  const choices = KENNEL.filter((word) => word !== previous);
  const pool = choices.length > 0 ? choices : KENNEL;
  return pool[Math.floor(Math.random() * pool.length)] ?? 'biscuit';
}
