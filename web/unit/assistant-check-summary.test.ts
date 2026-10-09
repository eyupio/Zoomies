import { test } from 'node:test';
import assert from 'node:assert/strict';
import { checkSummary } from '../src/lib/settings/assistant.ts';

// The card has one line for what the last Test learned, and an administrator
// reads it to decide whether the model is usable: which model answered, how
// long it took, and whether usage is counted, because the limits in a later
// slice rest on that last fact.
test('a passed check names the model, the latency and whether usage came back', () => {
  assert.equal(
    checkSummary({
      ok: true,
      model: 'gpt-4o',
      latency_ms: 312,
      usage_reported: true,
      checked_at: 'x',
    }),
    'Answered as gpt-4o in 312 ms; usage reported',
  );
  assert.equal(
    checkSummary({
      ok: true,
      model: 'llama3',
      latency_ms: 40,
      usage_reported: false,
      checked_at: 'x',
    }),
    'Answered as llama3 in 40 ms; usage not reported',
  );
});

test('a failed check reads its error, and no check reads as never tested', () => {
  assert.equal(
    checkSummary({
      ok: false,
      latency_ms: 0,
      usage_reported: false,
      error: 'the provider refused the key (HTTP 401)',
      checked_at: 'x',
    }),
    'the provider refused the key (HTTP 401)',
  );
  assert.equal(checkSummary(undefined), 'Never tested');
  assert.equal(checkSummary(null), 'Never tested');
});
