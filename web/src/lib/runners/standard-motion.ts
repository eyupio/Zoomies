import { dogPhase, type DogMotion, type DogCue } from '../mascot/dog-motion';

const STATES = [
  'busy',
  'zoomies',
  'maximum_zoomies',
  'idle',
  'provisioning',
  'registering',
  'throttled',
  'draining',
  'failed',
  'removed',
  'unknown',
] as const;
export type StandardMotionState = (typeof STATES)[number];

const MOTIONS: Record<StandardMotionState, { motion: DogMotion; cue: DogCue; duration: number }> = {
  busy: { motion: 'trot', cue: 'work', duration: 2.8 },
  zoomies: { motion: 'zoomies', cue: 'boost', duration: 2.2 },
  maximum_zoomies: { motion: 'orbit', cue: 'maximum', duration: 1.6 },
  idle: { motion: 'wait', cue: 'ready', duration: 6 },
  provisioning: { motion: 'sniff', cue: 'search', duration: 3.6 },
  registering: { motion: 'wave', cue: 'hello', duration: 3.2 },
  throttled: { motion: 'wait', cue: 'pause', duration: 7 },
  draining: { motion: 'settle', cue: 'rest', duration: 5 },
  failed: { motion: 'sad', cue: 'error', duration: 1 },
  removed: { motion: 'sleep', cue: 'gone', duration: 1 },
  unknown: { motion: 'puzzled', cue: 'question', duration: 1 },
};

/** Status cues survive reduced motion; failures never bounce like happy dogs. */
export function standardMotion(state: string, seed: string) {
  const known = STATES.includes(state as StandardMotionState)
    ? (state as StandardMotionState)
    : 'unknown';
  const animation = MOTIONS[known];
  return {
    state: known,
    ...animation,
    duration: animation.duration + dogPhase(seed) * 0.15,
    still: ['failed', 'removed', 'unknown'].includes(known),
  };
}
