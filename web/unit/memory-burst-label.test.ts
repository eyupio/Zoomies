import { test } from 'node:test';
import assert from 'node:assert/strict';
import { memoryBurstLabel } from '../src/lib/pools/vocabulary.ts';

// The pool page and the command line say the same thing about the same policy,
// so an operator who reads one finds the other.
test('a pool says what its memory valve is set to, in one line', () => {
  assert.equal(memoryBurstLabel(undefined), 'Off', 'a pool made before the valve');
  assert.equal(memoryBurstLabel({ mode: 'off' }), 'Off');
  assert.equal(memoryBurstLabel({ mode: 'observe' }), 'Observe only');
  assert.equal(
    memoryBurstLabel({ mode: 'automatic' }),
    'Automatic, up to half as much again as a runner starts with',
  );
  assert.equal(
    memoryBurstLabel({ mode: 'automatic', max_memory_mb: 12288, spill_mb: 2048 }),
    'Automatic, up to 12 GB per runner, with up to 2 GB of swap as the last resort',
  );
  assert.equal(
    memoryBurstLabel({ mode: 'observe', max_memory_mb: 12288 }),
    'Observe only',
    'observing does not claim a ceiling it does not use',
  );
});
