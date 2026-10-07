import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  ACCEPT_EXPIRY_CHOICES,
  acceptEndsAt,
  acceptNote,
  acceptedIds,
  acceptedLast,
  canAccept,
  endedNote,
} from '../src/lib/hosts/accept.ts';
import { attention, healthSummary } from '../src/lib/hosts/health.ts';
import type { DoctorResult } from '../src/lib/hosts/health.ts';
import { reportChanges } from '../src/lib/hosts/report-changes.ts';
import { rowKind } from '../src/lib/hosts/row-kind.ts';
import { REASON_MAX, REASON_MIN, reasonHint, reasonLength } from '../src/lib/reason.ts';
import type { Host } from '../src/lib/api/types.ts';

type Report = NonNullable<Host['doctor']>;
const t0 = Date.parse('2026-10-04T10:00:00Z');
const ctx = { prevVersion: 'v1', nextVersion: 'v1' };
const grant = {
  reason: 'Pinned on purpose until the vendor kernel ships',
  by: 'Sam',
  at: '2026-10-01T10:00:00Z',
  expires_at: '2027-10-01T10:00:00Z',
};

function row(id: string, status: DoctorResult['status'], over: Partial<DoctorResult> = {}) {
  return {
    id,
    title: `Title of ${id}`,
    tier: 'safe',
    status,
    current: 'now',
    recommended: 'later',
    rationale: '',
    actionable: false,
    ...over,
  } as DoctorResult;
}
function report(results: DoctorResult[], at = t0): Report {
  const counted = results.filter((r) => r.tier === 'safe' && r.optional !== true);
  const accepted = counted.filter((r) => r.accepted).length;
  return {
    checked_at: new Date(at).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: false,
    results,
    summary: {
      counted: counted.length,
      warnings: counted.filter((r) => r.status === 'warn').length - accepted,
      errors: counted.filter((r) => r.status === 'error').length,
      skipped: counted.filter((r) => r.status === 'skip').length,
      suggestions: 0,
      accepted,
    },
  } as Report;
}

test('an accepted warning leaves Needs attention, so its length is still warnings plus errors', () => {
  // The controller stops counting it; a list that kept it would disagree with
  // the pill beside it.
  const r = report([row('a', 'warn', { accepted: grant }), row('b', 'warn')]);
  assert.deepEqual(
    attention(r).map((c) => c.id),
    ['b'],
  );
  assert.equal(attention(r).length, r.summary.warnings + r.summary.errors);
});

test('the pill hint says how many checks were accepted, and says nothing when none were', () => {
  const some = report([row('a', 'warn', { accepted: grant }), row('b', 'ok')]);
  const hint = healthSummary(some, t0 + 1000).hint;
  assert.match(hint, /Also 1 accepted check, which do not count towards health\./);
  assert.equal(healthSummary(some, t0 + 1000).label, 'Health OK');
  assert.doesNotMatch(healthSummary(report([row('b', 'ok')]), t0 + 1000).hint, /accepted/);
});

test('a controller that predates acceptance reads as none accepted', () => {
  const old = report([row('b', 'ok')]);
  delete (old.summary as Partial<Report['summary']>).accepted;
  assert.equal(healthSummary(old, t0 + 1000).accepted, 0);
});

test('an accepted row has no Fixable, Advice or Optional word', () => {
  assert.equal(rowKind(row('a', 'warn', { accepted: grant, actionable: true }), false), null);
  assert.equal(rowKind(row('a', 'warn', { actionable: true }), false), 'fixable');
});

test('only a row the controller marks acceptable offers Accept, and an accepted one no longer does', () => {
  assert.equal(canAccept(row('a', 'warn', { acceptable: true })), true);
  assert.equal(canAccept(row('a', 'warn')), false);
  assert.equal(canAccept(row('a', 'warn', { acceptable: true, accepted: grant })), false);
});

test('accepted rows sort after the unaccepted ones and keep their order otherwise', () => {
  const rows = [
    row('a', 'warn', { accepted: grant }),
    row('b', 'warn'),
    row('c', 'warn', { accepted: grant }),
    row('d', 'warn'),
  ];
  assert.deepEqual(
    acceptedLast(rows).map((r) => r.id),
    ['b', 'd', 'a', 'c'],
  );
});

test('the accepted note names who, when, until when and the reason, as plain text', () => {
  const now = Date.parse('2026-10-04T10:00:00Z');
  const note = acceptNote({ ...grant, reason: '<b>why</b>' }, now);
  assert.match(note, /^Accepted by Sam 3d ago, until [^:]*2027: “<b>why<\/b>”$/);
});

test('an ended acceptance says why the row counts again, in the three ways it can', () => {
  const was = { by: 'Sam', at: '2026-10-01T10:00:00Z', was: '5.15' };
  assert.match(
    endedNote({ current: '5.4', ended: { ...was, why: 'changed' } }) ?? '',
    /while it read “5\.15”\. It now reads “5\.4”, so it counts again\. Accept it again/,
  );
  assert.match(
    endedNote({ current: '5.15', ended: { ...was, why: 'expired' } }) ?? '',
    /^Sam’s acceptance ended on .+, so it counts again\.$/,
  );
  assert.match(
    endedNote({ current: '', ended: { ...was, why: 'worse' } }) ?? '',
    /because the check could not run/,
  );
  assert.equal(endedNote({ current: 'x' }), null);
});

test('an accept frame announces nothing, and neither does an accepted check turning OK', () => {
  // The decision is the person's own; announcing "resolved" for it, or "needs
  // attention" for a revoke, would be the page contradicting what they just did.
  const before = report([row('a', 'warn')]);
  const accepted = report([row('a', 'warn', { accepted: grant })]);
  assert.deepEqual(reportChanges(before, accepted, ctx), []);
  const ok = report([row('a', 'ok')], t0 + 60_000);
  assert.deepEqual(reportChanges(accepted, ok, ctx), []);
});

test('the accepted ids are compared as a sorted set, so a frame that moves one is told from a heartbeat', () => {
  const one = [row('b', 'warn', { accepted: grant }), row('a', 'warn')];
  const two = [row('b', 'warn', { accepted: grant }), row('a', 'warn', { accepted: grant })];
  assert.equal(acceptedIds(one), 'b');
  assert.equal(acceptedIds(two), 'a\nb');
  assert.equal(acceptedIds([...two].reverse()), acceptedIds(two));
});

test('every way to choose how long an acceptance runs is shorter than the controller allows, and none is shorter than a week', () => {
  const days = ACCEPT_EXPIRY_CHOICES.map((choice) => Number(choice.value));
  assert.ok(Math.max(...days) <= 364);
  assert.equal(Math.min(...days), 7);
  const now = new Date('2026-10-04T10:00:00Z');
  assert.equal(acceptEndsAt(7, now), '2026-10-11T10:00:00.000Z');
});

test('the reason is held to the bounds the controller states', () => {
  assert.equal(REASON_MIN, 10);
  assert.equal(REASON_MAX, 500);
  assert.equal(reasonLength('🐕🐕🐕🐕🐕🐕🐕🐕🐕🐕'), 10);
  assert.match(reasonHint('short', 'deliberate'), /say why this is deliberate/);
});
