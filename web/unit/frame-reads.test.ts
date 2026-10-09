import { test } from 'node:test';
import assert from 'node:assert/strict';
import { FrameReads } from '../src/lib/state/frame-reads.ts';

/** A read the test answers by hand, so two can be in flight at once. */
function deferredReads() {
  const pending: ((value: string) => void)[] = [];
  const get = () => new Promise<string>((resolve) => pending.push(resolve));
  return { pending, get };
}

// The overlap the Hosts page met: a read is in flight, a frame lands, the
// rollout button's answer is overtaken, and the page asks again. Joining the
// read already in flight would end on nothing, because that read began before
// the frame and its answer is thrown away; the page would keep the button's
// older picture until something else moved.
test('a fresh read after a frame is not the read that began before it', async () => {
  const { pending, get } = deferredReads();
  const taken: string[] = [];
  const reads = new FrameReads<string>({
    get,
    answer: (value) => taken.push(value),
    failed: () => assert.fail('no read fails here'),
  });

  const first = reads.refresh();
  reads.took();
  const second = reads.fresh();
  assert.equal(pending.length, 2, 'the fresh read is a request of its own');

  pending[0]('older than the frame');
  await first;
  pending[1]('newer than the frame');
  await second;
  assert.deepEqual(taken, ['newer than the frame']);
});

// Several pages asking at once is one request, which is what refresh is for.
test('a refresh while a read is in flight joins it', async () => {
  const { pending, get } = deferredReads();
  const taken: string[] = [];
  const reads = new FrameReads<string>({ get, answer: (v) => taken.push(v), failed: () => {} });

  const one = reads.refresh();
  const two = reads.refresh();
  assert.equal(pending.length, 1);
  pending[0]('the answer');
  await Promise.all([one, two]);
  assert.deepEqual(taken, ['the answer']);
});

// A read that began before a frame says nothing the frame did not say better,
// and its failure is not an error the page has.
test('a read overtaken by a frame neither answers nor fails', async () => {
  let reject: (cause: unknown) => void = () => {};
  const failures: unknown[] = [];
  const reads = new FrameReads<string>({
    get: () => new Promise<string>((_, no) => (reject = no)),
    answer: () => assert.fail('an overtaken read must not answer'),
    failed: (cause) => failures.push(cause),
  });
  const read = reads.refresh();
  reads.took();
  reject(new Error('the network went away'));
  await read;
  assert.deepEqual(failures, []);
});

// The page's spinner is the reads in flight, so it stays on until the last
// one, fresh or joined, has answered.
test('loading lasts until every read has answered', async () => {
  const { pending, get } = deferredReads();
  const reads = new FrameReads<string>({ get, answer: () => {}, failed: () => {} });
  const first = reads.refresh();
  reads.took();
  const second = reads.fresh();
  pending[0]('a');
  await first;
  assert.equal(reads.loading, true);
  pending[1]('b');
  await second;
  assert.equal(reads.loading, false);
});
