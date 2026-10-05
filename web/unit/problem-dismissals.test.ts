import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  carriedOver,
  covers,
  fromServer,
  spentKeys,
  type Dismissals,
} from '../src/lib/problems/dismissals.ts';
import type { Problem } from '../src/lib/api/types.ts';

const NOW = Date.parse('2026-10-05T12:00:00Z');
const iso = (offsetMs: number) => new Date(NOW + offsetMs).toISOString();
const HOUR = 60 * 60_000;

const problem = (over: Partial<Problem> = {}): Problem => ({
  code: 'pool.no_capacity',
  severity: 'warning',
  title: 'pool zoomies-linux-x64 has 6 jobs waiting for a host with room',
  target_kind: 'pool',
  target_id: 'pool_x64',
  ...over,
});

// The bug this guards: a queue drains for one reconciliation pass, the
// controller stops reporting pool.no_capacity, and the sweep treated that as
// "resolved" and forgot a 24 hour snooze. The fault came back and so did the
// banner, hours early.
test('a snooze outlives the problem clearing for a pass', () => {
  const held: Dismissals = {
    'pool.no_capacity|pool|pool_x64||': { severity: 'warning', at: iso(0), until: iso(24 * HOUR) },
    'type:pool.no_capacity': { severity: 'warning', at: iso(0), until: iso(24 * HOUR) },
  };
  assert.deepEqual(spentKeys(held, new Set(), NOW + HOUR), []);
});

test('a snooze of a whole kind survives with no problem of that kind reported', () => {
  const held: Dismissals = {
    'type:pool.no_capacity': { severity: 'warning', at: iso(0), until: iso(4 * HOUR) },
  };
  assert.deepEqual(spentKeys(held, new Set(['host.unhealthy|host|h1||']), NOW + HOUR), []);
});

test('a snooze is spent when its own clock runs out, and not before', () => {
  const held: Dismissals = {
    a: { severity: 'info', at: iso(0), until: iso(HOUR) },
  };
  assert.deepEqual(spentKeys(held, new Set(['a']), NOW + HOUR - 1), []);
  assert.deepEqual(spentKeys(held, new Set(['a']), NOW + HOUR), ['a']);
});

test('a plain dismissal is still forgotten once the problem stops being reported', () => {
  const held: Dismissals = {
    a: { severity: 'info', at: iso(0) },
    b: { severity: 'info', at: iso(0) },
  };
  assert.deepEqual(spentKeys(held, new Set(['a']), NOW), ['b']);
});

test('a dismissal does not cover a problem that has become worse', () => {
  const record = { severity: 'warning' as const, at: iso(0), until: iso(HOUR) };
  assert.equal(covers(record, problem({ severity: 'warning' }), NOW), true);
  assert.equal(covers(record, problem({ severity: 'info' }), NOW), true);
  assert.equal(covers(record, problem({ severity: 'error' }), NOW), false);
});

test('an expired snooze covers nothing', () => {
  const record = { severity: 'warning' as const, at: iso(-2 * HOUR), until: iso(-HOUR) };
  assert.equal(covers(record, problem(), NOW), false);
});

test('what the server returns is adopted, and a snooze the clock says is over is not', () => {
  const got = fromServer(
    [
      { key: 'live', severity: 'warning', at: iso(0), until: iso(HOUR) },
      { key: 'stale', severity: 'warning', at: iso(-2 * HOUR), until: iso(-HOUR) },
      { key: 'plain', severity: 'info', at: iso(0) },
    ],
    NOW,
  );
  assert.deepEqual(Object.keys(got).sort(), ['live', 'plain']);
  assert.equal(got.plain.until, undefined);
});

// A decision already on the account was made on purpose and more recently than
// whatever has sat in a browser, so uploading the old copy over it would undo it.
test('carrying a browser copy up never overwrites what the account already holds', () => {
  const local: Dismissals = {
    held: { severity: 'info', at: iso(-HOUR) },
    fresh: { severity: 'warning', at: iso(-HOUR), until: iso(HOUR) },
    spent: { severity: 'warning', at: iso(-3 * HOUR), until: iso(-HOUR) },
  };
  const remote: Dismissals = { held: { severity: 'error', at: iso(0), until: iso(HOUR) } };
  assert.deepEqual(
    carriedOver(local, remote, NOW).map((d) => d.key),
    ['fresh'],
  );
});
