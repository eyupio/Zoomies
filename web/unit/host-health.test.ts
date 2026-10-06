import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  attention,
  bySeverity,
  findingsLabel,
  healthSummary,
  type DoctorReport,
  type DoctorResult,
} from '../src/lib/hosts/health.ts';
const now = Date.parse('2026-10-04T10:00:00Z');

/**
 * The controller's count of a report, written out as a stand-in for the real
 * one (hosttune.Report.Summary in Go). The pill reads `summary` and never the
 * results, so a fixture without one would test nothing; the rule is repeated
 * here only so a fixture's numbers are the ones a controller would send. That
 * the stand-in and the controller agree is not proved here but by the
 * host-health Playwright spec, which sends results to the real binary.
 */
function summaryOf(results: DoctorResult[], reboot: boolean): DoctorReport['summary'] {
  const out = { counted: 0, warnings: 0, errors: 0, skipped: 0, suggestions: 0 };
  for (const r of results) {
    // A pending reboot is counted once, as the reboot.
    if (reboot && r.id === 'kernel.pending' && r.status === 'warn') continue;
    if (!(r.tier === 'safe' && r.optional !== true)) {
      if (r.status === 'warn') out.suggestions++;
      continue;
    }
    out.counted++;
    if (r.status === 'warn') out.warnings++;
    if (r.status === 'error') out.errors++;
    if (r.status === 'skip') out.skipped++;
  }
  return out;
}

function report(status: 'ok' | 'warn' | 'error' | 'skip'): DoctorReport {
  const results: DoctorResult[] = [
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
  ];
  return {
    checked_at: new Date(now).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: false,
    results,
    summary: summaryOf(results, false),
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
    summary: summaryOf(results, reboot),
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

// The problems, the metrics, the feed and `zoomies hosts list` all read the
// controller's count. A pill that tallied the rows itself would be the one
// surface that could disagree with them, and the day the two rules drift it is
// the badge that gets believed.
test("the pill says the controller's count, not its own tally of the rows", () => {
  const r = reportOf([
    result('inotify.watches', 'warn'),
    result('docker.logs', 'warn'),
    result('disk.space', 'warn'),
  ]);
  r.summary = { counted: 3, warnings: 1, errors: 0, skipped: 0, suggestions: 0 };
  const s = healthSummary(r, now);
  assert.equal(s.label, '1 warning');
  assert.equal(s.warnings, 1);
  assert.match(s.hint, /1 warning, 0 errors, 0 skipped checks/);
});

// A payload with no summary is a controller older than this page, and the
// badge must not throw on every host card because of it. It also must not make
// up an answer: counting the rows would be a second opinion.
test('a report without the controller’s count reads as unavailable and never throws', () => {
  const r = reportOf([result('disk.space', 'error')]);
  delete (r as Partial<DoctorReport>).summary;
  const s = healthSummary(r, now);
  assert.equal(s.label, 'Health unavailable');
  assert.equal(s.tone, 'neutral');
  assert.match(s.hint, /without the controller’s count/);
});

// The agent sets reboot_pending from the kernel.pending warning, so a report
// says the one fact twice. The controller counts it once, as the reboot: a
// host whose only finding is a restart is waiting for one, not failing a check,
// and the Needs attention list must not show a row the count left out.
test('a pending reboot is counted once, as the reboot', () => {
  const only = reportOf([result('inotify.watches', 'ok'), result('kernel.pending', 'warn')], true);
  const s = healthSummary(only, now);
  assert.equal(s.label, 'Reboot pending');
  assert.equal(s.tone, 'pending');
  assert.equal(s.warnings, 0);
  assert.deepEqual(attention(only), []);

  // Beside a real finding, the reboot is said once and the finding once.
  const both = reportOf(
    [result('inotify.watches', 'warn'), result('kernel.pending', 'warn')],
    true,
  );
  assert.equal(healthSummary(both, now).label, '1 warning · reboot pending');
  assert.deepEqual(
    attention(both).map((r) => r.id),
    ['inotify.watches'],
  );
});

test('the list of what needs attention is as long as the count says', () => {
  const cases: DoctorReport[] = [
    reportOf([result('a', 'warn'), result('b', 'error'), result('c', 'ok')]),
    reportOf([result('a', 'warn'), result('kernel.pending', 'warn'), result('b', 'error')], true),
    reportOf([result('kernel.pending', 'warn'), result('t', 'warn', 'aggressive')], true),
    reportOf([result('a', 'warn', 'safe', true), result('s', 'skip')]),
  ];
  for (const r of cases) {
    const s = r.summary;
    assert.equal(attention(r).length, s.warnings + s.errors);
  }
});

// Only a warning is the reboot. A kernel.pending that could not run, or a
// report that contradicts itself, is judged by its rows rather than hidden.
test('only the kernel.pending warning of a report that says reboot is left out', () => {
  const noFlag = reportOf([result('kernel.pending', 'warn')], false);
  assert.deepEqual(
    attention(noFlag).map((r) => r.id),
    ['kernel.pending'],
  );
  const errored = reportOf([result('kernel.pending', 'error')], true);
  assert.deepEqual(
    attention(errored).map((r) => r.id),
    ['kernel.pending'],
  );
});

// A container's report is the container's view: most checks skipped, and its
// image's distribution warns for ever. The controller raises nothing for it,
// so a pill that painted it red or amber would be a verdict nobody else gives.
test('a container’s partial report is Partial report, whatever it found', () => {
  const r = reportOf(
    [result('environment', 'warn'), result('disk.space', 'error'), result('kernel.pending', 'ok')],
    true,
  );
  r.container = true;
  const s = healthSummary(r, now);
  assert.equal(s.label, 'Partial report');
  assert.equal(s.tone, 'neutral');
  assert.match(s.hint, /Partial report from the container/);
  assert.doesNotMatch(s.hint, /warning|error/);
  // Nor does a stale one carry its reboot flag: that is no more a verdict than
  // its rows are, and "Reboot pending · stale" would say it was.
  assert.equal(healthSummary(r, now + 4 * 60_000).label, 'Health stale');
});

test('findings are said errors first, in the words the pill and the feed share', () => {
  assert.equal(findingsLabel({ errors: 0, warnings: 0 }), '');
  assert.equal(findingsLabel({ errors: 0, warnings: 1 }), '1 warning');
  assert.equal(findingsLabel({ errors: 0, warnings: 2 }), '2 warnings');
  assert.equal(findingsLabel({ errors: 1, warnings: 0 }), '1 health error');
  assert.equal(findingsLabel({ errors: 1, warnings: 2 }), '1 health error · 2 warnings');
  assert.equal(findingsLabel({ errors: 3, warnings: 1 }), '3 health errors · 1 warning');
});
