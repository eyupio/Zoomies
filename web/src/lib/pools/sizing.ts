/**
 * The notches a runner's size moves between, and the words for them.
 *
 * Pure, and imported by a unit test. The sliders exist because the three
 * figures they carry -- how much of a machine one runner gets, and how much
 * disk its cache may keep -- are the settings an operator is least equipped to
 * type into an empty box: a number field accepts 3000 MB as readily as 4096,
 * and neither the field nor the pool says which of them the hosts can actually
 * back. A notch is a value somebody has a reason to choose.
 *
 * The arithmetic that says how many runners a fleet has room for is the
 * controller's, not this file's: what a runner is charged is not always what
 * the pool says -- a docker-in-docker slot is charged for its sidecar too --
 * and a browser that worked it out again would disagree with where the fleet
 * actually places runners. This file's counting is only what a slider needs
 * before the server's answer lands.
 */

/**
 * The CPU notches: shares of a core below one, then whole cores, then the
 * steps a build machine is actually bought in. A quarter core is the floor the
 * API enforces -- below it the runner binary cannot keep up with its own job.
 */
export const CPU_NOTCHES: readonly number[] = [
  0.25, 0.5, 1, 1.5, 2, 3, 4, 6, 8, 12, 16, 24, 32, 48, 64,
];

/** The memory notches, in megabytes. 512 MB is the API's floor. */
export const MEMORY_NOTCHES: readonly number[] = [
  512, 1024, 2048, 3072, 4096, 6144, 8192, 12288, 16384, 24576, 32768, 49152, 65536, 98304, 131072,
];

/**
 * The swap notches, in megabytes, with zero for none. Swap is the memory valve's
 * last resort: it turns a kill into a slowdown, and some operators would rather
 * have the kill, so none is the answer a pool gives until somebody asks.
 */
export const SWAP_NOTCHES: readonly number[] = [0, 512, 1024, 2048, 4096, 8192, 16384];

/**
 * The disk notches, in gigabytes, with zero for "no limit".
 *
 * Disk is the one runner limit that is genuinely optional: it is advisory on
 * every backend, and what stops a host filling up is its own disk reserve
 * rather than a figure on a pool. A pool that asks for it is charged it, which
 * is why it belongs on the same panel as the other two.
 */
export const DISK_NOTCHES: readonly number[] = [
  0, 10, 20, 40, 60, 80, 120, 160, 240, 320, 480, 640,
];

/**
 * The cache notches, in gigabytes, with zero for "no limit". The limit is kept
 * by evicting whole entries between one runner and the next, so it is a
 * high-water mark on a host's disk rather than an allocation.
 */
export const CACHE_NOTCHES: readonly number[] = [
  0, 1, 2, 5, 10, 20, 30, 50, 75, 100, 150, 200, 300, 500,
];

const BYTES_PER_GB = 1024 * 1024 * 1024;

/** Bytes as the API carries a cache limit, from the gigabytes a slider moves in. */
export function cacheBytes(gb: number): number {
  return Math.round(gb * BYTES_PER_GB);
}

/**
 * The gigabytes a stored cache limit comes to. It is not snapped to a notch:
 * a limit typed as 7 GB is 7 GB, and the slider takes it in as a notch of its
 * own the way it takes any other figure set off the notches.
 */
export function cacheGb(bytes: number | undefined): number {
  if (!bytes || bytes <= 0) return 0;
  return Math.round((bytes / BYTES_PER_GB) * 100) / 100;
}

/** The notch nearest a value, so a figure typed elsewhere lands on the slider. */
export function nearest(notches: readonly number[], value: number): number {
  let best = notches[0] ?? 0;
  for (const n of notches) {
    if (Math.abs(n - value) < Math.abs(best - value)) best = n;
  }
  return best;
}

/**
 * A notch list that includes a value the notches do not have.
 *
 * A pool created through the API, or before these sliders existed, can hold
 * 3000 MB; snapping it to 3072 the moment its page opens would change a pool
 * an operator came to read. The value joins the list instead, and the slider
 * moves off it onto the notches.
 */
export function withValue(notches: readonly number[], value: number): number[] {
  if (!Number.isFinite(value) || value <= 0 || notches.includes(value)) return [...notches];
  return [...notches, value].sort((a, b) => a - b);
}

/** "2 cores", "half a core", "1 core". */
export function cpuLabel(cpus: number): string {
  if (cpus === 0.5) return 'half a core';
  if (cpus === 0.25) return 'a quarter core';
  if (cpus === 1) return '1 core';
  return `${trimNumber(cpus)} cores`;
}

