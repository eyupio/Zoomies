import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  attention,
  bySeverity,
  healthSummary,
  type DoctorReport,
  type DoctorResult,
} from '../src/lib/hosts/health.ts';
const now = Date.parse('2026-10-04T10:00:00Z');
function report(status: 'ok' | 'warn' | 'error' | 'skip'): DoctorReport {
  return {
    checked_at: new Date(now).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: false,
    results: [
      {
        id: 'disk.space',
        title: 'Disk space',
        tier: 'safe',
        status,
        current: '',
        recommended: '',
        rationale: '',
        actionable: false,
      },
    ],
  };
}
test('health badges distinguish warning, error, unknown and healthy', () => {
  assert.equal(healthSummary(undefined, now).label, 'Health unavailable');
  assert.equal(healthSummary(report('ok'), now).label, 'Health OK');
  assert.equal(healthSummary(report('warn'), now).label, '1 warning');
  assert.equal(healthSummary(report('error'), now).tone, 'danger');
  assert.equal(healthSummary(report('skip'), now).label, 'Checks unavailable');
});
test('freshness and reboot status update when time or the report changes', () => {
  const r = report('ok');
  r.reboot_pending = true;
  assert.equal(healthSummary(r, now).label, 'Reboot pending');
  assert.equal(healthSummary(r, now + 4 * 60_000).label, 'Reboot pending · stale');
  assert.equal(healthSummary(r, now, false).stale, true);
  r.reboot_pending = false;
  assert.equal(healthSummary(r, now).label, 'Health OK');
});
test('partial container reports never claim full OS visibility', () => {
  const r = report('ok');
  r.container = true;
  assert.match(healthSummary(r, now).hint, /Partial report/);
});

function result(
  id: string,
  status: DoctorResult['status'],
  tier: DoctorResult['tier'] = 'safe',
  optional = false,
): DoctorResult {
  return {
    id,
    title: id,
    tier,
    status,
    current: '',
    recommended: '',
    rationale: '',
    actionable: false,
    optional,
  };
}
function reportOf(results: DoctorResult[], reboot = false): DoctorReport {
  return {
    checked_at: new Date(now).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: reboot,
    results,
  };
}

// The monitor reports every tier, but `zoomies doctor` counts the safe one, and
// the docs call the others choices. A host that has made none of them is not
// unwell, and a badge that disagrees with the command the page names is
// believed by nobody.
test("only safe, non-optional checks count towards a host's health", () => {
  const r = reportOf([
    result('inotify.watches', 'ok'),
    result('memory.swappiness', 'warn', 'aggressive'),
    result('service.multipathd', 'warn', 'dedicated'),
    result('service.apport', 'warn', 'dedicated'),
    result('tmp.tmpfs', 'warn', 'aggressive', true),
    result('docker.logs', 'warn', 'safe', true),
  ]);
  const s = healthSummary(r, now);
  assert.equal(s.label, 'Health OK');
  assert.equal(s.tone, 'idle');
  assert.equal(s.warnings, 0);
  assert.equal(s.suggestions, 5);
  assert.match(s.hint, /Also 5 optional suggestions/);
  assert.deepEqual(attention(r), []);
});

test('a safe warning still counts when other tiers are noisy', () => {
  const r = reportOf([
    result('inotify.watches', 'warn'),
    result('service.apport', 'warn', 'dedicated'),
  ]);
  assert.equal(healthSummary(r, now).label, '1 warning');
});

// A reboot used to return before the errors were read, so a full disk showed as
// an amber "Reboot pending" and the danger tone could not be reached at all.
test('an error outranks a pending reboot, and both are said', () => {
  const r = reportOf([result('disk.space', 'error'), result('inotify.watches', 'warn')], true);
  const s = healthSummary(r, now);
  assert.equal(s.label, '1 health error · 1 warning · reboot pending');
  assert.equal(s.tone, 'danger');
  const calm = healthSummary(reportOf([result('inotify.watches', 'warn')], true), now);
  assert.equal(calm.label, '1 warning · reboot pending');
  assert.equal(calm.tone, 'pending');
});

test('a stale report is never worse than stale, whatever it says', () => {
  const r = reportOf([result('disk.space', 'error')]);
  assert.equal(healthSummary(r, now + 4 * 60_000).label, 'Health stale');
  assert.equal(healthSummary(r, now + 4 * 60_000).tone, 'neutral');
});

test('a report with nothing counted to read is unavailable, not OK', () => {
  assert.equal(healthSummary(reportOf([]), now).label, 'Checks unavailable');
  const dedicatedOnly = reportOf([result('service.apport', 'warn', 'dedicated')]);
  assert.equal(healthSummary(dedicatedOnly, now).label, 'Checks unavailable');
});

test("findings come first, worst first, in the engine's order within a rank", () => {
  const rows = [
    result('a', 'ok'),
    result('b', 'warn'),
    result('c', 'skip'),
    result('d', 'error'),
    result('e', 'warn'),
  ];
  assert.deepEqual(
    bySeverity(rows).map((r) => r.id),
    ['d', 'b', 'e', 'a', 'c'],
  );
  assert.deepEqual(
    attention(reportOf(rows)).map((r) => r.id),
    ['d', 'b', 'e'],
  );
});
