import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  archWords,
  capWords,
  isAutomatic,
  limitWords,
  modeHint,
  modeWords,
  pendingWords,
  runnerWords,
  warmWords,
} from '../src/lib/pools/auto.ts';
import type { PoolAuto, SizeClassLimits } from '../src/lib/api/types.ts';

const auto: PoolAuto = {
  key: 'amd64/medium',
  arch: 'amd64',
  class: 'medium',
  warm: 0,
  cap: 0,
  paused: false,
  hosts: [],
  slots: 0,
  summary: 'x',
};

test('a pool is automatic only when the controller says it keeps it', () => {
  assert.equal(isAutomatic({ auto }), true);
  assert.equal(isAutomatic({}), false);
  assert.equal(isAutomatic(null), false);
  assert.equal(isAutomatic(undefined), false);
});

test('the middle setting is called what it does, not what it is spelled', () => {
  assert.equal(modeWords('off'), 'Off');
  assert.equal(modeWords('shadow'), 'Report only');
  assert.equal(modeWords('on'), 'On');
  assert.equal(modeWords(undefined), 'Off');
});

test('each switch says what it does at each setting, and never promises what it cannot', () => {
  for (const setting of ['auto_pools', 'size_routing'] as const) {
    for (const mode of ['off', 'shadow', 'on'] as const) {
      assert.ok(modeHint(setting, mode).endsWith('.'), `${setting} ${mode} is a sentence`);
    }
  }
  // Report-only changes nothing, and the sentence has to say so, because it is
  // the one setting that reads as though it might.
  assert.match(modeHint('auto_pools', 'shadow'), /changes nothing/);
  assert.match(modeHint('size_routing', 'shadow'), /Nothing is sent anywhere/);
  // Pools an operator made are untouched whatever the switch says.
  assert.match(modeHint('auto_pools', 'off'), /Pools you made are unaffected/);
});

test('a change the controller has not made is worded as one it would make', () => {
  assert.equal(pendingWords('create'), 'Would make');
  assert.equal(pendingWords('resize'), 'Would resize');
  assert.equal(pendingWords('disable'), 'Would put out of use');
  assert.equal(pendingWords('enable'), 'Would put back in use');
  assert.equal(pendingWords('reshape'), 'Would bring back in line');
});

test('a class says where it ends, and the largest says it has no end', () => {
  const small: SizeClassLimits = {
    class: 'small',
    label: 'zoomies-small',
    host_max_cpus: 4,
    host_max_memory_mb: 16384,
    runner_cpus: 1,
    runner_memory_mb: 2048,
  };
  assert.equal(limitWords(small), 'up to 4 CPUs and 16 GB');
  assert.equal(runnerWords(small), '1 CPU and 2 GB');

  const large: SizeClassLimits = {
    class: 'large',
    label: 'zoomies-large',
    runner_cpus: 4,
    runner_memory_mb: 8192,
  };
  assert.equal(limitWords(large), 'anything larger');
  assert.equal(runnerWords(large), '4 CPUs and 8 GB');
});

test('zero is not a number for a warm count or a cap', () => {
  assert.equal(warmWords(0), 'none kept ready');
  assert.equal(warmWords(3), '3 kept ready');
  assert.equal(capWords(0), 'no cap');
  assert.equal(capWords(12), 'capped at 12');
});

test('an architecture is said the way the controller says it', () => {
  assert.equal(archWords('amd64'), 'x64');
  assert.equal(archWords('arm64'), 'arm64');
});
