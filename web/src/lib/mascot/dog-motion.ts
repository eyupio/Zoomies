/** A stable offset keeps rows and workflow packs from moving in lockstep. */
export function dogPhase(seed: string): number {
  let hash = 2166136261;
  for (const char of seed) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619) >>> 0;
  return (hash % 1000) / 1000;
}
