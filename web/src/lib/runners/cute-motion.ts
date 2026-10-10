import { dogPhase } from '../mascot/dog-motion';

/** Preserve the Cute avatar's existing cadence when Standard changes style. */
export function cuteMotion(state: string, seed: string) {
  const fraction = dogPhase(seed);
  const walking = state === 'busy';
  const stride =
    (state === 'maximum_zoomies' ? 0.56 : state === 'zoomies' ? 0.8 : walking ? 1.6 : 0.64) +
    fraction * (walking ? 0.12 : 0.02);
  return {
    walking,
    spinning: state === 'maximum_zoomies' || state === 'zoomies',
    stride,
    phase: -fraction * stride,
  };
}
