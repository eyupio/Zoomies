/**
 * A host's runner profile: from the form to the request and back, the rules the
 * API will hold it to, and the slots it gives.
 *
 * Pure, and imported by a unit test. The slot count is the controller's own
 * arithmetic (`Host.Slots` in the store) worked out again, for one use: the
 * sentence under the fields while the operator is still typing. It runs against
 * the machine less its reserve as the controller last reported it, so a reserve
 * changed in the other dialog is not in it until that is saved. Once the profile
 * is saved the host's card shows the controller's own figure, and where the two
 * ever disagree that one is right.
 */
import type { Host, RunnerProfile } from '../api/types';
import { MIN_TMPFS_MB, cpuLabel, memoryLabel } from '../pools/sizing';

/**
 * The figures a profile holds. Zero is "not set": the fleet's setting
 * stands in for it, which is how every host starts. The last two are not sizes
 * but the host's say over pools' in-memory folders, and are "not said" the same
 * way: off is false and no ceiling is zero.
 */
export interface ProfileFigures {
  /** The least a runner is given here. */
  minCpus: number;
  minMemoryMb: number;
  /** One runner's size here, for a pool that takes its size from the host. */
  standardCpus: number;
  standardMemoryMb: number;
  /** The most CPU one runner may use, its guaranteed share and any CPU lent to it together. */
  burstMaxCpus: number;
  /** Keep every in-memory folder off this host, whatever a pool asks for. */
  tmpfsOff: boolean;
  /** The most any one in-memory folder may be here, in MB; zero is no host ceiling. */
  tmpfsMaxMb: number;
  /**
   * The size each in-memory folder is asked for here when a pool leaves it to size
   * itself, in MB, in place of the built-in default; zero is the default. The
   * folder-sized counterpart of a standard runner size.
   */
  tmpfsWorkMb: number;
  tmpfsTmpMb: number;
  tmpfsDaemonMb: number;
}

export const NO_FIGURES: Readonly<ProfileFigures> = {
  minCpus: 0,
  minMemoryMb: 0,
  standardCpus: 0,
  standardMemoryMb: 0,
  burstMaxCpus: 0,
  tmpfsOff: false,
  tmpfsMaxMb: 0,
  tmpfsWorkMb: 0,
  tmpfsTmpMb: 0,
  tmpfsDaemonMb: 0,
};

/**
 * The floors under any runner, which the API enforces on a number somebody
 * typed: below them the runner binary cannot keep up with its own job, or is
 * killed before it takes one.
 */
export const MIN_RUNNER_CPUS = 0.25;
export const MIN_RUNNER_MEMORY_MB = 512;

/** The form's figures for a host's stored profile; every field it does not name is zero. */
export function figuresOf(profile: RunnerProfile | null | undefined): ProfileFigures {
  return {
    minCpus: profile?.minimum?.cpus ?? 0,
    minMemoryMb: profile?.minimum?.memory_mb ?? 0,
    standardCpus: profile?.standard?.cpus ?? 0,
    standardMemoryMb: profile?.standard?.memory_mb ?? 0,
    burstMaxCpus: profile?.standard?.burst_max_cpus ?? 0,
    tmpfsOff: profile?.tmpfs?.disabled === true,
    tmpfsMaxMb: profile?.tmpfs?.max_mb ?? 0,
    tmpfsWorkMb: profile?.tmpfs?.work_mb ?? 0,
    tmpfsTmpMb: profile?.tmpfs?.tmp_mb ?? 0,
    tmpfsDaemonMb: profile?.tmpfs?.daemon_mb ?? 0,
  };
}

/**
 * The request's `runner_profile`: only the figures that are set, and `{}` where
 * none is. The API replaces the whole profile with what it is sent, so an empty
 * object is how "follow the fleet everywhere" is said, and a field left out is
 * one the host stops overriding.
 */
export function profileBody(f: ProfileFigures): RunnerProfile {
  const minimum: NonNullable<RunnerProfile['minimum']> = {};
  if (f.minCpus > 0) minimum.cpus = f.minCpus;
  if (f.minMemoryMb > 0) minimum.memory_mb = f.minMemoryMb;
  const standard: NonNullable<RunnerProfile['standard']> = {};
  if (f.standardCpus > 0) standard.cpus = f.standardCpus;
  if (f.standardMemoryMb > 0) standard.memory_mb = f.standardMemoryMb;
  if (f.burstMaxCpus > 0) standard.burst_max_cpus = f.burstMaxCpus;
  const tmpfs: NonNullable<RunnerProfile['tmpfs']> = {};
  if (f.tmpfsOff) tmpfs.disabled = true;
  // A ceiling on a host that keeps the folders off is a contradiction the API
  // refuses, and nothing to send: the toggle is the stronger of the two.
  if (f.tmpfsMaxMb > 0 && !f.tmpfsOff) tmpfs.max_mb = f.tmpfsMaxMb;
  // The same for the sizes: there is no folder to size where they are off.
  if (!f.tmpfsOff) {
    if (f.tmpfsWorkMb > 0) tmpfs.work_mb = f.tmpfsWorkMb;
    if (f.tmpfsTmpMb > 0) tmpfs.tmp_mb = f.tmpfsTmpMb;
    if (f.tmpfsDaemonMb > 0) tmpfs.daemon_mb = f.tmpfsDaemonMb;
  }
  const body: RunnerProfile = {};
  if (Object.keys(minimum).length > 0) body.minimum = minimum;
  if (Object.keys(standard).length > 0) body.standard = standard;
  if (Object.keys(tmpfs).length > 0) body.tmpfs = tmpfs;
  return body;
}

