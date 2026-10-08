import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cardLine, diskIsLow, diskLink } from '../src/lib/hosts/health-card.ts';
import type { DoctorReport, DoctorResult } from '../src/lib/hosts/health.ts';

function check(id: string, title: string, status: DoctorResult['status'] = 'warn'): DoctorResult {
  return {
    id,
    title,
    tier: 'safe',
    status,
    current: '',
    recommended: '',
    rationale: '',
    actionable: false,
  };
}

function host(results: DoctorResult[], over: Partial<DoctorReport> = {}) {
  const doctor: DoctorReport = {
    checked_at: '2026-10-04T10:00:00Z',
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: false,
    results,
    summary: { counted: results.length, warnings: 0, errors: 0, skipped: 0, suggestions: 0 },
    ...over,
  };
  return { id: 'host_1', doctor };
}
const fresh = { stale: false };

test('the card names one, two or three checks in the order Needs attention lists them', () => {
  // The line must never disagree with the host page, so errors come first.
  const one = cardLine(host([check('a', 'Disk space', 'error')]), fresh);
  assert.equal(one.text, 'Disk space');
  const two = cardLine(
    host([check('a', 'File watches'), check('b', 'Disk space', 'error')]),
    fresh,
  );
  assert.equal(two.text, 'Disk space and File watches');
  const four = cardLine(
    host([check('a', 'A'), check('b', 'B'), check('c', 'C'), check('d', 'D')]),
    fresh,
  );
  assert.equal(four.text, 'A, B and 2 more');
});

test('the link lands on the first check named and encodes its id', () => {
  const line = cardLine(host([check('disk space/x', 'Disk', 'error')]), fresh);
  assert.equal(line.firstId, 'disk space/x');
  assert.equal(line.href, '/hosts/host_1#disk%20space%2Fx');
});

test('a stale report, a container and a clean host get no line and no hash', () => {
  // A stale or container card must never list last-known checks as current.
  const warn = [check('a', 'Disk space')];
  for (const line of [
    cardLine(host(warn), { stale: true }),
    cardLine(host(warn, { container: true }), fresh),
    cardLine(host([check('a', 'Disk space', 'ok')]), fresh),
    cardLine(host([check('kernel.pending', 'Reboot')], { reboot_pending: true }), fresh),
    cardLine({ id: 'host_1', doctor: undefined }, fresh),
  ]) {
    assert.equal(line.text, '');
    assert.equal(line.firstId, null);
    assert.equal(line.href, '/hosts/host_1');
  }
});

test('a title a host wrote at length is clipped so the card keeps its shape', () => {
  const line = cardLine(host([check('a', 'x'.repeat(200), 'error')]), fresh);
  assert.equal([...line.text].length, 40);
  assert.ok(line.text.endsWith('…'));
});

test('the card calls a disk low by the same rule as the check, strictly below both limits', () => {
  // The card and the Warning row must never disagree: 8 GiB free of 60 is a
  // Warning row, so it must be amber too.
  assert.equal(diskIsLow(8 * 1024, 60 * 1024), true, 'under 10 GiB');
  assert.equal(diskIsLow(10 * 1024, 100 * 1024), false, 'exactly 10 GiB and 10 percent');
  assert.equal(diskIsLow(10 * 1024 - 1, 100 * 1024), true, 'a MiB under 10 GiB');
  assert.equal(diskIsLow(51_200, 512_000), false, 'exactly 10 percent of a big disk');
  assert.equal(diskIsLow(51_199, 512_000), true, 'just under 10 percent of a big disk');
  assert.equal(diskIsLow(31 * 1024, 512 * 1024), true, 'six percent');
  assert.equal(diskIsLow(0, 0), false, 'unmeasured is not full');
  assert.equal(diskIsLow(500, 0), false, 'no total, no verdict');
});

test('the disk figure links to its check only when a current full report has one', () => {
  const withDisk = host([check('disk.space', 'Work directory free space', 'ok')]);
  assert.equal(diskLink(withDisk, fresh), '/hosts/host_1#disk.space');
  assert.equal(diskLink(withDisk, { stale: true }), null, 'a stale report is not evidence');
  assert.equal(diskLink(host([check('a', 'Other')]), fresh), null, 'no disk row to land on');
  assert.equal(
    diskLink(host([check('disk.space', 'Free', 'warn')], { container: true }), fresh),
    null,
    'a container sees its own filesystem',
  );
  assert.equal(diskLink({ id: 'host_1' }, fresh), null, 'no report at all');
  assert.equal(
    diskLink({ ...withDisk, id: 'a/b' }, fresh),
    '/hosts/a%2Fb#disk.space',
    'the id is escaped in the path',
  );
});
