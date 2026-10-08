import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  countsAreAFloor,
  coverageStatesText,
  floorSentence,
  openFindingsText,
  reasonHint,
  reasonLength,
  trackingReasonHint,
  trackSentence,
  TRACKING_STOP,
  waiverEndsAt,
  waiverRole,
  WAIVER_EXPIRY_CHOICES,
  WAIVER_REASON_MAX,
  WAIVER_REASON_MIN,
  worstCoverageState,
  worstSeverity,
  KENNEL_TURN_OFF,
  kennelSwitchOutcome,
  kennelSwitchSentence,
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

test('a reason is counted in characters, after its ends are trimmed, as the controller counts it', () => {
  assert.equal(reasonLength(''), 0);
  assert.equal(reasonLength('   padded   '), 6);
  // An emoji is one character to the controller and two UTF-16 units to
  // `.length`: a form that disagreed would say "9 of 10" at the tenth.
  assert.equal(reasonLength('🐕🐕🐕🐕🐕🐕🐕🐕🐕🐕'), 10);
  assert.equal('🐕🐕🐕🐕🐕🐕🐕🐕🐕🐕'.length, 20);
});

test('the reason field says the rule while it is unmet and the count once it is met', () => {
  assert.match(reasonHint('too short'), new RegExp(`At least ${WAIVER_REASON_MIN} characters`));
  // Ten spaces are not ten characters of reason.
  assert.match(reasonHint(' '.repeat(12)), /At least 10 characters/);
  assert.equal(reasonHint('x'.repeat(10)), `10 of ${WAIVER_REASON_MAX} characters.`);
});

// Stopping tracking is held to a waiver's length, but it is not asking why a finding
// is acceptable, and a form that told a person to say so would be asking the wrong thing.
test('the reason for stopping tracking is asked for in its own words, to the same length', () => {
  assert.match(
    trackingReasonHint('too short'),
    /At least 10 characters: say why Kennel Club should not look at this repository/,
  );
  assert.doesNotMatch(trackingReasonHint('too short'), /acceptable/);
  // The count after the rule is met is the same sentence for both.
  assert.equal(trackingReasonHint('x'.repeat(10)), `10 of ${WAIVER_REASON_MAX} characters.`);
  // And a waiver's is still a waiver's.
  assert.match(reasonHint('too short'), /say why this is acceptable here/);
});

test('the switch on a repository says its state, and to somebody who cannot change it, who can', () => {
  const admin = { admin: true, operator: true };
  const operator = { admin: false, operator: true };
  const viewer = { admin: false, operator: false };

  // Stopping is an administrator's decision.
  assert.equal(trackSentence(true, admin), 'Kennel Club reads this repository from GitHub.');
  assert.equal(
    trackSentence(true, operator),
    'Kennel Club reads this repository from GitHub. An administrator can stop that.',
  );
  assert.match(trackSentence(true, viewer), /An administrator can stop that\.$/);

  // Starting again is an operator's, so an operator is told nothing they cannot do.
  assert.equal(trackSentence(false, admin), 'Kennel Club is not looking at this repository.');
  assert.equal(trackSentence(false, operator), 'Kennel Club is not looking at this repository.');
  assert.equal(
    trackSentence(false, viewer),
    'Kennel Club is not looking at this repository. An operator can start it again.',
  );
});

test('stopping tracking says what it does before it is done', () => {
  const said = TRACKING_STOP.consequences.join(' ');
  for (const part of ['not read from GitHub', 'no problem', 'counted apart', 'waivers are kept']) {
    assert.ok(said.includes(part), `the warning does not say "${part}"`);
  }
});

test('every way to choose how long a waiver runs is shorter than the controller allows', () => {
  const days = WAIVER_EXPIRY_CHOICES.map((choice) => Number(choice.value));
  assert.deepEqual(
    days,
    [...days].sort((a, b) => a - b),
  );
  // 365 is the controller's own limit: the longest choice stays inside it.
  assert.ok(Math.max(...days) < 365);
  assert.ok(Math.min(...days) > 0);
});

test('a waiver ends the stated number of days after it is made', () => {
  const now = new Date('2026-10-07T09:00:00.000Z');
  assert.equal(waiverEndsAt(30, now), '2026-11-06T09:00:00.000Z');
  assert.equal(waiverEndsAt(364, now), '2027-10-06T09:00:00.000Z');
});

test('an error is waived by an administrator and anything lighter by an operator', () => {
  assert.equal(waiverRole('error'), 'admin');
  assert.equal(waiverRole('warning'), 'operator');
  assert.equal(waiverRole('info'), 'operator');
});

/* -- the on/off switch ------------------------------------------------------ */

test('the line under the switch says the state, and says who can change it to somebody who cannot', () => {
  // Not heard yet: a switch that has not heard must not say it is off.
  assert.equal(kennelSwitchSentence(null, true), '');
  assert.equal(kennelSwitchSentence(null, false), '');
  assert.equal(kennelSwitchSentence(true, true), 'Kennel Club is on and reading from GitHub.');
  assert.equal(kennelSwitchSentence(false, true), 'Kennel Club is off and reads nothing.');
  assert.equal(
    kennelSwitchSentence(true, false),
    'Kennel Club is on and reading from GitHub. An administrator can change that.',
  );
  assert.equal(
    kennelSwitchSentence(false, false),
    'Kennel Club is off and reads nothing. An administrator can change that.',
  );
});

test('turning it off says what that does before it is done, including that AI Context is untouched', () => {
  const said = KENNEL_TURN_OFF.consequences.join(' ');
  assert.match(said, /stops reading from GitHub/);
  assert.match(said, /problems list/);
  assert.match(said, /kept, and shown again/);
  assert.match(said, /AI Context is not affected/);
});

test('a change that held is a plain success', () => {
  assert.deepEqual(kennelSwitchOutcome(true, { value: true, env: 'ZOOMIES_KENNEL_ENABLED' }), {
    holds: true,
    message: '',
  });
  assert.deepEqual(kennelSwitchOutcome(false, { value: false }), { holds: false, message: '' });
});

// The environment has the last word. A switch that moved because it was pressed
// would show what was asked for, and the controller would go on doing the opposite.
test('a change the environment overruled is not a success, and says so naming the variable', () => {
  const stayedOff = kennelSwitchOutcome(true, { value: false, env: 'ZOOMIES_KENNEL_ENABLED' });
  assert.equal(stayedOff.holds, false);
  assert.match(stayedOff.message, /ZOOMIES_KENNEL_ENABLED is set/);
  assert.match(stayedOff.message, /stays off/);

  const stayedOn = kennelSwitchOutcome(false, { value: true, env: 'ZOOMIES_KENNEL_ENABLED' });
  assert.equal(stayedOn.holds, true);
  assert.match(stayedOn.message, /stays on/);
});

test('without the setting in the answer the request is taken at its word, and the variable is still named', () => {
  assert.deepEqual(kennelSwitchOutcome(true, undefined), { holds: true, message: '' });
  assert.match(
    kennelSwitchOutcome(true, { value: false }).message,
    /ZOOMIES_KENNEL_ENABLED is set/,
  );
});
