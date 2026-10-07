import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { DoctorResult } from '../src/lib/hosts/health.ts';
import { KIND_WORDS, previewCommand, rowKind } from '../src/lib/hosts/row-kind.ts';

function row(over: Partial<DoctorResult> = {}): DoctorResult {
  return {
    id: 'inotify.watches',
    title: 'inotify watches',
    tier: 'safe',
    status: 'warn',
    current: '',
    recommended: '',
    rationale: '',
    actionable: false,
    ...over,
  };
}

test('a warning the report marks actionable reads Fixable', () => {
  assert.equal(rowKind(row({ actionable: true }), false), 'fixable');
  assert.equal(KIND_WORDS.fixable, 'Fixable');
});

test('a warning that is not actionable reads Advice', () => {
  assert.equal(rowKind(row(), false), 'advice');
});

test('optional wins over actionable, because a trade-off is read before it is fixed', () => {
  assert.equal(rowKind(row({ optional: true, actionable: true }), false), 'optional');
});

test('only a true actionable counts, so an old agent that omits it reads Advice', () => {
  // The safe way to be wrong: never offer a command the agent did not vouch for.
  const old = row() as Partial<DoctorResult>;
  delete old.actionable;
  assert.equal(rowKind(old as DoctorResult, false), 'advice');
  const odd = row({ actionable: 'yes' as unknown as boolean });
  assert.equal(rowKind(odd, false), 'advice');
});

test('a report-only host and a row that is not a warning have no kind', () => {
  assert.equal(rowKind(row({ actionable: true }), true), null);
  for (const status of ['ok', 'skip', 'error'] as const) {
    assert.equal(rowKind(row({ status, actionable: true }), false), null);
  }
});

test('each tier previews with the flags tune demands, in the documented order', () => {
  assert.equal(previewCommand(row()), 'sudo zoomies tune --only inotify.watches --dry-run');
  assert.equal(
    previewCommand(row({ id: 'cpu.governor', tier: 'aggressive' })),
    'sudo zoomies tune --tier aggressive --only cpu.governor --dry-run',
  );
  assert.equal(
    previewCommand(row({ id: 'journal.size', tier: 'dedicated' })),
    'sudo zoomies tune --dedicated --only journal.size --dry-run',
  );
});

test('a mixed-case unit name such as service.ModemManager is accepted', () => {
  // Rejecting it would drop a real row's button without saying why.
  assert.equal(
    previewCommand(row({ id: 'service.ModemManager' })),
    'sudo zoomies tune --only service.ModemManager --dry-run',
  );
});

test('a hostile or malformed id gives no command at all', () => {
  const bad = [
    'inotify watches',
    "inotify.watches'",
    'inotify.watches; rm -rf /',
    'inotify.`id`',
    'inotify.$HOME',
    'inotify.watches\nsudo reboot',
    'Inotify.watches',
    'a.' + 'b'.repeat(63),
    '',
    'environment',
  ];
  for (const id of bad) assert.equal(previewCommand(row({ id })), null, JSON.stringify(id));
});

test('an id of exactly 64 characters passes and 65 does not', () => {
  const ok = 'a.' + 'b'.repeat(62);
  assert.equal(ok.length, 64);
  assert.notEqual(previewCommand(row({ id: ok })), null);
  assert.equal(previewCommand(row({ id: ok + 'b' })), null);
});

test('an unknown or missing tier gives no command', () => {
  assert.equal(previewCommand(row({ tier: 'root' as unknown as DoctorResult['tier'] })), null);
  assert.equal(previewCommand(row({ tier: undefined as unknown as DoctorResult['tier'] })), null);
  assert.equal(previewCommand(row({ tier: '__proto__' as unknown as DoctorResult['tier'] })), null);
});

test('the preview never carries a flag that changes the host', () => {
  const cmd = previewCommand(row({ tier: 'aggressive' })) ?? '';
  for (const flag of ['--yes', '--revert', '--background', '--force']) {
    assert.ok(!cmd.includes(flag), flag);
  }
});
