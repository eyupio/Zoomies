import { test } from 'node:test';
import assert from 'node:assert/strict';
import { cardLine } from '../src/lib/hosts/health-card.ts';
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
