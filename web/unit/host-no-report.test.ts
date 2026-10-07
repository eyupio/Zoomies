import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  FIRST_REPORT_GRACE_MS,
  READ_ONLY_DOCTOR_COMMAND,
  noReport,
  noReportSubtitle,
  type NoReport,
} from '../src/lib/hosts/no-report.ts';

const now = Date.parse('2026-10-04T10:00:00Z');
const longAgo = new Date(now - 3 * 3_600_000).toISOString();
const UPGRADE = 'sudo zoomies upgrade --mode agent --version v1.2.0';

function host(over: Partial<Parameters<typeof noReport>[0]['host']> = {}) {
  return {
    healthy: true,
    version: 'v1.2.0',
    created_at: longAgo,
    last_heartbeat: new Date(now - 5_000).toISOString(),
    ...over,
  };
}
const run = (h = host(), canOperate = true): NoReport => noReport({ host: h, now, canOperate });

test('a host that is not connected is told so before anything about its release', () => {
  // Version skew and a fresh row are both true of a host that has dropped off,
  // and neither is the reason nothing can arrive.
  const r = run(host({ healthy: false, version_skew: 'behind', upgrade_command: UPGRADE }));
  assert.equal(r.kind, 'disconnected');
  assert.equal(r.command, null);
  assert.equal(r.tail, 'heartbeat');
  assert.equal(run(host({ healthy: false, last_heartbeat: undefined })).tail, null);
});

test('a behind or different agent may be too old, and an operator gets the controller-made command byte for byte', () => {
  for (const skew of ['behind', 'differs'] as const) {
    const r = run(
      host({ version_skew: skew, upgrade_command: UPGRADE, upgrade_note: 'Updates it.' }),
    );
    assert.equal(r.kind, 'release');
    assert.match(r.description, /may be too old to send OS reports/);
    assert.equal(r.command, UPGRADE);
    assert.equal(r.copyLabel, 'Copy the upgrade command');
    assert.equal(r.note, 'Updates it.');
  }
  // Ahead is the card's business ("upgrade the controller first"), not a cause here.
  assert.notEqual(run(host({ version_skew: 'ahead' })).kind, 'release');
});

test('the upgrade command is never built from a host field and never reaches a viewer', () => {
  const evil = host({
    version: 'v1; rm -rf /',
    version_skew: 'behind',
    upgrade_command: UPGRADE,
  });
  assert.equal(run(evil).command, UPGRADE);
  const viewer = run(evil, false);
  assert.equal(viewer.kind, 'release');
  assert.equal(viewer.command, null);
  assert.match(viewer.detail, /needs the operator role/);
  // No command to offer: say so rather than invent one.
  const none = run(host({ version_skew: 'differs' }));
  assert.equal(none.command, null);
  assert.match(none.detail, /no command to offer/);
  // The embedded agent is the controller; updating it cannot help.
  assert.equal(
    run(host({ version_skew: 'behind', embedded: true, upgrade_command: UPGRADE })).command,
    null,
  );
});

test('skew outranks the grace period, because an old agent never reports however new its row is', () => {
  const r = run(
    host({ version_skew: 'differs', created_at: new Date(now - 60_000).toISOString() }),
  );
  assert.equal(r.kind, 'release');
  assert.equal(r.tail, 'joined-late');
});

test('a host that joined within the grace period is waiting, not faulty', () => {
  const young = new Date(now - FIRST_REPORT_GRACE_MS + 1_000).toISOString();
  const r = run(host({ created_at: young }));
  assert.equal(r.kind, 'joining');
  assert.equal(r.tail, 'joined');
  assert.equal(r.command, null);
  const old = new Date(now - FIRST_REPORT_GRACE_MS).toISOString();
  assert.notEqual(run(host({ created_at: old })).kind, 'joining');
  // An unreadable date must not read as "just joined": that would hide a fault.
  assert.notEqual(run(host({ created_at: 'not a date' })).kind, 'joining');
  assert.notEqual(run(host({ created_at: undefined })).kind, 'joining');
});

test('the embedded agent is not told to update itself', () => {
  const r = run(host({ embedded: true }));
  assert.equal(r.kind, 'embedded');
  assert.equal(r.command, null);
  assert.match(r.detail, /updating it cannot help/);
});

test('an agent that gives no release gets no command, because Zoomies would not know which to offer', () => {
  const op = run(host({ version: '' }));
  assert.equal(op.kind, 'no-release');
  assert.equal(op.command, null);
  assert.doesNotMatch(op.detail, /Ask an operator/);
  assert.match(run(host({ version: '' }), false).detail, /Ask an operator to update it/);
});

test('when nothing explains it, both roles get the read-only doctor command and nothing else', () => {
  for (const canOperate of [true, false]) {
    const r = run(host(), canOperate);
    assert.equal(r.kind, 'unknown');
    assert.equal(r.command, READ_ONLY_DOCTOR_COMMAND);
    assert.equal(r.command, 'sudo zoomies doctor');
    assert.doesNotMatch(r.command ?? '', /interactive|tune/);
    assert.equal(r.copyLabel, 'Copy command');
  }
  // Even a host whose fields look hostile never changes the constant.
  assert.equal(run(host({ version: '$(reboot)' })).command, READ_ONLY_DOCTOR_COMMAND);
});

test('no cause offers tuning, draining or an American spelling, or implies the controller runs anything', () => {
  const cases = [
    host({ healthy: false }),
    host({ version_skew: 'behind', upgrade_command: UPGRADE, upgrade_note: 'n' }),
    host({ version_skew: 'differs' }),
    host({ created_at: new Date(now - 1000).toISOString() }),
    host({ embedded: true }),
    host({ version: '' }),
    host(),
  ];
  for (const h of cases) {
    for (const canOperate of [true, false]) {
      const r = run(h, canOperate);
      const text = [r.description, r.detail, r.after, r.commandCaption].join(' ');
      assert.doesNotMatch(text, /No tuning|tune|apply|drain|behavior|utiliz|authoriz/i);
      assert.doesNotMatch(text, /Zoomies (will )?(run|runs) it/);
    }
  }
  assert.equal(noReportSubtitle(), 'No OS report has arrived from this host yet.');
});
