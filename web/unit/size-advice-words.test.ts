import { test } from 'node:test';
import assert from 'node:assert/strict';
import { observedWords, sparseWords } from '../src/lib/jobs/size.ts';

// The figures are what a reader checks the recommendation against, so they are
// worded once, here, and the card and its tests agree on the sentence.
test('the observed figures read as the p95, the max and the sample size over the window', () => {
  assert.equal(
    observedWords(
      {
        runs: 12,
        cpu: { p50: 1, p95: 1.5, max: 2 },
        memory_mb: { p50: 5200, p95: 6000, max: 7100 },
      },
      '336h0m0s',
    ),
    'p95 5.9 GB · max 6.9 GB · 1.5 CPU at p95 · 12 runs in 14 days',
  );
});

test('a dimension no run measured is left out rather than shown as nothing', () => {
  assert.equal(
    observedWords(
      { runs: 1, cpu: { p50: 0, p95: 0, max: 0 }, memory_mb: { p50: 1000, p95: 1000, max: 1000 } },
      '24h0m0s',
    ),
    'p95 1000 MB · max 1000 MB · 1 run in 24 hours',
  );
});

test('a sparse row says how far it is from being advised on, from the payload', () => {
  assert.equal(sparseWords(2, 5), 'Not enough data yet, 2 of 5 runs.');
});