/** True when nothing is set, which is a host that follows the fleet in everything. */
export function isUnset(f: ProfileFigures): boolean {
  return (
    f.minCpus <= 0 &&
    f.minMemoryMb <= 0 &&
    f.standardCpus <= 0 &&
    f.standardMemoryMb <= 0 &&
    f.burstMaxCpus <= 0 &&
    !f.tmpfsOff &&
    f.tmpfsMaxMb <= 0 &&
    f.tmpfsWorkMb <= 0 &&
    f.tmpfsTmpMb <= 0 &&
    f.tmpfsDaemonMb <= 0
  );
}

/** Whether two sets of figures say the same thing, for a Save that has nothing to save. */
export function sameFigures(a: ProfileFigures, b: ProfileFigures): boolean {
  return (
    a.minCpus === b.minCpus &&
    a.minMemoryMb === b.minMemoryMb &&
    a.standardCpus === b.standardCpus &&
    a.standardMemoryMb === b.standardMemoryMb &&
    a.burstMaxCpus === b.burstMaxCpus &&
    a.tmpfsOff === b.tmpfsOff &&
    a.tmpfsMaxMb === b.tmpfsMaxMb &&
    a.tmpfsWorkMb === b.tmpfsWorkMb &&
    a.tmpfsTmpMb === b.tmpfsTmpMb &&
    a.tmpfsDaemonMb === b.tmpfsDaemonMb
  );
}

/** What a host's machine is, as far as it has said. Zero is "not measured". */
export interface MachineShape {
  cpus: number;
  memoryMb: number;
  /** The machine less its reserve, with the documented floors applied: what the scheduler places onto. */
  allocatableCpus: number;
  allocatableMemoryMb: number;
}

export function machineOf(host: Host | null | undefined): MachineShape {
  return {
    cpus: host?.cpus ?? 0,
    memoryMb: host?.memory_mb ?? 0,
    allocatableCpus: host?.allocatable_cpus ?? 0,
    allocatableMemoryMb: host?.allocatable_memory_mb ?? 0,
  };
}

/**
 * The rules the API will hold a profile to, keyed by the field names it answers
 * with so a server refusal and one found here sit under the same input.
 *
 * Found here so the operator learns while typing, not after Save: the checks are
 * the server's own, in the same order, and the server still makes them.
 */
