import assert from 'node:assert/strict';
import test from 'node:test';
import { withSuggestedShare } from '../src/lib/problems/daemonShare.ts';

// An update replaces a pool's resources whole, so the button that moves one
// percentage must hand the rest back or it quietly clears the pool's minimum.
test('applying a suggested share keeps everything else the pool says about its size', () => {
  const next = withSuggestedShare(
    {
      min_cpus: 1,
      min_memory_mb: 2048,
      daemon_cpu_share_percent: 35,
      daemon_memory_share_percent: 35,
    },
    { cpu_percent: 20 },
  );
  assert.deepEqual(next, {
    min_cpus: 1,
    min_memory_mb: 2048,
    daemon_cpu_share_percent: 20,
    daemon_memory_share_percent: 35,
  });
});

test('a resource the notice has no advice for keeps the share it has', () => {
  assert.deepEqual(withSuggestedShare({ daemon_cpu_share_percent: 70 }, { memory_percent: 30 }), {
    daemon_cpu_share_percent: 70,
    daemon_memory_share_percent: 30,
  });
});

test('a pool with no resources set is given only the shares', () => {
  assert.deepEqual(withSuggestedShare(undefined, { cpu_percent: 20, memory_percent: 25 }), {
    daemon_cpu_share_percent: 20,
    daemon_memory_share_percent: 25,
  });
});