/** "4 GB", "512 MB". */
export function memoryLabel(mb: number): string {
  if (mb <= 0) return 'none';
  if (mb < 1024) return `${mb} MB`;
  return `${trimNumber(mb / 1024)} GB`;
}

/** "no limit" is a real answer for disk and for the cache, and reads as one. */
export function gbLabel(gb: number): string {
  return gb <= 0 ? 'no limit' : `${trimNumber(gb)} GB`;
}

function trimNumber(value: number): string {
  return value.toLocaleString(undefined, { maximumFractionDigits: 1 });
}

/** One runner's size as a phrase: "2 cores and 4 GB". */
export function sizeLabel(cpus: number, memoryMb: number): string {
  return `${cpuLabel(cpus)} and ${memoryLabel(memoryMb)}`;
}

/**
 * What a runner of this pool is charged, which is twice the size it was given
 * where the pool gives its jobs a daemon of their own: the build runs in that
 * daemon, so it is given the same limits and the machine carries the pair.
 *
 * It is asked only about a size somebody typed. A pool sized by its host puts
 * the runner and the daemon in one slot between them, so it is charged one --
 * a slot is one runner, whatever the runner brought with it.
 *
 * It is the figure the fleet's room is counted from, and the one an operator
 * reading "2 cores" against a ten-core host cannot otherwise arrive at.
 */
export function chargedSize(
  cpus: number,
  memoryMb: number,
  dockerMode: string | undefined,
): { cpus: number; memoryMb: number; pair: boolean } {
  const pair = dockerMode === 'dind';
  const factor = pair ? 2 : 1;
  return { cpus: cpus * factor, memoryMb: memoryMb * factor, pair };
}

/**
 * How many runners of a size a machine has room for, on an empty host.
 *
 * This is the browser's copy of the controller's arithmetic, and it is used
 * for one thing: the sentence under a slider while the server's own answer is
 * still in flight. Where the two disagree the server wins, because it knows
 * what the fleet is actually charged.
 */
export function roomOn(
  machine: { cpus: number; memoryMb: number },
  charge: { cpus: number; memoryMb: number },
): number {
  let room = Infinity;
  if (machine.cpus > 0 && charge.cpus > 0)
    room = Math.min(room, Math.floor(machine.cpus / charge.cpus));
  if (machine.memoryMb > 0 && charge.memoryMb > 0)
    room = Math.min(room, Math.floor(machine.memoryMb / charge.memoryMb));
  return Number.isFinite(room) ? Math.max(0, room) : 0;
}

/**
 * What ran out on a host, as the clause that follows what the machine is.
 *
 * The distinction it carries is the one an operator acts on: a host held to
 * fewer runners than its machine could take is an operator's own ceiling and
 * is working as asked, while one whose machine runs out first is a host that
 * will refuse creates for slots every page counts as free.
 */
export function limitPhrase(limitedBy: string | undefined): string {
  switch (limitedBy) {
    case 'cpu':
      return 'its cores run out first';
    case 'memory':
      return 'its memory runs out first';
    case 'disk':
      return 'its disk runs out first';
    case 'slots':
      return 'held there by its slot count';
    case 'profile':
      return 'its runner profile keeps this pool off it';
    default:
      return '';
  }
}

/**
 * The folders a pool may keep in memory, and what they cost.
 *
 * A tmpfs is charged to the runner's own memory limit, so what a folder may
 * fill comes out of the limit the pool was sized with for the job rather than
 * being added to it. These mirror the controller's arithmetic
 * (store.TmpfsConfig) so the editor can propose the limit that leaves the job
 * the room it has now, before the server's answer lands; the server's own
 * warning, pool.tmpfs_memory_tight, is the authority and says the same number.
 */
export const MIN_TMPFS_MB = 64;
/** What a folder with no size of its own is given on a runner with room. */
export const DEFAULT_TMPFS_WORK_MB = 4096;
export const DEFAULT_TMPFS_TMP_MB = 1024;
/** The Docker-in-Docker sidecar's image store: larger, because a pull that does not fit fails the job. */
export const DEFAULT_TMPFS_DAEMON_MB = 8192;

export interface TmpfsFolder {
  enabled: boolean;
  /** The typed size in MB; undefined or zero lets the memory limit pick it. */
  sizeMb: number | undefined;
}

/**
 * The memory the enabled folders may take: what was typed, or the default for
 * one left to size itself. This is what a limit sized for the job alone should
 * be raised by.
 */
