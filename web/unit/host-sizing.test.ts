import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  hostIsInvolved,
  hostSizingWords,
  sourceWords,
  tierWords,
} from '../src/lib/pools/hostSizing.ts';

test('the fleet is two different settings, and a row says which one stands in', () => {
  assert.equal(sourceWords('global', 'standard'), "the fleet's default");
  assert.equal(sourceWords('global', 'floor'), "the fleet's minimum");
  assert.equal(sourceWords('host', 'standard'), 'the host');
  assert.equal(sourceWords('pool', 'floor'), 'the pool');
  assert.equal(sourceWords(undefined, 'standard'), '');
});

test('a tier with one owner says it once, and a tier with two says each', () => {
  assert.equal(tierWords(3, 'host', 8192, 'host', 'standard'), '3 cores and 8 GB, from the host');
  // The operator set the CPU on the host and left the memory to the fleet: the
  // sentence has to say which of the two to go and change.
  assert.equal(
    tierWords(3, 'host', 4096, 'global', 'standard'),
    "3 cores from the host and 4 GB from the fleet's default",
  );
  assert.equal(tierWords(0, undefined, 0, undefined, 'floor'), '');
  assert.equal(tierWords(2, 'global', 0, undefined, 'floor'), "2 cores, from the fleet's minimum");
});

test('a host row carries what a runner there is given, at least, and at most', () => {
  assert.deepEqual(
    hostSizingWords({
      standard: { cpus: 3, memory_mb: 8192 },
      standard_cpus_source: 'host',
      standard_memory_mb_source: 'host',
      floor: { cpus: 2, memory_mb: 4096 },
      floor_cpus_source: 'host',
      floor_memory_mb_source: 'global',
      ceiling_cpus: 4,
      ceiling_source: 'pool',
    }),
    {
      standard: '3 cores and 8 GB, from the host',
      floor: "2 cores from the host and 4 GB from the fleet's minimum",
      ceiling: '4 cores, from the pool',
    },
  );
  // A pool that lends no CPU has no ceiling to explain, and one that nobody
  // set a floor for has no floor.
  assert.deepEqual(
    hostSizingWords({
      standard: { cpus: 2, memory_mb: 4096 },
      standard_cpus_source: 'pool',
      standard_memory_mb_source: 'pool',
      floor: { cpus: 0, memory_mb: 0 },
    }),
    { standard: '2 cores and 4 GB, from the pool', floor: '', ceiling: '' },
  );
  assert.deepEqual(hostSizingWords(undefined), { standard: '', floor: '', ceiling: '' });
});

// A fleet that has never set a profile must see no more than it did before:
// the explanation is owed only where the host's own settings are doing
// something to the pool's runners.
test('a row explains itself only where the host is involved', () => {
  assert.equal(hostIsInvolved(undefined, true), false);
  assert.equal(hostIsInvolved({ standard: { cpus: 2, memory_mb: 4096 } }, false), false);
  assert.equal(
    hostIsInvolved({ standard: { cpus: 2, memory_mb: 4096 }, floor_cpus_source: 'global' }, false),
    false,
  );
  assert.equal(hostIsInvolved({ floor_cpus_source: 'host' }, false), true);
  assert.equal(hostIsInvolved({ floor_memory_mb_source: 'host' }, false), true);
  assert.equal(hostIsInvolved({ ceiling_source: 'host' }, false), true);
  // A pool that takes its size from the host is always about the host.
  assert.equal(hostIsInvolved({ standard: { cpus: 2, memory_mb: 4096 } }, true), true);
});