export function profileErrors(f: ProfileFigures, machine: MachineShape): Record<string, string> {
  const errors: Record<string, string> = {};
  const key = (field: string) => `runner_profile.${field}`;

  const cpuFloor = (field: string, value: number): void => {
    if (value > 0 && value < MIN_RUNNER_CPUS)
      errors[key(field)] =
        'A runner needs at least a quarter of a core. Leave it empty to follow the fleet.';
  };
  const memoryFloor = (field: string, value: number): void => {
    if (value > 0 && value < MIN_RUNNER_MEMORY_MB)
      errors[key(field)] = 'A runner needs at least 512 MB. Leave it empty to follow the fleet.';
  };
  cpuFloor('minimum.cpus', f.minCpus);
  memoryFloor('minimum.memory_mb', f.minMemoryMb);
  cpuFloor('standard.cpus', f.standardCpus);
  memoryFloor('standard.memory_mb', f.standardMemoryMb);
  cpuFloor('standard.burst_max_cpus', f.burstMaxCpus);

  // A folder smaller than the floor is not one a checkout fits in. The ceiling
  // is moot on a host that keeps the folders off, so it is only held to the floor
  // while it is in force.
  if (!f.tmpfsOff && f.tmpfsMaxMb > 0 && f.tmpfsMaxMb < MIN_TMPFS_MB)
    errors[key('tmpfs.max_mb')] =
      `A ceiling below ${MIN_TMPFS_MB} MB leaves no folder a checkout fits in. Leave it empty to set none, or keep the folders off here instead.`;

  // The sizes a folder is asked for here are held to the same floor.
  if (!f.tmpfsOff) {
    for (const [name, mb] of [
      ['tmpfs.work_mb', f.tmpfsWorkMb],
      ['tmpfs.tmp_mb', f.tmpfsTmpMb],
      ['tmpfs.daemon_mb', f.tmpfsDaemonMb],
    ] as const) {
      if (mb > 0 && mb < MIN_TMPFS_MB)
        errors[key(name)] =
          `A folder below ${MIN_TMPFS_MB} MB is not one a checkout fits in. Leave it empty to use the default.`;
    }
  }

  // A minimum above the standard is a floor above the size it floors.
  if (
    !errors[key('minimum.cpus')] &&
    f.minCpus > 0 &&
    f.standardCpus > 0 &&
    f.minCpus > f.standardCpus
  )
    errors[key('minimum.cpus')] =
      `The minimum is above the standard, ${cpuLabel(f.standardCpus)}. A runner is never given less than the minimum, so it has to be the smaller of the two.`;
  if (
    !errors[key('minimum.memory_mb')] &&
    f.minMemoryMb > 0 &&
    f.standardMemoryMb > 0 &&
    f.minMemoryMb > f.standardMemoryMb
  )
    errors[key('minimum.memory_mb')] =
      `The minimum is above the standard, ${memoryLabel(f.standardMemoryMb)}. A runner is never given less than the minimum, so it has to be the smaller of the two.`;
  // The ceiling is the most a runner may use, its own share included.
  if (
    !errors[key('standard.burst_max_cpus')] &&
    f.burstMaxCpus > 0 &&
    f.standardCpus > 0 &&
    f.burstMaxCpus < f.standardCpus
  )
    errors[key('standard.burst_max_cpus')] =
      `The ceiling is the most a runner may use, its own share included, so it has to be at least the standard, ${cpuLabel(f.standardCpus)}.`;

  // What the machine can give. A figure above all of it could never run one
  // runner, which is the host refusing every pool.
  if (machine.cpus > 0) {
    const room = machine.allocatableCpus;
    if (!errors[key('standard.cpus')] && f.standardCpus > room + 1e-6)
      errors[key('standard.cpus')] =
        `This host has ${cpuLabel(room)} to place runners on once its reserve is held back, so a standard of ${cpuLabel(f.standardCpus)} could never run a runner here.`;
    if (!errors[key('minimum.cpus')] && f.minCpus > room + 1e-6)
      errors[key('minimum.cpus')] =
        `This host has ${cpuLabel(room)} to place runners on once its reserve is held back, which is less than this minimum.`;
  }
  if (machine.memoryMb > 0) {
    const room = machine.allocatableMemoryMb;
    if (!errors[key('standard.memory_mb')] && f.standardMemoryMb > room)
      errors[key('standard.memory_mb')] =
        `This host has ${memoryLabel(room)} to place runners on once its reserve is held back, so a standard of ${memoryLabel(f.standardMemoryMb)} could never run a runner here.`;
    if (!errors[key('minimum.memory_mb')] && f.minMemoryMb > room)
      errors[key('minimum.memory_mb')] =
        `This host has ${memoryLabel(room)} to place runners on once its reserve is held back, which is less than this minimum.`;
  }
  return errors;
}

/** What sets a derived slot count: the machine's cores or memory, or the operator's own capacity. */
export type SlotLimit = '' | 'cpu' | 'memory' | 'capacity';

export interface SlotCount {
  slots: number;
  /** What the machine holds of the standard before the capacity is applied. The capacity itself where nothing was divided. */
  held: number;
  /** What sets the count where a standard size gave it. Empty where nothing does. */
  limitedBy: SlotLimit;
  /** Whether a standard size gave the count, rather than the capacity alone. */
  derived: boolean;
}

/** Absorbs the rounding of CPU that does not divide evenly; the store's own constant. */
const SLOT_EPSILON = 1e-6;

/**
 * How many runners a host takes with this standard size: as many as its
 * allocatable machine holds, never more than its capacity.
 *
 * Only a standard the host was given counts. The fleet's default size is what a
 * pool that takes its size from the host is given where the host names none, and
 * it does not set slots: a host that says nothing keeps the capacity it has
 * always had. A capacity of zero is how an operator pauses a host, and a size
 * never un-pauses one.
 */
export function slotsFor(machine: MachineShape, capacity: number, f: ProfileFigures): SlotCount {
  if (capacity <= 0) return { slots: 0, held: 0, limitedBy: '', derived: false };
  if (f.standardCpus <= 0 && f.standardMemoryMb <= 0)
    return { slots: capacity, held: capacity, limitedBy: '', derived: false };
  let byCpu = capacity;
  let byMemory = capacity;
  let bound = false;
  if (f.standardCpus > 0 && machine.cpus > 0) {
    byCpu = Math.floor((machine.allocatableCpus + SLOT_EPSILON) / f.standardCpus);
    bound = true;
  }
  if (f.standardMemoryMb > 0 && machine.memoryMb > 0) {
    byMemory = Math.floor(machine.allocatableMemoryMb / f.standardMemoryMb);
    bound = true;
  }
  // A host that has not measured its machine has nothing to divide, and is
  // placed by its capacity exactly as it was before it had a size.
  if (!bound) return { slots: capacity, held: capacity, limitedBy: '', derived: false };
  const held = Math.max(Math.min(byCpu, byMemory), 0);
  const slots = Math.min(capacity, held);
  let limitedBy: SlotLimit = '';
  if (slots < capacity) limitedBy = byCpu <= byMemory ? 'cpu' : 'memory';
  else if (capacity < held) limitedBy = 'capacity';
  return { slots, held, limitedBy, derived: true };
}
