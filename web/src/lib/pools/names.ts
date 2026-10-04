/**
 * The name a new pool arrives with, and the dice that roll another one.
 *
 * A blank name field is the first thing an operator meets in the wizard, and
 * the name they invent to get past it -- "test", "pool1" -- is then in every
 * runner name, every audit line and every `runs-on` for the life of the fleet.
 * Renaming a pool later does not un-write the workflows that already point at
 * it. So the wizard fills one in instead.
 *
 * The name it fills in is the pool's shape -- `zoomies-4vcpu-ubuntu-2404`, the
 * grammar in docs/naming.md -- because that is what a workflow author reading a
 * `runs-on` needs to know before they send a job to it, and because it is the
 * same name `zoomies init` suggests for a machine of that size. The wizard used
 * to lead with a kennel word instead, which left an operator who met both with
 * two conventions for one thing.
 *
 * The spaniel is still here, and still earns its place: it is what tells two
 * pools of the same shape apart, and what names a pool created before anything
 * about it is known. The brand prefix is not optional either -- it is what marks
 * a runner in the GitHub UI as ours rather than GitHub's.
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
 * The size every pool in this fleet gets unless it says otherwise.
 *
 * It is passed in rather than assumed because it is a fleet setting, and
 * because of what the name does with it: a figure every pool shares says
 * nothing about any of them. See `shape`.
 */
export interface FleetSize {
  cpus?: number;
  memory_mb?: number;
}

/**
 * The parts of a draft a name is made of.
 *
 * Structural rather than the wizard's own PoolDraft, so this module owes the
 * wizard nothing: a draft satisfies it by having the fields, and the naming
 * rules can be read without opening a component.
 */
export interface NameShape {
  backend: BackendKind;
  /**
   * How the pool decides its runners' size. A pool that leaves it to the host
   * -- by a share of the machine, or by the standard size the host's own
   * profile names -- has no size to be named for: its runners are 3.8 cores
   * here and 7.6 on the next machine to join, so `4vcpu` in the name would be a
   * promise it stopped keeping the day the fleet grew.
   */
  sizing: 'automatic' | 'fixed' | 'profile';
  cpus: string;
  memory_mb: string;
  platform_os: string;
  platform_os_version: string;
  platform_arch: string;
}

/**
 * A version with its dots taken out: 24.04 becomes 2404, 12 stays 12.
 *
 * The same rule as naming.CompactVersion in Go, because the name a pool is
 * suggested here has to be the name `zoomies init` would suggest for the same
 * machine -- an operator who meets both should meet one convention.
 */
function compactVersion(v: string): string {
  return v
    .trim()
    .split(/[._]/)
    .slice(0, 2)
    .map((part) => part.replace(/\D/g, ''))
    .join('');
}

/**
 * What a pool of this draft is made of, most significant part first.
 *
 * This is the shape half of the grammar in docs/naming.md: how much machine,
 * running what. The draft's own fields win, and where it has not said, the
 * connected hosts answer -- but only when every host that could run this pool
 * agrees, because a name that picked one of two answers is a name that lies
 * about half the fleet.
 *
 * Architecture follows the same rule the rest of the grammar does: amd64 is the
 * overwhelming default and saying it on every pool stops the word carrying
 * information, so only the others are spelled out.
 */
export function shape(
  draft: NameShape,
  hosts: readonly Host[],
  fleetDefault?: FleetSize | null,
): string[] {
  const parts: string[] = [];

  // Size is stated when the pool has one of its own and it is not what every
  // pool here gets. Naming it unconditionally would put the same two words on
  // every pool in the fleet -- which is the argument that keeps amd64 out of
  // these names, applied to the figure it applies to twice as hard. A pool
  // that asks for more than the fleet's usual is exactly the pool a workflow
  // author is choosing, and it says so.
  //
  // A pool that leaves the size to its host says nothing at all: the figure
  // the sliders happen to be holding is not this pool's, and a name built from
  // it would advertise a size the pool does not have on any machine.
  if (draft.sizing === 'fixed') {
    const cpus = Math.ceil(Number(draft.cpus));
    if (Number.isFinite(cpus) && cpus > 0 && cpus !== defaultCPUs(fleetDefault)) {
      parts.push(`${cpus}vcpu`);
    }
    const gb = Math.floor(Number(draft.memory_mb) / 1024);
    if (Number.isFinite(gb) && gb > 0 && gb !== defaultGB(fleetDefault)) parts.push(`${gb}gb`);
  }

  const offering = hosts.filter((host) => offersBackend(host, draft.backend));
  const from = offering.length > 0 ? offering : hosts;

  const os = sanitizeLabel(draft.platform_os) || agreed(from.map((host) => host.platform?.os));
  if (os !== '') {
    parts.push(os);
    const version =
      compactVersion(draft.platform_os_version) ||
      compactVersion(unanimous(from.map((host) => host.platform?.os_version)));
    if (version !== '') parts.push(version);
  }

  const arch =
    sanitizeLabel(draft.platform_arch) || agreed(from.map((host) => host.platform?.arch));
  if (arch !== '' && arch !== 'amd64') parts.push(arch);

  return parts;
}

