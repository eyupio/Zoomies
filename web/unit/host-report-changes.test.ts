import { test } from 'node:test';
import assert from 'node:assert/strict';
import { allClear, announcement, logLine, reportChanges } from '../src/lib/hosts/report-changes.ts';
import type { Host } from '../src/lib/api/types.ts';

type Report = NonNullable<Host['doctor']>;
type Result = Report['results'][number];
const t0 = Date.parse('2026-10-04T10:00:00Z');
const ctx = { prevVersion: 'v1', nextVersion: 'v1' };

function row(id: string, status: Result['status'], extra: Partial<Result> = {}): Result {
  return {
    id,
    title: `Title of ${id}`,
    tier: 'safe',
    status,
    current: '',
    recommended: '',
    rationale: '',
    actionable: false,
    ...extra,
  } as Result;
}
function report(results: Result[], over: Record<string, unknown> = {}, at = t0): Report {
  const counted = results.filter((r) => r.tier === 'safe' && r.optional !== true);
  return {
    checked_at: new Date(at).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: false,
    results,
    summary: {
      counted: counted.length,
      warnings: counted.filter((r) => r.status === 'warn').length,
      errors: counted.filter((r) => r.status === 'error').length,
      skipped: counted.filter((r) => r.status === 'skip').length,
      suggestions: 0,
    },
    ...over,
  } as Report;
}
const later = (s: number) => t0 + s * 1000;

test('a check that goes from warning to OK is reported as resolved', () => {
  const prev = report([row('a', 'warn'), row('b', 'ok')]);
  const next = report([row('a', 'ok'), row('b', 'ok')], {}, later(60));
  assert.deepEqual(
    reportChanges(prev, next, ctx).map((c) => [c.id, c.kind]),
    [['a', 'resolved']],
  );
});

test('a check that goes from OK to warning is new, and warning to error is escalated', () => {
  const prev = report([row('a', 'ok'), row('b', 'warn'), row('c', 'ok')]);
  const next = report([row('a', 'warn'), row('b', 'error'), row('c', 'ok')], {}, later(60));
  const kinds = Object.fromEntries(reportChanges(prev, next, ctx).map((c) => [c.id, c.kind]));
  assert.deepEqual(kinds, { b: 'escalated', a: 'new' });
});

test('an error that eases to a warning is not announced', () => {
  // Better, but not fixed: telling someone it cleared would be wrong.
  const prev = report([row('a', 'error')]);
  const next = report([row('a', 'warn')], {}, later(60));
  assert.deepEqual(reportChanges(prev, next, ctx), []);
});

test('a skipped check is not an OK one', () => {
  // A stopped Docker daemon skips a group at once; "now OK" would be false.
  const prev = report([row('a', 'warn')]);
  const next = report([row('a', 'skip')], {}, later(60));
  assert.deepEqual(reportChanges(prev, next, ctx), []);
});

test('only counted checks are announced', () => {
  // The pill ignores optional and non-safe tiers, so the page must too.
  const prev = report([
    row('a', 'warn', { optional: true }),
    row('b', 'warn', { tier: 'aggressive' }),
  ]);
  const next = report(
    [row('a', 'ok', { optional: true }), row('b', 'ok', { tier: 'aggressive' })],
    {},
    later(60),
  );
  assert.deepEqual(reportChanges(prev, next, ctx), []);
});

test('the first report seen announces nothing', () => {
  assert.deepEqual(reportChanges(undefined, report([row('a', 'warn')]), ctx), []);
  assert.deepEqual(reportChanges(report([row('a', 'warn')]), undefined, ctx), []);
});

test('a repeated, older or undated report announces nothing', () => {
  // Frames are replayed and cordon frames carry the same report; only a later check time is news.
  const prev = report([row('a', 'warn')]);
  const ok = [row('a', 'ok')];
  assert.deepEqual(reportChanges(prev, report(ok), ctx), []);
  assert.deepEqual(reportChanges(prev, report(ok, {}, t0 - 1000), ctx), []);
  assert.deepEqual(reportChanges(prev, report(ok, { checked_at: 'soon' }), ctx), []);
});

test('a different source is not compared', () => {
  const prev = report([row('a', 'warn')]);
  const next = report([row('a', 'ok')], {}, later(60));
  assert.deepEqual(reportChanges(prev, next, { prevVersion: 'v1', nextVersion: 'v2' }), []);
  assert.deepEqual(reportChanges(prev, { ...next, distro: 'debian 12' }, ctx), []);
});

test('partial and report-only reports are never compared', () => {
  const prev = report([row('a', 'warn')]);
  assert.deepEqual(
    reportChanges(prev, report([row('a', 'ok')], { container: true }, later(60)), ctx),
    [],
  );
  const envSkip = [row('environment', 'skip'), row('a', 'ok')];
  assert.deepEqual(reportChanges(prev, report(envSkip, {}, later(60)), ctx), []);
  assert.deepEqual(
    reportChanges(prev, report([row('a', 'ok')], { summary: undefined }, later(60)), ctx),
    [],
  );
});

test('a check present in only one report is unknown and stays silent', () => {
  const prev = report([row('a', 'warn')]);
  const next = report([row('b', 'warn')], {}, later(60));
  assert.deepEqual(reportChanges(prev, next, ctx), []);
});

test('the kernel warning folded into a pending reboot is not a change', () => {
  const prev = report([row('kernel.pending', 'warn')], { reboot_pending: true });
  const next = report([row('kernel.pending', 'ok')], { reboot_pending: false }, later(60));
  assert.deepEqual(reportChanges(prev, next, ctx), []);
});

test('several changes make one announcement, worded without a verb to agree', () => {
  const one = reportChanges(
    report([row('a', 'warn')]),
    report([row('a', 'ok')], {}, later(60)),
    ctx,
  );
  assert.equal(announcement(one, false), 'Title of a — now OK');
  const ids = ['a', 'b', 'c', 'd', 'e'];
  const prev = report(ids.map((i) => row(i, 'warn')));
  const next = report(
    ids.map((i) => row(i, 'ok')),
    {},
    later(60),
  );
  const many = reportChanges(prev, next, ctx);
  assert.equal(
    announcement(many, true),
    '5 checks are now OK. Nothing on this host needs attention now',
  );
  assert.equal(announcement(many.slice(0, 2), false), 'Title of a and Title of b — now OK');
  assert.equal(announcement([], true), '');
});

test('new, error and escalated changes are each worded as what they are', () => {
  const prev = report([row('a', 'ok'), row('b', 'ok'), row('c', 'warn')]);
  const next = report([row('a', 'warn'), row('b', 'error'), row('c', 'error')], {}, later(60));
  const lines = reportChanges(prev, next, ctx).map(logLine);
  assert.deepEqual(lines.sort(), [
    'Title of a — now needs attention',
    'Title of b — now has an error',
    'Title of c — warning became an error',
  ]);
  assert.match(announcement(reportChanges(prev, next, ctx), false), /warning became an error/);
});

test('a long title is cut and no announcement carries a time', () => {
  const prev = report([row('a', 'warn', { title: 'x'.repeat(200) })]);
  const next = report([row('a', 'ok', { title: 'x'.repeat(200) })], {}, later(60));
  const text = announcement(reportChanges(prev, next, ctx), false);
  assert.ok(text.length < 100);
  assert.doesNotMatch(text, /just now|ago|\d+s\b/);
});

test('allClear needs no findings and a Health OK verdict', () => {
  assert.equal(allClear(report([row('a', 'ok'), row('b', 'ok')]), t0), true);
  assert.equal(allClear(report([row('a', 'warn'), row('b', 'ok')]), t0), false);
});
