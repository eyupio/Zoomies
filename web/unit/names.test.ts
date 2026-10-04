import { test } from 'node:test';
import assert from 'node:assert/strict';
import { poolName } from '../src/lib/pools/names.ts';
import type { NameShape } from '../src/lib/pools/names.ts';

const shape = (over: Partial<NameShape> = {}): NameShape => ({
  backend: 'docker',
  sizing: 'automatic',
  cpus: '',
  memory_mb: '',
  platform_os: 'ubuntu',
  platform_os_version: '24.04',
  platform_arch: 'amd64',
  ...over,
});

// A pool that leaves its runners' size to the host has no size to be named
// for: its runners are 3.8 cores on one machine and 7.6 on the next, so a name
// carrying `4vcpu` would advertise a promise it stopped keeping the day the
// fleet acquired a second, different machine.
//
// The figure the sliders happen to be holding is deliberately not consulted.
// The wizard keeps it so that switching back to a fixed size does not lose
// what was chosen, and that convenience must not leak into the pool's name.
test('a pool sized by its host is named for the machine, not for a share of one', () => {
  const automatic = poolName('spaniel', shape({ cpus: '4', memory_mb: '8192' }), [], []);
  assert.equal(automatic, 'zoomies-ubuntu-2404');
});

// The same holds when the answer is the standard size each host names for
// itself: it is different on every machine, and the sliders' position is just
// as little the pool's. Naming it for the sliders would be the mistake the
// automatic pool is protected from, made by a third answer.
test('a pool that takes its size from each host is named for the machine too', () => {
  const profile = poolName(
    'spaniel',
    shape({ sizing: 'profile', cpus: '4', memory_mb: '8192' }),
    [],
    [],
  );
  assert.equal(profile, 'zoomies-ubuntu-2404');
});

// A size somebody chose is part of what a workflow author is choosing between,
// so a fixed pool says it.
test('a pool with a size of its own says so', () => {
  const fixed = poolName(
    'spaniel',
    shape({ sizing: 'fixed', cpus: '4', memory_mb: '8192' }),
    [],
    [],
  );
  assert.equal(fixed, 'zoomies-4vcpu-8gb-ubuntu-2404');
});

// The process backend's marker is about what a job gets rather than how much
// of it, so it survives either sizing.
test('the process marker is kept whichever way the pool is sized', () => {
  const name = poolName('spaniel', shape({ backend: 'process' }), [], []);
  assert.match(name, /-host$/);
});