/**
 * The fleet's default in the units a name is written in: whole cores rounded
 * the way a pool's own figure is, and whole gigabytes. A fleet that has not
 * said, or a call that did not pass it, has no default to compare against and
 * every figure is worth stating.
 */
function defaultCPUs(fleetDefault?: FleetSize | null): number {
  const cpus = fleetDefault?.cpus;
  return cpus !== undefined && cpus > 0 ? Math.ceil(cpus) : -1;
}

function defaultGB(fleetDefault?: FleetSize | null): number {
  const mb = fleetDefault?.memory_mb;
  return mb !== undefined && mb > 0 ? Math.floor(mb / 1024) : -1;
}

/** The raw value every entry agrees on, before sanitising, or "". */
function unanimous(values: readonly (string | undefined)[]): string {
  let found = '';
  for (const value of values) {
    const clean = (value ?? '').trim();
    if (clean === '') continue;
    if (found === '') found = clean;
    else if (found !== clean) return '';
  }
  return found;
}

/**
 * The name for a pool of this draft, and the kennel word it falls back on.
 *
 * A pool is named for its shape, which is the same name `zoomies init` prints
 * for a machine of that size and platform -- so the wizard and the installer
 * stop offering an operator two conventions for the same thing.
 *
 * The kennel word earns its place where the shape cannot carry the name on its
 * own. A pool created before anything about it is known has no shape to be
 * named for, and two pools of the same shape are two pools GitHub needs two
 * names for; in both cases a spaniel tells them apart, which
 * `zoomies-4vcpu-ubuntu-2404-2` never would.
 *
 * Over budget, the least significant parts go first -- architecture, then the
 * release, then the operating system -- because the size is what an operator is
 * choosing between and the word is what they recognise the pool by.
 */
export function poolName(
  word: string,
  draft: NameShape,
  hosts: readonly Host[],
  taken: readonly string[] = [],
  fleetDefault?: FleetSize | null,
): string {
  const parts = named(draft, hosts, fleetDefault);
  const spelled = new Set(taken.map((name) => sanitizeLabel(name)));
  const bare = fit(parts);
  if (parts.length > 0 && !spelled.has(sanitizeLabel(bare))) return bare;
  return nicknamedPoolName(word, draft, hosts, fleetDefault);
}

/**
 * The same name with a spaniel in it whether it needs one or not.
 *
 * This is what the dice ask for. A pool whose shape is already unique is
 * offered a name without a word, so a button that only sometimes changed
 * anything would read as broken -- and an operator who wants a handle for a
 * pool is entitled to one without having to invent it themselves.
 */
export function nicknamedPoolName(
  word: string,
  draft: NameShape,
  hosts: readonly Host[],
  fleetDefault?: FleetSize | null,
): string {
  return fit(named(draft, hosts, fleetDefault), sanitizeLabel(word) || 'pool');
}

/**
 * The shape, plus the marker a process-backend pool carries.
 *
 * That backend is a different thing to ask for at the same shape -- its jobs get
 * the host itself rather than a container -- and a workflow author choosing
 * between two pools needs the name to say which. `zoomies init` marks it the
 * same way.
 */
function named(
  draft: NameShape,
  hosts: readonly Host[],
  fleetDefault?: FleetSize | null,
): string[] {
  const parts = shape(draft, hosts, fleetDefault);
  if (draft.backend === 'process') parts.push('host');
  return parts;
}

/**
 * These parts as a branded name, dropping the least significant until it fits.
 *
 * `keep` is never dropped: when it is there at all it is the only thing telling
 * this pool from one of the same shape, so a budget that spent it would produce
 * the name it was added to avoid.
 */
function fit(parts: readonly string[], keep = ''): string {
  const kept = [...parts];
  const tail = keep === '' ? '' : `-${keep}`;
  while (kept.length > 0) {
    const candidate = RUNNER_NAME_PREFIX + kept.join('-') + tail;
    if (candidate.length <= MAX_NAME) return candidate;
    kept.pop();
  }
  return (RUNNER_NAME_PREFIX + keep).slice(0, MAX_NAME).replace(/-+$/, '');
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
