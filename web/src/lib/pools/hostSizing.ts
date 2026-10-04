/**
 * How one pool is sized on one host, in the words a row says it in.
 *
 * Pure, and imported by a unit test. A runner's size is decided from three
 * places -- the pool, the host's runner profile and the fleet's runners.*
 * settings -- and an operator reading a number needs to know which of them it
 * came from before they know where to change it. The controller says which, in
 * `PoolHostSizing`; this only puts it into a sentence, so that every page that
 * shows a size says the same thing about it.
 */
import type { PoolHostSizing } from '../api/types';
import { cpuLabel, memoryLabel } from './sizing';

/** The tiers a figure can belong to, which decides what "the fleet's" means for it. */
export type Tier = 'standard' | 'floor' | 'ceiling';

/**
 * Whose a figure is. The fleet's setting is a different one for each tier --
 * `runners.default_*` stands in for a standard and `runners.minimum_*` for a
 * floor -- and an operator told "from the fleet" has to be told which.
 */
export function sourceWords(source: string | undefined, tier: Tier): string {
  switch (source) {
    case 'pool':
      return 'the pool';
    case 'host':
      return 'the host';
    case 'global':
      return tier === 'floor' ? "the fleet's minimum" : "the fleet's default";
    default:
      return '';
  }
}

/** One tier as "3 cores and 8 GB, from the host", or with a clause each where the two have different owners. */
export function tierWords(
  cpus: number | undefined,
  cpusSource: string | undefined,
  memoryMb: number | undefined,
  memorySource: string | undefined,
  tier: Tier,
): string {
  const hasCpus = (cpus ?? 0) > 0;
  const hasMemory = (memoryMb ?? 0) > 0;
  if (!hasCpus && !hasMemory) return '';
  if (hasCpus && hasMemory && cpusSource !== memorySource) {
    return `${cpuLabel(cpus ?? 0)} from ${sourceWords(cpusSource, tier)} and ${memoryLabel(memoryMb ?? 0)} from ${sourceWords(memorySource, tier)}`;
  }
  const figures = [hasCpus ? cpuLabel(cpus ?? 0) : '', hasMemory ? memoryLabel(memoryMb ?? 0) : '']
    .filter(Boolean)
    .join(' and ');
  const owner = sourceWords(hasCpus ? cpusSource : memorySource, tier);
  return owner === '' ? figures : `${figures}, from ${owner}`;
}

export interface HostSizingWords {
  /** What one runner is given on the host. */
  standard: string;
  /** The least it is given, where anybody has set one. */
  floor: string;
  /** The most CPU it may use, its own share and any lent to it together, where the pool lends CPU. */
  ceiling: string;
}

export function hostSizingWords(sizing: PoolHostSizing | undefined): HostSizingWords {
  return {
    standard: tierWords(
      sizing?.standard?.cpus,
      sizing?.standard_cpus_source,
      sizing?.standard?.memory_mb,
      sizing?.standard_memory_mb_source,
      'standard',
    ),
    floor: tierWords(
      sizing?.floor?.cpus,
      sizing?.floor_cpus_source,
      sizing?.floor?.memory_mb,
      sizing?.floor_memory_mb_source,
      'floor',
    ),
    ceiling:
      (sizing?.ceiling_cpus ?? 0) > 0
        ? tierWords(sizing?.ceiling_cpus, sizing?.ceiling_source, undefined, undefined, 'ceiling')
        : '',
  };
}

/**
 * Whether the host's own settings are doing anything to this pool's runners
 * there, which is the only time a row owes an operator the explanation. A host
 * that gives one slot's share and holds no floor of its own is the case every
 * fleet was before profiles, and says nothing more than it ever did.
 */
export function hostIsInvolved(sizing: PoolHostSizing | undefined, fromProfile: boolean): boolean {
  if (!sizing) return false;
  if (fromProfile) return true;
  return (
    sizing.floor_cpus_source === 'host' ||
    sizing.floor_memory_mb_source === 'host' ||
    sizing.ceiling_source === 'host'
  );
}
