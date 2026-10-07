import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Host } from '../src/lib/api/types.ts';
import { healthSummary, type DoctorReport, type DoctorResult } from '../src/lib/hosts/health.ts';
import {
  HEALTH_FILTERS,
  attentionDetail,
  emptyCopy,
  filterSentence,
  healthBucket,
  healthCounts,
  healthFilterFrom,
  hostsFor,
  type HealthCounts,
} from '../src/lib/hosts/health-filter.ts';

const now = Date.parse('2026-10-04T10:00:00Z');
const MINUTE = 60_000;

function result(id: string, status: DoctorResult['status']): DoctorResult {
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

/**
 * The controller's count of a report, written out as a stand-in for the real one
 * (hosttune.Report.Summary), because the bucket reads `summary` through the pill
 * and a fixture without one would be "No report" every time. That this agrees
 * with a real controller is the host-health Playwright spec's to prove.
 */
function summaryOf(results: DoctorResult[], reboot: boolean): DoctorReport['summary'] {
  const out = { counted: 0, warnings: 0, errors: 0, skipped: 0, suggestions: 0 };
  for (const r of results) {
    if (reboot && r.id === 'kernel.pending' && r.status === 'warn') continue;
    out.counted++;
    if (r.status === 'warn') out.warnings++;
    if (r.status === 'error') out.errors++;
    if (r.status === 'skip') out.skipped++;
  }
  return out;
}

function report(
  results: DoctorResult[],
  { reboot = false, container = false, age = 0 } = {},
): DoctorReport {
  return {
    checked_at: new Date(now - age).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container,
    reboot_pending: reboot,
    results,
    summary: summaryOf(results, reboot),
  };
}

function host(doctor: DoctorReport | undefined, healthy: boolean | undefined = true) {
  return { doctor, healthy } satisfies Pick<Host, 'doctor' | 'healthy'>;
}

const ok = [result('disk.space', 'ok')];

test('a host that has sent no OS report is in No report', () => {
  assert.equal(healthBucket(host(undefined), now), 'no-report');
});

// The pill calls a report that arrived without the controller's count "Health
// unavailable", and healthSummary also calls a missing report stale. If the
// stale test ran first, both would land in Report stale and the chip called No
// report would be empty for the one host it exists to find.
test('a report that arrived without the controller’s count is No report, not stale', () => {
  const r = report(ok);
  delete (r as Partial<DoctorReport>).summary;
  assert.equal(healthBucket(host(r), now), 'no-report');
});

test('a fresh report with an error, a warning or only a pending reboot needs attention', () => {
  assert.equal(healthBucket(host(report([result('disk.space', 'error')])), now), 'attention');
  assert.equal(healthBucket(host(report([result('disk.space', 'warn')])), now), 'attention');
  assert.equal(healthBucket(host(report(ok, { reboot: true })), now), 'attention');
});

test('a fresh report with nothing to do, or nothing it can say, is in no bucket at all', () => {
  assert.equal(healthBucket(host(report(ok)), now), null);
  // Every check skipped is "Checks unavailable", which is not a finding.
  assert.equal(healthBucket(host(report([result('disk.space', 'skip')])), now), null);
  assert.equal(healthBucket(host(report([])), now), null);
});

// The pill ignores a container's findings and its reboot flag, because the
// controller raises nothing for them. A filter that counted them would put a
// host in "Need attention" that nothing else on the page, or in the problems
// drawer, says needs it.
test('a container’s partial report is never counted, whatever it found', () => {
  const partial = report([result('environment', 'warn'), result('disk.space', 'error')], {
    reboot: true,
    container: true,
  });
  assert.equal(healthBucket(host(partial), now), null);
});

// Mirrors the strict `>` in healthSummary. If the threshold moves, this fails,
// and so do the "three minutes" strings in the chip titles and the empty state,
// which is the point: they are written for this number.
test('a report is stale only once it is older than three minutes', () => {
  const errored = [result('disk.space', 'error')];
  assert.equal(healthBucket(host(report(errored, { age: 3 * MINUTE })), now), 'attention');
  assert.equal(healthBucket(host(report(errored, { age: 3 * MINUTE + 1 })), now), 'stale');
});

test('a host whose agent is not connected is stale, even with a fresh report', () => {
  const fresh = report([result('disk.space', 'error')]);
  assert.equal(healthBucket(host(fresh, false), now), 'stale');
  // An unknown reachability is the pill's default, which is reachable.
  assert.equal(healthBucket(host(fresh, undefined), now), 'attention');
});

// Its pill is the neutral "Reboot pending · stale", not the amber one, so the
// chip that says "Need attention" must not hold it.
test('a stale host that is also waiting for a reboot is stale, not attention', () => {
  const waiting = report(ok, { reboot: true, age: 4 * MINUTE });
  assert.equal(healthSummary(waiting, now).label, 'Reboot pending · stale');
  assert.equal(healthBucket(host(waiting), now), 'stale');
});

// The tile, the chip and the card's pill must agree, and the only way to be sure
// they always do is to ask the pill. This walks every shape of report the page
// can meet and checks the bucket against the pill's own words.
test('a host is in Need attention exactly when its pill is amber or red and current', () => {
  const shapes: Array<[string, Pick<Host, 'doctor' | 'healthy'>]> = [
    ['no report', host(undefined)],
    ['clean', host(report(ok))],
    ['warning', host(report([result('a', 'warn')]))],
    ['error', host(report([result('a', 'error')]))],
    ['reboot only', host(report(ok, { reboot: true }))],
    ['error and reboot', host(report([result('a', 'error')], { reboot: true }))],
    ['skipped', host(report([result('a', 'skip')]))],
    ['empty', host(report([]))],
    ['container', host(report([result('a', 'warn')], { reboot: true, container: true }))],
    ['old error', host(report([result('a', 'error')], { age: 10 * MINUTE }))],
    ['old reboot', host(report(ok, { reboot: true, age: 10 * MINUTE }))],
    ['unreachable error', host(report([result('a', 'error')]), false)],
    ['unreachable clean', host(report(ok), false)],
  ];
  for (const [name, h] of shapes) {
    const pill = h.doctor?.summary ? healthSummary(h.doctor, now, h.healthy) : undefined;
    const expected = pill !== undefined && !pill.stale && ['danger', 'pending'].includes(pill.tone);
    assert.equal(healthBucket(h, now) === 'attention', expected, name);
  }
});

// The shortfall is the point: a clean host is in none of the three, and the
// page says so rather than letting the chips look as though they sum to All.
test('the buckets are exclusive and do not add up to All', () => {
  const hosts = [
    host(undefined),
    host(report(ok)),
    host(report([result('a', 'warn')])),
    host(report(ok, { reboot: true })),
    host(report(ok, { age: 5 * MINUTE })),
    host(report(ok), false),
    host(report([], { container: true })),
  ];
  const counts = healthCounts(hosts, now);
  assert.equal(counts.all, hosts.length);
  assert.deepEqual(
    [counts.attention, counts.stale, counts['no-report']],
    [2, 2, 1],
    'each host is counted once, in one bucket',
  );
  assert.ok(counts.attention + counts.stale + counts['no-report'] < counts.all);
});

test('filtering by All hands back the same array, and a bucket keeps only its hosts', () => {
  const warned = { ...host(report([result('a', 'warn')])), id: 'warned' };
  const clean = { ...host(report(ok)), id: 'clean' };
  const silent = { ...host(undefined), id: 'silent' };
  const hosts = [warned, clean, silent];
  assert.equal(hostsFor(hosts, 'all', now), hosts);
  assert.deepEqual(
    hostsFor(hosts, 'attention', now).map((h) => h.id),
    ['warned'],
  );
  assert.deepEqual(
    hostsFor(hosts, 'no-report', now).map((h) => h.id),
    ['silent'],
  );
  assert.deepEqual(hostsFor(hosts, 'stale', now), []);
});

// An unknown value in the address is what somebody typed or an old bookmark
// carried. Showing every host is what no filter means; an empty page would look
// like good news about a fleet nobody had checked.
test('an address that names no filter shows every host', () => {
  for (const raw of ['bogus', '', 'Attention', 'ATTENTION', 'none', 'all ']) {
    assert.equal(healthFilterFrom(raw), 'all', JSON.stringify(raw));
  }
  for (const option of HEALTH_FILTERS) assert.equal(healthFilterFrom(option.value), option.value);
});

test('the chips are named as the page promises, in order', () => {
  assert.deepEqual(
    HEALTH_FILTERS.map((option) => option.label),
    ['All', 'Need attention', 'Report stale', 'No report'],
  );
});

test('the sentence under the chips names the filter, and is empty for All', () => {
  assert.equal(filterSentence('all', 3, 3), '');
  assert.equal(
    filterSentence('attention', 2, 5),
    'Showing 2 of 5 hosts: OS settings below the recommendation, or a reboot pending.',
  );
  assert.equal(
    filterSentence('no-report', 0, 1),
    'Showing 0 of 1 host: no OS report has arrived from the agent yet.',
  );
});

function counted(over: Partial<HealthCounts>): HealthCounts {
  return { all: 3, attention: 0, stale: 0, 'no-report': 0, ...over };
}

// Zero beside hosts that have said nothing must not read as a clean bill of
// health, which is what a bare "0" on an amber tile would be.
test('the tile says how many hosts are silent only when none needs attention', () => {
  assert.equal(
    attentionDetail(counted({ stale: 1, 'no-report': 2 })),
    '3 without a current OS report',
  );
  assert.equal(
    attentionDetail(counted({ attention: 1, stale: 1 })),
    'OS settings below the recommendation, or a reboot pending',
  );
  assert.equal(
    attentionDetail(counted({})),
    'OS settings below the recommendation, or a reboot pending',
  );
});

test('an empty Need attention view mentions silent hosts only when there are some', () => {
  const quiet = emptyCopy('attention', counted({}));
  assert.equal(quiet.title, 'No host needs attention');
  assert.doesNotMatch(quiet.description, /no current report/);

  const one = emptyCopy('attention', counted({ 'no-report': 1 }));
  assert.match(one.description, /1 host has no current report, so nothing is known about it yet\./);

  const two = emptyCopy('attention', counted({ stale: 1, 'no-report': 1 }));
  assert.match(
    two.description,
    /2 hosts have no current report, so nothing is known about them yet\./,
  );
});

test('an empty Report stale view says how many have never reported, and an empty No report view says why it matters', () => {
  assert.equal(emptyCopy('stale', counted({})).title, 'No stale reports');
  assert.doesNotMatch(emptyCopy('stale', counted({})).description, /not reported at all/);
  assert.match(
    emptyCopy('stale', counted({ 'no-report': 1 })).description,
    /1 host has not reported at all\./,
  );
  assert.match(
    emptyCopy('stale', counted({ 'no-report': 3 })).description,
    /3 hosts have not reported at all\./,
  );
  const none = emptyCopy('no-report', counted({}));
  assert.equal(none.title, 'Every host has reported');
  assert.match(none.description, /health service/);
});
