import { test } from 'node:test';
import assert from 'node:assert/strict';
import { kennelStatus, kennelTrackingStatus } from '../src/lib/status.ts';

// A repository Kennel Club was told not to look at has no standing: those are what the
// evaluator concluded, and nothing is evaluated for it. Drawing it as one would read
// "Pending" as a thing that is about to be looked at, which is the opposite of the truth.
test('a repository nobody tracks is drawn as a decision and not as one of the four standings', () => {
  const status = kennelTrackingStatus();
  assert.equal(status.label, 'Not tracked');
  for (const state of ['pending', 'partial', 'attention', 'best_in_show'] as const) {
    const standing = kennelStatus(state);
    assert.notEqual(status.key, standing.key);
    assert.notEqual(status.label, standing.label);
  }
});

// The colour never carries a meaning alone, and a status that borrowed another's tone and
// shape would be read as that one. A decision somebody took is neutral, and slashed, as
// a source nobody granted is.
test('it is neutral and has a shape of its own among the neutral kennel statuses', () => {
  const status = kennelTrackingStatus();
  assert.equal(status.tone, 'neutral');
  assert.equal(status.shape, 'slash');
  assert.ok(status.hint && status.hint.length > 20, 'the badge explains itself');
  for (const state of ['pending', 'partial'] as const) {
    assert.notEqual(kennelStatus(state).shape, status.shape);
  }
});
