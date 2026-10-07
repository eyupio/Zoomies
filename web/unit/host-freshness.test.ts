import { test } from 'node:test';
import assert from 'node:assert/strict';
import { freshness } from '../src/lib/hosts/freshness.ts';
import type { Host } from '../src/lib/api/types.ts';

const now = Date.parse('2026-10-04T10:00:00Z');
const summary = { counted: 3, warnings: 0, errors: 0, skipped: 0, suggestions: 0 };
function report(over: Record<string, unknown> = {}): Host['doctor'] {
  return {
    checked_at: new Date(now - 40_000).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: false,
    results: [],
    summary,
    ...over,
  } as Host['doctor'];
}
const run = (r: Host['doctor'], healthy = true, stale = false) =>
  freshness({ report: r, healthy, stale, now });

test('a fresh report says when it was checked and that the page updates by itself', () => {
  const f = run(report());
  assert.equal(f?.lead, 'Report checked');
  assert.match(f?.tail ?? '', /updates by itself when the agent sends a newer one/);
  assert.equal(f?.future, false);
});

test('a disconnected host says nothing newer can arrive, ahead of staleness', () => {
  // Old and gone are different problems; the connection is the one a person can act on.
  const f = run(report(), false, true);
  assert.equal(f?.lead, 'Last report checked');
  assert.match(f?.tail ?? '', /not connected/);
});

test('a stale report from a connected agent says the agent has sent nothing newer', () => {
  assert.match(run(report(), true, true)?.tail ?? '', /connected but has sent nothing newer/);
});

test('a container report says where it came from', () => {
  assert.match(run(report({ container: true }))?.tail ?? '', /from inside the container/);
});

test('no report, no count or an unreadable date gives no line', () => {
  assert.equal(run(undefined), null);
  assert.equal(run(report({ summary: undefined })), null);
  assert.equal(run(report({ checked_at: 'yesterday-ish' })), null);
});

test('a report dated ahead of the clock is flagged so the page prints just now', () => {
  assert.equal(run(report({ checked_at: new Date(now + 5_000).toISOString() }))?.future, true);
});

test('no wording promises a cadence', () => {
  // The page cannot see the heartbeat interval, so any number would be a guess.
  for (const f of [
    run(report()),
    run(report(), false, true),
    run(report(), true, true),
    run(report({ container: true })),
  ])
    assert.doesNotMatch(`${f?.lead}${f?.tail}`, /\d|every|minute|second/i);
});
