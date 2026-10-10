import { test } from 'node:test';
import assert from 'node:assert/strict';
import { FrameReads } from '../src/lib/state/frame-reads.ts';
import { updateActions } from '../src/lib/state/update-actions.ts';

/** Calls and reads the test answers by hand, so an answer and a frame can overlap. */
function harness() {
  const reads: ((value: string) => void)[] = [];
  const asked: { resolve: (value: string) => void; reject: (cause: unknown) => void }[] = [];
  const taken: string[] = [];
  const frames = new FrameReads<string>({
    get: () => new Promise<string>((resolve) => reads.push(resolve)),
    answer: (value) => taken.push(value),
    failed: () => assert.fail('no read fails here'),
  });
  const call = () => new Promise<string>((resolve, reject) => asked.push({ resolve, reject }));
  const actions = updateActions(frames, {
    updateController: call,
    startRollout: call,
    resumeRollout: call,
    cancelRollout: call,
  });
  /** A frame from the stream, taken as the store's adopt takes one. */
  const frame = (value: string) => {
    frames.took();
    taken.push(value);
  };
  return { reads, asked, taken, frames, actions, frame };
}

// The controller starts a pass as it answers the Update button, and that pass's
// frame (the attempt open, say) can land before the answer does. Taken last, the
// answer would put the older picture back until something else moved, so the
// page reads the document again and ends on the newest.
test('an Update answer overtaken by a frame is followed by a read of its own', async () => {
  const { reads, asked, taken, actions, frame } = harness();
  const pressed = actions.updateController('v1.3.5');
  frame('the frame of the pass the press started');
  asked[0].resolve('the answer, older than the frame');
  await pressed;
  assert.equal(reads.length, 1, 'the document is read again');
  reads[0]('the document as it is now');
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(taken.at(-1), 'the document as it is now');
});

// With no frame while the call was out, the answer is the newest thing there
// is, and a read on top of it would be a request for nothing.
test('an Update answer with no frame meanwhile is taken and nothing is read again', async () => {
  const { reads, asked, taken, actions } = harness();
  const pressed = actions.updateController('v1.3.5');
  asked[0].resolve('the answer');
  await pressed;
  assert.deepEqual(taken, ['the answer']);
  assert.equal(reads.length, 0);
});

// A read that began before the press is older than the frame that overtook the
// answer; joining it would end on an answer that is dropped.
test('the read after an overtaken Update answer is not one already in flight', async () => {
  const { reads, asked, taken, frames, actions, frame } = harness();
  const earlier = frames.refresh();
  const pressed = actions.updateController('v1.3.5');
  frame('a frame');
  asked[0].resolve('the answer');
  await pressed;
  assert.equal(reads.length, 2, 'the read after the answer is a request of its own');
  reads[0]('older than the frame');
  await earlier;
  reads[1]('the newest');
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(taken, ['a frame', 'the answer', 'the newest']);
});

// A refusal is the controller's sentence for the dialog. It is not a status,
// so nothing is taken and nothing is read.
test('a refused Update takes nothing, reads nothing and throws the refusal', async () => {
  const { reads, asked, taken, actions } = harness();
  const pressed = actions.updateController('v1.3.5');
  asked[0].reject(new Error('Updating is off.'));
  await assert.rejects(pressed, /Updating is off/);
  assert.deepEqual(taken, []);
  assert.equal(reads.length, 0);
});

// The rollout buttons were the first to meet the overlap; they keep the order.
test('the rollout buttons follow an overtaken answer with a read of their own', async () => {
  for (const press of ['startRollout', 'resumeRollout', 'cancelRollout'] as const) {
    const { reads, asked, actions, frame } = harness();
    const pressed = actions[press]();
    frame('a frame');
    asked[0].resolve('the answer');
    await pressed;
    assert.equal(reads.length, 1, press);
  }
});
