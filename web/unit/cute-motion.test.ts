import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cuteMotion } from '../src/lib/runners/cute-motion';

test('Cute keeps its existing gait and speed tiers when Standard changes', () => {
  for (const seed of ['runner-one', 'runner-two', 'workflow-left', 'workflow-right']) {
    const maximum = cuteMotion('maximum_zoomies', seed);
    const extra = cuteMotion('zoomies', seed);
    const busy = cuteMotion('busy', seed);
    assert.ok(maximum.stride >= 0.56 && maximum.stride < 0.58);
    assert.ok(extra.stride >= 0.8 && extra.stride < 0.82);
    assert.ok(busy.stride >= 1.6 && busy.stride < 1.72);
    assert.ok(busy.walking && !extra.walking && !maximum.walking);
    assert.ok(maximum.spinning && extra.spinning && !busy.spinning);
    assert.deepEqual(cuteMotion('maximum_zoomies', seed), maximum);
  }
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
    assert.equal(cuteMotion(state, 'runner').walking, false);
    assert.equal(cuteMotion(state, 'runner').spinning, false);
  }
});
