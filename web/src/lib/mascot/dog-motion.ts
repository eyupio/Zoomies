export const THINKING_MOTIONS = ['zoomies', 'fetch', 'sniff', 'pounce', 'wiggle', 'orbit'] as const;
export type DogMotion =
  | (typeof THINKING_MOTIONS)[number]
  | 'trot'
  | 'wave'
  | 'settle'
  | 'wait'
  | 'sad'
  | 'sleep'
  | 'puzzled';
export type DogCue =
  | 'none'
  | 'work'
  | 'boost'
  | 'maximum'
  | 'ready'
  | 'search'
  | 'hello'
  | 'pause'
  | 'rest'
  | 'error'
  | 'gone'
  | 'question';

/** A stable offset keeps rows and workflow packs from moving in lockstep. */
export function dogPhase(seed: string): number {
  let hash = 2166136261;
  for (const char of seed) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619) >>> 0;
  return (hash % 1000) / 1000;
}
