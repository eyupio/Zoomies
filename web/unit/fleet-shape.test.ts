import { test } from 'node:test';
import assert from 'node:assert/strict';
import { shapeDiffers } from '../src/lib/state/fleet-shape.ts';

const host = {
  id: 'hst_1',
  name: 'builder-1',
  healthy: true,
  capacity: 4,
  cpu_percent: 4,
  last_heartbeat: '2026-01-01T00:00:00Z',
  memory_pool: { total_mb: 32768, lent_mb: 0, free_mb: 20000 },
};

test('a host whose memory pool moved is still the same fleet', () => {
  // The case this list exists for. A host's pool carries the free memory its
  // agent measured, so it differs on every heartbeat; if that counted as a
  // change to the fleet, every Runners and Pools grid would fetch its page
  // again once per host per heartbeat to learn nothing.
  const next = { ...host, memory_pool: { total_mb: 32768, lent_mb: 1536, free_mb: 17000 } };
  assert.equal(shapeDiffers(host, next), false);
});

test('a host that only has a pool now, or has lost it, is still the same fleet', () => {
  // The first heartbeat after a pool turns the valve on adds the figure, and the
  // last before it is switched off takes it away; neither is a host appearing,
  // changing health or moving pool.
  const { memory_pool: _gone, ...bare } = host;
  assert.equal(shapeDiffers(bare, host), false);
  assert.equal(shapeDiffers(host, bare), false);
});

test('a runner whose valve view moved is still the same fleet, and one that changed state is not', () => {
  // The Runners grid draws its pills from the cache row, so a loan made, or a
  // would-be loan that grew, repaints it without a round trip; a runner that
  // moved from idle to busy is a change to what the grid lists.
  const runner = {
    id: 'run_1',
    name: 'zoomies-1',
    state: 'busy',
    memory_resource: { state: 'observing', mode: 'observe', would_lend_mb: 128 },
  };
  const grown = { ...runner, memory_resource: { ...runner.memory_resource, would_lend_mb: 512 } };
  assert.equal(shapeDiffers(runner, grown), false);
  const { memory_resource: _gone, ...bare } = runner;
  assert.equal(shapeDiffers(bare, runner), false);
  assert.equal(shapeDiffers(runner, { ...runner, state: 'idle' }), true);
});

test('the figures every heartbeat reports do not move the fleet either', () => {
  for (const [key, value] of [
    ['cpu_percent', 91],
    ['memory_bytes', 123456],
    ['last_heartbeat', '2026-01-01T00:00:30Z'],
    ['usage', { cpu_percent: 80 }],
    ['resource_sample', { cpu_percent: 80 }],
  ] as const) {
    assert.equal(shapeDiffers(host, { ...host, [key]: value }), false, key);
  }
});

test('a host that changed in anything else is a change worth fetching again', () => {
  // The counterpart: the rule is a list of what may move, not a way to hide a
  // host from the grids. Health, capacity and name are what a row is.
  assert.equal(shapeDiffers(host, { ...host, healthy: false }), true);
  assert.equal(shapeDiffers(host, { ...host, capacity: 8 }), true);
  assert.equal(shapeDiffers(host, { ...host, name: 'builder-2' }), true);
});

test('a row the cache has not seen is a change', () => {
  assert.equal(shapeDiffers(undefined, host), true);
});

test('a report that only moved its own time does not move the fleet', () => {
  // The controller sends no frame for such a report, but a frame can still carry
  // a newer checked_at beside a heartbeat. Refetching the Runners and Pools grids
  // for it is a round trip that has nothing to show.
  const summary = { counted: 3, warnings: 1, errors: 0, skipped: 0, suggestions: 0, accepted: 0 };
  const before = { ...host, doctor: { checked_at: '2026-01-01T00:00:00Z', summary, results: [] } };
  const later = { ...before, doctor: { ...before.doctor, checked_at: '2026-01-01T00:01:30Z' } };
  assert.equal(shapeDiffers(before, later), false);
  const worse = {
    ...later,
    doctor: { ...later.doctor, summary: { ...summary, warnings: 2 } },
  };
  assert.equal(shapeDiffers(before, worse), true);
  assert.equal(shapeDiffers(host, before), true, 'a first report is news');
});
