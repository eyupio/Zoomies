import { test } from 'node:test';
import assert from 'node:assert/strict';
import { judgeFirstJob } from '../src/lib/overview/firstJob.ts';

const ran = {
  state: 'completed' as const,
  runner_id: 'run_1',
  runner_name: 'zoomies-linux-x64-1',
  conclusion: 'success',
  duration_ms: 14_000,
};

// The checklist hides the moment a job is *queued*, so these events arrive
// after it has gone. Congratulating a job that has not run yet would be the
// claim the whole feature exists to avoid.
test('a job that is still queued or running is not the moment yet', () => {
  for (const state of ['waiting', 'queued', 'in_progress'] as const) {
    assert.deepEqual(judgeFirstJob({ ...ran, state }), { kind: 'waiting' });
  }
});

// A job that finished somewhere else says nothing about this fleet: GitHub
// reports every job it hosts, and a hosted runner's success is not the
// operator's.
test('a finished job no runner of ours took is not the moment either', () => {
  assert.deepEqual(judgeFirstJob({ ...ran, runner_id: '' }), { kind: 'waiting' });
  assert.deepEqual(judgeFirstJob({ ...ran, runner_id: undefined }), { kind: 'waiting' });
});

test('a first job that succeeded is said once, with the runner and how long it took', () => {
  const result = judgeFirstJob(ran);
  assert.equal(result.kind, 'finished');
  assert.ok(result.kind === 'finished' && result.toast);
  assert.equal(result.toast.title, 'Your first job ran on your own runner');
  assert.match(result.toast.message, /zoomies-linux-x64-1 finished it in 14s\./);
});

// The watch ends on the first job that ran, whatever happened to it: a second
// job succeeding an hour later is not "your first job", and cheering it would
// be wrong. And a failure is not cheered at all.
test('a first job that failed ends the watch without a toast', () => {
  for (const conclusion of ['failure', 'cancelled', 'skipped', '']) {
    assert.deepEqual(judgeFirstJob({ ...ran, conclusion }), { kind: 'finished', toast: null });
  }
});

test('a job with no recorded duration or runner name is still described truthfully', () => {
  const result = judgeFirstJob({ ...ran, duration_ms: undefined, runner_name: '' });
  assert.ok(result.kind === 'finished' && result.toast);
  assert.match(result.toast.message, /^A runner finished it\. /);
});
