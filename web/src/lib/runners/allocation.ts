/**
 * What a runner was given, and where that figure came from, in the words a page
 * says it in.
 *
 * Pure, and imported by a unit test. The runner's page and the job's drawer say
 * it about the same figure -- a job records the size of the runner that took it
 * -- so they share one sentence rather than each keeping a copy that drifts.
 */
import { formatMegabytes, formatNumber } from '$lib/format';

/** Why a runner is the size it is. Empty for a source this client does not know. */
export function allocationSourceWords(source: string | undefined): string {
  switch (source) {
    case 'host':
      return "the host's default share";
    case 'pool':
      return 'from the pool';
    case 'profile':
      return 'the standard size set for its host';
    case 'reduced':
      return 'reduced: no host had room for the pool’s standard size';
    case 'history':
      return 'sized for what the jobs waiting are known to need';
    default:
      return '';
  }
}

/** "1.87 CPU · 3.9 GB, the host's default share": what it got, and why that figure. Empty where nothing was recorded. */
export function allocationWords(
  cpus: number | undefined,
  memoryMb: number | undefined,
  source: string | undefined,
): string {
  const parts: string[] = [];
  if ((cpus ?? 0) > 0) parts.push(`${formatNumber(cpus ?? 0)} CPU`);
  if ((memoryMb ?? 0) > 0) parts.push(formatMegabytes(memoryMb ?? 0));
  if (parts.length === 0) return '';
  const why = allocationSourceWords(source);
  return why ? `${parts.join(' · ')}, ${why}` : parts.join(' · ');
}
