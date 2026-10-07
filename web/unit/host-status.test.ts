import { test } from 'node:test';
import assert from 'node:assert/strict';
import { healthSummary, type DoctorReport } from '../src/lib/hosts/health.ts';
import { hostStatus } from '../src/lib/status.ts';

// The pill that says a host is sending heartbeats used to read "Healthy", beside
// a separate pill for the host's OS report. Two pills on one card, one saying
// "Healthy" and one saying "2 warnings", read as the page contradicting itself.
// Only the label moved: the key mirrors the API's `host.healthy`, and the tone
// and shape are what operators have learned, so they are pinned here too.
test('a host that is sending heartbeats reads Connected, in the colour and shape it always had', () => {
  const status = hostStatus({ healthy: true });
  assert.equal(status.label, 'Connected');
  assert.equal(status.key, 'healthy');
  assert.equal(status.tone, 'idle');
  assert.equal(status.shape, 'hollow');
});

test('a host that is not sending heartbeats is Unreachable', () => {
  const status = hostStatus({ healthy: false });
  assert.equal(status.label, 'Unreachable');
  assert.equal(status.tone, 'danger');
});

test('a cordoned host reads Cordoned, and a throttled one Throttled', () => {
  const cordoned = hostStatus({ healthy: true, cordoned: true });
  assert.equal(cordoned.label, 'Cordoned');
  assert.equal(cordoned.tone, 'draining');
  const throttled = hostStatus({ healthy: true, throttle: { level: 1 } as never });
  assert.equal(throttled.label, 'Throttled');
  assert.equal(throttled.tone, 'pending');
});

test('what is wrong with a host outranks what is right: unreachable, then cordoned, then throttled, then connected', () => {
  const throttle = { level: 2 } as never;
  assert.equal(hostStatus({ healthy: false, cordoned: true, throttle }).label, 'Unreachable');
  assert.equal(hostStatus({ healthy: true, cordoned: true, throttle }).label, 'Cordoned');
  assert.equal(hostStatus({ healthy: true, throttle }).label, 'Throttled');
  assert.equal(hostStatus({ healthy: true }).label, 'Connected');
  // A host whose reachability is not known yet is not called unreachable.
  assert.equal(hostStatus({}).label, 'Connected');
});

// "Healthy" is the OS report's word now, and "Connected" is the heartbeat's. A
// label that crept back into either set would put the two meanings on one card
// again.
test('no host status label says healthy, and the OS health pill never says Connected', () => {
  const throttle = { level: 1 } as never;
  const labels = [
    hostStatus({ healthy: true }),
    hostStatus({ healthy: false }),
    hostStatus({ healthy: true, cordoned: true }),
    hostStatus({ healthy: true, throttle }),
  ].map((status) => status.label);
  for (const label of labels) assert.doesNotMatch(label, /healthy/i);

  const now = Date.parse('2026-10-04T10:00:00Z');
  const fine: DoctorReport = {
    checked_at: new Date(now).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container: false,
    reboot_pending: false,
    results: [],
    summary: { counted: 1, warnings: 0, errors: 0, skipped: 0, suggestions: 0 },
  };
  for (const report of [undefined, fine, { ...fine, reboot_pending: true }]) {
    for (const reachable of [true, false]) {
      assert.notEqual(healthSummary(report, now, reachable).label, 'Connected');
    }
  }
});