export function tmpfsReserveMb(work: TmpfsFolder, tmp: TmpfsFolder): number {
  const one = (f: TmpfsFolder, fallback: number) =>
    !f.enabled ? 0 : f.sizeMb && f.sizeMb > 0 ? f.sizeMb : fallback;
  return one(work, DEFAULT_TMPFS_WORK_MB) + one(tmp, DEFAULT_TMPFS_TMP_MB);
}

/** What the sidecar's image store may take: what was typed, or the default. */
export function daemonReserveMb(daemon: TmpfsFolder): number {
  return !daemon.enabled
    ? 0
    : daemon.sizeMb && daemon.sizeMb > 0
      ? daemon.sizeMb
      : DEFAULT_TMPFS_DAEMON_MB;
}

/**
 * The limit that leaves the job the room the current one gives it, with the
 * folders on top. Zero when there is nothing to propose: no limit to raise
 * (the host's share is sized from the machine), or nothing in memory.
 *
 * A typed limit is given to the runner and to the daemon alike, and each is
 * charged for its own folders, so the limit covers whichever needs more. Adding
 * the two would pay for room neither container uses.
 */
export function recommendedMemoryMb(
  limitMb: number,
  work: TmpfsFolder,
  tmp: TmpfsFolder,
  daemon: TmpfsFolder = { enabled: false, sizeMb: undefined },
): number {
  const reserve = Math.max(tmpfsReserveMb(work, tmp), daemonReserveMb(daemon));
  // At least twice what the folders may take, as well as the limit plus them:
  // folders are fitted into half a limit, so a proposal that left them more than
  // half would still be tight when it was taken. On a limit smaller than the
  // folders the sum alone falls short of that.
  return limitMb > 0 && reserve > 0 ? Math.max(limitMb + reserve, 2 * reserve) : 0;
}

/**
 * Whether the folders take enough of the limit to be worth a word: more than
 * half of it, the line the controller's pool.tmpfs_memory_tight draws too.
 *
 * Without it the proposal would follow the limit upward -- accept 12 GB and the
 * folders are proposed on top of that -- when what an operator needs is one
 * answer, and silence once the job has its room. Folders left to size
 * themselves are fitted into half the limit, so at the line they are not a
 * problem; past it they are either shrunk or, if typed, a job that fills them
 * is killed for want of memory.
 */
export function tmpfsIsTight(limitMb: number, reserveMb: number): boolean {
  return limitMb > 0 && reserveMb * 2 > limitMb;
}

/**
 * The ways of dividing a host-sized slot between a runner and its Docker sidecar
 * that the pool editor offers, as the sidecar's share of CPU and of memory. They
 * mirror the controller's priced presets (`room.split_plan`), which carry the same
 * ids and figures; the words are here because they are guidance, not data.
 */
export interface SplitPreset {
  id: 'even' | 'build' | 'runner';
  label: string;
  /** The sidecar's share of a slot's CPU, in percent. */
  cpu: number;
  /** The sidecar's share of a slot's memory, in percent. */
  memory: number;
  description: string;
}

export const SPLIT_PRESETS: readonly SplitPreset[] = [
  {
    id: 'even',
    label: 'Even',
    cpu: 50,
    memory: 50,
    description:
      'Half of the CPU and half of the memory to each. The right start when you do not yet know where the work is.',
  },
  {
    id: 'build',
    label: 'Image builds in the sidecar',
    cpu: 70,
    memory: 50,
    description:
      'The sidecar gets 70% of the CPU and the memory stays even. For jobs that build images or run containers: the CPU is spent in the sidecar while the runner waits.',
  },
  {
    id: 'runner',
    label: 'Work in the runner',
    cpu: 35,
    memory: 35,
    description:
      'The runner gets 65% of both. For jobs that compile, test or install in the runner with a Docker step at the end, and for an in-memory work folder, which is charged to the runner.',
  },
];

/**
 * Which preset a pair of typed shares is, or `custom`. An empty share is the even
 * split, which is how the API reads zero; anything out of step with every preset is
 * the person's own.
 */
export function splitPreset(cpu: string, memory: string): SplitPreset['id'] | 'custom' {
  const read = (s: string): number | null => {
    const t = s.trim();
    if (t === '') return 50;
    const n = Number(t);
    return Number.isInteger(n) ? n : null;
  };
  const c = read(cpu);
  const m = read(memory);
  if (c === null || m === null) return 'custom';
  return SPLIT_PRESETS.find((p) => p.cpu === c && p.memory === m)?.id ?? 'custom';
}
