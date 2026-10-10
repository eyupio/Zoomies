import { test } from 'node:test';
import assert from 'node:assert/strict';
import { hiddenNote } from '../src/lib/assistant/redaction.ts';

test('nothing hidden says nothing', () => {
  assert.equal(hiddenNote(0, 0), '');
});

test('what was hidden is counted in words, singular and plural', () => {
  assert.equal(hiddenNote(1, 0), '1 credential was hidden before this went to the model.');
  assert.equal(hiddenNote(0, 1), '1 email address was hidden before this went to the model.');
  assert.equal(hiddenNote(2, 0), '2 credentials were hidden before this went to the model.');
  assert.equal(hiddenNote(0, 3), '3 email addresses were hidden before this went to the model.');
  assert.equal(
    hiddenNote(1, 2),
    '1 credential and 2 email addresses were hidden before this went to the model.',
  );
  assert.equal(
    hiddenNote(1, 1),
    '1 credential and 1 email address were hidden before this went to the model.',
  );
});
