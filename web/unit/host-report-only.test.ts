import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { DoctorReport, DoctorResult } from '../src/lib/hosts/health.ts';
import {
  REPORT_ONLY_SAFE_DESCRIPTION,
  reportOnly,
  reportOnlySentence,
  reportOnlySubtitle,
  reportOrigin,
} from '../src/lib/hosts/report-only.ts';
import { nextStep } from '../src/lib/hosts/next-step.ts';

function row(id: string, status: DoctorResult['status']): DoctorResult {
  return {
    id,
    title: id,
    tier: 'safe',
    status,
    current: '',
    recommended: '',
    rationale: '',
    actionable: false,
  };
}

function report(results: DoctorResult[], over: Partial<DoctorReport> = {}): DoctorReport {
  return {
    checked_at: '2026-10-04T10:00:00Z',
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: false,
    results,
    ...over,
  };
}

const host = { id: 'host_1', name: 'builder-1', healthy: true, active_runners: 0 };

test('a host that reports no environment row is treated as tunable', () => {
  // An agent that predates the row must keep today's behaviour: telling a real
  // Linux host it is report-only would hide the one command that helps it.
  assert.equal(reportOnly(report([row('disk.space', 'ok')])), null);
  assert.equal(reportOnly(undefined), null);
});

test('the environment row, not the operating system name, decides what is report-only', () => {
  assert.equal(reportOnly(report([row('environment', 'skip')], { os: 'darwin' })), 'os');
  assert.equal(reportOnly(report([row('environment', 'warn')])), 'distro');
  // A container wins: its image's distribution warning is not something to act on either.
  assert.equal(reportOnly(report([row('environment', 'warn')], { container: true })), 'container');
});

test('the report-only sentences never offer to change the host and name the right place to look', () => {
  assert.match(reportOnlySentence('os', 'darwin'), /does not tune darwin hosts/);
  assert.match(reportOnlySentence('os', ''), /does not tune this kind of host/);
  assert.match(reportOnlySentence('distro', 'linux'), /zoomies tune will not change it/);
  assert.match(reportOnlySentence('container', 'linux'), /not inside the container/);
  assert.equal(reportOnlySubtitle('distro'), null);
  assert.match(reportOnlySubtitle('os') ?? '', /Linux hosts only/);
  assert.match(REPORT_ONLY_SAFE_DESCRIPTION, /does not change this host/);
});

test('a blank distribution prints no stray separators', () => {
  // A Mac reports no distribution, and "darwin ·  · Checked" read as a bug.
  assert.equal(reportOrigin({ os: 'darwin', distro: '' }), 'darwin');
  assert.equal(reportOrigin({ os: 'darwin', distro: '  ' }), 'darwin');
  assert.equal(reportOrigin({ os: 'linux', distro: 'ubuntu 24.04' }), 'linux · ubuntu 24.04');
});

test('an unsupported distribution gives no next step on its own warning, and no command when cordoned', () => {
  // The environment warning is counted by the controller, but nobody can clear
  // it, so it must not put a panel with a doctor command on every such host.
  const distro = report([row('environment', 'warn')]);
  assert.equal(nextStep({ host, report: distro, cordoned: false }), null);

  const cordoned = nextStep({ host, report: distro, cordoned: true });
  assert.ok(cordoned);
  assert.equal(cordoned.where, null);
});

test('an unsupported distribution with a real finding or a reboot still gets cordon advice but no command', () => {
  const finding = report([row('environment', 'warn'), row('disk.space', 'warn')]);
  const step = nextStep({ host, report: finding, cordoned: false });
  assert.ok(step, 'a real finding still deserves the panel');
  assert.equal(step.where, null);
  assert.equal(step.button.label, 'Cordon this host');

  const reboot = report([row('environment', 'warn')], { reboot_pending: true });
  const rebooting = nextStep({ host, report: reboot, cordoned: false });
  assert.ok(rebooting);
  assert.equal(rebooting.where, null);
  assert.match(rebooting.detail, /Reboot|reboot/);
});

test('a tunable Linux host keeps its command', () => {
  const step = nextStep({ host, report: report([row('disk.space', 'warn')]), cordoned: false });
  assert.ok(step);
  assert.ok(step.where);
});
