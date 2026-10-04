import { test } from 'node:test';
import assert from 'node:assert/strict';
import { AUTOMATIC_ACTIONS, automaticTitle, causeOf, subjectOf } from '../src/lib/feed/cause.ts';

test('an automatic change carries its reason, and an operator’s own action has none', () => {
  assert.equal(
    causeOf({ after: JSON.stringify({ max_runners: 10, cause: ' a host joined ' }) }),
    'a host joined',
  );
  assert.equal(causeOf({ after: JSON.stringify({ max_runners: 10 }) }), undefined);
  assert.equal(causeOf({ after: JSON.stringify({ cause: '' }) }), undefined);
  assert.equal(causeOf({ after: JSON.stringify({ cause: 7 }) }), undefined);
  assert.equal(causeOf({ after: 'not json' }), undefined);
  assert.equal(causeOf({ after: 'null' }), undefined);
  assert.equal(causeOf({ after: undefined }), undefined);
});

test('every automatic change has a title, and an operator’s action is not one', () => {
  for (const action of [
    'pool.auto_create',
    'pool.auto_resize',
    'pool.auto_enable',
    'pool.auto_disable',
    'pool.auto_reshape',
    'host.size_class',
    'size_class.move',
  ]) {
    assert.ok(automaticTitle(action), `${action} is named`);
  }
  // What an operator did to the same pool or host is "who changed what", and
  // stays in that category: the pin they set, the pool they edited.
  for (const action of ['pool.update', 'size_pin.set', 'size_pin.delete', 'host.update', '']) {
    assert.equal(automaticTitle(action), undefined, `${action} is not automatic`);
  }
  assert.equal(automaticTitle(undefined), undefined);
  assert.equal(Object.keys(AUTOMATIC_ACTIONS).length, 7);
});

test('an automatic change is about something by name, not by the id the row carries', () => {
  assert.equal(
    subjectOf({ after: JSON.stringify({ name: 'zoomies-medium', cause: 'x' }) }),
    'zoomies-medium',
  );
  assert.equal(
    subjectOf({ after: JSON.stringify({ class: 'large', host: 'build-2' }) }),
    'build-2',
  );
  // A pool's name wins where a row carries both.
  assert.equal(subjectOf({ after: JSON.stringify({ name: 'p', host: 'h' }) }), 'p');
  assert.equal(subjectOf({ after: JSON.stringify({ class: 'large' }) }), undefined);
  assert.equal(subjectOf({ after: JSON.stringify({ name: 3 }) }), undefined);
  assert.equal(subjectOf({ after: 'not json' }), undefined);
  assert.equal(subjectOf({ after: undefined }), undefined);
});
