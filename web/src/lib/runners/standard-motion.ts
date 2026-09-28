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
/**
 * Four silhouettes, so a glance at a 32px pack tells working from about to
 * work, waiting and winding down. A Record rather than a list, so a new state
 * cannot ship without someone choosing its pose.
 */
export type StandardPose = 'run' | 'stand' | 'sit' | 'lie';
const POSES: Record<StandardMotionState, StandardPose> = {
  busy: 'run',
  zoomies: 'run',
  maximum_zoomies: 'run',
  provisioning: 'stand',
  throttled: 'stand',
  idle: 'sit',
  registering: 'sit',
  failed: 'sit',
  unknown: 'sit',
  draining: 'lie',
  removed: 'lie',
};

/** Stable phases keep a fleet (and each workflow pack) from moving in lockstep. */
export function standardMotion(state: string, seed: string) {
  const motion = STATES.includes(state as StandardMotionState)
    ? (state as StandardMotionState)
    : 'unknown';
  let hash = 2166136261;
  for (const char of seed) hash = Math.imul(hash ^ char.charCodeAt(0), 16777619) >>> 0;
  const fraction = (hash % 1000) / 1000;
  // A second and third slice of the same hash for the slow gestures and the
  // blink. The gait's phase is under a second, so reusing it would start every
  // idle dog on a page -- and all three in a pack -- glancing up and blinking
  // within the same moment; these spread across the whole period instead.
  const gestureFraction = ((hash >>> 10) % 1000) / 1000;
  const blinkFraction = ((hash >>> 20) % 1000) / 1000;
  const running = ['busy', 'zoomies', 'maximum_zoomies'].includes(motion);
  const stride =
    (motion === 'maximum_zoomies' ? 0.32 : motion === 'zoomies' ? 0.48 : 0.64) + fraction * 0.02;
  const spin = (motion === 'maximum_zoomies' ? 10 : 16) + fraction * 3;
  const gesture = 6 + fraction * 3;
  const blink = 5.9 + fraction * 2;
  return {
    state: motion,
    pose: POSES[motion],
    running,
    spinning: motion === 'maximum_zoomies' || motion === 'zoomies',
    still: ['failed', 'removed', 'unknown'].includes(motion),
    stride,
    phase: -fraction * stride,
    spin,
    spinPhase: -fraction * spin,
    gesture,
    gesturePhase: -gestureFraction * gesture,
    blink,
    blinkPhase: -blinkFraction * blink,
  };
}
