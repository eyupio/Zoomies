import { test } from 'node:test';
import assert from 'node:assert/strict';
import { thinkingBeat, THINKING_MOTIONS, THINKING_QUOTES } from '../src/lib/assistant/personality';

test('every starting point visits all quotes without repetition and all six routines', () => {
  for (let offset = 0; offset < THINKING_QUOTES.length; offset += 2) {
    const beats = Array.from({ length: THINKING_QUOTES.length }, (_, step) =>
      thinkingBeat(step, offset),
    );
    assert.equal(new Set(beats.map((beat) => beat.quote)).size, THINKING_QUOTES.length);
    assert.deepEqual(new Set(beats.map((beat) => beat.motion)), new Set(THINKING_MOTIONS));
    assert.deepEqual(thinkingBeat(THINKING_QUOTES.length, offset), thinkingBeat(0, offset));
    for (let step = 0; step < beats.length; step += 2) {
      assert.equal(beats[step].motion, beats[step + 1].motion);
      assert.notEqual(beats[step].quote, beats[step + 1].quote);
    }
  }
});
