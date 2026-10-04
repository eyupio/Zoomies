import { test } from 'node:test';
import assert from 'node:assert/strict';
import { healthSummary, type DoctorReport } from '../src/lib/hosts/health.ts';
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
