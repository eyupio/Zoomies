/**
 * Automatic pools, in the words the pages use for them.
 *
 * A pool the controller keeps is the one kind of pool an operator cannot fully
 * edit, so where a page shows or offers something on a pool it asks here first.
 * The sentences that explain a decision come from the controller; what is here
 * is the vocabulary around them, kept in one place so the Pools page, a pool's
 * own page and the hosts that count towards it never name the same thing three
 * ways.
 */
import type { AutoPoolPending, AutoPools, Pool, SizeClassLimits } from '../api/types';
import { memoryLabel } from './sizing';

export type SwitchMode = AutoPools['auto_pools'];

/** True for a pool the controller keeps. */
export function isAutomatic(pool: Pick<Pool, 'auto'> | null | undefined): boolean {
  return pool?.auto !== undefined && pool.auto !== null;
}

/**
 * An architecture as the controller's own sentences say it, which is the word
 * GitHub's runner label uses: amd64 is "x64" there, and a panel that called it
 * one thing beside a sentence that called it another would read as two kinds.
 */
export function archWords(arch: string): string {
  return arch === 'amd64' ? 'x64' : arch;
}

/** The switch's own spelling, for a badge. */
export function modeWords(mode: SwitchMode | undefined): string {
  if (mode === 'on') return 'On';
  if (mode === 'shadow') return 'Report only';
  return 'Off';
}

/** What a switch does at each setting, for the sentence beside it. */
export function modeHint(setting: 'auto_pools' | 'size_routing', mode: SwitchMode): string {
  if (setting === 'auto_pools') {
    if (mode === 'on') return 'The controller makes and keeps these pools.';
    if (mode === 'shadow') {
      return 'The controller says what it would do below and changes nothing. Hosts are still given a class.';
    }
    return 'The controller keeps no pool. Pools you made are unaffected either way.';
  }
  if (mode === 'on') return 'Each job is sent to the pool of its class.';
  if (mode === 'shadow') {
    return 'Each job is given a class and where it ran is recorded. Nothing is sent anywhere.';
  }
  return 'No job is given a class.';
}

const PENDING: Record<AutoPoolPending['kind'], string> = {
  create: 'Would make',
  resize: 'Would resize',
  enable: 'Would put back in use',
  disable: 'Would put out of use',
  reshape: 'Would bring back in line',
};

/** The verb for a change the controller has worked out and is not allowed to make yet. */
export function pendingWords(kind: AutoPoolPending['kind']): string {
  return PENDING[kind] ?? 'Would change';
}

/** "up to 4 CPUs and 16 GB", or "anything larger" for the class with no limit above it. */
export function limitWords(limits: SizeClassLimits): string {
  const cpus = limits.host_max_cpus ?? 0;
  const memory = limits.host_max_memory_mb ?? 0;
  if (cpus <= 0 && memory <= 0) return 'anything larger';
  const parts: string[] = [];
  if (cpus > 0) parts.push(`${Math.round(cpus * 10) / 10} CPUs`);
  if (memory > 0) parts.push(memoryLabel(memory));
  return `up to ${parts.join(' and ')}`;
}

/** What one runner of the class is, where a host's own profile names no size. */
export function runnerWords(limits: SizeClassLimits): string {
  return `${Math.round(limits.runner_cpus * 10) / 10} CPU${limits.runner_cpus === 1 ? '' : 's'} and ${memoryLabel(limits.runner_memory_mb)}`;
}

/**
 * A pool's own figures as a line for the cap and the warm count: zero means
 * "none" for a warm count and "no cap" for a cap, and neither reads as a number.
 */
export function warmWords(warm: number): string {
  return warm > 0 ? `${warm} kept ready` : 'none kept ready';
}

export function capWords(cap: number): string {
  return cap > 0 ? `capped at ${cap}` : 'no cap';
}
