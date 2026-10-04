import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  autoPoolLine,
  classWord,
  labelsFromRows,
  overrideNote,
  pendingMove,
  tagRows,
  tagText,
} from '../src/lib/hosts/tags.ts';
import type { HostAutoPool, HostSizeClass, HostTag } from '../src/lib/api/types.ts';

const tags: HostTag[] = [
  { key: 'arch', value: 'amd64', source: 'automatic' },
  { key: 'gpu', value: '', source: 'operator' },
  { key: 'os', value: 'linux', source: 'automatic' },
  { key: 'rack', value: 'b4', source: 'operator' },
  { key: 'size', value: 'large', source: 'operator', overrides: 'medium' },
];

test('a tag is written the way an operator types it, and a flag is just its name', () => {
  assert.equal(tagText({ key: 'rack', value: 'b4' }), 'rack=b4');
  assert.equal(tagText({ key: 'gpu', value: '' }), 'gpu');
});

test('the operator’s tags come first and keep their order, then the ones worked out', () => {
  const rows = tagRows(tags);
  assert.deepEqual(
    rows.map((row) => `${row.automatic ? 'auto' : 'own'}:${row.text}`),
    ['own:gpu', 'own:rack=b4', 'own:size=large', 'auto:arch=amd64', 'auto:os=linux'],
  );
  assert.deepEqual(tagRows(undefined), []);
});

test('a tag that replaces what the machine measures says what it replaced', () => {
  const size = tagRows(tags).find((row) => row.key === 'size');
  assert.match(size?.hint ?? '', /replaces size=medium/);
  // And an ordinary tag does not claim to replace anything.
  const rack = tagRows(tags).find((row) => row.key === 'rack');
  assert.doesNotMatch(rack?.hint ?? '', /replaces/);
});

test('a derived tag cannot be mistaken for one that can be edited', () => {
  for (const row of tagRows(tags).filter((r) => r.automatic)) {
    assert.match(row.hint, /controller/);
  }
  const derivedSize = tagRows([{ key: 'size', value: 'medium', source: 'automatic' }])[0];
  assert.match(derivedSize?.hint ?? '', /Set a size tag of your own/);
});

test('a class is capitalised for a badge, and nothing is a class of no name', () => {
  assert.equal(classWord('medium'), 'Medium');
  assert.equal(classWord(undefined), '');
});

test('only a class an operator set is explained in the open', () => {
  const measured: HostSizeClass = { class: 'medium', source: 'measured', reason: 'x' };
  assert.equal(overrideNote(measured), '');
  assert.equal(overrideNote(undefined), '');

  const set: HostSizeClass = { class: 'large', source: 'tag', measured: 'medium', reason: 'x' };
  assert.equal(overrideNote(set), "Set by this host's size tag. Its machine measures as medium.");

  // A tag that merely agrees with the machine is still the operator's, and
  // saying so is what stops it silently becoming wrong when the machine changes.
  const agrees: HostSizeClass = { class: 'medium', source: 'tag', reason: 'x' };
  assert.match(overrideNote(agrees), /agrees with what its machine measures/);
});

test('a move being held is reported with when it began and when it takes effect', () => {
  assert.equal(pendingMove({ class: 'small', source: 'measured', reason: 'x' }), null);
  assert.deepEqual(
    pendingMove({
      class: 'small',
      source: 'measured',
      reason: 'x',
      pending: 'medium',
      pending_since: '2026-10-04T10:00:00Z',
      pending_until: '2026-10-04T10:10:00Z',
    }),
    { to: 'medium', since: '2026-10-04T10:00:00Z', until: '2026-10-04T10:10:00Z' },
  );
});

test('the pool line says whether the pool exists, and otherwise gives the controller’s reason', () => {
  assert.equal(autoPoolLine(undefined), null);

  const counted: HostAutoPool = { counted: true, pool: 'zoomies-medium', pool_id: 'pol_1' };
  assert.deepEqual(autoPoolLine(counted), {
    counted: true,
    text: 'Its slots count towards zoomies-medium.',
    poolId: 'pol_1',
  });

  // Report-only: the pool is the one that would be made, so it has no id yet.
  const wouldBe: HostAutoPool = { counted: true, pool: 'zoomies-medium' };
  assert.equal(
    autoPoolLine(wouldBe)?.text,
    'Its slots would count towards zoomies-medium, which has not been made yet.',
  );
  assert.equal(autoPoolLine(wouldBe)?.poolId, undefined);

  const skipped: HostAutoPool = {
    counted: false,
    reason:
      'It is cordoned, so its slots do not count towards an automatic pool until it is uncordoned.',
    reason_code: 'cordoned',
  };
  assert.deepEqual(autoPoolLine(skipped), { counted: false, text: skipped.reason });
});

test('a tag with no value is a flag, and a flag is true whichever surface set it', () => {
  assert.deepEqual(
    labelsFromRows([
      { key: ' rack ', value: ' b4 ' },
      { key: 'gpu', value: '' },
      { key: '  ', value: 'ignored' },
    ]),
    { rack: 'b4', gpu: 'true' },
  );
});

test('saving the dialog does not rewrite a label somebody left empty on purpose', () => {
  // The host already carries `legacy=` with an empty value, and nobody touched
  // it; turning it into `legacy=true` would change what a selector matches.
  assert.deepEqual(
    labelsFromRows(
      [
        { key: 'legacy', value: '' },
        { key: 'gpu', value: '' },
      ],
      { legacy: '' },
    ),
    { legacy: '', gpu: 'true' },
  );
  // A value that was not empty and has been cleared is a new flag, not a keep.
  assert.deepEqual(labelsFromRows([{ key: 'zone', value: '' }], { zone: 'demo' }), {
    zone: 'true',
  });
});
