import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  countsAreAFloor,
  coverageStatesText,
  floorSentence,
  openFindingsText,
  worstCoverageState,
  worstSeverity,
} from '../src/lib/kennel/words.ts';

const none = { error: 0, warning: 0, info: 0 };

test('the worst open severity is the one a repository is drawn by', () => {
  assert.equal(worstSeverity(none), undefined);
  assert.equal(worstSeverity({ error: 0, warning: 0, info: 3 }), 'info');
  assert.equal(worstSeverity({ error: 0, warning: 1, info: 3 }), 'warning');
  assert.equal(worstSeverity({ error: 2, warning: 1, info: 3 }), 'error');
});

test('what is open is said in the words the problems drawer uses, with one a singular', () => {
  assert.equal(openFindingsText(none), 'No open findings');
  assert.equal(openFindingsText({ error: 1, warning: 0, info: 0 }), '1 error');
  assert.equal(openFindingsText({ error: 2, warning: 1, info: 0 }), '2 errors and 1 warning');
  // An info finding is a note, as it is everywhere else in the UI.
  assert.equal(
    openFindingsText({ error: 2, warning: 1, info: 4 }),
    '2 errors, 1 warning and 4 notes',
  );
});

test('a count is a floor whenever something has not been read in full', () => {
  const clear = {
    states: { pending: 0, partial: 0, attention: 3, best_in_show: 5 },
    unavailable: [],
  };
  assert.equal(countsAreAFloor(clear), false);
  assert.equal(countsAreAFloor({ ...clear, states: { ...clear.states, partial: 1 } }), true);
  assert.equal(countsAreAFloor({ ...clear, states: { ...clear.states, pending: 1 } }), true);
  // An installation whose reads are not getting through may hold repositories
  // there is no row for, so even a clean-looking total is not one.
  const note = {
    installation_id: 'ins_1',
    target: 'acme',
    state: 'error',
    reason: 'x',
    since: '2026-10-06T00:00:00Z',
  } as const;
  assert.equal(countsAreAFloor({ ...clear, unavailable: [note] }), true);
});

test('how a source was read leads with the news that is worst', () => {
  assert.equal(coverageStatesText({}), 'none read yet');
  assert.equal(coverageStatesText({ ok: 40 }), '40 read');
  assert.equal(
    coverageStatesText({ ok: 40, denied: 2, error: 1, held: 3 }),
    '1 could not be read, 3 held for a rate limit, 2 not granted, 40 read',
  );
  // A zero is not a thing to say.
  assert.equal(coverageStatesText({ ok: 3, denied: 0 }), '3 read');
});

test('the sentence over the counts says what is missing from them, or nothing when nothing is', () => {
  const states = { pending: 0, partial: 0, attention: 3, best_in_show: 5 };
  assert.equal(floorSentence({ states, unavailable: [] }), '');
  assert.equal(
    floorSentence({ states: { ...states, partial: 1 }, unavailable: [] }),
    'These counts are a minimum: 1 repository is only partly checked.',
  );
  assert.equal(
    floorSentence({ states: { ...states, partial: 2, pending: 1 }, unavailable: [] }),
    'These counts are a minimum: 2 repositories are only partly checked and 1 repository has not been looked at yet.',
  );
  const note = {
    installation_id: 'ins_1',
    target: 'acme',
    state: 'error',
    reason: 'x',
    since: 'x',
  } as const;
  assert.equal(
    floorSentence({
      states,
      unavailable: [note, { ...note, installation_id: 'ins_2', target: 'beta' }],
    }),
    'These counts are a minimum: reads from acme and beta are not getting through, so some repositories may not be listed.',
  );
});

test('a source is drawn by the worst state any repository is in', () => {
  assert.equal(worstCoverageState({}), undefined);
  assert.equal(worstCoverageState({ ok: 9 }), 'ok');
  assert.equal(worstCoverageState({ ok: 9, partial: 1 }), 'partial');
  assert.equal(worstCoverageState({ ok: 9, denied: 1, partial: 1 }), 'denied');
  assert.equal(worstCoverageState({ ok: 9, denied: 1, error: 1 }), 'error');
});
