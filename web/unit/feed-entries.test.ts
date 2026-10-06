import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Host } from '../src/lib/api/types.ts';
import type { HostChange } from '../src/lib/feed/changes.ts';
import { hostEntry } from '../src/lib/feed/entries.ts';
import { hostStatus } from '../src/lib/status.ts';

const AT = '2026-10-04T10:05:00.000Z';

function host(fields: Partial<Host> = {}): Host {
  return { id: 'hst_1', name: 'builder-1', healthy: true, active_runners: 0, ...fields } as Host;
}

function reporting(warnings: number, errors: number, checkedAt = '2020-01-01T00:00:00Z'): Host {
  return host({
    doctor: {
      checked_at: checkedAt,
      os: 'linux',
      distro: 'ubuntu 24.04',
      container: false,
      reboot_pending: false,
      results: [],
      summary: { counted: 12, warnings, errors, skipped: 0, suggestions: 0 },
    },
  } as Partial<Host>);
}

const OS_LINES: HostChange[] = ['attention', 'failing', 'clear', 'reboot'];

// These are the lines an operator reads on the Overview, so what they say, how
// loudly they say it and where they lead are the contract, not an accident of
// the map they are built from.
test('the four lines about a host’s OS say what changed, in the right tone', () => {
  const lines = Object.fromEntries(
    OS_LINES.map((change) => [change, hostEntry(reporting(2, 0), change, AT)]),
  );
  assert.equal(lines.attention?.title, 'A host needs attention');
  assert.equal(lines.attention?.tone, 'pending');
  assert.equal(lines.failing?.title, 'A host has a health error');
  assert.equal(lines.failing?.tone, 'danger');
  assert.equal(lines.clear?.title, 'A host no longer needs attention');
  assert.equal(lines.clear?.tone, 'idle');
  assert.equal(lines.reboot?.title, 'A host is waiting for a reboot');
  assert.equal(lines.reboot?.tone, 'pending');
  for (const line of Object.values(lines)) {
    assert.equal(line?.category, 'hosts', 'on by default, with the rest of the host lines');
  }
});

test('the detail is the controller’s count in the pill’s own words', () => {
  assert.equal(hostEntry(reporting(2, 0), 'attention', AT)?.detail, '2 warnings');
  assert.equal(hostEntry(reporting(1, 0), 'attention', AT)?.detail, '1 warning');
  assert.equal(hostEntry(reporting(2, 1), 'failing', AT)?.detail, '1 health error · 2 warnings');
  assert.equal(hostEntry(reporting(0, 0), 'clear', AT)?.detail, undefined);
});

// The question the line is read to answer: can it be done now. The count of
// runners is always sent, so zero means nothing is running.
test('a reboot line says whether the host can be rebooted now', () => {
  const idle = hostEntry(host({ active_runners: 0 }), 'reboot', AT);
  assert.equal(idle?.detail, 'Nothing is running on it, so it can be rebooted now.');
  const busy = hostEntry(host({ active_runners: 2 }), 'reboot', AT);
  assert.equal(
    busy?.detail,
    'Runners are still running on it. Cordon it and reboot once they finish; Zoomies never reboots a host itself.',
  );
});

// The report is on the host's own page, and its rows are what the line is
// about; the list of hosts is where every other host line goes.
test('a line about a host’s OS opens the host, and the others still open the list', () => {
  for (const change of OS_LINES) {
    const entry = hostEntry(reporting(1, 0), change, AT);
    assert.deepEqual(entry?.target, { label: 'builder-1', href: '/hosts/hst_1' }, change);
  }
  for (const change of ['joined', 'unreachable', 'cordoned', 'throttled'] as HostChange[]) {
    assert.equal(hostEntry(host(), change, AT)?.target?.href, '/hosts', change);
  }
});

// A healthy host's own icon is a plain circle, which on a line about its
// settings would read as a line about its heartbeat.
test('the OS lines carry marks of their own, apart from the host’s status icon', () => {
  const own = hostStatus(host()).icon;
  const icons = OS_LINES.map((change) => hostEntry(reporting(1, 0), change, AT)?.icon);
  for (const icon of icons) assert.notEqual(icon, own);
  const [attention, failing, , reboot] = icons;
  assert.equal(
    new Set([attention, failing, reboot]).size,
    3,
    'a warning, an error and a reboot differ',
  );
  // The ones that were already there keep theirs.
  assert.equal(hostEntry(host(), 'cordoned', AT)?.icon, hostStatus(host()).icon);
});

// checked_at is the agent's clock. A host whose clock is wrong would date its
// own news a year ago, and a line that moves with a clock is one that flaps.
test('a line is dated by when it arrived, never by the report’s own time', () => {
  const entry = hostEntry(reporting(1, 0, '2020-01-01T00:00:00Z'), 'attention', AT);
  assert.equal(entry?.at, AT);
  assert.equal(entry?.id, `host:hst_1:attention:${AT}`);
});

test('the lines the host already had are as they were', () => {
  const entry = hostEntry(
    host({ throttle_reason: 'load average 19.2' } as Partial<Host>),
    'throttled',
    AT,
  );
  assert.equal(entry?.title, 'A host was throttled');
  assert.equal(entry?.detail, 'load average 19.2');
  assert.equal(hostEntry(host({ id: undefined } as Partial<Host>), 'attention', AT), null);
});
