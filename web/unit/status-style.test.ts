import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DEFAULT_STATUS_STYLE, resolveStatusStyle } from '../src/lib/state/status-style';
import { standardMotion } from '../src/lib/runners/standard-motion';

test('a browser that has never chosen starts on Off, new or reset', () => {
  // Nothing stored is a new install and a browser whose preferences were
  // cleared alike; a value this build does not recognise is the same thing.
  assert.equal(DEFAULT_STATUS_STYLE, 'off');
  assert.equal(resolveStatusStyle({}), 'off');
  assert.equal(resolveStatusStyle({ statusStyle: null }), 'off');
  assert.equal(resolveStatusStyle({ statusStyle: 'unexpected' }), 'off');
});

test('upgrading keeps what the old vocabulary switch was showing', () => {
  // Changing the default is about browsers that have not chosen; an operator
  // who was looking at the kennel words yesterday still is today.
  assert.equal(resolveStatusStyle({ quirkyStatus: true }), 'cute');
  assert.equal(resolveStatusStyle({ quirkyStatus: false }), 'off');
  assert.equal(resolveStatusStyle({ statusStyle: 'unexpected', quirkyStatus: true }), 'cute');
  assert.equal(resolveStatusStyle({ statusStyle: 'unexpected', quirkyStatus: false }), 'off');
});

test('an explicit style wins over the legacy boolean on reload', () => {
  assert.equal(resolveStatusStyle({ statusStyle: 'standard', quirkyStatus: false }), 'standard');
  assert.equal(resolveStatusStyle({ statusStyle: 'off', quirkyStatus: true }), 'off');
  assert.equal(resolveStatusStyle({ statusStyle: 'cute', quirkyStatus: false }), 'cute');
});

test('maximum zoomies always runs faster than extra zoomies and normal work', () => {
  const seeds = ['runner-one', 'runner-two', 'workflow-left', 'workflow-right'];
  for (const seed of seeds) {
    const maximum = standardMotion('maximum_zoomies', seed);
    const extra = standardMotion('zoomies', seed);
    const busy = standardMotion('busy', seed);
    assert.ok(maximum.stride < extra.stride && extra.stride < busy.stride);
    assert.ok(maximum.stride >= 0.56 && maximum.stride < 0.58);
    assert.ok(extra.stride >= 0.8 && extra.stride < 0.82);
    assert.ok(busy.stride >= 1.6 && busy.stride < 1.72);
    assert.ok(busy.walking && !extra.walking && !maximum.walking);
    assert.ok(maximum.spin >= 10);
    assert.ok(maximum.spinning && extra.spinning && !busy.spinning);
    assert.deepEqual(standardMotion('maximum_zoomies', seed), maximum);
  }
  assert.notEqual(standardMotion('busy', seeds[0]).phase, standardMotion('busy', seeds[1]).phase);
});

test('waiting and lifecycle states cannot accidentally run or spin', () => {
  for (const state of [
    'idle',
    'provisioning',
    'registering',
    'throttled',
    'draining',
    'failed',
    'removed',
    'unknown',
  ]) {
    const motion = standardMotion(state, 'runner');
    assert.equal(motion.running, false, state);
    assert.equal(motion.walking, false, state);
    assert.equal(motion.spinning, false, state);
    assert.equal(motion.still, ['failed', 'removed', 'unknown'].includes(state), state);
  }
  assert.equal(standardMotion('future-state', 'runner').state, 'unknown');
  assert.equal(standardMotion('future-state', 'runner').still, true);
});

test('working, about-to-work, waiting and winding-down states keep separate silhouettes', () => {
  // At 32px in a workflow pack the pose is most of what survives, so a state
  // must never borrow another group's silhouette. Throttled stands rather than
  // lies: it is paused and will carry on, not being retired.
  const groups = {
    run: ['busy', 'zoomies', 'maximum_zoomies'],
    stand: ['provisioning', 'throttled'],
    sit: ['idle', 'registering', 'failed', 'unknown', 'future-state'],
    lie: ['draining', 'removed'],
  };
  for (const [pose, states] of Object.entries(groups))
    for (const state of states) assert.equal(standardMotion(state, 'runner').pose, pose, state);
});

test('idle gestures and blinks spread across their whole period, not with the gait', () => {
  // The gait's phase is under a second; if the gestures shared it, every idle
  // dog on a page would glance up and blink within the same moment. Phases are
  // spread at random per seed, not placed, so what holds is that across many
  // runners they cover the whole cycle and do not follow the gait's phase --
  // not that any two dogs in a pack are a set distance apart.
  const dogs = Array.from({ length: 400 }, (_, i) => standardMotion('idle', `run_${i}`));
  const start = (phase: number, period: number) => -phase / period;
  for (const [phase, period] of [
    ['gesturePhase', 'gesture'],
    ['blinkPhase', 'blink'],
  ] as const) {
    const starts = dogs.map((dog) => start(dog[phase], dog[period]));
    for (const s of starts) assert.ok(s >= 0 && s < 1, `${phase} ${s}`);
    assert.ok(Math.min(...starts) < 0.1, `${phase} never starts early in its cycle`);
    assert.ok(Math.max(...starts) > 0.9, `${phase} never starts late in its cycle`);
    // Measured round the cycle: 0.95 and 0.05 are a tenth apart, not nine.
    const gaits = dogs.map((dog) => start(dog.phase, dog.stride));
    let near = 0;
    let pairs = 0;
    for (let i = 0; i < dogs.length; i++)
      for (let j = i + 1; j < dogs.length; j++) {
        const gait = Math.abs(gaits[i] - gaits[j]);
        if (Math.min(gait, 1 - gait) > 0.02) continue;
        pairs++;
        const d = Math.abs(starts[i] - starts[j]);
        if (Math.min(d, 1 - d) < 0.12) near++;
      }
    // Dogs whose gaits start together should mostly not gesture together;
    // were the two phases one, every such pair would.
    assert.ok(pairs > 50 && near / pairs < 0.4, `${phase}: ${near} of ${pairs} in step`);
  }
});
