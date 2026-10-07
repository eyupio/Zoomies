import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  outcomeText,
  partialWindowNote,
  percentileTile,
  placeRows,
  successRate,
  successRateText,
  waitingText,
  windowStart,
} from '../src/lib/kennel/runtime.ts';

const none = {
  count: 0,
  succeeded: 0,
  failed: 0,
  cancelled: 0,
  fleet_failed: 0,
  fleet_failed_by_kind: {},
  fleet_failure_rate: 0,
  duration: { samples: 0, p50_ms: null, p95_ms: null },
  queue_wait: { samples: 0, p50_ms: null, p95_ms: null },
  startup: { samples: 0, p50_ms: null, p95_ms: null },
  peak_cpus: null,
  peak_memory_mb: null,
  oom_killed: 0,
  keys: {},
};
const group = (over: Partial<typeof none>) => ({ ...none, ...over });

test('a window starts the stated number of days before now', () => {
  const now = new Date('2026-10-07T12:00:00.000Z');
  assert.equal(windowStart(7, now), '2026-09-30T12:00:00.000Z');
  assert.equal(windowStart(30, now), '2026-09-07T12:00:00.000Z');
});

test('a window as long as the jobs are kept says it is partial, and a shorter one says nothing', () => {
  assert.equal(partialWindowNote(7), '');
  assert.match(partialWindowNote(30), /by default that is the last 30 days/);
  // When retention is longer, the same window is whole.
  assert.equal(partialWindowNote(30, 90), '');
});

test('how jobs ended is said with the fleet’s failures apart from the workflow’s', () => {
  assert.equal(outcomeText(undefined), 'No jobs finished');
  assert.equal(outcomeText(group({})), 'No jobs finished');
  assert.equal(
    outcomeText(group({ count: 12, succeeded: 10, failed: 2 })),
    '10 succeeded, 2 failed',
  );
  assert.equal(
    outcomeText(group({ count: 14, succeeded: 10, failed: 3, fleet_failed: 1, cancelled: 1 })),
    '10 succeeded, 3 failed, 1 of them lost to this fleet, 1 cancelled or skipped',
  );
});

test('a success rate counts the jobs that had a verdict and not the cancelled ones', () => {
  assert.equal(successRate(undefined), null);
  // Only cancelled jobs: there is no rate to give, and zero would read as one.
  assert.equal(successRate(group({ count: 3, cancelled: 3 })), null);
  assert.equal(successRate(group({ count: 5, succeeded: 3, failed: 1, cancelled: 1 })), 0.75);
  assert.equal(successRateText(group({ count: 5, succeeded: 3, failed: 1, cancelled: 1 })), '75%');
  assert.equal(successRateText(group({ count: 3, cancelled: 3 })), '--');
});

test('a duration is a median with its 95th percentile beside it, or says nothing was measured', () => {
  assert.deepEqual(percentileTile(undefined), { value: '--', detail: 'Nothing was measured' });
  // Zero would read as an instant; nothing measured is not that.
  assert.deepEqual(percentileTile({ p50_ms: null, p95_ms: null }), {
    value: '--',
    detail: 'Nothing was measured',
  });
  assert.deepEqual(percentileTile({ p50_ms: 4_200, p95_ms: 61_000 }), {
    value: '4.2s',
    detail: 'median, 1m 01s at the 95th percentile',
  });
});

test('the places jobs ran are the busiest first, and a place that is gone is still a row', () => {
  const groups = [
    group({ count: 2, keys: { pool: 'pool_gone' } }),
    group({ count: 6, keys: { pool: 'pool_b' } }),
    group({ count: 2, keys: { pool: 'unknown' } }),
  ];
  const names: Record<string, string> = { pool_b: 'build-large' };
  const rows = placeRows(groups, 'pool', (id) => names[id]);
  assert.deepEqual(
    rows.map((r) => [r.name, r.jobs, r.known]),
    [
      ['build-large', 6, true],
      ['Not recorded', 2, false],
      ['pool_gone', 2, false],
    ],
  );
  // The rows add up to the whole, so nobody has to wonder what was dropped.
  assert.equal(
    rows.reduce((sum, r) => sum + r.share, 0),
    1,
  );
});

test('a job with no recorded pool or host is one row that links nowhere, however the API says so', () => {
  // The API's own word is "unknown"; an answer that left the key out or empty
  // means the same, and neither is an ID the fleet could be asked about.
  for (const keys of [{ host: 'unknown' }, { host: '' }, {}] as Array<Record<string, string>>) {
    const [row] = placeRows([group({ count: 4, keys })], 'host', () => {
      throw new Error('an unrecorded host must not be looked up');
    });
    assert.deepEqual(
      [row?.id, row?.name, row?.known],
      ['', 'Not recorded', false],
      JSON.stringify(keys),
    );
  }
});

test('with nothing run there are no shares to divide by zero', () => {
  assert.deepEqual(
    placeRows([], 'host', () => undefined),
    [],
  );
  assert.equal(
    placeRows([group({ count: 0, keys: { host: 'h' } })], 'host', () => 'x')[0]?.share,
    0,
  );
});

test('what is waiting for a label nobody serves is said in the singular and the plural', () => {
  assert.equal(waitingText(0), 'Nothing is waiting for a label no pool serves.');
  assert.equal(waitingText(1), '1 job is waiting for a label no pool serves.');
  assert.equal(waitingText(3), '3 jobs are waiting for a label no pool serves.');
});
