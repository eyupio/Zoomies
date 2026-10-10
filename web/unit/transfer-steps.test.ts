import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  transferSteps,
  waitLabel,
  waitTone,
  type TransferProgress,
  type TransferWait,
} from '../src/lib/settings/transfer.ts';

function progress(over: Partial<TransferProgress> = {}): TransferProgress {
  return {
    draining: true,
    ready: false,
    live_runners: 0,
    busy_runners: 0,
    pending_cleanup: 0,
    active_jobs: 0,
    machine_operations: 0,
    cleanup_awaiting_host: 0,
    cleanup_awaiting_github: 0,
    cleanup_failed: 0,
    waiting: [],
    summary: 'Preparing.',
    ...over,
  };
}

function wait(over: Partial<TransferWait> = {}): TransferWait {
  return {
    kind: 'cleanup',
    id: 'run_1',
    name: 'pool-a-aaaaaaaa',
    host: 'vm-1',
    host_healthy: true,
    host_confirmed: false,
    registration_confirmed: false,
    detail: 'waiting for vm-1 to confirm it is gone',
    ...over,
  };
}

// The paragraph that read "0 busy runners, 0 runners remaining, 8 awaiting
// cleanup, 11 running jobs" gave the operator five numbers and no order. The
// checklist is the four things the fence waits on, each done or not, so what
// is holding the transfer is the one step that is not ticked.
test('a drained fleet is four steps done, and a count above zero is the one step that is not', () => {
  const done = transferSteps(progress());
  assert.deepEqual(
    done.map((s) => [s.kind, s.done, s.tone]),
    [
      ['job', true, 'idle'],
      ['runner', true, 'idle'],
      ['cleanup', true, 'idle'],
      ['machine', true, 'idle'],
    ],
  );
  const stuck = transferSteps(
    progress({ pending_cleanup: 8, cleanup_awaiting_host: 5, cleanup_awaiting_github: 8 }),
  );
  const cleanup = stuck.find((s) => s.kind === 'cleanup');
  assert.ok(cleanup);
  assert.equal(cleanup.done, false);
  assert.equal(cleanup.count, 8);
  assert.equal(cleanup.tone, 'pending');
  assert.equal(cleanup.note, 'Waiting to be confirmed gone: 5 by the host, 8 by GitHub.');
  assert.ok(stuck.filter((s) => !s.done).length === 1);
});

// A host that has stopped sending heartbeats is the one thing waiting cannot
// fix, so its step is red rather than amber, and so is each row on it.
test('a silent host turns its step and its rows to danger, and says so on the pill', () => {
  const silent = wait({ host: 'vm-silent', host_healthy: false });
  const steps = transferSteps(progress({ pending_cleanup: 1, waiting: [silent] }));
  assert.equal(steps.find((s) => s.kind === 'cleanup')?.tone, 'danger');
  assert.equal(waitTone(silent), 'danger');
  assert.equal(waitLabel(silent), 'Host silent');
  // A healthy host still to confirm is merely pending, and the pill names the
  // side that is outstanding.
  assert.equal(waitTone(wait()), 'pending');
  assert.equal(waitLabel(wait()), 'Host');
  assert.equal(waitLabel(wait({ host_confirmed: true })), 'GitHub');
});

test('a failed cleanup is danger whatever its host is doing, and a busy runner is busy', () => {
  const failed = wait({ error: 'container is in use' });
  assert.equal(waitTone(failed), 'danger');
  assert.equal(waitLabel(failed), 'Failed');
  assert.equal(
    transferSteps(progress({ pending_cleanup: 1, cleanup_failed: 1, waiting: [failed] })).find(
      (s) => s.kind === 'cleanup',
    )?.tone,
    'danger',
  );
  const busy = wait({ kind: 'runner', state: 'busy' });
  assert.equal(waitTone(busy), 'busy');
  assert.equal(waitLabel(busy), 'Busy');
  assert.equal(waitLabel(wait({ kind: 'job' })), 'Running');
});
