import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DEFAULT_STATUS_STYLE, resolveStatusStyle } from '../src/lib/state/status-style';
import { dogPhase } from '../src/lib/mascot/dog-motion';
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

test('more CPU means faster motion for every runner seed', () => {
  for (const seed of ['runner-one', 'runner-two', 'workflow-left', 'workflow-right']) {
    const maximum = standardMotion('maximum_zoomies', seed);
    const extra = standardMotion('zoomies', seed);
    const busy = standardMotion('busy', seed);
    assert.ok(maximum.duration < extra.duration && extra.duration < busy.duration);
    assert.deepEqual(standardMotion('maximum_zoomies', seed), maximum);
  }
});

test('every state has a distinct persistent cue even when motion is disabled', () => {
  const states = [
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
  ];
  const cues = states.map((state) => standardMotion(state, 'runner').cue);
  assert.equal(new Set(cues).size, states.length);
  assert.ok(!cues.includes('none'));
  assert.equal(standardMotion('throttled', 'runner').cue, 'pause');
  assert.equal(standardMotion('failed', 'runner').cue, 'error');
});

test('terminal and unrecognised states never play a happy animation', () => {
  for (const state of ['failed', 'removed', 'unknown', 'future-state'])
    assert.equal(standardMotion(state, 'runner').still, true);
  assert.deepEqual(standardMotion('future-state', 'runner'), standardMotion('unknown', 'runner'));
  assert.equal(standardMotion('failed', 'runner').motion, 'sad');
  assert.equal(standardMotion('removed', 'runner').motion, 'sleep');
});

test('runner and workflow seeds spread animation timing across the cycle', () => {
  const phases = Array.from({ length: 400 }, (_, i) => dogPhase('run_' + i));
  assert.ok(phases.every((phase) => phase >= 0 && phase < 1));
  assert.ok(Math.min(...phases) < 0.1);
  assert.ok(Math.max(...phases) > 0.9);
  assert.equal(dogPhase('runner'), dogPhase('runner'));
});
